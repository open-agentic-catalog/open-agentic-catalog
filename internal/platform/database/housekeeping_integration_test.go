//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

const (
	housekeepingTenantA = "00000000-0000-7000-8000-000000000801"
	housekeepingTenantB = "00000000-0000-7000-8000-000000000802"
)

type housekeepingDatabase struct {
	operator    *pgxpool.Pool
	api         *pgxpool.Pool
	worker      *pgxpool.Pool
	housekeeper *PostgresHousekeepingStore
	federation  *PostgresFederationStore
	apiJobs     *PostgresJobStore
	workerJobs  *PostgresJobStore
}

func newHousekeepingDatabase(tb testing.TB) *housekeepingDatabase {
	tb.Helper()
	baseDSN := os.Getenv("CATALOG_TEST_DATABASE_DSN")
	if baseDSN == "" {
		tb.Skip("CATALOG_TEST_DATABASE_DSN is not set")
	}
	ctx := context.Background()
	admin, err := Open(ctx, baseDSN, testPoolConfig(4))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(admin.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	schema := "catalog_housekeeping_" + suffix
	apiRole := "catalog_hk_api_" + suffix
	workerRole := "catalog_hk_worker_" + suffix
	apiPassword := "catalog-hk-api-" + suffix
	workerPassword := "catalog-hk-worker-" + suffix
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		_, _ = admin.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{apiRole}.Sanitize())
		_, _ = admin.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{workerRole}.Sanitize())
	})
	testDSN := withSearchPath(tb, baseDSN, schema+",public")
	operator, err := Open(ctx, testDSN, testPoolConfig(4))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(operator.Close)
	if _, err := applyMigrationsForTest(ctx, operator); err != nil {
		tb.Fatal(err)
	}
	if _, err := operator.Exec(ctx, `
INSERT INTO tenants(id,display_name,created_at) VALUES
  ($1,'Housekeeping tenant A',clock_timestamp()),
  ($2,'Housekeeping tenant B',clock_timestamp())`, housekeepingTenantA, housekeepingTenantB); err != nil {
		tb.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE ROLE ` + pgx.Identifier{apiRole}.Sanitize() + ` LOGIN PASSWORD '` + apiPassword + `' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS`,
		`CREATE ROLE ` + pgx.Identifier{workerRole}.Sanitize() + ` LOGIN PASSWORD '` + workerPassword + `' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION BYPASSRLS`,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			tb.Fatal(err)
		}
	}
	if _, reports, err := ApplyAndReconcileRuntimePrivileges(ctx, operator, apiRole, workerRole); err != nil || len(reports) != 2 || !reports[0].Compliant || !reports[1].Compliant {
		tb.Fatalf("reconcile housekeeping roles = %#v, %v", reports, err)
	}
	apiPool := rolePool(tb, testDSN, apiRole, apiPassword)
	tb.Cleanup(apiPool.Close)
	workerPool := rolePool(tb, testDSN, workerRole, workerPassword)
	tb.Cleanup(workerPool.Close)
	apiRepository, err := NewPostgresRepository(apiPool)
	if err != nil {
		tb.Fatal(err)
	}
	workerRepository, err := NewPostgresRepository(workerPool)
	if err != nil {
		tb.Fatal(err)
	}
	housekeeper, err := NewPostgresHousekeepingStore(workerRepository)
	if err != nil {
		tb.Fatal(err)
	}
	federationStore, err := NewPostgresFederationStore(apiRepository)
	if err != nil {
		tb.Fatal(err)
	}
	apiJobs, err := NewPostgresJobStore(apiRepository)
	if err != nil {
		tb.Fatal(err)
	}
	workerJobs, err := NewPostgresJobStore(workerRepository)
	if err != nil {
		tb.Fatal(err)
	}
	return &housekeepingDatabase{operator: operator, api: apiPool, worker: workerPool, housekeeper: housekeeper, federation: federationStore, apiJobs: apiJobs, workerJobs: workerJobs}
}

func TestPostgresHousekeepingReclaimsOnlyDisposableState(t *testing.T) {
	database := newHousekeepingDatabase(t)
	requireHousekeepingIndexes(t, database)
	now := time.Now().UTC().Truncate(time.Microsecond)
	usedInspection := housekeepingInspection("00000000-0000-7000-8000-000000000811", housekeepingTenantA, now, now.Add(2*time.Second))
	if _, err := database.federation.SaveInspection(t.Context(), usedInspection); err != nil {
		t.Fatal(err)
	}
	usedSource := housekeepingSource("00000000-0000-7000-8000-000000000821", housekeepingTenantA, now)
	usedSource.InspectionID = usedInspection.ID
	if _, err := database.federation.CreateSource(t.Context(), usedSource); err != nil {
		t.Fatal(err)
	}
	var linkedInspection *string
	if err := database.operator.QueryRow(t.Context(), `SELECT inspection_id::text FROM federation_source_revisions WHERE tenant_id=$1 AND source_id=$2 AND revision=1`, housekeepingTenantA, usedSource.ID).Scan(&linkedInspection); err != nil || linkedInspection == nil || *linkedInspection != usedInspection.ID {
		t.Fatalf("source inspection link = %v, %v", linkedInspection, err)
	}

	t.Run("idempotency responses", func(t *testing.T) {
		insertHousekeepingIdempotencyFixtures(t, database, now)
		var pendingReservation string
		if err := database.operator.QueryRow(t.Context(), `SELECT reservation_id::text FROM idempotency_keys WHERE idempotency_key='pending-current'`).Scan(&pendingReservation); err != nil {
			t.Fatal(err)
		}
		if _, err := database.worker.Exec(t.Context(), `DELETE FROM idempotency_keys WHERE reservation_id=$1`, pendingReservation); err == nil || !strings.Contains(err.Error(), "recovery remains authoritative") {
			t.Fatalf("pending idempotency delete guard = %v", err)
		}
		first, err := database.housekeeper.ReclaimExpiredIdempotency(t.Context(), 2)
		if err != nil || first.Reclaimed != 2 || !first.Saturated || first.OldestEligibleAge <= 0 {
			t.Fatalf("first idempotency cleanup = %#v, %v", first, err)
		}
		second, err := database.housekeeper.ReclaimExpiredIdempotency(t.Context(), 10)
		if err != nil || second.Reclaimed != 2 || second.Saturated {
			t.Fatalf("second idempotency cleanup = %#v, %v", second, err)
		}
		var retained []string
		rows, err := database.operator.Query(t.Context(), `SELECT idempotency_key FROM idempotency_keys ORDER BY idempotency_key`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				t.Fatal(err)
			}
			retained = append(retained, key)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if strings.Join(retained, ",") != "pending-current,pending-v2,unexpired-v2" {
			t.Fatalf("retained idempotency keys = %v", retained)
		}
	})

	t.Run("source previews and row locks", func(t *testing.T) {
		expired := housekeepingPreview("00000000-0000-7000-8000-000000000831", usedSource, now.Add(-time.Hour), now.Add(-time.Minute))
		future := housekeepingPreview("00000000-0000-7000-8000-000000000832", usedSource, now, now.Add(time.Hour))
		if _, err := database.federation.SaveChangePreview(t.Context(), expired); err != nil {
			t.Fatal(err)
		}
		if _, err := database.federation.SaveChangePreview(t.Context(), future); err != nil {
			t.Fatal(err)
		}
		result, err := database.housekeeper.ReclaimExpiredSourcePreviews(t.Context(), 10)
		if err != nil || result.Reclaimed != 1 {
			t.Fatalf("preview cleanup = %#v, %v", result, err)
		}
		if _, err := database.api.Exec(t.Context(), `DELETE FROM source_change_previews WHERE id=$1`, future.ID); err == nil {
			t.Fatal("API role could delete source previews")
		} else {
			requireInsufficientPrivilege(t, err)
		}
		locked := housekeepingPreview("00000000-0000-7000-8000-000000000833", usedSource, now.Add(-time.Hour), now.Add(-time.Minute))
		if _, err := database.federation.SaveChangePreview(t.Context(), locked); err != nil {
			t.Fatal(err)
		}
		lock, err := database.operator.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Rollback(context.Background()) }()
		if _, err := lock.Exec(t.Context(), `SELECT id FROM source_change_previews WHERE id=$1 FOR UPDATE`, locked.ID); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		result, err = database.housekeeper.ReclaimExpiredSourcePreviews(t.Context(), 1)
		if err != nil || result.Reclaimed != 0 || time.Since(started) > time.Second {
			t.Fatalf("locked preview cleanup = %#v after %s, %v", result, time.Since(started), err)
		}
		if err := lock.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		result, err = database.housekeeper.ReclaimExpiredSourcePreviews(t.Context(), 1)
		if err != nil || result.Reclaimed != 1 || !result.Saturated {
			t.Fatalf("unlocked preview cleanup = %#v, %v", result, err)
		}

		secondHousekeeper, err := NewPostgresHousekeepingStore(database.housekeeper.repository)
		if err != nil {
			t.Fatal(err)
		}
		for index, idValue := range []string{
			"00000000-0000-7000-8000-000000000834",
			"00000000-0000-7000-8000-000000000835",
			"00000000-0000-7000-8000-000000000836",
			"00000000-0000-7000-8000-000000000837",
		} {
			preview := housekeepingPreview(idValue, usedSource, now.Add(-time.Hour+time.Duration(index)*time.Microsecond), now.Add(-time.Minute))
			if _, err := database.federation.SaveChangePreview(t.Context(), preview); err != nil {
				t.Fatal(err)
			}
		}
		type replicaResult struct {
			reclaimed int64
			err       error
		}
		start := make(chan struct{})
		results := make(chan replicaResult, 2)
		for _, store := range []*PostgresHousekeepingStore{database.housekeeper, secondHousekeeper} {
			go func() {
				<-start
				result, cleanupErr := store.ReclaimExpiredSourcePreviews(t.Context(), 2)
				results <- replicaResult{reclaimed: result.Reclaimed, err: cleanupErr}
			}()
		}
		close(start)
		var reclaimed int64
		for range 2 {
			result := <-results
			if result.err != nil {
				t.Fatal(result.err)
			}
			reclaimed += result.reclaimed
		}
		var remaining int
		if err := database.operator.QueryRow(t.Context(), `SELECT count(*) FROM source_change_previews WHERE tenant_id=$1 AND source_id=$2 AND expires_at<=clock_timestamp()`, housekeepingTenantA, usedSource.ID).Scan(&remaining); err != nil || reclaimed != 4 || remaining != 0 {
			t.Fatalf("cooperating preview cleanup reclaimed %d, remaining %d: %v", reclaimed, remaining, err)
		}
	})

	t.Run("unused inspections", func(t *testing.T) {
		unused := housekeepingInspection("00000000-0000-7000-8000-000000000812", housekeepingTenantA, now.Add(-2*time.Hour), now.Add(-time.Hour))
		if _, err := database.federation.SaveInspection(t.Context(), unused); err != nil {
			t.Fatal(err)
		}
		if _, err := database.operator.Exec(t.Context(), `INSERT INTO source_inspections(id,tenant_id,document,expires_at,created_at) VALUES ($1,$2,'{}',clock_timestamp()-interval '1 hour',clock_timestamp()-interval '2 hours')`, "00000000-0000-7000-8000-000000000813", housekeepingTenantA); err != nil {
			t.Fatal(err)
		}
		waitForInspectionExpiry(t, database.operator, usedInspection.ID)
		result, err := database.housekeeper.ReclaimUnusedSourceInspections(t.Context(), 10)
		if err != nil || result.Reclaimed != 2 {
			t.Fatalf("inspection cleanup = %#v, %v", result, err)
		}
		var used, unreferenced int
		if err := database.operator.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE id=$1),count(*) FILTER (WHERE id=$2) FROM source_inspections`, usedInspection.ID, "00000000-0000-7000-8000-000000000813").Scan(&used, &unreferenced); err != nil || used != 1 || unreferenced != 0 {
			t.Fatalf("retained inspections = used %d unreferenced %d, %v", used, unreferenced, err)
		}
		if _, err := database.worker.Exec(t.Context(), `DELETE FROM source_inspections WHERE id=$1`, usedInspection.ID); err == nil || !strings.Contains(err.Error(), "retained or authoritative") {
			t.Fatalf("used inspection delete guard = %v", err)
		}
		if _, err := database.worker.Exec(t.Context(), `SELECT document FROM source_inspections WHERE id=$1`, usedInspection.ID); err == nil {
			t.Fatal("worker role could read private inspection evidence")
		} else {
			requireInsufficientPrivilege(t, err)
		}
	})

	t.Run("inspection expiry lock race", func(t *testing.T) {
		// Establish the API transaction before the short expiry window.
		// Connection setup is not part of the row-lock scenario.
		useTx, err := database.api.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// Release the borrowed connection even if a timing assertion fails.
		// Pool cleanup otherwise waits forever for this transaction.
		defer func() { _ = useTx.Rollback(context.Background()) }()
		if err := setTenantContext(t.Context(), useTx, housekeepingTenantA); err != nil {
			t.Fatal(err)
		}
		var created time.Time
		if err := database.operator.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&created); err != nil {
			t.Fatal(err)
		}
		inspection := housekeepingInspection("00000000-0000-7000-8000-000000000814", housekeepingTenantA, created, created.Add(250*time.Millisecond))
		if _, err := database.federation.SaveInspection(t.Context(), inspection); err != nil {
			t.Fatal(err)
		}
		var current bool
		if err := useTx.QueryRow(t.Context(), `SELECT expires_at>clock_timestamp() FROM source_inspections WHERE tenant_id=$1 AND id=$2 FOR KEY SHARE`, housekeepingTenantA, inspection.ID).Scan(&current); err != nil || !current {
			t.Fatalf("inspection use lock = %t, %v", current, err)
		}
		waitForInspectionExpiry(t, database.operator, inspection.ID)
		result, err := database.housekeeper.ReclaimUnusedSourceInspections(t.Context(), 1)
		if err != nil || result.Reclaimed != 0 {
			t.Fatalf("locked inspection cleanup = %#v, %v", result, err)
		}
		if err := useTx.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		result, err = database.housekeeper.ReclaimUnusedSourceInspections(t.Context(), 1)
		if err != nil || result.Reclaimed != 1 {
			t.Fatalf("released inspection cleanup = %#v, %v", result, err)
		}
	})

	t.Run("source coordination", func(t *testing.T) {
		empty, err := database.housekeeper.ReclaimObsoleteSourceCoordination(t.Context(), 1)
		if err != nil || empty != (housekeeping.Result{}) {
			t.Fatalf("empty single-row coordination cleanup = %#v, %v", empty, err)
		}
		cleanSource := createRevisedHousekeepingSource(t, database, "00000000-0000-7000-8000-000000000822", now.Add(time.Minute))
		blockedSource := createRevisedHousekeepingSource(t, database, "00000000-0000-7000-8000-000000000823", now.Add(2*time.Minute))
		staleSync := housekeepingJob("00000000-0000-7000-8000-000000000843", "", now.Add(3*time.Minute))
		staleSync.Kind = "source.sync"
		staleSync.PayloadVersion = 2
		staleSync.Payload = json.RawMessage(`{"sourceId":"` + cleanSource.ID + `","sourceRevision":1,"trigger":"manual"}`)
		if _, _, err := database.apiJobs.Enqueue(t.Context(), staleSync); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
			t.Fatalf("stale source synchronization enqueue returned %v", err)
		}
		insertHousekeepingSourceState(t, database, cleanSource, 1, now.Add(-time.Hour))
		insertHousekeepingSourceState(t, database, cleanSource, 2, now)
		insertHousekeepingSourceState(t, database, blockedSource, 1, now.Add(-time.Hour))
		blockerID := "00000000-0000-7000-8000-000000000841"
		insertHousekeepingSourceJob(t, database, blockerID, blockedSource, `{"sourceId":"`+blockedSource.ID+`","sourceRevision":1,"trigger":"automatic"}`)
		result, err := database.housekeeper.ReclaimObsoleteSourceCoordination(t.Context(), 20)
		if err != nil || result.Reclaimed != 2 {
			t.Fatalf("source coordination cleanup = %#v, %v", result, err)
		}
		assertHousekeepingSourceStateCount(t, database, cleanSource, 1, 0)
		assertHousekeepingSourceStateCount(t, database, cleanSource, 2, 2)
		assertHousekeepingSourceStateCount(t, database, blockedSource, 1, 2)
		if _, err := database.apiJobs.RequestCancellation(t.Context(), housekeepingTenantA, blockerID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		result, err = database.housekeeper.ReclaimObsoleteSourceCoordination(t.Context(), 20)
		if err != nil || result.Reclaimed != 2 {
			t.Fatalf("unblocked source coordination cleanup = %#v, %v", result, err)
		}

		malformedSource := createRevisedHousekeepingSource(t, database, "00000000-0000-7000-8000-000000000824", now.Add(3*time.Minute))
		insertHousekeepingSourceState(t, database, malformedSource, 1, now.Add(-time.Hour))
		malformedID := "00000000-0000-7000-8000-000000000842"
		if _, err := database.operator.Exec(t.Context(), `INSERT INTO jobs(id,tenant_id,kind,actor,payload_version,payload,state,priority,progress,not_before,attempt,attempt_limit,deduplication_key,created_at,updated_at)
VALUES ($1,$2,'source.sync','housekeeping-test',2,'{}','queued',0,0,clock_timestamp(),0,1,$3,clock_timestamp(),clock_timestamp())`, malformedID, malformedSource.TenantID, malformedID); err == nil {
			t.Fatal("database accepted a source synchronization Job without current provenance")
		} else {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "jobs_source_sync_current_payload" {
				t.Fatalf("unexpected malformed Job refusal: %v", err)
			}
		}
		result, err = database.housekeeper.ReclaimObsoleteSourceCoordination(t.Context(), 20)
		if err != nil || result.Reclaimed != 2 {
			t.Fatalf("rejected Job left coordination blocked = %#v, %v", result, err)
		}
	})

	t.Run("terminal Job retention and leaf order", func(t *testing.T) {
		anchor := housekeepingJob("00000000-0000-7000-8000-000000000851", "", now.Add(4*time.Minute))
		if _, _, err := database.apiJobs.Enqueue(t.Context(), anchor); err != nil {
			t.Fatal(err)
		}
		if _, err := database.apiJobs.RequestCancellation(t.Context(), housekeepingTenantA, anchor.ID, anchor.UpdatedAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		marked, err := database.housekeeper.ReclaimTerminalJobs(t.Context(), time.Hour, 100)
		if err != nil || marked.Marked < 1 {
			t.Fatalf("long Job retention mark = %#v, %v", marked, err)
		}
		var anchorCleanup time.Time
		if err := database.operator.QueryRow(t.Context(), `SELECT cleanup_after FROM jobs WHERE id=$1`, anchor.ID).Scan(&anchorCleanup); err != nil {
			t.Fatal(err)
		}
		if _, err := database.worker.Exec(t.Context(), `DELETE FROM jobs WHERE id=$1`, anchor.ID); err == nil || !strings.Contains(err.Error(), "active, retained, or referenced") {
			t.Fatalf("retained Job delete guard = %v", err)
		}
		missingParent := housekeepingJob("00000000-0000-7000-8000-000000000855", "00000000-0000-7000-8000-000000000899", now.Add(5*time.Minute))
		if _, _, err := database.apiJobs.Enqueue(t.Context(), missingParent); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("missing retry parent enqueue returned %v", err)
		}
		invalidParent := housekeepingJob("00000000-0000-7000-8000-000000000856", anchor.ID, now.Add(5*time.Minute))
		if _, _, err := database.apiJobs.Enqueue(t.Context(), invalidParent); catalog.ErrorCodeOf(err) != catalog.CodeInvalidTransition {
			t.Fatalf("cancelled retry parent enqueue returned %v", err)
		}

		parent := housekeepingJob("00000000-0000-7000-8000-000000000852", "", now.Add(5*time.Minute))
		if _, _, err := database.apiJobs.Enqueue(t.Context(), parent); err != nil {
			t.Fatal(err)
		}
		claimed, err := database.workerJobs.Claim(t.Context(), "housekeeping-worker", parent.NotBefore.Add(time.Second), time.Minute)
		if err != nil || claimed.ID != parent.ID {
			t.Fatalf("claim cleanup parent = %#v, %v", claimed, err)
		}
		if _, err := database.workerJobs.Fail(t.Context(), housekeepingTenantA, parent.ID, "housekeeping-worker", claimed.UpdatedAt.Add(time.Second), jobs.FailureInternal, "test terminal state", time.Time{}); err != nil {
			t.Fatal(err)
		}
		child := housekeepingJob("00000000-0000-7000-8000-000000000853", parent.ID, now.Add(6*time.Minute))
		if _, _, err := database.apiJobs.Enqueue(t.Context(), child); err != nil {
			t.Fatal(err)
		}
		claimed, err = database.workerJobs.Claim(t.Context(), "housekeeping-worker", child.NotBefore.Add(time.Second), time.Minute)
		if err != nil || claimed.ID != child.ID {
			t.Fatalf("claim cleanup child = %#v, %v", claimed, err)
		}
		if _, err := database.workerJobs.Complete(t.Context(), housekeepingTenantA, child.ID, "housekeeping-worker", claimed.UpdatedAt.Add(time.Second), json.RawMessage(`{"id":"00000000-0000-7000-8000-000000000854","state":"complete"}`)); err != nil {
			t.Fatal(err)
		}
		result, err := database.housekeeper.ReclaimTerminalJobs(t.Context(), 10*time.Millisecond, 100)
		if err != nil || result.Marked != 2 || result.Reclaimed != 0 {
			t.Fatalf("short Job retention mark = %#v, %v", result, err)
		}
		time.Sleep(20 * time.Millisecond)
		result, err = database.housekeeper.ReclaimTerminalJobs(t.Context(), 10*time.Millisecond, 100)
		if err != nil || result.Reclaimed != 1 {
			t.Fatalf("child Job cleanup = %#v, %v", result, err)
		}
		var parentCount, childCount int
		if err := database.operator.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE id=$1),count(*) FILTER (WHERE id=$2) FROM jobs`, parent.ID, child.ID).Scan(&parentCount, &childCount); err != nil || parentCount != 1 || childCount != 0 {
			t.Fatalf("Job leaf state = parent %d child %d, %v", parentCount, childCount, err)
		}
		result, err = database.housekeeper.ReclaimTerminalJobs(t.Context(), 10*time.Millisecond, 100)
		if err != nil || result.Reclaimed != 1 {
			t.Fatalf("parent Job cleanup = %#v, %v", result, err)
		}
		var anchorCleanupAfter time.Time
		if err := database.operator.QueryRow(t.Context(), `SELECT cleanup_after FROM jobs WHERE id=$1`, anchor.ID).Scan(&anchorCleanupAfter); err != nil || !anchorCleanupAfter.Equal(anchorCleanup) {
			t.Fatalf("immutable Job cleanup marker = %s then %s, %v", anchorCleanup, anchorCleanupAfter, err)
		}
		if _, err := database.api.Exec(t.Context(), `SELECT cleanup_after FROM jobs WHERE id=$1`, anchor.ID); err == nil {
			t.Fatal("API role could read internal Job cleanup marker")
		} else {
			requireInsufficientPrivilege(t, err)
		}
	})

	t.Run("acknowledged outbox retention", func(t *testing.T) {
		insertHousekeepingOutboxFixtures(t, database, now.Add(-7*time.Minute))
		if _, err := database.worker.Exec(t.Context(), `UPDATE outbox_events SET cleanup_after=clock_timestamp()+interval '1 hour' WHERE id=$1`, "00000000-0000-7000-8000-000000000863"); err == nil || !strings.Contains(err.Error(), "delivery evidence remains retained") {
			t.Fatalf("delivery-referenced outbox marker guard = %v", err)
		}
		result, err := database.housekeeper.ReclaimAcknowledgedOutbox(t.Context(), 10*time.Millisecond, 100)
		if err != nil || result.Marked != 2 || result.Reclaimed != 0 {
			t.Fatalf("outbox retention mark = %#v, %v", result, err)
		}
		time.Sleep(20 * time.Millisecond)
		result, err = database.housekeeper.ReclaimAcknowledgedOutbox(t.Context(), 10*time.Millisecond, 100)
		if err != nil || result.Reclaimed != 2 {
			t.Fatalf("outbox cleanup = %#v, %v", result, err)
		}
		var delivered, pending int
		if err := database.operator.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE id=$1),count(*) FILTER (WHERE id=$2) FROM outbox_events`, "00000000-0000-7000-8000-000000000863", "00000000-0000-7000-8000-000000000864").Scan(&delivered, &pending); err != nil || delivered != 1 || pending != 1 {
			t.Fatalf("retained outbox = delivered %d pending %d, %v", delivered, pending, err)
		}
	})
}

