package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestCatalogFacetStatementSharesOnlyRequestedValues(t *testing.T) {
	for _, text := range []string{"", "untrusted ' query"} {
		sql, limits, err := catalogFacetStatement(catalog.SearchQuery{Text: text}, []catalog.FacetRequest{{Field: "kind"}, {Field: "tag", Limit: 3, MinCount: 2}})
		if err != nil || !reflect.DeepEqual(limits, []any{1, 20, 2, 3}) {
			t.Fatalf("facet limits: %v %v", limits, err)
		}
		if !strings.Contains(sql, "matched AS MATERIALIZED (\n  SELECT ARRAY[kind::text] AS facet_0,tags AS facet_1\n    FROM records") || strings.Count(sql, "FROM records") != 1 || !strings.Contains(sql, "LEFT JOIN selected_buckets") {
			t.Fatal("facets must share one narrow matching set and preserve empty totals")
		}
		_, projection, _ := strings.Cut(sql, "matched AS MATERIALIZED (")
		projection, _, _ = strings.Cut(projection, "FROM records")
		for _, forbidden := range []string{"trust_state", "score", "vector", "document"} {
			if strings.Contains(projection, forbidden) {
				t.Fatalf("unrequested facet work: %s", forbidden)
			}
		}
		if text != "" && strings.Contains(sql, text) {
			t.Fatal("query text entered SQL")
		}
		for _, parameter := range []string{"$9", "$10", "$11", "$12", "$13", "$14"} {
			if !strings.Contains(sql, parameter) {
				t.Fatalf("missing bound limit %s", parameter)
			}
		}
	}
	if sql, _, err := catalogFacetStatement(catalog.SearchQuery{}, []catalog.FacetRequest{{Field: "kind'); DROP TABLE resources;--"}}); sql != "" || catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("unsupported facet entered SQL")
	}
	if sql, limits, err := catalogFacetStatement(catalog.SearchQuery{}, nil); err != nil || len(limits) != 0 || !strings.Contains(sql, "1 AS unused") || !strings.Contains(sql, "WHERE false") {
		t.Fatalf("count-only query: %v", err)
	}
}

func TestExploreCatalogCombinedRowContract(t *testing.T) {
	for _, scenario := range []string{"bucket", "empty", "missing-total", "incomplete", "bad-index", "query-error", "scan-error", "iteration-error", "invalid-field"} {
		t.Run(scenario, func(t *testing.T) {
			fault := errors.New("injected facet failure")
			index, count, other, value := 0, 3, 2, "skill"
			rows := &identityScriptedRows{values: [][]any{{5, &index, &value, &count, &other}}}
			facets := []catalog.FacetRequest{{Field: "kind"}, {Field: "tag"}}
			switch scenario {
			case "empty":
				rows.values = [][]any{{5, (*int)(nil), (*string)(nil), (*int)(nil), (*int)(nil)}}
			case "missing-total":
				rows.values = nil
			case "incomplete":
				rows.values[0][3] = (*int)(nil)
			case "bad-index":
				index = 2
			case "scan-error":
				rows.values = [][]any{{5}}
			case "iteration-error":
				rows.err = fault
			case "invalid-field":
				facets[0].Field = "unknown"
			}
			result := identityQueryResult{rows: rows}
			if scenario == "query-error" {
				result.err = fault
			}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{result}}
			page, err := exploreCatalogTx(t.Context(), tx, "tenant-a", catalog.SearchQuery{}, facets)
			if scenario == "invalid-field" {
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || len(tx.queryStatements) != 0 {
					t.Fatal("invalid facet reached the driver")
				}
				return
			}
			if scenario == "bucket" || scenario == "empty" {
				if err != nil || page.Matched != 5 || len(page.Facets) != 2 || page.Facets["tag"].Buckets == nil || len(page.Facets["tag"].Buckets) != 0 {
					t.Fatalf("facet response shape: %+v %v", page, err)
				}
				if scenario == "bucket" && (len(page.Facets["kind"].Buckets) != 1 || page.Facets["kind"].Buckets[0].Count != 3 || page.Facets["kind"].OtherCount != 2) {
					t.Fatalf("bucket counts: %+v", page)
				}
			} else if err == nil {
				t.Fatal("invalid driver result accepted")
			}
			if (scenario == "query-error" || scenario == "iteration-error") && !errors.Is(err, fault) {
				t.Fatalf("driver error hidden: %v", err)
			}
			if scenario != "query-error" && !rows.closed || len(tx.queryStatements) != 1 || len(tx.queryRowStatements) != 0 {
				t.Fatal("rows leaked or matching was repeated")
			}
			if tx.queryArguments[0][0] != pgx.QueryExecModeExec || tx.queryArguments[0][1] != "tenant-a" {
				t.Fatal("facet planning or tenant binding changed")
			}
		})
	}
}
