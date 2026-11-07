package catalog

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// These doubles only return a current policy revision and record selection.
// The nil embedded ports fail if admission unexpectedly invokes another path.
type personalWorkAdmissionAuthority struct {
	identity.PermissionStore
	err error
}

func (a personalWorkAdmissionAuthority) CheckPermissions(_ context.Context, _ identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	return identity.PermissionDecision{Revision: 23, Allowed: make([]bool, len(checks))}, a.err
}

type personalWorkAdmissionRepository struct {
	Repository
	observe func(context.Context)
	tenant  string
	query   PersonalWorkRequest
	calls   int
	items   []PersonalWorkItem
	err     error
}

func (r *personalWorkAdmissionRepository) ListPersonalWork(ctx context.Context, tenant string, query PersonalWorkRequest) ([]PersonalWorkItem, error) {
	r.calls++
	r.tenant, r.query = tenant, query
	if r.observe != nil {
		r.observe(ctx)
	}
	return r.items, r.err
}

func personalWorkAdmissionActor() identity.Actor {
	return identity.Actor{
		TenantID:     "00000000-0000-7000-8000-000000000051",
		MembershipID: "00000000-0000-7000-8000-000000000052",
		RealmID:      "00000000-0000-7000-8000-000000000053",
		Subject:      "personal-work-owner",
		ActorType:    identity.ActorHuman,
	}
}

type personalWorkContextKey struct{}

func TestPersonalWorkAdmissionForwardsBoundedSelection(t *testing.T) {
	t.Parallel()
	for _, trial := range []struct {
		queue, afterID, itemType, state string
		limit, readLimit                int
	}{
		{queue: "drafts", limit: 1, readLimit: 2, itemType: "resource", state: "empty_resource"},
		{queue: "submitted", afterID: "submission:previous", limit: 20, readLimit: 21, itemType: "submission", state: "submitted"},
		{queue: "changes_required", afterID: strings.Repeat("x", 80), limit: 100, readLimit: 101, itemType: "submission", state: "changes_required"},
		{queue: "approved", limit: 1, readLimit: 2, itemType: "submission", state: "approved"},
	} {
		t.Run(trial.queue, func(t *testing.T) {
			actor := identity.BindPermissionStore(personalWorkAdmissionActor(), personalWorkAdmissionAuthority{})
			ctx, cancel := context.WithCancel(context.WithValue(identity.WithActor(t.Context(), actor), personalWorkContextKey{}, "request-owned value"))
			defer cancel()
			want := []PersonalWorkItem{
				{ID: "work-first", Type: trial.itemType, ResourceID: "00000000-0000-7000-8000-000000000054", NamespaceID: "00000000-0000-7000-8000-000000000055", Kind: KindAgent, State: trial.state},
				{ID: "work-second", Type: trial.itemType, ResourceID: "00000000-0000-7000-8000-000000000056", NamespaceID: "00000000-0000-7000-8000-000000000055", Kind: KindSkill, State: trial.state},
			}
			var done <-chan struct{}
			repo := &personalWorkAdmissionRepository{items: want, observe: func(bound context.Context) {
				forwarded, present := identity.FromContext(bound)
				revision, selected := identity.CollectionRevision(bound)
				if !present || !reflect.DeepEqual(forwarded, actor) || !selected || revision != 23 || bound.Value(personalWorkContextKey{}) != "request-owned value" {
					t.Fatal("selection lost the caller or current-policy collection context")
				}
				done = bound.Done()
			}}
			service := &Service{repo: repo}
			got, err := service.ListPersonalWork(ctx, PersonalWorkRequest{Queue: trial.queue, AfterID: trial.afterID, Limit: trial.limit})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("selected rows=%+v, error=%v", got, err)
			}
			if repo.calls != 1 || repo.tenant != actor.TenantID || repo.query != (PersonalWorkRequest{Queue: trial.queue, AfterID: trial.afterID, Limit: trial.readLimit}) {
				t.Fatalf("selection lost tenant, queue, position or lookahead bound: calls=%d tenant=%q query=%+v", repo.calls, repo.tenant, repo.query)
			}
			cancel()
			select {
			case <-done:
			default:
				t.Fatal("selection detached request cancellation")
			}
		})
	}
}

func TestPersonalWorkAdmissionRejectsIdentityBeforeRead(t *testing.T) {
	t.Parallel()
	policyFailure := identity.E(identity.CodeDependency, "current policy unavailable")
	for _, trial := range []struct {
		name                 string
		change               func(*identity.Actor)
		noActor, noAuthority bool
		authorityError       error
		want                 ErrorCode
	}{
		{name: "absent principal", noActor: true, want: CodeForbidden},
		{name: "unbound authority", noAuthority: true, want: CodeForbidden},
		{name: "service principal", change: func(a *identity.Actor) { a.ActorType = identity.ActorService }, want: CodeForbidden},
		{name: "missing actor type", change: func(a *identity.Actor) { a.ActorType = "" }, want: CodeForbidden},
		{name: "missing membership", change: func(a *identity.Actor) { a.MembershipID = "" }, want: CodeForbidden},
		{name: "malformed membership", change: func(a *identity.Actor) { a.MembershipID = "not-a-uuid" }, want: CodeForbidden},
		{name: "missing realm", change: func(a *identity.Actor) { a.RealmID = "" }, want: CodeForbidden},
		{name: "malformed realm", change: func(a *identity.Actor) { a.RealmID = "not-a-uuid" }, want: CodeForbidden},
		{name: "unavailable current authority", authorityError: policyFailure, want: CodeDependencyUnavailable},
	} {
		t.Run(trial.name, func(t *testing.T) {
			actor := personalWorkAdmissionActor()
			if trial.change != nil {
				trial.change(&actor)
			}
			ctx := t.Context()
			if !trial.noActor {
				if !trial.noAuthority {
					// Return current authority even for malformed principals so
					// the Catalog's own human-membership guard must refuse them.
					actor = identity.BindPermissionStore(actor, personalWorkAdmissionAuthority{err: trial.authorityError})
				}
				ctx = identity.WithActor(ctx, actor)
			}
			repo := &personalWorkAdmissionRepository{items: []PersonalWorkItem{{ID: "must-not-be-disclosed"}}}
			got, err := (&Service{repo: repo}).ListPersonalWork(ctx, PersonalWorkRequest{Queue: "drafts", Limit: 20})
			if ErrorCodeOf(err) != trial.want || got != nil || repo.calls != 0 {
				t.Fatalf("identity refusal: rows=%+v error=%v reads=%d", got, err, repo.calls)
			}
			if trial.authorityError != nil && !errors.Is(err, trial.authorityError) {
				t.Fatal("authority failure was not preserved", err)
			}
		})
	}
}

