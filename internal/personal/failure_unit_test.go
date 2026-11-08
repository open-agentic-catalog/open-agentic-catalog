package personal

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

type failureCountingStore struct {
	Store
	reads, lists, writes int
}

func (s *failureCountingStore) Get(ctx context.Context, actor identity.Actor, id string) (Item, error) {
	s.reads++
	return s.Store.Get(ctx, actor, id)
}

func (s *failureCountingStore) List(ctx context.Context, actor identity.Actor, q ListRequest) ([]Item, error) {
	s.lists++
	return s.Store.List(ctx, actor, q)
}

func (s *failureCountingStore) Save(ctx context.Context, actor identity.Actor, item Item, expected int64, remove bool, evidence audit.Record) (Item, error) {
	s.writes++
	return s.Store.Save(ctx, actor, item, expected, remove, evidence)
}

type failureIDs struct {
	calls, failAt int
	err           error
}

func (ids *failureIDs) New(time.Time) (string, error) {
	ids.calls++
	if ids.calls == ids.failAt {
		return "", ids.err
	}
	return []string{"00000000-0000-7000-8000-000000000004", "00000000-0000-7000-8000-000000000005"}[ids.calls-1], nil
}

func exactLibraryActor(t *testing.T, template string) context.Context {
	t.Helper()
	policy, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenant, Issuer: "https://issuer.example.com", Subject: "current-human", ActorType: identity.ActorHuman, RoleTemplates: []string{template}}})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := policy.ResolvePrincipal(t.Context(), identity.Principal{TenantID: tenant, Issuer: "https://issuer.example.com", Subject: "current-human"})
	if err != nil || actor.ActorType != identity.ActorHuman || !identity.ValidTenantID(actor.MembershipID) || !identity.ValidTenantID(actor.RealmID) {
		t.Fatal("fixture must resolve a current human membership", actor, err)
	}
	return identity.WithActor(t.Context(), actor)
}

func TestCurrentHumanWithoutLibraryPermissionDoesNoLibraryWork(t *testing.T) {
	allowed := exactLibraryActor(t, "library_member")
	denied := exactLibraryActor(t, "reader")
	s, memory, evidence, admission, _ := fixture(t)
	store := &failureCountingStore{Store: memory}
	ids := &failureIDs{}
	s.store, s.ids = store, ids
	item, err := s.Save(allowed, "", 0, searchInput(), false)
	if err != nil {
		t.Fatal("library permission permits the same human to save", err)
	}
	if _, err := s.Get(allowed, item.ID); err != nil {
		t.Fatal("library permission permits reads", err)
	}
	if page, err := s.List(allowed, ListRequest{Limit: 1}); err != nil || len(page) != 1 {
		t.Fatal("library permission permits listing", page, err)
	}
	store.reads, store.lists, store.writes, ids.calls = 0, 0, 0, 0
	for name, operation := range map[string]func() error{
		"read":   func() error { _, err := s.Get(denied, item.ID); return err },
		"list":   func() error { _, err := s.List(denied, ListRequest{Limit: 1}); return err },
		"create": func() error { _, err := s.Save(denied, "", 0, searchInput(), false); return err },
		"update": func() error { _, err := s.Save(denied, item.ID, item.Revision, searchInput(), false); return err },
		"remove": func() error { _, err := s.Save(denied, item.ID, item.Revision, Input{}, true); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
				t.Fatal("current human without library.own was not refused", err)
			}
			if store.reads != 0 || store.lists != 0 || store.writes != 0 || ids.calls != 0 || admission.calls != 0 || len(evidence.records) != 1 {
				t.Fatal("permission refusal reached storage, allocation, resource admission or audit", store, ids.calls, admission.calls, len(evidence.records))
			}
		})
	}
}

