// Unicode tag tests keep validation, normalization and exact matching on the
// same character limit without requiring a database or a running service.
package catalog

import (
	"slices"
	"strings"
	"testing"
)

// TestUnicodeTagLimits covers the declared character boundary rather than the
// UTF-8 encoding width, including supplementary and combining code points.
func TestUnicodeTagLimits(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, tag string
		valid     bool
	}{
		{"ascii boundary", strings.Repeat("a", 80), true},
		{"ascii over", strings.Repeat("a", 81), false},
		{"original regression", strings.Repeat("\u00e9", 41), true},
		{"accent boundary", strings.Repeat("\u00c9", 80), true},
		{"accent over", strings.Repeat("\u00e9", 81), false},
		{"supplementary boundary", strings.Repeat("\U0001f642", 80), true},
		{"supplementary over", strings.Repeat("\U0001f642", 81), false},
		{"combining boundary", strings.Repeat("e\u0301", 40), true},
		{"combining over", strings.Repeat("e\u0301", 41), false},
		{"NUL", "a\x00b", false},
		{"invalid UTF-8", "\xff", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, err := range []error{
				validateCatalogSearchQuery(SearchQuery{Tags: []string{test.tag}}),
				validateListingDiscoveryMetadata([]string{test.tag}, nil, nil),
				validateListingDiscoveryMetadata(nil, []string{test.tag}, nil),
			} {
				if (err == nil) != test.valid || err != nil && ErrorCodeOf(err) != CodeInvalidArgument {
					t.Fatalf("valid=%t, error=%v", test.valid, err)
				}
			}
			terms := normalizeTerms([]string{test.tag}, 32)
			if !test.valid {
				if len(terms) != 0 {
					t.Fatalf("invalid term retained: %q", terms)
				}
				return
			}
			want := strings.ToLower(test.tag)
			if !slices.Equal(terms, []string{want}) {
				t.Fatalf("normalized tags=%q", terms)
			}
			query := normalizeCatalogSearchQuery(SearchQuery{Tags: []string{test.tag, " " + test.tag + " "}})
			if !slices.Equal(query.Tags, terms) {
				t.Fatalf("query lost or duplicated its tag: %q", query.Tags)
			}
			records := []CatalogSummary{
				{ListingRevision: CatalogDisplaySummary{Tags: terms}, Release: CatalogReleaseSummary{ID: "match"}},
				{ListingRevision: CatalogDisplaySummary{Tags: []string{"unrelated"}}, Release: CatalogReleaseSummary{ID: "other"}},
			}
			matched := filterAndScoreRecords(records, query)
			if len(matched) != 1 || matched[0].Release.ID != "match" {
				t.Fatalf("exact filter lost its Unicode tag: %+v", matched)
			}
		})
	}
}
