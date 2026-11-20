// Maintenance units use retained metadata and injected clocks, never servers or databases.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// maintenanceSeed retains independently chosen metadata and an immutable evidence digest.
func maintenanceSeed(t *testing.T, f guardrailFixture, n int, expires time.Time, blocked, stale bool) string {
	t.Helper()
	key := func(offset int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", 10000+n*10+offset) }
	r := Resource{ID: key(1), TenantID: guardrailTenantID, NamespaceID: collectionNamespace, Kind: KindAgent, CanonicalIdentifier: fmt.Sprintf("urn:air:example.com:agents:maintenance-%d", n)}
	v := ResourceVersion{ID: key(2), TenantID: r.TenantID, ResourceID: r.ID, Version: "1.0.0", CandidateRevisionID: key(3)}
	revision := VersionRevision{ID: key(3), TenantID: r.TenantID, ResourceVersionID: v.ID, RevisionNumber: 1, ContentSetDigest: digestBytes([]byte("retained"))}
	representation := Representation{ID: key(4), Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"x":1}`), ContentDigest: digestBytes([]byte("retained")), Size: 8}
	retrievals := []ExternalRetrieval{}
	created := f.clock.Now()
	if !expires.IsZero() {
		created = expires.Add(-ExternalRepresentationFreshness)
		representation.URL = "https://metadata.example.com/card.json"
		representation.Data = nil
		representation.ArtifactID = key(5)
		retrievals = append(retrievals, ExternalRetrieval{RepresentationID: representation.ID, RequestedURL: representation.URL, FinalURL: representation.URL, Status: 200, MediaType: representation.MediaType, ContentDigest: representation.ContentDigest, Size: representation.Size, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: f.fetcher.policyDigest, RetrievedAt: created, ExpiresAt: expires})
	}
	findings := []Finding{}
	if blocked {
		findings = append(findings, Finding{Code: "TEST_BLOCKED", Severity: SeverityError, Message: "Blocked fixture"})
	}
	policy := f.service.policy
	if stale {
		f.service.policy = "previous-policy"
	}
	e, err := f.service.newValidationEvidence(r.TenantID, revision.ContentSetDigest, findings, retrievals, created)
	f.service.policy = policy
	if err != nil {
		t.Fatal(err)
	}
	revision.EvidenceSetID = e.ID
	revision.Representations = []Representation{representation}
	f.repo.resources[r.ID] = r
	f.repo.versions[v.ID] = v
	f.repo.versionRevisions[revision.ID] = revision
	f.repo.evidenceSets[e.ID] = e
	f.repo.validationRuns[revision.ID] = []string{e.ID}
	return revision.ID
}

// maintenanceAdvanceCandidate leaves the old revision intact while pointing the
// same unsealed Version at new deterministic evidence with no time expiry.
func maintenanceAdvanceCandidate(t *testing.T, f guardrailFixture, id string) {
	t.Helper()
	old := f.repo.versionRevisions[id]
	revision := clone(old)
	var err error
	revision.ID, err = f.service.ids.New(f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	revision.RevisionNumber++
	rep := revision.Representations[0]
	rep.ID, err = f.service.ids.New(f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	rep.URL = ""
	rep.ArtifactID = ""
	rep.Data = json.RawMessage(`{"x":1}`)
	rep.Size = int64(len(rep.Data))
	rep.ContentDigest = digestBytes(rep.Data)
	revision.Representations = []Representation{rep}
	revision.ContentSetDigest, err = contentSetDigest(revision.Representations)
	if err != nil {
		t.Fatal(err)
	}
	e, err := f.service.newValidationEvidence(guardrailTenantID, revision.ContentSetDigest, nil, nil, f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	revision.EvidenceSetID = e.ID
	f.repo.versionRevisions[revision.ID] = revision
	f.repo.evidenceSets[e.ID] = e
	f.repo.validationRuns[revision.ID] = []string{e.ID}
	v := f.repo.versions[old.ResourceVersionID]
	v.CandidateRevisionID = revision.ID
	v.Revision++
	f.repo.versions[v.ID] = v
}

func TestValidationMaintenanceExpiryEqualityAndOverlappingConditions(t *testing.T) {
	f := newGuardrailFixture(t)
	at := f.clock.Now().Add(24 * time.Hour)
	for _, tc := range []struct {
		name                   string
		offset                 time.Duration
		expiry, blocked, stale bool
		want                   []string
		applicable             bool
	}{
		{"expired-before", -time.Microsecond, true, false, false, []string{"expired"}, false},
		{"expired-equality", 0, true, false, false, []string{"expired"}, false},
		{"expiring-after", time.Microsecond, true, false, false, []string{"expiring"}, true},
		{"horizon-equality", 24 * time.Hour, true, false, false, []string{"expiring"}, true},
		{"beyond-horizon", 24*time.Hour + time.Microsecond, true, false, false, []string{}, true},
		{"immutable", 0, false, false, false, []string{}, true},
		{"blocked", 0, false, true, false, []string{"blocked"}, false},
		{"policy-stale", 0, false, false, true, []string{"stale"}, false},
		{"overlap", 0, true, true, true, []string{"blocked", "stale", "expired"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expiry := time.Time{}
			if tc.expiry {
				expiry = at.Add(tc.offset)
			}
			id := maintenanceSeed(t, f, 1, expiry, tc.blocked, tc.stale)
			rev := f.repo.versionRevisions[id]
			e := f.repo.evidenceSets[rev.EvidenceSetID]
			q := ValidationMaintenanceRequest{ObservedAt: at, HorizonHours: 24, Policy: f.service.ValidationMaintenancePolicy()}
			item, err := ProjectValidationMaintenance(f.repo.resources[f.repo.versions[rev.ResourceVersionID].ResourceID], f.repo.versions[rev.ResourceVersionID], rev, e, q)
			if err != nil || !reflect.DeepEqual(item.Conditions, tc.want) || item.Applicable != tc.applicable || item.EvidenceID != e.ID {
				t.Fatal(item, err)
			}
		})
	}
}

func TestValidationMaintenanceCurrentPointersRunOrderAndBoundedPages(t *testing.T) {
	f := newGuardrailFixture(t)
	at := f.clock.Now().Add(24 * time.Hour)
	ids := []string{}
	for i := 0; i < 4; i++ {
		ids = append(ids, maintenanceSeed(t, f, i, at, false, false))
	}
	// A prior revision is no longer a Version candidate, but a Listing candidate
	// still references it. Current release references also preserve exact identity.
	maintenanceAdvanceCandidate(t, f, ids[1])
	v := f.repo.versions[f.repo.versionRevisions[ids[1]].ResourceVersionID]
	f.repo.listings["candidate"] = CatalogListing{ID: "candidate", TenantID: guardrailTenantID, ResourceID: v.ResourceID, CandidateRevisionID: "listing-candidate"}
	f.repo.listingRevisions["listing-candidate"] = ListingRevision{ID: "listing-candidate", TenantID: guardrailTenantID, VersionRevisionID: ids[1]}
	v = f.repo.versions[f.repo.versionRevisions[ids[2]].ResourceVersionID]
	v.SealedRevisionID = ids[2]
	f.repo.versions[v.ID] = v
	f.repo.listings["released"] = CatalogListing{ID: "released", TenantID: guardrailTenantID, ResourceID: v.ResourceID, CurrentReleaseID: "release"}
	f.repo.releases["release"] = ListingRelease{ID: "release", TenantID: guardrailTenantID, State: ReleaseDeprecated, VersionRevisionID: ids[2]}
	// One Listing plus sealed/candidate pointers must not duplicate its revision.
	v = f.repo.versions[f.repo.versionRevisions[ids[0]].ResourceVersionID]
	v.SealedRevisionID = ids[0]
	f.repo.versions[v.ID] = v
	f.repo.listings["duplicate"] = CatalogListing{ID: "duplicate", TenantID: guardrailTenantID, ResourceID: v.ResourceID, CandidateRevisionID: "duplicate-revision", CurrentReleaseID: "duplicate-release"}
	f.repo.listingRevisions["duplicate-revision"] = ListingRevision{ID: "duplicate-revision", TenantID: guardrailTenantID, VersionRevisionID: ids[0]}
	f.repo.releases["duplicate-release"] = ListingRelease{ID: "duplicate-release", TenantID: guardrailTenantID, State: ReleasePublished, VersionRevisionID: ids[0]}
	// Old history without a current pointer is omitted.
	id := maintenanceSeed(t, f, 9, at, false, false)
	maintenanceAdvanceCandidate(t, f, id)
	q := ValidationMaintenanceRequest{State: "expired", ObservedAt: at, HorizonHours: 24, Limit: 1}
	for i, want := range ids {
		rows, err := f.service.ListValidationMaintenance(f.adminContext(), q)
		expected := 2
		if i == len(ids)-1 {
			expected = 1
		}
		if err != nil || len(rows) != expected || rows[0].RevisionID != want {
			t.Fatal(i, rows, err)
		}
		q.AfterID = want
	}
	if rows, err := f.service.ListValidationMaintenance(f.adminContext(), q); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	// A later blocked run with an earlier creation time wins over old complete evidence.
	rev := f.repo.versionRevisions[ids[0]]
	e, err := f.service.newValidationEvidence(guardrailTenantID, rev.ContentSetDigest, []Finding{{Code: "BLOCKED", Severity: SeverityError, Message: "Later run"}}, nil, f.clock.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.repo.evidenceSets[e.ID] = e
	f.repo.validationRuns[rev.ID] = append(f.repo.validationRuns[rev.ID], e.ID)
	q.State = "blocked"
	q.AfterID = ""
	q.Limit = 100
	rows, err := f.service.ListValidationMaintenance(f.adminContext(), q)
	if err != nil || len(rows) != 1 || rows[0].EvidenceID != e.ID || !rows[0].ValidatedAt.Equal(e.CreatedAt) {
		t.Fatal(rows, err)
	}
	if len(f.repo.audit) != 0 || len(f.repo.outbox) != 0 || len(f.fetcher.requests) != 0 {
		t.Fatal("inventory fetched or mutated evidence")
	}
}

func TestValidationMaintenanceIntegrityPolicyAndInputRefusals(t *testing.T) {
	f := newGuardrailFixture(t)
	at := f.clock.Now().Add(24 * time.Hour)
	id := maintenanceSeed(t, f, 1, at, false, false)
	rev := f.repo.versionRevisions[id]
	original := f.repo.evidenceSets[rev.EvidenceSetID]
	q := ValidationMaintenanceRequest{State: "attention", ObservedAt: at, HorizonHours: 24, Limit: 20}
	f.fetcher.policyDigest = "sha256:" + strings.Repeat("b", 64)
	rows, err := f.service.ListValidationMaintenance(f.adminContext(), q)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].Conditions, []string{"stale", "expired"}) {
		t.Fatal(rows, err)
	}
	// A valid seal for another subject must fail the subject binding, rather
	// than letting a broken digest mask a missing subject check.
	foreign, err := f.service.newValidationEvidence(guardrailTenantID, digestBytes([]byte("other subject")), original.Findings, original.ExternalRetrievals, original.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidationMaintenanceConditions(foreign, foreign.SubjectDigest, q); err != nil {
		t.Fatal("valid foreign-subject evidence control", err)
	}
	if _, err := ValidationMaintenanceConditions(foreign, rev.ContentSetDigest, q); ErrorCodeOf(err) != CodeDependencyUnavailable {
		t.Fatal("valid evidence was accepted for the wrong revision subject", err)
	}
	for _, corrupt := range []func(*EvidenceSet){func(e *EvidenceSet) { e.Digest = "sha256:" + strings.Repeat("f", 64) }, func(e *EvidenceSet) { e.ValidationProfile = "unsupported-profile" }, func(e *EvidenceSet) { e.SubjectDigest = digestBytes([]byte("other")) }} {
		e := original
		corrupt(&e)
		f.repo.evidenceSets[e.ID] = e
		if _, err := f.service.ListValidationMaintenance(f.adminContext(), q); ErrorCodeOf(err) != CodeDependencyUnavailable {
			t.Fatal("corruption treated as ordinary maintenance", err)
		}
	}
	delete(f.repo.evidenceSets, original.ID)
	if _, err := f.service.ListValidationMaintenance(f.adminContext(), q); ErrorCodeOf(err) != CodeDependencyUnavailable {
		t.Fatal(err)
	}
	for _, change := range []func(*ValidationMaintenanceRequest){func(q *ValidationMaintenanceRequest) { q.State = "unknown" }, func(q *ValidationMaintenanceRequest) { q.Limit = 0 }, func(q *ValidationMaintenanceRequest) { q.Limit = 101 }, func(q *ValidationMaintenanceRequest) { q.HorizonHours = 0 }, func(q *ValidationMaintenanceRequest) { q.HorizonHours = 169 }, func(q *ValidationMaintenanceRequest) { q.ObservedAt = time.Time{} }, func(q *ValidationMaintenanceRequest) { q.AfterID = "forged" }} {
		bad := q
		change(&bad)
		if _, err := f.service.ListValidationMaintenance(f.adminContext(), bad); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal(bad, err)
		}
	}
}

type maintenancePermissionFault struct {
	identity.PermissionStore
	denied identity.Permission
}

func (s maintenancePermissionFault) CheckPermissions(ctx context.Context, a identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	d, err := s.PermissionStore.CheckPermissions(ctx, a, checks)
	if err != nil {
		return d, err
	}
	for i, c := range checks {
		if c.Permission == s.denied {
			d.Allowed[i] = false
		}
	}
	return d, nil
}

func TestValidationMaintenanceRequiresEveryScopedPermissionBeforeLimit(t *testing.T) {
	f := newGuardrailFixture(t)
	at := f.clock.Now().Add(24 * time.Hour)
	id := maintenanceSeed(t, f, 1, at, false, false)
	store, err := identity.NewMemoryStore([]identity.Seed{{TenantID: guardrailTenantID, Issuer: "maintenance", Subject: "validator", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: guardrailTenantID, Issuer: "maintenance", Subject: "validator"})
	if err != nil {
		t.Fatal(err)
	}
	q := ValidationMaintenanceRequest{State: "expired", ObservedAt: at, HorizonHours: 24, Limit: 1}
	if rows, err := f.service.ListValidationMaintenance(identity.WithActor(t.Context(), a), q); err != nil || len(rows) != 1 || rows[0].RevisionID != id {
		t.Fatal("valid permission control", rows, err)
	}
	for _, p := range []identity.Permission{"resource.read", "resource.read_content", "resource.validate"} {
		actor := identity.BindPermissionStore(a, maintenancePermissionFault{PermissionStore: store, denied: p})
		if rows, err := f.service.ListValidationMaintenance(identity.WithActor(t.Context(), actor), q); err != nil || len(rows) != 0 {
			t.Fatal(p, rows, err)
		}
	}
	other := f.admin
	other.TenantID = "00000000-0000-7000-8000-000000000088"
	if rows, err := f.service.ListValidationMaintenance(identity.WithActor(t.Context(), identity.BindPermissionStore(other, store)), q); err == nil && len(rows) != 0 {
		t.Fatal("cross-tenant maintenance", rows, err)
	}
}
