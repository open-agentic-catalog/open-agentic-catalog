// Semantic indexing converts bounded, eligible metadata pages into immutable
// generation membership. Inference happens between, never inside, store calls.
package catalog

import (
	"context"
	"errors"
)

// SemanticIndexer reuses compatible vectors and isolates permanently invalid
// inputs without turning provider outages or bad credentials into data loss.
type SemanticIndexer struct {
	store         SemanticIndexStore
	provider      SemanticProvider
	batchSize     int
	maxInputBytes int
}

// NewSemanticIndexer accepts process-owned dependencies and validated work bounds.
func NewSemanticIndexer(store SemanticIndexStore, provider SemanticProvider, batchSize, maxInputBytes int) (*SemanticIndexer, error) {
	if store == nil || provider == nil || batchSize < 1 || batchSize > 32 || maxInputBytes < 64 || maxInputBytes > SemanticMaxInputBytes {
		return nil, errors.New("invalid semantic indexer dependencies or bounds")
	}
	return &SemanticIndexer{store: store, provider: provider, batchSize: batchSize, maxInputBytes: maxInputBytes}, nil
}

// Run resumes one profile/Job-bound generation and activates only fully accounted
// membership. The store verifies the current source generation before each page
// and completion; rejected stale builds never replace the current search state.
func (s *SemanticIndexer) Run(ctx context.Context, generation SemanticGeneration, owner string) (int64, error) {
	if err := s.store.Begin(ctx, generation, owner); err != nil {
		return 0, err
	}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		inputs, total, err := s.store.Inputs(ctx, generation, after, s.batchSize)
		if err != nil {
			return 0, err
		}
		if len(inputs) > s.batchSize || total < 0 {
			return 0, E(CodeInternal, "invalid semantic input page")
		}
		if len(inputs) == 0 {
			if err := s.store.Complete(ctx, generation, owner, total); err != nil {
				return 0, err
			}
			return total, nil
		}
		members := make([]SemanticMember, len(inputs))
		texts := make([]string, len(inputs))
		digests := make([]string, 0, len(inputs))
		for i, input := range inputs {
			if input.Key <= after {
				return 0, E(CodeInternal, "semantic input order did not advance")
			}
			after = input.Key
			members[i].Key = input.Key
			text, digest, err := input.Document.Projection()
			if err != nil || len(text) > s.maxInputBytes {
				members[i].Failure = "input_too_large"
				continue
			}
			texts[i] = text
			members[i].InputDigest = digest
			digests = append(digests, digest)
		}
		known, err := s.store.KnownInputs(ctx, generation, digests)
		if err != nil {
			return 0, err
		}
		pending := make([]int, 0, len(inputs))
		firstInput := make(map[string]int, len(inputs))
		duplicates := make(map[int]int)
		for i := range members {
			if members[i].Failure != "" {
				continue
			}
			members[i].Reuse = known[members[i].InputDigest]
			if !members[i].Reuse {
				if first, ok := firstInput[members[i].InputDigest]; ok {
					duplicates[i] = first
					continue
				}
				firstInput[members[i].InputDigest] = i
				pending = append(pending, i)
			}
		}
		if err := s.embed(ctx, texts, members, pending); err != nil {
			return 0, err
		}
		for duplicate, first := range duplicates {
			members[duplicate].Failure = members[first].Failure
			members[duplicate].InputDigest = members[first].InputDigest
			members[duplicate].Reuse = members[first].Failure == ""
		}
		if err := s.store.SaveBatch(ctx, generation, owner, members); err != nil {
			return 0, err
		}
	}
}

// embed bisects only permanent input failures. At most 2*n-1 requests can be
// made for a rejected batch; transient/authentication failures end immediately.
func (s *SemanticIndexer) embed(ctx context.Context, texts []string, members []SemanticMember, indices []int) error {
	if len(indices) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	input := make([]string, len(indices))
	for i, index := range indices {
		input[i] = texts[index]
	}
	vectors, err := s.provider.Documents(ctx, input)
	if errors.Is(err, ErrEmbeddingRejected) {
		if len(indices) == 1 {
			m := &members[indices[0]]
			m.InputDigest = ""
			m.Failure = "input_rejected"
			return nil
		}
		middle := len(indices) / 2
		if err := s.embed(ctx, texts, members, indices[:middle]); err != nil {
			return err
		}
		return s.embed(ctx, texts, members, indices[middle:])
	}
	if err != nil {
		return err
	}
	if len(vectors) != len(indices) {
		return ErrEmbeddingInvalidResponse
	}
	for i, index := range indices {
		members[index].Vector = vectors[i]
	}
	return nil
}
