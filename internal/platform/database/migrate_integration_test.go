//go:build integration

package database

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Use the already migrated exact-role fixture; advisory locks are database-wide,
// so these cases must remain serial with the other migration tests.
func testMigrationApplyLockFailures(t *testing.T, pool, admin *pgxpool.Pool, schema, apiRole, workerRole string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	before, err := Plan(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func() {
		t.Helper()
		after, err := Plan(ctx, pool)
		if err != nil || !slices.Equal(before, after) {
			t.Fatalf("failed apply changed migration ledger: %v", err)
		}
		for _, binding := range []struct{ runtimeRole, databaseRole string }{{RuntimeRoleAPI, apiRole}, {RuntimeRoleWorker, workerRole}} {
			report, err := inspectRuntimePrivileges(ctx, pool, schema, binding.runtimeRole, binding.databaseRole)
			if err != nil || !report.Compliant {
				t.Fatalf("failed apply changed runtime privileges: %#v, %v", report, err)
			}
		}
	}

	func() {
		holder, err := admin.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Release()
		if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLockKey); err != nil {
			t.Fatal(err)
		}
		applyCtx, cancelApply := context.WithCancel(ctx)
		done := make(chan struct{})
		var applied []Migration
		var reports []RuntimePrivilegeReport
		var applyErr error
		// Install cleanup before starting the waiter, including on assertion failure.
		defer func() {
			cancelApply()
			if err := releaseMigrationLock(ctx, holder.Conn()); err != nil {
				t.Errorf("release competing migration lock: %v", err)
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("migration apply did not stop during cleanup")
			}
		}()
		go func() {
			applied, reports, applyErr = ApplyAndReconcileRuntimePrivileges(applyCtx, pool, apiRole, workerRole)
			close(done)
		}()
		poll := time.NewTicker(5 * time.Millisecond)
		defer poll.Stop()
		for {
			var waiting bool
			if err := admin.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_catalog.pg_stat_activity
   WHERE $1 = ANY(pg_catalog.pg_blocking_pids(pid))
)`, holder.Conn().PgConn().PID()).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			select {
			case <-done:
				t.Fatalf("migration apply completed without waiting for the competing lock: %v", applyErr)
			case <-ctx.Done():
				t.Fatal("migration apply never waited for the competing lock")
			case <-poll.C:
			}
		}
		cancelApply()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("migration apply did not return after cancellation")
		}
		// Open sends a PostgreSQL cancel request before falling back to closing
		// an unresponsive connection; either cancellation form is valid here.
		var pgErr *pgconn.PgError
		canceled := errors.Is(applyErr, context.Canceled) || (errors.As(applyErr, &pgErr) && pgErr.Code == "57014")
		if !canceled || applyCtx.Err() != context.Canceled || ctx.Err() != nil || len(applied) != 0 || len(reports) != 0 {
			t.Fatalf("cancelled migration apply = %#v / %#v, %v", applied, reports, applyErr)
		}
		assertUnchanged()
	}()

	// A syntactically valid missing role fails only after acquiring the lock.
	missingRole := apiRole + "_missing"
	applied, reports, err := ApplyAndReconcileRuntimePrivileges(ctx, pool, missingRole, workerRole)
	if !errors.Is(err, pgx.ErrNoRows) || !strings.Contains(err.Error(), missingRole) || len(applied) != 0 || len(reports) != 0 {
		t.Fatalf("missing-role migration apply = %#v / %#v, %v", applied, reports, err)
	}
	assertUnchanged()
	// Pin a session from a different pool: reacquiring on the applying session
	// would succeed even if it leaked its session-level lock.
	probe, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Release()
	var available bool
	if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, migrationAdvisoryLockKey).Scan(&available); err != nil || !available {
		t.Fatalf("failed role validation retained the migration lock: available=%v err=%v", available, err)
	}
	if err := releaseMigrationLock(ctx, probe.Conn()); err != nil {
		t.Fatal(err)
	}
	applied, reports, err = ApplyAndReconcileRuntimePrivileges(ctx, pool, apiRole, workerRole)
	if err != nil || len(applied) != 0 || len(reports) != 2 || !reports[0].Compliant || !reports[1].Compliant {
		t.Fatalf("migration apply did not recover after lock failures: %#v / %#v, %v", applied, reports, err)
	}
}

func TestMigrationPlannerRejectsUnknownAppliedVersionBeforeMutation(t *testing.T) {
	baseDSN := os.Getenv("CATALOG_TEST_DATABASE_DSN")
	if baseDSN == "" {
		t.Skip("CATALOG_TEST_DATABASE_DSN is not set")
	}
	ctx := context.Background()
	admin, err := Open(ctx, baseDSN, testPoolConfig(2))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "catalog_unknown_migration_" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
	}()

	pool, err := Open(ctx, withSearchPath(t, baseDSN, schema+",public"), testPoolConfig(2))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := applyMigrationsForTest(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO catalog_schema_migrations(version,checksum) VALUES (999,'unknown')`); err != nil {
		t.Fatal(err)
	}
	ledger := func() string {
		t.Helper()
		var snapshot string
		if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY version)::text,'[]') FROM catalog_schema_migrations m`).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	before := ledger()

	for name, invoke := range map[string]func() error{
		"plan":  func() error { _, err := Plan(ctx, pool); return err },
		"apply": func() error { _, err := applyMigrationsForTest(ctx, pool); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := invoke(); err == nil || !strings.Contains(err.Error(), "applied migration version 999 is absent from the embedded migration bundle") {
				t.Fatalf("unknown applied migration returned %v", err)
			}
			if after := ledger(); after != before {
				t.Fatal("migration ledger changed after refusal")
			}
		})
	}
}
