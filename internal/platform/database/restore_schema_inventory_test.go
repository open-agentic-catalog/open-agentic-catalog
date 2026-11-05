package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// These are catalog query projections. They do not execute policies or prove
// that the SQL selects the complete inventory; PostgreSQL tests own that boundary.
func restoreRLSRows() [][]any {
	var rows [][]any
	for _, table := range catalogTableNames {
		if table == "catalog_schema_migrations" {
			rows = append(rows, []any{table, false, false, "", "", false, "", ""})
			continue
		}
		column := "tenant_id"
		if table == "tenants" {
			column = "id"
		}
		predicate := "(" + column + " = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)"
		rows = append(rows, []any{table, true, true, "tenant_isolation", "*", true, predicate, predicate})
		if table == "personal_items" {
			const owner = `(membership_id = (NULLIF(current_setting('app.membership_id'::text, true), ''::text))::uuid)`
			rows = append(rows, []any{table, true, true, "personal_owner", "*", false, owner, owner})
		}
		if table == "tenant_identity_realms" {
			const realm = `((state = 'active'::text) AND (issuer = NULLIF(current_setting('app.identity_issuer'::text, true), ''::text)) AND (((NULLIF(current_setting('app.identity_tenant_id'::text, true), ''::text) IS NOT NULL) AND (tenant_id = (NULLIF(current_setting('app.identity_tenant_id'::text, true), ''::text))::uuid)) OR ((external_organization_key <> ''::text) AND (external_organization_key = NULLIF(current_setting('app.identity_organization'::text, true), ''::text)))))`
			rows = append(rows, []any{table, true, true, "verified_identity_realm_resolution", "r", true, realm, ""})
		}
	}
	return rows
}

func restoreTableRows() [][]any {
	names := slices.Clone(catalogTableNames)
	slices.Sort(names)
	rows := make([][]any, 0, len(names))
	for _, name := range names {
		rows = append(rows, []any{name})
	}
	return rows
}

func TestRestoreSchemaInventoriesPropagateReadFailures(t *testing.T) {
	failure := errors.New("schema inventory unavailable")
	for _, test := range []struct {
		name      string
		check     func(context.Context, pgx.Tx, string) error
		arguments []any
	}{
		{"tables", verifyRestoreTableInventory, []any{privilegeTestSchema, len(catalogTableNames) + len(semanticTableColumns) + 1}},
		{"policies", verifyRestoreRLS, []any{privilegeTestSchema, catalogTableNames, len(catalogTableNames) + 3}},
		{"constraints", verifyRestoreConstraints, []any{privilegeTestSchema, maxRestoreSchemaConstraints + 1}},
		{"triggers", verifyRestoreTriggers, []any{privilegeTestSchema, maxRestoreSchemaTriggers + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, mode := range []string{"query", "scan", "iteration"} {
				t.Run(mode, func(t *testing.T) {
					rows := &identityScriptedRows{}
					result := identityQueryResult{rows: rows}
					switch mode {
					case "query":
						result = identityQueryResult{err: failure}
					case "scan":
						rows.values = [][]any{{}}
					case "iteration":
						rows.err = failure
					}
					tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{result}}
					err := test.check(t.Context(), tx, privilegeTestSchema)
					if err == nil || mode != "scan" && !errors.Is(err, failure) {
						t.Fatalf("inventory error = %v", err)
					}
					if !reflect.DeepEqual(tx.queryArguments, [][]any{test.arguments}) || mode != "query" && !rows.closed || len(tx.queryRowStatements) != 0 || len(tx.statements) != 0 {
						t.Fatalf("inventory scope/cleanup: args=%v closed=%v", tx.queryArguments, rows.closed)
					}
				})
			}
		})
	}
}

func TestRestoreTableInventoryRequiresExactMembership(t *testing.T) {
	for _, mode := range []string{"exact", "missing", "extra", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			rows := &identityScriptedRows{values: restoreTableRows()}
			switch mode {
			case "missing":
				rows.values = rows.values[1:]
			case "extra":
				rows.values = append(rows.values, []any{"unexpected_table"})
			case "duplicate":
				rows.values[1] = rows.values[0]
			}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			err := verifyRestoreTableInventory(t.Context(), tx, privilegeTestSchema)
			if mode == "exact" && err != nil || mode != "exact" && (err == nil || err.Error() != "catalog table inventory drifted") || !rows.closed {
				t.Fatalf("inventory error = %v, closed = %v", err, rows.closed)
			}
		})
	}
}

