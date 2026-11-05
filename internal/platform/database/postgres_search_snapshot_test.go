// Search repository units select and hydrate controlled rows in one transaction.
package database

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// selectedCatalogHydration returns bounded batch rows using the existing record
// and revision fixtures. Reversed joined rows let the caller test selection order.
func selectedCatalogHydration() []identityQueryResult {

	r, _, v, l := catalogRecordReadFixture()
	row := []any{r.Resource.ID, r.Resource.TenantID, r.Resource.NamespaceID, r.Resource.CanonicalIdentifier, r.Resource.Kind, r.Version.ID, r.Version.Version,
		v[0], v[4], v[10], r.Listing.ID, r.Listing.NamespaceID, l[0], l[5], l[6], l[7], l[10], l[15],
		r.Release.ID, r.Release.VersionRevisionID, r.Release.ListingRevisionID, r.Release.State, r.Release.ActivatedAt,
		v[13], v[14], catalog.TrustNotAssessed, v[21], v[16], v[20], catalog.LegalRightsNotAssessed, "Example publisher", "Example namespace", "Apache-2.0", false, []byte(nil)}
	second := append([]any(nil), row...)
	second[10], second[12], second[18], second[20] = "second-listing", "second-listing-revision", "second-release", "second-listing-revision"
	return []identityQueryResult{
		{rows: &identityScriptedRows{values: [][]any{second, row}}},
		{rows: &identityScriptedRows{values: [][]any{{"revision", "representation", "", "application/json", restoreExampleContent, int64(18), false}}}},
	}
}

// TestLocalSearchHydratesOnlySelectedPage checks limit+1 truncation before child
// reads, output ordering/scores and failure propagation through the public method.
func TestLocalSearchHydratesOnlySelectedPage(t *testing.T) {
	pool := &pgxpool.Pool{}
	repo := &PostgresRepository{pool: pool}
	failure := errors.New("selected hydration failed")
	for _, outcome := range []string{"page", "default limit", "hydration failure"} {
		t.Run(outcome, func(t *testing.T) {
			selection := &identityScriptedRows{values: [][]any{{9, new("release"), new(15)}, {9, new("second-release"), new(7)}, {9, new("excluded-release"), new(3)}}}
			results := append([]identityQueryResult{{rows: selection}}, selectedCatalogHydration()...)
			request := catalog.CatalogSearchRepositoryRequest{Query: catalog.SearchQuery{Text: "example", Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"tools"}, Publisher: "publisher", Visibility: catalog.VisibilityPublic, License: "Apache-2.0", Categories: []string{"tools"}}, Limit: 2, ExpectedGeneration: "7", After: &catalog.CatalogSearchPosition{Score: 20, DisplayName: "Before", ResultID: "local:before"}}
			if outcome == "default limit" {
				request.Limit = 0
				request.After = nil
				selection.values = selection.values[:2]
			}
			if outcome == "hydration failure" {
				results[1].err = failure
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(7), false})}, queryResults: results}
			connection := &captureCommandConnection{transaction: tx}
			command := &commandTransaction{pool: pool, conn: connection, tenantID: restoreTestTenant}
			page, err := repo.SearchCatalog(withCommandTransaction(t.Context(), command), restoreTestTenant, request)
			if !selection.closed {
				t.Fatal("selection rows leaked")
			}
			if outcome == "hydration failure" {
				if !errors.Is(err, failure) || !reflect.DeepEqual(page, catalog.CatalogSearchRepositoryPage{}) || tx.rollbacks != 1 || tx.commits != 0 || len(tx.queryStatements) != 2 {
					t.Fatalf("failure page=%+v error=%v", page, err)
				}
				return
			}
			if err != nil || len(page.Items) != 2 || page.Total != 9 || page.Generation != "7" || page.HasMore != (outcome == "page") {
				t.Fatalf("page=%+v error=%v", page, err)
			}
			if page.Items[0].Release.ID != "release" || page.Items[0].RelevanceScore != 15 || page.Items[1].Release.ID != "second-release" || page.Items[1].RelevanceScore != 7 {
				t.Fatalf("selection order/scores lost: %+v", page.Items)
			}
			want, _, _, _ := catalogRecordReadFixture()
			requireCatalogSummary(t, page.Items[0], want)
			if !reflect.DeepEqual(tx.queryArguments[1], []any{restoreTestTenant, []string{"release", "second-release"}}) {
				t.Fatalf("sentinel hydrated or ordering lost: %v", tx.queryArguments[1])
			}
			for _, result := range results {
				rows, ok := result.rows.(*identityScriptedRows)
				if !ok || !rows.closed {
					t.Fatal("hydration rows not closed")
				}
			}
			wantArgs := []any{pgx.QueryExecModeExec, restoreTestTenant, "example", "example", []string{"agent"}, []string{"tools"}, "publisher", "public", "", "Apache-2.0", []string{"tools"}, true, 20, "Before", "local:before", 3}
			if outcome == "default limit" {
				wantArgs[11], wantArgs[12], wantArgs[13], wantArgs[14], wantArgs[15] = false, 0, "", "", 21
			}
			if !reflect.DeepEqual(tx.queryArguments[0], wantArgs) {
				t.Fatalf("search args=%v want=%v", tx.queryArguments[0], wantArgs)
			}
			if !reflect.DeepEqual(connection.options, []pgx.TxOptions{{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}}) || tx.commits != 1 || tx.rollbacks != 0 {
				t.Fatalf("snapshot ownership options=%v commits=%d rollback=%d", connection.options, tx.commits, tx.rollbacks)
			}
		})
	}
}

