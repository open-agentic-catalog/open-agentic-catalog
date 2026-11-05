package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCoreLexicalSharedCorpus(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/core-lexical-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Documents []struct {
			Identifier            string   `json:"identifier"`
			DisplayName           string   `json:"displayName"`
			Description           string   `json:"description"`
			Tags                  []string `json:"tags"`
			RepresentativeQueries []string `json:"representativeQueries"`
		} `json:"documents"`
		Cases []struct {
			Name     string `json:"name"`
			Document int    `json:"document"`
			Query    string `json:"query"`
			Score    int    `json:"score"`
		} `json:"cases"`
		Ordering []struct {
			Name     string `json:"name"`
			SortName string `json:"sortName"`
		} `json:"ordering"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, test := range corpus.Cases {
		t.Run(test.Name, func(t *testing.T) {
			fixture := corpus.Documents[test.Document]
			document := SearchDocument{Identifier: fixture.Identifier, DisplayName: fixture.DisplayName, Description: fixture.Description, Tags: fixture.Tags, RepresentativeQueries: fixture.RepresentativeQueries}
			if got := CoreLexicalScore(document, test.Query); got != test.Score {
				t.Fatalf("score=%d want=%d", got, test.Score)
			}
			records := filterAndScoreRecords([]CatalogSummary{{Resource: CatalogResourceSummary{CanonicalIdentifier: document.Identifier}, ListingRevision: CatalogDisplaySummary{DisplayName: document.DisplayName, Description: document.Description, Tags: document.Tags, RepresentativeQueries: document.RepresentativeQueries}}}, SearchQuery{Text: test.Query})
			want := 0
			if strings.TrimSpace(test.Query) == "" || test.Score > 0 {
				want = 1
			}
			if len(records) != want {
				t.Fatalf("matches=%d want=%d", len(records), want)
			}
		})
	}
	ordered := make([]CatalogSummary, 0, len(corpus.Ordering))
	for i, test := range corpus.Ordering {
		ordered = append(ordered, CatalogSummary{Release: CatalogReleaseSummary{ID: fmt.Sprintf("release-%03d", i)}, ListingRevision: CatalogDisplaySummary{DisplayName: test.Name}})
	}
	for size := 1; size <= 2; size++ {
		page := filterAndScoreRecords(ordered, SearchQuery{})
		visited := 0
		for len(page) > 0 {
			count := min(size, len(page))
			for _, record := range page[:count] {
				if visited >= len(ordered) {
					t.Fatal("memory traversal repeated records")
				}
				if record.Release.ID != ordered[visited].Release.ID {
					t.Fatal("memory ordering differs from corpus")
				}
				visited++
			}
			page = recordsAfterPosition(page, catalogRecordPosition(page[count-1]))
		}
		if visited != len(ordered) {
			t.Fatal("memory traversal omitted records")
		}
	}
	for _, test := range corpus.Ordering {
		if got := CatalogSearchSortName(test.Name); got != test.SortName {
			t.Fatalf("sort(%q)=%q want=%q", test.Name, got, test.SortName)
		}
	}
}
