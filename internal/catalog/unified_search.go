// Unified search composes local releases and controlled federated observations
// under a common query, authorization and continuation-generation contract.

package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// SearchService merges local published records, indexed observations, and the
// live results under a signed bounded-window continuation contract.
type SearchService struct {
	local     *Service
	federated FederatedCatalogSearcher
}

// NewSearchService requires both local Catalog reads and the controlled
// federation search port.
func NewSearchService(local *Service, federated FederatedCatalogSearcher) (*SearchService, error) {
	if local == nil || federated == nil {
		return nil, errors.New("catalog search dependencies are required")
	}
	return &SearchService{local: local, federated: federated}, nil
}

// GetIndexed resolves a durable external bookmark through the same current
// eligibility and tenant boundary as indexed-only Search.
func (s *SearchService) GetIndexed(ctx context.Context, selected IndexedIdentity) (CatalogSearchResult, error) {
	ctx, _, err := collectionContext(ctx)
	if err != nil {
		return CatalogSearchResult{}, err
	}
	_, err = requirePermission(ctx, "federation.discover", "")
	if err != nil {
		return CatalogSearchResult{}, err
	}
	identifier, err := ParseAIRIdentifier(selected.Identifier)
	if err != nil || identifier.String() != selected.Identifier || len(selected.Identifier) > 512 || !validTextLength(selected.Version, 128) || !selected.VersionPresent && selected.Version != "" {
		return CatalogSearchResult{}, E(CodeInvalidArgument, "indexed identity is invalid")
	}
	page, err := s.federated.SearchFederatedCatalog(ctx, FederatedCatalogSearchRequest{Query: SearchQuery{Origins: []SearchResultType{SearchResultIndexed}}, Limit: 1, ExactIdentity: &selected})
	if err != nil {
		return CatalogSearchResult{}, err
	}
	if len(page.Indexed) == 0 {
		return CatalogSearchResult{}, E(CodeNotFound, "indexed catalog record was not found")
	}
	item := page.Indexed[0]
	if len(page.Indexed) != 1 || item.Identifier != selected.Identifier || item.Version != selected.Version || item.VersionPresent != selected.VersionPresent || item.Type != SearchResultIndexed || item.Indexed == nil || item.Local != nil || item.Live != nil {
		return CatalogSearchResult{}, E(CodeInternal, "indexed projection did not match the selected identity")
	}
	return item, nil
}

// Search queries each durable source at the same logical position and merges
// their stable order. Signed live cursors replay one unchanged bounded window.
// Hybrid results retain a durable prefix followed by a separate live tail.
func (s *SearchService) Search(ctx context.Context, query SearchQuery) (response CatalogSearchResponse, resultErr error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return CatalogSearchResponse{}, err
	}
	ctx, endTrace := StartTrace(ctx, "catalog search", TraceInternal, "")
	defer func() {
		outcome := "ok"
		if resultErr != nil {
			outcome = "error"
		} else if response.Partial {
			outcome = "partial"
		}
		endTrace(outcome)
	}()
	if err := validateCatalogSearchQuery(query); err != nil {
		return CatalogSearchResponse{}, err
	}

	// Default discovery is the caller's authorized population. Explicit external
	// origins require tenant federation authority before any provider/source work.
	federationAllowed, err := identity.CollectionAllows(ctx, "federation.discover", "")
	if err != nil {
		return CatalogSearchResponse{}, PermissionError("federation admission failed", err)
	}
	if !federationAllowed {
		if len(query.Origins) != 0 && (query.IncludesOrigin(SearchResultIndexed) || query.IncludesOrigin(SearchResultLive)) {
			return CatalogSearchResponse{}, E(CodeForbidden, "federation discovery permission is required")
		}
		query.Origins = []SearchResultType{SearchResultLocal}
	}
	query = normalizeCatalogSearchQuery(query)
	if query.Limit <= 0 {
		query.Limit = 20
	}
	if !query.IncludesOrigin(SearchResultLocal) && !query.IncludesOrigin(SearchResultIndexed) {
		return s.searchLiveOnly(ctx, actor, query)
	}
	if strings.HasPrefix(query.Cursor, LiveCursorPrefix) {
		binding, err := s.local.unifiedSearchCursorBinding(ctx, actor, query)
		if err != nil {
			return CatalogSearchResponse{}, err
		}
		state, err := DecodeLiveCursor(query.Cursor, "control", s.local.cursorKey, binding, s.local.clock.Now())
		if err != nil {
			return CatalogSearchResponse{}, err
		}
		if state.Mode == "hybrid" {
			return s.searchHybridLive(ctx, actor, query, &state)
		}
		if state.Mode != "" {
			return CatalogSearchResponse{}, E(CodeInvalidArgument, "catalog cursor mode is invalid")
		}
	}
	hybrid, used, fallback, err := s.local.semanticPage(ctx, actor, query, false)
	if err != nil {
		return CatalogSearchResponse{}, err
	}
	if used {
		if query.Cursor == "" && query.IncludesOrigin(SearchResultLive) {
			return s.continueHybridLive(ctx, actor, query, hybrid, nil)
		}
		return hybrid, nil
	}
	return s.searchCore(ctx, actor, query, fallback)
}

