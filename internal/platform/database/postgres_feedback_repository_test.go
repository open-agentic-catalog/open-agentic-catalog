// Scripted repository calls check bindings and failure handling, not SQL enforcement.
package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
)

// feedbackRepositoryRecord supplies immutable retained context for one isolated feedback operation.
func feedbackRepositoryRecord() feedback.Record {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	return feedback.Record{ID: "00000000-0000-7000-8000-000000000021", TenantID: restoreTestTenant,
		MembershipID: "00000000-0000-7000-8000-000000000022", ResourceID: "00000000-0000-7000-8000-000000000023",
		ReleaseID: "00000000-0000-7000-8000-000000000024", VersionRevisionID: "00000000-0000-7000-8000-000000000025",
		Category: "documentation", Comment: "Clarify configuration.", State: "open", Revision: 1, CreatedAt: now, UpdatedAt: now}
}

// feedbackRepositoryValues projects a record in the adapter's selected column order.
func feedbackRepositoryValues(r feedback.Record) []any {
	return []any{r.ID, r.TenantID, r.MembershipID, r.ResourceID, r.ReleaseID, r.VersionRevisionID,
		r.Category, r.Comment, r.Response, r.State, r.Revision, r.CreatedAt, r.UpdatedAt}
}

// TestFeedbackRepositoryConstructionAndGet checks repository ownership and tenant-bound reads without a pool connection.
func TestFeedbackRepositoryConstructionAndGet(t *testing.T) {
	if store, err := NewPostgresFeedbackStore(nil); err == nil || store != nil {
		t.Fatalf("nil repository accepted: %v, %v", store, err)
	}
	pool := &pgxpool.Pool{}
	repository := &PostgresRepository{pool: pool}
	store, err := NewPostgresFeedbackStore(repository)
	if err != nil || store.repository != repository {
		t.Fatalf("repository ownership: %v", err)
	}
	want := feedbackRepositoryRecord()
	failure := errors.New("feedback read failure")
	for _, test := range []struct {
		name string
		row  pgx.Row
		code catalog.ErrorCode
	}{
		{"record", scriptedValueRow(feedbackRepositoryValues(want)), ""},
		{"missing", scannerFunc(func(...any) error { return pgx.ErrNoRows }), catalog.CodeNotFound},
		{"query failure", scannerFunc(func(...any) error { return failure }), catalog.CodeDependencyUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{test.row}}
			got, err := store.Get(identityRepositoryContext(pool, tx), want.TenantID, want.ID)
			if test.code == "" && err != nil || test.code != "" && (err == nil || catalog.ErrorCodeOf(err) != test.code) {
				t.Fatalf("Get error: %v", err)
			}
			if err == nil && got != want || err != nil && got != (feedback.Record{}) {
				t.Fatalf("Get = %+v, %v", got, err)
			}
			if test.code == catalog.CodeDependencyUnavailable && !errors.Is(err, failure) {
				t.Fatalf("lost cause: %v", err)
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{want.TenantID, want.ID}}) || !strings.Contains(tx.queryRowStatements[0], "tenant_id=$1 AND id=$2") {
				t.Fatalf("unbound read: %v %v", tx.queryRowStatements, tx.queryRowArguments)
			}
		})
	}
}

