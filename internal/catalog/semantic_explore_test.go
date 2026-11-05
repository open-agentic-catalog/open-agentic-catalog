// Explore coordinator tests isolate retrieval policy from databases and models.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type semanticExploreRepository struct {
	Repository
	calls  int
	tenant string
	query  SearchQuery
	facets []FacetRequest
	err    error
}

func (r *semanticExploreRepository) ExploreCatalog(_ context.Context, tenant string, query SearchQuery, facets []FacetRequest) (ExploreResponse, error) {
	r.calls++
	r.tenant, r.query, r.facets = tenant, query, facets
	return ExploreResponse{Tier: "core", Profile: CoreSearchProfile, Matched: 7}, r.err
}

// semanticExploreRequest exercises normalization and preserves explicit filters.
func semanticExploreRequest() ExploreRequest {
	return ExploreRequest{
		Query:  SearchQuery{Text: " find   tools ", Kinds: []Kind{KindAgent}, Tags: []string{"tools"}, License: "MIT", Categories: []string{"tools"}, Publisher: " publisher ", Visibility: VisibilityPublic},
		Facets: []FacetRequest{{Field: "kind", MinCount: 2}},
	}
}

// TestSemanticExploreSelection checks success, fallback, and failures through the public service entry point.
func TestSemanticExploreSelection(t *testing.T) {
	coreFailure := errors.New("core store failed")
	providerFailure := errors.New("unexpected provider failure")
	for _, tc := range []struct {
		name                                        string
		notReady                                    bool
		activeErr, providerErr, exploreErr, wantErr error
		reason                                      string
		queries, explores, lexical                  int
	}{
		{name: "hybrid", queries: 1, explores: 1},
		{name: "indexing", notReady: true, reason: "indexing", lexical: 1},
		{name: "unavailable", providerErr: fmt.Errorf("provider: %w", ErrEmbeddingUnavailable), reason: "provider_unavailable", queries: 1, lexical: 1},
		{name: "hinted unavailable", providerErr: fmt.Errorf("provider: %w", &EmbeddingRetryError{RetryAfter: 2 * time.Minute}), reason: "provider_unavailable", queries: 1, lexical: 1},
		{name: "authentication", providerErr: ErrEmbeddingAuthentication, reason: "provider_unavailable", queries: 1, lexical: 1},
		{name: "rejected", providerErr: ErrEmbeddingRejected, reason: "provider_unavailable", queries: 1, lexical: 1},
		{name: "invalid response", providerErr: ErrEmbeddingInvalidResponse, reason: "provider_unavailable", queries: 1, lexical: 1},
		{name: "provider deadline", providerErr: context.DeadlineExceeded, reason: "provider_unavailable", queries: 1, lexical: 1},
		{name: "changed generation", exploreErr: fmt.Errorf("explore: %w", ErrSearchGenerationChanged), reason: "index_changed", queries: 1, explores: 1, lexical: 1},
		{name: "work limit", exploreErr: E(CodeWorkLimitExceeded, "bounded work"), reason: "work_limit", queries: 1, explores: 1, lexical: 1},
		{name: "active store failure", activeErr: coreFailure, wantErr: coreFailure},
		{name: "provider failure", providerErr: providerFailure, wantErr: providerFailure, queries: 1},
		{name: "explore store failure", exploreErr: coreFailure, wantErr: coreFailure, queries: 1, explores: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			repo := &semanticExploreRepository{}
			s.repo = repo
			f.ready, f.storeError, f.providerError, f.exploreError = !tc.notReady, tc.activeErr, tc.providerErr, tc.exploreErr
			var observations []string
			s.semantic.options.Observe = func(operation, outcome string) { observations = append(observations, operation+":"+outcome) }
			response, err := s.Explore(identityfixture.WithActor(t.Context(), actor), semanticExploreRequest())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if f.activeCalls != 1 || f.queries != tc.queries || f.exploreCalls != tc.explores || repo.calls != tc.lexical {
				t.Fatalf("calls active/query/explore/lexical = %d/%d/%d/%d", f.activeCalls, f.queries, f.exploreCalls, repo.calls)
			}
			if f.lookup != (semanticLookup{actor.TenantID, f.g.Profile, "control", 2}) {
				t.Fatalf("lookup = %+v", f.lookup)
			}
			wantQuery := SearchQuery{Text: "find tools", Kinds: []Kind{KindAgent}, Tags: []string{"tools"}, License: "MIT", Categories: []string{"tools"}, Publisher: "publisher", Visibility: VisibilityPublic}
			wantFacets := []FacetRequest{{Field: "kind", Limit: 20, MinCount: 2}}
			if f.queries != 0 && f.queryText != wantQuery.Text {
				t.Fatalf("provider query = %q", f.queryText)
			}
			if f.exploreCalls != 0 {
				want := SemanticSearchRequest{Generation: f.g, Query: wantQuery, Vector: f.vector, Cutoff: .5, LocalOnly: true}
				if !reflect.DeepEqual(f.exploreRequest, want) || !reflect.DeepEqual(f.facets, wantFacets) {
					t.Fatalf("hybrid request = %+v, facets = %+v", f.exploreRequest, f.facets)
				}
			}
			outcome := "hybrid"
			if tc.lexical != 0 {
				outcome = tc.reason
				if repo.tenant != actor.TenantID || !reflect.DeepEqual(repo.query, wantQuery) || !reflect.DeepEqual(repo.facets, wantFacets) {
					t.Fatalf("lexical request = %+v", repo)
				}
				if response.Tier != "core" || response.Profile != CoreSearchProfile || response.Matched != 7 || response.FallbackReason != tc.reason {
					t.Fatalf("fallback = %+v", response)
				}
			} else if tc.wantErr == nil {
				// An empty successful index must not be replaced with lexical matches.
				if response.Tier != "hybrid" || response.Profile != s.semantic.options.RankingProfile || response.Matched != 0 || response.FallbackReason != "" {
					t.Fatalf("hybrid = %+v", response)
				}
			}
			if tc.wantErr != nil {
				outcome = "error"
			}
			if !reflect.DeepEqual(observations, []string{"explore:" + outcome}) {
				t.Fatalf("observations = %v", observations)
			}
		})
	}
}

