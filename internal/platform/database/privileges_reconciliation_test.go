package database

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRuntimePrivilegeReconciliationCommitsVerifiedManifests(t *testing.T) {
	for _, extensionSchema := range []string{privilegeTestSchema, "pg_catalog", `extension"schema`} {
		for _, acquireLock := range []bool{false, true} {
			name := extensionSchema + "/existing lock"
			if acquireLock {
				name = extensionSchema + "/transaction lock"
			}
			t.Run(name, func(t *testing.T) {
				f := newPrivilegeReconciliationFixture(t, extensionSchema)
				reports, err := reconcileRuntimePrivileges(context.Background(), f.database, privilegeTestAPI, privilegeTestWorker, acquireLock)
				want := []RuntimePrivilegeReport{
					{ManifestVersion: runtimePrivilegeManifestVersion, Schema: privilegeTestSchema, RuntimeRole: RuntimeRoleAPI, DatabaseRole: privilegeTestAPI, Compliant: true},
					{ManifestVersion: runtimePrivilegeManifestVersion, Schema: privilegeTestSchema, RuntimeRole: RuntimeRoleWorker, DatabaseRole: privilegeTestWorker, Compliant: true},
				}
				if err != nil || !reflect.DeepEqual(reports, want) {
					t.Fatalf("reports = %#v, error = %v", reports, err)
				}
				if f.tx.commits != 1 || f.tx.rollbacks != 1 || len(f.database.transactions) != 0 || len(f.database.queryRows) != 0 || len(f.tx.queryRows) != 0 || len(f.tx.queryResults) != 0 || len(f.database.statements) != 0 {
					t.Fatalf("owner transaction: commits=%d rollbacks=%d remaining transactions=%d binding rows=%d inventory rows=%d queries=%d outside writes=%d", f.tx.commits, f.tx.rollbacks, len(f.database.transactions), len(f.database.queryRows), len(f.tx.queryRows), len(f.tx.queryResults), len(f.database.statements))
				}
				for _, rows := range []*identityScriptedRows{f.functions, f.extensions, f.api.columns, f.api.publicTables, f.api.functions, f.api.extensions, f.api.sequences, f.worker.columns, f.worker.publicTables, f.worker.functions, f.worker.extensions, f.worker.sequences} {
					if !rows.closed {
						t.Fatal("inventory rows were not closed")
					}
				}
				start := 0
				if acquireLock {
					if f.tx.statements[0] != "SELECT pg_advisory_xact_lock($1)" || !reflect.DeepEqual(f.tx.arguments[0], []any{migrationAdvisoryLockKey}) {
						t.Fatalf("unexpected reconciliation lock: %s, %v", f.tx.statements[0], f.tx.arguments[0])
					}
					start = 1
				}
				const roles = `"api""role","worker""role"`
				prefix := []string{
					`REVOKE ALL PRIVILEGES ON SCHEMA "catalog""schema" FROM PUBLIC,` + roles,
					`GRANT USAGE ON SCHEMA "catalog""schema" TO ` + roles,
					`REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA "catalog""schema" FROM PUBLIC,` + roles,
					`REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA "catalog""schema" FROM PUBLIC,` + roles,
				}
				if !reflect.DeepEqual(f.tx.statements[start:start+len(prefix)], prefix) {
					t.Fatalf("revocation prefix = %v", f.tx.statements[start:start+len(prefix)])
				}
				for _, function := range append(slices.Clone(catalogFunctionNames), "future_private_function()") {
					statement := `REVOKE ALL PRIVILEGES ON FUNCTION "catalog""schema".` + function + ` FROM PUBLIC, ` + roles
					if !slices.Contains(f.tx.statements, statement) {
						t.Fatalf("missing Catalog function revocation: %s", statement)
					}
				}
				if !reflect.DeepEqual(f.tx.queryArguments[0], []any{privilegeTestSchema}) || !reflect.DeepEqual(f.tx.queryArguments[1], []any{runtimeCryptoExtension, privilegeTestAPI, privilegeTestWorker}) {
					t.Fatalf("unscoped inventories: %v", f.tx.queryArguments[:2])
				}
				for i, table := range catalogTableNames {
					if !reflect.DeepEqual(f.tx.queryRowArguments[i+2], []any{privilegeTestSchema, table}) {
						t.Fatalf("owner check for %s = %v", table, f.tx.queryRowArguments[i+2])
					}
				}
				extension := pgx.Identifier{extensionSchema}.Sanitize()
				for _, function := range []string{`"digest"(bytea, text)`, `"digest"(text, text)`, `"hmac"(bytea, bytea, text)`, `"array_to_vector"(real[], integer, boolean)`, `"cosine_distance"(vector, vector)`, `"vector_dims"(vector)`, `"vector_norm"(vector)`} {
					statement := "REVOKE ALL PRIVILEGES ON FUNCTION " + extension + "." + function + " FROM PUBLIC, " + roles
					if !slices.Contains(f.tx.statements, statement) {
						t.Fatalf("missing extension revocation: %s", statement)
					}
				}
				if !slices.Contains(f.tx.statements, `REVOKE ALL PRIVILEGES ON FUNCTION "unrelated_extensions"."digest"(bytea, text) FROM PUBLIC, `+roles) {
					t.Fatal("unrelated extension kept its function grants")
				}
				for _, grant := range []struct{ signature, roles string }{
					{`"digest"(bytea, text)`, roles}, {`"digest"(text, text)`, `"api""role"`},
					{`"array_to_vector"(real[], integer, boolean)`, roles}, {`"cosine_distance"(vector, vector)`, `"api""role"`},
					{`"vector_dims"(vector)`, `"worker""role"`}, {`"vector_norm"(vector)`, `"worker""role"`},
				} {
					statement := "GRANT EXECUTE ON FUNCTION " + extension + "." + grant.signature + " TO " + grant.roles
					index := slices.IndexFunc(f.tx.statements, func(sql string) bool { return sql == statement || strings.Contains(sql, statement+"'';") })
					if index < 0 {
						t.Fatalf("missing exact extension grant %s", statement)
					}
					revocation := "REVOKE ALL PRIVILEGES ON FUNCTION " + extension + "." + grant.signature + " FROM PUBLIC, " + roles
					revokeIndex := slices.Index(f.tx.statements, revocation)
					if revokeIndex < 0 || revokeIndex >= index {
						t.Fatalf("extension grant at %d must follow revocation at %d: %s", index, revokeIndex, statement)
					}
					if !strings.HasPrefix(grant.signature, `"digest"`) && (!strings.HasPrefix(f.tx.statements[index], "DO ") || !strings.Contains(f.tx.statements[index], "catalog_semantic_schema") || !strings.Contains(f.tx.statements[index], "current_user")) {
						t.Fatalf("unguarded optional grant: %s", f.tx.statements[index])
					}
				}
				apiGrant, workerGrant, workerCleanup := false, false, false
				for _, statement := range f.tx.statements {
					apiGrant = apiGrant || strings.HasPrefix(statement, "GRANT SELECT (") && strings.HasSuffix(statement, `ON TABLE "catalog""schema"."tenants" TO "api""role"`)
					workerGrant = workerGrant || statement == `GRANT EXECUTE ON FUNCTION "catalog""schema".catalog_core_lexical_terms_v1(text) TO "worker""role"`
					workerCleanup = workerCleanup || statement == `GRANT DELETE ON TABLE "catalog""schema"."jobs" TO "worker""role"`
					if strings.Contains(statement, "GRANT OPTION") || strings.Contains(statement, `GRANT EXECUTE ON FUNCTION `+extension+`."hmac"`) || strings.Contains(statement, `GRANT EXECUTE ON FUNCTION "unrelated_extensions"."digest"`) {
						t.Fatalf("unexpected authority: %s", statement)
					}
					if strings.HasPrefix(statement, "GRANT SELECT (") && strings.HasSuffix(statement, `ON TABLE "catalog""schema"."tenants" TO "worker""role"`) || statement == `GRANT DELETE ON TABLE "catalog""schema"."jobs" TO "api""role"` || statement == `GRANT EXECUTE ON FUNCTION "catalog""schema".catalog_core_lexical_query_v1(text) TO "worker""role"` {
						t.Fatalf("API/worker manifest authority crossed roles: %s", statement)
					}
				}
				if !apiGrant || !workerGrant || !workerCleanup {
					t.Fatal("reconciliation omitted an API or worker manifest")
				}
				for _, scope := range []string{"", ` IN SCHEMA "catalog""schema"`} {
					for _, kind := range []string{"TABLES", "SEQUENCES", "FUNCTIONS", "TYPES"} {
						statement := `ALTER DEFAULT PRIVILEGES FOR ROLE "migration""role"` + scope + ` REVOKE ALL PRIVILEGES ON ` + kind + ` FROM PUBLIC,` + roles
						if !slices.Contains(f.tx.statements, statement) {
							t.Fatalf("missing default revocation: %s", statement)
						}
					}
				}
				outside := extensionSchema != "pg_catalog" && extensionSchema != privilegeTestSchema
				for _, statement := range []string{"REVOKE CREATE ON SCHEMA " + extension + " FROM " + roles, "GRANT USAGE ON SCHEMA " + extension + " TO " + roles} {
					// Main schema usage is already granted in the fixed prefix.
					if slices.Contains(f.tx.statements[start+len(prefix):], statement) != outside {
						t.Fatalf("extension schema privilege mismatch: %s", statement)
					}
				}
			})
		}
	}
}

