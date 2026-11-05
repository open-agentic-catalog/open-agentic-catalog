package ratings

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"sync"
	"testing"
	"time"
)

const tenant = "00000000-0000-7000-8000-000000000001"
const member = "00000000-0000-7000-8000-000000000002"
const realm = "00000000-0000-7000-8000-000000000003"
const resource = "00000000-0000-7000-8000-000000000004"
const revision = "00000000-0000-7000-8000-000000000005"
const release = "00000000-0000-7000-8000-000000000006"

type authorityFixture struct{ deny bool }

func (a *authorityFixture) Admit(_ context.Context, input Admission) (Admission, error) {
	if a.deny {
		return input, catalog.E(catalog.CodeNotFound, "target is no longer readable")
	}
	input.NamespaceID = resource
	return input, nil
}

type sink struct {
	records []audit.Record
	fail    bool
}

func (s *sink) AppendAuditEvent(_ context.Context, r audit.Record) error {
	if s.fail {
		return errors.New("audit unavailable")
	}
	s.records = append(s.records, r)
	return nil
}
func fixture(t *testing.T) (*Service, *MemoryStore, *sink, *authorityFixture, context.Context) {
	t.Helper()
	e := &sink{}
	a := &authorityFixture{}
	store := NewMemoryStore(e)
	service, err := New(store, a, clock.NewFake(time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{Issuer: "https://issuer.example", TenantID: tenant, Subject: "alice", MembershipID: member, RealmID: realm, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}}
	return service, store, e, a, identityfixture.WithActor(t.Context(), actor)
}
func admission() Admission {
	return Admission{Target: Target{Origin: "local", ResourceID: resource, VersionRevisionID: revision, Identifier: "urn:air:catalog.example:demo:agent", VersionPresent: true, Version: "1.0", Kind: catalog.KindAgent}, ReleaseID: release, EntryDigest: "sha256:" + strings.Repeat("a", 64)}
}
func code(t *testing.T, err error, want catalog.ErrorCode) {
	t.Helper()
	if catalog.ErrorCodeOf(err) != want {
		t.Fatalf("error=%v want=%s", err, want)
	}
}
func TestRatingLifecycleConsentAndModeration(t *testing.T) {
	s, store, e, a, ctx := fixture(t)
	v, err := s.Read(ctx, admission())
	if err != nil || v.Count != 0 || v.Average != nil || v.Own != nil {
		t.Fatalf("empty=%+v %v", v, err)
	}
	yes := true
	r, err := s.Submit(ctx, admission(), 5, &yes, 0)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.Submit(ctx, admission(), 1, nil, r.Revision)
	if err != nil || !r.Consent {
		t.Fatalf("edit=%+v %v", r, err)
	}
	v, err = s.Read(ctx, admission())
	if err != nil || v.Count != 1 || v.Average == nil || *v.Average != 1 {
		t.Fatalf("rollup=%+v %v", v, err)
	}
	changed := admission()
	changed.EntryDigest = "sha256:" + strings.Repeat("b", 64)
	v, err = s.Read(ctx, changed)
	if err != nil || !v.ChangedEntry {
		t.Fatal("changed entry context lost", err)
	}
	a.deny = true
	_, err = s.Submit(ctx, admission(), 5, nil, r.Revision)
	code(t, err, catalog.CodeNotFound)
	r, err = s.Change(ctx, r.ID, r.Revision, "withdraw", "")
	if err != nil || r.Consent || r.Score != 0 {
		t.Fatal("visibility loss prevented disposal", err)
	}
	a.deny = false
	r, err = s.Submit(ctx, admission(), 3, nil, r.Revision)
	if err != nil || r.Consent {
		t.Fatal("resubmission inherited withdrawn consent", err)
	}
	actor, _ := identity.FromContext(ctx)
	actor.Roles = []identity.Role{identity.RoleTenantAdmin}
	admin := identityfixture.WithActor(ctx, actor)
	_, err = s.Change(admin, r.ID, r.Revision, "remove", "")
	code(t, err, catalog.CodeInvalidArgument)
	r, err = s.Change(admin, r.ID, r.Revision, "remove", "Abuse report confirmed")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Submit(ctx, admission(), 5, nil, r.Revision)
	code(t, err, catalog.CodeConflict)
	r, err = s.Change(admin, r.ID, r.Revision, "permit", "Allow a new opinion")
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.Submit(ctx, admission(), 4, nil, r.Revision)
	if err != nil || r.Consent {
		t.Fatal(err)
	}
	totals, _ := store.Aggregates(ctx, tenant, []Target{r.Target})
	if totals[Key(r.Target)] != (Aggregate{Count: 1, Sum: 4}) {
		t.Fatal(totals)
	}
	for _, record := range e.records {
		raw, ok := record.Details["facts"].(map[string]any)
		if !ok {
			t.Fatal("missing audit facts")
		}
		if len(raw) != 2 {
			t.Fatal("audit exposed opinion data", raw)
		}
	}
}
func TestRatingStablePrincipalTargetAndIsolation(t *testing.T) {
	s, store, _, _, ctx := fixture(t)
	r, err := s.Submit(ctx, admission(), 2, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := identity.FromContext(ctx)
	actor.MembershipID = release
	recreated := identityfixture.WithActor(ctx, actor)
	_, err = s.Submit(recreated, admission(), 5, nil, 0)
	code(t, err, catalog.CodePreconditionFailed)
	r, err = s.Submit(recreated, admission(), 5, nil, r.Revision)
	if err != nil || r.ID == "" {
		t.Fatal(err)
	}
	actor.Issuer = "https://other.example"
	other := identityfixture.WithActor(ctx, actor)
	view, err := s.Read(other, admission())
	if err != nil || view.Own != nil || view.Count != 1 {
		t.Fatal(view, err)
	}
	_, err = s.Change(other, r.ID, r.Revision, "withdraw", "")
	code(t, err, catalog.CodeNotFound)
	actor.ActorType = identity.ActorService
	machine := identityfixture.WithActor(ctx, actor)
	_, err = s.Submit(machine, admission(), 3, nil, 0)
	code(t, err, catalog.CodeForbidden)
	_, err = store.Get(ctx, resource, r.ID)
	code(t, err, catalog.CodeNotFound)
	changed := admission()
	changed.ReleaseID = member
	if Key(changed.Target) != Key(r.Target) {
		t.Fatal("re-release reset opinion identity")
	}
	changed.Target.VersionRevisionID = member
	if Key(changed.Target) == Key(r.Target) {
		t.Fatal("different immutable revision merged")
	}
}
func TestRatingConcurrentFirstVoteAndAuditFailure(t *testing.T) {
	s, store, e, _, ctx := fixture(t)
	e.fail = true
	_, err := s.Submit(ctx, admission(), 5, nil, 0)
	if err == nil {
		t.Fatal("audit failure accepted")
	}
	values, _ := store.Aggregates(ctx, tenant, []Target{admission().Target})
	if values[Key(admission().Target)].Count != 0 {
		t.Fatal("audit failure changed aggregate")
	}
	e.fail = false
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.Submit(ctx, admission(), 5, nil, 0); results <- err })
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			code(t, err, catalog.CodePreconditionFailed)
		}
	}
	if success != 1 || len(e.records) != 1 {
		t.Fatalf("commits=%d audit=%d", success, len(e.records))
	}
}
func TestRatingBoundsKeysAndRounding(t *testing.T) {
	s, _, _, _, ctx := fixture(t)
	for _, score := range []int{-1, 0, 6, 99} {
		_, err := s.Submit(ctx, admission(), score, nil, 0)
		code(t, err, catalog.CodeInvalidArgument)
	}
	target := Target{Origin: "foreign", SourceID: resource, PresentationGeneration: 1, Identifier: "urn:air:catalog.example:demo:agent", Kind: catalog.KindAgent}
	if err := ValidateTarget(target); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{Key(target): true}
	for _, change := range []func(*Target){func(t *Target) { t.VersionPresent = true }, func(t *Target) { t.SourceID = member }, func(t *Target) { t.PresentationGeneration = 2 }, func(t *Target) { t.Kind = catalog.KindPlugin }} {
		changed := target
		change(&changed)
		if keys[Key(changed)] {
			t.Fatal("foreign target collision")
		}
		keys[Key(changed)] = true
	}
	for _, test := range []struct {
		count, sum int64
		want       float64
	}{{0, 0, 0}, {2, 9, 4.5}, {8, 33, 4.1}, {20, 83, 4.2}} {
		a := Aggregate{Count: test.count, Sum: test.sum}
		if !a.Valid() || a.Average() != test.want {
			t.Fatal(a, a.Average(), test.want)
		}
	}
	if (Aggregate{Count: 1, Sum: 6}).Valid() {
		t.Fatal("invalid score sum")
	}
	if _, err := s.List(ctx, ListInput{Limit: 101}, false); err == nil {
		t.Fatal("unbounded page")
	}
}
func TestRatingSharingRequiresAdminAndReason(t *testing.T) {
	s, _, _, _, ctx := fixture(t)
	_, err := s.SetSharing(ctx, "catalog.example", "anonymous", true, 0, "Share consented summaries")
	code(t, err, catalog.CodeForbidden)
	actor, _ := identity.FromContext(ctx)
	actor.Roles = []identity.Role{identity.RoleTenantAdmin}
	ctx = identityfixture.WithActor(ctx, actor)
	value, err := s.Sharing(ctx, "catalog.example", "anonymous")
	if err != nil || value.Enabled || value.Revision != 0 {
		t.Fatal(value, err)
	}
	value, err = s.SetSharing(ctx, "catalog.example", "anonymous", true, 0, "Share consented summaries")
	if err != nil || !value.Enabled || value.Revision != 1 {
		t.Fatal(value, err)
	}
	_, err = s.SetSharing(ctx, "catalog.example", "anonymous", false, 0, "Stop sharing")
	code(t, err, catalog.CodePreconditionFailed)
	_, err = s.SetSharing(ctx, "catalog.example/path", "anonymous", true, 0, "bad host")
	code(t, err, catalog.CodeInvalidArgument)
}

