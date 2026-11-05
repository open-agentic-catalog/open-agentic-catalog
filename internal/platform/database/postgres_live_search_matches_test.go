package database

import (
	"errors"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestBoundedLiveMatchRowsCloseOnEveryOutcome(t *testing.T) {
	failure := errors.New("injected identity projection failure")
	for _, mode := range []string{"success", "query", "scan", "iterate", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			rows := &identityScriptedRows{values: [][]any{{"urn:air:example.com:agent:a", "1", true}}}
			result := identityQueryResult{rows: rows}
			switch mode {
			case "query":
				result.err = failure
			case "scan":
				rows.values = [][]any{{"wrong shape"}}
			case "iterate":
				rows.err = failure
			case "overflow":
				rows.values = make([][]any, 51)
				for i := range rows.values {
					rows.values[i] = []any{"urn:air:example.com:agent:a", "1", true}
				}
			}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{result}}
			got, err := readLiveMatches(t.Context(), tx, "SELECT bounded", nil)
			if mode == "success" {
				if err != nil || len(got) != 1 || !got[0].VersionPresent {
					t.Fatalf("success: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s", mode)
			}
			if mode != "query" && !rows.closed {
				t.Fatal("rows not closed")
			}
			if mode == "overflow" && catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("bound: %v", err)
			}
		})
	}
	for _, n := range []int{0, 50, 51} {
		args, err := identityProbeParameters(make([]catalog.IndexedIdentity, n))
		if n > 50 {
			if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("bound: %v", err)
			}
		} else if err != nil || len(args) != 3 {
			t.Fatalf("probe %d: %v", n, err)
		}
	}
}

func TestLocalLiveProjectionReusesFiltersWithoutHydration(t *testing.T) {
	tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: &identityScriptedRows{}}}}
	key := catalog.IndexedIdentity{Identifier: "urn:air:example.com:agent:a", Version: "1", VersionPresent: true}
	page, err := localLiveMatches(t.Context(), tx, restoreTestTenant, catalog.CatalogSearchRepositoryRequest{Query: catalog.SearchQuery{Text: "needle", License: "MIT"}, IdentityProbes: []catalog.IndexedIdentity{key}}, "42")
	if err != nil || page.Generation != "42" || len(tx.queryStatements) != 1 {
		t.Fatalf("projection: %+v %v", page, err)
	}
	statement := tx.queryStatements[0]
	for _, required := range []string{"listing.tenant_id=$1", "release.tenant_id=$1", "revision.tenant_id=$1", "version.tenant_id=$1", "unnest($11::text[],$12::text[],$13::boolean[])", "LIMIT 51"} {
		if !strings.Contains(statement, required) {
			t.Fatalf("missing selection fence %q", required)
		}
	}
	if len(tx.queryArguments[0]) != 13 {
		t.Fatalf("unbounded or changed parameters: %v", tx.queryArguments[0])
	}
}
