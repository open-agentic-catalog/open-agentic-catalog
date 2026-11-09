//go:build functional && integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Two independent backend sessions must both be observed waiting on the real
// admission lock before either can proceed. Goroutine scheduling is not the
// concurrency oracle, and all rows live in this invocation's private schema.
func TestReceiverConcurrentAdmission(t *testing.T) {
	dsn := os.Getenv("CATALOG_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Fatal("select the receiver's owned PostgreSQL Make target")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "admission_" + hex.EncodeToString(nonce[:])
	if _, err := owner.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := owner.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{name}.Sanitize()+" CASCADE"); err != nil {
			t.Error("owned admission schema cleanup failed")
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	q.Set("application_name", name)
	u.RawQuery = q.Encode()
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	s := &postgresStore{pool: pool, capacity: 1, retention: time.Minute}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	event := func(label string) Event {
		ids := map[string]string{"distinct-a": "00000000-0000-4000-8000-000000000001", "distinct-b": "00000000-0000-4000-8000-000000000002", "same": "00000000-0000-4000-8000-000000000003", "next": "00000000-0000-4000-8000-000000000004", "bounded-prune": "00000000-0000-4000-8000-000000000005"}
		return Event{Source: eventSource + tenant, ID: ids[label], Action: "listing.release.published"}
	}
	count := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM mcp_receiver_inbox").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	overlap := func(a, b Event) []error {
		t.Helper()
		blocker, err := owner.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Release()
		if _, err := blocker.Exec(ctx, "SELECT pg_advisory_lock($1)", admissionLock); err != nil {
			t.Fatal(err)
		}
		unlocked := false
		defer func() {
			if !unlocked {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				defer stop()
				_, _ = blocker.Exec(cleanup, "SELECT pg_advisory_unlock($1)", admissionLock)
			}
		}()
		done := make(chan error, 2)
		for _, e := range []Event{a, b} {
			go func() { done <- s.Accept(ctx, e, at) }()
		}
		// Native pg_stat_activity proves distinct sessions reached and blocked at
		// this lock. A removed lock fails here rather than a random unique violation.
		barrier, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			var waiting int
			if err := owner.QueryRow(barrier, `SELECT count(DISTINCT pid) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND wait_event='advisory'`, name).Scan(&waiting); err != nil {
				if barrier.Err() != nil {
					t.Fatal("both independent admissions did not wait on the serialization lock")
				}
				t.Fatal(err)
			}
			if waiting == 2 {
				break
			}
			select {
			case <-barrier.Done():
				t.Fatal("both independent admissions did not wait on the serialization lock")
			case <-tick.C:
			}
		}
		if _, err := blocker.Exec(ctx, "SELECT pg_advisory_unlock($1)", admissionLock); err != nil {
			t.Fatal(err)
		}
		unlocked = true
		result := make([]error, 0, 2)
		for range 2 {
			select {
			case err := <-done:
				result = append(result, err)
			case <-ctx.Done():
				t.Fatal("admissions did not complete")
			}
		}
		return result
	}
	errorsFound := overlap(event("distinct-a"), event("distinct-b"))
	success, full := 0, 0
	for _, err := range errorsFound {
		switch {
		case err == nil:
			success++
		case errors.Is(err, errFull):
			full++
		default:
			t.Fatalf("unexpected admission error: %v", err)
		}
	}
	if success != 1 || full != 1 || count() != 1 {
		t.Fatal("concurrent distinct events exceeded the last capacity slot")
	}
	if _, err := pool.Exec(ctx, "DELETE FROM mcp_receiver_inbox"); err != nil {
		t.Fatal(err)
	}
	for _, err := range overlap(event("same"), event("same")) {
		if err != nil {
			t.Fatalf("concurrent retained duplicate refused: %v", err)
		}
	}
	if count() != 1 {
		t.Fatal("concurrent duplicate lost its single retained identity")
	}
	if err := s.Accept(ctx, event("same"), at); err != nil {
		t.Fatal("retained duplicate not acknowledged at capacity")
	}
	if err := s.Accept(ctx, event("next"), at.Add(24*time.Hour)); !errors.Is(err, errFull) || count() != 1 {
		t.Fatal("pending work was pruned or capacity ignored")
	}
	if _, err := pool.Exec(ctx, "UPDATE mcp_receiver_inbox SET completed_at=$1", at.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Accept(ctx, event("next"), at); err != nil || count() != 1 {
		t.Fatal("eligible completed receipt did not release capacity", err)
	}
	if err := s.Accept(ctx, event("same"), at); !errors.Is(err, errFull) {
		t.Fatal("pruned identity unexpectedly retained duplicate authority")
	}
	if _, err := pool.Exec(ctx, "UPDATE mcp_receiver_inbox SET completed_at=$1", at.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Accept(ctx, event("same"), at); err != nil || count() != 1 {
		t.Fatal("redelivery after eligible pruning was not readmitted", err)
	}
	// One call may prune only 128 eligible receipts; it must preserve pending work.
	if _, err := pool.Exec(ctx, `INSERT INTO mcp_receiver_inbox(source,id,event,accepted_at,completed_at,next_attempt_at) SELECT 'https://owned.example/old',i::text,'{}',$1,$2,$1 FROM generate_series(1,129) i`, at, at.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s.capacity = 3
	if err := s.Accept(ctx, event("bounded-prune"), at); err != nil || count() != 3 {
		t.Fatal("prune work was not bounded to 128 or pending identity was lost", err)
	}
	var pending int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM mcp_receiver_inbox WHERE id=$1 AND completed_at IS NULL", event("same").ID).Scan(&pending); err != nil || pending != 1 {
		t.Fatal("bounded cleanup lost pending evidence", err)
	}
}
