// Cleanup units exercise Go sequencing and progress with scripted transactions;
// PostgreSQL integration owns eligibility, locking and committed effects.
package database

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
)

func TestHousekeepingMarkAndReclaimTx(t *testing.T) {
	t.Parallel()
	const limit = 4
	const retention = 3*time.Hour + 17*time.Minute
	for _, operation := range []struct {
		name, table, markError, reclaimError string
		run                                  func(context.Context, pgx.Tx, time.Duration, int) (housekeeping.Result, error)
		job, semantic                        bool
	}{
		{"webhook deliveries", "webhook_deliveries", "mark webhook deliveries for cleanup", "reclaim webhook deliveries", reclaimWebhookDeliveriesTx, false, false},
		{"acknowledged outbox", "outbox_events", "mark acknowledged outbox events for cleanup", "reclaim acknowledged outbox events", reclaimAcknowledgedOutboxTx, false, false},
		{"core terminal jobs", "jobs", "mark terminal jobs for cleanup", "reclaim terminal jobs", reclaimTerminalJobsTx, true, false},
		{"semantic terminal jobs", "jobs", "mark terminal jobs for cleanup", "reclaim terminal jobs", reclaimTerminalJobsTx, true, true},
	} {
		for _, mode := range []string{"success", "mark failure", "reclaim failure"} {
			t.Run(operation.name+"/"+mode, func(t *testing.T) {
				failure := &pgconn.PgError{Code: "42501"}
				marked := []any{int64(4), 9.5}
				if operation.job {
					marked = append(marked, operation.semantic)
				}
				rows := []pgx.Row{
					scriptedValueRow(marked),
					scriptedValueRow([]any{int64(2), int64(0), false, 7.0}),
				}
				wantCalls, wantError := 2, ""
				failedRow := scannerFunc(func(destinations ...any) error {
					setScanDestination(t, destinations, 0, int64(99))
					return failure
				})
				switch mode {
				case "mark failure":
					rows[0], wantCalls, wantError = failedRow, 1, operation.markError
				case "reclaim failure":
					rows[1], wantError = failedRow, operation.reclaimError
				}
				tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: rows}
				got, err := operation.run(t.Context(), tx, retention, limit)
				if wantError != "" {
					if got != (housekeeping.Result{}) || !errors.Is(err, failure) || catalog.ErrorCodeOf(err) != catalog.CodeForbidden || !strings.Contains(err.Error(), wantError) {
						t.Fatalf("failed cleanup = %+v, %v; want no progress and %q", got, err, wantError)
					}
				} else if want := (housekeeping.Result{Reclaimed: 2, Marked: 4, Saturated: true, OldestEligibleAge: 9500 * time.Millisecond}); err != nil || got != want {
					t.Fatalf("cleanup = %+v, %v; want %+v", got, err, want)
				}
				wantArguments := [][]any{{limit, retention.String()}, {limit}}
				if !reflect.DeepEqual(tx.queryRowArguments, wantArguments[:wantCalls]) || len(tx.queryRows) != 2-wantCalls {
					t.Fatalf("query arguments = %#v; unread rows = %d", tx.queryRowArguments, len(tx.queryRows))
				}
				if !strings.Contains(tx.queryRowStatements[0], "UPDATE "+operation.table) || wantCalls == 2 && !strings.Contains(tx.queryRowStatements[1], "DELETE FROM "+operation.table) {
					t.Fatal("cleanup did not mark before deleting from the expected table")
				}
				if operation.job && wantCalls == 2 {
					// Both candidate selection and deletion must select the optional
					// guard; this assertion does not interpret its SQL predicate.
					wantGuards := 0
					if operation.semantic {
						wantGuards = 2
					}
					if got := strings.Count(tx.queryRowStatements[1], "FROM semantic_generations generation"); got != wantGuards {
						t.Fatalf("semantic reference guards = %d, want %d", got, wantGuards)
					}
				}
				if tx.commits != 0 || tx.rollbacks != 0 || len(tx.statements) != 0 || len(tx.queryStatements) != 0 {
					t.Fatal("cleanup helper took transaction ownership or performed unrelated work")
				}
			})
		}
	}
}

