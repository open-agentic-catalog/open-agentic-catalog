package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type fakeCommandConnection struct {
	transactions []*fakeCommandTx
	beginErr     error
	options      []pgx.TxOptions
	deadlines    []time.Time
}

func (connection *fakeCommandConnection) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	connection.options = append(connection.options, options)
	deadline, _ := ctx.Deadline()
	connection.deadlines = append(connection.deadlines, deadline)
	if connection.beginErr != nil {
		return nil, connection.beginErr
	}
	if len(connection.transactions) == 0 {
		return nil, errors.New("unexpected transaction")
	}
	transaction := connection.transactions[0]
	connection.transactions = connection.transactions[1:]
	return transaction, nil
}

type fakeCommandTx struct {
	pgx.Tx
	execErrors       []error
	execTags         []pgconn.CommandTag
	statements       []string
	arguments        [][]any
	commitErr        error
	rollbackErr      error
	commits          int
	rollbacks        int
	rollbackCtx      error
	commitCtx        error
	commitDeadline   time.Time
	rollbackDeadline time.Time
}

func (transaction *fakeCommandTx) Exec(_ context.Context, statement string, args ...any) (pgconn.CommandTag, error) {
	transaction.statements = append(transaction.statements, statement)
	transaction.arguments = append(transaction.arguments, args)
	var err error
	if len(transaction.execErrors) > 0 {
		err = transaction.execErrors[0]
		transaction.execErrors = transaction.execErrors[1:]
	}
	tag := pgconn.NewCommandTag("INSERT 0 1")
	if len(transaction.execTags) > 0 {
		tag = transaction.execTags[0]
		transaction.execTags = transaction.execTags[1:]
	}
	return tag, err
}

func (transaction *fakeCommandTx) Commit(ctx context.Context) error {
	transaction.commits++
	transaction.commitCtx = ctx.Err()
	transaction.commitDeadline, _ = ctx.Deadline()
	return transaction.commitErr
}

func (transaction *fakeCommandTx) Rollback(ctx context.Context) error {
	transaction.rollbacks++
	transaction.rollbackCtx = ctx.Err()
	transaction.rollbackDeadline, _ = ctx.Deadline()
	return transaction.rollbackErr
}

func TestCommandTransactionContextAndBoundaryValidation(t *testing.T) {
	t.Parallel()
	pool := &pgxpool.Pool{}
	command := &commandTransaction{pool: pool, tenantID: "tenant-a"}
	if commandTransactionFrom(context.Background()) != nil {
		t.Fatal("empty context contained a command transaction")
	}
	if got := commandTransactionFrom(withCommandTransaction(context.Background(), command)); got != command {
		t.Fatalf("context command = %p, want %p", got, command)
	}
	if err := command.validate(pool, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	uuidCommand := &commandTransaction{pool: pool, tenantID: "ABCDEFAB-CDEF-7ABC-8DEF-ABCDEFABCDEF"}
	if err := uuidCommand.validate(pool, "abcdefab-cdef-7abc-8def-abcdefabcdef"); err != nil {
		t.Fatalf("equivalent UUID tenant casing was rejected: %v", err)
	}
	if err := command.validate(&pgxpool.Pool{}, "tenant-a"); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("cross-pool validation = %v", err)
	}
	if err := command.validate(pool, "tenant-b"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("cross-tenant validation = %v", err)
	}
}