// searchPages uses the repository's shared durable snapshot where available.
// Independent stores retain bounded generation reconciliation. Live discovery
// is always outside the durable transaction and is never retried here.
func (s *SearchService) searchPages(ctx context.Context, tenantID string, localRequest CatalogSearchRepositoryRequest, federatedRequest FederatedCatalogSearchRequest, hasCursor bool) (CatalogSearchRepositoryPage, FederatedCatalogSearchPage, error) {
	if !federatedRequest.Query.IncludesOrigin(SearchResultIndexed) && !federatedRequest.Query.IncludesOrigin(SearchResultLive) {
		local, err := s.local.repo.SearchCatalog(ctx, tenantID, localRequest)
		return local, FederatedCatalogSearchPage{Indexed: []CatalogSearchResult{}, Generation: local.Generation}, err
	}

	if snapshotReader, ok := s.local.repo.(CatalogSearchSnapshotReader); ok {
		if liveSearcher, ok := s.federated.(FederatedCatalogLiveSearcher); ok {
			snapshot, err := snapshotReader.SearchCatalogSnapshot(ctx, tenantID, localRequest, federatedRequest)
			if err != nil {
				return CatalogSearchRepositoryPage{}, FederatedCatalogSearchPage{}, err
			}
			if snapshot.Local.Generation == "" || snapshot.Local.Generation != snapshot.Indexed.Generation {
				return CatalogSearchRepositoryPage{}, FederatedCatalogSearchPage{}, E(CodeInternal, "durable search snapshot is inconsistent")
			}
			if err := ctx.Err(); err != nil {
				return CatalogSearchRepositoryPage{}, FederatedCatalogSearchPage{}, err
			}
			live := FederatedCatalogSearchPage{}
			if federatedRequest.IncludeLive {
				live, err = liveSearcher.SearchLiveCatalog(ctx, federatedRequest)
			}
			if err != nil {
				return CatalogSearchRepositoryPage{}, FederatedCatalogSearchPage{}, err
			}
			snapshot.Indexed.Live, snapshot.Indexed.Partial, snapshot.Indexed.Diagnostics = live.Live, live.Partial, live.Diagnostics
			snapshot.Indexed.ReplayFingerprint = live.ReplayFingerprint
			return snapshot.Local, snapshot.Indexed, nil
		}
	}
	localPage, err := s.local.repo.SearchCatalog(ctx, tenantID, localRequest)
	if err != nil {
		return CatalogSearchRepositoryPage{}, FederatedCatalogSearchPage{}, err
	}
	externalPage, err := s.federated.SearchFederatedCatalog(ctx, federatedRequest)
	if err != nil {
		return CatalogSearchRepositoryPage{}, FederatedCatalogSearchPage{}, err
	}
	if !hasCursor && sharedNumericSearchGeneration(localPage.Generation, externalPage.Generation) && localPage.Generation != externalPage.Generation {
		// A second commit can race the existing local re-read. Refresh only
		// durable indexed state before another bounded attempt; never repeat
		// outbound live discovery or reinterpret a client continuation.
		for attempt := 0; attempt < 3; attempt++ {
			if err := ctx.Err(); err != nil {
				return CatalogSearchRepositoryPage{}, FederatedCatalogSearchPage{}, err
			}
			localRequest.ExpectedGeneration = externalPage.Generation
			localPage, err = s.local.repo.SearchCatalog(ctx, tenantID, localRequest)
			if err == nil {
				break
			}
			if !errors.Is(err, ErrSearchGenerationChanged) || attempt == 2 {
				return CatalogSearchRepositoryPage{}, FederatedCatalogSearchPage{}, err
			}
			federatedRequest.IncludeLive = false
			refreshed, err := s.federated.SearchFederatedCatalog(ctx, federatedRequest)
			if err != nil {
				return CatalogSearchRepositoryPage{}, FederatedCatalogSearchPage{}, err
			}
			externalPage.Indexed = refreshed.Indexed
			externalPage.IndexedTotal = refreshed.IndexedTotal
			externalPage.HasMore = refreshed.HasMore
			externalPage.Generation = refreshed.Generation
		}
	}
	return localPage, externalPage, nil
}

