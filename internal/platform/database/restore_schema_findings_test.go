package database

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type restoreSchemaFixture struct {
	tx          *identityScriptedTx
	rows        map[string]int
	queries     map[string]int
	api, worker privilegeInspectionFixture
}

func restoreInventoryRows(t *testing.T, result identityQueryResult) *identityScriptedRows {
	t.Helper()
	rows, ok := result.rows.(*identityScriptedRows)
	if !ok {
		t.Fatalf("restore fixture rows have type %T", result.rows)
	}
	return rows
}

// The fixture has valid migration/role/RLS projections, but deliberately empty
// constraint and trigger inventories. Their findings must survive even when
// tenant scans can proceed; usability is not a clean qualification result.
func newRestoreSchemaFixture(t *testing.T) restoreSchemaFixture {
	t.Helper()
	ledger, _ := migrationLedgerFixture(t, 0)
	f := restoreSchemaFixture{tx: ledger.identityScriptedTx, rows: map[string]int{}, queries: map[string]int{}, api: newPrivilegeInspectionFixture(t, RuntimeRoleAPI, false, privilegeTestSchema), worker: newPrivilegeInspectionFixture(t, RuntimeRoleWorker, false, privilegeTestSchema)}
	addRow := func(name string, values ...any) {
		f.rows[name] = len(f.tx.queryRows)
		f.tx.queryRows = append(f.tx.queryRows, scriptedValueRow(values))
	}
	addQuery := func(name string, values [][]any) {
		f.queries[name] = len(f.tx.queryResults)
		f.tx.queryResults = append(f.tx.queryResults, identityQueryResult{rows: &identityScriptedRows{values: values}})
	}
	addRow("version", 180004)
	addRow("crypto", true)
	addRow("schema", privilegeTestSchema, privilegeTestOwner)
	addRow("owner", privilegeTestOwner, privilegeTestOwner)
	for _, table := range catalogTableNames {
		addRow("table/"+table, privilegeTestOwner)
	}
	addQuery("tables", restoreTableRows())
	addRow("functions", len(catalogFunctionNames))
	f.rows["roles"] = len(f.tx.queryRows)
	f.tx.queryRows = append(f.tx.queryRows, privilegeBindingRows()...)
	for _, inspection := range []struct {
		name    string
		fixture privilegeInspectionFixture
	}{{"api", f.api}, {"worker", f.worker}} {
		f.rows[inspection.name], f.queries[inspection.name] = len(f.tx.queryRows), len(f.tx.queryResults)
		f.tx.queryRows = append(f.tx.queryRows, inspection.fixture.tx.queryRows...)
		f.tx.queryResults = append(f.tx.queryResults, inspection.fixture.tx.queryResults...)
	}
	addQuery("rls", restoreRLSRows())
	addQuery("constraints", nil)
	const predicate = `((normalized_entry_digest IS NOT NULL) AND ((search_display_name IS NULL) OR (search_description IS NULL) OR (search_tags IS NULL) OR (search_representative_queries IS NULL) OR (search_publisher IS NULL) OR (search_vector IS NULL) OR (search_vector_version <> 2) OR (search_filter_version <> 1)))`
	addRow("index", true, true, true, false, 2, 2, "observations", "btree", "tenant_id", "id", predicate)
	addQuery("triggers", nil)
	addRow("tenant", true)
	addRow("authorization", true)
	return f
}

func restoreSchemaOptions() RestoreVerificationOptions {
	options := validRestoreOptions()
	options.APIRole, options.WorkerRole = privilegeTestAPI, privilegeTestWorker
	return options
}

func restoreFindingCodes(report RestoreVerificationReport) []string {
	codes := make([]string, 0, len(report.Findings))
	for _, finding := range report.Findings {
		codes = append(codes, finding.Code)
	}
	return codes
}

