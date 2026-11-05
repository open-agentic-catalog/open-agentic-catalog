// Public-ranking tests exercise the metadata-only port and safe degradation
// without a database or inference process.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSemanticSnapshotBoundaryAndPartialIndex(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	input := []HybridCandidate{{ID: "entry:0000000001", SortName: "item", LexicalScore: 50}}
	ranking, err := s.semantic.RankSnapshot(t.Context(), actor.TenantID, f.g.ID, "1", "find", input)
	if err != nil || ranking.Profile == "" || !ranking.PartialIndex || len(ranking.Ranks) != 1 || input[0].Similarity != nil {
		t.Fatalf("partial public ranking or input mutation: %+v %v", ranking, err)
	}
	if f.queries != 1 || f.queryText != "find" {
		t.Fatalf("public provider input changed: %q (%d calls)", f.queryText, f.queries)
	}
	f.ready = false
	ranking, err = s.semantic.RankSnapshot(t.Context(), actor.TenantID, f.g.ID, "1", "find", input)
	if err != nil || ranking.FallbackReason != "indexing" || f.queries != 1 {
		t.Fatalf("indexing fallback: %+v %v", ranking, err)
	}
	f.ready = true
	s.semantic.options.ExcludedTenants = []string{actor.TenantID}
	ranking, err = s.semantic.RankSnapshot(t.Context(), actor.TenantID, f.g.ID, "1", "find", input)
	if err != nil || ranking.Profile != "" || f.queries != 1 {
		t.Fatal("excluded public tenant reached inference")
	}
	var disabled *SemanticSearch
	if _, err := disabled.RankSnapshot(t.Context(), actor.TenantID, f.g.ID, "1", "find", input); err != nil {
		t.Fatal(err)
	}
}

// TestSemanticSnapshotComparisonFailures reaches the comparison port after a
// successful generation lookup; an ordinary store failure is never a fallback.
func TestSemanticSnapshotComparisonFailures(t *testing.T) {
	for _, cause := range []error{ErrSearchGenerationChanged, errors.New("comparison unavailable")} {
		t.Run(cause.Error(), func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			f.similaritiesError = cause
			ranking, err := s.semantic.RankSnapshot(t.Context(), actor.TenantID, f.g.ID, "1", "find", []HybridCandidate{{ID: "entry:0000000001"}})
			if errors.Is(cause, ErrSearchGenerationChanged) {
				if err != nil || ranking.FallbackReason != "index_changed" || ranking.Profile != "" {
					t.Fatalf("comparison generation fallback: %+v, %v", ranking, err)
				}
			} else if !errors.Is(err, cause) || ranking.FallbackReason != "" {
				t.Fatalf("comparison error hidden: %+v, %v", ranking, err)
			}
			if f.activeCalls != 1 || f.queries != 1 || f.similaritiesCalls != 1 || len(ranking.Ranks) != 0 {
				t.Fatalf("comparison failed at wrong stage: %+v fixture=%+v", ranking, f)
			}
		})
	}
}

// TestSemanticSnapshotCallerFailures preserves caller cancellation independently
// of provider availability and never starts a comparison after inference fails.
func TestSemanticSnapshotCallerFailures(t *testing.T) {
	for _, mode := range []string{"canceled", "expired", "unknown provider error"} {
		t.Run(mode, func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			ctx := t.Context()
			want := errors.New("unexpected provider failure")
			f.providerError = want
			switch mode {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				f.queryHook = func(context.Context) { cancel() }
				f.providerError, want = ErrEmbeddingUnavailable, context.Canceled
			case "expired":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
				f.providerError, want = context.DeadlineExceeded, context.DeadlineExceeded
			}
			ranking, err := s.semantic.RankSnapshot(ctx, actor.TenantID, f.g.ID, "1", "find", []HybridCandidate{{ID: "entry:0000000001"}})
			if !errors.Is(err, want) || ranking.FallbackReason != "" || f.queries != 1 || f.similaritiesCalls != 0 {
				t.Fatalf("caller failure: ranking=%+v err=%v queries=%d comparisons=%d", ranking, err, f.queries, f.similaritiesCalls)
			}
		})
	}
}

func TestSemanticSnapshotErrorsAreNotAllFallbacks(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	for _, cause := range []error{ErrEmbeddingUnavailable, fmt.Errorf("provider: %w", &EmbeddingRetryError{RetryAfter: 2 * time.Minute}), ErrEmbeddingAuthentication, ErrEmbeddingRejected, ErrEmbeddingInvalidResponse, context.DeadlineExceeded} {
		f.providerError = cause
		ranking, err := s.semantic.RankSnapshot(t.Context(), actor.TenantID, f.g.ID, "1", "find", []HybridCandidate{{ID: "entry:0000000001"}})
		if err != nil || ranking.FallbackReason != "provider_unavailable" {
			t.Fatalf("provider classification: %+v %v", ranking, err)
		}
	}
	f.providerError = nil
	f.storeError = errors.New("database unavailable")
	if _, err := s.semantic.RankSnapshot(t.Context(), actor.TenantID, f.g.ID, "1", "find", nil); !errors.Is(err, f.storeError) {
		t.Fatal("core database error hidden as fallback")
	}
}