// deduplicateCatalogResults applies local-result precedence while retaining
// distinct external identities and version-presence semantics.
func deduplicateCatalogResults(items []CatalogSearchResult) []CatalogSearchResult {
	groups := make(map[string][]CatalogSearchResult, len(items))
	keys := make([]string, 0, len(items))
	for _, item := range items {
		key := item.Identifier + "\xff"
		if item.VersionPresent {
			key += "1\xff" + item.Version
		} else {
			key += "0\xff"
		}
		if _, exists := groups[key]; !exists {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], item)
	}
	selected := make([]CatalogSearchResult, 0, len(groups))
	for _, key := range keys {
		group := groups[key]
		sortCatalogResults(group)
		choice := group[0]
		for _, candidate := range group {
			if candidate.Type == SearchResultLocal {
				choice = candidate
				break
			}
			if choice.Type == SearchResultLive && candidate.Type == SearchResultIndexed {
				choice = candidate
			}
		}
		selected = append(selected, choice)
	}
	sortCatalogResults(selected)
	return selected
}

// sharedNumericSearchGeneration reports whether both repositories expose the
// same numeric generation.
func sharedNumericSearchGeneration(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	_, leftErr := strconv.ParseUint(left, 10, 64)
	_, rightErr := strconv.ParseUint(right, 10, 64)
	return leftErr == nil && rightErr == nil
}

// localCatalogSearchResult projects a governed release into the shared
// local/federated result shape.
func localCatalogSearchResult(record CatalogSummary) CatalogSearchResult {
	record.Source = "local"
	return CatalogSearchResult{
		ID: "local:" + record.Release.ID, Type: SearchResultLocal, Identifier: record.Resource.CanonicalIdentifier,
		Version: record.Version.Version, VersionPresent: true, Kind: record.Resource.Kind,
		DisplayName: record.ListingRevision.DisplayName, Description: record.ListingRevision.Description,
		Tags: append([]string{}, record.ListingRevision.Tags...), Publisher: record.ListingRevision.PublisherID,
		Score: record.RelevanceScore, Local: new(record.SearchLocal()), Artwork: record.Artwork,
	}
}

// sortCatalogResults applies deterministic descending-score, normalized-name
// and result-ID ordering.
func sortCatalogResults(items []CatalogSearchResult, order ...SearchOrder) {
	sort.Slice(items, func(i, j int) bool {
		return searchPositionAfter(CatalogSearchResultPosition(items[j], order...), CatalogSearchResultPosition(items[i], order...), order...)
	})
}

// combinedSearchGeneration binds local and indexed generations into one
// continuation fingerprint.
func combinedSearchGeneration(local, indexed string) string {
	digest := sha256.Sum256([]byte("catalog-unified-search-generation-v1\x00" + local + "\x00" + indexed))
	return "sha256:" + hex.EncodeToString(digest[:])
}
