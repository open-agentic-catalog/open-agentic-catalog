//go:build integration

// Maintenance qualification uses exact API-role SQL and retained evidence, not a search oracle.
package database

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresValidationMaintenanceBindingSelectionLimit(t *testing.T) {
	f := newMCPToolFixture(t)
	r, err := f.service.CreateResource(f.author(t), catalog.CreateResourceInput{NamespaceID: f.namespace, Kind: catalog.KindMCPServer, CanonicalIdentifier: "urn:air:catalog.example.com:maintenance:binding-limit"})
	if err != nil {
		t.Fatal(err)
	}
	_, revision, _, err := f.service.CreateVersion(f.author(t), r.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: f.representations(t, "1.0.0", "", false)})
	if err != nil {
		t.Fatal(err)
	}
	// Only the disposable owner fixture injects an oversized retained set.
	// Runtime readers must transfer at most 17 metadata rows, then refuse it.
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("{}")))
	_, err = f.db.operator.Exec(t.Context(), `INSERT INTO representations(id,tenant_id,version_revision_id,role,media_type,profile_id,inline_document,content_digest,size_bytes,validation_state)
 SELECT gen_random_uuid(),$1,$2,'metadata','application/json','maintenance-overflow-'||n::text,'{}'::jsonb,$3,2,'valid' FROM generate_series(1,20) n`, housekeepingTenantA, revision.ID, digest)
	if err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := f.db.operator.QueryRow(t.Context(), `SELECT count(*) FROM representations WHERE tenant_id=$1 AND version_revision_id=$2`, housekeepingTenantA, revision.ID).Scan(&stored); err != nil || stored != 21 {
		t.Fatal("oversized retained control", stored, err)
	}
	count, err := withTenantRead(t.Context(), f.repo.pool, housekeepingTenantA, func(tx pgx.Tx) (int, error) {
		rows, err := tx.Query(t.Context(), validationMaintenanceBindingsSQL, housekeepingTenantA, []string{revision.ID})
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
		}
		return n, rows.Err()
	})
	if err != nil || count != 17 {
		t.Fatal("metadata selection did not enforce its SQL lookahead limit", count, err)
	}
	_, err = withTenantRead(t.Context(), f.repo.pool, housekeepingTenantA, func(tx pgx.Tx) ([]catalog.ExternalRepresentationBinding, error) {
		return readMaintenanceBindings(t.Context(), tx, housekeepingTenantA, revision.ID)
	})
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatal("oversized retained bindings were accepted", err)
	}
}