func TestRestoreRLSRequiresExactPolicyProperties(t *testing.T) {
	for _, mode := range []string{"exact", "whitespace", "ledger enabled", "ledger forced", "ledger policy", "disabled", "not forced", "restrictive", "unnamed", "unknown", "command", "qualifier", "check", "realm table", "realm command", "realm check", "realm qualifier", "missing table", "duplicate", "missing realm"} {
		t.Run(mode, func(t *testing.T) {
			rows := &identityScriptedRows{values: restoreRLSRows()}
			index := func(table, policy string) int {
				return slices.IndexFunc(rows.values, func(v []any) bool { return v[0] == table && v[3] == policy })
			}
			ledger := rows.values[index("catalog_schema_migrations", "")]
			tenant := rows.values[index("tenants", "tenant_isolation")]
			realmIndex := index("tenant_identity_realms", "verified_identity_realm_resolution")
			realm := rows.values[realmIndex]
			message := ""
			switch mode {
			case "whitespace":
				predicate, ok := tenant[6].(string)
				if !ok {
					t.Fatal("tenant predicate fixture is not text")
				}
				tenant[6], tenant[7] = " \n"+predicate+"\t", "\t"+predicate+"\n"
			case "ledger enabled":
				ledger[1], message = true, "migration table unexpectedly uses tenant RLS"
			case "ledger forced":
				ledger[2], message = true, "migration table unexpectedly uses tenant RLS"
			case "ledger policy":
				ledger[3], message = "unexpected", "migration table unexpectedly uses tenant RLS"
			case "disabled":
				tenant[1], message = false, "missing forced RLS"
			case "not forced":
				tenant[2], message = false, "missing forced RLS"
			case "restrictive":
				tenant[5], message = false, "missing forced RLS"
			case "unnamed":
				tenant[3], message = "", "missing forced RLS"
			case "unknown":
				tenant[3], message = "unknown", "unexpected Catalog RLS policy"
			case "command":
				tenant[4], message = "r", "tenant isolation policy definition drifted"
			case "qualifier":
				tenant[6], message = "true", "tenant isolation policy definition drifted"
			case "check":
				tenant[7], message = "true", "tenant isolation policy definition drifted"
			case "realm table":
				realm[0], message = "tenants", "identity-realm resolution policy definition drifted"
			case "realm command":
				realm[4], message = "*", "identity-realm resolution policy definition drifted"
			case "realm check":
				realm[7], message = "true", "identity-realm resolution policy definition drifted"
			case "realm qualifier":
				realm[6], message = "true", "identity-realm resolution policy definition drifted"
			case "missing table":
				rows.values = slices.Delete(rows.values, index("tenants", "tenant_isolation"), index("tenants", "tenant_isolation")+1)
				message = "inventory is incomplete"
			case "duplicate":
				rows.values = append(rows.values, tenant)
				message = "inventory is incomplete"
			case "missing realm":
				rows.values = slices.Delete(rows.values, realmIndex, realmIndex+1)
				message = "inventory is incomplete"
			}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			err := verifyRestoreRLS(t.Context(), tx, privilegeTestSchema)
			if message == "" && err != nil || message != "" && (err == nil || !strings.Contains(err.Error(), message)) || !rows.closed {
				t.Fatalf("policy error = %v, want %q; closed=%v", err, message, rows.closed)
			}
		})
	}
}

func TestRestoreFunctionInventoryBound(t *testing.T) {
	failure := errors.New("function inventory unavailable")
	for _, count := range []int{0, maxRestoreSchemaFunctions, maxRestoreSchemaFunctions + 1, -1} {
		row := scriptedValueRow([]any{count})
		if count == -1 {
			row = scannerFunc(func(...any) error { return failure })
		}
		tx := &identityScriptedTx{queryRows: []pgx.Row{row}}
		err := verifyRestoreMetadataBounds(t.Context(), tx, privilegeTestSchema)
		if (err != nil) != (count == -1 || count > maxRestoreSchemaFunctions) || count == -1 && !errors.Is(err, failure) || !reflect.DeepEqual(tx.queryRowArguments, [][]any{{privilegeTestSchema, maxRestoreSchemaFunctions + 1}}) {
			t.Fatalf("function count %d: error=%v args=%v", count, err, tx.queryRowArguments)
		}
	}
}

func TestRestoreConstraintsRejectDriftAndUnboundedInventory(t *testing.T) {
	for _, mode := range []string{"empty", "definition drift", "whitespace", "changed definition", "unvalidated", "at bound", "over bound"} {
		t.Run(mode, func(t *testing.T) {
			rows := &identityScriptedRows{}
			message := "catalog constraint inventory or definition drifted (observed digest "
			wantDigest := ""
			switch mode {
			case "empty":
				wantDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
			case "definition drift", "whitespace", "changed definition":
				// Fixed digests bind every projected field, row/field separator and
				// normalized definition, independently of the production encoder.
				// Neither two-row inventory matches the complete Catalog schema.
				rows.values = [][]any{
					{"resources", "fixture_check", "c", true, "CHECK (true)"},
					{"tenants", "fixture_primary", "p", true, "PRIMARY KEY (id)"},
				}
				wantDigest = "39387f5904450b1f21e1fe67bbdb145c3aaf5195c0ab74388c22cfe711e44287"
				if mode == "whitespace" {
					rows.values[0][4] = "\n CHECK \t (true) "
					rows.values[1][4] = " PRIMARY\nKEY \t(id)\n"
				}
				if mode == "changed definition" {
					rows.values[0][4] = "CHECK (false)"
					wantDigest = "90202f07add5ed32d246b5ff767af8fc966b67f181f7c954d72d4a4ab9cba634"
				}
			case "unvalidated":
				rows.values = [][]any{{"resources", "fixture_check", "c", false, "CHECK (true)"}}
				message = "unexpected unvalidated Catalog constraint"
			case "at bound", "over bound":
				count := maxRestoreSchemaConstraints
				if mode == "over bound" {
					count++
					message = "catalog constraint inventory exceeds its verification bound"
				}
				for i := range count {
					rows.values = append(rows.values, []any{"resources", fmt.Sprintf("fixture_%04d", i), "c", true, "CHECK (true)"})
				}
			}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			err := verifyRestoreConstraints(t.Context(), tx, privilegeTestSchema)
			if err == nil || !strings.HasPrefix(err.Error(), message) || !rows.closed || rows.index != len(rows.values) {
				t.Fatalf("constraint error=%v rows=%d closed=%v", err, rows.index, rows.closed)
			}
			if wantDigest != "" && err.Error() != message+wantDigest+")" {
				t.Fatalf("constraint fingerprint = %v, want digest %s", err, wantDigest)
			}
		})
	}
}
