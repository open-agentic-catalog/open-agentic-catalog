package collections

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

const tenant = "00000000-0000-7000-8000-000000000001"
const owner = "aaaaaaaa-0000-7000-8000-000000000010"
const target = "00000000-0000-7000-8000-000000000011"

func unitID(n int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", n) }

type localFixture struct {
	namespaces map[string]bool
	records    map[string]catalog.CatalogSummary
	evidence   []audit.Record
	fail       bool
	entryErr   error
	generation string
}

func (f *localFixture) GetNamespace(_ context.Context, ten, ns string) (catalog.Namespace, error) {
	if ten == tenant && (ns == owner || ns == target || f.namespaces[ns]) {
		return catalog.Namespace{ID: ns, TenantID: ten}, nil
	}
	return catalog.Namespace{}, catalog.E(catalog.CodeNotFound, "namespace unavailable")
}
func (f *localFixture) AppendAuditEvent(_ context.Context, e audit.Record) error {
	if f.fail {
		return errors.New("audit unavailable")
	}
	f.evidence = append(f.evidence, e)
	return nil
}
func (f *localFixture) CollectionEntries(ctx context.Context, ten string, ids []string) ([]catalog.CatalogSummary, string, error) {
	if f.entryErr != nil {
		return nil, "", f.entryErr
	}
	out := []catalog.CatalogSummary{}
	for _, id := range ids {
		record, ok := f.records[id]
		if !ok || record.Resource.TenantID != ten {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.read", record.Listing.NamespaceID)
		if err != nil {
			return nil, "", err
		}
		if allowed {
			out = append(out, record)
		}
	}
	return out, f.generation, nil
}
func actor(t *testing.T, typ identity.ActorType, roles []identity.Role, scopes map[string][]identity.Role) context.Context {
	t.Helper()
	return identityfixture.WithActor(t.Context(), identity.Actor{TenantID: tenant, MembershipID: unitID(2), RealmID: unitID(3), Subject: "curator", ActorType: typ, Roles: roles, Namespaces: scopes})
}
func fixture(t *testing.T) (*Service, *MemoryStore, *localFixture, context.Context) {
	t.Helper()
	source := &localFixture{records: map[string]catalog.CatalogSummary{}, generation: "initial", namespaces: map[string]bool{}}
	store := NewMemoryStore(source)
	service, err := New(store, clock.NewFake(time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, source, actor(t, identity.ActorHuman, []identity.Role{identity.RoleTenantAdmin}, nil)
}
func create(t *testing.T, s *Service, ctx context.Context) Collection {
	t.Helper()
	c, err := s.Create(ctx, Input{NamespaceID: owner, Name: " Finance tools ", Description: " Useful entries "})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func seed(source *localFixture, n int, ns string, kind catalog.Kind) string {
	key := unitID(n)
	source.records[key] = catalog.CatalogSummary{Source: "local", Resource: catalog.CatalogResourceSummary{ID: unitID(n + 1000), TenantID: tenant, NamespaceID: ns, Kind: kind}, Listing: catalog.CatalogListingSummary{ID: key, NamespaceID: ns}, Release: catalog.CatalogReleaseSummary{ID: unitID(n + 2000), State: catalog.ReleasePublished}}
	return key
}
func TestTextBoundsAndNormalization(t *testing.T) {
	for _, test := range []struct {
		name, description string
		valid             bool
	}{{" Finance tools ", " Shared\ntext ", true}, {strings.Repeat("📚", 160), strings.Repeat("📚", 2000), true}, {strings.Repeat("x", 161), "", false}, {"Good", strings.Repeat("x", 2001), false}, {" \t ", "", false}, {"line\nbreak", "", false}, {"line\u2028break", "", false}, {"line\u2029break", "", false}, {"name\x00", "", false}, {"name", "bad\x01", false}, {"name", "ok\r\n\ttext", true}, {string([]byte{255}), "", false}} {
		name, description, err := Normalize(test.name, test.description)
		if (err == nil) != test.valid {
			t.Fatalf("Normalize(%q): %v", test.name, err)
		}
		if test.valid && (name != strings.TrimSpace(test.name) || description != strings.TrimSpace(test.description)) {
			t.Fatal("unexpected text normalization")
		}
	}
}
func TestMetadataRevisionAndAtomicAudit(t *testing.T) {
	s, m, f, ctx := fixture(t)
	c := create(t, s, ctx)
	if c.Name != "Finance tools" || c.Description != "Useful entries" || c.Revision != 1 || len(f.evidence) != 1 {
		t.Fatal(c, f.evidence)
	}
	name := "New name"
	next, err := s.Change(ctx, c.ID, 1, Update, Patch{Name: &name}, "")
	if err != nil || next.Revision != 2 || next.Description != c.Description {
		t.Fatal(next, err)
	}
	if _, err := s.Change(ctx, c.ID, 1, Delete, Patch{}, ""); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("stale write accepted", err)
	}
	empty := ""
	next, err = s.Change(ctx, c.ID, 2, Update, Patch{Description: &empty}, "")
	if err != nil || next.Description != "" {
		t.Fatal(next, err)
	}
	if _, err := s.Change(ctx, c.ID, 3, Update, Patch{}, ""); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("empty patch", err)
	}
	f.fail = true
	if _, err := s.Change(ctx, c.ID, 3, Delete, Patch{}, ""); err == nil {
		t.Fatal("audit failure ignored")
	}
	if got := m.items[tenant][c.ID]; got.Revision != 3 {
		t.Fatal("failed audit changed state", got)
	}
	f.fail = false
	receipt, err := s.Change(ctx, c.ID, 3, Delete, Patch{}, "")
	if err != nil || receipt.Revision != 4 {
		t.Fatal(receipt, err)
	}
	if _, err := s.Get(ctx, c.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("deletion retained live collection", err)
	}
	for _, e := range f.evidence {
		facts, ok := e.Details["facts"].(map[string]any)
		if !ok {
			t.Fatal("audit facts missing")
		}
		if len(facts) != 2 || facts["namespaceId"] != owner {
			t.Fatal("unexpected audit disclosure", facts)
		}
	}
}
func TestMembersKindsFollowingAndSnapshotFences(t *testing.T) {
	s, _, f, ctx := fixture(t)
	c := create(t, s, ctx)
	for n, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin} {
		key := seed(f, n+100, target, kind)
		var err error
		c, err = s.Change(ctx, c.ID, c.Revision, Add, Patch{}, key)
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Members(ctx, c.ID, MemberRequest{Limit: 2})
	if err != nil || len(page.Items) != 3 {
		t.Fatal(page, err)
	}
	if _, err := s.Change(ctx, c.ID, c.Revision, Add, Patch{}, unitID(100)); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatal("duplicate accepted", err)
	}
	old := f.records[unitID(100)]
	old.Release.ID = unitID(9000)
	f.records[unitID(100)] = old
	f.generation = "replacement"
	if _, err := s.Members(ctx, c.ID, MemberRequest{Limit: 2, Revision: c.Revision, Generation: page.Generation}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("stale release continuation accepted", err)
	}
	page, err = s.Members(ctx, c.ID, MemberRequest{Limit: 100})
	if err != nil || len(page.Items) != 4 || page.Items[0].Release.ID != unitID(9000) {
		t.Fatal("did not follow current release", page, err)
	}
	delete(f.records, unitID(100))
	page, err = s.Members(ctx, c.ID, MemberRequest{Limit: 100})
	if err != nil || len(page.Items) != 3 {
		t.Fatal("ineligible target retained in result", page, err)
	}
	// Membership persists and becomes visible when a later eligible release exists.
	f.records[unitID(100)] = old
	page, err = s.Members(ctx, c.ID, MemberRequest{Limit: 100})
	if err != nil || len(page.Items) != 4 {
		t.Fatal(page, err)
	}
}
func TestOwnerAndTargetScopesHiddenPrefixAndRemoval(t *testing.T) {
	s, _, f, admin := fixture(t)
	c := create(t, s, admin)
	for n := 100; n < 140; n++ {
		key := seed(f, n, target, catalog.KindAgent)
		var err error
		c, err = s.Change(admin, c.ID, c.Revision, Add, Patch{}, key)
		if err != nil {
			t.Fatal(err)
		}
	}
	visible := seed(f, 200, owner, catalog.KindSkill)
	var err error
	c, err = s.Change(admin, c.ID, c.Revision, Add, Patch{}, visible)
	if err != nil {
		t.Fatal(err)
	}
	scoped := actor(t, identity.ActorHuman, nil, map[string][]identity.Role{owner: {identity.RoleContributor}})
	page, err := s.Members(scoped, c.ID, MemberRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Listing.ID != visible {
		t.Fatal("hidden prefix blocked visible result", page, err)
	}
	denied := seed(f, 300, target, catalog.KindPlugin)
	if _, err := s.Change(scoped, c.ID, c.Revision, Add, Patch{}, denied); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("collection manager obtained target access", err)
	}
	// A manager can remove a known membership without reading its target.
	c, err = s.Change(scoped, c.ID, c.Revision, Remove, Patch{}, unitID(100))
	if err != nil {
		t.Fatal("inaccessible exact removal failed", err)
	}
	if _, err := s.Change(scoped, c.ID, c.Revision, Remove, Patch{}, unitID(999)); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal(err)
	}
	reader := actor(t, identity.ActorHuman, nil, map[string][]identity.Role{owner: {identity.RoleReader}})
	if _, err := s.Change(reader, c.ID, c.Revision, Delete, Patch{}, ""); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("reader managed collection", err)
	}
	if _, err := s.Change(scoped, c.ID, c.Revision, Delete, Patch{}, ""); err != nil {
		t.Fatal("hidden members prevented deletion", err)
	}
}
func TestServiceMembershipCurationAndTenantIsolation(t *testing.T) {
	s, _, _, _ := fixture(t)
	service := actor(t, identity.ActorService, []identity.Role{identity.RoleContributor}, nil)
	c := create(t, s, service)
	other := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: unitID(9), MembershipID: unitID(2), RealmID: unitID(3), Subject: "other", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	if _, err := s.Get(other, c.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("cross tenant collection disclosed", err)
	}
	page, err := s.List(other, ListRequest{Limit: 20})
	if err != nil || len(page) != 0 {
		t.Fatal("cross tenant directory", page, err)
	}
}
func TestQuotaChecksAndConcurrentAggregateEdits(t *testing.T) {
	s, m, f, ctx := fixture(t)
	c := create(t, s, ctx)
	for n := 100; n < 300; n++ {
		key := seed(f, n, target, catalog.KindAgent)
		var err error
		c, err = s.Change(ctx, c.ID, c.Revision, Add, Patch{}, key)
		if err != nil {
			t.Fatal(n, err)
		}
	}
	extra := seed(f, 400, target, catalog.KindAgent)
	if _, err := s.Change(ctx, c.ID, c.Revision, Add, Patch{}, extra); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatal("member cap not enforced", err)
	}
	for n := 1; n < MaxNamespaceCollections; n++ {
		if _, err := s.Create(ctx, Input{NamespaceID: owner, Name: "Same name"}); err != nil {
			t.Fatal(n, err)
		}
	}
	if _, err := s.Create(ctx, Input{NamespaceID: owner, Name: "One too many"}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatal("namespace cap not enforced", err)
	}
	// Populate other namespaces with valid retained metadata to isolate the tenant cap.
	for n := 1000; len(m.items[tenant]) < MaxTenantCollections; n++ {
		item := c
		item.ID = unitID(n)
		item.NamespaceID = unitID(10000 + (n-1000)/100)
		f.namespaces[item.NamespaceID] = true
		m.items[tenant][item.ID] = item
	}
	if _, err := s.Create(ctx, Input{NamespaceID: target, Name: "Tenant overflow"}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatal("tenant cap not enforced", err)
	}
	// Two edits admitted at the same revision can commit only once.
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, name := range []string{"First", "Second"} {
		wg.Go(func() { _, err := s.Change(ctx, c.ID, c.Revision, Update, Patch{Name: &name}, ""); out <- err })
	}
	wg.Wait()
	close(out)
	successes, stale := 0, 0
	for err := range out {

		switch {
		case err == nil:
			successes++
		case catalog.ErrorCodeOf(err) == catalog.CodePreconditionFailed:
			stale++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatal("concurrent edits lost revision fencing", successes, stale)
	}
}

