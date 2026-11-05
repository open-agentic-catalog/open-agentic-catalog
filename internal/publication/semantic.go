// Optional publication ranking operates on the active ARD entries only. Mode
// details are internal/header metadata, not additions to the pinned ARD body.
package publication

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// rankSemantic filters before comparison while retaining snapshot ordinal keys.
func (m *Manager) rankSemantic(ctx context.Context, tenant string, snapshot Snapshot, query SearchQuery, native []nativeProjection) ([]SearchResult, catalog.SnapshotRanking, error) {
	if m.semantic == nil {
		return nil, catalog.SnapshotRanking{}, nil
	}
	candidates := make([]catalog.HybridCandidate, 0, len(snapshot.ARD.Entries))
	entries := make(map[string]Entry, len(snapshot.ARD.Entries))
	for i, entry := range snapshot.ARD.Entries {
		if !matchFilter(entry, query.Filter, nativeAt(native, i)) {
			continue
		}
		key := fmt.Sprintf("entry:%010d", i+1)
		entries[key] = entry
		// Ordinal ties follow the deterministic retained snapshot order.
		candidates = append(candidates, catalog.HybridCandidate{ID: key, SortName: strings.ToLower(entry.DisplayName), LexicalScore: CoreLexicalScore(entry, query.Text), Exact: entry.Identifier == query.Text})
	}
	ranking, err := m.semantic.RankSnapshot(ctx, tenant, snapshot.ID, strconv.FormatInt(snapshot.Generation, 10), query.Text, candidates)
	if err != nil {
		return nil, ranking, err
	}
	results := make([]SearchResult, 0, len(ranking.Ranks))
	for _, rank := range ranking.Ranks {
		results = append(results, SearchResult{Entry: entries[rank.ID], Score: rank.Score, Source: m.baseURL + "/ard/v1/"})
	}
	return results, ranking, nil
}

// semanticTokenParts parses only transport metadata; decodeCursor verifies the
// signature over all of it before any token position is accepted.
func semanticTokenParts(token string) (prefix, inner string) {
	if !strings.HasPrefix(token, "h1:") {
		return "", token
	}
	parts := strings.SplitN(token, ":", 5)
	if len(parts) != 5 {
		return "invalid", token
	}
	return strings.Join(parts[:4], ":") + ":", parts[4]
}