func TestRuntimePrivilegeReconciliationStopsBeforeUnsafeWrites(t *testing.T) {
	injected := errors.New("privilege inventory unavailable")
	for _, stage := range []string{"role names", "role binding", "begin", "schema", "schema owner", "table owner"} {
		t.Run(stage, func(t *testing.T) {
			f := newPrivilegeReconciliationFixture(t, privilegeTestSchema)
			apiRole, wantError, reads, rollback := privilegeTestAPI, "", 5, 0
			wantCause := injected
			switch stage {
			case "role names":
				apiRole, wantError, reads, wantCause = "", "role name", 0, nil
			case "role binding":
				f.database.queryRows[0] = scannerFunc(func(...any) error { return injected })
				wantError, reads = "migration role", 1
			case "begin":
				f.database.beginErr, wantError = injected, "begin runtime privilege reconciliation"
			case "schema":
				f.tx.queryRows[0] = scannerFunc(func(...any) error { return injected })
				wantError, rollback = "resolve active Catalog schema", 1
			case "schema owner":
				f.tx.queryRows[1] = scriptedValueRow([]any{"other_owner", "other_owner"})
				wantError, rollback, wantCause = "must be owned by migration role", 1, nil
			case "table owner":
				f.tx.queryRows[2] = scriptedValueRow([]any{"other_owner"})
				wantError, rollback, wantCause = "must be owned by migration role", 1, nil
			}
			reports, err := reconcileRuntimePrivileges(context.Background(), f.database, apiRole, privilegeTestWorker, false)
			if err == nil || !strings.Contains(err.Error(), wantError) || wantCause != nil && !errors.Is(err, wantCause) || reports != nil {
				t.Fatalf("reports = %#v, error = %v", reports, err)
			}
			if len(f.database.queryRowStatements) != reads || len(f.database.statements) != 0 || len(f.tx.statements) != 0 || len(f.tx.queryStatements) != 0 || f.tx.commits != 0 || f.tx.rollbacks != rollback {
				t.Fatalf("preflight: reads=%d outside writes=%d owner writes=%d inventories=%d commits=%d rollbacks=%d", len(f.database.queryRowStatements), len(f.database.statements), len(f.tx.statements), len(f.tx.queryStatements), f.tx.commits, f.tx.rollbacks)
			}
		})
	}
}

