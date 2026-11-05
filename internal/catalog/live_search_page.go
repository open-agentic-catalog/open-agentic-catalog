package catalog

import (
	"context"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// searchCore keeps the durable read closed during outbound work. Only a signed
// verified live window permits replay; an ordinary durable cursor never does.
func (s *SearchService) searchCore(ctx context.Context, actor identity.Actor, query SearchQuery, fallback string) (CatalogSearchResponse, error) {
	binding, err := s.local.unifiedSearchCursorBinding(ctx, actor, query)
	if err != nil {
		return CatalogSearchResponse{}, err
	}
	state := LiveCursor{Surface: "control", ExpiresAt: s.local.clock.Now().Add(catalogSearchCursorTTL).Unix()}
	hasCursor, composite := query.Cursor != "", strings.HasPrefix(query.Cursor, LiveCursorPrefix)
	if composite {
		state, err = DecodeLiveCursor(query.Cursor, "control", s.local.cursorKey, binding, s.local.clock.Now())
	} else if hasCursor {
		var old catalogSearchCursor
		old, _, err = s.local.decodeSearchCursor(query.Cursor, binding)
		state.Generation, state.IndexedGeneration, state.ExpiresAt = old.Generation, old.IndexedGeneration, old.ExpiresAt
		state.Position = &CatalogSearchPosition{Score: old.Score, DisplayName: old.DisplayName, ResultID: old.ResultID, SortValue: old.SortValue}
	}
	if err != nil {
		return CatalogSearchResponse{}, err
	}
	if hasCursor && state.IndexedGeneration == "" {
		return CatalogSearchResponse{}, E(CodeInvalidArgument, "catalog cursor is invalid for unified search")
	}
	localRequest := CatalogSearchRepositoryRequest{Query: query, Limit: query.Limit + 1, After: state.Position, ExpectedGeneration: state.Generation}
	includeLive := query.IncludesOrigin(SearchResultLive) && (!hasCursor || composite && state.Fingerprint != "")
	federatedRequest := FederatedCatalogSearchRequest{Query: query, Limit: query.Limit + 1, After: state.Position, ExpectedGeneration: state.IndexedGeneration, IncludeLive: includeLive, LiveLimit: LiveSearchWindow}
	local, external, err := s.searchPages(ctx, actor.TenantID, localRequest, federatedRequest, hasCursor)
	if err != nil {
		if composite && state.Fingerprint != "" && ErrorCodeOf(err) != CodeInvalidArgument && ErrorCodeOf(err) != CodeForbidden && ErrorCodeOf(err) != CodeUnauthenticated {
			return CatalogSearchResponse{}, E(CodeDependencyUnavailable, "live search continuation is unavailable; retry the same cursor")
		}
		return CatalogSearchResponse{}, err
	}
	if hasCursor && (local.Generation != state.Generation || external.Generation != state.IndexedGeneration) {
		return CatalogSearchResponse{}, E(CodeInvalidArgument, "catalog generation changed; restart the search")
	}
	if composite && state.Fingerprint != "" {
		if external.ReplayFingerprint == "" {
			return CatalogSearchResponse{}, E(CodeDependencyUnavailable, "live search continuation is unavailable; retry the same cursor")
		}
		if state.Fingerprint != external.ReplayFingerprint {
			return CatalogSearchResponse{}, E(CodeInvalidArgument, "live search window changed; restart the search")
		}
	}
	live := []CatalogSearchResult{}
	if includeLive {
		live = deduplicateCatalogResults(external.Live)
	}
	if len(live) > LiveSearchWindow {
		return CatalogSearchResponse{}, E(CodeInternal, "live search window exceeds its bound")
	}
	if len(live) > 0 {
		sortCatalogResults(live, query.Order)
		live, err = s.unshadowedLive(ctx, actor.TenantID, query, live, local.Generation, external.Generation)
		if err != nil {
			// The post-replay durable check has the same retry contract as the
			// initial page read. Stale cursors and access refusals stay final.
			if composite && state.Fingerprint != "" && ErrorCodeOf(err) != CodeInvalidArgument && ErrorCodeOf(err) != CodeForbidden && ErrorCodeOf(err) != CodeUnauthenticated {
				return CatalogSearchResponse{}, E(CodeDependencyUnavailable, "live search continuation is unavailable; retry the same cursor")
			}
			return CatalogSearchResponse{}, err
		}
	}
	if state.LiveOffset > len(live) {
		return CatalogSearchResponse{}, E(CodeInvalidArgument, "live search window changed; restart the search")
	}
	durable := make([]CatalogSearchResult, 0, len(local.Items)+len(external.Indexed))
	for _, item := range local.Items {
		durable = append(durable, localCatalogSearchResult(item))
	}
	durable = append(durable, external.Indexed...)
	all := deduplicateCatalogResults(append(durable, live[state.LiveOffset:]...))
	if query.Order != "" {
		// Keep the sorted durable population ahead of the separately sorted live window.
		durable = deduplicateCatalogResults(durable)
		sortCatalogResults(durable, query.Order)
		all = durable
		all = append(all, live[state.LiveOffset:]...)
	}
	selected := all[:min(query.Limit, len(all))]
	liveCount := 0
	for _, item := range selected {
		if item.Type == SearchResultLive {
			liveCount++
		} else {
			p := CatalogSearchResultPosition(item, query.Order)
			state.Position = &p
		}
	}
	durableMore := local.HasMore || external.HasMore
	for _, item := range all[len(selected):] {
		if item.Type != SearchResultLive {
			durableMore = true
		}
	}
	state.LiveOffset += liveCount
	liveMore := state.LiveOffset < len(live)
	diagnostics, partial := external.Diagnostics, external.Partial
	state.Generation, state.IndexedGeneration = local.Generation, external.Generation
	state.Fingerprint = external.ReplayFingerprint
	if !liveMore {
		state.Fingerprint, state.LiveOffset = "", 0
	}
	if liveMore && state.Fingerprint == "" {
		partial = true
		diagnostics = append(diagnostics, CatalogSearchDiagnostic{Status: "live_results_limited", Message: "Additional live results could not be safely continued; restart the search."})
	}
	if state.Fingerprint == "" {
		state.LiveOffset = 0
	}
	next := ""
	if durableMore || liveMore && state.Fingerprint != "" {
		next, err = EncodeLiveCursor(state, s.local.cursorKey, binding)
		if err != nil {
			return CatalogSearchResponse{}, err
		}
	}
	return CatalogSearchResponse{Items: selected, NextCursor: next, DurableTotal: local.Total + external.IndexedTotal, LiveCount: liveCount, Profile: CoreSearchProfile, Generation: combinedSearchGeneration(local.Generation, external.Generation), Tier: "core", FallbackReason: fallback, Partial: partial, Diagnostics: diagnostics}, nil
}

// unshadowedLive checks the whole selected matching durable population for at
// most 50 exact identities. A separate short snapshot must match the page's
// generations; neither read holds a transaction across an HTTP request.
func (s *SearchService) unshadowedLive(ctx context.Context, tenant string, query SearchQuery, live []CatalogSearchResult, localGeneration, indexedGeneration string) ([]CatalogSearchResult, error) {
	probes := make([]IndexedIdentity, 0, len(live))
	for _, item := range live {
		probes = append(probes, IndexedIdentity{item.Identifier, item.Version, item.VersionPresent})
	}
	local, indexed, err := s.searchPages(ctx, tenant,
		CatalogSearchRepositoryRequest{Query: query, IdentityProbes: probes, ExpectedGeneration: localGeneration},
		FederatedCatalogSearchRequest{Query: query, IdentityProbes: probes, ExpectedGeneration: indexedGeneration}, true)
	if err != nil {
		return nil, err
	}
	if local.Generation != localGeneration || indexed.Generation != indexedGeneration {
		return nil, E(CodeInvalidArgument, "catalog generation changed; restart the search")
	}
	matched := make(map[IndexedIdentity]bool, len(local.Matches)+len(indexed.Matches))
	for _, key := range local.Matches {
		matched[key] = true
	}
	for _, key := range indexed.Matches {
		matched[key] = true
	}
	result := make([]CatalogSearchResult, 0, len(live))
	for _, item := range live {
		if !matched[IndexedIdentity{item.Identifier, item.Version, item.VersionPresent}] {
			result = append(result, item)
		}
	}
	return result, nil
}