func TestIdentifierAllocationFailureDoesNotRetainItemOrEvidence(t *testing.T) {
	for _, failure := range []struct {
		name string
		at   int
	}{{"item", 1}, {"audit evidence", 2}} {
		t.Run(failure.name, func(t *testing.T) {
			s, memory, evidence, _, ctx := fixture(t)
			ids := &failureIDs{}
			store := &failureCountingStore{Store: memory}
			s.store, s.ids = store, ids
			item, err := s.Save(ctx, "", 0, searchInput(), false)
			if err != nil || item.ID != "00000000-0000-7000-8000-000000000004" || item.Revision != 1 || ids.calls != 2 || store.writes != 1 || len(evidence.records) != 1 {
				t.Fatal("valid allocation control must save exactly one item and audit record", item, err)
			}

			s, memory, evidence, _, ctx = fixture(t)
			cause := errors.New("synthetic identifier source unavailable")
			ids = &failureIDs{failAt: failure.at, err: cause}
			store = &failureCountingStore{Store: memory}
			s.store, s.ids = store, ids
			got, err := s.Save(ctx, "", 0, searchInput(), false)
			if catalog.ErrorCodeOf(err) != catalog.CodeInternal || !errors.Is(err, cause) || !reflect.DeepEqual(got, Item{}) {
				t.Fatal("identifier failure must retain its classified cause and return no item", got, err)
			}
			actor, _ := identity.FromContext(ctx)
			items, listErr := memory.List(ctx, actor, ListRequest{Limit: 1})
			if listErr != nil || len(items) != 0 || store.writes != 0 || len(evidence.records) != 0 || ids.calls != failure.at {
				t.Fatal("allocation failure wrote an item or evidence", items, listErr, store.writes, len(evidence.records), ids.calls)
			}
		})
	}
}

func TestDirectSavedItemRevisionAndTimeTransitions(t *testing.T) {
	s, _, evidence, _, ctx := fixture(t)
	first, err := s.Save(ctx, "", 0, searchInput(), false)
	if err != nil {
		t.Fatal(err)
	}
	fakeClock, ok := s.clock.(*clock.Fake)
	if !ok {
		t.Fatal("fixture must supply the injected fake clock")
	}
	fakeClock.Advance(time.Second)
	old, err := s.Save(ctx, first.ID, first.Revision, searchInput(), false)
	if err != nil {
		t.Fatal(err)
	}
	fakeClock.Advance(time.Second)
	item, err := s.Save(ctx, old.ID, old.Revision, searchInput(), false)
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := identity.FromContext(ctx)
	record := evidence.records[2]
	if err := ValidateSave(old, item, old.Revision, false, record, actor); err != nil {
		t.Fatal("unchanged update is valid", err)
	}
	equalTime, equalEvidence := item, record
	equalTime.UpdatedAt, equalEvidence.At = old.UpdatedAt, old.UpdatedAt
	if err := ValidateSave(old, equalTime, old.Revision, false, equalEvidence, actor); err != nil {
		t.Fatal("equal retained update time is allowed", err)
	}
	for name, change := range map[string]func(*Item){
		"revision skipped":              func(i *Item) { i.Revision++ },
		"identity changed":              func(i *Item) { i.ID = realm },
		"creation changed":              func(i *Item) { i.CreatedAt = i.CreatedAt.Add(time.Microsecond) },
		"update before retained update": func(i *Item) { i.UpdatedAt = old.UpdatedAt.Add(-time.Microsecond) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := item
			change(&candidate)
			if err := ValidateSave(old, candidate, old.Revision, false, record, actor); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
				t.Fatal("invalid saved transition was not classified as a conflict", err)
			}
		})
	}
	if err := ValidateSave(old, item, old.Revision-1, false, record, actor); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("stale expected revision must fail its precondition", err)
	}
	if err := ValidateSave(Item{}, first, 0, false, evidence.records[0], actor); err != nil {
		t.Fatal("valid creation control", err)
	}
	for name, change := range map[string]func(*Item){
		"creation absent":        func(i *Item) { i.CreatedAt = time.Time{} },
		"update before creation": func(i *Item) { i.UpdatedAt = i.CreatedAt.Add(-time.Microsecond) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate, record := first, evidence.records[0]
			change(&candidate)
			record.At = candidate.UpdatedAt
			if err := ValidateSave(Item{}, candidate, 0, false, record, actor); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
				t.Fatal("invalid creation time was not classified as a conflict", err)
			}
		})
	}
	if err := ValidateSave(Item{}, first, 0, true, evidence.records[0], actor); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("an absent item cannot be removed", err)
	}
}
