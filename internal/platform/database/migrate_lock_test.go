// Migration lock units verify bounded cleanup and session discard without a pool.
package database

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type migrationLockStub struct {
	queryRow func(context.Context, string, ...any) pgx.Row
	close    func(context.Context) error
}

func (session migrationLockStub) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return session.queryRow(ctx, sql, args...)
}

func (session migrationLockStub) Close(ctx context.Context) error { return session.close(ctx) }

// TestReleaseMigrationLock keeps a confirmed-unlocked session usable and discards
// an uncertain session, preserving both errors under one independent deadline.
func TestReleaseMigrationLock(t *testing.T) {
	t.Parallel()
	queryFailure := errors.New("injected unlock failure")
	closeFailure := errors.New("injected close failure")
	for _, test := range []struct {
		name               string
		unlocked           bool
		queryErr, closeErr error
		wantError          string
	}{
		{name: "unlocked", unlocked: true},
		{name: "query failure", queryErr: queryFailure, wantError: "release migration lock: injected unlock failure"},
		{name: "query and close failures", queryErr: queryFailure, closeErr: closeFailure, wantError: "release migration lock: injected unlock failure\nclose migration lock connection: injected close failure"},
		{name: "not held", wantError: "release migration lock: advisory lock was not held by its connection"},
		{name: "not held and close failure", closeErr: closeFailure, wantError: "release migration lock: advisory lock was not held by its connection\nclose migration lock connection: injected close failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, parentState := range []string{"active", "canceled", "expired"} {
				t.Run(parentState, func(t *testing.T) {
					type contextKey struct{}
					parent := context.WithValue(t.Context(), contextKey{}, "migration cleanup")
					switch parentState {
					case "canceled":
						var cancel context.CancelFunc
						parent, cancel = context.WithCancel(parent)
						cancel()
					case "expired":
						var cancel context.CancelFunc
						parent, cancel = context.WithDeadline(parent, time.Now().Add(-time.Hour))
						defer cancel()
					}
					var queryDone <-chan struct{}
					var queryDeadline time.Time
					queries, closes := 0, 0
					session := migrationLockStub{
						queryRow: func(ctx context.Context, sql string, args ...any) pgx.Row {
							queries++
							queryDone = ctx.Done()
							queryDeadline, _ = ctx.Deadline()
							if sql != "SELECT pg_advisory_unlock($1)" || !reflect.DeepEqual(args, []any{int64(880185172399)}) {
								t.Fatalf("unexpected unlock: %q %v", sql, args)
							}
							if ctx.Err() != nil || ctx.Value(contextKey{}) != "migration cleanup" {
								t.Fatalf("cleanup inherited cancellation or lost context values: %v", ctx.Err())
							}
							if test.queryErr != nil {
								return scannerFunc(func(...any) error { return test.queryErr })
							}
							return scriptedValueRow([]any{test.unlocked})
						},
						close: func(ctx context.Context) error {
							closes++
							deadline, ok := ctx.Deadline()
							if ctx.Done() != queryDone || !ok || !deadline.Equal(queryDeadline) || ctx.Err() != nil {
								t.Fatalf("session close did not share live cleanup context: %v", ctx.Err())
							}
							return test.closeErr
						},
					}
					started := time.Now()
					err := releaseMigrationLock(parent, session)
					finished := time.Now()
					if test.wantError == "" {
						if err != nil {
							t.Fatalf("confirmed unlock failed: %v", err)
						}
					} else if err == nil || err.Error() != test.wantError {
						t.Fatalf("cleanup error = %v, want %q", err, test.wantError)
					}
					if test.queryErr != nil && !errors.Is(err, test.queryErr) || test.closeErr != nil && !errors.Is(err, test.closeErr) {
						t.Fatalf("cleanup lost an error cause: %v", err)
					}
					wantCloses := 1
					if test.unlocked {
						wantCloses = 0
					}
					if queries != 1 || closes != wantCloses {
						t.Fatalf("cleanup calls: queries=%d closes=%d, want 1/%d", queries, closes, wantCloses)
					}
					if queryDeadline.Before(started.Add(5*time.Second)) || queryDeadline.After(finished.Add(5*time.Second)) {
						t.Fatalf("cleanup deadline %v is not independently bounded to five seconds", queryDeadline)
					}
					select {
					case <-queryDone:
					default:
						t.Fatal("cleanup context was not canceled after return")
					}
				})
			}
		})
	}
}