// TestFeedbackRepositoryList checks bounded selection arguments, decoding and row cleanup on errors.
func TestFeedbackRepositoryList(t *testing.T) {
	pool := &pgxpool.Pool{}
	store := &PostgresFeedbackStore{repository: &PostgresRepository{pool: pool}}
	want := feedbackRepositoryRecord()
	failure := errors.New("feedback page failure")
	for _, test := range []struct {
		name     string
		rows     *identityScriptedRows
		queryErr error
		code     catalog.ErrorCode
	}{
		{"bounded page", &identityScriptedRows{values: [][]any{feedbackRepositoryValues(want)}}, nil, ""},
		{"empty page", &identityScriptedRows{}, nil, ""},
		{"query error", nil, failure, catalog.CodeDependencyUnavailable},
		{"scan error", &identityScriptedRows{values: [][]any{{"incomplete"}}}, nil, catalog.CodeDependencyUnavailable},
		{"iteration error", &identityScriptedRows{values: [][]any{feedbackRepositoryValues(want)}, err: failure}, nil, catalog.CodeDependencyUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: test.rows, err: test.queryErr}}}
			q := feedback.ListInput{MembershipID: want.MembershipID, ResourceID: want.ResourceID, Category: want.Category, State: want.State, AfterID: "00000000-0000-7000-8000-000000000020", Limit: 101}
			got, err := store.List(identityRepositoryContext(pool, tx), want.TenantID, q)
			if test.code == "" && err != nil || test.code != "" && (err == nil || catalog.ErrorCodeOf(err) != test.code) {
				t.Fatalf("List: %v", err)
			}
			if err == nil && (got == nil || len(got) != len(test.rows.values)) || err != nil && got != nil {
				t.Fatalf("List result: %+v, %v", got, err)
			}
			if err == nil && len(got) > 0 && got[0] != want {
				t.Fatalf("page = %+v", got)
			}
			if (test.queryErr != nil || test.rows != nil && test.rows.err != nil) && !errors.Is(err, failure) {
				t.Fatalf("lost cause: %v", err)
			}
			if test.rows != nil && !test.rows.closed {
				t.Fatal("rows not closed")
			}
			if !reflect.DeepEqual(tx.queryArguments, [][]any{{want.TenantID, q.MembershipID, q.ResourceID, q.Category, q.State, q.AfterID, q.Limit}}) {
				t.Fatalf("arguments = %v", tx.queryArguments)
			}
			for _, predicate := range []string{"tenant_id=$1", "resources.tenant_id=item_feedback.tenant_id", "resources.deleted_at IS NULL", "ORDER BY id LIMIT $7"} {
				if !strings.Contains(tx.queryStatements[0], predicate) {
					t.Fatalf("missing selection boundary %s", predicate)
				}
			}
		})
	}
	for _, limit := range []int{-1, 0, 102} {
		tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
		got, err := store.List(identityRepositoryContext(pool, tx), want.TenantID, feedback.ListInput{Limit: limit})
		if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || got != nil || len(tx.queryStatements) != 0 {
			t.Fatalf("invalid limit %d: %+v, %v", limit, got, err)
		}
	}
}

// TestFeedbackRepositorySave checks validated creation and updates append audit in the existing command transaction.
func TestFeedbackRepositorySave(t *testing.T) {
	pool := &pgxpool.Pool{}
	store := &PostgresFeedbackStore{repository: &PostgresRepository{pool: pool}}
	for _, action := range []string{"feedback.created", "feedback.edit", "feedback.address", "feedback.remove"} {
		t.Run(action, func(t *testing.T) {
			old := feedbackRepositoryRecord()
			next := old
			expected := int64(0)
			if action != "feedback.created" {
				expected = old.Revision
				next.Revision++
				next.UpdatedAt = old.UpdatedAt.Add(time.Hour)
			}
			switch action {
			case "feedback.edit":
				next.Comment = "Clarify the example."
			case "feedback.address":
				next.State, next.Response = "addressed", "Example added."
			case "feedback.remove":
				next.State, next.Comment = "removed", ""
			}
			evidence := currentTestAuditFacts(t, action, "feedback", next.ID, next.UpdatedAt, map[string]any{"revision": next.Revision, "state": next.State})
			predecessor := old
			if expected == 0 {
				predecessor = feedback.Record{}
			}
			if err := feedback.ValidateSave(predecessor, next, expected, evidence); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			if expected > 0 {
				tx.queryRows = []pgx.Row{scriptedValueRow(feedbackRepositoryValues(old))}
			}
			got, err := store.Save(identityRepositoryContext(pool, tx), next, expected, evidence)
			if err != nil || got != next || len(tx.statements) != 2 || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatalf("Save = %+v, %v; statements=%v", got, err, tx.statements)
			}
			wantArgs := feedbackRepositoryValues(next)
			if expected > 0 {
				wantArgs = []any{next.TenantID, next.ID, next.Category, next.Comment, next.Response, next.State, next.Revision, next.UpdatedAt}
				if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{next.TenantID, next.ID}}) || !strings.Contains(tx.queryRowStatements[0], "FOR UPDATE") {
					t.Fatal("predecessor not tenant bound and locked")
				}
				if !strings.Contains(tx.statements[0], "WHERE tenant_id=$1 AND id=$2") {
					t.Fatal("update missing tenant predicate")
				}
			} else if len(tx.queryRowStatements) != 0 || !strings.Contains(tx.statements[0], "INSERT INTO item_feedback") {
				t.Fatal("creation read a predecessor or did not insert")
			}
			if !reflect.DeepEqual(tx.arguments[0], wantArgs) {
				t.Fatalf("write args = %v; want %v", tx.arguments[0], wantArgs)
			}
			if !strings.Contains(tx.statements[1], "INSERT INTO audit_events") || tx.arguments[1][2] != next.TenantID || tx.arguments[1][4] != action || tx.arguments[1][6] != next.ID {
				t.Fatalf("unbound audit: %v", tx.arguments[1])
			}
		})
	}
}