func requireHousekeepingIndexes(t *testing.T, database *housekeepingDatabase) {
	t.Helper()
	type expectation struct {
		table      string
		definition []string
		predicate  bool
	}
	expected := map[string]expectation{
		"idempotency_housekeeping":              {table: "idempotency_keys", definition: []string{"(expires_at, tenant_id, reservation_id)", "protocol_version = 2", "atomic_response_status is not null", "released_at is not null"}, predicate: true},
		"source_change_previews_housekeeping":   {table: "source_change_previews", definition: []string{"(expires_at, tenant_id, id)"}},
		"source_inspections_housekeeping":       {table: "source_inspections", definition: []string{"(expires_at, tenant_id, id)"}},
		"source_retrieval_states_housekeeping":  {table: "source_retrieval_states", definition: []string{"(updated_at, tenant_id, source_id, source_revision, request_key)"}},
		"source_maintenance_state_housekeeping": {table: "source_maintenance_state", definition: []string{"(last_admitted_at, tenant_id, source_id, source_revision)"}},
		"jobs_housekeeping_mark":                {table: "jobs", definition: []string{"(updated_at, tenant_id, id)", "cleanup_after is null", "state", "succeeded", "failed", "cancelled", "dead_letter"}, predicate: true},
		"jobs_housekeeping_delete":              {table: "jobs", definition: []string{"(cleanup_after, tenant_id, id)", "cleanup_after is not null"}, predicate: true},
		"outbox_events_housekeeping_mark":       {table: "outbox_events", definition: []string{"(published_at, tenant_id, id)", "published_at is not null", "cleanup_after is null"}, predicate: true},
		"outbox_events_housekeeping_delete":     {table: "outbox_events", definition: []string{"(cleanup_after, tenant_id, id)", "cleanup_after is not null"}, predicate: true},
		"jobs_live_source_revision":             {table: "jobs", definition: []string{"tenant_id", "payload ->> 'sourceid'", "payload ->> 'sourcerevision'", "created_at", "kind = 'source.sync'", "queued", "running", "waiting"}, predicate: true},
	}
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	rows, err := database.operator.Query(t.Context(), `
SELECT index_record.relname,table_record.relname,index_state.indisvalid,index_state.indisready,index_state.indislive,
       access_method.amname,pg_catalog.pg_get_indexdef(index_state.indexrelid)
  FROM pg_catalog.pg_index index_state
  JOIN pg_catalog.pg_class index_record ON index_record.oid=index_state.indexrelid
  JOIN pg_catalog.pg_class table_record ON table_record.oid=index_state.indrelid
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=index_record.relnamespace
  JOIN pg_catalog.pg_am access_method ON access_method.oid=index_record.relam
 WHERE namespace.nspname=current_schema() AND index_record.relname=ANY($1::text[])
 ORDER BY index_record.relname`, names)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := make(map[string]bool, len(expected))
	for rows.Next() {
		var name, table, method, definition string
		var valid, ready, live bool
		if err := rows.Scan(&name, &table, &valid, &ready, &live, &method, &definition); err != nil {
			t.Fatal(err)
		}
		want, ok := expected[name]
		normalized := strings.ToLower(strings.Join(strings.Fields(definition), " "))
		if !ok || table != want.table || method != "btree" || !valid || !ready || !live || strings.Contains(normalized, " unique ") {
			t.Fatalf("housekeeping index %q identity = table %q method %q valid=%t ready=%t live=%t definition %q", name, table, method, valid, ready, live, normalized)
		}
		for _, fragment := range want.definition {
			if !strings.Contains(normalized, fragment) {
				t.Fatalf("housekeeping index %q omits %q: %s", name, fragment, normalized)
			}
		}
		if strings.Contains(normalized, " where ") != want.predicate {
			t.Fatalf("housekeeping index %q predicate shape = %s", name, normalized)
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(expected) {
		t.Fatalf("housekeeping index inventory = %v, want %v", seen, names)
	}
}

func housekeepingInspection(id, tenantID string, createdAt, expiresAt time.Time) federation.Inspection {
	digest := "sha256:" + strings.Repeat("a", 64)
	return federation.Inspection{ID: id, TenantID: tenantID, RequestedURL: "https://source.example/ard.json", ResolvedURL: "https://source.example/ard.json", SourceType: federation.SourceARDManifest, ProfileID: "ard-0.91-aa3e598", MediaType: "application/json", BodyDigest: digest, KindCounts: map[string]int{}, Publishers: []string{}, Tags: []string{}, Capabilities: map[string]bool{}, PolicyID: "housekeeping-policy", PolicyRevision: 1, PolicyDigest: digest, CreatedAt: createdAt, ExpiresAt: expiresAt}
}

func housekeepingSource(id, tenantID string, at time.Time) federation.Source {
	return federation.Source{ID: id, TenantID: tenantID, DisplayName: "Housekeeping " + id[len(id)-3:], Type: federation.SourceARDManifest, Endpoint: "https://" + id[len(id)-3:] + ".source.example/ard.json", Status: federation.SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: federation.RetrievalIndexed, UpdateMode: federation.UpdateAutomatic, Live: federation.LivePolicy{FederationMode: "none"}, Referrals: federation.ReferralPolicy{}, Nesting: federation.NestingPolicy{}, PolicyID: "housekeeping-policy", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("a", 64), Revision: 1, CreatedBy: "housekeeping-test", CreatedAt: at, UpdatedAt: at}
}

func createRevisedHousekeepingSource(t *testing.T, database *housekeepingDatabase, id string, at time.Time) federation.Source {
	t.Helper()
	source := housekeepingSource(id, housekeepingTenantA, at)
	if _, err := database.federation.CreateSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	source.Revision = 2
	source.DisplayName += " revised"
	source.UpdatedAt = at.Add(time.Second)
	updated, err := database.federation.UpdateSource(t.Context(), source, 1)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func housekeepingPreview(id string, source federation.Source, createdAt, expiresAt time.Time) federation.ChangePreview {
	return federation.ChangePreview{ID: id, TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, Intent: federation.ChangeUpdate, ConfigurationDigest: "sha256:" + strings.Repeat("b", 64), ObservationSetDigest: "sha256:" + strings.Repeat("c", 64), Digest: "sha256:" + strings.Repeat(id[len(id)-1:], 64), CreatedAt: createdAt, ExpiresAt: expiresAt}
}

func insertHousekeepingIdempotencyFixtures(t *testing.T, database *housekeepingDatabase, now time.Time) {
	t.Helper()
	for index, values := range []struct {
		tenantID string
		key      string
		status   *int
		expires  time.Time
	}{
		{housekeepingTenantA, "expired-current-a", pointerTo(200), now.Add(-2 * time.Hour)},
		{housekeepingTenantB, "expired-current-b", pointerTo(201), now.Add(-time.Hour)},
		{housekeepingTenantA, "pending-current", nil, now.Add(-time.Hour)},
	} {
		if _, err := database.operator.Exec(t.Context(), `
INSERT INTO idempotency_keys(tenant_id,actor,route,idempotency_key,request_digest,atomic_response_status,expires_at,created_at,protocol_version,authorization_digest,execution_token,started_at,completed_at)
VALUES ($1,'housekeeping','POST /test',$2,'sha256:test',$3,$4,$5::timestamptz,2,'sha256:'||repeat('d',64),uuidv7(),$5::timestamptz,CASE WHEN $3::integer IS NULL THEN NULL ELSE $5::timestamptz END)`, values.tenantID, values.key, values.status, values.expires, now.Add(time.Duration(index)*time.Microsecond)); err != nil {
			t.Fatal(err)
		}
	}
	authorization := "sha256:" + strings.Repeat("d", 64)
	for index, values := range []struct {
		key      string
		expires  time.Time
		status   *int
		released bool
	}{
		{"expired-v2", now.Add(-time.Hour), pointerTo(200), false},
		{"expired-released-v2", now.Add(-time.Hour), nil, true},
		{"pending-v2", now.Add(-time.Hour), nil, false},
		{"unexpired-v2", now.Add(time.Hour), pointerTo(200), false},
	} {
		reservation := fmt.Sprintf("00000000-0000-7000-8000-%012d", 870+index)
		var executionToken any = reservation
		var releasedAt, repairReason any
		if values.released {
			executionToken = nil
			releasedAt = now
			repairReason = "reviewed test release"
		}
		var completedAt any
		if values.status != nil {
			completedAt = now
		}
		if _, err := database.operator.Exec(t.Context(), `
INSERT INTO idempotency_keys(
  tenant_id,actor,route,idempotency_key,request_digest,expires_at,created_at,reservation_id,protocol_version,
  authorization_digest,execution_token,started_at,completed_at,atomic_response_status,released_at,repair_reason)
VALUES ($1,'housekeeping','POST /test',$2,'sha256:test',$3,$4,$5,2,$6,$7,$4,$8,$9,$10,$11)`, housekeepingTenantA, values.key, values.expires, now.Add(time.Duration(index+10)*time.Microsecond), reservation, authorization, executionToken, completedAt, values.status, releasedAt, repairReason); err != nil {
			t.Fatal(err)
		}
	}
}

func insertHousekeepingSourceState(t *testing.T, database *housekeepingDatabase, source federation.Source, revision int64, at time.Time) {
	t.Helper()
	body := []byte(fmt.Sprintf("%s:%d", source.ID, revision))
	representation := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	request := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("request:"+source.ID+fmt.Sprint(revision))))
	vary := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("vary:"+source.ID+fmt.Sprint(revision))))
	if _, err := database.operator.Exec(t.Context(), `
INSERT INTO source_retrieval_documents(tenant_id,source_id,source_revision,representation_digest,body,created_at)
VALUES ($1,$2,$3,$4,$5,$6)`, source.TenantID, source.ID, revision, representation, body, at); err != nil {
		t.Fatal(err)
	}
	if _, err := database.operator.Exec(t.Context(), `
INSERT INTO source_retrieval_states(
  tenant_id,source_id,source_revision,request_key,requested_url,final_url,request_identity_digest,profile_id,media_type,
  policy_digest,representation_digest,etag,vary_selection_digest,updated_at)
VALUES ($1,$2,$3,$4,$5,$5,$4,'ard-0.91-aa3e598','application/json',$6,$7,'"test"',$8,$9)`, source.TenantID, source.ID, revision, request, source.Endpoint, source.PolicyDigest, representation, vary, at); err != nil {
		t.Fatal(err)
	}
	if _, err := database.operator.Exec(t.Context(), `INSERT INTO source_maintenance_state(tenant_id,source_id,source_revision,last_admitted_at) VALUES ($1,$2,$3,$4)`, source.TenantID, source.ID, revision, at); err != nil {
		t.Fatal(err)
	}
}