func TestUUIDSpellingsSelectTheSameScopeAndReferences(t *testing.T) {
	s, _, source, ctx := fixture(t)
	c, err := s.Create(ctx, Input{NamespaceID: strings.ToUpper(owner), Name: "Case spelling"})
	if err != nil || c.NamespaceID != owner {
		t.Fatal(c, err)
	}
	got, err := s.Get(ctx, strings.ToUpper(c.ID))
	if err != nil || got.ID != c.ID {
		t.Fatal(got, err)
	}
	page, err := s.List(ctx, ListRequest{NamespaceID: strings.ToUpper(owner), Limit: 20})
	if err != nil || len(page) != 1 {
		t.Fatal(page, err)
	}
	oldID := seed(source, 100, target, catalog.KindAgent)
	record := source.records[oldID]
	delete(source.records, oldID)
	key := "bbbbbbbb-0000-7000-8000-000000000100"
	record.Listing.ID = key
	source.records[key] = record
	c, err = s.Change(ctx, strings.ToUpper(c.ID), c.Revision, Add, Patch{}, strings.ToUpper(key))
	if err != nil {
		t.Fatal(err)
	}
	members, err := s.Members(ctx, strings.ToUpper(c.ID), MemberRequest{Limit: 1, AfterID: strings.ToUpper(key)})
	if err != nil || len(members.Items) != 0 {
		t.Fatal(members, err)
	}
	if _, err := s.Change(ctx, c.ID, c.Revision, Remove, Patch{}, strings.ToUpper(key)); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryPreselectsOwnerScopesAndContinues(t *testing.T) {
	s, _, _, ctx := fixture(t)
	for range 4 {
		if _, err := s.Create(ctx, Input{NamespaceID: target, Name: "Other team's collection"}); err != nil {
			t.Fatal(err)
		}
	}
	want := make([]string, 0, 3)
	for range 3 {
		c := create(t, s, ctx)
		want = append(want, c.ID)
	}
	slices.Sort(want)
	reader := actor(t, identity.ActorHuman, nil, map[string][]identity.Role{owner: {identity.RoleReader}})
	first, err := s.List(reader, ListRequest{Limit: 1})
	if err != nil || len(first) != 2 || first[0].ID != want[0] || first[1].ID != want[1] {
		t.Fatal("owner selection must precede limit+1", first, err)
	}
	second, err := s.List(reader, ListRequest{Limit: 1, AfterID: first[0].ID})
	if err != nil || len(second) != 2 || second[0].ID != want[1] || second[1].ID != want[2] {
		t.Fatal("directory continuation", second, err)
	}
	last, err := s.List(reader, ListRequest{NamespaceID: owner, Limit: 1, AfterID: want[2]})
	if err != nil || len(last) != 0 {
		t.Fatal("directory terminal page", last, err)
	}
	hidden, err := s.List(reader, ListRequest{NamespaceID: target, Limit: 100})
	if err != nil || len(hidden) != 0 {
		t.Fatal("namespace filter granted access", hidden, err)
	}
}

func TestRejectedCommandsLeaveCollectionAndAuditUnchanged(t *testing.T) {
	s, _, source, ctx := fixture(t)
	c := create(t, s, ctx)
	baseline := len(source.evidence)
	blank := "  "
	valid := "Changed"
	for name, run := range map[string]func() error{
		"zero revision":          func() error { _, e := s.Change(ctx, c.ID, 0, Update, Patch{Name: &valid}, ""); return e },
		"overflow revision":      func() error { _, e := s.Change(ctx, c.ID, 1<<63-1, Update, Patch{Name: &valid}, ""); return e },
		"create through edit":    func() error { _, e := s.Change(ctx, c.ID, c.Revision, Create, Patch{}, ""); return e },
		"unknown operation":      func() error { _, e := s.Change(ctx, c.ID, c.Revision, Operation("unknown"), Patch{}, ""); return e },
		"empty edit":             func() error { _, e := s.Change(ctx, c.ID, c.Revision, Update, Patch{}, ""); return e },
		"empty normalized name":  func() error { _, e := s.Change(ctx, c.ID, c.Revision, Update, Patch{Name: &blank}, ""); return e },
		"metadata in add":        func() error { _, e := s.Change(ctx, c.ID, c.Revision, Add, Patch{Name: &valid}, unitID(20)); return e },
		"metadata in delete":     func() error { _, e := s.Change(ctx, c.ID, c.Revision, Delete, Patch{Name: &valid}, ""); return e },
		"invalid collection ID":  func() error { _, e := s.Get(ctx, "bad"); return e },
		"invalid namespace ID":   func() error { _, e := s.Create(ctx, Input{NamespaceID: "bad", Name: "Good"}); return e },
		"invalid list limit":     func() error { _, e := s.List(ctx, ListRequest{Limit: 0}); return e },
		"invalid list namespace": func() error { _, e := s.List(ctx, ListRequest{NamespaceID: "bad", Limit: 1}); return e },
		"invalid member page":    func() error { _, e := s.Members(ctx, c.ID, MemberRequest{Limit: 101}); return e },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatal(err)
			}
			got, err := s.Get(ctx, c.ID)
			if err != nil || got != c || len(source.evidence) != baseline {
				t.Fatal("refusal changed state or audit", got, err, len(source.evidence))
			}
		})
	}
}

