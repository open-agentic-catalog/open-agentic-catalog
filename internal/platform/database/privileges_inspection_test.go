package database

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRuntimePrivilegeInspectionAcceptsExactInventories(t *testing.T) {
	for _, runtimeRole := range []string{RuntimeRoleAPI, RuntimeRoleWorker} {
		for _, mode := range []string{"core", "semantic", "separate schema", "system schema"} {
			t.Run(runtimeRole+"/"+mode, func(t *testing.T) {
				schema := privilegeTestSchema
				if mode == "separate schema" {
					schema = `extension"schema`
				} else if mode == "system schema" {
					schema = "pg_catalog"
				}
				f := newPrivilegeInspectionFixture(t, runtimeRole, mode != "core", schema)
				role := privilegeTestAPI
				if runtimeRole == RuntimeRoleWorker {
					role = privilegeTestWorker
				}
				report, err := inspectRuntimePrivileges(t.Context(), f.tx, privilegeTestSchema, runtimeRole, role)
				want := RuntimePrivilegeReport{ManifestVersion: runtimePrivilegeManifestVersion, Schema: privilegeTestSchema, RuntimeRole: runtimeRole, DatabaseRole: role, Compliant: true}
				if err != nil || !reflect.DeepEqual(report, want) {
					t.Fatalf("exact inventory report=%+v error=%v", report, err)
				}
				if len(f.tx.statements) != 0 || len(f.tx.queryRows) != 0 || len(f.tx.queryResults) != 0 {
					t.Fatal("inspection wrote state or left scripted results")
				}
				wantQueries := [][]any{{role, privilegeTestSchema}, {privilegeTestSchema, role}, {role, privilegeTestSchema}, {runtimeCryptoExtension, role, role}, {role, privilegeTestSchema}}
				if !reflect.DeepEqual(f.tx.queryArguments, wantQueries) {
					t.Fatalf("inventory scope=%v", f.tx.queryArguments)
				}
				wantRows := [][]any{{role, privilegeTestSchema}}
				for _, table := range f.tableNames {
					wantRows = append(wantRows, []any{role, `"catalog""schema"."` + table + `"`})
				}
				if mode == "separate schema" {
					wantRows = append(wantRows, []any{role, schema})
				}
				wantRows = append(wantRows, []any{privilegeTestSchema, role})
				if !reflect.DeepEqual(f.tx.queryRowArguments, wantRows) {
					t.Fatalf("table/schema/default binding=%v", f.tx.queryRowArguments)
				}
				for _, rows := range []*identityScriptedRows{f.columns, f.publicTables, f.functions, f.extensions, f.sequences} {
					if !rows.closed {
						t.Fatal("inventory rows leaked")
					}
				}
			})
		}
	}
}