func assertHousekeepingSourceStateCount(t *testing.T, database *housekeepingDatabase, source federation.Source, revision int64, want int) {
	t.Helper()
	var count int
	if err := database.operator.QueryRow(t.Context(), `
SELECT (SELECT count(*) FROM source_retrieval_states WHERE tenant_id=$1 AND source_id=$2 AND source_revision=$3)
     + (SELECT count(*) FROM source_maintenance_state WHERE tenant_id=$1 AND source_id=$2 AND source_revision=$3)`, source.TenantID, source.ID, revision).Scan(&count); err != nil || count != want {
		t.Fatalf("source %s revision %d coordination rows = %d, want %d: %v", source.ID, revision, count, want, err)
	}
}

func insertHousekeepingSourceJob(t *testing.T, database *housekeepingDatabase, id string, source federation.Source, payload string) {
	t.Helper()
	if _, err := database.operator.Exec(t.Context(), `
INSERT INTO jobs(id,tenant_id,kind,actor,payload_version,payload,state,priority,progress,not_before,attempt,attempt_limit,deduplication_key,created_at,updated_at)
VALUES ($1,$2,'source.sync','housekeeping-test',2,$3,'queued',0,0,clock_timestamp()-interval '1 second',0,1,$4,clock_timestamp()-interval '2 seconds',clock_timestamp()-interval '2 seconds')`, id, source.TenantID, payload, id); err != nil {
		t.Fatal(err)
	}
}

