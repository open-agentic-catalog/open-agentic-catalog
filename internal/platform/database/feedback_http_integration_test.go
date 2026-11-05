//go:build integration

// Feedback replica tests use two HTTP servers and the suite-owned PostgreSQL roles.
package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/httpserver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresFeedbackTwoAPIReplayConcurrencyAndOffboarding(t *testing.T) {
	db := newHousekeepingDatabase(t)
	for _, user := range []struct {
		subject string
		role    identity.Role
	}{{"feedback-admin", identity.RoleTenantAdmin}, {"feedback-reader", identity.RoleReader}} {
		_, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: authn.DevelopmentIssuer, TenancyMode: "dedicated", Subject: user.subject, ActorType: identity.ActorHuman, Roles: []identity.Role{user.role}, Operator: "feedback-test", Reason: "Synthetic replica test", AcknowledgeOutbox: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	key := sha256.Sum256([]byte(t.Name()))
	newServer := func(c catalog.Clock) *httptest.Server {
		t.Helper()
		repo, err := NewPostgresRepository(db.api)
		if err != nil {
			t.Fatal(err)
		}
		resources, err := catalog.NewService(catalog.Options{Repository: repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "feedback-test"})
		if err != nil {
			t.Fatal(err)
		}
		access, err := NewPostgresIdentityStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		identities, err := identity.NewService(identity.ServiceOptions{Store: access, Clock: c, IDs: id.Generator{}, CursorKey: key[:]})
		if err != nil {
			t.Fatal(err)
		}
		authenticator, err := authn.New(t.Context(), config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
			"admin": {Subject: "feedback-admin", TenantID: housekeepingTenantA}, "reader": {Subject: "feedback-reader", TenantID: housekeepingTenantA},
		}}, access)
		if err != nil {
			t.Fatal(err)
		}
		store, err := NewPostgresFeedbackStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		service, err := feedback.NewService(store, repo, c, id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		commands, err := NewPostgresIdempotencyStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		outbound, err := fetcher.NewRouter(fetcher.RoutingPolicy{Public: fetcher.PublicRoute{Mode: fetcher.PublicDisabled}, ID: "feedback-test", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64), Timeout: time.Second, MaxResponseBytes: 1024}, nil)
		if err != nil {
			t.Fatal(err)
		}
		peers, err := federation.NewManager(federation.NewMemoryStore(), outbound, c, id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		work, err := jobs.NewService(jobs.NewMemoryStore(), c, id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := httpserver.New(httpserver.Dependencies{Catalog: resources, Identity: identities, Federation: peers, Jobs: work, Feedback: service, Authenticator: authenticator, AtomicIdempotency: commands, Logger: slog.New(slog.DiscardHandler), MaxBodyBytes: 1 << 20, RequestTimeout: 10 * time.Second, CursorKey: key[:]})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler.Handler())
		t.Cleanup(server.Close)
		return server
	}
	// The second API has an earlier clock; persisted transition time must not regress.
	one, two := newServer(clock.Real{}), newServer(clock.NewFake(time.Now().Add(-time.Second)))
	type response struct {
		status int
		header http.Header
		body   []byte
	}
	request := func(server *httptest.Server, token, method, path, command, etag string, body any) response {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if command != "" {
			req.Header.Set("Idempotency-Key", command)
		}
		if etag != "" {
			req.Header.Set("If-Match", etag)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		return response{res.StatusCode, res.Header, data}
	}
	check := func(got response, status int) {
		t.Helper()
		if got.status != status {
			t.Fatalf("HTTP status=%d want=%d body=%s", got.status, status, got.body)
		}
	}
	identifier := func(got response) string {
		t.Helper()
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(got.body, &item); err != nil || item.ID == "" {
			t.Fatalf("missing response identity: %s", got.body)
		}
		return item.ID
	}
	ns := request(one, "admin", "POST", "/api/v1/namespaces", "namespace", "", map[string]any{"slug": "feedback", "displayName": "Feedback"})
	check(ns, 201)
	resource := request(one, "admin", "POST", "/api/v1/resources", "resource", "", map[string]any{"namespaceId": identifier(ns), "canonicalIdentifier": "urn:air:catalog.example.com:feedback:replicas", "kind": "agent"})
	check(resource, 201)
	payload := map[string]any{"resourceId": identifier(resource), "category": "problem", "comment": "Synthetic private replica feedback"}
	// Exercise every sequential transition and its replay through separate APIs.
	// In particular, withdrawal must clear an edited, previously addressed record.
	lifecycle := request(one, "reader", "POST", "/api/v1/feedback", "lifecycle-create", "", payload)
	check(lifecycle, 201)
	lifecyclePath := "/api/v1/feedback/" + identifier(lifecycle)
	for _, step := range []struct {
		actor, method, suffix string
		body                  map[string]any
	}{
		{"admin", "POST", ":address", map[string]any{"response": "Synthetic private response"}},
		{"admin", "POST", ":reopen", map[string]any{}},
		{"reader", "PATCH", "", map[string]any{"category": "suggestion", "comment": "Revised feedback"}},
		{"reader", "POST", ":withdraw", map[string]any{}},
	} {
		etag := lifecycle.header.Get("ETag")
		command := "lifecycle-" + step.method + step.suffix
		lifecycle = request(two, step.actor, step.method, lifecyclePath+step.suffix, command, etag, step.body)
		check(lifecycle, 200)
		replayed := request(one, step.actor, step.method, lifecyclePath+step.suffix, command, etag, step.body)
		check(replayed, 200)
		if !bytes.Equal(lifecycle.body, replayed.body) || replayed.header.Get("Idempotency-Replayed") != "true" {
			t.Fatal("sequential lifecycle replay mismatch")
		}
	}
	withdrawn := request(two, "reader", "GET", lifecyclePath, "", "", nil)
	check(withdrawn, 200)
	var withdrawnRecord feedback.Record
	if err := json.Unmarshal(withdrawn.body, &withdrawnRecord); err != nil || withdrawnRecord.State != "withdrawn" || withdrawnRecord.Comment != "" || withdrawnRecord.Response != "" {
		t.Fatalf("sequential withdrawal did not clear current text: %v", err)
	}
	created := request(one, "reader", "POST", "/api/v1/feedback", "create", "", payload)
	check(created, 201)
	path := "/api/v1/feedback/" + identifier(created)
	replay := request(two, "reader", "POST", "/api/v1/feedback", "create", "", payload)
	check(replay, 201)
	if !bytes.Equal(created.body, replay.body) || replay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("cross-API durable replay mismatch")
	}
	results := make(chan response, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		results <- request(one, "reader", "PATCH", path, "edit", created.header.Get("ETag"), map[string]any{"category": "other", "comment": "Concurrent private edit"})
	})
	wg.Go(func() {
		results <- request(two, "admin", "POST", path+":address", "address", created.header.Get("ETag"), map[string]any{"response": "Concurrent private response"})
	})
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for result := range results {
		counts[result.status]++
	}
	if counts[200] != 1 || counts[412] != 1 {
		t.Fatalf("concurrent command outcomes: %v", counts)
	}
	current := request(two, "reader", "GET", path, "", "", nil)
	check(current, 200)
	// Terminal commands also compete on one revision; only one may commit.
	terminal := request(one, "reader", "POST", "/api/v1/feedback", "terminal-create", "", payload)
	check(terminal, 201)
	terminalPath := "/api/v1/feedback/" + identifier(terminal)
	terminalResults := make(chan response, 2)
	wg.Go(func() {
		terminalResults <- request(one, "reader", "POST", terminalPath+":withdraw", "withdraw-race", terminal.header.Get("ETag"), map[string]any{})
	})
	wg.Go(func() {
		terminalResults <- request(two, "admin", "POST", terminalPath+":remove", "remove-race", terminal.header.Get("ETag"), map[string]any{})
	})
	wg.Wait()
	close(terminalResults)
	counts = map[int]int{}
	for result := range terminalResults {
		counts[result.status]++
	}
	if counts[200] != 1 || counts[412] != 1 {
		t.Fatalf("terminal command outcomes: %v", counts)
	}
	cleared := request(two, "reader", "GET", terminalPath, "", "", nil)
	check(cleared, 200)
	if strings.Contains(string(cleared.body), "private") {
		t.Fatal("competing terminal commands retained text")
	}
	me := request(one, "reader", "GET", "/api/v1/me", "", "", nil)
	check(me, 200)
	var access struct {
		MembershipID string `json:"membershipId"`
	}
	if err := json.Unmarshal(me.body, &access); err != nil {
		t.Fatal(err)
	}
	memberPath := "/api/v1/memberships/" + access.MembershipID
	member := request(one, "admin", "GET", memberPath, "", "", nil)
	check(member, 200)
	offboarded := request(one, "admin", "POST", memberPath+":offboard", "offboard", member.header.Get("ETag"), map[string]any{"reason": "Synthetic feedback offboarding"})
	check(offboarded, 200)
	check(request(two, "reader", "GET", path, "", "", nil), 401)
	check(request(two, "reader", "POST", "/api/v1/feedback", "create", "", payload), 401)
	removed := request(two, "admin", "POST", path+":remove", "remove", current.header.Get("ETag"), map[string]any{})
	check(removed, 200)
	// Recreate an API to prove content and acknowledgements survive no in-process state.
	three := newServer(clock.Real{})
	retained := request(three, "admin", "GET", path, "", "", nil)
	check(retained, 200)
	if strings.Contains(string(retained.body), "private") {
		t.Fatal("removed private text survived in current reads")
	}
	replayed := request(three, "admin", "POST", path+":remove", "remove", current.header.Get("ETag"), map[string]any{})
	check(replayed, 200)
	if !bytes.Equal(replayed.body, removed.body) || replayed.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("restart replay mismatch")
	}
	var leaked bool
	if err := db.operator.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM audit_events WHERE details::text LIKE '%private%')`).Scan(&leaked); err != nil || leaked {
		t.Fatalf("private audit text: %v %v", leaked, err)
	}
}
