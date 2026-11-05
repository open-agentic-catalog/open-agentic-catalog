// Execution handoff units use scripted transactions after lock acquisition.
// Real exclusion, SQL ownership predicates and atomic persistence stay in integration tests.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

const (
	executionTestReservationID = "00000000-0000-7000-8000-000000000031"
	executionTestToken         = "00000000-0000-7000-8000-000000000042"
)

type atomicExecutionFixture struct {
	scope       idempotency.Scope
	expires     time.Time
	response    idempotency.Response
	command     *commandTransaction
	connection  *captureCommandConnection
	reservation *identityScriptedTx
	completion  *fakeCommandTx
}

func newAtomicExecutionFixture() atomicExecutionFixture {
	scope := idempotency.Scope{TenantID: restoreTestTenant, Actor: "actor-a", Route: "POST /api/v1/resources", Key: "execution-key", AuthorizationDigest: "sha256:" + strings.Repeat("a", 64)}
	reservation := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{executionTestReservationID, executionTestToken})}}
	completion := &fakeCommandTx{}
	connection := &captureCommandConnection{transactions: []pgx.Tx{reservation, completion}}
	return atomicExecutionFixture{
		scope: scope, expires: time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC),
		response: idempotency.Response{Digest: "sha256:" + strings.Repeat("b", 64), Status: 201,
			Headers: map[string][]string{"ETag": {`"rev-2"`}, "Link": {"</first>; rel=first", "</next>; rel=next"}}, Body: []byte("{\"id\":\"retained\"}\n")},
		command:    &commandTransaction{conn: connection, tenantID: scope.TenantID},
		connection: connection, reservation: reservation, completion: completion,
	}
}

func TestExecuteAtomicSkipsCallbackWithoutAcquisition(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"pending", "replay", "reservation read failure", "reservation commit failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newAtomicExecutionFixture()
			failure := errors.New("reservation unavailable")
			want := idempotency.Execution{}
			var wantErr error
			wantCommits, wantRollbacks := 1, 0
			switch mode {
			case "pending", "replay":
				var status *int
				var headers, body []byte
				want = idempotency.Execution{ReservationID: executionTestReservationID, Pending: true}
				if mode == "replay" {
					status, body = &f.response.Status, f.response.Body
					headers = []byte(`{"ETag":["\"rev-2\""],"Link":["</first>; rel=first","</next>; rel=next"]}`)
					want = idempotency.Execution{ReservationID: executionTestReservationID, Replayed: true, Response: f.response}
				}
				f.reservation.queryRows = []pgx.Row{
					scannerFunc(func(...any) error { return pgx.ErrNoRows }),
					scriptedValueRow([]any{executionTestReservationID, f.response.Digest, status, headers, body, false, atomicCommandProtocolVersion, &f.scope.AuthorizationDigest, (*time.Time)(nil), []byte(nil)}),
				}
			case "reservation read failure":
				f.reservation.queryRows = []pgx.Row{scannerFunc(func(...any) error { return failure })}
				wantErr, wantCommits, wantRollbacks = failure, 0, 1
			case "reservation commit failure":
				f.reservation.commitErr = failure
				wantErr, wantRollbacks = failure, 1
			}
			calls := 0
			got, err := (&PostgresIdempotencyStore{}).executeAtomic(t.Context(), f.command, f.scope, f.response.Digest, f.expires, func(context.Context) idempotency.Response {
				calls++
				return f.response
			})
			if !errors.Is(err, wantErr) || wantErr != nil && catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !reflect.DeepEqual(got, want) {
				t.Fatalf("execution = %#v, %v; want %#v, cause %v", got, err, want, wantErr)
			}
			if calls != 0 || len(f.connection.options) != 1 || len(f.reservation.queryRows) != 0 || f.command.tx != nil || len(f.completion.statements) != 0 || f.completion.commits != 0 {
				t.Fatalf("unexpected execution after reservation: calls=%d begins=%d remaining rows=%d completion=%#v", calls, len(f.connection.options), len(f.reservation.queryRows), f.completion)
			}
			if f.reservation.commits != wantCommits || f.reservation.rollbacks != wantRollbacks {
				t.Fatalf("reservation commits/rollbacks = %d/%d; want %d/%d", f.reservation.commits, f.reservation.rollbacks, wantCommits, wantRollbacks)
			}
		})
	}
}

