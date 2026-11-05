package catalog

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestHybridLiveTailPreservesPreciseOrderAndWholeSetWinners(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	clock, ok := s.clock.(*mutableCatalogSearchClock)
	if !ok {
		t.Fatal("fixture clock is not mutable")
	}
	actor.Roles = []identity.Role{identity.RoleTenantAdmin}
	durable := hybridDurablePage().Items
	// This is a semantic-only match outside the current durable page. Its
	// live duplicate has a higher display score and must still lose.
	live := []CatalogSearchResult{
		hybridLiveResult(durable[1].Result.Identifier, "1", true, 100),
		hybridLiveResult("urn:air:peer.example:agent:first", "", false, 100),
		hybridLiveResult("urn:air:peer.example:agent:second", "", true, 99),
	}
	f.searchHook = func(r SemanticSearchRequest) (SemanticSearchPage, error) {
		if r.IdentityProbes != nil {
			if len(r.IdentityProbes) != 3 || r.After != nil || r.Generation != f.g || !slices.Equal(r.Vector, f.vector) {
				t.Fatalf("unfenced hybrid probes: %+v", r)
			}
			return SemanticSearchPage{Matches: []IndexedIdentity{{durable[1].Result.Identifier, "1", true}}}, nil
		}
		start := 0
		if r.After != nil {
			for i, item := range durable {
				if item.Position == *r.After {
					start = i + 1
				}
			}
		}
		end := min(start+r.Query.Limit, len(durable))
		return SemanticSearchPage{Items: durable[start:end], Total: 2, HasMore: end < len(durable)}, nil
	}
	fp := strings.Repeat("a", 64)
	calls := 0
	var liveError error
	service := &SearchService{local: s, federated: snapshotLiveSearcher{live: func(_ context.Context, r FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		calls++
		if r.LiveLimit != 50 {
			t.Fatal("live traversal is not bounded")
		}
		return FederatedCatalogSearchPage{Live: live, ReplayFingerprint: fp, Partial: true, Diagnostics: []CatalogSearchDiagnostic{{Status: "continuation_unavailable"}}}, liveError
	}}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	query := SearchQuery{Text: "find", Limit: 1}
	want := []string{"local:release-a", durable[1].Result.ID, live[1].ID, live[2].ID}
	expiry := s.clock.Now().Add(15 * time.Minute).Unix()
	for i, id := range want {
		page, err := service.Search(ctx, query)
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != id || page.DurableTotal != 2 || page.LiveCount != map[bool]int{false: 0, true: 1}[i >= 2] || page.Tier != "hybrid" || !page.Partial {
			t.Fatalf("page %d: %+v %v", i, page, err)
		}
		if i == len(want)-1 {
			if page.NextCursor != "" {
				t.Fatal("extra terminal cursor")
			}
			break
		}
		bindingCtx := identity.WithPermissionContext(ctx)
		if _, err := identity.CurrentDecisionDigest(bindingCtx); err != nil {
			t.Fatal(err)
		}
		binding, _ := s.unifiedSearchCursorBinding(bindingCtx, actor, query)
		state, err := DecodeLiveCursor(page.NextCursor, "control", s.cursorKey, binding, s.clock.Now())
		if err != nil || state.ExpiresAt != expiry || state.Mode != "hybrid" {
			t.Fatalf("flat fixed-expiry cursor: %+v %v", state, err)
		}
		query.Cursor = page.NextCursor
		if i == 1 {
			liveError = errors.New("private peer fault")
			if failed, err := service.Search(ctx, query); ErrorCodeOf(err) != CodeDependencyUnavailable || failed.NextCursor != "" || len(failed.Items) != 0 {
				t.Fatalf("outage advanced cursor: %+v %v", failed, err)
			}
			liveError = nil
			fp = strings.Repeat("b", 64)
			if _, err := service.Search(ctx, query); ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatalf("changed replay: %v", err)
			}
			fp = strings.Repeat("a", 64)
		}
		clock.now = clock.Now().Add(time.Second)
	}
	if calls != 6 {
		t.Fatalf("unexpected replay calls: %d", calls)
	}
}