// TestCombinedCatalogSearchSnapshot preserves both result sets and runs indexed
// reads only after successful local selection, all before the one read commit.
func TestCombinedCatalogSearchSnapshot(t *testing.T) {
	pool := &pgxpool.Pool{}
	repo := &PostgresRepository{pool: pool}
	failure := errors.New("snapshot read failure")
	for _, outcome := range []string{"combined", "local failure", "indexed failure"} {
		t.Run(outcome, func(t *testing.T) {
			localRows := &identityScriptedRows{values: [][]any{{2, new("release"), new(15)}, {2, new("second-release"), new(7)}}}
			indexedRows := &identityScriptedRows{values: [][]any{{"urn:air:peer.example:agents:remote", "1.0", true, catalog.KindAgent, "sha256:digest", "Remote", "Remote description", []string{"tools"}, "peer", "indexed:remote", 5, false, false, 1, []string{"source"}, "source", int64(1), int64(1)}}}
			results := append([]identityQueryResult{{rows: localRows}}, selectedCatalogHydration()...)
			results = append(results, identityQueryResult{rows: indexedRows})
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(7), false}), scriptedValueRow([]any{int64(7), false}), scriptedValueRow([]any{1})}, queryResults: results}
			if outcome == "local failure" {
				tx.queryResults[0].err = failure
			}
			if outcome == "indexed failure" {
				tx.queryRows[1] = scannerFunc(func(...any) error { return failure })
			}
			connection := &captureCommandConnection{transaction: tx}
			command := &commandTransaction{pool: pool, conn: connection, tenantID: restoreTestTenant}
			page, err := repo.SearchCatalogSnapshot(withCommandTransaction(t.Context(), command), restoreTestTenant, catalog.CatalogSearchRepositoryRequest{Limit: 2, ExpectedGeneration: "7"}, catalog.FederatedCatalogSearchRequest{Limit: 2, ExpectedGeneration: "7"})
			if outcome != "combined" {
				if !errors.Is(err, failure) || !reflect.DeepEqual(page, catalog.CatalogSearchSnapshot{}) || tx.rollbacks != 1 || tx.commits != 0 {
					t.Fatalf("partial snapshot=%+v error=%v", page, err)
				}
				reads, queries := 1, 1
				if outcome == "indexed failure" {
					reads, queries = 2, 3
				}
				if len(tx.queryRowStatements) != reads || len(tx.queryStatements) != queries {
					t.Fatalf("continued after failure: reads=%d queries=%d", len(tx.queryRowStatements), len(tx.queryStatements))
				}
			} else {
				if err != nil || page.Local.Generation != "7" || page.Indexed.Generation != "7" || page.Local.Total != 2 || len(page.Local.Items) != 2 || page.Indexed.IndexedTotal != 1 || len(page.Indexed.Indexed) != 1 || page.Local.HasMore || page.Indexed.HasMore {
					t.Fatalf("combined=%+v error=%v", page, err)
				}
				item := page.Indexed.Indexed[0]
				if item.Type != catalog.SearchResultIndexed || item.Identifier != "urn:air:peer.example:agents:remote" || item.Indexed == nil || item.Indexed.ObservationGroupID == "" {
					t.Fatalf("indexed projection=%+v", item)
				}
				if tx.commits != 1 || tx.rollbacks != 0 || !localRows.closed || !indexedRows.closed || len(tx.queryStatements) != 4 {
					t.Fatal("snapshot did not close and commit both result sets")
				}
			}
			if !reflect.DeepEqual(connection.options, []pgx.TxOptions{{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}}) {
				t.Fatalf("split transaction=%v", connection.options)
			}
			for _, args := range tx.queryRowArguments {
				if args[0] != restoreTestTenant {
					t.Fatal("generation/count lost tenant")
				}
			}
		})
	}
}

func requireCatalogSummary(t *testing.T, got catalog.CatalogSummary, want catalog.CatalogRecord) {
	t.Helper()
	if got.Display == nil || got.Display.PublisherName != "Example publisher" || got.Display.NamespaceName != "Example namespace" || got.Display.License != "Apache-2.0" || got.Display.LicenseOmitted {
		t.Fatalf("display=%+v", got.Display)
	}
	if got.Resource.ID != want.Resource.ID || got.Resource.TenantID != want.Resource.TenantID || got.Version.Version != want.Version.Version || got.Release.ID != want.Release.ID || got.Listing.ID != want.Listing.ID || got.VersionRevision.ID != want.Release.VersionRevisionID || got.VersionRevision.EvidenceSetID != "latest-evidence" || got.Evidence.ID != "latest-evidence" || got.Evidence.State != catalog.EvidenceComplete || got.Evidence.Trust.State != catalog.TrustNotAssessed || got.Evidence.LegalRights.State != catalog.LegalRightsNotAssessed || got.ListingRevision.DisplayName != "Example" || got.Source != "local" {
		t.Fatalf("summary=%+v", got)
	}
}