func TestUnauthenticatedCollectionCallsFailWithoutDisclosure(t *testing.T) {
	s, _, source, ctx := fixture(t)
	c := create(t, s, ctx)
	for name, run := range map[string]func(context.Context) error{
		"read":    func(ctx context.Context) error { _, e := s.Get(ctx, c.ID); return e },
		"list":    func(ctx context.Context) error { _, e := s.List(ctx, ListRequest{Limit: 1}); return e },
		"members": func(ctx context.Context) error { _, e := s.Members(ctx, c.ID, MemberRequest{Limit: 1}); return e },
		"create": func(ctx context.Context) error {
			_, e := s.Create(ctx, Input{NamespaceID: owner, Name: "Forbidden"})
			return e
		},
		"edit": func(ctx context.Context) error {
			_, e := s.Change(ctx, c.ID, c.Revision, Delete, Patch{}, "")
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(t.Context()); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
				t.Fatal(err)
			}
		})
	}
	if len(source.evidence) != 1 {
		t.Fatal("unauthenticated command emitted audit")
	}
}

type failingIDs struct{ calls, failOn int }

func (f *failingIDs) New(time.Time) (string, error) {
	f.calls++
	if f.calls == f.failOn {
		return "", errors.New("injected identifier allocation failure")
	}
	return unitID(100 + f.calls), nil
}

