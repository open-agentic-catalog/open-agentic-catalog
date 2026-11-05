package catalog

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"reflect"
	"strings"
	"testing"
)

func TestSearchProvenanceValidationAndBinding(t *testing.T) {
	const namespace = "00000000-0000-7000-8000-00000000000a"
	for _, query := range []SearchQuery{
		{Origins: []SearchResultType{}}, {Origins: []SearchResultType{"unknown"}},
		{Origins: []SearchResultType{SearchResultLocal, SearchResultLocal}},
		{Origins: []SearchResultType{SearchResultLocal, SearchResultIndexed, SearchResultLive, SearchResultLocal}},
		{NamespaceID: "foreign:namespace"}, {NamespaceID: namespace, Origins: []SearchResultType{SearchResultIndexed}},
		{NamespaceID: namespace, Origins: []SearchResultType{SearchResultLocal, SearchResultLive}},
		{Origins: []SearchResultType{SearchResultLive}, Cursor: "opaque"},
	} {
		if ErrorCodeOf(validateCatalogSearchQuery(query)) != CodeInvalidArgument {
			t.Fatalf("accepted invalid query: %+v", query)
		}
	}
	for mask := 1; mask < 8; mask++ {
		values := []SearchResultType{SearchResultLocal, SearchResultIndexed, SearchResultLive}
		query := SearchQuery{}
		for i, value := range values {
			if mask&(1<<i) != 0 {
				query.Origins = append(query.Origins, value)
			}
		}
		if err := validateCatalogSearchQuery(query); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if query.IncludesOrigin(value) != (mask&(1<<i) != 0) {
				t.Fatal("incorrect origin inclusion")
			}
		}
	}
	query := normalizeCatalogSearchQuery(SearchQuery{NamespaceID: strings.ToUpper(namespace), Origins: []SearchResultType{SearchResultLocal}})
	if query.NamespaceID != namespace || !query.IncludesOrigin(SearchResultLocal) || query.IncludesOrigin(SearchResultLive) {
		t.Fatal("namespace did not narrow origins")
	}
	service := newUnifiedSearchTestService(t, &unifiedSearchRepository{}, unifiedFederatedSearcher{})
	actor, _ := requirePermission(unifiedReaderContext(), "resource.read", "")
	a, _ := service.local.unifiedSearchCursorBinding(identity.WithActor(context.Background(), actor), actor, SearchQuery{Origins: []SearchResultType{SearchResultLive, SearchResultLocal}})
	b, _ := service.local.unifiedSearchCursorBinding(identity.WithActor(context.Background(), actor), actor, SearchQuery{Origins: []SearchResultType{SearchResultLocal, SearchResultLive}})
	c, _ := service.local.unifiedSearchCursorBinding(identity.WithActor(context.Background(), actor), actor, SearchQuery{Origins: []SearchResultType{SearchResultLocal}})
	d, _ := service.local.unifiedSearchCursorBinding(identity.WithActor(context.Background(), actor), actor, SearchQuery{NamespaceID: namespace})
	if a != b || a == c || c == d {
		t.Fatal("filter cursor binding is not canonical/distinct")
	}
}

func TestNamespaceFilteringUsesListingAndPreservesOrder(t *testing.T) {
	const selected = "00000000-0000-7000-8000-000000000001"
	const other = "00000000-0000-7000-8000-000000000002"
	first := unifiedLocalRecord("a", "urn:air:local.example:foreign:a", "A", 0)
	first.Resource.NamespaceID = other
	first.Listing.NamespaceID = selected
	second := unifiedLocalRecord("b", "urn:air:local.example:selected:b", "B", 0)
	second.Resource.NamespaceID = selected
	second.Listing.NamespaceID = other
	for _, query := range []SearchQuery{{NamespaceID: selected}, {NamespaceID: other}, {NamespaceID: "00000000-0000-7000-8000-000000000003"}, {Origins: []SearchResultType{SearchResultIndexed}}} {
		got := filterAndScoreRecords([]CatalogSummary{first, second}, query)
		want := 0
		if query.NamespaceID == selected || query.NamespaceID == other {
			want = 1
		}
		if len(got) != want || want == 1 && got[0].Listing.NamespaceID != query.NamespaceID {
			t.Fatalf("filtered=%+v", got)
		}
	}
}

type filterLiveSearcher struct {
	unifiedFederatedSearcher
	calls   int
	request FederatedCatalogSearchRequest
	items   []CatalogSearchResult
}

