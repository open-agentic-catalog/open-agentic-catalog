// Service-free tests exercise private ownership, transitions, bounds and atomic failures.
package feedback

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

const testTenant = "00000000-0000-7000-8000-000000000001"
const testMember = "00000000-0000-7000-8000-000000000002"
const testRealm = "00000000-0000-7000-8000-000000000003"
const testResource = "00000000-0000-7000-8000-000000000004"
const testRevision = "00000000-0000-7000-8000-000000000005"
const testRelease = "00000000-0000-7000-8000-000000000006"

type contextFixture struct {
	fail     bool
	mismatch bool
}

// GetResource models tenant-qualified current resource reads.
func (f *contextFixture) GetResource(_ context.Context, tenant, resource string) (catalog.Resource, error) {
	if f.fail || tenant != testTenant || resource != testResource {
		return catalog.Resource{}, catalog.E(catalog.CodeNotFound, "resource was not found")
	}
	return catalog.Resource{ID: resource}, nil
}

// GetRelease returns an exact immutable revision association.
func (f *contextFixture) GetRelease(context.Context, string, string) (catalog.ListingRelease, error) {
	return catalog.ListingRelease{VersionRevisionID: testRevision}, nil
}

// GetVersionRevision supplies only retained identity, without external reads.
func (f *contextFixture) GetVersionRevision(context.Context, string, string) (catalog.VersionRevision, catalog.EvidenceSet, error) {
	return catalog.VersionRevision{ResourceVersionID: testRevision}, catalog.EvidenceSet{}, nil
}

// GetVersion permits an explicit cross-resource context denial test.
func (f *contextFixture) GetVersion(context.Context, string, string) (catalog.ResourceVersion, error) {
	resource := testResource
	if f.mismatch {
		resource = testRelease
	}
	return catalog.ResourceVersion{ResourceID: resource}, nil
}

type auditSink struct {
	records []audit.Record
	fail    bool
}

// AppendAuditEvent captures safe evidence or fails before state commit.
func (s *auditSink) AppendAuditEvent(_ context.Context, r audit.Record) error {
	if s.fail {
		return errors.New("audit unavailable")
	}
	s.records = append(s.records, r)
	return nil
}

