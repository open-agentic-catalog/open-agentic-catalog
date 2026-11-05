// Hybrid Explore uses automatic retrieval selection with the same local-only
// population and query/model-free rules as ordinary Catalog facets.
package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// semanticExplore never falls back after an authorization or core-store error.
// Empty successful hybrid facets remain empty rather than rerunning lexical.
func (s *Service) semanticExplore(ctx context.Context, actor identity.Actor, request ExploreRequest) (response ExploreResponse, used bool, fallback string, resultErr error) {
	defer func() { s.semantic.observe("explore", used, fallback, resultErr) }()
	_, exactErr := ParseAIRIdentifier(request.Query.Text)
	if s.semantic == nil || s.semantic.excludesTenant(actor.TenantID) || request.Query.Text == "" || exactErr == nil {
		return ExploreResponse{}, false, "", nil
	}
	options := s.semantic.options
	g, ready, err := options.Store.Active(ctx, actor.TenantID, options.Profile, "control", options.Dimensions)
	if err != nil {
		return ExploreResponse{}, true, "", err
	}
	if !ready {
		return ExploreResponse{}, false, "indexing", nil
	}
	timeout := options.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline)/2)
	}
	queryContext, cancel := context.WithTimeout(ctx, timeout)
	vector, err := options.Provider.Query(queryContext, request.Query.Text)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return ExploreResponse{}, true, "", ctx.Err()
		}
		if errors.Is(err, ErrEmbeddingUnavailable) || errors.Is(err, ErrEmbeddingAuthentication) || errors.Is(err, ErrEmbeddingRejected) || errors.Is(err, ErrEmbeddingInvalidResponse) || errors.Is(err, context.DeadlineExceeded) {
			return ExploreResponse{}, false, "provider_unavailable", nil
		}
		return ExploreResponse{}, true, "", err
	}
	response, err = options.Store.Explore(ctx, SemanticSearchRequest{Generation: g, Query: request.Query, Vector: vector, Cutoff: options.Cutoff, LocalOnly: true}, request.Facets)
	if errors.Is(err, ErrSearchGenerationChanged) {
		return ExploreResponse{}, false, "index_changed", nil
	}
	if ErrorCodeOf(err) == CodeWorkLimitExceeded {
		return ExploreResponse{}, false, "work_limit", nil
	}
	response.Tier = "hybrid"
	response.Profile = options.RankingProfile
	return response, true, "", err
}