func TestRuntimePrivilegeReconciliationRollsBackWriteFailures(t *testing.T) {
	baseline := newPrivilegeReconciliationFixture(t, `extension"schema`)
	if _, err := reconcileRuntimePrivileges(context.Background(), baseline.database, privilegeTestAPI, privilegeTestWorker, true); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("privilege write rejected")
	// One representative per write phase covers error classification and fail-fast
	// rollback without repeating the same test for every manifest column/table.
	for _, test := range []struct{ prefix, wantError string }{
		{"SELECT pg_advisory_xact_lock", "lock runtime privilege reconciliation"},
		{"REVOKE ALL PRIVILEGES ON SCHEMA", "revoke runtime schema privileges"},
		{"GRANT USAGE ON SCHEMA", "grant runtime schema usage"},
		{"REVOKE ALL PRIVILEGES ON ALL TABLES", "revoke runtime table privileges"},
		{"REVOKE ALL PRIVILEGES ON ALL SEQUENCES", "revoke runtime sequence privileges"},
		{`REVOKE ALL PRIVILEGES ON FUNCTION "catalog""schema"`, "revoke runtime execution on admit_webhook_notification()"},
		{`REVOKE ALL PRIVILEGES ON FUNCTION "extension""schema"`, "revoke runtime execution on pgcrypto function digest(bytea, text)"},
		{"GRANT EXECUTE ON FUNCTION", "grant runtime execution on pgcrypto function digest(bytea, text)"},
		{"DO ", "grant runtime execution on vector function array_to_vector(real[], integer, boolean)"},
		{"REVOKE CREATE ON SCHEMA", "revoke runtime creation"},
		{`GRANT USAGE ON SCHEMA "extension""schema"`, "grant runtime usage"},
		{"ALTER DEFAULT PRIVILEGES", "remove runtime default privileges"},
		{"GRANT SELECT (", "role privileges on"},
		{`GRANT EXECUTE ON FUNCTION "catalog""schema"`, "role execution on"},
	} {
		t.Run(test.wantError, func(t *testing.T) {
			index := slices.IndexFunc(baseline.tx.statements, func(sql string) bool { return strings.HasPrefix(sql, test.prefix) })
			if index < 0 {
				t.Fatalf("fixture did not exercise write phase %q", test.prefix)
			}
			f := newPrivilegeReconciliationFixture(t, `extension"schema`)
			f.tx.execErrors = make([]error, index+1)
			f.tx.execErrors[index] = injected
			reports, err := reconcileRuntimePrivileges(context.Background(), f.database, privilegeTestAPI, privilegeTestWorker, true)
			if !errors.Is(err, injected) || !strings.Contains(err.Error(), test.wantError) || reports != nil {
				t.Fatalf("reports = %#v, error = %v", reports, err)
			}
			if !reflect.DeepEqual(f.tx.statements, baseline.tx.statements[:index+1]) || f.tx.commits != 0 || f.tx.rollbacks != 1 || len(f.database.statements) != 0 {
				t.Fatalf("failed write %d: writes=%d commits=%d rollbacks=%d outside writes=%d", index, len(f.tx.statements), f.tx.commits, f.tx.rollbacks, len(f.database.statements))
			}
		})
	}
}

