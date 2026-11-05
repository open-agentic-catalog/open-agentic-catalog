// Search boundary tests distinguish fresh fallback from failed continuation
// using controlled ports and cancellation, without services or timed sleeps.
package catalog

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"testing"
	"time"
)

// TestSemanticSearchLocalPurpose binds local-only selection and rejects cursor
// reuse across local and unified searches before any further inference.
func TestSemanticSearchLocalPurpose(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	f.page = hybridDurablePage()
	f.page.Items = f.page.Items[:1]
	f.page.HasMore = true
	query := SearchQuery{Text: "find", Limit: 1}
	ctx := identityfixture.WithActor(t.Context(), actor)
	local, err := s.Search(ctx, query)
	if err != nil || !reflect.DeepEqual(local.Items, []CatalogSummary{*f.page.Items[0].Local}) || local.NextCursor == "" || local.Total != 2 || local.Tier != "hybrid" || local.Profile != s.semantic.options.RankingProfile || !local.PartialIndex || !f.searchRequest.LocalOnly {
		t.Fatalf("local hybrid projection: %+v, %v; request=%+v", local, err, f.searchRequest)
	}
	query.Cursor = local.NextCursor
	if _, _, _, err := s.semanticPage(ctx, actor, query, false); ErrorCodeOf(err) != CodeInvalidArgument || f.queries != 1 || f.searches != 1 {
		t.Fatalf("local cursor admitted to unified search: %v", err)
	}
	query.Cursor = ""
	unified, used, _, err := s.semanticPage(ctx, actor, query, false)
	if err != nil || !used || unified.NextCursor == "" || f.searchRequest.LocalOnly {
		t.Fatalf("unified control: %+v, %v", unified, err)
	}
	query.Cursor = unified.NextCursor
	if _, err := s.Search(ctx, query); ErrorCodeOf(err) != CodeInvalidArgument || f.queries != 2 || f.searches != 2 {
		t.Fatalf("unified cursor admitted to local search: %v", err)
	}
}

// TestSemanticSearchStoreFailurePolicy never changes continuation ranking when
// a durable generation races selection or the exact matching work bound is hit.
func TestSemanticSearchStoreFailurePolicy(t *testing.T) {
	for _, test := range []struct {
		name, reason string
		cause        error
	}{
		{"generation changed", "index_changed", ErrSearchGenerationChanged},
		{"work bound", "work_limit", E(CodeWorkLimitExceeded, "bounded match population exceeded")},
		{"ordinary failure", "", errors.New("durable selection failed")},
	} {
		for _, continuation := range []bool{false, true} {
			name := test.name + "/fresh"
			if continuation {
				name = test.name + "/continuation"
			}
			t.Run(name, func(t *testing.T) {
				s, f, actor := newSemanticSearchTest(t)
				query := SearchQuery{Text: "find", Limit: 1}
				if continuation {
					first, used, _, err := s.semanticPage(t.Context(), actor, query, false)
					if err != nil || !used || first.NextCursor == "" {
						t.Fatalf("initial page: %+v, %v", first, err)
					}
					query.Cursor = first.NextCursor
				}
				before := f.searches
				f.searchError = test.cause
				page, used, reason, err := s.semanticPage(t.Context(), actor, query, false)
				if !continuation && test.reason != "" {
					if err != nil || used || reason != test.reason {
						t.Fatalf("fresh fallback: used=%v reason=%q err=%v", used, reason, err)
					}
				} else if !errors.Is(err, test.cause) || !used || reason != "" {
					t.Fatalf("failure became fallback: used=%v reason=%q err=%v", used, reason, err)
				}
				if f.searches != before+1 || len(page.Items) != 0 || page.NextCursor != "" {
					t.Fatalf("failed selection returned results or repeated work: %+v calls=%d", page, f.searches)
				}
			})
		}
	}
}

// TestSemanticSearchCallerFailures distinguishes request cancellation and
// unexpected provider errors from the already-covered optional-provider outage.
func TestSemanticSearchCallerFailures(t *testing.T) {
	for _, mode := range []string{"canceled", "canceled with hint", "expired", "unknown provider error"} {
		t.Run(mode, func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			ctx := t.Context()
			want := errors.New("unexpected provider failure")
			f.providerError = want
			switch mode {
			case "canceled", "canceled with hint":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				f.queryHook = func(context.Context) { cancel() }
				f.providerError, want = ErrEmbeddingUnavailable, context.Canceled
				if mode == "canceled with hint" {
					f.providerError = &EmbeddingRetryError{RetryAfter: 24 * time.Hour}
				}
			case "expired":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
				f.providerError, want = context.DeadlineExceeded, context.DeadlineExceeded
			}
			_, used, reason, err := s.semanticPage(ctx, actor, SearchQuery{Text: "find", Limit: 1}, false)
			if !errors.Is(err, want) || !used || reason != "" || f.queries != 1 || f.searches != 0 {
				t.Fatalf("caller failure: used=%v reason=%q err=%v queries=%d searches=%d", used, reason, err, f.queries, f.searches)
			}
		})
	}
}

// TestSemanticSearchContinuationLosesCapability rejects an existing hybrid
// cursor before inference when its configuration or active index disappears.
func TestSemanticSearchContinuationLosesCapability(t *testing.T) {
	for _, mode := range []string{"disabled", "excluded", "index not ready"} {
		t.Run(mode, func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			query := SearchQuery{Text: "find", Limit: 1}
			first, used, _, err := s.semanticPage(t.Context(), actor, query, false)
			if err != nil || !used || first.NextCursor == "" {
				t.Fatalf("initial page: %+v, %v", first, err)
			}
			query.Cursor = first.NextCursor
			switch mode {
			case "disabled":
				s.semantic = nil
			case "excluded":
				s.semantic.options.ExcludedTenants = []string{actor.TenantID}
			case "index not ready":
				f.ready = false
			}
			_, used, reason, err := s.semanticPage(t.Context(), actor, query, false)
			if ErrorCodeOf(err) != CodeInvalidArgument || !used || reason != "" || f.queries != 1 || f.searches != 1 {
				t.Fatalf("unsafe continuation: used=%v reason=%q err=%v queries=%d searches=%d", used, reason, err, f.queries, f.searches)
			}
		})
	}
}
