//go:build integration

package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/httpserver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

type ratingTestAuthentication struct {
	store      *PostgresIdentityStore
	principals map[string]identity.Principal
}

func (a ratingTestAuthentication) Authenticate(ctx context.Context, token string) (identity.Actor, error) {
	p, ok := a.principals[token]
	if !ok {
		return identity.Actor{}, identity.E(identity.CodeUnauthenticated, "unknown synthetic token")
	}
	return a.store.ResolvePrincipal(ctx, p)
}

type postgresRatingAuthority func(context.Context, ratings.Admission) (ratings.Admission, error)

func (a postgresRatingAuthority) Admit(ctx context.Context, input ratings.Admission) (ratings.Admission, error) {
	return a(ctx, input)
}

func TestPostgresRatingsTwoAPIsLifecycleAndIsolation(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t)
	seed(0)
	author := semanticDisclosureActor(t, db, housekeepingTenantA)
	page, err := repo.SearchCatalog(author, housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("seed %v %v", page, err)
	}
	record, err := repo.GetCatalogRecord(author, housekeepingTenantA, page.Items[0].Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	principals := map[string]identity.Principal{}
	for _, name := range []string{"reader", "second", "third", "fourth", "fifth", "sixth", "race", "machine", "admin", "other"} {
		tenant := housekeepingTenantA
		roles := []identity.Role{identity.RoleReader}
		if name == "admin" {
			roles = []identity.Role{identity.RoleTenantAdmin}
		}
		if name == "other" {
			tenant = housekeepingTenantB
		}
		typ := identity.ActorHuman
		if name == "machine" {
			typ = identity.ActorService
		}
		ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: tenant, Subject: "rating-" + name, Roles: roles, ActorType: typ})
		a, _ := identity.FromContext(ctx)
		principals[name] = identity.Principal{TenantID: tenant, Issuer: a.Issuer, Subject: a.Subject}
	}
	key := sha256.Sum256([]byte(t.Name()))
	var publishers []*publication.Manager
	makeAPI := func(c catalog.Clock) *httptest.Server {
		t.Helper()
		resources, err := catalog.NewService(catalog.Options{Repository: repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), Policy: "search-cost-test", CursorKey: key[:]})
		if err != nil {
			t.Fatal(err)
		}
		identities, err := identity.NewService(identity.ServiceOptions{Store: access, Clock: c, IDs: id.Generator{}, CursorKey: key[:]})
		if err != nil {
			t.Fatal(err)
		}
		work, err := jobs.NewService(jobs.NewMemoryStore(), c, id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		peers, err := federation.NewManager(federation.NewMemoryStore(), &postgresFederationFetcher{policyDigest: "sha256:" + strings.Repeat("a", 64)}, c, id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		store, err := NewPostgresRatingsStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		publicStore, err := NewPostgresPublicationStore(repo, "catalog.example.com")
		if err != nil {
			t.Fatal(err)
		}
		publisher, err := publication.NewManager(publication.Options{Records: resources, Store: publicStore, Clock: c, IDs: id.Generator{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: key[:], Evidence: repo, Audience: publication.AudienceAuthenticated, Ratings: store, RatingsSharingEnabled: true, RatingsMinimumSharedCount: 5})
		if err != nil {
			t.Fatal(err)
		}
		publishers = append(publishers, publisher)
		commands, err := NewPostgresIdempotencyStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := httpserver.New(httpserver.Dependencies{Catalog: resources, Identity: identities, Federation: peers, Jobs: work, RatingsStore: store, DiscoveryHosts: []httpserver.DiscoveryHost{{Host: "catalog.example.com", TenantID: housekeepingTenantA, PublisherDomain: "catalog.example.com", Mode: httpserver.DiscoveryAuthenticated, Publication: publisher}}, Authenticator: ratingTestAuthentication{access, principals}, AtomicIdempotency: commands, CursorKey: key[:], Now: c.Now, MaxBodyBytes: 1 << 20, RequestTimeout: 10 * time.Second, Logger: slog.New(slog.DiscardHandler)})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler.Handler())
		t.Cleanup(server.Close)
		return server
	}
	one, two := makeAPI(clock.Real{}), makeAPI(clock.NewFake(time.Now().Add(-time.Hour)))
	type response struct {
		status int
		header http.Header
		body   []byte
	}
	request := func(server *httptest.Server, token, method, path, key, etag string, body any) response {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if etag != "" {
			r.Header.Set("If-Match", etag)
		}
		reply, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = reply.Body.Close() }()
		data, err := io.ReadAll(io.LimitReader(reply.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		return response{reply.StatusCode, reply.Header, data}
	}
	check := func(r response, status int) {
		t.Helper()
		if r.status != status {
			t.Fatalf("HTTP %d want %d: %s", r.status, status, r.body)
		}
	}
	target := ratings.Target{Origin: "local", ResourceID: record.Resource.ID, VersionRevisionID: record.VersionRevision.ID, Identifier: record.Resource.CanonicalIdentifier, Kind: record.Resource.Kind, VersionPresent: true, Version: record.Version.Version}
	input := map[string]any{"target": target, "releaseId": record.Release.ID, "score": 5, "consent": true}
	check(request(one, "machine", "POST", "/api/v1/ratings", "machine-vote", "", input), 403)
	for _, score := range []any{0, 6, 1.5, "five", nil} {
		invalid := map[string]any{"target": target, "releaseId": record.Release.ID, "score": score}
		check(request(one, "reader", "POST", "/api/v1/ratings", fmt.Sprintf("invalid-%v", score), "", invalid), 400)
	}
	created := request(one, "reader", "POST", "/api/v1/ratings", "create", "", input)
	check(created, 201)
	var receipt struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	if err = json.Unmarshal(created.body, &receipt); err != nil {
		t.Fatal(err)
	}
	replay := request(two, "reader", "POST", "/api/v1/ratings", "create", "", input)
	check(replay, 201)
	if replay.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(replay.body, created.body) {
		t.Fatal("durable replay mismatch")
	}
	path := "/api/v1/ratings/" + receipt.ID
	check(request(one, "second", "GET", path, "", "", nil), 404)
	check(request(one, "other", "GET", path, "", "", nil), 404)
	read := func() ratings.View {
		t.Helper()
		r := request(one, "reader", "POST", "/api/v1/ratings/read", "", "", map[string]any{"target": target, "releaseId": record.Release.ID})
		check(r, 200)
		var value ratings.View
		if err := json.Unmarshal(r.body, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if v := read(); v.Count != 1 || v.Average == nil || *v.Average != 5 {
		t.Fatalf("totals %+v", v)
	}
	input["score"] = 2
	edited := request(two, "reader", "POST", "/api/v1/ratings", "edit", created.header.Get("ETag"), input)
	check(edited, 200)
	current := request(one, "reader", "GET", path, "", "", nil)
	check(current, 200)
	var vote ratings.Record
	if err = json.Unmarshal(current.body, &vote); err != nil {
		t.Fatal(err)
	}
	if !vote.Consent || vote.Revision != 2 {
		t.Fatalf("edit %+v", vote)
	}
	// Independent human votes commit concurrently without lost rollup increments.
	var wg sync.WaitGroup
	results := make(chan response, 5)
	for _, token := range []string{"second", "third", "fourth", "fifth", "sixth"} {
		wg.Go(func() { results <- request(one, token, "POST", "/api/v1/ratings", "score-"+token, "", input) })
	}
	wg.Wait()
	close(results)
	for r := range results {
		check(r, 201)
	}
	if v := read(); v.Count != 6 || v.Average == nil || *v.Average != 2 {
		t.Fatalf("concurrent totals %+v", v)
	}
	removed := request(one, "admin", "POST", path+":remove", "remove", edited.header.Get("ETag"), map[string]any{"reason": "Confirmed abuse"})
	check(removed, 200)
	check(request(two, "reader", "POST", "/api/v1/ratings", "after-removal", removed.header.Get("ETag"), input), 409)
	// Old successful replay returns its minimal acknowledgement, never reactivates.
	check(request(two, "reader", "POST", "/api/v1/ratings", "create", "", map[string]any{"target": target, "releaseId": record.Release.ID, "score": 5, "consent": true}), 201)
	if v := read(); v.Count != 5 || v.Own == nil || v.Own.State != "removed" {
		t.Fatalf("replay resurrected opinion %+v", v)
	}
	permitted := request(one, "admin", "POST", path+":permit", "permit", removed.header.Get("ETag"), map[string]any{"reason": "Allow a fresh opinion"})
	check(permitted, 200)
	delete(input, "consent")
	fresh := request(two, "reader", "POST", "/api/v1/ratings", "fresh", permitted.header.Get("ETag"), input)
	check(fresh, 200)
	if v := read(); v.Own == nil || v.Own.Consent {
		t.Fatal("fresh submission inherited consent")
	}
	withdrawn := request(two, "reader", "POST", path+":withdraw", "withdraw", fresh.header.Get("ETag"), map[string]any{})
	check(withdrawn, 200)
	replay = request(one, "reader", "POST", path+":withdraw", "withdraw", fresh.header.Get("ETag"), map[string]any{})
	check(replay, 200)
	if replay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("withdraw replay missing")
	}
	check(request(one, "reader", "GET", "/api/v1/ratings?limit=1", "", "", nil), 200)
	check(request(one, "reader", "GET", "/api/v1/ratings/inbox", "", "", nil), 403)
	check(request(one, "admin", "GET", "/api/v1/ratings/inbox?state=active&limit=1", "", "", nil), 200)
	sharingPath := "/api/v1/ratings/sharing"
	check(request(one, "reader", "GET", sharingPath+"?host=catalog.example.com&audience=authenticated", "", "", nil), 403)
	check(request(one, "admin", "GET", sharingPath+"?host=catalog.example.com&audience=authenticated", "", "", nil), 200)
	check(request(one, "admin", "POST", sharingPath, "missing-sharing-choice", "", map[string]any{"host": "catalog.example.com", "audience": "authenticated", "reason": "Missing explicit choice"}), 400)
	sharingInput := map[string]any{"host": "catalog.example.com", "audience": "authenticated", "enabled": true, "reason": "Explicit consented-summary opt-in"}
	shared := request(one, "admin", "POST", sharingPath, "share", "", sharingInput)
	check(shared, 200)
	check(request(two, "admin", "POST", sharingPath, "share", "", sharingInput), 200)
	check(request(two, "admin", "POST", sharingPath, "stale-share", "", sharingInput), 412)
	adminActor, err := access.ResolvePrincipal(t.Context(), principals["admin"])
	if err != nil {
		t.Fatal(err)
	}
	adminContext := identity.WithActor(t.Context(), adminActor)
	candidate, err := publishers[0].BuildCandidate(adminContext, housekeepingTenantA)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.AICatalog.Entries) != 1 || candidate.AICatalog.Entries[0].Extensions[ratings.IRI] == nil {
		t.Fatalf("missing shared summary %+v", candidate)
	}
	activated, err := publishers[0].Activate(adminContext, housekeepingTenantA, candidate.ID, "Publish consented aggregate")
	if err != nil {
		t.Fatal(err)
	}
	sharingInput["enabled"] = false
	disabled := request(two, "admin", "POST", sharingPath, "disable-share", shared.header.Get("ETag"), sharingInput)
	check(disabled, 200)
	if _, err = publishers[0].Rollback(adminContext, housekeepingTenantA, activated.ID, "Must reject current opt-out"); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
		t.Fatal("opt-out did not block stale summary")
	}
	retained, err := publishers[0].Active(adminContext, housekeepingTenantA)
	if err != nil || retained.ID != activated.ID {
		t.Fatal("opt-out changed immutable active snapshot", err)
	}
	// Two concurrent first submissions by one verified account create one vote.
	racing := make(chan response, 2)
	for _, key := range []string{"race-one", "race-two"} {
		wg.Go(func() { racing <- request(one, "race", "POST", "/api/v1/ratings", key, "", input) })
	}
	wg.Wait()
	close(racing)
	var winner response
	for value := range racing {
		if value.status == 201 {
			if winner.status != 0 {
				t.Fatal("duplicate first vote")
			}
			winner = value
		} else {
			check(value, 412)
		}
	}
	check(winner, 201)
	var raceReceipt struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(winner.body, &raceReceipt); err != nil {
		t.Fatal(err)
	}
	check(request(one, "race", "POST", "/api/v1/ratings/"+raceReceipt.ID+":withdraw", "race-withdraw", winner.header.Get("ETag"), map[string]any{}), 200)
	// An audit failure rolls back the score, rollup, revision and command result.
	_, err = db.operator.Exec(t.Context(), `CREATE FUNCTION ratings_test_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic rating audit failure'; END $$; CREATE TRIGGER ratings_test_audit_failure BEFORE INSERT ON audit_events FOR EACH ROW WHEN (NEW.action='rating.submit') EXECUTE FUNCTION ratings_test_audit_failure()`)
	if err != nil {
		t.Fatal(err)
	}
	failed := request(one, "third", "POST", "/api/v1/ratings", "audit-failure", `"rev-1"`, map[string]any{"target": target, "releaseId": record.Release.ID, "score": 5})
	_, dropErr := db.operator.Exec(t.Context(), `DROP TRIGGER ratings_test_audit_failure ON audit_events; DROP FUNCTION ratings_test_audit_failure()`)
	if dropErr != nil {
		t.Fatal(dropErr)
	}
	check(failed, http.StatusServiceUnavailable)
	if v := read(); v.Count != 5 || v.Average == nil || *v.Average != 2 {
		t.Fatal("audit failure committed rating", v)
	}
	memberActor, err := access.ResolvePrincipal(t.Context(), principals["second"])
	if err != nil {
		t.Fatal(err)
	}
	identityService, err := identity.NewService(identity.ServiceOptions{Store: access, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	member, err := identityService.GetMembership(adminContext, memberActor.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = identityService.OffboardMembership(adminContext, member.ID, "Owned rating offboarding check", "offboard-rating", member.Revision); err != nil {
		t.Fatal(err)
	}
	denied := request(two, "second", "POST", "/api/v1/ratings", "score-second", "", map[string]any{"target": target, "releaseId": record.Release.ID, "score": 2, "consent": true})
	if denied.status != 403 && denied.status != 401 {
		t.Fatalf("offboarded replay admitted %d %s", denied.status, denied.body)
	}
	if v := read(); v.Count != 5 {
		t.Fatal("offboarding silently removed opinion", v)
	}
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	restored, more, err := scanRestoreRatings(t.Context(), tx, housekeepingTenantA, "", 100)
	_ = tx.Rollback(t.Context())
	if err != nil || more || len(restored) < 7 {
		t.Fatal("restore rating selection", restored, more, err)
	}
	for _, value := range restored {
		if value.finding != nil {
			t.Fatalf("valid rating restore finding %+v", value.finding)
		}
	}
	// SQL roles cannot read other tenants or edit trigger-owned totals/history.
	raw := t.Context()
	_, err = withTenantRead(raw, db.api, housekeepingTenantB, func(tx pgx.Tx) (string, error) {
		var id string
		err := tx.QueryRow(raw, `SELECT id FROM item_ratings WHERE id=$1`, receipt.ID).Scan(&id)
		return id, err
	})
	if err == nil {
		t.Fatal("RLS disclosed another tenant")
	}
	for _, statement := range []string{`UPDATE rating_targets SET vote_count=0`, `INSERT INTO item_rating_revisions(tenant_id) VALUES('` + housekeepingTenantA + `')`, `SELECT principal_key FROM item_ratings`} {
		pool := db.api
		if strings.HasPrefix(statement, "SELECT") {
			pool = db.worker
		}
		_, err = pool.Exec(raw, statement)
		requireInsufficientPrivilege(t, err)
	}
	var count, sum, history, audits int64
	if err = db.operator.QueryRow(raw, `SELECT vote_count,score_sum,(SELECT count(*) FROM item_rating_revisions WHERE tenant_id=$1),(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action LIKE 'rating.%' AND action<>'rating.sharing') FROM rating_targets WHERE tenant_id=$1 AND target_key=$2`, housekeepingTenantA, ratings.Key(target)).Scan(&count, &sum, &history, &audits); err != nil {
		t.Fatal(err)
	}
	if count != 5 || sum != 10 || history != audits {
		t.Fatalf("atomic totals/history/audit %d %d %d %d", count, sum, history, audits)
	}
}
