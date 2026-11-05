// Privilege fixtures describe catalog query results, not effective PostgreSQL
// permissions. The independent manifest and integration tests own authority.
package database

import (
	"slices"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"
)

const (
	privilegeTestSchema = `catalog"schema`
	privilegeTestOwner  = `migration"role`
	privilegeTestAPI    = `api"role`
	privilegeTestWorker = `worker"role`
)

type privilegeInspectionFixture struct {
	tx                                                      *identityScriptedTx
	columns, publicTables, functions, extensions, sequences *identityScriptedRows
	tableNames                                              []string
	tableValues                                             map[string][]any
	schemaValues, extensionSchemaValues, defaultValues      []any
}

func newPrivilegeInspectionFixture(t *testing.T, runtimeRole string, semantic bool, extensionSchema string) privilegeInspectionFixture {
	t.Helper()
	manifest, err := manifestForRuntimeRole(runtimeRole)
	if err != nil {
		t.Fatal(err)
	}
	f := privilegeInspectionFixture{
		tx:      &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}},
		columns: &identityScriptedRows{}, publicTables: &identityScriptedRows{}, functions: &identityScriptedRows{}, extensions: &identityScriptedRows{}, sequences: &identityScriptedRows{},
		tableValues: make(map[string][]any), schemaValues: []any{true, false, false, false}, extensionSchemaValues: []any{true, false, false}, defaultValues: []any{0},
	}
	columns := make(map[string][]string, len(catalogTableColumns)+len(semanticTableColumns))
	for table, names := range catalogTableColumns {
		columns[table] = names
	}
	if semantic {
		for table, names := range semanticTableColumns {
			columns[table] = names
		}
	}
	for table := range columns {
		f.tableNames = append(f.tableNames, table)
	}
	sort.Strings(f.tableNames)
	f.tx.queryRows = append(f.tx.queryRows, scriptedValueRow(f.schemaValues))
	for _, table := range f.tableNames {
		rights := manifest.tables[table]
		for _, column := range columns[table] {
			f.columns.values = append(f.columns.values, []any{table, column, slices.Contains(rights.selectColumns, column), slices.Contains(rights.insertColumns, column), slices.Contains(rights.updateColumns, column)})
		}
		f.publicTables.values = append(f.publicTables.values, []any{table, false, false})
		f.tableValues[table] = []any{false, false, false, rights.deleteRows, false, false, false}
		f.tx.queryRows = append(f.tx.queryRows, scriptedValueRow(f.tableValues[table]))
	}
	for _, function := range catalogFunctionNames {
		f.functions.values = append(f.functions.values, []any{function, slices.Contains(manifest.functions, function), false, false})
	}
	f.extensions.values = [][]any{
		{"pgcrypto", extensionSchema, "digest", "bytea, text", false, true, false},
		{"pgcrypto", extensionSchema, "digest", "text, text", false, runtimeRole == RuntimeRoleAPI, false},
		{"pgcrypto", extensionSchema, "hmac", "bytea, bytea, text", false, false, false},
		{"vector", extensionSchema, "array_to_vector", "real[], integer, boolean", false, semantic, false},
		{"vector", extensionSchema, "cosine_distance", "vector, vector", false, semantic && runtimeRole == RuntimeRoleAPI, false},
		{"vector", extensionSchema, "vector_dims", "vector", false, semantic && runtimeRole == RuntimeRoleWorker, false},
		{"vector", extensionSchema, "vector_norm", "vector", false, semantic && runtimeRole == RuntimeRoleWorker, false},
		{"unrelated", "unrelated_extensions", "digest", "bytea, text", false, false, false},
	}
	if extensionSchema != privilegeTestSchema && extensionSchema != "pg_catalog" {
		f.tx.queryRows = append(f.tx.queryRows, scriptedValueRow(f.extensionSchemaValues))
	}
	f.tx.queryRows = append(f.tx.queryRows, scriptedValueRow(f.defaultValues))
	f.tx.queryResults = []identityQueryResult{{rows: f.columns}, {rows: f.publicTables}, {rows: f.functions}, {rows: f.extensions}, {rows: f.sequences}}
	return f
}

func privilegeRoleRow(role string, worker bool) pgx.Row {
	return scriptedValueRow([]any{role, true, false, false, false, false, false, worker})
}

func privilegeBindingRows() []pgx.Row {
	return []pgx.Row{scriptedValueRow([]any{privilegeTestOwner}), privilegeRoleRow(privilegeTestAPI, false), scriptedValueRow([]any{false}), privilegeRoleRow(privilegeTestWorker, true), scriptedValueRow([]any{false})}
}

type privilegeReconciliationFixture struct {
	database              *migrationScriptedDatabase
	tx                    *identityScriptedTx
	functions, extensions *identityScriptedRows
	api, worker           privilegeInspectionFixture
}

func newPrivilegeReconciliationFixture(t *testing.T, extensionSchema string) privilegeReconciliationFixture {
	t.Helper()
	f := privilegeReconciliationFixture{
		tx:         &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}},
		functions:  &identityScriptedRows{},
		extensions: &identityScriptedRows{},
		api:        newPrivilegeInspectionFixture(t, RuntimeRoleAPI, true, extensionSchema),
		worker:     newPrivilegeInspectionFixture(t, RuntimeRoleWorker, true, extensionSchema),
	}
	functions := append(slices.Clone(catalogFunctionNames), "future_private_function()")
	slices.Sort(functions)
	for _, function := range functions {
		f.functions.values = append(f.functions.values, []any{function})
	}
	for _, rows := range []*identityScriptedRows{f.api.functions, f.worker.functions} {
		rows.values = append(rows.values, []any{"future_private_function()", false, false, false})
	}
	f.database = &migrationScriptedDatabase{
		identityScriptedTx: &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: privilegeBindingRows()},
		transactions:       []pgx.Tx{f.tx},
	}
	f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{privilegeTestSchema, privilegeTestOwner}), scriptedValueRow([]any{privilegeTestOwner, privilegeTestOwner})}
	for range catalogTableNames {
		f.tx.queryRows = append(f.tx.queryRows, scriptedValueRow([]any{privilegeTestOwner}))
	}
	// The owner inventories all eligible extension functions before revoking
	// their grants. The subsequent role inventories model the verified result.
	for _, values := range f.api.extensions.values {
		f.extensions.values = append(f.extensions.values, slices.Clone(values))
	}
	f.tx.queryResults = []identityQueryResult{{rows: f.functions}, {rows: f.extensions}}
	for _, inspection := range []privilegeInspectionFixture{f.api, f.worker} {
		f.tx.queryRows = append(f.tx.queryRows, inspection.tx.queryRows...)
		f.tx.queryResults = append(f.tx.queryResults, inspection.tx.queryResults...)
	}
	return f
}