func TestHybridEmptyDurablePopulationStillContinuesLiveTail(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	actor.Roles = []identity.Role{identity.RoleTenantAdmin}
	f.page = SemanticSearchPage{}
	items := []CatalogSearchResult{hybridLiveResult("urn:air:peer.example:agent:a", "", false, 90), hybridLiveResult("urn:air:peer.example:agent:b", "", false, 80)}
	service := &SearchService{local: s, federated: snapshotLiveSearcher{live: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		return FederatedCatalogSearchPage{Live: items, ReplayFingerprint: strings.Repeat("a", 64)}, nil
	}}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	query := SearchQuery{Text: "find", Limit: 1}
	first, err := service.Search(ctx, query)
	if err != nil || first.NextCursor == "" || first.LiveCount != 1 || first.DurableTotal != 0 {
		t.Fatalf("empty durable: %+v %v", first, err)
	}
	query.Cursor = first.NextCursor
	second, err := service.Search(ctx, query)
	if err != nil || second.NextCursor != "" || len(second.Items) != 1 || second.Items[0].ID != items[1].ID {
		t.Fatalf("tail: %+v %v", second, err)
	}
	f.vector = []float32{0, 1}
	if _, err := service.Search(ctx, query); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("embedding drift: %v", err)
	}
	f.vector = []float32{1, 0}
	s.semantic.options.RankingProfile = "sha256:" + strings.Repeat("c", 64)
	if _, err := service.Search(ctx, query); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("ranking drift: %v", err)
	}
}

func TestLiveOnlyWindowPagesWithoutDurableOrEmbeddingWork(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	actor.Roles = []identity.Role{identity.RoleTenantAdmin}
	f.providerError, f.storeError = errors.New("must not infer"), errors.New("must not read")
	items := []CatalogSearchResult{hybridLiveResult("urn:air:peer.example:agent:a", "", false, 90), hybridLiveResult("urn:air:peer.example:agent:b", "", false, 80)}
	fp := strings.Repeat("a", 64)
	service := &SearchService{local: s, federated: snapshotLiveSearcher{live: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		return FederatedCatalogSearchPage{Live: items, ReplayFingerprint: fp}, nil
	}}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	query := SearchQuery{Text: "find", Origins: []SearchResultType{SearchResultLive}, Limit: 1}
	first, err := service.Search(ctx, query)
	if err != nil || first.NextCursor == "" || first.DurableTotal != 0 || len(first.Items) != 1 || first.Items[0].ID != items[0].ID {
		t.Fatalf("first: %+v %v", first, err)
	}
	query.Cursor = first.NextCursor
	fp = ""
	if _, err := service.Search(ctx, query); ErrorCodeOf(err) != CodeDependencyUnavailable {
		t.Fatalf("incomplete replay: %v", err)
	}
	fp = strings.Repeat("b", 64)
	if _, err := service.Search(ctx, query); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("changed replay: %v", err)
	}
	fp = strings.Repeat("a", 64)
	last, err := service.Search(ctx, query)
	if err != nil || last.NextCursor != "" || last.LiveCount != 1 || last.Items[0].ID != items[1].ID {
		t.Fatalf("last: %+v %v", last, err)
	}
	if f.queries != 0 || f.searches != 0 || f.activeCalls != 0 || f.probes != 0 {
		t.Fatal("live-only accessed durable/embedding ports")
	}
	query.Text = "changed"
	if _, err := service.Search(ctx, query); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("query binding: %v", err)
	}
}