func TestPostgresValidationMaintenanceBoundariesCurrentScopeAndPermissions(t *testing.T) {
	f := newMCPToolFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond)
	c := clock.NewFake(base)
	observed := base.Add(24 * time.Hour)
	key := sha256.Sum256([]byte(t.Name()))
	external := &postgresExternalFetcher{at: base, body: f.representations(t, "1.0.0", "", false)[0].Data, policyDigest: "sha256:" + strings.Repeat("a", 64)}
	service, err := catalog.NewService(catalog.Options{Repository: f.repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: external, CursorKey: key[:], Policy: "maintenance-test"})
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	hidden, err := service.CreateNamespace(f.author(t), catalog.CreateNamespaceInput{Slug: "maintenance-hidden", DisplayName: "Hidden maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	type subject struct {
		r   catalog.Resource
		v   catalog.ResourceVersion
		rev catalog.VersionRevision
		e   catalog.EvidenceSet
	}
	create := func(n int, namespace string, expiry time.Time) subject {
		t.Helper()
		c.Advance(2 * time.Millisecond)
		r, err := service.CreateResource(f.author(t), catalog.CreateResourceInput{NamespaceID: namespace, Kind: catalog.KindMCPServer, CanonicalIdentifier: fmt.Sprintf("urn:air:catalog.example.com:maintenance:mcp-%d", n)})
		if err != nil {
			t.Fatal(err)
		}
		inputs := f.representations(t, "1.0.0", "", false)
		if !expiry.IsZero() {
			external.at = expiry.Add(-catalog.ExternalRepresentationFreshness)
			inputs = []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaMCPServerCard, URL: fmt.Sprintf("https://metadata.example.com/mcp-%d.json", n)}}
		}
		v, rev, e, err := service.CreateVersion(f.author(t), r.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: inputs})
		if err != nil || e.State != catalog.EvidenceComplete {
			t.Fatal(e, err)
		}
		return subject{r, v, rev, e}
	}
	create(0, hidden.ID, observed)
	subjects := make([]subject, 0, 6)
	for n, delta := range []time.Duration{-time.Microsecond, 0, time.Microsecond, 24 * time.Hour, 24*time.Hour + time.Microsecond} {
		subjects = append(subjects, create(n+1, f.namespace, observed.Add(delta)))
	}
	immutable := create(6, f.namespace, time.Time{})
	old := create(7, f.namespace, observed)
	_, current, _, err := service.CreateVersionRevision(f.author(t), old.v.ID, f.representations(t, "1.0.0", "", false), old.v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID == old.rev.ID {
		t.Fatal("history control did not create another revision")
	}
	// An older unsealed revision remains current through its Listing candidate
	// after the Version candidate advances; unrelated older history stays omitted.
	candidateOnly := create(8, f.namespace, observed)
	candidateListing, err := service.CreateListing(f.author(t), catalog.CreateListingInput{NamespaceID: f.namespace, ResourceID: candidateOnly.r.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.CreateListingRevision(f.author(t), candidateListing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: candidateOnly.rev.ID, PublisherID: f.publisher, DisplayName: "Older current Listing candidate", Description: "Retained candidate evidence", Visibility: catalog.VisibilityTenant}, candidateListing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = service.CreateVersionRevision(f.author(t), candidateOnly.v.ID, f.representations(t, "1.0.0", "", false), candidateOnly.v.Revision); err != nil {
		t.Fatal(err)
	}
	subjects = append(subjects, candidateOnly)
	// One Listing per Resource is the implemented boundary. Its candidate and
	// release plus the Version candidate/sealed pointers reach this revision once.
	listing, err := service.CreateListing(f.author(t), catalog.CreateListingInput{NamespaceID: f.namespace, ResourceID: subjects[0].r.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.publish(t, listing, subjects[0].rev, "Current maintenance reference")
	store, err := NewPostgresIdentityStore(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: c, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := authority.GrantRole(f.author(t), identity.GrantRoleInput{Subject: "maintenance-validator", ActorType: identity.ActorHuman, Role: identity.RoleContributor, NamespaceID: f.namespace, Reason: "Qualify scoped maintenance"}, "maintenance-grant")
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "maintenance-validator"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), a)
	q := catalog.ValidationMaintenanceRequest{State: "attention", ObservedAt: observed, HorizonHours: 24, Limit: 1}
	requests := external.requests
	var runCount int
	if err := f.db.operator.QueryRow(t.Context(), `SELECT count(*) FROM version_validation_runs WHERE tenant_id=$1`, housekeepingTenantA).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	want := []string{subjects[0].rev.ID, subjects[1].rev.ID, subjects[2].rev.ID, subjects[3].rev.ID, subjects[5].rev.ID}
	sort.Strings(want)
	var got []string
	for {
		rows, err := service.ListValidationMaintenance(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		if len(rows) > 2 {
			t.Fatal("selection exceeded lookahead", rows)
		}
		got = append(got, rows[0].RevisionID)
		q.AfterID = rows[0].RevisionID
		if len(got) > len(want) {
			t.Fatal("duplicate or non-current rows", got)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("current scope, permissions, equality or deduplication", got, want)
	}
	for _, tc := range []struct {
		state   string
		indices []int
	}{{"expired", []int{0, 1, 5}}, {"expiring", []int{2, 3}}, {"blocked", nil}, {"stale", nil}} {
		q.State = tc.state
		q.AfterID = ""
		q.Limit = 100
		rows, err := service.ListValidationMaintenance(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{}
		for _, i := range tc.indices {
			want = append(want, subjects[i].rev.ID)
		}
		sort.Strings(want)
		got := make([]string, 0, len(rows))
		for _, r := range rows {
			got = append(got, r.RevisionID)
			if r.EvidenceID == "" || r.ExpiresAt.IsZero() {
				t.Fatal(r)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal(tc.state, got, want)
		}
	}
	if external.requests != requests {
		t.Fatal("maintenance fetched external content")
	}
	var runsAfter int
	if err := f.db.operator.QueryRow(t.Context(), `SELECT count(*) FROM version_validation_runs WHERE tenant_id=$1`, housekeepingTenantA).Scan(&runsAfter); err != nil || runsAfter != runCount {
		t.Fatal("maintenance appended validation runs", runsAfter, runCount, err)
	}
	for _, tc := range []struct {
		hours   int
		indices []int
	}{{1, []int{2}}, {168, []int{2, 3, 4}}} {
		q.State = "expiring"
		q.HorizonHours = tc.hours
		rows, err := service.ListValidationMaintenance(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{}
		for _, i := range tc.indices {
			want = append(want, subjects[i].rev.ID)
		}
		sort.Strings(want)
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, row.RevisionID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("horizon selection", tc.hours, got, want)
		}
	}
	q.HorizonHours = 24
	external.policyDigest = "sha256:" + strings.Repeat("b", 64)
	q.State = "stale"
	policyRows, err := service.ListValidationMaintenance(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	foundBeyondHorizon := false
	for _, row := range policyRows {
		if row.RevisionID == immutable.rev.ID {
			t.Fatal("outbound policy expired immutable evidence")
		}
		if row.RevisionID == subjects[4].rev.ID {
			foundBeyondHorizon = true
		}
	}
	if !foundBeyondHorizon {
		t.Fatal("outbound policy drift was not selected before the limit")
	}
	external.policyDigest = "sha256:" + strings.Repeat("a", 64)
	// A changed deployment policy selects immutable evidence too, without time expiry.
	changed, err := catalog.NewService(catalog.Options{Repository: f.repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: external, CursorKey: key[:], Policy: "changed-policy"})
	if err != nil {
		t.Fatal(err)
	}
	q.State = "stale"
	rows, err := changed.ListValidationMaintenance(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	seenImmutable := false
	for _, r := range rows {
		if r.RevisionID == immutable.rev.ID {
			seenImmutable = true
			if !r.ExpiresAt.IsZero() || r.Applicable {
				t.Fatal(r)
			}
		}
		if r.RevisionID == old.rev.ID {
			t.Fatal("historical revision entered maintenance")
		}
	}
	if !seenImmutable {
		t.Fatal("policy-stale deterministic evidence omitted")
	}
	// A later blocked run wins even when its creation timestamp moves backward.
	c.Advance(-time.Minute)
	external.err = errors.New("owned retrieval failure")
	_, blocked, _, err := service.RevalidateVersionRevision(f.author(t), subjects[1].rev.ID)
	if err != nil || blocked.State != catalog.EvidenceBlocked {
		t.Fatal(blocked, err)
	}
	q.State = "blocked"
	rows, err = service.ListValidationMaintenance(ctx, q)
	if err != nil || len(rows) != 1 || rows[0].EvidenceID != blocked.ID || !rows[0].ValidatedAt.Before(subjects[1].e.CreatedAt) {
		t.Fatal("latest run selected by time instead of number", rows, err)
	}
	c.Advance(time.Minute)
	external.err = nil
	// Reader authority alone cannot reveal evidence maintenance metadata.
	_, err = authority.GrantRole(f.author(t), identity.GrantRoleInput{Subject: "maintenance-reader", ActorType: identity.ActorHuman, Role: identity.RoleReader, NamespaceID: f.namespace, Reason: "Qualify validate admission"}, "maintenance-reader-grant")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "maintenance-reader"})
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := service.ListValidationMaintenance(identity.WithActor(t.Context(), reader), q); err != nil || len(rows) != 0 {
		t.Fatal("reader disclosed private maintenance", rows, err)
	}
	// A valid content+validate role without resource.read is independently denied.
	role, err := authority.SaveRoleDefinition(f.author(t), "", 0, identity.RoleDefinitionInput{Name: "Validate without metadata", Permissions: []identity.Permission{"resource.read_content", "resource.validate"}, Reason: "Independent metadata admission control"}, false, "maintenance-role")
	if err != nil {
		t.Fatal(err)
	}
	_, err = authority.GrantAuthorizationAssignment(f.author(t), identity.AccessAssignmentInput{MembershipID: reader.MembershipID, RoleDefinitionID: role.ID, NamespaceID: hidden.ID, Reason: "No metadata grant in this namespace"}, "maintenance-no-metadata")
	if err != nil {
		t.Fatal(err)
	}
	// Content is already a required role companion of validation. Do not
	// manufacture an impossible granted role to test the SQL intersection.
	_, err = authority.SaveRoleDefinition(f.author(t), "", 0, identity.RoleDefinitionInput{Name: "Validate without content", Permissions: []identity.Permission{"resource.read", "resource.validate"}, Reason: "Qualify required content companion"}, false, "maintenance-no-content-role")
	if identity.ErrorCodeOf(err) != identity.CodeInvalidArgument {
		t.Fatal("role admission accepted validation without content", err)
	}
	q.State = "attention"
	if rows, err := service.ListValidationMaintenance(identity.WithActor(t.Context(), reader), q); err != nil || len(rows) != 0 {
		t.Fatal("permissions from different namespaces combined", rows, err)
	}
	foreign := semanticDisclosureActor(t, f.db, housekeepingTenantB)
	if rows, err := service.ListValidationMaintenance(foreign, q); err != nil || len(rows) != 0 {
		t.Fatal("foreign tenant disclosed maintenance", rows, err)
	}
	// A policy change rejects a previously admitted collection context.
	staleContext, _, err := identity.WithCollectionContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GrantRole(f.author(t), identity.GrantRoleInput{Subject: "policy-change", ActorType: identity.ActorHuman, Role: identity.RoleReader, NamespaceID: f.namespace, Reason: "Advance current policy"}, "maintenance-policy"); err != nil {
		t.Fatal(err)
	}
	q.Policy = service.ValidationMaintenancePolicy()
	q.Limit = 1
	if _, err := f.repo.ListValidationMaintenance(staleContext, housekeepingTenantA, q); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("stale authorization admitted", err)
	}
	// The same authenticated actor must lose its rows after its actual grant is
	// revoked. An unrelated policy change alone cannot establish revocation.
	if rows, err := service.ListValidationMaintenance(ctx, q); err != nil || len(rows) == 0 {
		t.Fatal("current grant control did not disclose maintenance", rows, err)
	}
	if _, err := authority.RevokeRole(f.author(t), grant.ID, "Remove maintenance validation authority", "maintenance-revoke", grant.Revision); err != nil {
		t.Fatal(err)
	}
	if rows, err := service.ListValidationMaintenance(ctx, q); err != nil || len(rows) != 0 {
		t.Fatal("revoked grant retained maintenance disclosure", rows, err)
	}
	// Corruption is injected only into this disposable fixture, with the
	// immutable-history guard restored before reading through the exact API role.
	tx, err := f.db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `ALTER TABLE evidence_sets DISABLE TRIGGER evidence_sets_immutable_history`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE evidence_sets SET digest=$3 WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, blocked.ID, "sha256:"+strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `ALTER TABLE evidence_sets ENABLE TRIGGER evidence_sets_immutable_history`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	q.State = "blocked"
	q.Limit = 100
	if rows, err := service.ListValidationMaintenance(f.author(t), q); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || len(rows) != 0 {
		t.Fatal("corrupt evidence became a maintenance row", rows, err)
	}
}