func TestPersonalWorkAdmissionRejectsPagingBeforeRead(t *testing.T) {
	t.Parallel()
	for _, trial := range []struct {
		name  string
		query PersonalWorkRequest
	}{
		{name: "missing queue", query: PersonalWorkRequest{Limit: 20}},
		{name: "unknown queue", query: PersonalWorkRequest{Queue: "unknown", Limit: 20}},
		{name: "queue spelling", query: PersonalWorkRequest{Queue: "DRAFTS", Limit: 20}},
		{name: "missing page size", query: PersonalWorkRequest{Queue: "drafts"}},
		{name: "negative page size", query: PersonalWorkRequest{Queue: "drafts", Limit: -1}},
		{name: "excessive page size", query: PersonalWorkRequest{Queue: "drafts", Limit: 101}},
		{name: "oversized position", query: PersonalWorkRequest{Queue: "drafts", Limit: 20, AfterID: strings.Repeat("x", 81)}},
		{name: "NUL in position", query: PersonalWorkRequest{Queue: "drafts", Limit: 20, AfterID: "work\x00position"}},
	} {
		t.Run(trial.name, func(t *testing.T) {
			actor := identity.BindPermissionStore(personalWorkAdmissionActor(), personalWorkAdmissionAuthority{})
			repo := &personalWorkAdmissionRepository{items: []PersonalWorkItem{{ID: "must-not-be-disclosed"}}}
			got, err := (&Service{repo: repo}).ListPersonalWork(identity.WithActor(t.Context(), actor), trial.query)
			if ErrorCodeOf(err) != CodeInvalidArgument || got != nil || repo.calls != 0 {
				t.Fatalf("page refusal: rows=%+v error=%v reads=%d", got, err, repo.calls)
			}
		})
	}
}

func TestPersonalWorkAdmissionRequiresOptionalReader(t *testing.T) {
	t.Parallel()
	actor := identity.BindPermissionStore(personalWorkAdmissionActor(), personalWorkAdmissionAuthority{})
	// Repository alone deliberately has no PersonalWorkReader implementation.
	service := &Service{repo: struct{ Repository }{}}
	got, err := service.ListPersonalWork(identity.WithActor(t.Context(), actor), PersonalWorkRequest{Queue: "drafts", Limit: 20})
	if ErrorCodeOf(err) != CodeDependencyUnavailable || got != nil {
		t.Fatalf("missing reader: rows=%+v error=%v", got, err)
	}
}

func TestPersonalWorkAdmissionPreservesRepositoryFailure(t *testing.T) {
	t.Parallel()
	cause := errors.New("owned repository failure")
	failure := Wrap(CodeDependencyUnavailable, "personal selection unavailable", cause)
	actor := identity.BindPermissionStore(personalWorkAdmissionActor(), personalWorkAdmissionAuthority{})
	repo := &personalWorkAdmissionRepository{err: failure}
	got, err := (&Service{repo: repo}).ListPersonalWork(identity.WithActor(t.Context(), actor), PersonalWorkRequest{Queue: "submitted", Limit: 20})
	if !errors.Is(err, failure) || !errors.Is(err, cause) || ErrorCodeOf(err) != CodeDependencyUnavailable || got != nil || repo.calls != 1 {
		t.Fatalf("repository failure: rows=%+v error=%v reads=%d", got, err, repo.calls)
	}
}

func TestSavedSearchAdmissionExcludesTransientPaging(t *testing.T) {
	t.Parallel()
	criteria := SearchQuery{
		Order: SearchOrderPublisherAsc, Text: "  équipe,工具  ", Origins: []SearchResultType{SearchResultLocal},
		NamespaceID: "00000000-0000-7000-8000-000000000055", Kinds: []Kind{KindAgent, KindSkill},
		Tags: []string{"literal,tag", "工具"}, Categories: []string{"native.agent"}, License: "Apache-2.0",
		Publisher: "00000000-0000-7000-8000-000000000057", Visibility: VisibilityTenant,
	}
	for _, query := range []SearchQuery{{}, criteria, {Text: "live criteria", Origins: []SearchResultType{SearchResultLive}}} {
		if err := ValidateSavedSearch(query); err != nil {
			t.Fatal("valid saved criteria refused", err)
		}
	}
	for _, trial := range []struct {
		name   string
		cursor string
		limit  int
	}{
		{name: "cursor", cursor: "opaque-continuation"},
		{name: "positive page size", limit: 1},
		{name: "maximum page size", limit: 100},
		{name: "negative page size", limit: -1},
	} {
		t.Run(trial.name, func(t *testing.T) {
			query := criteria
			query.Cursor, query.Limit = trial.cursor, trial.limit
			if err := ValidateSavedSearch(query); ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatal("saved criteria admitted transient paging", err)
			}
		})
	}
}