// fixture supplies resolved membership and injected clock without real services.
func fixture(t *testing.T) (*Service, *MemoryStore, *auditSink, context.Context) {
	t.Helper()
	sink := &auditSink{}
	reader := &contextFixture{}
	store := NewMemoryStore(sink, reader)
	s, err := NewService(store, reader, clock.NewFake(time.Date(2026, 9, 14, 12, 0, 0, 123456789, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	a := identity.Actor{TenantID: testTenant, MembershipID: testMember, RealmID: testRealm, Subject: "same-subject", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}}
	return s, store, sink, identityfixture.WithActor(t.Context(), a)
}

// input is valid general-resource feedback containing deliberate markup-like data.
func input() CreateInput {
	return CreateInput{ResourceID: testResource, Category: "problem", Comment: "<script>not executable</script>"}
}

// requireCode compares public error classifications without matching private causes.
func requireCode(t *testing.T, err error, code catalog.ErrorCode) {
	t.Helper()
	if catalog.ErrorCodeOf(err) != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestFeedbackLifecyclePrivacyAndAudit(t *testing.T) {
	s, store, sink, ctx := fixture(t)
	r, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := identity.FromContext(ctx)
	a.MembershipID = testRealm
	other := identityfixture.WithActor(ctx, a)
	_, err = s.Get(other, r.ID)
	requireCode(t, err, catalog.CodeNotFound)
	page, err := s.List(other, ListInput{Limit: 20}, false)
	if err != nil || len(page) != 0 {
		t.Fatalf("other page: %v %v", page, err)
	}
	a.Roles = []identity.Role{identity.RoleTenantAdmin}
	admin := identityfixture.WithActor(ctx, a)
	r, err = s.Change(admin, r.ID, 1, "address", EditInput{}, "Fixed the documentation")
	if err != nil || r.State != "addressed" {
		t.Fatalf("address: %v", err)
	}
	_, err = s.Change(ctx, r.ID, 1, "edit", EditInput{Category: "other", Comment: "stale"}, "")
	requireCode(t, err, catalog.CodePreconditionFailed)
	r, err = s.Change(ctx, r.ID, 2, "edit", EditInput{Category: "suggestion", Comment: "New comment"}, "")
	if err != nil || r.State != "open" || r.Response != "" {
		t.Fatalf("edit: %#v %v", r, err)
	}
	_, err = s.Change(admin, r.ID, 3, "edit", EditInput{Category: "other", Comment: "admin overwrite"}, "")
	requireCode(t, err, catalog.CodeNotFound)
	r, err = s.Change(admin, r.ID, 3, "address", EditInput{}, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.Change(admin, r.ID, 4, "reopen", EditInput{}, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.Change(ctx, r.ID, 5, "withdraw", EditInput{}, "")
	if err != nil || r.Comment != "" || r.Response != "" || r.State != "withdrawn" {
		t.Fatalf("withdraw: %#v %v", r, err)
	}
	_, err = s.Change(admin, r.ID, 6, "address", EditInput{}, "revive")
	requireCode(t, err, catalog.CodeConflict)
	stored, err := store.Get(ctx, testTenant, r.ID)
	if err != nil || stored.Comment != "" || stored.Response != "" {
		t.Fatal("terminal text retained")
	}
	encoded, err := audit.EncodeRecords(sink.records)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"New comment", "Fixed the documentation", "not executable"} {
		if strings.Contains(string(encoded), text) {
			t.Fatal("audit retained private text")
		}
	}
}

// Replica clock skew must not turn valid edits or erasure into invalid records.
func TestFeedbackMutationTimesAcrossReplicaClocks(t *testing.T) {
	for _, terminal := range []string{"withdraw", "remove"} {
		t.Run(terminal, func(t *testing.T) {
			first, store, sink, ctx := fixture(t)
			r, err := first.Create(ctx, input())
			if err != nil {
				t.Fatal(err)
			}
			a, _ := identity.FromContext(ctx)
			a.Roles = []identity.Role{identity.RoleTenantAdmin}
			ctx = identityfixture.WithActor(ctx, a)
			ahead := clock.NewFake(r.CreatedAt.Add(time.Second))
			first.clock = ahead
			r, err = first.Change(ctx, r.ID, r.Revision, "address", EditInput{}, "Response")
			if err != nil {
				t.Fatal(err)
			}
			behind := clock.NewFake(r.CreatedAt.Add(-time.Second))
			second, err := NewService(store, first.context, behind, first.ids)
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"reopen", "edit", "address", terminal} {
				previous := r
				r, err = second.Change(ctx, r.ID, r.Revision, action, EditInput{Category: "other", Comment: "Edited"}, "Response")
				if err != nil {
					t.Fatalf("%s with an earlier replica clock: %v", action, err)
				}
				if !r.CreatedAt.Equal(previous.CreatedAt) || !r.UpdatedAt.Equal(previous.UpdatedAt) || r.Revision != previous.Revision+1 || !sink.records[len(sink.records)-1].At.Equal(r.UpdatedAt) {
					t.Fatalf("%s changed immutable time, regressed ordering or diverged from audit", action)
				}
				_, err = first.Change(ctx, r.ID, previous.Revision, "reopen", EditInput{}, "")
				requireCode(t, err, catalog.CodePreconditionFailed)
			}
			if r.Comment != "" || r.Response != "" {
				t.Fatal("terminal transition retained private text")
			}
		})
	}
}

func TestFeedbackContextAuthorizationAndBounds(t *testing.T) {
	s, _, _, ctx := fixture(t)
	for _, mutate := range []func(*identity.Actor){func(a *identity.Actor) { a.ActorType = identity.ActorService }, func(a *identity.Actor) { a.MembershipID = "" }, func(a *identity.Actor) { a.Roles = []identity.Role{identity.RoleServiceClient} }, func(a *identity.Actor) {
		a.Roles = nil
		a.Namespaces = map[string][]identity.Role{testResource: {identity.RoleReader}}
	}} {
		a, _ := identity.FromContext(ctx)
		mutate(&a)
		_, err := s.Create(identityfixture.WithActor(ctx, a), input())
		requireCode(t, err, catalog.CodeForbidden)
	}
	for _, comment := range []string{"", " \n", string([]byte{0xff}), "bad\x00text", strings.Repeat("x", 4001)} {
		in := input()
		in.Comment = comment
		_, err := s.Create(ctx, in)
		requireCode(t, err, catalog.CodeInvalidArgument)
	}
	in := input()
	in.Comment = strings.Repeat("\U0001f600", 4000)
	if _, err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	in = input()
	in.ReleaseID = testRelease
	r, err := s.Create(ctx, in)
	if err != nil || r.VersionRevisionID != testRevision {
		t.Fatalf("derived context: %#v %v", r, err)
	}
	in.VersionRevisionID = testResource
	_, err = s.Create(ctx, in)
	requireCode(t, err, catalog.CodeInvalidArgument)
	reader, ok := s.context.(*contextFixture)
	if !ok {
		t.Fatal("unexpected context reader")
	}
	reader.mismatch = true
	in.VersionRevisionID = ""
	_, err = s.Create(ctx, in)
	requireCode(t, err, catalog.CodeInvalidArgument)
	for _, q := range []ListInput{{Limit: 0}, {Limit: 101}, {Limit: 20, AfterID: "bad"}, {Limit: 20, Category: "rating"}, {Limit: 20, State: "unknown"}} {
		_, err := s.List(ctx, q, false)
		requireCode(t, err, catalog.CodeInvalidArgument)
	}
	_, err = s.List(ctx, ListInput{Limit: 20}, true)
	requireCode(t, err, catalog.CodeForbidden)
}

// Access loss hides text before pagination without preventing administrative removal.
func TestFeedbackResourceAccessLossAndRealmOwnership(t *testing.T) {
	s, store, _, ctx := fixture(t)
	r, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := identity.FromContext(ctx)
	a.RealmID, a.MembershipID = testRelease, testRevision
	otherRealm := identityfixture.WithActor(ctx, a) // Same subject is not the same membership.
	_, err = s.Get(otherRealm, r.ID)
	requireCode(t, err, catalog.CodeNotFound)
	reader, ok := s.context.(*contextFixture)
	if !ok {
		t.Fatal("unexpected context reader")
	}
	reader.fail = true
	_, err = s.Get(ctx, r.ID)
	requireCode(t, err, catalog.CodeNotFound)
	page, err := s.List(ctx, ListInput{Limit: 1}, false)
	if err != nil || len(page) != 0 {
		t.Fatalf("inaccessible feedback included in page: %d %v", len(page), err)
	}
	a.Roles = []identity.Role{identity.RoleTenantAdmin}
	removed, err := s.Change(identityfixture.WithActor(ctx, a), r.ID, 1, "remove", EditInput{}, "")
	if err != nil || removed.State != "removed" {
		t.Fatalf("admin could not remove retained feedback: %v", err)
	}
	retained, err := store.Get(ctx, testTenant, r.ID)
	if err != nil || retained.Comment != "" || retained.Response != "" {
		t.Fatal("retained text after removal")
	}
}

func TestFeedbackAtomicFailureAdmissionAndConcurrentEdits(t *testing.T) {
	s, store, sink, ctx := fixture(t)
	r, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	sink.fail = true
	_, err = s.Change(ctx, r.ID, 1, "edit", EditInput{Category: "other", Comment: "not committed"}, "")
	if err == nil {
		t.Fatal("audit failure ignored")
	}
	stored, err := store.Get(ctx, testTenant, r.ID)
	if err != nil || stored != r || len(sink.records) != 1 {
		t.Fatal("state committed without audit")
	}
	sink.fail = false
	s.active.Store(8)
	_, err = s.Create(ctx, input())
	requireCode(t, err, catalog.CodeDependencyUnavailable)
	s.active.Store(0)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := s.Change(ctx, r.ID, 1, "edit", EditInput{Category: "other", Comment: "concurrent"}, "")
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			requireCode(t, err, catalog.CodePreconditionFailed)
		}
	}
	if success != 1 {
		t.Fatalf("successful concurrent edits=%d", success)
	}
	if len(sink.records) != 2 {
		t.Fatalf("audit count=%d", len(sink.records))
	}
}
