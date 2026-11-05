package database

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// SQL/RLS and composed results are checked by the PostgreSQL suite. These
// driver-level tests prove empty search keeps generation checks and propagates
// failures without hydration or redundant selection.
func TestSearchEmptyResultsAndGenerationChecks(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		for _, scenario := range []string{"empty", "stale", "repair", "generation-error", "count-error", "nonempty", "excluded-empty", "excluded-stale", "excluded-repair", "excluded-generation-error"} {
			excluded := strings.HasPrefix(scenario, "excluded-")
			if indexed && excluded {
				continue // Origin exclusion is owned by local search.
			}
			scenario = strings.TrimPrefix(scenario, "excluded-")
			if !indexed && scenario == "nonempty" {
				continue // Local count and page share one query-error path.
			}
			name := "local/" + scenario
			if !indexed && scenario == "count-error" {
				name = "local/query-error"
			}
			if excluded {
				name = "local/origin-excluded/" + scenario
			}
			if indexed {
				name = "indexed/" + scenario
			}
			t.Run(name, func(t *testing.T) {
				fault := errors.New("injected search failure")
				generation := scannerFunc(func(dest ...any) error {
					if scenario == "generation-error" {
						return fault
					}
					setScanDestination(t, dest, 0, int64(7))
					setScanDestination(t, dest, 1, scenario == "repair")
					return nil
				})
				count := scannerFunc(func(dest ...any) error {
					if scenario == "count-error" {
						return fault
					}
					if scenario == "nonempty" {
						setScanDestination(t, dest, 0, 1)
					}
					return nil
				})
				result := identityQueryResult{err: fault}
				if !indexed && scenario == "empty" {
					result = identityQueryResult{rows: &identityScriptedRows{values: [][]any{{0, (*string)(nil), (*int)(nil)}}}}
				}
				tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{generation, count}, queryResults: []identityQueryResult{result}}
				expected := "7"
				if scenario == "stale" {
					expected = "6"
				}
				var err error
				if indexed {
					page, searchErr := searchIndexedCatalogTx(t.Context(), tx, "tenant-a", catalog.FederatedCatalogSearchRequest{ExpectedGeneration: expected})
					err = searchErr
					if scenario == "empty" && (page.Indexed == nil || len(page.Indexed) != 0 || page.IndexedTotal != 0 || page.Generation != "7" || page.HasMore) {
						t.Fatalf("empty indexed page changed: %+v", page)
					}
				} else {
					request := catalog.CatalogSearchRepositoryRequest{ExpectedGeneration: expected}
					if excluded {
						request.Query.Origins = []catalog.SearchResultType{catalog.SearchResultIndexed}
					}
					page, searchErr := searchCatalogTx(t.Context(), tx, "tenant-a", request)
					err = searchErr
					if scenario == "empty" && (page.Items == nil || len(page.Items) != 0 || page.Total != 0 || page.Generation != "7" || page.HasMore) {
						t.Fatalf("empty local page changed: %+v", page)
					}
				}
				switch scenario {
				case "empty":
					if err != nil {
						t.Fatal(err)
					}
				case "stale":
					if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || excluded && !errors.Is(err, catalog.ErrSearchGenerationChanged) {
						t.Fatalf("stale generation bypassed: %v", err)
					}
				case "repair":
					if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
						t.Fatalf("repair requirement bypassed: %v", err)
					}
				default:
					if !errors.Is(err, fault) {
						t.Fatalf("driver failure hidden: %v", err)
					}
				}
				wantRows, wantPages := 2, 0
				if scenario == "stale" || scenario == "repair" || scenario == "generation-error" {
					wantRows = 1
				}
				if scenario == "nonempty" {
					wantPages = 1
				}
				if !indexed {
					wantPages = wantRows - 1 // Local count and page are one statement.
					wantRows = 1
				}
				if excluded {
					wantRows, wantPages = 1, 0
					if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{"tenant-a"}}) || len(tx.queryRows) != 1 || len(tx.queryResults) != 1 {
						t.Fatalf("origin exclusion consumed count/page/hydration or lost generation binding: %+v", tx)
					}
				}
				if len(tx.queryRowStatements) != wantRows || len(tx.queryStatements) != wantPages {
					t.Fatalf("unexpected count/page work: %d/%d", len(tx.queryRowStatements), len(tx.queryStatements))
				}
				if indexed {
					configured := scenario != "stale" && scenario != "repair" && scenario != "generation-error"
					if configured && !reflect.DeepEqual(tx.statements, []string{"SET LOCAL jit=off"}) || !configured && len(tx.statements) != 0 {
						t.Fatal("indexed query planning changed or ran before generation admission", tx.statements)
					}
				}
				for _, args := range append(tx.queryRowArguments, tx.queryArguments...) {
					if len(args) > 0 && args[0] == pgx.QueryExecModeExec {
						args = args[1:]
					}
					if args[0] != "tenant-a" {
						t.Fatal("tenant predicate lost")
					}
				}
			})
		}
	}
}