func TestReclaimObsoleteSourceCoordinationTx(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		limit     int
		values    [][]any
		arguments [][]any
		want      housekeeping.Result
	}{
		{name: "even allowance", limit: 6, arguments: [][]any{{3}, {3}},
			values: [][]any{{int64(2), int64(0), false, 9.0}, {int64(1), int64(0), false, 5.0}},
			want:   housekeeping.Result{Reclaimed: 3, OldestEligibleAge: 9 * time.Second}},
		{name: "odd allowance and maintenance saturation", limit: 5, arguments: [][]any{{2}, {3}},
			values: [][]any{{int64(1), int64(0), false, 3.0}, {int64(3), int64(0), true, 7.0}},
			want:   housekeeping.Result{Reclaimed: 4, Saturated: true, OldestEligibleAge: 7 * time.Second}},
		{name: "retrieval saturation", limit: 6, arguments: [][]any{{3}, {3}},
			values: [][]any{{int64(3), int64(0), true, 9.0}, {int64(1), int64(0), false, 5.0}},
			want:   housekeeping.Result{Reclaimed: 4, Saturated: true, OldestEligibleAge: 9 * time.Second}},
		{name: "empty pages", limit: 6, arguments: [][]any{{3}, {3}},
			values: [][]any{{int64(0), int64(0), false, 0.0}, {int64(0), int64(0), false, 0.0}}},
		{name: "single empty page", limit: 1, arguments: [][]any{{1}},
			values: [][]any{{int64(0), int64(0), false, 0.0}}},
		{name: "single full page", limit: 1, arguments: [][]any{{1}},
			values: [][]any{{int64(1), int64(0), true, 4.0}},
			want:   housekeeping.Result{Reclaimed: 1, Saturated: true, OldestEligibleAge: 4 * time.Second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			for _, values := range test.values {
				tx.queryRows = append(tx.queryRows, scriptedValueRow(values))
			}
			got, err := reclaimObsoleteSourceCoordinationTx(t.Context(), tx, test.limit)
			if err != nil || got != test.want {
				t.Fatalf("coordination cleanup = %+v, %v; want %+v", got, err, test.want)
			}
			if !reflect.DeepEqual(tx.queryRowArguments, test.arguments) || len(tx.queryRows) != 0 {
				t.Fatalf("query arguments = %#v; want %#v; unread rows = %d", tx.queryRowArguments, test.arguments, len(tx.queryRows))
			}
			tables := []string{"source_retrieval_states", "source_maintenance_state"}
			if test.limit == 1 {
				tables = []string{"source_maintenance_state"}
			}
			for index, table := range tables {
				if !strings.Contains(tx.queryRowStatements[index], "DELETE FROM "+table) {
					t.Fatalf("query %d did not target %s", index, table)
				}
			}
			if tx.commits != 0 || tx.rollbacks != 0 || len(tx.statements) != 0 || len(tx.queryStatements) != 0 {
				t.Fatal("coordination helper took transaction ownership or performed unrelated work")
			}
		})
	}
}

func TestReclaimObsoleteSourceCoordinationTxStopsOnFailure(t *testing.T) {
	t.Parallel()
	for index, operation := range []string{"reclaim obsolete source retrieval state", "reclaim obsolete source maintenance state"} {
		t.Run(operation, func(t *testing.T) {
			failure := &pgconn.PgError{Code: "42501"}
			rows := []pgx.Row{
				scriptedValueRow([]any{int64(2), int64(0), false, 9.0}),
				scriptedValueRow([]any{int64(1), int64(0), false, 5.0}),
			}
			rows[index] = scannerFunc(func(destinations ...any) error {
				setScanDestination(t, destinations, 0, int64(99))
				return failure
			})
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: rows}
			got, err := reclaimObsoleteSourceCoordinationTx(t.Context(), tx, 6)
			if got != (housekeeping.Result{}) || !errors.Is(err, failure) || catalog.ErrorCodeOf(err) != catalog.CodeForbidden || !strings.Contains(err.Error(), operation) {
				t.Fatalf("failed cleanup = %+v, %v; want no progress and %q", got, err, operation)
			}
			if len(tx.queryRowStatements) != index+1 || len(tx.queryRows) != 1-index {
				t.Fatalf("cleanup queries = %d; unread rows = %d", len(tx.queryRowStatements), len(tx.queryRows))
			}
		})
	}
}

