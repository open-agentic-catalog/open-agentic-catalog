// Indexing workflow tests use an in-memory port and deterministic inference;
// SQL lease and tenant checks belong to the separate integration suite.
package catalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type semanticIndexFixture struct {
	inputs        []SemanticInput
	saved         map[string]SemanticMember
	known         map[string]bool
	completed     bool
	fail          string
	calls         int
	providerError error
	documentHook  func([]string)
}

func (f *semanticIndexFixture) Begin(context.Context, SemanticGeneration, string) error {
	return f.err("begin")
}
func (f *semanticIndexFixture) Inputs(_ context.Context, _ SemanticGeneration, after string, limit int) ([]SemanticInput, int64, error) {
	var result []SemanticInput
	for _, input := range f.inputs {
		if input.Key > after {
			if _, saved := f.saved[input.Key]; !saved {
				result = append(result, input)
			}
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, int64(len(f.inputs)), f.err("inputs")
}
func (f *semanticIndexFixture) KnownInputs(context.Context, SemanticGeneration, []string) (map[string]bool, error) {
	return f.known, f.err("known")
}
func (f *semanticIndexFixture) SaveBatch(_ context.Context, _ SemanticGeneration, _ string, members []SemanticMember) error {
	if err := f.err("save"); err != nil {
		return err
	}
	for _, m := range members {
		f.saved[m.Key] = m
	}
	return nil
}
func (f *semanticIndexFixture) Complete(_ context.Context, _ SemanticGeneration, _ string, total int64) error {
	if total != int64(len(f.saved)) {
		return errors.New("missing members")
	}
	f.completed = f.fail != "complete"
	return f.err("complete")
}
func (f *semanticIndexFixture) err(stage string) error {
	if f.fail == stage {
		return errors.New("fixture failure")
	}
	return nil
}
func (f *semanticIndexFixture) Query(context.Context, string) ([]float32, error) {
	panic("indexer must not call query inference")
}
func (f *semanticIndexFixture) Documents(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.documentHook != nil {
		f.documentHook(texts)
	}
	if f.providerError != nil {
		return nil, f.providerError
	}
	for _, text := range texts {
		if strings.Contains(text, "reject-me") {
			return nil, ErrEmbeddingRejected
		}
	}
	if f.fail == "response" {
		return nil, nil
	}
	result := make([][]float32, len(texts))
	for i := range texts {
		result[i] = []float32{1, 0, 0}
	}
	return result, nil
}

func newSemanticIndexFixture() *semanticIndexFixture {
	return &semanticIndexFixture{inputs: []SemanticInput{
		{Key: "a", Document: SemanticDocument{DisplayName: "Reuse"}},
		{Key: "b", Document: SemanticDocument{DisplayName: "New"}},
		{Key: "c", Document: SemanticDocument{DisplayName: "reject-me"}},
	}, saved: map[string]SemanticMember{}, known: map[string]bool{}}
}

// semanticBoundaryFixture substitutes malformed pages or a mid-pass outage;
// ordinary metadata, storage and provider behavior reuse the existing fixture.
type semanticBoundaryFixture struct {
	*semanticIndexFixture
	mode   string
	cancel context.CancelFunc
}

func (f *semanticBoundaryFixture) Inputs(ctx context.Context, g SemanticGeneration, after string, limit int) ([]SemanticInput, int64, error) {
	switch f.mode {
	case "negative-total":
		return nil, -1, nil
	case "oversized-page":
		return f.inputs, 3, nil
	case "repeated-key":
		return []SemanticInput{f.inputs[0], f.inputs[0]}, 2, nil
	default:
		return f.semanticIndexFixture.Inputs(ctx, g, after, limit)
	}
}

func (f *semanticBoundaryFixture) KnownInputs(ctx context.Context, g SemanticGeneration, digests []string) (map[string]bool, error) {
	if f.cancel != nil {
		f.cancel()
	}
	return f.semanticIndexFixture.KnownInputs(ctx, g, digests)
}

func (f *semanticBoundaryFixture) Documents(ctx context.Context, texts []string) ([][]float32, error) {
	if f.mode == "outage-during-bisection" || f.mode == "hinted-outage-during-bisection" {
		f.calls++
		if f.calls == 1 {
			return nil, ErrEmbeddingRejected
		}
		if f.providerError != nil {
			return nil, f.providerError
		}
		return nil, ErrEmbeddingUnavailable
	}
	return f.semanticIndexFixture.Documents(ctx, texts)
}

// TestSemanticIndexerAbortsInvalidPagesAndInterruptedBatches prevents a broken
// port or a partial provider failure from completing or storing an invalid page.
func TestSemanticIndexerAbortsInvalidPagesAndInterruptedBatches(t *testing.T) {
	for _, mode := range []string{"negative-total", "oversized-page", "repeated-key", "canceled-before-inference", "outage-during-bisection", "hinted-outage-during-bisection"} {
		t.Run(mode, func(t *testing.T) {
			f := &semanticBoundaryFixture{semanticIndexFixture: newSemanticIndexFixture(), mode: mode}
			if mode == "hinted-outage-during-bisection" {
				f.providerError = fmt.Errorf("provider: %w", &EmbeddingRetryError{RetryAfter: 2 * time.Minute})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled-before-inference" {
				f.cancel = cancel
			}
			indexer, err := NewSemanticIndexer(f, f, 2, 8192)
			if err != nil {
				t.Fatal(err)
			}
			_, err = indexer.Run(ctx, SemanticGeneration{}, "worker")
			calls := 0
			switch mode {
			case "canceled-before-inference":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case "outage-during-bisection", "hinted-outage-during-bisection":
				calls = 2
				if !errors.Is(err, ErrEmbeddingUnavailable) {
					t.Fatalf("outage lost: %v", err)
				}
				if f.providerError != nil {
					var hint *EmbeddingRetryError
					if !errors.Is(err, f.providerError) || !errors.As(err, &hint) || hint.RetryAfter != 2*time.Minute {
						t.Fatalf("bisection lost hint: %v", err)
					}
				}
			default:
				if err == nil || ErrorCodeOf(err) != CodeInternal {
					t.Fatalf("malformed page accepted: %v", err)
				}
			}
			if f.calls != calls || len(f.saved) != 0 || f.completed {
				t.Fatalf("interrupted page continued: calls=%d saved=%d complete=%v", f.calls, len(f.saved), f.completed)
			}
		})
	}
}

func TestSemanticIndexerReuseIsolationAndResume(t *testing.T) {
	f := newSemanticIndexFixture()
	_, digest, _ := f.inputs[0].Document.Projection()
	f.known[digest] = true
	indexer, err := NewSemanticIndexer(f, f, 3, 8192)
	if err != nil {
		t.Fatal(err)
	}
	count, err := indexer.Run(t.Context(), SemanticGeneration{}, "worker")
	if err != nil || count != 3 || !f.completed || f.calls != 3 {
		t.Fatalf("count=%d calls=%d complete=%v err=%v", count, f.calls, f.completed, err)
	}
	if !f.saved["a"].Reuse || len(f.saved["a"].Vector) != 0 || !slices.Equal(f.saved["b"].Vector, []float32{1, 0, 0}) || f.saved["c"].Failure != "input_rejected" || f.saved["c"].InputDigest != "" {
		t.Fatal("reuse or invalid-input isolation failed")
	}
	if _, err := indexer.Run(t.Context(), SemanticGeneration{}, "worker"); err != nil || f.calls != 3 {
		t.Fatalf("replay called inference: %v", err)
	}
}

func TestSemanticIndexerRejectsDependencyFailuresWithoutDiscardingMetadata(t *testing.T) {
	for _, cause := range []error{ErrEmbeddingUnavailable, fmt.Errorf("provider: %w", &EmbeddingRetryError{RetryAfter: 2 * time.Minute}), ErrEmbeddingAuthentication, ErrEmbeddingInvalidResponse, context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			f := newSemanticIndexFixture()
			f.providerError = cause
			indexer, _ := NewSemanticIndexer(f, f, 3, 8192)
			if _, err := indexer.Run(t.Context(), SemanticGeneration{}, "worker"); !errors.Is(err, cause) || f.calls != 1 || len(f.saved) != 0 || f.completed {
				t.Fatalf("provider failure swallowed: calls=%d saved=%d err=%v", f.calls, len(f.saved), err)
			}
		})
	}
}