func TestIndexedSearchPlanningFailurePreventsSelection(t *testing.T) {
	fault := errors.New("private planner setup failure")
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{execErrors: []error{fault}}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(7), false})}}
	page, err := searchIndexedCatalogTx(t.Context(), tx, "tenant-a", catalog.FederatedCatalogSearchRequest{})
	if !errors.Is(err, fault) || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || page.Generation != "" || len(tx.queryRowStatements) != 1 || len(tx.queryStatements) != 0 || !reflect.DeepEqual(tx.statements, []string{"SET LOCAL jit=off"}) {
		t.Fatal("planner setup failure reached selection or changed its error", page, err)
	}
}

func TestIndexedSearchExclusionPreservesGenerationWithoutSelection(t *testing.T) {
	for name, query := range map[string]catalog.SearchQuery{
		"local-only": {Origins: []catalog.SearchResultType{catalog.SearchResultLocal}},
		"public":     {Visibility: catalog.VisibilityPublic},
	} {
		for _, stale := range []bool{false, true} {
			t.Run(name+"/stale="+strconv.FormatBool(stale), func(t *testing.T) {
				tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(7), false})}}
				expected := "7"
				if stale {
					expected = "6"
				}
				page, err := searchIndexedCatalogTx(t.Context(), tx, "tenant-a", catalog.FederatedCatalogSearchRequest{Query: query, ExpectedGeneration: expected})
				if stale {
					if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
						t.Fatal("excluded indexed search bypassed generation validation", err)
					}
				} else if err != nil || page.Generation != "7" || page.Indexed == nil || len(page.Indexed) != 0 || page.IndexedTotal != 0 || page.HasMore {
					t.Fatal("excluded indexed search changed its empty result or generation", page, err)
				}
				if len(tx.statements) != 0 || len(tx.queryStatements) != 0 || len(tx.queryRowStatements) != 1 || !reflect.DeepEqual(tx.queryRowArguments, [][]any{{"tenant-a"}}) {
					t.Fatal("excluded indexed search configured or selected indexed records", tx)
				}
			})
		}
	}
}

func TestLocalSearchCombinedSelectionRowContract(t *testing.T) {
	for _, scenario := range []string{"terminal", "missing-total", "incomplete-row", "scan-error", "iteration-error"} {
		t.Run(scenario, func(t *testing.T) {
			fault := errors.New("injected row failure")
			rows := &identityScriptedRows{values: [][]any{{45, (*string)(nil), (*int)(nil)}}}
			switch scenario {
			case "missing-total":
				rows.values = nil
			case "incomplete-row":
				id := "release-id"
				rows.values[0][1] = &id
			case "scan-error":
				rows.values = [][]any{{45}}
			case "iteration-error":
				rows.err = fault
			}
			tx := &identityScriptedTx{queryRows: []pgx.Row{scriptedValueRow([]any{int64(7), false})}, queryResults: []identityQueryResult{{rows: rows}}}
			page, err := searchCatalogTx(t.Context(), tx, "tenant-a", catalog.CatalogSearchRepositoryRequest{ExpectedGeneration: "7", After: &catalog.CatalogSearchPosition{Score: 1, DisplayName: "last", ResultID: "local:last"}})
			if scenario == "terminal" {
				if err != nil || page.Total != 45 || page.Items == nil || len(page.Items) != 0 || page.HasMore || page.Generation != "7" {
					t.Fatalf("terminal total or shape lost: %+v %v", page, err)
				}
			} else if err == nil {
				t.Fatal("invalid or failed result accepted")
			}
			if scenario == "iteration-error" && !errors.Is(err, fault) {
				t.Fatalf("iteration error hidden: %v", err)
			}
			if !rows.closed || len(tx.queryRowStatements) != 1 || len(tx.queryStatements) != 1 {
				t.Fatal("rows leaked or combined query repeated")
			}
			if tx.queryArguments[0][0] != pgx.QueryExecModeExec || tx.queryArguments[0][1] != "tenant-a" {
				t.Fatal("local search must retain typed parameters and request-specific planning")
			}
		})
	}
}