func TestExecuteAtomicCompletesBoundResponse(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"without writes", "shared write", "canceled after write", "aborted failure response"} {
		t.Run(mode, func(t *testing.T) {
			f := newAtomicExecutionFixture()
			work := f.completion
			failure := errors.New("response encoding failed")
			wantBegins := 2
			if mode == "aborted failure response" {
				work = &fakeCommandTx{}
				f.connection.transactions = []pgx.Tx{f.reservation, work, f.completion}
				f.response.Status, wantBegins = 500, 3
			}
			attribution := audit.Execution{RequestID: "request-42", AuthorizationDigest: f.scope.AuthorizationDigest}
			parent, cancel := context.WithCancel(audit.WithExecution(t.Context(), attribution))
			defer cancel()
			calls := 0
			got, err := (&PostgresIdempotencyStore{}).executeAtomic(parent, f.command, f.scope, f.response.Digest, f.expires, func(ctx context.Context) idempotency.Response {
				calls++
				wantAttribution := attribution
				wantAttribution.CommandID = executionTestReservationID
				if commandTransactionFrom(ctx) != f.command || audit.ExecutionFrom(ctx) != wantAttribution || ctx.Done() != parent.Done() {
					t.Fatal("callback lost the command coordinator, audit attribution or request cancellation")
				}
				if f.reservation.commits != 1 || f.reservation.rollbacks != 0 || f.command.tx != nil || len(f.connection.options) != 1 {
					t.Fatal("callback began before reservation committed or with an open preparation transaction")
				}
				if mode != "without writes" {
					if err := f.command.run(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error {
						if tx != work {
							t.Fatal("callback joined the wrong transaction")
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "canceled after write" {
					cancel()
				}
				if mode == "aborted failure response" {
					idempotency.AbortCommand(ctx, failure)
					if !errors.Is(f.command.failed, failure) || work.rollbacks != 1 || work.commits != 0 || f.command.tx != nil {
						t.Fatal("callback abort hook failed to roll back the shared transaction")
					}
				}
				return f.response
			})
			want := idempotency.Execution{ReservationID: executionTestReservationID, Response: f.response}
			if err != nil || !reflect.DeepEqual(got, want) || calls != 1 || !f.command.closed || f.command.tx != nil || len(f.connection.options) != wantBegins || len(f.connection.transactions) != 0 {
				t.Fatalf("execution = %#v, %v; calls=%d begins=%d closed=%t remaining transactions=%d", got, err, calls, len(f.connection.options), f.command.closed, len(f.connection.transactions))
			}
			if audit.ExecutionFrom(parent) != attribution || commandTransactionFrom(parent) != nil {
				t.Fatal("execution changed the parent context")
			}
			if f.completion.commits != 1 || f.completion.commitCtx != nil || f.completion.commitDeadline.IsZero() || !f.completion.commitDeadline.Equal(f.command.cleanupDeadline) {
				t.Fatal("response completion lost its cancellation-independent cleanup deadline")
			}
			if len(f.completion.arguments) != 2 || len(f.completion.arguments[1]) != 11 || !strings.Contains(f.completion.statements[1], "UPDATE idempotency_keys") {
				t.Fatalf("completion writes = %#v", f.completion.arguments)
			}
			args := f.completion.arguments[1]
			headers, ok := args[8].([]byte)
			if !ok {
				t.Fatalf("encoded headers have type %T", args[8])
			}
			var decoded map[string][]string
			if err := json.Unmarshal(headers, &decoded); err != nil || !reflect.DeepEqual(decoded, f.response.Headers) {
				t.Fatalf("retained headers = %#v, %v", decoded, err)
			}
			authorityJSON, ok := args[10].([]byte)
			if !ok {
				t.Fatalf("encoded authority has type %T", args[10])
			}
			var authority idempotency.ReplayAuthority
			if err := json.Unmarshal(authorityJSON, &authority); err != nil || !reflect.DeepEqual(authority, f.response.Authority) {
				t.Fatalf("retained replay authority = %#v, %v", authority, err)
			}
			wantArgs := []any{f.scope.TenantID, f.scope.Actor, f.scope.Route, f.scope.Key, f.response.Digest, executionTestToken, f.scope.AuthorizationDigest, f.response.Status, headers, f.response.Body, args[10]}
			if !reflect.DeepEqual(args, wantArgs) || !reflect.DeepEqual(f.reservation.queryRowArguments, [][]any{{f.scope.TenantID, f.scope.Actor, f.scope.Route, f.scope.Key, f.response.Digest, f.expires, f.scope.AuthorizationDigest}}) {
				t.Fatalf("reservation/completion bindings = %#v / %#v", f.reservation.queryRowArguments, args)
			}
		})
	}
}

func TestExecuteAtomicWithholdsResponseOnFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"digest mismatch", "completion write failure", "lost ownership", "commit failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newAtomicExecutionFixture()
			failure := errors.New("completion unavailable")
			wantCode := catalog.CodeDependencyUnavailable
			var wantErr error
			wantWrites := 2
			wantCommits := 0
			switch mode {
			case "digest mismatch":
				wantCode, wantWrites = catalog.CodeInternal, 1
			case "completion write failure":
				f.completion.execErrors = []error{nil, failure}
				wantErr = failure
			case "lost ownership":
				f.completion.execTags = []pgconn.CommandTag{pgconn.NewCommandTag("SELECT 1"), pgconn.NewCommandTag("UPDATE 0")}
				wantCode = catalog.CodeConflict
			case "commit failure":
				f.completion.commitErr = failure
				wantErr, wantCommits = failure, 1
			}
			calls := 0
			got, err := (&PostgresIdempotencyStore{}).executeAtomic(t.Context(), f.command, f.scope, f.response.Digest, f.expires, func(ctx context.Context) idempotency.Response {
				calls++
				if err := f.command.run(ctx, pgx.TxOptions{}, func(pgx.Tx) error { return nil }); err != nil {
					t.Fatal(err)
				}
				response := f.response
				if mode == "digest mismatch" {
					response.Digest = "sha256:" + strings.Repeat("c", 64)
				}
				return response
			})
			if catalog.ErrorCodeOf(err) != wantCode || wantErr != nil && !errors.Is(err, wantErr) || !reflect.DeepEqual(got, idempotency.Execution{}) {
				t.Fatalf("execution = %#v, %v; want empty result, %s and cause %v", got, err, wantCode, wantErr)
			}
			if calls != 1 || f.reservation.commits != 1 || f.completion.commits != wantCommits || len(f.completion.statements) != wantWrites || f.command.tx != nil || len(f.connection.options) != 2 {
				t.Fatalf("failed execution calls=%d completion=%#v begins=%d", calls, f.completion, len(f.connection.options))
			}
			if mode != "commit failure" && f.completion.rollbacks != 1 {
				t.Fatalf("failed execution rollbacks = %d; want 1", f.completion.rollbacks)
			}
		})
	}
}