func TestWithHousekeepingTx(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "begin failure", "statement timeout failure", "lock timeout failure", "operation failure", "commit failure"} {
		t.Run(mode, func(t *testing.T) {
			failure := &pgconn.PgError{Code: "42501"}
			operationFailure := catalog.E(catalog.CodeConflict, "cleanup fixture conflict")
			tx := &fakeCommandTx{}
			connection := &fakeCommandConnection{transactions: []*fakeCommandTx{tx}}
			var wantError error
			wantCode, wantOperation := catalog.CodeForbidden, ""
			wantSetup, wantCalls, wantCommits, wantRollbacks := 2, 1, 0, 1
			switch mode {
			case "success":
				wantCommits = 1
			case "begin failure":
				connection.beginErr, wantError = failure, failure
				wantSetup, wantCalls, wantRollbacks, wantOperation = 0, 0, 0, "begin housekeeping transaction"
			case "statement timeout failure":
				tx.execErrors, wantError = []error{failure}, failure
				wantSetup, wantCalls, wantOperation = 1, 0, "bound housekeeping statement time"
			case "lock timeout failure":
				tx.execErrors, wantError = []error{nil, failure}, failure
				wantCalls, wantOperation = 0, "bound housekeeping lock time"
			case "operation failure":
				wantError, wantCode = operationFailure, catalog.CodeConflict
			case "commit failure":
				tx.commitErr, wantError = failure, failure
				wantCommits, wantOperation = 1, "commit housekeeping transaction"
			}
			setup := []string{"SET LOCAL statement_timeout = '2s'", "SET LOCAL lock_timeout = '250ms'"}
			provisional := housekeeping.Result{Reclaimed: 2, Marked: 3, Saturated: true, OldestEligibleAge: 4 * time.Second}
			calls := 0
			got, err := withHousekeepingTx(t.Context(), connection, func(actual pgx.Tx) (housekeeping.Result, error) {
				calls++
				if actual != tx || !reflect.DeepEqual(tx.statements, setup) || tx.commits != 0 || tx.rollbacks != 0 {
					t.Fatal("cleanup did not receive the open transaction after both timeout settings")
				}
				if mode == "operation failure" {
					return provisional, operationFailure
				}
				return provisional, nil
			})
			if wantError != nil {
				if got != (housekeeping.Result{}) || !errors.Is(err, wantError) || catalog.ErrorCodeOf(err) != wantCode || !strings.Contains(err.Error(), wantOperation) {
					t.Fatalf("failed transaction = %+v, %v; want no progress and %v", got, err, wantError)
				}
			} else if err != nil || got != provisional {
				t.Fatalf("committed transaction = %+v, %v; want %+v", got, err, provisional)
			}
			if calls != wantCalls || tx.commits != wantCommits || tx.rollbacks != wantRollbacks {
				t.Fatalf("operation/commit/rollback calls = %d/%d/%d; want %d/%d/%d", calls, tx.commits, tx.rollbacks, wantCalls, wantCommits, wantRollbacks)
			}
			if !reflect.DeepEqual(connection.options, []pgx.TxOptions{{}}) || len(tx.statements) != wantSetup {
				t.Fatalf("transaction options = %#v; timeout statements = %v", connection.options, tx.statements)
			}
			for index, statement := range tx.statements {
				if statement != setup[index] || len(tx.arguments[index]) != 0 {
					t.Fatalf("timeout %d = %q, %#v", index, statement, tx.arguments[index])
				}
			}
		})
	}
}