func TestCommandTransactionKeepsWritesAndCompletionAtomic(t *testing.T) {
	t.Parallel()
	transaction := &fakeCommandTx{}
	connection := &fakeCommandConnection{transactions: []*fakeCommandTx{transaction}}
	command := &commandTransaction{conn: connection, tenantID: "tenant-a"}
	options := pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite}
	operations := 0
	if err := command.run(context.Background(), options, func(pgx.Tx) error {
		operations++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := command.run(context.Background(), pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(pgx.Tx) error {
		operations++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(connection.options) != 1 || operations != 2 {
		t.Fatalf("begins = %d, operations = %d", len(connection.options), operations)
	}
	if len(transaction.statements) != 1 || !strings.Contains(transaction.statements[0], "set_config") {
		t.Fatalf("tenant setup statements = %#v", transaction.statements)
	}
	completed := false
	if err := command.complete(context.Background(), 200, func(context.Context, pgx.Tx) error {
		completed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !completed || transaction.commits != 1 || transaction.rollbacks != 0 || command.tx != nil {
		t.Fatalf("completion state: completed=%t commits=%d rollbacks=%d tx=%v", completed, transaction.commits, transaction.rollbacks, command.tx)
	}
	lateOperation := false
	if err := command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error {
		lateOperation = true
		return nil
	}); catalog.ErrorCodeOf(err) != catalog.CodeInternal || lateOperation || len(connection.options) != 1 {
		t.Fatalf("late command operation error=%v ran=%t begins=%d", err, lateOperation, len(connection.options))
	}
	if err := command.complete(context.Background(), 500, func(context.Context, pgx.Tx) error { return nil }); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("second completion error = %v", err)
	}
}

func TestCommandTransactionReadPreparationUsesShortTransaction(t *testing.T) {
	t.Parallel()
	transaction := &fakeCommandTx{}
	connection := &fakeCommandConnection{transactions: []*fakeCommandTx{transaction}}
	command := &commandTransaction{conn: connection, tenantID: "tenant-a"}
	if err := command.run(context.Background(), pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if transaction.commits != 1 || transaction.rollbacks != 0 || command.tx != nil {
		t.Fatalf("short transaction commits=%d rollbacks=%d retained=%v", transaction.commits, transaction.rollbacks, command.tx)
	}
	if len(transaction.statements) != 1 || !strings.Contains(transaction.statements[0], "set_config") {
		t.Fatalf("read preparation statements = %#v", transaction.statements)
	}
}

func TestCommandTransactionRejectsSuccessAfterRollback(t *testing.T) {
	t.Parallel()
	for _, status := range []int{200, 201, 204, 302, 304} {
		command := &commandTransaction{failed: errors.New("private database failure")}
		if err := command.complete(t.Context(), status, func(context.Context, pgx.Tx) error { t.Fatal("retained a rolled-back success"); return nil }); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
			t.Fatalf("rolled-back success status %d: %v", status, err)
		}
	}
	for _, status := range []int{400, 404, 409, 500, 503} {
		transaction := &fakeCommandTx{}
		command := &commandTransaction{failed: errors.New("private database failure"), conn: &fakeCommandConnection{transactions: []*fakeCommandTx{transaction}}}
		if err := command.complete(t.Context(), status, func(context.Context, pgx.Tx) error { return nil }); err != nil {
			t.Fatalf("failure status %d: %v", status, err)
		}
	}
	transaction := &fakeCommandTx{}
	command := &commandTransaction{conn: &fakeCommandConnection{transactions: []*fakeCommandTx{transaction}}}
	if err := command.complete(t.Context(), 201, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// Retaining an error response uses a fresh transaction, but must still fail
// closed at every database boundary and must never reopen a completed command.
func TestCommandFailureRetentionRefusesDatabaseFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"begin", "tenant", "operation", "commit"} {
		t.Run(stage, func(t *testing.T) {
			injected := errors.New("private retention failure")
			tx := &fakeCommandTx{}
			conn := &fakeCommandConnection{transactions: []*fakeCommandTx{tx}}
			switch stage {
			case "begin":
				conn.beginErr = injected
			case "tenant":
				tx.execErrors = []error{injected}
			case "commit":
				tx.commitErr = injected
			}
			command := &commandTransaction{conn: conn, tenantID: "tenant-a", failed: errors.New("original mutation failure")}
			parent, cancel := context.WithCancel(t.Context())
			cancel()
			called := false
			err := command.complete(parent, 503, func(ctx context.Context, actual pgx.Tx) error {
				called = true
				if ctx.Err() != nil || actual != tx {
					t.Fatal("retention lost its cancellation-independent transaction")
				}
				if stage == "operation" {
					return injected
				}
				return nil
			})
			if !errors.Is(err, injected) || called != (stage == "operation" || stage == "commit") {
				t.Fatalf("error=%v callback=%t", err, called)
			}
			wantRollback, wantCommit := 1, 0
			if stage == "begin" {
				wantRollback = 0
			}
			if stage == "commit" {
				wantCommit = 1
			}
			if tx.rollbacks != wantRollback || tx.commits != wantCommit || !command.closed || command.tx != nil {
				t.Fatalf("rollback=%d commit=%d closed=%t retained=%v", tx.rollbacks, tx.commits, command.closed, command.tx)
			}
			if wantRollback != 0 && (tx.rollbackCtx != nil || tx.rollbackDeadline.IsZero() || !tx.rollbackDeadline.Equal(conn.deadlines[0])) {
				t.Fatal("retention cleanup did not preserve its bounded independent context")
			}
			if err := command.complete(parent, 503, func(context.Context, pgx.Tx) error {
				t.Fatal("repeated completion ran")
				return nil
			}); catalog.ErrorCodeOf(err) != catalog.CodeInternal || len(conn.options) != 1 {
				t.Fatal("failed retention reopened the command")
			}
		})
	}
}

func TestCommandTenantContextPreservesAttributionAndRejectsCrossTenant(t *testing.T) {
	t.Parallel()
	const tenant = "abcdefab-cdef-7abc-8def-abcdefabcdef"
	for _, mode := range []string{"anonymous", "same tenant", "other tenant"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			if mode != "anonymous" {
				actorTenant := strings.ToUpper(tenant)
				if mode == "other tenant" {
					actorTenant = "00000000-0000-7000-8000-000000000001"
				}
				ctx = identity.WithActor(ctx, identity.Actor{TenantID: actorTenant, Subject: "owned-subject", MembershipID: "owned-membership"})
			}
			tx := &fakeCommandTx{}
			err := setTenantContext(ctx, tx, tenant)
			if mode == "other tenant" {
				if catalog.ErrorCodeOf(err) != catalog.CodeForbidden || len(tx.statements) != 0 {
					t.Fatal("cross-tenant actor reached SQL")
				}
				return
			}
			if err != nil || len(tx.arguments) != 1 || len(tx.arguments[0]) != 3 {
				t.Fatalf("tenant context arguments=%v error=%v", tx.arguments, err)
			}
			wantSubject, wantMembership := "", ""
			if mode == "same tenant" {
				wantSubject, wantMembership = "owned-subject", "owned-membership"
			}
			args := tx.arguments[0]
			if args[0] != tenant || args[1] != wantSubject || args[2] != wantMembership {
				t.Fatal("tenant or actor attribution was not explicitly bound")
			}
		})
	}
}

func TestCommandTransactionFailureRollsBackBeforeRetainingFailureResponse(t *testing.T) {
	t.Parallel()
	mutation := &fakeCommandTx{}
	retention := &fakeCommandTx{}
	connection := &fakeCommandConnection{transactions: []*fakeCommandTx{mutation, retention}}
	command := &commandTransaction{conn: connection, tenantID: "tenant-a"}
	operationErr := errors.New("mutation failed")
	if err := command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return operationErr }); !errors.Is(err, operationErr) {
		t.Fatalf("mutation error = %v", err)
	}
	if mutation.rollbacks != 1 || command.tx != nil {
		t.Fatalf("failed mutation rollbacks=%d retained=%v", mutation.rollbacks, command.tx)
	}
	if err := command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return nil }); !errors.Is(err, operationErr) {
		t.Fatalf("later operation error = %v", err)
	}
	retained := false
	if err := command.complete(context.Background(), 500, func(context.Context, pgx.Tx) error {
		retained = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !retained || retention.commits != 1 {
		t.Fatalf("failure response retained=%t commits=%d", retained, retention.commits)
	}
}

func TestCommandTransactionRejectsIncompatibleOptionsAndRecoversPanics(t *testing.T) {
	t.Parallel()
	t.Run("incompatible options", func(t *testing.T) {
		transaction := &fakeCommandTx{}
		command := &commandTransaction{conn: &fakeCommandConnection{transactions: []*fakeCommandTx{transaction}}, tenantID: "tenant-a"}
		if err := command.run(context.Background(), pgx.TxOptions{IsoLevel: pgx.Serializable}, func(pgx.Tx) error { return nil }); err != nil {
			t.Fatal(err)
		}
		err := command.run(context.Background(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(pgx.Tx) error { return nil })
		if catalog.ErrorCodeOf(err) != catalog.CodeInternal || transaction.rollbacks != 1 {
			t.Fatalf("incompatible options error=%v rollbacks=%d", err, transaction.rollbacks)
		}
	})
	t.Run("panic", func(t *testing.T) {
		transaction := &fakeCommandTx{}
		command := &commandTransaction{conn: &fakeCommandConnection{transactions: []*fakeCommandTx{transaction}}, tenantID: "tenant-a"}
		defer func() {
			if recovered := recover(); recovered != "test panic" {
				t.Fatalf("recovered = %#v", recovered)
			}
			if transaction.rollbacks != 1 || catalog.ErrorCodeOf(command.failed) != catalog.CodeInternal {
				t.Fatalf("panic cleanup rollbacks=%d error=%v", transaction.rollbacks, command.failed)
			}
		}()
		_ = command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { panic("test panic") })
	})
}

func TestCommandTransactionCoversDependencyAndCompletionFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*commandTransaction) error
		tx   *fakeCommandTx
		conn *fakeCommandConnection
	}{
		{
			name: "begin",
			conn: &fakeCommandConnection{beginErr: errors.New("begin failed")},
			run: func(command *commandTransaction) error {
				return command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return nil })
			},
		},
		{
			name: "tenant context",
			tx:   &fakeCommandTx{execErrors: []error{errors.New("context failed")}},
			run: func(command *commandTransaction) error {
				return command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return nil })
			},
		},
		{
			name: "short begin",
			conn: &fakeCommandConnection{beginErr: errors.New("begin failed")},
			run: func(command *commandTransaction) error {
				return command.run(context.Background(), pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(pgx.Tx) error {
					t.Fatal("read ran after begin failure")
					return nil
				})
			},
		},
		{
			name: "short tenant context",
			tx:   &fakeCommandTx{execErrors: []error{errors.New("context failed")}},
			run: func(command *commandTransaction) error {
				return command.run(context.Background(), pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(pgx.Tx) error {
					t.Fatal("read ran without tenant context")
					return nil
				})
			},
		},
		{
			name: "short operation",
			tx:   &fakeCommandTx{},
			run: func(command *commandTransaction) error {
				return command.runShort(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return errors.New("operation failed") })
			},
		},
		{
			name: "short commit",
			tx:   &fakeCommandTx{commitErr: errors.New("commit failed")},
			run: func(command *commandTransaction) error {
				return command.runShort(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return nil })
			},
		},
		{
			name: "completion operation",
			tx:   &fakeCommandTx{},
			run: func(command *commandTransaction) error {
				if err := command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return nil }); err != nil {
					return err
				}
				return command.complete(context.Background(), 500, func(context.Context, pgx.Tx) error { return errors.New("completion failed") })
			},
		},
		{
			name: "completion commit",
			tx:   &fakeCommandTx{commitErr: errors.New("commit failed")},
			run: func(command *commandTransaction) error {
				if err := command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return nil }); err != nil {
					return err
				}
				return command.complete(context.Background(), 500, func(context.Context, pgx.Tx) error { return nil })
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.conn == nil {
				test.conn = &fakeCommandConnection{transactions: []*fakeCommandTx{test.tx}}
			}
			command := &commandTransaction{conn: test.conn, tenantID: "tenant-a"}
			if err := test.run(command); err == nil {
				t.Fatal("failure path returned nil")
			}
		})
	}
}