// TestFeedbackRepositorySaveFailures checks locked predecessor validation and write errors abort the command before further effects.
func TestFeedbackRepositorySaveFailures(t *testing.T) {
	pool := &pgxpool.Pool{}
	store := &PostgresFeedbackStore{repository: &PostgresRepository{pool: pool}}
	failure := errors.New("feedback write failure")
	for _, test := range []struct {
		name       string
		expected   int64
		readErr    error
		execErrors []error
		code       catalog.ErrorCode
		writes     int
	}{
		{"missing predecessor", 1, pgx.ErrNoRows, nil, catalog.CodeNotFound, 0},
		{"predecessor read failure", 1, failure, nil, catalog.CodeDependencyUnavailable, 0},
		{"stale revision", 2, nil, nil, catalog.CodePreconditionFailed, 0},
		{"invalid evidence", 0, nil, nil, catalog.CodeInternal, 0},
		{"insert failure", 0, nil, []error{failure}, catalog.CodeDependencyUnavailable, 1},
		{"update failure", 1, nil, []error{failure}, catalog.CodeDependencyUnavailable, 1},
		{"audit failure", 1, nil, []error{nil, failure}, catalog.CodeDependencyUnavailable, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			old := feedbackRepositoryRecord()
			next := old
			action := "feedback.created"
			if test.expected > 0 {
				next.Revision = test.expected + 1
				next.UpdatedAt = old.UpdatedAt.Add(time.Hour)
				action = "feedback.edit"
			}
			evidence := currentTestAuditFacts(t, action, "feedback", next.ID, next.UpdatedAt, map[string]any{"revision": next.Revision, "state": next.State})
			if test.name == "invalid evidence" {
				evidence.TargetID = "00000000-0000-7000-8000-000000000026"
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{execErrors: test.execErrors}}
			if test.expected > 0 {
				row := scriptedValueRow(feedbackRepositoryValues(old))
				if test.readErr != nil {
					row = scannerFunc(func(...any) error { return test.readErr })
				}
				tx.queryRows = []pgx.Row{row}
			}
			got, err := store.Save(identityRepositoryContext(pool, tx), next, test.expected, evidence)
			if (test.code == "" && err != nil || test.code != "" && (err == nil || catalog.ErrorCodeOf(err) != test.code)) || got != (feedback.Record{}) || len(tx.statements) != test.writes || tx.commits != 0 || tx.rollbacks != 1 {
				t.Fatalf("Save=%+v err=%v writes=%d rollback=%d", got, err, len(tx.statements), tx.rollbacks)
			}
			if test.code == catalog.CodeDependencyUnavailable && !errors.Is(err, failure) {
				t.Fatalf("lost cause: %v", err)
			}
		})
	}
}

// TestFeedbackRepositoryTransactionOptions checks standalone read intent and deferred command commit ownership for writes.
func TestFeedbackRepositoryTransactionOptions(t *testing.T) {
	pool := &pgxpool.Pool{}
	store := &PostgresFeedbackStore{repository: &PostgresRepository{pool: pool}}
	record := feedbackRepositoryRecord()
	evidence := currentTestAuditFacts(t, "feedback.created", "feedback", record.ID, record.UpdatedAt, map[string]any{"revision": record.Revision, "state": record.State})
	for _, operation := range []string{"get", "list", "save"} {
		t.Run(operation, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			connection := &captureCommandConnection{transaction: tx}
			command := &commandTransaction{pool: pool, tenantID: record.TenantID, conn: connection}
			ctx := withCommandTransaction(t.Context(), command)
			options := pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}
			var err error
			switch operation {
			case "get":
				tx.queryRows = []pgx.Row{scriptedValueRow(feedbackRepositoryValues(record))}
				_, err = store.Get(ctx, record.TenantID, record.ID)
			case "list":
				tx.queryResults = []identityQueryResult{{rows: &identityScriptedRows{}}}
				_, err = store.List(ctx, record.TenantID, feedback.ListInput{Limit: 1})
			case "save":
				options = pgx.TxOptions{}
				_, err = store.Save(ctx, record, 0, evidence)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(connection.options, []pgx.TxOptions{options}) || !reflect.DeepEqual(tx.arguments[0], []any{record.TenantID, "", ""}) {
				t.Fatalf("options=%v context=%v", connection.options, tx.arguments[0])
			}
			commits := 1
			if operation == "save" {
				commits = 0
			}
			if tx.commits != commits || tx.rollbacks != 0 {
				t.Fatalf("transaction ownership: commits=%d rollbacks=%d", tx.commits, tx.rollbacks)
			}
		})
	}
}
