// Representative query bounds are schema characters, not their encoded width.
package catalog

import (
	"slices"
	"strings"
	"testing"
)

func TestRepresentativeQueryUnicodeBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, query string
		valid       bool
	}{
		{"ascii minimum", "abc", true}, {"ascii maximum", strings.Repeat("a", 240), true},
		{"accent short", "éé", false}, {"supplementary short", "🙂🙂", false},
		{"supplementary minimum", "🙂🙂🙂", true}, {"supplementary maximum", strings.Repeat("🙂", 240), true},
		{"supplementary over", strings.Repeat("🙂", 241), false}, {"combining maximum", strings.Repeat("e\u0301", 120), true},
		{"combining over", strings.Repeat("e\u0301", 120) + "a", false},
		{"normalized whitespace", "  🙂\t🙂  🙂\n ", true}, {"NUL", "a\x00b", false}, {"invalid UTF8", "a\xffb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateListingDiscoveryMetadata(nil, nil, []string{tc.query})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			values := normalizeQueries([]string{tc.query, tc.query})
			if tc.valid {
				want := strings.Join(strings.Fields(tc.query), " ")
				if !slices.Equal(values, []string{want}) {
					t.Fatalf("retained=%q want=%q", values, want)
				}
			} else if len(values) != 0 {
				t.Fatalf("invalid query retained: %q", values)
			}
		})
	}
	if err := validateListingDiscoveryMetadata(nil, nil, []string{"one", "two", "three", "four", "five", "six"}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("universal count limit: %v", err)
	}
}