func TestIdentifierFailuresDoNotPersistPartialCollections(t *testing.T) {
	for _, failure := range []int{1, 2} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			s, store, source, ctx := fixture(t)
			s.ids = &failingIDs{failOn: failure}
			if _, err := s.Create(ctx, Input{NamespaceID: owner, Name: "Unsaved"}); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatal(err)
			}
			got, err := s.List(ctx, ListRequest{Limit: 100})
			if err != nil || len(got) != 0 || len(source.evidence) != 0 {
				t.Fatal("allocation failure retained partial state", got, err)
			}
			if _, err := New(store, nil, id.Generator{}); err == nil {
				t.Fatal("accepted absent clock")
			}
		})
	}
}

func TestRepositoryPortsRejectAnotherPrincipal(t *testing.T) {
	s, store, source, ctx := fixture(t)
	c := create(t, s, ctx)
	bound, a, err := readContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other := a
	other.MembershipID = unitID(99)
	for name, run := range map[string]func() error{
		"read":    func() error { _, e := store.Get(bound, other, c.ID); return e },
		"list":    func() error { _, e := store.List(bound, other, ListRequest{Limit: 1}); return e },
		"target":  func() error { _, e := store.Target(bound, other, unitID(20)); return e },
		"members": func() error { _, e := store.Members(bound, other, c.ID, MemberRequest{Limit: 1}); return e },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
				t.Fatal("repository accepted a mismatched actor", err)
			}
		})
	}
	if len(source.evidence) != 1 {
		t.Fatal("denied repository read changed audit")
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil || got != c {
		t.Fatal("denial changed collection", got, err)
	}
	if _, err := store.List(bound, a, ListRequest{Limit: 102}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("unbounded repository directory", err)
	}
	if _, err := store.Members(bound, a, c.ID, MemberRequest{Limit: 102}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("unbounded repository member page", err)
	}
	if _, err := s.Create(ctx, Input{NamespaceID: unitID(999), Name: "No such namespace"}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("created collection without owner namespace", err)
	}
	if len(source.evidence) != 1 {
		t.Fatal("failed creation emitted evidence")
	}
}