func TestExecuteAtomicPanicAbortsBeforeReturningToLockOwner(t *testing.T) {
	t.Parallel()
	f := newAtomicExecutionFixture()
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	panicValue := &struct{ message string }{message: "executor panicked"}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = (&PostgresIdempotencyStore{}).executeAtomic(parent, f.command, f.scope, f.response.Digest, f.expires, func(ctx context.Context) idempotency.Response {
			if err := f.command.run(ctx, pgx.TxOptions{}, func(pgx.Tx) error { return nil }); err != nil {
				t.Fatal(err)
			}
			cancel()
			panic(panicValue)
		})
	}()
	if recovered != panicValue || f.command.tx != nil || catalog.ErrorCodeOf(f.command.failed) != catalog.CodeInternal {
		t.Fatalf("panic = %v; active transaction=%v failure=%v", recovered, f.command.tx, f.command.failed)
	}
	if f.reservation.commits != 1 || f.completion.commits != 0 || f.completion.rollbacks != 1 || len(f.completion.statements) != 1 || len(f.connection.options) != 2 {
		t.Fatalf("panic cleanup reservation=%#v completion=%#v begins=%d", f.reservation.fakeCommandTx, f.completion, len(f.connection.options))
	}
	if f.completion.rollbackCtx != nil || f.completion.rollbackDeadline.IsZero() || !f.completion.rollbackDeadline.Equal(f.command.cleanupDeadline) {
		t.Fatal("panic rollback lost the cancellation-independent cleanup deadline")
	}
}