func TestCommandAbortAndCleanupIgnoreCanceledParent(t *testing.T) {
	t.Parallel()
	transaction := &fakeCommandTx{}
	command := &commandTransaction{conn: &fakeCommandConnection{transactions: []*fakeCommandTx{transaction}}, tenantID: "tenant-a"}
	if err := command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	command.abort(parent, nil)
	if transaction.rollbacks != 1 || transaction.rollbackCtx != nil || catalog.ErrorCodeOf(command.failed) != catalog.CodeInternal {
		t.Fatalf("abort cleanup rollbacks=%d context=%v error=%v", transaction.rollbacks, transaction.rollbackCtx, command.failed)
	}

	cleanupTransaction := &fakeCommandTx{}
	rollbackForCleanup(parent, cleanupTransaction)()
	if cleanupTransaction.rollbacks != 1 || cleanupTransaction.rollbackCtx != nil {
		t.Fatalf("deferred cleanup rollbacks=%d context=%v", cleanupTransaction.rollbacks, cleanupTransaction.rollbackCtx)
	}
}

func TestCommandCompletionUsesOneCancellationIndependentCleanupDeadline(t *testing.T) {
	t.Parallel()
	transaction := &fakeCommandTx{}
	command := &commandTransaction{conn: &fakeCommandConnection{transactions: []*fakeCommandTx{transaction}}, tenantID: "tenant-a"}
	if err := command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	var completionDeadline time.Time
	var completionContextErr error
	started := time.Now()
	if err := command.complete(parent, 200, func(ctx context.Context, _ pgx.Tx) error {
		completionDeadline, _ = ctx.Deadline()
		completionContextErr = ctx.Err()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if completionDeadline.IsZero() || completionDeadline.Before(started.Add(4*time.Second)) || completionDeadline.After(started.Add(commandTransactionCleanupLimit+time.Second)) {
		t.Fatalf("cleanup deadline = %v, started = %v", completionDeadline, started)
	}
	laterContext, cancelLater := command.cleanupContext(context.Background())
	laterDeadline, ok := laterContext.Deadline()
	cancelLater()
	if !ok || !completionDeadline.Equal(transaction.commitDeadline) || !completionDeadline.Equal(laterDeadline) {
		t.Fatal("completion operation, commit, and later cleanup did not share one absolute deadline")
	}
	if completionContextErr != nil || transaction.commitCtx != nil {
		t.Fatalf("cleanup inherited caller cancellation: operation=%v commit=%v", completionContextErr, transaction.commitCtx)
	}
}

func TestCommandFailureReusesRollbackDeadlineForFailureRetention(t *testing.T) {
	t.Parallel()
	mutation := &fakeCommandTx{}
	retention := &fakeCommandTx{}
	connection := &fakeCommandConnection{transactions: []*fakeCommandTx{mutation, retention}}
	command := &commandTransaction{conn: connection, tenantID: "tenant-a"}
	operationErr := errors.New("mutation failed")
	if err := command.run(context.Background(), pgx.TxOptions{}, func(pgx.Tx) error { return operationErr }); !errors.Is(err, operationErr) {
		t.Fatalf("mutation error = %v", err)
	}
	if err := command.complete(context.Background(), 500, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(connection.deadlines) != 2 {
		t.Fatalf("transaction contexts = %d", len(connection.deadlines))
	}
	if mutation.rollbackDeadline.IsZero() || !mutation.rollbackDeadline.Equal(connection.deadlines[1]) || !mutation.rollbackDeadline.Equal(retention.commitDeadline) {
		t.Fatal("rollback, failure-response transaction, and commit did not share one cleanup deadline")
	}
}
