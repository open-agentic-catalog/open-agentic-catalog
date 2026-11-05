//go:build integration

package database

import (
	"context"
	"testing"
	"time"
)

// Called with the owned unprivileged role from the connection-admission test.
// Keep real database scheduling outside unit tests and use eventual counters,
// not an assumed exact maintenance deadline, to observe recycling.
func verifyPoolRecycling(t *testing.T, dsn, role string) {
	t.Helper()
	for _, mode := range []string{"idle", "lifetime"} {
		t.Run(mode+" recycling", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			settings := PoolConfig{MaxConnections: 1, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Hour, HealthCheckPeriod: 20 * time.Millisecond}
			if mode == "idle" {
				settings.MaxConnIdleTime = 100 * time.Millisecond
			} else {
				settings.MaxConnLifetime = 250 * time.Millisecond
			}
			pool, err := OpenRuntimeObserved(ctx, dsn, settings, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			actual := pool.Config()
			if actual.MaxConns != 1 || actual.MaxConnLifetime != settings.MaxConnLifetime || actual.MaxConnIdleTime != settings.MaxConnIdleTime || actual.HealthCheckPeriod != settings.HealthCheckPeriod {
				t.Fatal("configured policy did not reach the physical pool")
			}
			connection, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Release()
			oldPID := connection.Conn().PgConn().PID()
			tx, err := connection.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			// A transaction can outlive the configured idle/lifetime threshold.
			// SET LOCAL must not leak into the next connection's session.
			if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'Pacific/Honolulu'`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `SELECT pg_sleep(0.35)`); err != nil {
				t.Fatalf("maintenance interrupted an acquired transaction: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if connection.Conn().PgConn().PID() != oldPID {
				t.Fatal("acquired connection was replaced")
			}
			connection.Release()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				stat := pool.Stat()
				count := stat.MaxIdleDestroyCount()
				if mode == "lifetime" {
					count = stat.MaxLifetimeDestroyCount()
				}
				if count > 0 && stat.TotalConns() == 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatalf("%s connection did not recycle: %v", mode, ctx.Err())
				case <-ticker.C:
				}
			}
			var pid uint32
			var user, timezone, readOnly string
			if err := pool.QueryRow(ctx, `SELECT pg_backend_pid(), current_user, current_setting('TimeZone'), current_setting('transaction_read_only')`).Scan(&pid, &user, &timezone, &readOnly); err != nil {
				t.Fatal(err)
			}
			if pid == oldPID || user != role || timezone != "UTC" || readOnly != "off" {
				t.Fatalf("replacement session: pid=%d previous=%d user=%s timezone=%s readOnly=%s", pid, oldPID, user, timezone, readOnly)
			}
			repository, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.Ping(ctx); err != nil {
				t.Fatalf("replacement readiness: %v", err)
			}
		})
	}
}