func TestRatingOwnReadsListsAndSelectedTotals(t *testing.T) {
	s, _, _, _, ctx := fixture(t)
	first, err := s.Submit(ctx, admission(), 3, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondTarget := admission()
	secondTarget.Target.VersionRevisionID = member
	second, err := s.Submit(ctx, secondTarget, 5, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	own, err := s.Get(ctx, first.ID)
	if err != nil || own.Score != 3 {
		t.Fatal("own read", own, err)
	}
	a, _ := identity.FromContext(ctx)
	a.Subject = "bob"
	other := identityfixture.WithActor(ctx, a)
	_, err = s.Get(other, first.ID)
	code(t, err, catalog.CodeNotFound)
	a.Roles = []identity.Role{identity.RoleTenantAdmin}
	admin := identityfixture.WithActor(ctx, a)
	if _, err = s.Get(admin, first.ID); err != nil {
		t.Fatal("moderator read", err)
	}
	for name, bound := range map[string]context.Context{"own": ctx, "other": other, "admin": admin} {
		want := 2
		if name == "other" {
			want = 0
		}
		page, err := s.List(bound, ListInput{Limit: 1, State: "active"}, name == "admin")
		if err != nil || len(page) != want {
			t.Fatal("bounded ownership", page, err)
		}
	}
	lastID := max(first.ID, second.ID)
	page, err := s.List(ctx, ListInput{Limit: 1, AfterID: lastID}, false)
	if err != nil || len(page) != 0 {
		t.Fatal("continuation", page, err)
	}
	_, err = s.List(other, ListInput{Limit: 25}, true)
	code(t, err, catalog.CodeForbidden)
	for _, q := range []ListInput{{Limit: 0}, {Limit: 101}, {Limit: 1, State: "unknown"}, {Limit: 1, AfterID: "bad"}} {
		_, err = s.List(ctx, q, false)
		code(t, err, catalog.CodeInvalidArgument)
	}
	_, err = s.Get(ctx, "bad")
	code(t, err, catalog.CodeInvalidArgument)
	totals, err := s.SelectedSummaries(ctx, []Target{first.Target, second.Target})
	if err != nil || totals[Key(first.Target)].Sum != 3 || totals[Key(second.Target)].Sum != 5 {
		t.Fatal("selected totals", totals, err)
	}
	_, err = s.SelectedSummaries(ctx, make([]Target, 101))
	code(t, err, catalog.CodeInvalidArgument)
	anonymous := context.Background()
	_, err = s.Get(anonymous, first.ID)
	code(t, err, catalog.CodeUnauthenticated)
	_, err = s.List(anonymous, ListInput{Limit: 25}, false)
	code(t, err, catalog.CodeUnauthenticated)
	_, err = s.Read(anonymous, admission())
	code(t, err, catalog.CodeUnauthenticated)
	_, err = s.Submit(anonymous, admission(), 3, nil, 0)
	code(t, err, catalog.CodeUnauthenticated)
	_, err = s.Change(anonymous, first.ID, first.Revision, "withdraw", "")
	code(t, err, catalog.CodeUnauthenticated)
	_, err = s.SelectedSummaries(anonymous, nil)
	code(t, err, catalog.CodeUnauthenticated)
}
func TestRatingPersistenceRejectsIdentityLifecycleAndAuditChanges(t *testing.T) {
	s, _, e, _, ctx := fixture(t)
	old, err := s.Submit(ctx, admission(), 4, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Submit(ctx, admission(), 2, nil, old.Revision)
	if err != nil {
		t.Fatal(err)
	}
	valid := e.records[1]
	if err := ValidateSave(old, current, old.Revision, valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Record, *audit.Record){
		"identity":           func(v *Record, _ *audit.Record) { v.ID = member },
		"principal":          func(v *Record, _ *audit.Record) { v.PrincipalKey = "bad" },
		"target":             func(v *Record, _ *audit.Record) { v.Target.Kind = "unknown" },
		"active zero":        func(v *Record, _ *audit.Record) { v.Score = 0 },
		"inactive score":     func(v *Record, _ *audit.Record) { v.State = "withdrawn" },
		"backdated":          func(v *Record, _ *audit.Record) { v.UpdatedAt = old.UpdatedAt.Add(-time.Second) },
		"creation rewritten": func(v *Record, _ *audit.Record) { v.CreatedAt = old.CreatedAt.Add(-time.Second) },
		"unknown audit":      func(_ *Record, a *audit.Record) { a.Action = "rating.unknown" },
		"wrong target":       func(_ *Record, a *audit.Record) { a.TargetID = member },
		"false transition":   func(v *Record, a *audit.Record) { a.Action = "rating.withdraw"; v.State = "active" },
		"different facts": func(_ *Record, a *audit.Record) {
			a.Details = map[string]any{"facts": map[string]any{"revision": 99, "state": "active"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			v, a := current, valid
			mutate(&v, &a)
			if ValidateSave(old, v, old.Revision, a) == nil {
				t.Fatal("invalid retained write accepted")
			}
		})
	}
	if ValidateSave(old, current, 0, valid) == nil {
		t.Fatal("stale expected revision accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.Get(cancelled, current.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}
func TestRatingTargetJSONRequiresExplicitClosedIdentity(t *testing.T) {
	target := admission().Target
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var got Target
	if json.Unmarshal(raw, &got) != nil || got != target {
		t.Fatal("target round trip")
	}
	for _, bad := range []string{`null`, `[]`, `{`, strings.Replace(string(raw), `"versionPresent":true,`, "", 1), strings.Replace(string(raw), `"origin":"local"`, `"Origin":"local"`, 1), strings.Replace(string(raw), `"kind":"agent"`, `"kind":"agent","private":true`, 1), strings.Replace(string(raw), `"versionPresent":true`, `"versionPresent":null`, 1)} {
		if json.Unmarshal([]byte(bad), &got) == nil {
			t.Fatal("invalid target accepted", bad)
		}
	}
}

type readFailureStore struct {
	Store
	failure string
}

func (s readFailureStore) Own(ctx context.Context, tenant string, target Target, owner string) (Record, error) {
	if s.failure == "own" {
		return Record{}, errors.New("private rating read unavailable")
	}
	return s.Store.Own(ctx, tenant, target, owner)
}
func (s readFailureStore) Aggregates(ctx context.Context, tenant string, targets []Target) (map[string]Aggregate, error) {
	if s.failure == "aggregate" {
		return nil, errors.New("aggregate unavailable")
	}
	if s.failure == "corrupt" {
		return map[string]Aggregate{Key(targets[0]): {Count: 1, Sum: 6}}, nil
	}
	return s.Store.Aggregates(ctx, tenant, targets)
}

type changedAuthority struct{}

func (changedAuthority) Admit(_ context.Context, a Admission) (Admission, error) {
	a.Target.Version = "changed"
	return a, nil
}
func TestRatingReadsRejectIncompleteOrIncoherentState(t *testing.T) {
	for _, failure := range []string{"own", "aggregate", "corrupt"} {
		t.Run(failure, func(t *testing.T) {
			s, store, _, _, ctx := fixture(t)
			s.store = readFailureStore{Store: store, failure: failure}
			if _, err := s.Read(ctx, admission()); err == nil {
				t.Fatal("failed dependency admitted as empty opinion")
			}
			if failure == "own" {
				if _, err := s.Submit(ctx, admission(), 4, nil, 0); err == nil {
					t.Fatal("lookup failure treated as absent vote")
				}
			}
		})
	}
	s, _, _, _, ctx := fixture(t)
	s.authority = changedAuthority{}
	if _, err := s.Read(ctx, admission()); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal(err)
	}
	s, _, _, _, ctx = fixture(t)
	if _, err := s.Change(ctx, resource, 1, "withdraw", ""); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal(err)
	}
	r, err := s.Submit(ctx, admission(), 3, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := identity.FromContext(ctx)
	actor.Subject = "other"
	if _, err := s.Change(identityfixture.WithActor(ctx, actor), r.ID, r.Revision, "withdraw", ""); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal(err)
	}
	if _, err := s.Change(ctx, r.ID, r.Revision, "permit", "Cannot permit active score"); err == nil {
		t.Fatal("ordinary owner permitted own score")
	}
	for _, action := range []string{"unknown", "withdraw"} {
		if _, err := s.Change(ctx, "bad", 0, action, ""); err == nil {
			t.Fatal("invalid command")
		}
	}
}

func TestRatingPersistenceRejectsImpossibleLifecycleAndPrivateAuditFacts(t *testing.T) {
	s, _, events, _, ctx := fixture(t)
	old, err := s.Submit(ctx, admission(), 3, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Submit(ctx, admission(), 4, nil, old.Revision)
	if err != nil {
		t.Fatal(err)
	}
	evidence := events.records[1]
	for name, change := range map[string]func(*Record, *Record, *audit.Record){
		"removed submission": func(old, _ *Record, _ *audit.Record) { old.State = "removed" },
		"rewritten context": func(_, v *Record, e *audit.Record) {
			v.ReleaseID = member
			e.Action = "rating.withdraw"
			v.State = "withdrawn"
			v.Score = 0
		},
		"inactive withdrawal": func(old, v *Record, e *audit.Record) {
			old.State = "withdrawn"
			v.State = "withdrawn"
			v.Score = 0
			e.Action = "rating.withdraw"
		},
		"repeat removal": func(old, v *Record, e *audit.Record) {
			old.State = "removed"
			v.State = "removed"
			v.Score = 0
			e.Action = "rating.remove"
		},
		"premature permission": func(_, v *Record, e *audit.Record) { v.State = "withdrawn"; v.Score = 0; e.Action = "rating.permit" },
		"non-JSON facts":       func(_, _ *Record, e *audit.Record) { e.Details = map[string]any{"facts": func() {}} },
	} {
		t.Run(name, func(t *testing.T) {
			before, after, a := old, current, evidence
			change(&before, &after, &a)
			if ValidateSave(before, after, old.Revision, a) == nil {
				t.Fatal("invalid lifecycle retained")
			}
		})
	}
	creation := events.records[0]
	first := old
	first.UpdatedAt = first.UpdatedAt.Add(time.Second)
	creation.At = first.UpdatedAt
	if ValidateSave(Record{}, first, 0, creation) == nil {
		t.Fatal("creation times disagree")
	}
	if _, err := s.Change(ctx, old.ID, current.Revision, "unknown", ""); err == nil {
		t.Fatal("unknown action")
	}
	if _, err := s.Change(ctx, old.ID, old.Revision, "withdraw", ""); err == nil {
		t.Fatal("stale action")
	}
}
