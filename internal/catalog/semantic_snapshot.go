// Snapshot ranking accepts only the publication owner's bounded, filtered
// immutable entries. It never reads private Control metadata for public search.
package catalog

import (
	"context"
	"errors"
	"time"
)

// SnapshotRanking retains transport-private retrieval details separately from
// standard ARD envelopes. Bind profile, generation and fingerprint to cursors.
type SnapshotRanking struct {
	Ranks          []HybridRank
	Profile        string
	Generation     string
	Fingerprint    string
	FallbackReason string
	PartialIndex   bool
}

// RankSnapshot performs exact comparison for a bounded immutable population.
// Fresh calls may explicitly fall back; callers reject fallback on continuation.
func (s *SemanticSearch) RankSnapshot(ctx context.Context, tenant, snapshot, sourceGeneration, text string, candidates []HybridCandidate) (ranking SnapshotRanking, resultErr error) {
	defer func() { s.observe("public", ranking.Profile != "", ranking.FallbackReason, resultErr) }()
	_, exactErr := ParseAIRIdentifier(text)
	if s == nil || s.excludesTenant(tenant) || text == "" || exactErr == nil {
		return SnapshotRanking{}, nil
	}
	if len(candidates) > MaxPublicationRecords {
		// A valid snapshot may also contain its Registry self-entry. Keep
		// fresh queries searchable without exceeding the comparison budget.
		return SnapshotRanking{FallbackReason: "work_limit"}, nil
	}
	g, ready, err := s.options.Store.Active(ctx, tenant, s.options.Profile, "public:"+snapshot, s.options.Dimensions)
	if err != nil {
		return SnapshotRanking{}, err
	}
	if !ready || g.SourceGeneration != sourceGeneration {
		return SnapshotRanking{FallbackReason: "indexing"}, nil
	}
	result := SnapshotRanking{Profile: s.options.RankingProfile, Generation: g.ID, Ranks: []HybridRank{}}
	if len(candidates) == 0 {
		return result, nil
	}
	timeout := s.options.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline)/2)
	}
	queryContext, cancel := context.WithTimeout(ctx, timeout)
	vector, err := s.options.Provider.Query(queryContext, text)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return SnapshotRanking{}, ctx.Err()
		}
		if errors.Is(err, ErrEmbeddingUnavailable) || errors.Is(err, ErrEmbeddingAuthentication) || errors.Is(err, ErrEmbeddingRejected) || errors.Is(err, ErrEmbeddingInvalidResponse) || errors.Is(err, context.DeadlineExceeded) {
			return SnapshotRanking{FallbackReason: "provider_unavailable"}, nil
		}
		return SnapshotRanking{}, err
	}
	keys := make([]string, len(candidates))
	for i, c := range candidates {
		keys[i] = c.ID
	}
	similarities, err := s.options.Store.Similarities(ctx, g, vector, keys)
	if errors.Is(err, ErrSearchGenerationChanged) {
		return SnapshotRanking{FallbackReason: "index_changed"}, nil
	}
	if err != nil {
		return SnapshotRanking{}, err
	}
	candidates = append([]HybridCandidate(nil), candidates...)
	for i := range candidates {
		if similarity, ok := similarities[candidates[i].ID]; ok {
			candidates[i].Similarity = &similarity
		} else {
			result.PartialIndex = true
		}
	}
	result.Ranks, err = RankHybrid(candidates, s.options.Cutoff)
	result.Fingerprint = semanticVectorFingerprint(vector)
	return result, err
}

// observe emits only fixed retrieval outcomes through a process-owned metric port.
func (s *SemanticSearch) observe(operation string, hybrid bool, fallback string, err error) {
	if s == nil || s.options.Observe == nil {
		return
	}
	outcome := "core"
	if hybrid {
		outcome = "hybrid"
	}
	if fallback != "" {
		outcome = fallback
	}
	if err != nil {
		outcome = "error"
	}
	s.options.Observe(operation, outcome)
}
