//go:build integration

package database

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPostgresFacetsShareMatchingAndPreserveBuckets(t *testing.T) {
	documents := make([]catalog.SearchDocument, 3)
	for index, tags := range [][]string{{"alpha", "zeta"}, {"beta", "zeta"}, {"alpha"}} {
		documents[index] = catalog.SearchDocument{Identifier: fmt.Sprintf("urn:air:catalog.example.com:finance:facet-%d", index), DisplayName: fmt.Sprintf("Facet %d", index), Description: "Shared facet fixture", Tags: tags}
	}
	_, repository, counts, seed := newSearchCostFixture(t, documents...)
	for index := range documents {
		seed(index)
	}
	for _, test := range []struct {
		name    string
		tenant  string
		query   catalog.SearchQuery
		limit   int
		minimum int
		total   int
		buckets []catalog.FacetBucket
		other   int
	}{
		{"limited-ties", housekeepingTenantA, catalog.SearchQuery{Text: "Shared"}, 1, 0, 3, []catalog.FacetBucket{{Value: "alpha", Count: 2}}, 3},
		{"minimum", housekeepingTenantA, catalog.SearchQuery{}, 1, 2, 3, []catalog.FacetBucket{{Value: "alpha", Count: 2}}, 2},
		{"two-buckets", housekeepingTenantA, catalog.SearchQuery{}, 2, 0, 3, []catalog.FacetBucket{{Value: "alpha", Count: 2}, {Value: "zeta", Count: 2}}, 1},
		{"no-buckets", housekeepingTenantA, catalog.SearchQuery{}, 2, 4, 3, []catalog.FacetBucket{}, 0},
		{"no-matches", housekeepingTenantA, catalog.SearchQuery{Text: "absent"}, 2, 0, 0, []catalog.FacetBucket{}, 0},
		{"tenant-denial", housekeepingTenantB, catalog.SearchQuery{}, 2, 0, 0, []catalog.FacetBucket{}, 0},
		{"filtered", housekeepingTenantA, catalog.SearchQuery{Tags: []string{"alpha"}}, 2, 0, 2, []catalog.FacetBucket{{Value: "alpha", Count: 2}, {Value: "zeta", Count: 1}}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			facets := []catalog.FacetRequest{{Field: "tag", Limit: test.limit, MinCount: test.minimum}, {Field: "kind"}, {Field: "category"}, {Field: "source"}, {Field: "namespace"}, {Field: "publisher"}, {Field: "visibility"}, {Field: "trustState"}}
			counts.reset()
			result, err := repository.ExploreCatalog(t.Context(), test.tenant, test.query, facets)
			if err != nil || result.Matched != test.total || len(result.Facets) != len(facets) || !reflect.DeepEqual(result.Facets["tag"].Buckets, test.buckets) || result.Facets["tag"].OtherCount != test.other {
				t.Fatalf("facet contract changed: %+v %v", result, err)
			}
			if counts.counts["with"] != 1 || counts.counts["begin"] != 1 || counts.counts["commit"] != 1 {
				t.Fatalf("matching repeated for facets: %v", counts.counts)
			}
			for _, facet := range facets[1:] {
				buckets := result.Facets[facet.Field].Buckets
				if buckets == nil || result.Facets[facet.Field].OtherCount != 0 {
					t.Fatalf("invalid facet shape: %s %+v", facet.Field, result)
				}
				if test.total == 0 || facet.Field == "category" {
					if len(buckets) != 0 {
						t.Fatalf("unexpected buckets: %s %+v", facet.Field, buckets)
					}
				} else if len(buckets) != 1 || buckets[0].Count != test.total {
					t.Fatalf("scalar counts changed: %s %+v", facet.Field, buckets)
				}
			}
		})
	}
	result, err := repository.ExploreCatalog(t.Context(), housekeepingTenantA, catalog.SearchQuery{}, nil)
	if err != nil || result.Matched != 3 || result.Facets == nil || len(result.Facets) != 0 {
		t.Fatalf("count-only request changed: %+v %v", result, err)
	}
}