func (s *filterLiveSearcher) SearchLiveCatalog(_ context.Context, r FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
	s.calls++
	s.request = r
	return FederatedCatalogSearchPage{Live: s.items}, nil
}
func TestLiveOnlySearchSkipsDurableSelection(t *testing.T) {
	live := &filterLiveSearcher{unifiedFederatedSearcher: unifiedFederatedSearcher{search: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		t.Fatal("indexed selection in live-only search")
		return FederatedCatalogSearchPage{}, nil
	}}, items: []CatalogSearchResult{{ID: "live-a", Type: SearchResultLive, Identifier: "urn:air:remote.example:agent", Tags: []string{}}}}
	repository := &unifiedSearchRepository{search: func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		t.Fatal("local selection in live-only search")
		return CatalogSearchRepositoryPage{}, nil
	}}
	service := newUnifiedSearchTestService(t, repository, live)
	semantic, provider, _ := newSemanticSearchTest(t)
	service.local.semantic = semantic.semantic
	provider.storeError = errors.New("excluded semantic state must not be read")
	provider.providerError = errors.New("excluded provider must not be called")
	got, err := service.Search(unifiedReaderContext(), SearchQuery{Text: "agent", Origins: []SearchResultType{SearchResultLive}, Limit: 1})
	if err != nil || live.calls != 1 || got.DurableTotal != 0 || got.LiveCount != 1 || got.NextCursor != "" || !reflect.DeepEqual(got.Items, live.items) {
		t.Fatalf("live response=%+v err=%v", got, err)
	}
	if provider.activeCalls != 0 || provider.statusCalls != 0 || provider.queries != 0 || provider.searches != 0 {
		t.Fatal("live-only search contacted semantic state or query embeddings")
	}
	if !live.request.IncludeLive {
		t.Fatal("live participation missing")
	}
}

func TestLiveOnlySearchBoundsAndFailures(t *testing.T) {
	for _, mode := range []string{"empty", "limited", "error", "missing port"} {
		t.Run(mode, func(t *testing.T) {
			repository := &unifiedSearchRepository{search: func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
				t.Fatal("durable read")
				return CatalogSearchRepositoryPage{}, nil
			}}
			boundary := snapshotLiveSearcher{unifiedFederatedSearcher: unifiedFederatedSearcher{}, live: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
				if mode == "error" {
					return FederatedCatalogSearchPage{}, E(CodeDependencyUnavailable, "source unavailable")
				}
				if mode == "empty" {
					return FederatedCatalogSearchPage{}, nil
				}
				return FederatedCatalogSearchPage{Live: []CatalogSearchResult{{ID: "one", Identifier: "urn:air:peer.example:one", Type: SearchResultLive}, {ID: "two", Identifier: "urn:air:peer.example:two", Type: SearchResultLive}}}, nil
			}}
			var federated FederatedCatalogSearcher = boundary
			if mode == "missing port" {
				federated = unifiedFederatedSearcher{}
			}
			response, err := newUnifiedSearchTestService(t, repository, federated).Search(unifiedReaderContext(), SearchQuery{Origins: []SearchResultType{SearchResultLive}, Limit: 1})
			switch mode {
			case "error":
				if ErrorCodeOf(err) != CodeDependencyUnavailable {
					t.Fatal(err)
				}
			case "missing port":
				if ErrorCodeOf(err) != CodeInternal {
					t.Fatal(err)
				}
			case "empty":
				if err != nil || response.Items == nil || len(response.Items) != 0 {
					t.Fatal("empty live response")
				}
			case "limited":
				if err != nil || len(response.Items) != 1 || !response.Partial || len(response.Diagnostics) != 1 || response.NextCursor != "" {
					t.Fatal("unbounded live response")
				}
			}
		})
	}
}

