package database

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

// migrationLedgerFixture uses the immutable plan as input for ledger/transaction
// cases. TestPlanMigrationsLeavesAnEmptySchemaUnchanged checks its metadata.
// A negative pending count describes an absent ledger; otherwise only that many
// final migrations are absent from the scripted ledger.
func migrationLedgerFixture(t *testing.T, pending int) (*migrationScriptedDatabase, []Migration) {
	t.Helper()
	absent := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{false})}}
	plan, err := planMigrations(t.Context(), absent)
	if err != nil || len(plan) < 3 {
		t.Fatalf("embedded plan = %v, error = %v", plan, err)
	}
	rows := []pgx.Row{scriptedValueRow([]any{pending >= 0})}
	if pending >= 0 {
		for i := range plan {
			plan[i].Applied = i < len(plan)-pending
			if plan[i].Applied {
				rows = append(rows, scriptedValueRow([]any{plan[i].Checksum}))
			} else {
				rows = append(rows, scannerFunc(func(...any) error { return pgx.ErrNoRows }))
			}
		}
		rows = append(rows, scannerFunc(func(...any) error { return pgx.ErrNoRows }))
	}
	return &migrationScriptedDatabase{identityScriptedTx: &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: rows}}, plan
}

func TestPlanMigrationsReadsBoundLedgerWithoutWrites(t *testing.T) {
	database, want := migrationLedgerFixture(t, 2)
	plan, err := planMigrations(t.Context(), database)
	if err != nil || !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan = %+v, error = %v", plan, err)
	}
	versions := make([]int64, 0, len(want))
	arguments := make([][]any, 0, len(want)+2)
	arguments = append(arguments, nil)
	for _, migration := range want {
		versions = append(versions, migration.Version)
		arguments = append(arguments, []any{migration.Version})
	}
	arguments = append(arguments, []any{versions})
	if !reflect.DeepEqual(database.queryRowArguments, arguments) || len(database.queryRows) != 0 || len(database.statements) != 0 || len(database.queryStatements) != 0 || database.commits != 0 || database.rollbacks != 0 {
		t.Fatalf("planning scope/side effects: reads=%v writes=%v", database.queryRowArguments, database.statements)
	}
}

func TestMigrationReadFailuresPreventApplication(t *testing.T) {
	injected := errors.New("migration ledger unavailable")
	for _, stage := range []string{"table lookup", "stored checksum", "unknown versions"} {
		t.Run(stage, func(t *testing.T) {
			database, _ := migrationLedgerFixture(t, 2)
			index, message := 0, "inspect migration table"
			switch stage {
			case "stored checksum":
				index, message = 1, "read migration state"
			case "unknown versions":
				index, message = len(database.queryRows)-1, "inspect unknown migration versions"
			}
			database.queryRows[index] = scannerFunc(func(...any) error { return injected })
			tx := &fakeCommandTx{}
			database.transactions = []pgx.Tx{tx}
			applied, err := applyPendingMigrations(t.Context(), database)
			if !errors.Is(err, injected) || !strings.Contains(err.Error(), message) || applied != nil {
				t.Fatalf("applied = %+v, error = %v", applied, err)
			}
			if len(database.queryRowStatements) != index+1 || len(database.transactions) != 1 || len(database.statements) != 0 || len(tx.statements) != 0 || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatal("planning failure did not stop before migration transactions")
			}
		})
	}
}

