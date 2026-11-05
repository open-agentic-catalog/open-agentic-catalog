// Indexed search units exercise selection arguments and driver result handling.
package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// TestFederationIndexedSearch checks exact identity/continuation parameter shifts,
// result evidence and read-only repeatable-read intent without executing SQL.
func TestFederationIndexedSearch(t *testing.T) {
	failure := errors.New("indexed selection failure")
	for _, outcome := range []string{"page", "default limit", "exact", "public visibility", "scan", "iteration"} {
		t.Run(outcome, func(t *testing.T) {
			store, tx, _, source := federationAdapterFixture(t)
			identifier := "urn:air:peer.example:agents:remote"
			row := []any{identifier, "1.0", true, catalog.KindAgent, "sha256:" + strings.Repeat("a", 64), "Remote", "Remote description", []string{"tools"}, "peer.example", "indexed:remote", 80, true, true, 3, []string{source.ID}, source.ID, int64(1), int64(1)}
			second := append([]any(nil), row...)
			second[0], second[5], second[9], second[10] = "urn:air:peer.example:agents:second", "Second", "indexed:second", 70
			rows := &identityScriptedRows{values: [][]any{row, second}}
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{int64(7), false}), scriptedValueRow([]any{2})}
			tx.queryResults = []identityQueryResult{{rows: rows}}
			request := catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "remote", Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"tools"}, Publisher: "peer.example", Visibility: catalog.VisibilityTenant, License: "MIT", Categories: []string{"tools"}, Origins: []catalog.SearchResultType{catalog.SearchResultIndexed}}, Limit: 1, ExpectedGeneration: "7", After: &catalog.CatalogSearchPosition{Score: 90, DisplayName: "Before", ResultID: "indexed:before"}}
			code := catalog.ErrorCode("")
			switch outcome {
			case "default limit":
				request.Limit = 0
				request.After = nil
			case "exact":
				request.ExactIdentity = &catalog.IndexedIdentity{Identifier: identifier, Version: "1.0", VersionPresent: true}
				rows.values = rows.values[:1]
				tx.queryRows[1] = scriptedValueRow([]any{1})
			case "public visibility":
				request.Query.Visibility = catalog.VisibilityPublic
			case "scan":
				rows.values = [][]any{{}}
				code = catalog.CodeDependencyUnavailable
			case "iteration":
				rows.err = failure
				code = catalog.CodeDependencyUnavailable
			}
			connection := &captureCommandConnection{transaction: tx}
			command := &commandTransaction{pool: store.repository.pool, conn: connection, tenantID: source.TenantID}
			got, err := store.SearchIndexedCatalog(withCommandTransaction(t.Context(), command), source.TenantID, request)
			if code != "" {
				if err == nil || catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, catalog.FederatedCatalogSearchPage{}) || tx.rollbacks != 1 || tx.commits != 0 {
					t.Fatalf("failed page=%+v %v", got, err)
				}
			} else {
				if err != nil || got.Generation != "7" || got.Indexed == nil || tx.commits != 1 || tx.rollbacks != 0 {
					t.Fatalf("page=%+v %v", got, err)
				}
				if outcome == "public visibility" {
					if len(got.Indexed) != 0 || got.IndexedTotal != 0 || len(tx.queryStatements) != 0 || len(tx.queryRowStatements) != 1 {
						t.Fatal("public scope reached indexed corpus")
					}
				} else {
					total, items := 2, 1
					if outcome == "exact" {
						total = 1
					}
					if outcome == "default limit" {
						items = 2
					}
					if got.IndexedTotal != total || len(got.Indexed) != items || got.HasMore != (outcome == "page") {
						t.Fatalf("page bounds=%+v", got)
					}
					item := got.Indexed[0]
					if item.Identifier != identifier || item.Score != 80 || item.Type != catalog.SearchResultIndexed || item.Indexed == nil || !item.Indexed.Conflict || !item.Indexed.SameSourceConflict || !item.Indexed.SourcesTruncated || item.Indexed.SourceCount != 3 || !reflect.DeepEqual(item.Indexed.SourceIDs, []string{source.ID}) || item.Indexed.ObservationGroupID == "" {
						t.Fatalf("indexed evidence=%+v", item)
					}
				}
			}
			if !reflect.DeepEqual(connection.options, []pgx.TxOptions{{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}}) {
				t.Fatalf("options=%v", connection.options)
			}
			if outcome == "public visibility" {
				return
			}
			if !rows.closed {
				t.Fatal("indexed rows leaked")
			}
			base := []any{source.TenantID, "remote", "remote", []string{"agent"}, []string{"tools"}, "peer.example", "MIT", []string{"tools"}}
			if outcome == "exact" {
				base = append(base, identifier, "1.0", true)
				if !strings.Contains(tx.queryStatements[0], "observation.identifier=$9") || !strings.Contains(tx.queryStatements[0], "LIMIT $16") {
					t.Fatal("exact identity query parameter positions lost")
				}
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{source.TenantID}, base}) {
				t.Fatalf("generation/count args=%v", tx.queryRowArguments)
			}
			tail := []any{true, 90, "Before", "indexed:before", 2}
			if outcome == "default limit" {
				tail = []any{false, 0, "", "", 21}
			}
			if !reflect.DeepEqual(tx.queryArguments, [][]any{append(base, tail...)}) {
				t.Fatalf("page args=%v", tx.queryArguments)
			}
			if outcome == "iteration" && !errors.Is(err, failure) {
				t.Fatalf("lost cause: %v", err)
			}
		})
	}
}
