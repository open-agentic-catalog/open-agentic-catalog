package publication

import (
	"sort"
	"strconv"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// NeedsLiveReplay excludes ordinary durable continuation. Composite tokens
// require a fresh verified window in either core or hybrid mode.
func (response SearchResponse) NeedsLiveReplay(first bool) bool {
	return first || response.page.live != nil && response.page.live.Fingerprint != ""
}

// ContinueLiveSearch merges a verified bounded window with the current local
// page. Local offsets count only emitted local results, including an all-live
// page. The live offset counts only projected, unshadowed live results.
func (m *Manager) ContinueLiveSearch(response SearchResponse, live []SearchResult, fingerprint string) (SearchResponse, error) {
	page := response.page
	if (response.Tier != "core" && response.Tier != "hybrid") || page.limit < 1 || len(live) > catalog.LiveSearchWindow {
		return SearchResponse{}, catalog.E(catalog.CodeInternal, "invalid live continuation state")
	}
	state := catalog.LiveCursor{Surface: "public", Generation: strconv.FormatInt(page.generation, 10), ExpiresAt: page.expiresAt}
	if page.live != nil {
		state = *page.live
		if fingerprint == "" {
			return SearchResponse{}, catalog.E(catalog.CodeDependencyUnavailable, "live search continuation is unavailable; retry the same token")
		}
		if fingerprint != state.Fingerprint {
			return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "live search window changed; restart the search")
		}
	}
	if state.LiveOffset > len(live) {
		return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "live search window changed; restart the search")
	}
	if response.Tier == "hybrid" {
		state.Mode, state.Ranking = "hybrid", page.prefix
	}
	results := append([]SearchResult{}, response.Results...)
	if response.Tier == "core" || page.offset+page.count == page.total {
		results = append(results, live[state.LiveOffset:]...)
	}
	if response.Tier == "core" {
		sort.SliceStable(results, func(i, j int) bool {
			a, b := results[i], results[j]
			if a.Score != b.Score {
				return a.Score > b.Score
			}
			if a.Identifier != b.Identifier {
				return a.Identifier < b.Identifier
			}
			if a.Version != b.Version {
				return a.Version < b.Version
			}
			return a.Source < b.Source
		})
	}
	response.Results = results[:min(page.limit, len(results))]
	emittedLocal, emittedLive := 0, 0
	for _, result := range response.Results {
		if response.ContainsLocalIdentity(result.Identifier, result.Version) {
			emittedLocal++
		} else {
			emittedLive++
		}
	}
	state.Offset = page.offset + emittedLocal
	state.LiveOffset += emittedLive
	state.Fingerprint = fingerprint
	response.PageToken = ""
	if state.LiveOffset < len(live) && fingerprint != "" {
		token, err := catalog.EncodeLiveCursor(state, m.cursorKey, page.baseBinding)
		if err != nil {
			return SearchResponse{}, err
		}
		response.PageToken = token
	} else {
		if state.LiveOffset < len(live) {
			response.Partial = true
			response.Diagnostics = append(response.Diagnostics, SourceDiagnostic{Source: "federation", Status: "live_results_limited", Message: "Additional live results could not be safely continued; restart the search."})
		}
		if state.Offset < page.total {
			token, err := m.SearchContinuation(response, emittedLocal)
			if err != nil {
				return SearchResponse{}, err
			}
			response.PageToken = token
		}
	}
	return response, nil
}
