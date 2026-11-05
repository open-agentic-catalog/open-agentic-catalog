//go:build integration

package database

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Both restore readers must discard their report and release their transaction
// after cancellation inside a real snapshot, while preserving retained rows.
func testInterruptedRestoreRead(t *testing.T, pool *pgxpool.Pool, relation string, read func(context.Context) (any, error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	readState := func() string {
		t.Helper()
		var state string
		if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(retained) ORDER BY id),'[]'::jsonb)::text FROM `+pgx.Identifier{relation}.Sanitize()+` retained`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := readState()
	holder, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(ctx, holder)()
	if _, err := holder.Exec(ctx, `LOCK TABLE `+pgx.Identifier{relation}.Sanitize()+` IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	// Pin a distinct observer so a pool query cannot reuse the released reader
	// connection and mistake its own transaction for unfinished cleanup.
	observer, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Release()
	readCtx, cancelRead := context.WithCancel(ctx)
	done := make(chan struct{})
	var report any
	var readErr error
	defer func() {
		cancelRead()
		rollbackForCleanup(ctx, holder)()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("interrupted restore reader did not stop during cleanup")
		}
	}()
	go func() {
		report, readErr = read(readCtx)
		close(done)
	}()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	var readerPID uint32
	for {
		if err := observer.QueryRow(ctx, `
SELECT coalesce((SELECT pid FROM pg_catalog.pg_stat_activity
 WHERE $1=ANY(pg_catalog.pg_blocking_pids(pid)) AND xact_start IS NOT NULL AND wait_event_type='Lock' LIMIT 1),0)`, holder.Conn().PgConn().PID()).Scan(&readerPID); err != nil {
			t.Fatal(err)
		}
		if readerPID != 0 {
			break
		}
		select {
		case <-done:
			t.Fatalf("restore reader returned before the blocked read: %v", readErr)
		case <-ctx.Done():
			t.Fatal("restore reader never reached the blocked read")
		case <-poll.C:
		}
	}
	cancelRead()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("restore reader did not return after cancellation")
	}
	var pgErr *pgconn.PgError
	canceled := errors.Is(readErr, context.Canceled) || (errors.As(readErr, &pgErr) && pgErr.Code == "57014")
	if !canceled || readCtx.Err() != context.Canceled || ctx.Err() != nil {
		t.Fatalf("interrupted restore read returned %v (context: %v)", readErr, ctx.Err())
	}
	if report == nil || !reflect.ValueOf(report).IsZero() {
		t.Fatal("interrupted restore read returned a successful or partial report")
	}
	// pgx may discard a canceled connection asynchronously. Require both the
	// transaction and its pool acquisition to disappear within the deadline.
	for {
		var active bool
		if err := observer.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_stat_activity WHERE pid=$1 AND xact_start IS NOT NULL)`, readerPID).Scan(&active); err != nil {
			t.Fatal(err)
		}
		acquired := pool.Stat().AcquiredConns()
		if !active && acquired == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("restore reader cleanup did not finish: active=%t acquired=%d, want only the blocker and observer", active, acquired)
		case <-poll.C:
		}
	}
	if holder.Conn().PgConn().TxStatus() != 'T' {
		t.Fatal("restore read cancellation lost its blocker")
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if readState() != before {
		t.Fatal("interrupted restore verification changed retained rows")
	}
}