func TestRuntimePrivilegeInspectionReportsSpecificDrift(t *testing.T) {
	for _, mode := range []string{"schema", "missing column grant", "unexpected column", "missing column", "missing table", "partial semantic", "PUBLIC table", "table authority", "missing delete", "missing function grant", "unexpected function", "missing function", "extension authority", "missing digest", "separate schema", "sequence", "defaults"} {
		t.Run(mode, func(t *testing.T) {
			f := newPrivilegeInspectionFixture(t, RuntimeRoleAPI, true, `extension"schema`)
			var want []string
			switch mode {
			case "schema":
				copy(f.schemaValues, []any{false, true, true, true})
				want = []string{"schema USAGE is missing", "schema CREATE is not permitted", "unexpected PUBLIC privilege on Catalog schema", "unexpected grant option on Catalog schema"}
			case "missing column grant":
				for _, row := range f.columns.values {
					if row[0] == "tenants" && row[1] == "display_name" {
						row[2] = false
					}
				}
				want = []string{"missing SELECT on tenants.display_name"}
			case "unexpected column":
				f.columns.values = append(f.columns.values, []any{"tenants", "future_private", true, true, true})
				want = []string{"unexpected SELECT on tenants.future_private", "unexpected INSERT on tenants.future_private", "unexpected UPDATE on tenants.future_private"}
			case "missing column":
				f.columns.values = slices.DeleteFunc(f.columns.values, func(row []any) bool { return row[0] == "tenants" && row[1] == "display_name" })
				want = []string{"missing Catalog column tenants.display_name"}
			case "missing table", "partial semantic":
				table := "tenants"
				want = []string{"missing Catalog table tenants"}
				if mode == "partial semantic" {
					table = "semantic_members"
					want = make([]string, 0, 1+len(semanticTableColumns[table]))
					want = append(want, "missing optional Catalog table semantic_members")
					for _, column := range semanticTableColumns[table] {
						want = append(want, "missing optional Catalog column "+table+"."+column)
					}
				}
				f.columns.values = slices.DeleteFunc(f.columns.values, func(row []any) bool { return row[0] == table })
				index := slices.Index(f.tableNames, table) + 1
				f.tx.queryRows = slices.Delete(f.tx.queryRows, index, index+1)
			case "PUBLIC table":
				f.publicTables.values = [][]any{{"tenants", true, true}}
				want = []string{"unexpected PUBLIC privilege on table tenants", "unexpected grant option on table tenants"}
			case "table authority":
				copy(f.tableValues["tenants"], []any{true, true, true, true, true, true, true})
				want = []string{"unexpected table SELECT on tenants", "unexpected table INSERT on tenants", "unexpected table UPDATE on tenants", "unexpected DELETE on tenants", "unexpected TRUNCATE on tenants", "unexpected REFERENCES on tenants", "unexpected TRIGGER on tenants"}
			case "missing delete":
				f.tableValues["idempotency_keys"][3] = false
				want = []string{"missing DELETE on idempotency_keys"}
			case "missing function grant":
				for _, row := range f.functions.values {
					if row[0] == "catalog_core_lexical_query_v1(text)" {
						row[1] = false
					}
				}
				want = []string{"missing EXECUTE on catalog_core_lexical_query_v1(text)"}
			case "unexpected function":
				f.functions.values = append(f.functions.values, []any{"future_private_function()", true, true, true})
				want = []string{"unexpected EXECUTE on future_private_function()", "unexpected PUBLIC EXECUTE on future_private_function()", "unexpected grant option on function future_private_function()"}
			case "missing function":
				f.functions.values = slices.DeleteFunc(f.functions.values, func(row []any) bool { return row[0] == "catalog_core_lexical_query_v1(text)" })
				want = []string{"missing Catalog function catalog_core_lexical_query_v1(text)"}
			case "extension authority":
				f.extensions.values[0][5] = false
				f.extensions.values[2][4], f.extensions.values[2][5], f.extensions.values[2][6] = true, true, true
				f.extensions.values[7][5] = true // A namesake in another extension is not pgcrypto.
				want = []string{"missing EXECUTE on pgcrypto function digest(bytea, text)", "unexpected EXECUTE on pgcrypto function hmac(bytea, bytea, text)", "unexpected PUBLIC EXECUTE on pgcrypto function hmac(bytea, bytea, text)", "unexpected grant option on pgcrypto function hmac(bytea, bytea, text)", "unexpected EXECUTE on unrelated function digest(bytea, text)"}
			case "missing digest":
				f.extensions.values = f.extensions.values[1:]
				want = []string{"missing pgcrypto function digest(bytea, text)"}
			case "separate schema":
				copy(f.extensionSchemaValues, []any{false, true, true})
				want = []string{`missing schema USAGE on pgcrypto extension schema extension"schema`, `unexpected schema CREATE on pgcrypto extension schema extension"schema`, `unexpected grant option on pgcrypto extension schema extension"schema`}
			case "sequence":
				f.sequences.values = [][]any{{"future_sequence", true, false, false, true}}
				want = []string{"unexpected privilege on sequence future_sequence", "unexpected grant option on sequence future_sequence"}
			case "defaults":
				f.defaultValues[0] = 1
				want = []string{"unexpected future-object default privileges"}
			}
			report, err := inspectRuntimePrivileges(t.Context(), f.tx, privilegeTestSchema, RuntimeRoleAPI, privilegeTestAPI)
			slices.Sort(want)
			slices.Sort(report.Differences)
			if err != nil || report.Compliant || !slices.Equal(report.Differences, want) {
				t.Fatalf("drift=%v want=%v error=%v", report.Differences, want, err)
			}
			if len(f.tx.statements) != 0 || len(f.tx.queryRows) != 0 || len(f.tx.queryResults) != 0 {
				t.Fatal("inspection mutated or stopped before completing its inventory")
			}
		})
	}
}