// A later outage must preserve saved pages and resume only unfinished metadata.
func TestSemanticIndexerResumesAfterHintedPageFailure(t *testing.T) {
	f := newSemanticIndexFixture()
	f.inputs = f.inputs[:2]
	firstText, _, err := f.inputs[0].Document.Projection()
	if err != nil {
		t.Fatal(err)
	}
	secondText, _, err := f.inputs[1].Document.Projection()
	if err != nil {
		t.Fatal(err)
	}
	hint := &EmbeddingRetryError{RetryAfter: 2 * time.Minute}
	cause := fmt.Errorf("provider: %w", hint)
	var requests [][]string
	f.documentHook = func(texts []string) {
		requests = append(requests, slices.Clone(texts))
		if f.calls == 2 {
			f.providerError = cause
		}
	}
	indexer, err := NewSemanticIndexer(f, f, 1, 8192)
	if err != nil {
		t.Fatal(err)
	}
	_, err = indexer.Run(t.Context(), SemanticGeneration{}, "worker")
	var retained *EmbeddingRetryError
	if !errors.Is(err, cause) || !errors.Is(err, ErrEmbeddingUnavailable) || !errors.As(err, &retained) || retained != hint || f.calls != 2 || f.completed || len(f.saved) != 1 {
		t.Fatalf("outage lost hint or partial progress: calls=%d saved=%d err=%v", f.calls, len(f.saved), err)
	}
	first := f.saved["a"]
	first.Vector = slices.Clone(first.Vector)
	if first.Failure != "" || first.Reuse || !slices.Equal(first.Vector, []float32{1, 0, 0}) {
		t.Fatal("first page was not retained")
	}
	f.providerError = nil
	count, err := indexer.Run(t.Context(), SemanticGeneration{}, "worker")
	if err != nil || count != 2 || !f.completed || len(f.saved) != 2 || !reflect.DeepEqual(first, f.saved["a"]) || f.saved["b"].Failure != "" || !slices.Equal(f.saved["b"].Vector, []float32{1, 0, 0}) {
		t.Fatalf("resume changed saved metadata or failed to complete: count=%d err=%v", count, err)
	}
	if !reflect.DeepEqual(requests, [][]string{{firstText}, {secondText}, {secondText}}) {
		t.Fatalf("resume repeated completed work: %v", requests)
	}
}