func TestRestoreSchemaFindingsDistinguishTraversalFromQualification(t *testing.T) {
	for _, mode := range []string{"usable with findings", "ledger failure", "pending migration", "unsupported server", "missing crypto", "wrong owner", "runtime roles", "API privileges", "worker privileges", "RLS", "index", "missing tenant", "missing authorization"} {
		t.Run(mode, func(t *testing.T) {
			f := newRestoreSchemaFixture(t)
			usable := true
			codes := []string{"RESTORE_CONSTRAINT_INVENTORY_INVALID", "RESTORE_GUARD_INVENTORY_INVALID"}
			var before, after string
			switch mode {
			case "ledger failure":
				f.tx.queryRows[0] = scannerFunc(func(...any) error { return errors.New("unreadable ledger") })
				f.tx.queryRows = slices.Delete(f.tx.queryRows, 1, f.rows["version"])
				usable, before = false, "RESTORE_MIGRATION_LEDGER_INVALID"
			case "pending migration":
				f.tx.queryRows[f.rows["version"]-2] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				usable, before = false, "RESTORE_MIGRATION_PENDING"
			case "unsupported server":
				f.tx.queryRows[f.rows["version"]] = scriptedValueRow([]any{170006})
				usable, before = false, "RESTORE_POSTGRES_MAJOR_INVALID"
			case "missing crypto":
				f.tx.queryRows[f.rows["crypto"]] = scriptedValueRow([]any{false})
				usable, before = false, "RESTORE_PGCRYPTO_MISSING"
			case "wrong owner":
				f.tx.queryRows[f.rows["owner"]] = scriptedValueRow([]any{"other", "other"})
				usable, before = false, "RESTORE_SCHEMA_OWNERSHIP_INVALID"
			case "runtime roles":
				f.tx.queryRows = slices.Delete(f.tx.queryRows, f.rows["roles"]+1, f.rows["index"])
				f.tx.queryRows[f.rows["roles"]] = scannerFunc(func(...any) error { return errors.New("unreadable role") })
				f.tx.queryResults = slices.Delete(f.tx.queryResults, f.queries["api"], f.queries["rls"])
				before = "RESTORE_RUNTIME_ROLE_ATTRIBUTES_INVALID"
			case "API privileges":
				f.api.schemaValues[0] = false
				before = "RESTORE_RUNTIME_PRIVILEGES_INVALID"
			case "worker privileges":
				f.worker.schemaValues[0] = false
				before = "RESTORE_RUNTIME_PRIVILEGES_INVALID"
			case "RLS":
				restoreInventoryRows(t, f.tx.queryResults[f.queries["rls"]]).values = nil
				usable, before = false, "RESTORE_RLS_POLICY_INVALID"
			case "index":
				f.tx.queryRows[f.rows["index"]] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				codes = []string{codes[0], "RESTORE_SEARCH_REPAIR_INDEX_INVALID", codes[1]}
			case "missing authorization":
				f.tx.queryRows[f.rows["authorization"]] = scriptedValueRow([]any{false})
				usable, after = false, "RESTORE_AUTHORIZATION_STATE_MISSING"
			case "missing tenant":
				f.tx.queryRows[f.rows["tenant"]] = scriptedValueRow([]any{false})
				f.tx.queryRows = f.tx.queryRows[:f.rows["authorization"]]
				usable, after = false, "RESTORE_TENANT_MISSING"
			}
			if !usable && mode != "missing tenant" && mode != "missing authorization" {
				f.tx.queryRows = f.tx.queryRows[:len(f.tx.queryRows)-2]
			}
			if before != "" {
				codes = append([]string{before}, codes...)
			}
			if after != "" {
				codes = append(codes, after)
			}
			rows := slices.Clone(f.tx.queryResults)
			report := RestoreVerificationReport{}
			got, err := verifyRestoreSchema(t.Context(), f.tx, restoreSchemaOptions(), &report)
			if err != nil || got != usable || report.SchemaVersion != 47 || !slices.Equal(restoreFindingCodes(report), codes) {
				t.Fatalf("usable=%v report=%+v error=%v", got, report, err)
			}
			for _, finding := range report.Findings {
				if finding.Class != "integrity" || finding.Severity != "error" || finding.Disposition == "" {
					t.Fatalf("finding lost classification: %+v", finding)
				}
				domain, recordID := "schema", ""
				switch finding.Code {
				case "RESTORE_MIGRATION_PENDING":
					recordID = "47"
				case "RESTORE_TENANT_MISSING", "RESTORE_AUTHORIZATION_STATE_MISSING":
					domain, recordID = "tenant", restoreTestTenant
				case "RESTORE_RUNTIME_ROLE_ATTRIBUTES_INVALID":
					domain = "database_role"
				case "RESTORE_RUNTIME_PRIVILEGES_INVALID":
					domain, recordID = "database_role", strings.ToLower(strings.TrimSuffix(mode, " privileges"))
				}
				if finding.Domain != domain || finding.RecordID != recordID {
					t.Fatalf("finding binding=%+v, want domain=%q record=%q", finding, domain, recordID)
				}
			}
			if len(f.tx.queryRows) != 0 || len(f.tx.queryResults) != 0 || len(f.tx.statements) != 0 || f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatal("unconsumed schema work or verification changed state")
			}
			for _, result := range rows {
				if !restoreInventoryRows(t, result).closed {
					t.Fatal("schema verification left inventory rows open")
				}
			}
			if usable || mode == "missing tenant" {
				if !reflect.DeepEqual(f.tx.queryRowArguments[len(f.tx.queryRowArguments)-1], []any{restoreTestTenant}) {
					t.Fatal("tenant existence query lost its binding")
				}
			}
		})
	}
}