// TestSemanticExploreBypasses keeps exact, empty, disabled and excluded requests model-free.
func TestSemanticExploreBypasses(t *testing.T) {
	for _, mode := range []string{"empty", "exact", "disabled", "excluded"} {
		t.Run(mode, func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			repo := &semanticExploreRepository{}
			s.repo = repo
			request := semanticExploreRequest()
			var observations []string
			s.semantic.options.Observe = func(operation, outcome string) { observations = append(observations, operation+":"+outcome) }
			switch mode {
			case "empty":
				request.Query.Text = "  "
			case "exact":
				request.Query.Text = "urn:air:catalog.example:default:tool"
			case "disabled":
				s.semantic = nil
			case "excluded":
				s.semantic.options.ExcludedTenants = []string{actor.TenantID}
			}
			response, err := s.Explore(identityfixture.WithActor(t.Context(), actor), request)
			if err != nil || response.Tier != "core" || response.FallbackReason != "" || repo.calls != 1 || repo.tenant != actor.TenantID || f.activeCalls != 0 || f.queries != 0 || f.exploreCalls != 0 {
				t.Fatalf("response=%+v err=%v fixture=%+v lexical=%+v", response, err, f, repo)
			}
			if mode != "disabled" && !reflect.DeepEqual(observations, []string{"explore:core"}) {
				t.Fatalf("observations = %v", observations)
			}
		})
	}
}

// TestSemanticExploreAuthorizationAndErrors ensures denial and cancellation never trigger lexical work.
func TestSemanticExploreAuthorizationAndErrors(t *testing.T) {
	for _, mode := range []string{"anonymous", "unbound principal", "canceled", "expired", "store forbidden", "lexical failure"} {
		t.Run(mode, func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			repo := &semanticExploreRepository{}
			s.repo = repo
			ctx := identityfixture.WithActor(t.Context(), actor)
			var wantErr error
			queries, explores, lexical := 0, 0, 0
			switch mode {
			case "anonymous":
				ctx = t.Context()
			case "unbound principal":
				actor.Roles = nil
				ctx = identity.WithActor(t.Context(), actor)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				f.queryHook = func(context.Context) { cancel() }
				f.providerError, wantErr, queries = ErrEmbeddingUnavailable, context.Canceled, 1
			case "expired":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
				f.providerError, wantErr, queries = context.DeadlineExceeded, context.DeadlineExceeded, 0
			case "store forbidden":
				f.exploreError = E(CodeForbidden, "access denied")
				wantErr, queries, explores = f.exploreError, 1, 1
			case "lexical failure":
				f.ready = false
				repo.err = errors.New("lexical store failed")
				wantErr, lexical = repo.err, 1
			}
			_, err := s.Explore(ctx, semanticExploreRequest())
			if wantErr == nil {
				if ErrorCodeOf(err) != CodeForbidden || f.activeCalls != 0 {
					t.Fatalf("authorization = %v, active calls = %d", err, f.activeCalls)
				}
			} else if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want %v", err, wantErr)
			}
			if f.queries != queries || f.exploreCalls != explores || repo.calls != lexical {
				t.Fatalf("calls query/explore/lexical = %d/%d/%d", f.queries, f.exploreCalls, repo.calls)
			}
		})
	}
}

// TestSemanticExploreDeadlineAndResult preserves the parent budget for matching and returns index evidence unchanged.
func TestSemanticExploreDeadlineAndResult(t *testing.T) {
	for _, parentBudget := range []time.Duration{4 * time.Second, time.Minute} {
		t.Run(parentBudget.String(), func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			s.semantic.options.Timeout = 10 * time.Second
			ctx, cancel := context.WithTimeout(identityfixture.WithActor(t.Context(), actor), parentBudget)
			defer cancel()
			var queryError func() error
			started := time.Now()
			f.queryHook = func(child context.Context) {
				queryError = child.Err
				deadline, ok := child.Deadline()
				parentDeadline, _ := ctx.Deadline()
				now := time.Now()
				// Leave scheduling tolerance around the half-budget bound while
				// rejecting a provider deadline that consumes the parent budget.
				if !ok || !deadline.After(started) || deadline.After(now.Add(s.semantic.options.Timeout)) || deadline.After(now.Add(parentDeadline.Sub(now)*3/4)) {
					t.Fatalf("query deadline = %v, parent = %v", deadline, parentDeadline)
				}
				expectedActor, _ := identity.FromContext(ctx)
				if got, _ := identity.FromContext(child); !reflect.DeepEqual(got, expectedActor) {
					t.Fatal("query lost actor context")
				}
			}
			f.exploreResponse = ExploreResponse{PartialIndex: true, Matched: 3, Facets: map[string]FacetResult{"kind": {Buckets: []FacetBucket{{Value: "agent", Count: 2}}, OtherCount: 1}}}
			got, err := s.Explore(ctx, semanticExploreRequest())
			want := f.exploreResponse
			want.Tier, want.Profile = "hybrid", s.semantic.options.RankingProfile
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("response = %+v, error = %v", got, err)
			}
			if queryError == nil || !errors.Is(queryError(), context.Canceled) || ctx.Err() != nil || f.queries != 1 || f.exploreCalls != 1 {
				t.Fatal("query budget was not canceled independently of the parent")
			}
		})
	}
}