func TestHybridLiveIdentityProbeFailuresPreserveTheCorrectBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault error
		code  ErrorCode
	}{
		{"generation", ErrSearchGenerationChanged, CodeInvalidArgument},
		{"authorization", E(CodeForbidden, "refused"), CodeForbidden},
		{"authentication", E(CodeUnauthenticated, "refused"), CodeUnauthenticated},
		{"invalid", E(CodeInvalidArgument, "changed"), CodeInvalidArgument},
		{"dependency", errors.New("private SQL fault"), CodeDependencyUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			actor.Roles = []identity.Role{identity.RoleTenantAdmin}
			durable := f.page
			durable.Total = len(durable.Items) + 1
			durable.HasMore = true
			var fault error
			f.searchHook = func(r SemanticSearchRequest) (SemanticSearchPage, error) {
				if r.IdentityProbes != nil {
					return SemanticSearchPage{}, fault
				}
				return durable, nil
			}
			item := hybridLiveResult("urn:air:peer.example:agent:tail", "", false, 90)
			service := &SearchService{local: s, federated: snapshotLiveSearcher{live: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
				return FederatedCatalogSearchPage{Live: []CatalogSearchResult{item}, ReplayFingerprint: strings.Repeat("a", 64)}, nil
			}}}
			ctx := identityfixture.WithActor(t.Context(), actor)
			query := SearchQuery{Text: "find", Limit: 2}
			first, err := service.Search(ctx, query)
			if err != nil || !strings.HasPrefix(first.NextCursor, LiveCursorPrefix) {
				t.Fatalf("healthy control: %+v %v", first, err)
			}
			fault = tc.fault
			query.Cursor = first.NextCursor
			if page, err := service.Search(ctx, query); ErrorCodeOf(err) != tc.code || len(page.Items) != 0 || page.NextCursor != "" {
				t.Fatalf("probe failure advanced continuation: %+v %v", page, err)
			}
			query.Cursor = ""
			page, err := service.Search(ctx, query)
			if tc.name == "dependency" {
				if err != nil || len(page.Items) != len(durable.Items) || !page.Partial || page.LiveCount != 0 || !strings.HasPrefix(page.NextCursor, "h1.") {
					t.Fatalf("healthy durable first page lost: %+v %v", page, err)
				}
			} else if ErrorCodeOf(err) != tc.code {
				t.Fatalf("first-page fault misclassified: %v", err)
			}
			fault = nil
			query.Cursor = first.NextCursor
			if _, err := service.Search(ctx, query); err != nil {
				t.Fatalf("same-token recovery: %v", err)
			}
		})
	}
}

func TestHybridLiveContinuationClassifiesDurableReadFailures(t *testing.T) {
	for _, stage := range []string{"generation", "page"} {
		for _, tc := range []struct {
			name  string
			fault error
			code  ErrorCode
		}{
			{"dependency", errors.New("private database failure"), CodeDependencyUnavailable},
			{"transaction conflict", E(CodeConflict, "read transaction changed"), CodeDependencyUnavailable},
			{"stale generation", Wrap(CodeConflict, "generation changed", ErrSearchGenerationChanged), CodeInvalidArgument},
			{"authorization", E(CodeForbidden, "refused"), CodeForbidden},
			{"authentication", E(CodeUnauthenticated, "refused"), CodeUnauthenticated},
			{"invalid", E(CodeInvalidArgument, "changed"), CodeInvalidArgument},
		} {
			t.Run(stage+"/"+tc.name, func(t *testing.T) {
				s, f, actor := newSemanticSearchTest(t)
				actor.Roles = []identity.Role{identity.RoleTenantAdmin}
				calls := 0
				service := &SearchService{local: s, federated: snapshotLiveSearcher{live: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
					calls++
					return FederatedCatalogSearchPage{Live: []CatalogSearchResult{hybridLiveResult("urn:air:peer.example:agent:tail", "", false, 90)}, ReplayFingerprint: strings.Repeat("a", 64)}, nil
				}}}
				ctx := identityfixture.WithActor(t.Context(), actor)
				query := SearchQuery{Text: "find", Limit: 1}
				first, err := service.Search(ctx, query)
				if err != nil || !strings.HasPrefix(first.NextCursor, LiveCursorPrefix) {
					t.Fatalf("healthy first page: %+v %v", first, err)
				}
				query.Cursor = first.NextCursor
				healthy, err := service.Search(ctx, query)
				if err != nil || calls != 2 {
					t.Fatalf("healthy continuation: %+v %v; calls=%d", healthy, err, calls)
				}
				if stage == "generation" {
					f.storeError = tc.fault
				} else {
					f.searchError = tc.fault
				}
				failed, err := service.Search(ctx, query)
				if ErrorCodeOf(err) != tc.code || len(failed.Items) != 0 || failed.NextCursor != "" || calls != 2 {
					t.Fatalf("durable failure misclassified or contacted peer: %+v %v; calls=%d", failed, err, calls)
				}
				f.storeError, f.searchError = nil, nil
				recovered, err := service.Search(ctx, query)
				if err != nil || !reflect.DeepEqual(recovered, healthy) || calls != 3 {
					t.Fatalf("same-token recovery changed page: %+v %v; calls=%d", recovered, err, calls)
				}
			})
		}
	}
}