func TestRuntimePrivilegeReconciliationRejectsUnverifiedInventories(t *testing.T) {
	injected := errors.New("privilege verification failed")
	for _, stage := range []string{"function query", "function scan", "function iteration", "extension query", "missing byte digest", "missing text digest", "API inspection", "worker inspection", "API drift", "worker drift", "commit"} {
		t.Run(stage, func(t *testing.T) {
			f := newPrivilegeReconciliationFixture(t, privilegeTestSchema)
			wantError, wantCause, commits := "", injected, 0
			inspectionStart := 2 + len(catalogTableNames)
			switch stage {
			case "function query":
				f.tx.queryResults[0] = identityQueryResult{err: injected}
				wantError = "list Catalog-owned functions"
			case "function scan":
				f.functions.values = [][]any{{}}
				wantError, wantCause = "scan Catalog-owned function", nil
			case "function iteration":
				f.functions.err, wantError = injected, "iterate Catalog-owned functions"
			case "extension query":
				f.tx.queryResults[1] = identityQueryResult{err: injected}
				wantError = "inspect installed extension functions"
			case "missing byte digest", "missing text digest":
				index := 0
				if stage == "missing text digest" {
					index = 1
				}
				wantError, wantCause = "required pgcrypto function "+[]string{"digest(bytea, text)", "digest(text, text)"}[index]+" is missing", nil
				f.extensions.values = slices.Delete(f.extensions.values, index, index+1)
			case "API inspection":
				f.tx.queryRows[inspectionStart] = scannerFunc(func(...any) error { return injected })
				wantError = "schema privileges"
			case "worker inspection":
				f.tx.queryRows[inspectionStart+len(f.api.tx.queryRows)] = scannerFunc(func(...any) error { return injected })
				wantError = "schema privileges"
			case "API drift":
				f.api.schemaValues[0] = false
				wantError, wantCause = `PostgreSQL api role "api\"role" privilege reconciliation is incomplete: schema USAGE is missing`, nil
			case "worker drift":
				f.worker.schemaValues[0] = false
				wantError, wantCause = `PostgreSQL worker role "worker\"role" privilege reconciliation is incomplete: schema USAGE is missing`, nil
			case "commit":
				f.tx.commitErr, wantError, commits = injected, "commit runtime privilege reconciliation", 1
			}
			reports, err := reconcileRuntimePrivileges(context.Background(), f.database, privilegeTestAPI, privilegeTestWorker, false)
			if err == nil || !strings.Contains(err.Error(), wantError) || wantCause != nil && !errors.Is(err, wantCause) || reports != nil {
				t.Fatalf("reports = %#v, error = %v; want %q", reports, err, wantError)
			}
			if f.tx.commits != commits || f.tx.rollbacks != 1 || len(f.database.statements) != 0 {
				t.Fatalf("transaction: commits=%d rollbacks=%d outside writes=%d", f.tx.commits, f.tx.rollbacks, len(f.database.statements))
			}
			if stage != "function query" && !f.functions.closed {
				t.Fatal("Catalog function inventory was not closed")
			}
			if strings.HasPrefix(stage, "function") && len(f.tx.queryStatements) != 1 || stage == "API inspection" && len(f.tx.queryStatements) != 2 || stage == "worker inspection" && len(f.tx.queryStatements) != 7 || stage == "API drift" && len(f.tx.queryStatements) != 7 || stage == "worker drift" && len(f.tx.queryStatements) != 12 {
				t.Fatalf("continued inventory queries: %d", len(f.tx.queryStatements))
			}
		})
	}
}