func housekeepingJob(id, parentID string, at time.Time) jobs.Job {
	return jobs.Job{ID: id, TenantID: housekeepingTenantA, Kind: "housekeeping.test", Actor: "housekeeping-test", ParentJobID: parentID, PayloadVersion: 1, Payload: json.RawMessage(`{}`), State: jobs.StateQueued, NotBefore: at, AttemptLimit: 1, DeduplicationKey: id, CreatedAt: at, UpdatedAt: at}
}

func insertHousekeepingOutboxFixtures(t *testing.T, database *housekeepingDatabase, at time.Time) {
	t.Helper()
	for _, values := range []struct {
		id        string
		published bool
	}{
		{"00000000-0000-7000-8000-000000000861", true},
		{"00000000-0000-7000-8000-000000000862", true},
		{"00000000-0000-7000-8000-000000000863", true},
		{"00000000-0000-7000-8000-000000000864", false},
	} {
		var publishedAt any
		if values.published {
			publishedAt = at
		}
		if _, err := database.operator.Exec(t.Context(), `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at,published_at) VALUES ($1,$2,'com.open.agentic.catalog.resource.created.v1',$5,jsonb_build_object('action','resource.created','targetType','resource','targetId',$5::text),$3,$4)`, values.id, housekeepingTenantA, at, publishedAt, values.id); err != nil {
			t.Fatal(err)
		}
	}
	endpointID := "00000000-0000-7000-8000-000000000865"
	if _, err := database.operator.Exec(t.Context(), `
INSERT INTO webhook_endpoints(id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at)
VALUES ($1,$2,'retained delivery','https://webhook.example/events',ARRAY['com.open.agentic.catalog.resource.created.v1'],'active',1,1,'secret','housekeeping-test',$3,$3)`, endpointID, housekeepingTenantA, at); err != nil {
		t.Fatal(err)
	}
	if _, err := database.operator.Exec(t.Context(), `
INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,event_id,event_type,state,attempt,safe_error,created_at,updated_at)
VALUES ($1,$2,$3,$4,'com.open.agentic.catalog.resource.created.v1','pending',0,'',$5,$5)`, "00000000-0000-7000-8000-000000000866", housekeepingTenantA, endpointID, "00000000-0000-7000-8000-000000000863", at); err != nil {
		t.Fatal(err)
	}
}

func pointerTo[T any](value T) *T { return &value }