// A successful child of a rejected batch must not escape as a partial page
// when cancellation prevents the next child from being embedded.
func TestSemanticIndexerCancellationDuringBisection(t *testing.T) {
	f := newSemanticIndexFixture()
	f.inputs = f.inputs[1:]
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.documentHook = func([]string) {
		if f.calls == 2 {
			cancel()
		}
	}
	indexer, err := NewSemanticIndexer(f, f, 2, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Run(ctx, SemanticGeneration{}, "worker"); !errors.Is(err, context.Canceled) || f.calls != 2 || len(f.saved) != 0 || f.completed {
		t.Fatalf("canceled bisection continued or saved partial data: calls=%d saved=%d complete=%v err=%v", f.calls, len(f.saved), f.completed, err)
	}
}

// TestSemanticIndexerDeduplicatesBatchInputs covers reuse before a vector has
// been persisted, including repeated metadata that the provider rejects.
func TestSemanticIndexerDeduplicatesBatchInputs(t *testing.T) {
	for _, name := range []string{"shared", "reject-me"} {
		t.Run(name, func(t *testing.T) {
			f := newSemanticIndexFixture()
			f.inputs = []SemanticInput{{Key: "a", Document: SemanticDocument{DisplayName: name}}, {Key: "b", Document: SemanticDocument{DisplayName: name}}}
			indexer, err := NewSemanticIndexer(f, f, 2, 8192)
			if err != nil {
				t.Fatal(err)
			}
			if count, err := indexer.Run(t.Context(), SemanticGeneration{}, "worker"); err != nil || count != 2 || f.calls != 1 {
				t.Fatalf("count=%d calls=%d err=%v", count, f.calls, err)
			}
			a, b := f.saved["a"], f.saved["b"]
			if name == "shared" {
				if len(a.Vector) != 3 || a.Reuse || !b.Reuse || len(b.Vector) != 0 || b.InputDigest != a.InputDigest {
					t.Fatal("duplicate was not linked to the first vector")
				}
			} else if a.Failure != "input_rejected" || b.Failure != a.Failure || b.InputDigest != "" || b.Reuse {
				t.Fatal("duplicate rejected input was not accounted for")
			}
		})
	}
}

// TestSemanticIndexerMaximumRejectedBatch bounds bisection at the largest
// admitted page and preserves every successful or rejected member on resume.
func TestSemanticIndexerMaximumRejectedBatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wantCalls int
		accepted  int
	}{
		{"all rejected", 63, 0},
		{"accepted half", 33, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSemanticIndexFixture()
			f.inputs = nil
			want := make(map[string]SemanticMember, 32)
			for i := range 32 {
				key := fmt.Sprintf("item-%02d", i)
				name := "reject-me-" + key
				member := SemanticMember{Key: key, Failure: "input_rejected"}
				if i < tc.accepted {
					name = "accepted-" + key
					text := "Name: " + name + "\nDescription: \nTags: \nRepresentative queries: "
					member = SemanticMember{Key: key, InputDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))), Vector: []float32{1, 0, 0}}
				}
				f.inputs = append(f.inputs, SemanticInput{Key: key, Document: SemanticDocument{DisplayName: name}})
				want[key] = member
			}
			indexer, err := NewSemanticIndexer(f, f, 32, 8192)
			if err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"initial", "resume"} {
				count, err := indexer.Run(t.Context(), SemanticGeneration{}, "worker")
				if err != nil || count != 32 || !f.completed || f.calls != tc.wantCalls || !reflect.DeepEqual(f.saved, want) {
					t.Fatalf("%s: count=%d calls=%d saved=%d completed=%v error=%v", phase, count, f.calls, len(f.saved), f.completed, err)
				}
			}
		})
	}
}