func TestHybridLiveContinuationPreservesCallerCancellation(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	actor.Roles = []identity.Role{identity.RoleTenantAdmin}
	calls := 0
	service := &SearchService{local: s, federated: snapshotLiveSearcher{live: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		calls++
		return FederatedCatalogSearchPage{Live: []CatalogSearchResult{hybridLiveResult("urn:air:peer.example:agent:tail", "", false, 90)}, ReplayFingerprint: strings.Repeat("a", 64)}, nil
	}}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	query := SearchQuery{Text: "find", Limit: 1}
	first, err := service.Search(ctx, query)
	if err != nil || !strings.HasPrefix(first.NextCursor, LiveCursorPrefix) {
		t.Fatalf("healthy first page: %+v %v", first, err)
	}
	query.Cursor = first.NextCursor
	cancelled, stop := context.WithCancel(ctx)
	defer stop()
	f.queryHook = func(context.Context) { stop() }
	f.providerError = context.Canceled
	page, err := service.Search(cancelled, query)
	if !errors.Is(err, context.Canceled) || len(page.Items) != 0 || page.NextCursor != "" || calls != 1 {
		t.Fatalf("cancelled continuation was made retryable or contacted peer: %+v %v; calls=%d", page, err, calls)
	}
}

func TestLiveOnlyUnverifiedFirstWindowCannotCreateContinuation(t *testing.T) {
	s, _, actor := newSemanticSearchTest(t)
	actor.Roles = []identity.Role{identity.RoleTenantAdmin}
	items := []CatalogSearchResult{hybridLiveResult("urn:air:peer.example:agent:a", "", false, 90), hybridLiveResult("urn:air:peer.example:agent:b", "", false, 80)}
	service := &SearchService{local: s, federated: snapshotLiveSearcher{live: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		return FederatedCatalogSearchPage{Live: items}, nil
	}}}
	query := SearchQuery{Text: "find", Origins: []SearchResultType{SearchResultLive}, Limit: 1}
	page, err := service.Search(identityfixture.WithActor(t.Context(), actor), query)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != items[0].ID || page.NextCursor != "" || !page.Partial || len(page.Diagnostics) != 1 || page.Diagnostics[0].Status != "live_results_limited" {
		t.Fatalf("unverified window: %+v %v", page, err)
	}
}

func TestLiveOnlyContinuationPreservesCallerCancellation(t *testing.T) {
	s, _, actor := newSemanticSearchTest(t)
	actor.Roles = []identity.Role{identity.RoleTenantAdmin}
	items := []CatalogSearchResult{hybridLiveResult("urn:air:peer.example:agent:a", "", false, 90), hybridLiveResult("urn:air:peer.example:agent:b", "", false, 80)}
	var cancel context.CancelFunc
	service := &SearchService{local: s, federated: snapshotLiveSearcher{live: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		if cancel != nil {
			cancel()
			return FederatedCatalogSearchPage{}, context.Canceled
		}
		return FederatedCatalogSearchPage{Live: items, ReplayFingerprint: strings.Repeat("a", 64)}, nil
	}}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	query := SearchQuery{Text: "find", Origins: []SearchResultType{SearchResultLive}, Limit: 1}
	first, err := service.Search(ctx, query)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("healthy control: %+v %v", first, err)
	}
	query.Cursor = first.NextCursor
	cancelled, stop := context.WithCancel(ctx)
	defer stop()
	cancel = stop
	page, err := service.Search(cancelled, query)
	if !errors.Is(err, context.Canceled) || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("cancelled continuation was made retryable: %+v %v", page, err)
	}
}
