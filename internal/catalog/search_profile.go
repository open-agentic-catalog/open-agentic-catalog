// The core lexical profile supplies deterministic scoring, exact-identifier
// matching and facet construction shared by Catalog search implementations.

package catalog

import (
	"sort"
	"strings"
	"unicode"
)

type SearchDocument struct {
	Identifier            string
	DisplayName           string
	Description           string
	Tags                  []string
	RepresentativeQueries []string
}

// CoreLexicalScore is the code-defined core-lexical-v1 profile used by the
// in-memory implementation and mirrored by the bounded PostgreSQL queries.
func CoreLexicalScore(document SearchDocument, text string) int {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return 0
	}
	if CatalogSearchExactIdentifier(text) == document.Identifier {
		return 100
	}
	terms := lexicalTerms(text)
	if len(terms) == 0 {
		return 0
	}
	identifier := lexicalTermSet(document.Identifier)
	displayName := lexicalTermSet(document.DisplayName)
	description := lexicalTermSet(document.Description)
	tags := lexicalTermSet(strings.Join(document.Tags, " "))
	queries := lexicalTermSet(strings.Join(document.RepresentativeQueries, " "))
	combined := make(map[string]struct{}, len(identifier)+len(displayName)+len(description)+len(tags)+len(queries))
	for _, values := range []map[string]struct{}{identifier, displayName, description, tags, queries} {
		for value := range values {
			combined[value] = struct{}{}
		}
	}
	if !containsLexicalTerms(combined, terms) {
		return 0
	}
	score := 40
	if containsLexicalTerms(displayName, terms) {
		score += 20
	}
	if containsLexicalTerms(queries, terms) {
		score += 15
	}
	if containsLexicalTerms(description, terms) {
		score += 10
	}
	if containsLexicalTerms(tags, terms) {
		score += 5
	}
	if containsLexicalTerms(identifier, terms) {
		score += 5
	}
	return min(score, 99)
}

// CatalogSearchExactIdentifier preserves exact local vendor identifiers while
// canonicalizing AIR input before exact-precedence comparisons.
func CatalogSearchExactIdentifier(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if identifier, err := ParseAIRIdentifier(value); err == nil {
		return identifier.String()
	}
	return value
}

// lexicalTerms splits text into the normalized token vocabulary used by the
// core lexical profile.
func lexicalTerms(value string) []string {
	terms := make([]string, 0)
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		term := current.String()
		if !containsStringValue(terms, term) {
			terms = append(terms, term)
		}
		current.Reset()
	}
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			current.WriteRune(character)
			continue
		}
		flush()
	}
	flush()
	sort.Strings(terms)
	return terms
}

// lexicalTermSet builds a membership set of normalized lexical tokens.
func lexicalTermSet(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, term := range lexicalTerms(value) {
		result[term] = struct{}{}
	}
	return result
}

// containsLexicalTerms reports whether every requested token appears in the
// document term set.
func containsLexicalTerms(values map[string]struct{}, terms []string) bool {
	for _, term := range terms {
		if _, ok := values[term]; !ok {
			return false
		}
	}
	return true
}

// containsStringValue checks exact membership in an already normalized string
// slice.
func containsStringValue(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// buildCatalogFacets counts requested metadata values and applies each facet's
// ordering and bucket limit.
func buildCatalogFacets(records []CatalogSummary, facets []FacetRequest) ExploreResponse {
	response := ExploreResponse{Matched: len(records), Facets: make(map[string]FacetResult, len(facets))}
	for _, facet := range facets {
		if facet.Limit <= 0 {
			facet.Limit = 20
		}
		counts := make(map[string]int)
		for _, record := range records {
			for _, value := range catalogFacetValues(record, facet.Field) {
				if value != "" {
					counts[value]++
				}
			}
		}
		buckets := make([]FacetBucket, 0, len(counts))
		for value, count := range counts {
			if count >= max(1, facet.MinCount) {
				buckets = append(buckets, FacetBucket{Value: value, Count: count})
			}
		}
		sort.Slice(buckets, func(i, j int) bool {
			if buckets[i].Count != buckets[j].Count {
				return buckets[i].Count > buckets[j].Count
			}
			return buckets[i].Value < buckets[j].Value
		})
		result := FacetResult{}
		if len(buckets) > facet.Limit {
			for _, bucket := range buckets[facet.Limit:] {
				result.OtherCount += bucket.Count
			}
			buckets = buckets[:facet.Limit]
		}
		result.Buckets = buckets
		response.Facets[facet.Field] = result
	}
	return response
}
