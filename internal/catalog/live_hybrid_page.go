package catalog

import (
	"context"
	"errors"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// replayLive never repeats a failed exchange. A client can retry the same signed
// position; no result is consumed until the complete replay is verified.
func (s *SearchService) replayLive(ctx context.Context, query SearchQuery, state *LiveCursor) (FederatedCatalogSearchPage, error) {
	liveSearcher, ok := s.federated.(FederatedCatalogLiveSearcher)
	if !ok {
		return FederatedCatalogSearchPage{}, E(CodeInternal, "live search is unavailable")
	}
	page, err := liveSearcher.SearchLiveCatalog(ctx, FederatedCatalogSearchRequest{Query: query, Limit: query.Limit, IncludeLive: true, LiveLimit: LiveSearchWindow})
	if err != nil && ctx.Err() != nil {
		return FederatedCatalogSearchPage{}, ctx.Err()
	}
	if state != nil {
		if err != nil {
			if ErrorCodeOf(err) == CodeForbidden || ErrorCodeOf(err) == CodeUnauthenticated || ErrorCodeOf(err) == CodeInvalidArgument {
				return FederatedCatalogSearchPage{}, err
			}
			return FederatedCatalogSearchPage{}, E(CodeDependencyUnavailable, "live search continuation is unavailable; retry the same cursor")
		}
		if page.ReplayFingerprint == "" {
			return FederatedCatalogSearchPage{}, E(CodeDependencyUnavailable, "live search continuation is unavailable; retry the same cursor")
		}
		if page.ReplayFingerprint != state.Fingerprint {
			return FederatedCatalogSearchPage{}, E(CodeInvalidArgument, "live search window changed; restart the search")
		}
	}
	if err != nil {
		return FederatedCatalogSearchPage{}, err
	}
	page.Live = deduplicateCatalogResults(page.Live)
	if len(page.Live) > LiveSearchWindow {
		return FederatedCatalogSearchPage{}, E(CodeInternal, "live search window exceeds its bound")
	}
	return page, nil
}

func (s *SearchService) searchLiveOnly(ctx context.Context, actor identity.Actor, query SearchQuery) (CatalogSearchResponse, error) {
	binding, err := s.local.unifiedSearchCursorBinding(ctx, actor, query)
	if err != nil {
		return CatalogSearchResponse{}, err
	}
	state := LiveCursor{Mode: "live", Surface: "control", Generation: "live", IndexedGeneration: "live", ExpiresAt: s.local.clock.Now().Add(catalogSearchCursorTTL).Unix()}
	var replay *LiveCursor
	if query.Cursor != "" {
		state, err = DecodeLiveCursor(query.Cursor, "control", s.local.cursorKey, binding, s.local.clock.Now())
		if err != nil {
			return CatalogSearchResponse{}, err
		}
		if state.Mode != "live" {
			return CatalogSearchResponse{}, E(CodeInvalidArgument, "live-only cursor mode is invalid")
		}
		replay = &state
	}
	page, err := s.replayLive(ctx, query, replay)
	if err != nil {
		return CatalogSearchResponse{}, err
	}
	if state.LiveOffset > len(page.Live) {
		return CatalogSearchResponse{}, E(CodeInvalidArgument, "live search window changed; restart the search")
	}
	sortCatalogResults(page.Live, query.Order)
	end := min(state.LiveOffset+query.Limit, len(page.Live))
	response := CatalogSearchResponse{Items: page.Live[state.LiveOffset:end], LiveCount: end - state.LiveOffset, Profile: CoreSearchProfile, Generation: combinedSearchGeneration("", ""), Tier: "core", Partial: page.Partial, Diagnostics: page.Diagnostics}
	state.LiveOffset, state.Fingerprint = end, page.ReplayFingerprint
	if end < len(page.Live) {
		if state.Fingerprint != "" {
			response.NextCursor, err = EncodeLiveCursor(state, s.local.cursorKey, binding)
		} else {
			markLiveLimited(&response)
		}
	}
	return response, err
}

func (s *SearchService) searchHybridLive(ctx context.Context, actor identity.Actor, query SearchQuery, state *LiveCursor) (CatalogSearchResponse, error) {
	page, _, _, err := s.local.semanticPageWithLive(ctx, actor, query, false, state)
	if err != nil {
		// Initial durable reads follow the same continuation contract as the
		// post-replay identity check; neither failure consumes a live result.
		if ctx.Err() != nil {
			return CatalogSearchResponse{}, ctx.Err()
		}
		if errors.Is(err, ErrSearchGenerationChanged) {
			return CatalogSearchResponse{}, E(CodeInvalidArgument, "hybrid search generation changed; restart the search")
		}
		if ErrorCodeOf(err) == CodeInvalidArgument || ErrorCodeOf(err) == CodeForbidden || ErrorCodeOf(err) == CodeUnauthenticated {
			return CatalogSearchResponse{}, err
		}
		return CatalogSearchResponse{}, E(CodeDependencyUnavailable, "live search continuation is unavailable; retry the same cursor")
	}
	return s.continueHybridLive(ctx, actor, query, page, state)
}

func (s *SearchService) continueHybridLive(ctx context.Context, actor identity.Actor, query SearchQuery, response CatalogSearchResponse, previous *LiveCursor) (CatalogSearchResponse, error) {
	live, err := s.replayLive(ctx, query, previous)
	if err != nil {
		if ctx.Err() != nil {
			return CatalogSearchResponse{}, ctx.Err()
		}
		if previous != nil || ErrorCodeOf(err) == CodeForbidden || ErrorCodeOf(err) == CodeUnauthenticated {
			return CatalogSearchResponse{}, err
		}
		response.Partial = true
		response.Diagnostics = append(response.Diagnostics, CatalogSearchDiagnostic{Status: "failed", Message: "Live federation was unavailable within the request deadline."})
		return response, nil
	}
	if response.semantic == nil {
		return CatalogSearchResponse{}, E(CodeInternal, "hybrid continuation state is unavailable")
	}
	meta := response.semantic
	if len(live.Live) > 0 {
		probes := make([]IndexedIdentity, 0, len(live.Live))
		for _, item := range live.Live {
			probes = append(probes, IndexedIdentity{item.Identifier, item.Version, item.VersionPresent})
		}
		matched, err := s.local.semantic.options.Store.Search(ctx, SemanticSearchRequest{Generation: meta.generation, Query: query, Vector: meta.vector, Cutoff: s.local.semantic.options.Cutoff, IdentityProbes: probes})
		if err != nil {
			if ctx.Err() != nil {
				return CatalogSearchResponse{}, ctx.Err()
			}
			if errors.Is(err, ErrSearchGenerationChanged) {
				return CatalogSearchResponse{}, E(CodeInvalidArgument, "hybrid search generation changed; restart the search")
			}
			if ErrorCodeOf(err) == CodeInvalidArgument || ErrorCodeOf(err) == CodeForbidden || ErrorCodeOf(err) == CodeUnauthenticated {
				return CatalogSearchResponse{}, err
			}
			if previous != nil {
				return CatalogSearchResponse{}, E(CodeDependencyUnavailable, "live search continuation is unavailable; retry the same cursor")
			}
			response.Partial = true
			response.Diagnostics = append(response.Diagnostics, CatalogSearchDiagnostic{Status: "failed", Message: "Live identities could not be checked within the request deadline."})
			return response, nil
		}
		keys := make(map[IndexedIdentity]bool, len(matched.Matches))
		for _, key := range matched.Matches {
			keys[key] = true
		}
		unshadowed := make([]CatalogSearchResult, 0, len(live.Live))
		for _, item := range live.Live {
			if !keys[IndexedIdentity{item.Identifier, item.Version, item.VersionPresent}] {
				unshadowed = append(unshadowed, item)
			}
		}
		live.Live = unshadowed
	}
	state := LiveCursor{Mode: "hybrid", Surface: "control", Generation: meta.cursor.Generation, IndexedGeneration: meta.cursor.SourceGeneration, Ranking: response.Profile, VectorFingerprint: meta.cursor.VectorFingerprint, ExpiresAt: meta.cursor.ExpiresAt, Fingerprint: live.ReplayFingerprint}
	if meta.cursor.Position.ID != "" {
		state.HybridPosition = &meta.cursor.Position
	}
	if previous != nil {
		state.LiveOffset = previous.LiveOffset
	}
	if state.LiveOffset > len(live.Live) {
		return CatalogSearchResponse{}, E(CodeInvalidArgument, "live search window changed; restart the search")
	}
	response.Partial, response.Diagnostics = live.Partial, live.Diagnostics
	// All durable ranks precede the live tail. Display scores from independent
	// registries are never used to interleave these two relevance populations.
	if response.NextCursor == "" {
		end := min(state.LiveOffset+query.Limit-len(response.Items), len(live.Live))
		response.Items = append(response.Items, live.Live[state.LiveOffset:end]...)
		response.LiveCount = end - state.LiveOffset
		state.LiveOffset = end
	}
	if state.LiveOffset < len(live.Live) {
		if state.Fingerprint == "" {
			markLiveLimited(&response)
		} else {
			binding, err := s.local.unifiedSearchCursorBinding(ctx, actor, query)
			if err != nil {
				return CatalogSearchResponse{}, err
			}
			response.NextCursor, err = EncodeLiveCursor(state, s.local.cursorKey, binding)
			if err != nil {
				return CatalogSearchResponse{}, err
			}
		}
	}
	return response, nil
}

func markLiveLimited(response *CatalogSearchResponse) {
	response.Partial = true
	response.Diagnostics = append(response.Diagnostics, CatalogSearchDiagnostic{Status: "live_results_limited", Message: "Additional live results could not be safely continued; restart the search."})
}