func TestListingFilterValidationAndCursorBinding(t *testing.T) {
	local := []SearchResultType{SearchResultLocal}
	maximumCategories := make([]string, 16)
	for index := range maximumCategories {
		maximumCategories[index] = strings.Repeat("🙂", 79) + string(rune('a'+index))
	}
	for _, query := range []SearchQuery{
		{License: " \t "}, {License: "x\x00"}, {License: "\xff"}, {License: strings.Repeat("🙂", 257)},
		{License: strings.Repeat("🙂", 256) + " "},
		{Categories: []string{""}}, {Categories: []string{" "}}, {Categories: []string{"\xff"}}, {Categories: []string{"x\x00"}},
		{Categories: []string{strings.Repeat("🙂", 81)}}, {Categories: []string{strings.Repeat("🙂", 80) + " "}}, {Categories: make([]string, 17)},
	} {
		if ErrorCodeOf(validateCatalogSearchQuery(query)) != CodeInvalidArgument {
			t.Fatalf("accepted invalid Listing filter: %+v", query)
		}
	}
	for _, query := range []SearchQuery{{License: "MIT", Origins: []SearchResultType{SearchResultIndexed}}, {Categories: []string{"tools"}, Origins: []SearchResultType{SearchResultLocal, SearchResultLive}}, {License: "MIT"}, {License: strings.Repeat("🙂", 256), Origins: local}, {Categories: maximumCategories}, {Categories: []string{strings.Repeat("🙂", 80)}}, {Categories: []string{}}, {License: "", Origins: []SearchResultType{SearchResultLive}}} {
		if err := validateCatalogSearchQuery(query); err != nil {
			t.Fatal(err)
		}
	}
	service := newUnifiedSearchTestService(t, &unifiedSearchRepository{}, unifiedFederatedSearcher{})
	actor, _ := requirePermission(unifiedReaderContext(), "resource.read", "")
	binding := func(q SearchQuery) string {
		t.Helper()
		b, err := service.local.unifiedSearchCursorBinding(unifiedReaderContext(), actor, q)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	first := SearchQuery{License: " MIT ", Categories: []string{" TOOLS ", "finance", "tools"}}
	normalized := normalizeCatalogSearchQuery(first)
	if normalized.License != "MIT" || !reflect.DeepEqual(normalized.Categories, []string{"finance", "tools"}) || !normalized.IncludesOrigin(SearchResultIndexed) || !normalized.IncludesOrigin(SearchResultLive) || !normalized.IncludesOrigin(SearchResultLocal) {
		t.Fatalf("normalization/scope: %+v", normalized)
	}
	if binding(first) != binding(SearchQuery{License: "MIT", Categories: []string{"finance", "tools"}}) {
		t.Fatal("equivalent filters changed cursor binding")
	}
	for _, changed := range []SearchQuery{{License: "mit", Categories: normalized.Categories}, {License: "MIT", Categories: []string{"tools"}}, {Categories: normalized.Categories}, {}} {
		if binding(first) == binding(changed) {
			t.Fatal("changed Listing filter retained cursor authority")
		}
	}
}

func TestListingFiltersSelectBeforeScoring(t *testing.T) {
	records := []CatalogSummary{unifiedLocalRecord("a", "urn:air:local.example:a", "A", 0), unifiedLocalRecord("b", "urn:air:local.example:b", "B", 0), unifiedLocalRecord("c", "urn:air:local.example:c", "C", 0)}
	records[0].ListingRevision.License, records[0].ListingRevision.Categories = "Apache-2.0", []string{"finance", "tools"}
	records[1].ListingRevision.License, records[1].ListingRevision.Categories = "MIT", []string{"tools"}
	for _, tc := range []struct {
		query SearchQuery
		ids   []string
	}{
		{SearchQuery{License: "Apache-2.0"}, []string{"a"}},
		{SearchQuery{License: "apache-2.0"}, []string{}},
		{SearchQuery{Categories: []string{"TOOLS"}}, []string{"a", "b"}},
		{SearchQuery{Categories: []string{"tools", "finance"}}, []string{"a"}},
		{SearchQuery{License: "MIT", Categories: []string{"finance"}}, []string{}},
		{SearchQuery{License: " MIT ", Categories: []string{" TOOLS ", "tools"}, Text: "B"}, []string{"b"}},
		{SearchQuery{License: "MIT", Text: "A"}, []string{}},
		{SearchQuery{License: "MIT", Tags: []string{"absent"}}, []string{}},
		{SearchQuery{License: "MIT", Publisher: "other-publisher"}, []string{}},
	} {
		selected := filterAndScoreRecords(records, tc.query)
		got := []string{}
		for _, record := range selected {
			got = append(got, record.Release.ID)
		}
		if !reflect.DeepEqual(got, tc.ids) {
			t.Fatalf("query=%+v selected=%v want=%v", tc.query, got, tc.ids)
		}
		// The documented profile adds 40 base + 20 display-name + 5 identifier.
		if tc.query.Text == "B" && selected[0].RelevanceScore != 65 {
			t.Fatalf("Listing filter changed the lexical score: %d", selected[0].RelevanceScore)
		}
		for _, record := range records {
			if record.RelevanceScore != 0 {
				t.Fatal("scoring modified the input summary")
			}
		}
	}
}

func TestListingFiltersBindUnifiedContinuationBeforeRepositoryWork(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query SearchQuery
	}{
		{"license", SearchQuery{License: " MIT "}},
		{"categories", SearchQuery{Categories: []string{" TOOLS ", "finance", "tools"}}},
		{"combined", SearchQuery{License: " MIT ", Categories: []string{" TOOLS ", "finance", "tools"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := tc.query
			query.Origins = []SearchResultType{SearchResultLocal}
			calls := 0
			repository := &unifiedSearchRepository{search: func(_ context.Context, tenant string, request CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
				calls++
				if tenant != "tenant-1" || request.Limit != 2 || request.Query.IncludesOrigin(SearchResultIndexed) || request.Query.IncludesOrigin(SearchResultLive) {
					t.Fatalf("incorrect filtered selection: %+v", request)
				}
				if request.Query.License != strings.TrimSpace(query.License) || len(query.Categories) > 0 && !reflect.DeepEqual(request.Query.Categories, []string{"finance", "tools"}) {
					t.Fatalf("Listing filters lost during continuation: %+v", request.Query)
				}
				items := []CatalogSummary{unifiedLocalRecord("a", "urn:air:local.example:a", "Alpha", 0), unifiedLocalRecord("b", "urn:air:local.example:b", "Beta", 0)}
				if calls == 1 {
					if request.After != nil || request.ExpectedGeneration != "" {
						t.Fatal("fresh query carried continuation state")
					}
				} else {
					if request.ExpectedGeneration != "1" || request.After == nil || *request.After != (CatalogSearchPosition{DisplayName: "alpha", ResultID: "local:a"}) {
						t.Fatalf("incorrect continuation position: %+v", request)
					}
					items = items[1:]
				}
				return CatalogSearchRepositoryPage{Items: items, Total: 2, Generation: "1"}, nil
			}}
			live := &filterLiveSearcher{unifiedFederatedSearcher: unifiedFederatedSearcher{search: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
				t.Fatal("local Listing filters reached indexed federation")
				return FederatedCatalogSearchPage{}, nil
			}}}
			service := newUnifiedSearchTestService(t, repository, live)
			query.Limit = 1
			first, err := service.Search(unifiedReaderContext(), query)
			if err != nil || len(first.Items) != 1 || first.Items[0].ID != "local:a" || first.NextCursor == "" || first.DurableTotal != 2 || first.LiveCount != 0 {
				t.Fatalf("first filtered page=%+v error=%v", first, err)
			}
			query.Cursor = first.NextCursor
			for _, changed := range []SearchQuery{
				{License: "Other", Categories: query.Categories, Limit: 1, Cursor: query.Cursor},
				{License: query.License, Categories: []string{"other"}, Limit: 1, Cursor: query.Cursor},
				{License: query.License, Categories: []string{"tools"}, Origins: []SearchResultType{SearchResultLive}, Limit: 1, Cursor: query.Cursor},
			} {
				if _, err := service.Search(unifiedReaderContext(), changed); ErrorCodeOf(err) != CodeInvalidArgument || calls != 1 {
					t.Fatalf("changed filters reached repository: query=%+v calls=%d error=%v", changed, calls, err)
				}
			}
			query.License = strings.TrimSpace(query.License)
			if len(query.Categories) > 0 {
				query.Categories = []string{"finance", "tools"}
			}
			second, err := service.Search(unifiedReaderContext(), query)
			if err != nil || len(second.Items) != 1 || second.Items[0].ID != "local:b" || second.NextCursor != "" || second.DurableTotal != 2 || second.LiveCount != 0 || calls != 2 || live.calls != 0 {
				t.Fatalf("equivalent filtered continuation=%+v calls=%d error=%v", second, calls, err)
			}
		})
	}
}