func TestRestoreSchemaStopsBroadInspectionWhenInventoryIsUnsafe(t *testing.T) {
	for _, mode := range []string{"table ownership", "table inventory", "function bound", "function query"} {
		t.Run(mode, func(t *testing.T) {
			f := newRestoreSchemaFixture(t)
			// Only role-attribute validation may continue after the bounded schema
			// precondition fails. No privilege, RLS, guard or tenant scan is queued.
			f.tx.queryRows = f.tx.queryRows[:f.rows["api"]]
			f.tx.queryResults = f.tx.queryResults[:1]
			switch mode {
			case "table ownership":
				f.tx.queryRows[f.rows["functions"]-1] = scriptedValueRow([]any{"foreign_owner"})
				f.tx.queryRows = slices.Delete(f.tx.queryRows, f.rows["functions"], f.rows["functions"]+1)
				f.tx.queryResults = nil
			case "table inventory":
				restoreInventoryRows(t, f.tx.queryResults[0]).values = nil
				f.tx.queryRows = slices.Delete(f.tx.queryRows, f.rows["functions"], f.rows["functions"]+1)
			case "function bound":
				f.tx.queryRows[f.rows["functions"]] = scriptedValueRow([]any{maxRestoreSchemaFunctions + 1})
			case "function query":
				f.tx.queryRows[f.rows["functions"]] = scannerFunc(func(...any) error { return errors.New("function catalog unavailable") })
			}
			reads, queries := len(f.tx.queryRows), len(f.tx.queryResults)
			report := RestoreVerificationReport{}
			usable, err := verifyRestoreSchema(t.Context(), f.tx, restoreSchemaOptions(), &report)
			if err != nil || usable || !slices.Equal(restoreFindingCodes(report), []string{"RESTORE_SCHEMA_INVENTORY_INVALID"}) || len(f.tx.queryRowStatements) != reads || len(f.tx.queryStatements) != queries || len(f.tx.statements) != 0 {
				t.Fatalf("unsafe inventory: usable=%v findings=%+v error=%v reads=%d queries=%d", usable, report.Findings, err, len(f.tx.queryRowStatements), len(f.tx.queryStatements))
			}
		})
	}
}

func TestRestoreSchemaRequiredReadFailuresAbort(t *testing.T) {
	failure := errors.New("required restore read unavailable")
	for _, stage := range []string{"version", "crypto", "schema", "api", "worker", "tenant"} {
		t.Run(stage, func(t *testing.T) {
			f := newRestoreSchemaFixture(t)
			index := f.rows[stage]
			f.tx.queryRows[index] = scannerFunc(func(...any) error { return failure })
			queries := 0
			switch stage {
			case "api", "worker":
				queries = f.queries[stage]
			case "tenant":
				queries = len(f.tx.queryResults)
			}
			results := slices.Clone(f.tx.queryResults)
			report := RestoreVerificationReport{}
			usable, err := verifyRestoreSchema(t.Context(), f.tx, restoreSchemaOptions(), &report)
			if usable || !errors.Is(err, failure) || len(f.tx.queryRowStatements) != index+1 || len(f.tx.queryStatements) != queries || len(f.tx.statements) != 0 || f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatalf("read failure: usable=%v error=%v reads=%d", usable, err, len(f.tx.queryRowStatements))
			}
			for _, result := range results[:queries] {
				if !restoreInventoryRows(t, result).closed {
					t.Fatal("read failure left earlier inventory rows open")
				}
			}
		})
	}
}
