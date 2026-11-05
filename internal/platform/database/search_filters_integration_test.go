//go:build integration

package database

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"testing"
)

func TestPostgresSearchNamespaceTotalsAndOriginExclusion(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t)
	for i := 0; i < 26; i++ {
		seed(i)
	}
	var namespace string
	if err := db.operator.QueryRow(t.Context(), "SELECT namespace_id::text FROM catalog_listings WHERE tenant_id=$1 LIMIT 1", housekeepingTenantA).Scan(&namespace); err != nil {
		t.Fatal(err)
	}
	query := catalog.SearchQuery{NamespaceID: namespace, Limit: 7}
	seen := map[string]bool{}
	var after *catalog.CatalogSearchPosition
	generation := ""
	for {
		page, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: query, Limit: 7, After: after, ExpectedGeneration: generation})
		if err != nil || page.Total != 26 {
			t.Fatalf("page total=%d err=%v", page.Total, err)
		}
		generation = page.Generation
		for _, item := range page.Items {
			if item.Listing.NamespaceID != namespace || seen[item.Release.ID] {
				t.Fatal("namespace mismatch or duplicate continuation")
			}
			seen[item.Release.ID] = true
		}
		if !page.HasMore {
			break
		}
		last := page.Items[len(page.Items)-1]
		position := catalog.CatalogSearchPosition{Score: last.RelevanceScore, DisplayName: catalog.CatalogSearchSortName(last.ListingRevision.DisplayName), ResultID: "local:" + last.Release.ID}
		after = &position
	}
	if len(seen) != 26 {
		t.Fatalf("selected %d releases", len(seen))
	}
	other, err := repo.SearchCatalog(t.Context(), housekeepingTenantB, catalog.CatalogSearchRepositoryRequest{Query: query, Limit: 7})
	if err != nil || other.Total != 0 || len(other.Items) != 0 {
		t.Fatalf("another tenant namespace leaked: %+v %v", other, err)
	}
	for _, tenant := range []string{housekeepingTenantA, housekeepingTenantB} {
		for _, q := range []catalog.SearchQuery{{NamespaceID: "00000000-0000-7000-8000-000000000099"}, {Origins: []catalog.SearchResultType{catalog.SearchResultIndexed}}} {
			page, err := repo.SearchCatalog(t.Context(), tenant, catalog.CatalogSearchRepositoryRequest{Query: q, Limit: 7})
			if err != nil || page.Total != 0 || len(page.Items) != 0 || page.Generation == "" {
				t.Fatalf("excluded local page=%+v err=%v", page, err)
			}
		}
	}
	facets, err := repo.ExploreCatalog(t.Context(), housekeepingTenantA, query, []catalog.FacetRequest{{Field: "namespace", Limit: 10}})
	if err != nil || facets.Matched != 26 || len(facets.Facets["namespace"].Buckets) != 1 || facets.Facets["namespace"].Buckets[0].Value != namespace {
		t.Fatalf("facets=%+v err=%v", facets, err)
	}
	for mask := 1; mask < 8; mask++ {
		q := catalog.SearchQuery{}
		for i, v := range []catalog.SearchResultType{catalog.SearchResultLocal, catalog.SearchResultIndexed, catalog.SearchResultLive} {
			if mask&(1<<i) != 0 {
				q.Origins = append(q.Origins, v)
			}
		}
		snapshot, err := repo.SearchCatalogSnapshot(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: q, Limit: 7}, catalog.FederatedCatalogSearchRequest{Query: q, Limit: 7})
		want := 0
		if mask&1 != 0 {
			want = 26
		}
		if err != nil || snapshot.Local.Total != want || snapshot.Indexed.IndexedTotal != 0 || snapshot.Local.Generation != snapshot.Indexed.Generation {
			t.Fatalf("origin subset %d snapshot=%+v err=%v", mask, snapshot, err)
		}
	}
}