func TestListingFiltersPreserveRelevanceOrder(t *testing.T) {
	// The lexical profile awards 40 base, 20 name and 10 description points.
	// Alphabetical order conflicts with relevance; the highest score belongs
	// to an excluded license, so it must not displace either matching record.
	records := []CatalogSummary{
		unifiedLocalRecord("a", "urn:air:local.example:a", "A", 0),
		unifiedLocalRecord("b", "urn:air:local.example:b", "Invoice", 0),
		unifiedLocalRecord("c", "urn:air:local.example:c", "Invoice", 0),
	}
	for index := range records {
		records[index].ListingRevision.License = "MIT"
		records[index].ListingRevision.Categories = []string{"tools"}
	}
	records[0].ListingRevision.Description = "Invoice"
	records[2].ListingRevision.Description, records[2].ListingRevision.License = "Invoice", "Apache-2.0"
	got := filterAndScoreRecords(records, SearchQuery{Text: "Invoice", License: "MIT", Categories: []string{"tools"}})
	if len(got) != 2 || got[0].Release.ID != "b" || got[0].RelevanceScore != 60 || got[1].Release.ID != "a" || got[1].RelevanceScore != 50 {
		t.Fatalf("filtered relevance order=%+v", got)
	}
	for _, record := range records {
		if record.RelevanceScore != 0 {
			t.Fatal("filtering or ranking changed the retained input summary")
		}
	}
}
