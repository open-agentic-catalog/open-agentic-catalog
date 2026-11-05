//go:build integration

package database

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPostgresSearchPreparedPlanModesPreserveBrowseContracts(t *testing.T) {
	db, _, _, seed := newSearchCostFixture(t)
	for i := 0; i < 45; i++ {
		seed(i)
	}
	for _, mode := range []string{"auto", "force_custom_plan", "force_generic_plan"} {
		t.Run(mode, func(t *testing.T) {
			configuration := db.api.Config().Copy()
			configuration.MaxConns = 1
			configuration.ConnConfig.RuntimeParams["plan_cache_mode"] = mode
			pool, err := pgxpool.NewWithConfig(t.Context(), configuration)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			repository, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range []struct {
				query catalog.SearchQuery
				total int
			}{
				{catalog.SearchQuery{}, 45},
				{catalog.SearchQuery{Tags: []string{"performance"}, Visibility: catalog.VisibilityTenant}, 45},
				{catalog.SearchQuery{Tags: []string{"absent"}}, 0},
				{catalog.SearchQuery{Visibility: catalog.VisibilityPublic}, 0},
				{catalog.SearchQuery{Kinds: []catalog.Kind{catalog.KindAgent}}, 0},
				{catalog.SearchQuery{Kinds: []catalog.Kind{catalog.KindSkill}}, 45},
				{catalog.SearchQuery{Text: "Performance", Kinds: []catalog.Kind{catalog.KindSkill, catalog.KindPlugin}}, 45},
				{catalog.SearchQuery{Text: "Performance", Kinds: []catalog.Kind{catalog.KindAgent}}, 0},
				{catalog.SearchQuery{Text: "Performance", Kinds: []catalog.Kind{catalog.KindSkill}, Tags: []string{"absent"}}, 0},
				{catalog.SearchQuery{Text: "Performance"}, 45},
				{catalog.SearchQuery{Text: "missingterm"}, 0},
			} {
				page, err := repository.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: item.query, Limit: 20})
				if err != nil || page.Total != item.total || len(page.Items) != min(20, item.total) || page.HasMore != (item.total > 20) {
					t.Fatalf("query=%+v total=%d page=%d err=%v", item.query, page.Total, len(page.Items), err)
				}
				for _, record := range page.Items {
					if record.Resource.TenantID != housekeepingTenantA || item.query.Text == "" && record.RelevanceScore != 0 {
						t.Fatal("tenant boundary or empty-text score changed")
					}
				}
				if item.total > 20 {
					seen := map[string]bool{}
					for _, record := range page.Items {
						seen[record.Release.ID] = true
					}
					last := page.Items[len(page.Items)-1]
					for _, remaining := range []int{20, 5, 0} {
						position := catalog.CatalogSearchResultPosition(catalog.CatalogSearchResult{ID: "local:" + last.Release.ID, DisplayName: last.ListingRevision.DisplayName, Score: last.RelevanceScore})
						next, err := repository.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: item.query, Limit: 20, ExpectedGeneration: page.Generation, After: &position})
						if err != nil || next.Total != 45 || next.Items == nil || len(next.Items) != remaining || next.HasMore != (remaining == 20) {
							t.Fatalf("continuation changed: total=%d page=%d err=%v", next.Total, len(next.Items), err)
						}
						for _, record := range next.Items {
							nextPosition := catalog.CatalogSearchResultPosition(catalog.CatalogSearchResult{ID: "local:" + record.Release.ID, DisplayName: record.ListingRevision.DisplayName, Score: record.RelevanceScore})
							if seen[record.Release.ID] || !catalog.CatalogSearchPositionAfter(nextPosition, position) {
								t.Fatal("duplicate or unordered continuation result")
							}
							seen[record.Release.ID], last, position = true, record, nextPosition
						}
					}
					if len(seen) != 45 {
						t.Fatal("continuation skipped a result")
					}
				}
			}
			// Only local search uses an unnamed, request-planned statement.
			// Other prepared reads and the operator's planner setting survive.
			for _, text := range []string{"", "Performance"} {
				_, statement := catalogSearchStatements(catalog.SearchQuery{Text: text})
				var count int
				if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_prepared_statements WHERE statement=$1`, statement).Scan(&count); err != nil || count != 0 {
					t.Fatalf("local search retained a named plan: count=%d error=%v", count, err)
				}
			}
			var retainedMode string
			if err := pool.QueryRow(t.Context(), `SHOW plan_cache_mode`).Scan(&retainedMode); err != nil || retainedMode != mode {
				t.Fatalf("search changed session planning: %q %v", retainedMode, err)
			}
			for _, tenant := range []string{housekeepingTenantA, housekeepingTenantB} {
				wanted := 45
				if tenant == housekeepingTenantB {
					wanted = 0
				}
				facets, err := repository.ExploreCatalog(t.Context(), tenant, catalog.SearchQuery{Tags: []string{"performance"}}, []catalog.FacetRequest{{Field: "kind"}, {Field: "visibility"}})
				if err != nil || facets.Matched != wanted {
					t.Fatalf("facet boundary changed: matched=%d err=%v", facets.Matched, err)
				}
				for field, value := range map[string]string{"kind": "skill", "visibility": "tenant"} {
					buckets := facets.Facets[field].Buckets
					if wanted == 0 && len(buckets) != 0 || wanted > 0 && (len(buckets) != 1 || buckets[0].Value != value || buckets[0].Count != wanted) {
						t.Fatalf("facet %s buckets changed: %+v", field, buckets)
					}
				}
				page, err := repository.SearchCatalog(t.Context(), tenant, catalog.CatalogSearchRepositoryRequest{Limit: 20})
				if err != nil || page.Total != wanted {
					t.Fatalf("tenant search changed: total=%d err=%v", page.Total, err)
				}
			}
			var facetPlans int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_prepared_statements WHERE starts_with(statement,$1)`, catalogSearchBaseCTE).Scan(&facetPlans); err != nil || facetPlans != 0 {
				t.Fatalf("local facets retained a named plan: count=%d error=%v", facetPlans, err)
			}
		})
	}
}