func TestSemanticIndexerBoundsAndStoreErrors(t *testing.T) {
	for _, stage := range []string{"begin", "inputs", "known", "save", "complete", "response"} {
		t.Run(stage, func(t *testing.T) {
			f := newSemanticIndexFixture()
			f.fail = stage
			indexer, _ := NewSemanticIndexer(f, f, 2, 8192)
			if _, err := indexer.Run(t.Context(), SemanticGeneration{}, "worker"); err == nil || f.completed {
				t.Fatal("failed dependency completed generation")
			}
		})
	}
	text := "Name: Reuse\nDescription: " + strings.Repeat("é", 32) + "\nTags: \nRepresentative queries: "
	for _, limit := range []int{len(text), len(text) - 1} {
		t.Run(fmt.Sprintf("local byte limit %d", limit), func(t *testing.T) {
			f := newSemanticIndexFixture()
			f.inputs[0].Document.Description = strings.Repeat("é", 32)
			f.inputs = f.inputs[:1]
			indexer, err := NewSemanticIndexer(f, f, 1, limit)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			want := SemanticMember{Key: "a", Failure: "input_too_large"}
			if limit == len(text) {
				calls = 1
				want = SemanticMember{Key: "a", InputDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))), Vector: []float32{1, 0, 0}}
			}
			if count, err := indexer.Run(t.Context(), SemanticGeneration{}, "worker"); err != nil || count != 1 || !f.completed || f.calls != calls || !reflect.DeepEqual(f.saved, map[string]SemanticMember{"a": want}) {
				t.Fatalf("byte bound: count=%d calls=%d saved=%v complete=%v error=%v", count, f.calls, f.saved, f.completed, err)
			}
		})
	}
	f := newSemanticIndexFixture()
	for _, bounds := range [][2]int{{0, 8192}, {33, 8192}, {1, 63}, {1, 32769}} {
		if _, err := NewSemanticIndexer(f, f, bounds[0], bounds[1]); err == nil {
			t.Fatal("invalid work bound accepted")
		}
	}
	if _, err := NewSemanticIndexer(nil, f, 1, 8192); err == nil {
		t.Fatal("missing store accepted")
	}
	if _, err := NewSemanticIndexer(f, nil, 1, 8192); err == nil {
		t.Fatal("missing provider accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	indexer, _ := NewSemanticIndexer(f, f, 1, 128)
	if _, err := indexer.Run(ctx, SemanticGeneration{}, "worker"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}
