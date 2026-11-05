package database

import (
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestCatalogSearchNormalizesQueryOutsideCandidateRows(t *testing.T) {
	// Uncorrelated scalar subqueries are not extra database round trips.
	if strings.Count(catalogSearchRecordsCTE, "(SELECT catalog_core_lexical_query_v1($2))") != 6 {
		t.Fatal("query normalization must remain statement-local for each predicate")
	}
	if strings.Contains(catalogSearchRecordsCTE, "catalog_core_lexical_vector_v1(") || strings.Contains(catalogSearchRecordsCTE, "catalog_core_sort_name_v1(") {
		t.Fatal("immutable metadata must not be normalized for each query candidate")
	}
	if !strings.Contains(catalogSearchRecordsCTE, "resource.search_identifier_vector || revision.search_display_vector || revision.search_description_vector || revision.search_tags_vector || revision.search_queries_vector") {
		t.Fatal("document matching must preserve the positionless field union")
	}
}

func TestCatalogSearchCombinesTotalAndPageWithNarrowMatches(t *testing.T) {
	_, matched, ok := strings.Cut(catalogSearchPageSQL, "matched AS MATERIALIZED (")
	if !ok || !strings.HasPrefix(matched, "\n  SELECT release_id,sort_name,result_id,") {
		t.Fatal("shared matches must contain only identity, ordering and score")
	}
	if !strings.Contains(matched, "SELECT count(*) AS total FROM matched") || !strings.Contains(matched, "LEFT JOIN LATERAL") || !strings.Contains(matched, "totals.total>0") || !strings.Contains(matched, "LIMIT $15") {
		t.Fatal("combined selection must preserve empty/terminal totals and bounded page work")
	}
}

func TestIndexedSearchDefersScoringAndBoundsConflictAggregation(t *testing.T) {
	current, _, ok := strings.Cut(indexedCatalogSearchRecordsCTE, "), eligible AS NOT MATERIALIZED (")
	if !ok {
		t.Fatal("missing shared source-eligible set")
	}
	if !strings.Contains(current, "current_eligible AS NOT MATERIALIZED (") {
		t.Fatal("shared source projection must allow bounded identity lookups and count pruning")
	}
	scopes, observations, ok := strings.Cut(current, "), current_eligible AS NOT MATERIALIZED (")
	if !ok || !strings.Contains(scopes, "approved_sources AS MATERIALIZED (") || strings.Count(scopes, "jsonb_typeof(") != 5 || strings.Contains(observations, "jsonb_typeof(") || strings.Contains(observations, "federation_source_revisions") {
		t.Fatal("source scope parsing must precede per-observation selection")
	}
	for _, expensive := range []string{"catalog_core_lexical_vector_v1", "catalog_core_lexical_query_v1", "catalog_core_sort_name_v1", "digest("} {
		if strings.Contains(current, expensive) {
			t.Fatalf("shared evidence selection performs unused scoring work: %s", expensive)
		}
	}
	countInputs, pageInputs, ok := strings.Cut(indexedCatalogSearchRecordsCTE, "), members AS (")
	if !ok {
		t.Fatal("missing page-only member scoring")
	}
	members, _, ok := strings.Cut(pageInputs, "), selected_identities AS (")
	if !ok || strings.Contains(members, "SELECT DISTINCT") {
		t.Fatal("identity winner selection must not sort/deduplicate identical members twice")
	}
	for _, expensive := range []string{"catalog_core_lexical_vector_v1", "catalog_core_sort_name_v1", "digest("} {
		if strings.Contains(countInputs, expensive) {
			t.Fatalf("count inputs perform unused ranking: %s", expensive)
		}
	}
	if strings.Count(indexedCatalogSearchRecordsCTE, "(SELECT catalog_core_lexical_query_v1($2))") != 6 {
		t.Fatal("indexed query parsing must remain statement-local")
	}
	if strings.Contains(indexedCatalogSearchRecordsCTE, "catalog_core_lexical_vector_v1(") || !strings.Contains(indexedCatalogSearchRecordsCTE, "search_display_vector @@") {
		t.Fatal("indexed ranking must read stored vectors rather than normalize candidate text")
	}
	if strings.Contains(indexedCatalogSearchRecordsCTE, "member_count") || strings.Contains(indexedCatalogSearchRecordsCTE, "JOIN identities") {
		t.Fatal("unbounded identity conflict aggregation returned to ranking")
	}
	limit := strings.Index(indexedCatalogSearchPageSQL, "LIMIT $13")
	conflicts := strings.Index(indexedCatalogSearchPageSQL, "count(DISTINCT conflict.normalized_entry_digest)>1")
	if limit < 0 || conflicts < limit || !strings.Contains(indexedCatalogSearchPageSQL, "FROM current_eligible conflict") {
		t.Fatal("query-independent conflict evidence must follow bounded page selection")
	}
}

func TestCatalogSearchStatementSelectionFoldsOnlyTextPresence(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "Performance", " ", "private '$2='' text"} {
		query := catalog.SearchQuery{Text: text, Tags: []string{"selected"}, Publisher: "publisher", Visibility: catalog.VisibilityPublic}
		records, page := catalogSearchStatements(query)
		for index, sql := range []string{records, page} {
			original := []string{catalogSearchRecordsCTE, catalogSearchPageSQL}[index]
			presence := "false"
			if text == "" {
				presence = "true"
			}
			if strings.Count(original, "$2=''") != 2 || sql != strings.ReplaceAll(original, "$2=''", presence) {
				t.Fatal("SQL must change only the two known text-presence branches")
			}
			if !strings.Contains(sql, "$7='' OR visibility=$7") || !strings.Contains(sql, "listing.tenant_id=$1") {
				t.Fatal("filter or tenant predicate changed")
			}
		}
	}
}