// Real exact-role queries prove filters precede counts, facets and limit+1
// hydration; no owner update bypasses immutable Listing evidence.
func TestPostgresListingFiltersPreserveTotalsPagesAndFacets(t *testing.T) {
	listings := []catalog.CreateListingRevisionInput{
		{License: "MIT", Categories: []string{"finance", "tools"}},
		{License: "Apache-2.0", Categories: []string{"tools"}},
		{License: "MIT", Categories: []string{"finance", "tools"}},
		{},
	}
	db, _, _, seed := newSearchCostFixtureWithListings(t, listings)
	for i := range listings {
		seed(i)
	}
	for _, mode := range []string{"auto", "force_custom_plan", "force_generic_plan"} {
		t.Run(mode, func(t *testing.T) {
			configuration := db.api.Config().Copy()
			configuration.MaxConns = 1
			configuration.ConnConfig.RuntimeParams["plan_cache_mode"] = mode
			pool, err := pgxpool.NewWithConfig(t.Context(), configuration)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			repository, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				query catalog.SearchQuery
				total int
			}{
				{catalog.SearchQuery{License: "MIT"}, 2},
				{catalog.SearchQuery{License: "mit"}, 0},
				{catalog.SearchQuery{Categories: []string{"tools"}}, 3},
				{catalog.SearchQuery{Categories: []string{"finance", "tools"}}, 2},
				{catalog.SearchQuery{License: "MIT", Categories: []string{"finance", "tools"}}, 2},
				{catalog.SearchQuery{License: "Apache-2.0", Categories: []string{"finance"}}, 0},
				{catalog.SearchQuery{Categories: []string{"absent"}}, 0},
				{catalog.SearchQuery{License: "MIT", Tags: []string{"absent"}}, 0},
				{catalog.SearchQuery{License: "MIT", Text: "unmatched"}, 0},
				{catalog.SearchQuery{License: "MIT", Visibility: catalog.VisibilityPublic}, 0},
				{catalog.SearchQuery{Categories: []string{"tools"}, Kinds: []catalog.Kind{catalog.KindAgent}}, 0},
			} {
				page, err := repository.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: tc.query, Limit: 1})
				if err != nil || page.Total != tc.total || len(page.Items) != min(1, tc.total) || page.HasMore != (tc.total > 1) {
					t.Fatalf("query=%+v page=%+v err=%v", tc.query, page, err)
				}
				seen := map[string]bool{}
				for _, item := range page.Items {
					seen[item.Release.ID] = true
				}
				for page.HasMore {
					last := page.Items[len(page.Items)-1]
					position := catalog.CatalogSearchResultPosition(catalog.CatalogSearchResult{ID: "local:" + last.Release.ID, DisplayName: last.ListingRevision.DisplayName, Score: last.RelevanceScore})
					page, err = repository.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: tc.query, Limit: 1, ExpectedGeneration: page.Generation, After: &position})
					if err != nil || page.Total != tc.total || len(page.Items) != 1 || seen[page.Items[0].Release.ID] {
						t.Fatalf("filtered continuation=%+v err=%v", page, err)
					}
					seen[page.Items[0].Release.ID] = true
				}
				if len(seen) != tc.total {
					t.Fatal("filtered pagination lost an eligible release")
				}
				facets, err := repository.ExploreCatalog(t.Context(), housekeepingTenantA, tc.query, []catalog.FacetRequest{{Field: "category", Limit: 1}})
				if err != nil || facets.Matched != tc.total {
					t.Fatalf("filtered facets=%+v err=%v", facets, err)
				}
				if tc.total == 2 && (len(facets.Facets["category"].Buckets) != 1 || facets.Facets["category"].Buckets[0] != (catalog.FacetBucket{Value: "finance", Count: 2}) || facets.Facets["category"].OtherCount != 2) {
					t.Fatalf("full-population buckets=%+v", facets)
				}
				foreign, err := repository.SearchCatalog(t.Context(), housekeepingTenantB, catalog.CatalogSearchRepositoryRequest{Query: tc.query, Limit: 1})
				if err != nil || foreign.Total != 0 || len(foreign.Items) != 0 {
					t.Fatalf("cross-tenant filtered result=%+v err=%v", foreign, err)
				}
			}
		})
	}
}
