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

func TestPostgresMigrationWaiterRechecksLedgerBeforeReconcilingGrants(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := t.Context()
	apiRole, workerRole := db.api.Config().ConnConfig.User, db.worker.Config().ConnConfig.User
	job := housekeepingJob("00000000-0000-7000-8000-000000000901", "", time.Now().UTC())
	if _, _, err := db.apiJobs.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	checkJob := func() {
		t.Helper()
		got, err := db.workerJobs.Get(ctx, job.TenantID, job.ID)
		if err != nil || got.ID != job.ID || got.State != job.State {
			t.Fatalf("worker read job %q in state %q, error %v", got.ID, got.State, err)
		}
	}
	checkJob()
	if _, err := db.operator.Exec(ctx, "REVOKE SELECT (state) ON jobs FROM "+pgx.Identifier{workerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	checkMissingGrant := func() {
		t.Helper()
		if _, err := VerifyRuntimeRole(ctx, db.worker, RuntimeRoleWorker); err == nil || !strings.Contains(err.Error(), "missing SELECT on jobs.state") {
			t.Fatalf("worker role verification returned %v", err)
		}
		if _, err := VerifyRuntimeRole(ctx, db.api, RuntimeRoleAPI); err != nil {
			t.Fatalf("worker grant changed API access: %v", err)
		}
		_, err := db.workerJobs.Get(ctx, job.TenantID, job.ID)
		requireInsufficientPrivilege(t, err)
	}
	checkMissingGrant()

	waiter := singleConnectionTestPool(t, ctx, db.operator)
	var waiterPID uint32
	if err := waiter.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&waiterPID); err != nil {
		t.Fatal(err)
	}
	held, err := db.operator.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(ctx, held)()
	if _, err := held.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationAdvisoryLockKey); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec(ctx, "UPDATE catalog_schema_migrations SET checksum=repeat('0',64) WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	const ledgerRows = `SELECT jsonb_agg(to_jsonb(m) ORDER BY version)::text FROM catalog_schema_migrations m`
	var damaged string
	if err := held.QueryRow(ctx, ledgerRows).Scan(&damaged); err != nil {
		t.Fatal(err)
	}
	// The competing session can still see the healthy committed ledger.
	before, err := Plan(ctx, waiter)
	if err != nil || len(before) == 0 || before[0].Version != 1 || !before[0].Applied {
		t.Fatalf("pre-commit plan missing the first applied migration: %v", err)
	}
	done := trialAsync(t, func(ctx context.Context) error {
		_, _, err := ApplyAndReconcileRuntimePrivileges(ctx, waiter, apiRole, workerRole)
		return err
	})
	trialWaitBlocked(t, db.operator, waiterPID, trialPID(t, held))
	if err := held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	const changed = "migration 1 checksum changed after application"
	if err := <-done; err == nil || !strings.Contains(err.Error(), changed) {
		t.Fatalf("waiting apply returned %v", err)
	}
	if _, err := Plan(ctx, waiter); err == nil || !strings.Contains(err.Error(), changed) {
		t.Fatalf("plan against the committed ledger returned %v", err)
	}
	var retained string
	if err := db.operator.QueryRow(ctx, ledgerRows).Scan(&retained); err != nil || retained != damaged {
		t.Fatalf("refused migration changed the ledger: %v", err)
	}
	checkMissingGrant()

	// Restore just the injected fault and retry from a different pool. All other
	// ledger entries must also survive the refusal, and only grants need repair.
	tag, err := db.operator.Exec(ctx, "UPDATE catalog_schema_migrations SET checksum=$1 WHERE version=1 AND checksum=repeat('0',64)", before[0].Checksum)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("stored checksum changed during refusal: %v, %v", tag, err)
	}
	repairCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	applied, reports, err := ApplyAndReconcileRuntimePrivileges(repairCtx, db.operator, apiRole, workerRole)
	if err != nil || len(applied) != 0 || len(reports) != 2 || !reports[0].Compliant || !reports[1].Compliant {
		t.Fatalf("retry after checksum restoration: applied %v, reports %v, error %v", applied, reports, err)
	}
	if after, err := Plan(ctx, db.operator); err != nil || !slices.Equal(after, before) {
		t.Fatalf("retry changed the migration ledger: %v", err)
	}
	if _, err := VerifyRuntimeRole(ctx, db.worker, RuntimeRoleWorker); err != nil {
		t.Fatalf("repaired worker role refused admission: %v", err)
	}
	checkJob()
}
