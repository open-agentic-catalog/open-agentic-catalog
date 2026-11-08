package catalog

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func propertySearchName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

// This comparator is the literal contract: reversing the primary column never
// reverses the normalized-name and result-ID tie breakers.
func propertySearchLess(a, b CatalogSearchResult, order SearchOrder) bool {
	var left, right string
	switch order {
	case "":
		if a.Score != b.Score {
			return a.Score > b.Score
		}
	case SearchOrderNameAsc, SearchOrderNameDesc:
		left, right = propertySearchName(a.DisplayName), propertySearchName(b.DisplayName)
	case SearchOrderKindAsc, SearchOrderKindDesc:
		left, right = string(a.Kind), string(b.Kind)
	case SearchOrderPublisherAsc, SearchOrderPublisherDesc:
		left, right = propertySearchName(a.Publisher), propertySearchName(b.Publisher)
	}
	if left != right {
		if strings.HasSuffix(string(order), "_desc") {
			return left > right
		}
		return left < right
	}
	if propertySearchName(a.DisplayName) != propertySearchName(b.DisplayName) {
		return propertySearchName(a.DisplayName) < propertySearchName(b.DisplayName)
	}
	return a.ID < b.ID
}

func TestPropertySearchCompleteLexicalTraversal(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		order := rapid.SampledFrom([]SearchOrder{"", SearchOrderNameAsc, SearchOrderNameDesc, SearchOrderKindAsc, SearchOrderKindDesc, SearchOrderPublisherAsc, SearchOrderPublisherDesc}).Draw(rt, "order")
		limit := rapid.IntRange(1, 3).Draw(rt, "page size")
		count := rapid.IntRange(6, 12).Draw(rt, "identity groups")
		all, locals, indexed := []CatalogSearchResult{}, []CatalogSummary{}, []CatalogSearchResult{}
		expected := make([]CatalogSearchResult, 0, count)
		for i := range count {
			identifier := fmt.Sprintf("urn:air:property.example:demo:item-%d", i/3)
			version, present := "", i%3 != 0
			if i%3 == 2 {
				version = "1"
			}
			name := rapid.SampledFrom([]string{" Alpha ", "ALPHA", "Éclair", "界", "beta"}).Draw(rt, "name")
			score := rapid.IntRange(1, 3).Draw(rt, "score")
			item := CatalogSearchResult{ID: IndexedSearchResultID(identifier, version, present), Type: SearchResultIndexed,
				Identifier: identifier, Version: version, VersionPresent: present, DisplayName: name,
				Kind: []Kind{KindAgent, KindMCPServer, KindSkill, KindPlugin}[i%4], Score: score,
				Publisher: "publisher-local", Tags: []string{"keep"}, Indexed: &IndexedSearchEvidence{}}
			all = append(all, item)
			// Every group has a lower-precedence live copy, with a deliberately
			// greater score. Identity includes omitted versus present-empty version.
			live := item
			live.ID, live.Type, live.Score = LiveSearchResultID("https://source.example", identifier, version, present, "sha256:"+strings.Repeat("a", 64)), SearchResultLive, 100
			live.Indexed, live.Live = nil, &LiveSearchEvidence{}
			all = append(all, live)
			winner := item
			if present && version != "" && i%2 == 0 {
				local := unifiedLocalRecord(fmt.Sprintf("release-%d", i), identifier, name, score)
				local.Resource.Kind = item.Kind
				local.ListingRevision.Tags = []string{"keep"}
				locals = append(locals, local)
				winner.ID, winner.Type, winner.Indexed = "local:"+local.Release.ID, SearchResultLocal, nil
				winner.Local = new(local.SearchLocal())
				all = append(all, winner)
			} else {
				indexed = append(indexed, item)
			}
			expected = append(expected, winner)
		}
		// Independently selected winners verify precedence before exercising the
		// paged service ports, whose durable rows already exclude shadowed copies.
		dedup := deduplicateCatalogResults(rapid.Permutation(all).Draw(rt, "source order"))
		wantIDs, gotIDs := make([]string, 0, len(expected)), make([]string, 0, len(dedup))
		for _, item := range expected {
			wantIDs = append(wantIDs, item.ID)
		}
		for _, item := range dedup {
			gotIDs = append(gotIDs, item.ID)
		}
		slices.Sort(wantIDs)
		slices.Sort(gotIDs)
		if !reflect.DeepEqual(wantIDs, gotIDs) {
			rt.Fatalf("typed identity precedence: got %v want %v", gotIDs, wantIDs)
		}
		sort.Slice(expected, func(i, j int) bool { return propertySearchLess(expected[i], expected[j], order) })
		// Compare the opaque decoded position to an independently materialized
		// tuple; do not reuse production position/sorting helpers in the ports.
		after := func(item CatalogSearchResult, p *CatalogSearchPosition) bool {
			if p == nil {
				return true
			}
			previous := CatalogSearchResult{ID: p.ResultID, DisplayName: p.DisplayName, Score: p.Score, Publisher: p.SortValue, Kind: Kind(p.SortValue)}
			return propertySearchLess(previous, item, order)
		}
		generation := "42"
		repo := &unifiedSearchRepository{search: func(_ context.Context, tenant string, request CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
			if tenant != "tenant-1" || !slices.Equal(request.Query.Tags, []string{"keep"}) || request.Query.Order != order {
				rt.Fatal("local query lost its tenant/filter/order context")
			}
			items := []CatalogSummary{}
			for _, row := range locals {
				item := CatalogSearchResult{ID: "local:" + row.Release.ID, DisplayName: row.ListingRevision.DisplayName, Score: row.RelevanceScore, Kind: row.Resource.Kind, Publisher: row.ListingRevision.PublisherID}
				if after(item, request.After) {
					items = append(items, row)
				}
			}
			projection := func(row CatalogSummary) CatalogSearchResult {
				return CatalogSearchResult{ID: "local:" + row.Release.ID, DisplayName: row.ListingRevision.DisplayName, Score: row.RelevanceScore, Kind: row.Resource.Kind, Publisher: row.ListingRevision.PublisherID}
			}
			sort.Slice(items, func(i, j int) bool { return propertySearchLess(projection(items[i]), projection(items[j]), order) })
			more := len(items) > request.Limit
			return CatalogSearchRepositoryPage{Items: items[:min(request.Limit, len(items))], HasMore: more, Total: len(locals), Generation: generation}, nil
		}}
		external := unifiedFederatedSearcher{search: func(_ context.Context, request FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
			if !slices.Equal(request.Query.Tags, []string{"keep"}) || request.Query.Order != order {
				rt.Fatal("indexed query lost its filter/order context")
			}
			items := []CatalogSearchResult{}
			for _, row := range indexed {
				if after(row, request.After) {
					items = append(items, row)
				}
			}
			sort.Slice(items, func(i, j int) bool { return propertySearchLess(items[i], items[j], order) })
			more := len(items) > request.Limit
			return FederatedCatalogSearchPage{Indexed: items[:min(request.Limit, len(items))], HasMore: more, IndexedTotal: len(indexed), Generation: generation}, nil
		}}
		service := newUnifiedSearchTestService(t, repo, external)
		query := SearchQuery{Limit: limit, Order: order, Tags: []string{"keep"}}
		visited, cursors := []string{}, map[string]bool{}
		firstCursor := ""
		pages := (count + limit - 1) / limit
		for number := range pages {
			page, err := service.Search(unifiedReaderContext(), query)
			if err != nil || page.Tier != "core" || page.Partial || page.DurableTotal != count {
				rt.Fatalf("complete page=%+v error=%v", page, err)
			}
			if len(page.Items) != min(limit, count-number*limit) || (page.NextCursor != "") != (number < pages-1) {
				rt.Fatalf("page %d has incorrect size or terminal continuation: items=%d cursor=%t", number+1, len(page.Items), page.NextCursor != "")
			}
			for _, item := range page.Items {
				if !slices.Contains(item.Tags, "keep") {
					rt.Fatal("explicit filter was lost")
				}
				visited = append(visited, item.ID)
			}
			if page.NextCursor == "" {
				break
			}
			if cursors[page.NextCursor] {
				rt.Fatal("continuation repeated")
			}
			cursors[page.NextCursor] = true
			if firstCursor == "" {
				firstCursor = page.NextCursor
			}
			query.Cursor = page.NextCursor
		}
		wantIDs = wantIDs[:0]
		for _, item := range expected {
			wantIDs = append(wantIDs, item.ID)
		}
		if !reflect.DeepEqual(visited, wantIDs) {
			rt.Fatalf("traversal got=%v want=%v order=%q", visited, wantIDs, order)
		}
		query.Cursor = firstCursor
		changed := query
		changed.Tags = []string{"other"}
		if _, err := service.Search(unifiedReaderContext(), changed); ErrorCodeOf(err) != CodeInvalidArgument {
			rt.Fatalf("changed binding did not refuse: %v", err)
		}
		generation = "43"
		if _, err := service.Search(unifiedReaderContext(), query); ErrorCodeOf(err) != CodeInvalidArgument {
			rt.Fatalf("changed generation did not refuse: %v", err)
		}
	})
}