func TestApplyPendingMigrationsCommitsOnlyPendingEntries(t *testing.T) {
	for _, test := range []struct {
		name    string
		pending int
	}{{"fresh schema", -1}, {"pending tail", 2}, {"already current", 0}} {
		t.Run(test.name, func(t *testing.T) {
			database, plan := migrationLedgerFixture(t, test.pending)
			pending := slices.DeleteFunc(slices.Clone(plan), func(m Migration) bool { return m.Applied })
			txs := make([]*fakeCommandTx, 0, len(pending))
			for range pending {
				tx := &fakeCommandTx{}
				txs = append(txs, tx)
				database.transactions = append(database.transactions, tx)
			}
			applied, err := applyPendingMigrations(t.Context(), database)
			if err != nil || len(applied) != len(pending) {
				t.Fatalf("applied = %+v, error = %v", applied, err)
			}
			for i, migration := range pending {
				want := migration
				want.Applied = true
				if applied[i] != want {
					t.Fatalf("migration %d result = %+v, want %+v", i, applied[i], want)
				}
				body, err := migrations.Files.ReadFile(migration.Name)
				if err != nil {
					t.Fatal(err)
				}
				tx := txs[i]
				if !slices.Equal(tx.statements, []string{string(body), "INSERT INTO catalog_schema_migrations(version, checksum) VALUES ($1, $2)"}) || !reflect.DeepEqual(tx.arguments, [][]any{nil, {migration.Version, migration.Checksum}}) || tx.commits != 1 || tx.rollbacks != 0 {
					t.Fatalf("migration %d: writes=%d args=%v commits=%d rollbacks=%d", migration.Version, len(tx.statements), tx.arguments, tx.commits, tx.rollbacks)
				}
			}
			if len(database.queryRows) != 0 || len(database.transactions) != 0 || len(database.statements) != 0 {
				t.Fatal("unconsumed migration work or writes outside a transaction")
			}
		})
	}
}

func TestApplyPendingMigrationsKeepsEarlierCommitsOnLaterFailure(t *testing.T) {
	injected := errors.New("later migration failed")
	for _, stage := range []string{"body", "ledger", "commit"} {
		t.Run(stage, func(t *testing.T) {
			database, plan := migrationLedgerFixture(t, 3)
			first, failed, untouched := &fakeCommandTx{}, &fakeCommandTx{}, &fakeCommandTx{}
			database.transactions = []pgx.Tx{first, failed, untouched}
			writes, commits, rollbacks, message := 2, 0, 1, "record migration"
			switch stage {
			case "body":
				failed.execErrors, writes, message = []error{injected}, 1, "apply migration"
			case "ledger":
				failed.execErrors = []error{nil, injected}
			case "commit":
				failed.commitErr, commits, rollbacks, message = injected, 1, 0, "commit migration"
			}
			applied, err := applyPendingMigrations(t.Context(), database)
			if !errors.Is(err, injected) || !strings.Contains(err.Error(), fmt.Sprintf("%s %d", message, plan[len(plan)-2].Version)) || applied != nil {
				t.Fatalf("applied = %+v, error = %v", applied, err)
			}
			if first.commits != 1 || first.rollbacks != 0 || len(first.statements) != 2 || failed.commits != commits || failed.rollbacks != rollbacks || len(failed.statements) != writes || len(database.transactions) != 1 || len(database.statements) != 0 || len(untouched.statements) != 0 || untouched.commits != 0 || untouched.rollbacks != 0 {
				t.Fatal("later failure changed an earlier commit or attempted a subsequent migration")
			}
		})
	}
}

func TestMigrationApplyPreflightAvoidsPoolAccess(t *testing.T) {
	// An inert pool cannot supply a connection. Invalid command inputs must be
	// rejected before pool acquisition; nil-pool guards have existing tests.
	pool := &pgxpool.Pool{}
	for _, test := range []struct {
		name, api, worker, message string
		semantic                   []bool
	}{
		{"multiple semantic selections", "api", "worker", "at most one semantic setup selection", []bool{true, false}},
		{"shared runtime identity", "same", "same", "distinct PostgreSQL API and worker role names", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			applied, reports, err := ApplyAndReconcileRuntimePrivileges(t.Context(), pool, test.api, test.worker, test.semantic...)
			if err == nil || !strings.Contains(err.Error(), test.message) || applied != nil || reports != nil {
				t.Fatalf("applied=%v reports=%v error=%v", applied, reports, err)
			}
		})
	}
}