func TestTargetReadFailurePreservesCollectionAndMembership(t *testing.T) {
	s, _, source, ctx := fixture(t)
	c := create(t, s, ctx)
	first := seed(source, 600, owner, catalog.KindAgent)
	second := seed(source, 601, owner, catalog.KindMCPServer)
	var err error
	c, err = s.Change(ctx, c.ID, c.Revision, Add, Patch{}, first)
	if err != nil {
		t.Fatal(err)
	}
	// A completed read establishes the control before losing the dependency.
	page, err := s.Members(ctx, c.ID, MemberRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Listing.ID != first {
		t.Fatalf("control page=%+v err=%v", page, err)
	}
	sentinel := catalog.E(catalog.CodeDependencyUnavailable, "target read unavailable")
	source.entryErr = sentinel
	page, err = s.Members(ctx, c.ID, MemberRequest{Limit: 10})
	if !errors.Is(err, sentinel) || len(page.Items) != 0 {
		t.Fatalf("failed read disclosed a partial page: %+v, %v", page, err)
	}
	if got, err := s.Change(ctx, c.ID, c.Revision, Add, Patch{}, second); !errors.Is(err, sentinel) || got != (Collection{}) {
		t.Fatalf("target failure returned a successful mutation: %+v, %v", got, err)
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil || got != c || len(source.evidence) != 2 {
		t.Fatalf("target failure changed collection or audit: %+v, evidence=%d, %v", got, len(source.evidence), err)
	}
	source.entryErr = nil
	page, err = s.Members(ctx, c.ID, MemberRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Listing.ID != first {
		t.Fatalf("recovery lost or added a reference: %+v, %v", page, err)
	}
	if _, err := s.Change(ctx, c.ID, c.Revision, Add, Patch{}, unitID(999)); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("ineligible target=%v", err)
	}
	if len(source.evidence) != 2 {
		t.Fatal("ineligible target emitted evidence")
	}
}

func TestAggregateAndAuditBindingsRejectPartialWrites(t *testing.T) {
	s, store, source, ctx := fixture(t)
	c := create(t, s, ctx)
	ctx = identity.WithPermissionContext(ctx)
	if err := require(ctx, "collection.read", c.NamespaceID); err != nil {
		t.Fatal(err)
	}
	if err := require(ctx, "collection.manage", c.NamespaceID); err != nil {
		t.Fatal(err)
	}
	a, err := Actor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate := c
	candidate.Name = "Confirmed edit"
	candidate.Revision++
	candidate.UpdatedAt = c.UpdatedAt.Add(time.Second)
	evidence := func(next Collection) audit.Record {
		t.Helper()
		e, err := audit.NewRecord(unitID(501), tenant, audit.Principal{ID: a.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(a.ActorType), RealmID: a.RealmID}, "collection.updated", "collection", c.ID, "", audit.Execution{AuthorizationDigest: identity.DecisionDigest(ctx)}, map[string]any{"revision": next.Revision, "namespaceId": next.NamespaceID}, next.UpdatedAt)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	for name, tamper := range map[string]func(*Collection, *audit.Record){
		"owner transfer":             func(n *Collection, _ *audit.Record) { n.NamespaceID = target },
		"revision skipped":           func(n *Collection, _ *audit.Record) { n.Revision++ },
		"clock reversal":             func(n *Collection, _ *audit.Record) { n.UpdatedAt = c.CreatedAt.Add(-time.Second) },
		"unnormalized text":          func(n *Collection, _ *audit.Record) { n.Name = " " + n.Name },
		"unversioned evidence":       func(_ *Collection, e *audit.Record) { e.FormatVersion = nil },
		"another tenant in evidence": func(_ *Collection, e *audit.Record) { e.TenantID = unitID(2) },
		"another actor in evidence":  func(_ *Collection, e *audit.Record) { e.Actor = unitID(99) },
		"another namespace in evidence": func(_ *Collection, e *audit.Record) {
			e.Details["facts"] = map[string]any{"revision": candidate.Revision, "namespaceId": target}
		},
	} {
		t.Run(name, func(t *testing.T) {
			next := candidate
			e := evidence(next)
			tamper(&next, &e)
			if _, err := store.Save(ctx, a, next, c.Revision, Update, "", e); err == nil {
				t.Fatal("accepted an unbound aggregate or audit record")
			}
			got, err := s.Get(ctx, c.ID)
			if err != nil || got != c || len(source.evidence) != 1 {
				t.Fatal("refusal retained partial state or evidence", got, err)
			}
		})
	}
	// A valid aggregate/evidence pair commits once after every rejected attempt.
	got, err := store.Save(ctx, a, candidate, c.Revision, Update, "", evidence(candidate))
	if err != nil || got != candidate || len(source.evidence) != 2 {
		t.Fatal("valid control failed", got, err)
	}
}

func TestExactCollectionReadsHideOtherOwnerScopes(t *testing.T) {
	s, _, source, admin := fixture(t)
	c := create(t, s, admin)
	reader := actor(t, identity.ActorHuman, nil, map[string][]identity.Role{target: {identity.RoleReader}})
	for _, key := range []string{c.ID, unitID(999)} {
		if got, err := s.Get(reader, key); catalog.ErrorCodeOf(err) != catalog.CodeNotFound || got != (Collection{}) {
			t.Fatalf("exact read disclosed an unavailable collection: %+v, %v", got, err)
		}
		if got, err := s.Members(reader, key, MemberRequest{Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound || len(got.Items) != 0 || got.Collection != (Collection{}) {
			t.Fatalf("member read disclosed an unavailable collection: %+v, %v", got, err)
		}
		if got, err := s.Change(reader, key, c.Revision, Delete, Patch{}, ""); catalog.ErrorCodeOf(err) != catalog.CodeNotFound || got != (Collection{}) {
			t.Fatalf("mutation disclosed an unavailable collection: %+v, %v", got, err)
		}
	}
	// Reading one owner scope must not authorize creation there.
	ownerReader := actor(t, identity.ActorHuman, nil, map[string][]identity.Role{owner: {identity.RoleReader}})
	if got, err := s.Get(ownerReader, c.ID); err != nil || got != c {
		t.Fatalf("in-scope read control failed: %+v, %v", got, err)
	}
	if _, err := s.Create(ownerReader, Input{NamespaceID: owner, Name: "Denied creation"}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("reader created a collection: %v", err)
	}
	if got, err := s.Get(admin, c.ID); err != nil || got != c || len(source.evidence) != 1 {
		t.Fatalf("denied calls changed metadata or audit: %+v, audit=%d, %v", got, len(source.evidence), err)
	}
}

func TestValidateSaveKeepsMemberChangesSeparateFromMetadata(t *testing.T) {
	for _, op := range []Operation{Add, Remove} {
		t.Run(string(op), func(t *testing.T) {
			s, _, _, ctx := fixture(t)
			old := create(t, s, ctx)
			a, err := Actor(ctx)
			if err != nil {
				t.Fatal(err)
			}
			next := old
			next.Revision++
			next.UpdatedAt = next.UpdatedAt.Add(time.Second)
			listing := unitID(600)
			e, err := audit.NewRecord(unitID(601), a.TenantID, audit.Principal{ID: a.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(a.ActorType), RealmID: a.RealmID}, "collection."+string(op), "collection", next.ID, "", audit.Execution{}, map[string]any{"revision": next.Revision, "namespaceId": next.NamespaceID, "listingId": listing}, next.UpdatedAt)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateSave(a, old, next, old.Revision, op, listing, e); err != nil {
				t.Fatal("valid membership transition control", err)
			}
			for _, field := range []string{"name", "description"} {
				changed := next
				if field == "name" {
					changed.Name = "Changed metadata"
				} else {
					changed.Description = "Changed metadata"
				}
				if err := ValidateSave(a, old, changed, old.Revision, op, listing, e); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
					t.Fatalf("member command changed %s: %v", field, err)
				}
			}
			if err := ValidateSave(a, old, next, old.Revision, op, "", e); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("member command accepted no target: %v", err)
			}
		})
	}
}