func TestRuntimePrivilegeInspectionDiscardsFailedInventories(t *testing.T) {
	failure := errors.New("privilege catalog failure")
	for index, stage := range []string{"column privileges", "PUBLIC table privileges", "function privileges", "installed extension", "sequence privileges"} {
		for _, mode := range []string{"query", "scan", "iteration"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				f := newPrivilegeInspectionFixture(t, RuntimeRoleAPI, true, privilegeTestSchema)
				rows := []*identityScriptedRows{f.columns, f.publicTables, f.functions, f.extensions, f.sequences}
				switch mode {
				case "query":
					f.tx.queryResults[index].err = failure
				case "scan":
					rows[index].values = [][]any{{}}
				case "iteration":
					rows[index].err = failure
				}
				report, err := inspectRuntimePrivileges(t.Context(), f.tx, privilegeTestSchema, RuntimeRoleAPI, privilegeTestAPI)
				if err == nil || !reflect.DeepEqual(report, RuntimePrivilegeReport{}) || !strings.Contains(err.Error(), stage) || mode != "scan" && !errors.Is(err, failure) {
					t.Fatalf("failed inventory=%+v error=%v", report, err)
				}
				if len(f.tx.queryStatements) != index+1 || len(f.tx.statements) != 0 {
					t.Fatal("inspection continued or wrote after a failed inventory")
				}
				for i, row := range rows {
					wantClosed := i < index || i == index && mode != "query"
					if row.closed != wantClosed {
						t.Fatalf("rows %d closed=%t want=%t", i, row.closed, wantClosed)
					}
				}
			})
		}
	}
	for _, stage := range []string{"schema", "table", "extension schema", "defaults", "invalid role"} {
		t.Run(stage, func(t *testing.T) {
			f := newPrivilegeInspectionFixture(t, RuntimeRoleAPI, true, "extensions")
			index, queries := 0, 0
			runtimeRole := RuntimeRoleAPI
			switch stage {
			case "table":
				index, queries = 1, 2
			case "extension schema":
				index, queries = len(f.tableNames)+1, 4
			case "defaults":
				index, queries = len(f.tableNames)+2, 5
			case "invalid role":
				runtimeRole = "combined"
			}
			f.tx.queryRows[index] = scannerFunc(func(...any) error { return failure })
			report, err := inspectRuntimePrivileges(t.Context(), f.tx, privilegeTestSchema, runtimeRole, privilegeTestAPI)
			if err == nil || !reflect.DeepEqual(report, RuntimePrivilegeReport{}) || stage != "invalid role" && !errors.Is(err, failure) {
				t.Fatalf("failed inventory=%+v error=%v", report, err)
			}
			wantRows := index + 1
			if stage == "invalid role" {
				wantRows = 0
			}
			if len(f.tx.queryRowStatements) != wantRows || len(f.tx.queryStatements) != queries || len(f.tx.statements) != 0 {
				t.Fatal("inspection continued after failure")
			}
		})
	}
}

func TestRuntimePrivilegeInventoryOwnershipErrors(t *testing.T) {
	failure := errors.New("owner lookup failed")
	for _, stage := range []string{"schema", "schema owner", "table owner"} {
		t.Run(stage, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(...any) error { return failure })}}
			var err error
			switch stage {
			case "schema":
				var schema, role string
				schema, role, err = currentCatalogSchema(t.Context(), tx)
				if schema != "" || role != "" {
					t.Fatal("failed lookup returned schema authority")
				}
			case "schema owner":
				err = requireCatalogSchemaOwnership(t.Context(), tx, privilegeTestSchema, privilegeTestOwner)
			case "table owner":
				err = requireCatalogTables(t.Context(), tx, privilegeTestSchema, privilegeTestOwner)
			}
			if !errors.Is(err, failure) || len(tx.queryRowStatements) != 1 || len(tx.statements) != 0 {
				t.Fatalf("owner failure=%v calls=%v", err, tx.queryRowStatements)
			}
		})
	}
}
