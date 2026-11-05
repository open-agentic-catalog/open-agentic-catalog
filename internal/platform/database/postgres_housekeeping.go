// Housekeeping uses bounded worker selection, database-owned eligibility and row
// locks with explicit tenant predicates on follow-up mutations.

package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
)

// PostgresHousekeepingStore owns the worker's bounded cross-tenant cleanup
// transactions. Every method selects only logical identities and aggregate
// timing, leaving private row content outside the worker result boundary.
type PostgresHousekeepingStore struct{ repository *PostgresRepository }

// NewPostgresHousekeepingStore wraps a nonnil repository for worker-only bounded
// cleanup.
func NewPostgresHousekeepingStore(repository *PostgresRepository) (*PostgresHousekeepingStore, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL repository is required")
	}
	return &PostgresHousekeepingStore{repository: repository}, nil
}

// ReclaimExpiredIdempotency deletes a locked bounded page of expired completed or
// released protocol-v2 reservations, preserving pending commands.
func (p *PostgresHousekeepingStore) ReclaimExpiredIdempotency(ctx context.Context, limit int) (housekeeping.Result, error) {
	if err := validHousekeepingLimit(limit); err != nil {
		return housekeeping.Result{}, err
	}
	return withHousekeepingTx(ctx, p.repository.pool, func(tx pgx.Tx) (housekeeping.Result, error) {
		return scanHousekeepingResult(tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT tenant_id,reservation_id,expires_at
    FROM idempotency_keys
   WHERE expires_at<=clock_timestamp() AND (
     protocol_version=2 AND (atomic_response_status IS NOT NULL OR released_at IS NOT NULL)
   )
   ORDER BY expires_at,tenant_id,reservation_id
   FOR UPDATE SKIP LOCKED LIMIT $1
), deleted AS (
  DELETE FROM idempotency_keys key USING candidate
   WHERE key.tenant_id=candidate.tenant_id AND key.reservation_id=candidate.reservation_id
     AND key.expires_at<=clock_timestamp() AND (
       key.protocol_version=2 AND (key.atomic_response_status IS NOT NULL OR key.released_at IS NOT NULL)
     )
   RETURNING key.expires_at
)
SELECT count(*),0::bigint,count(*)=$1,
       coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(expires_at))),0)
  FROM deleted`, limit), "reclaim expired idempotency rows")
	})
}

// ReclaimExpiredSourcePreviews rechecks expiry while deleting a bounded
// skip-locked page with explicit tenant predicates.
func (p *PostgresHousekeepingStore) ReclaimExpiredSourcePreviews(ctx context.Context, limit int) (housekeeping.Result, error) {
	if err := validHousekeepingLimit(limit); err != nil {
		return housekeeping.Result{}, err
	}
	return withHousekeepingTx(ctx, p.repository.pool, func(tx pgx.Tx) (housekeeping.Result, error) {
		return scanHousekeepingResult(tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT tenant_id,id,expires_at
    FROM source_change_previews
   WHERE expires_at<=clock_timestamp()
   ORDER BY expires_at,tenant_id,id
   FOR UPDATE SKIP LOCKED LIMIT $1
), deleted AS (
  DELETE FROM source_change_previews preview USING candidate
   WHERE preview.tenant_id=candidate.tenant_id AND preview.id=candidate.id
     AND preview.expires_at<=clock_timestamp()
   RETURNING preview.expires_at
)
SELECT count(*),0::bigint,count(*)=$1,
       coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(expires_at))),0)
  FROM deleted`, limit), "reclaim expired source previews")
	})
}

// ReclaimUnusedSourceInspections removes expired inspections only when no retained
// source revision references them, rechecking eligibility at deletion.
func (p *PostgresHousekeepingStore) ReclaimUnusedSourceInspections(ctx context.Context, limit int) (housekeeping.Result, error) {
	if err := validHousekeepingLimit(limit); err != nil {
		return housekeeping.Result{}, err
	}
	return withHousekeepingTx(ctx, p.repository.pool, func(tx pgx.Tx) (housekeeping.Result, error) {
		return scanHousekeepingResult(tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT inspection.tenant_id,inspection.id,inspection.expires_at
    FROM source_inspections inspection
   WHERE inspection.expires_at<=clock_timestamp()
     AND NOT EXISTS (
       SELECT 1 FROM federation_source_revisions revision
        WHERE revision.tenant_id=inspection.tenant_id AND revision.inspection_id=inspection.id
     )
   ORDER BY inspection.expires_at,inspection.tenant_id,inspection.id
   FOR UPDATE OF inspection SKIP LOCKED LIMIT $1
), deleted AS (
  DELETE FROM source_inspections inspection USING candidate
   WHERE inspection.tenant_id=candidate.tenant_id AND inspection.id=candidate.id
     AND inspection.expires_at<=clock_timestamp()
     AND NOT EXISTS (
       SELECT 1 FROM federation_source_revisions revision
        WHERE revision.tenant_id=inspection.tenant_id AND revision.inspection_id=inspection.id
     )
   RETURNING inspection.expires_at
)
SELECT count(*),0::bigint,count(*)=$1,
       coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(expires_at))),0)
  FROM deleted`, limit), "reclaim unused source inspections")
	})
}

// ReclaimObsoleteSourceCoordination splits the batch allowance between obsolete
// retrieval validators and maintenance state, combining bounded progress.
func (p *PostgresHousekeepingStore) ReclaimObsoleteSourceCoordination(ctx context.Context, limit int) (housekeeping.Result, error) {
	if err := validHousekeepingLimit(limit); err != nil {
		return housekeeping.Result{}, err
	}
	return withHousekeepingTx(ctx, p.repository.pool, func(tx pgx.Tx) (housekeeping.Result, error) {
		return reclaimObsoleteSourceCoordinationTx(ctx, tx, limit)
	})
}

// reclaimObsoleteSourceCoordinationTx applies validated limits in the caller's bounded transaction.
// Progress is provisional until that transaction commits.
func reclaimObsoleteSourceCoordinationTx(ctx context.Context, tx pgx.Tx, limit int) (housekeeping.Result, error) {
	retrievalLimit := limit / 2
	maintenanceLimit := limit - retrievalLimit
	var retrieval housekeeping.Result
	// A zero-sized page has no work allowance and cannot report saturation.
	if retrievalLimit > 0 {
		var err error
		retrieval, err = scanHousekeepingResult(tx.QueryRow(ctx, sourceRetrievalCleanupSQL, retrievalLimit), "reclaim obsolete source retrieval state")
		if err != nil {
			return housekeeping.Result{}, err
		}
	}
	maintenance, err := scanHousekeepingResult(tx.QueryRow(ctx, sourceMaintenanceCleanupSQL, maintenanceLimit), "reclaim obsolete source maintenance state")
	if err != nil {
		return housekeeping.Result{}, err
	}
	return housekeeping.Result{
		Reclaimed:         retrieval.Reclaimed + maintenance.Reclaimed,
		Saturated:         retrieval.Saturated || maintenance.Saturated,
		OldestEligibleAge: max(retrieval.OldestEligibleAge, maintenance.OldestEligibleAge),
	}, nil
}

// ReclaimTerminalJobs first assigns a cleanup horizon from the database clock,
// then reclaims separately eligible terminal work within bounded locked
// selections.
func (p *PostgresHousekeepingStore) ReclaimTerminalJobs(ctx context.Context, retention time.Duration, limit int) (housekeeping.Result, error) {
	if err := validHousekeepingTiming(retention, limit); err != nil {
		return housekeeping.Result{}, err
	}
	return withHousekeepingTx(ctx, p.repository.pool, func(tx pgx.Tx) (housekeeping.Result, error) {
		return reclaimTerminalJobsTx(ctx, tx, retention, limit)
	})
}

// reclaimTerminalJobsTx applies validated limits in the caller's bounded transaction.
// Progress is provisional until that transaction commits.
func reclaimTerminalJobsTx(ctx context.Context, tx pgx.Tx, retention time.Duration, limit int) (housekeeping.Result, error) {
	var marked int64
	var markedOldestSeconds float64
	var semanticInstalled bool
	if err := tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT tenant_id,id
    FROM jobs
   WHERE state IN ('succeeded','failed','cancelled','dead_letter') AND cleanup_after IS NULL
   ORDER BY updated_at,tenant_id,id
   FOR UPDATE SKIP LOCKED LIMIT $1
), marked AS (
  UPDATE jobs job SET cleanup_after=clock_timestamp()+$2::interval
    FROM candidate
   WHERE job.tenant_id=candidate.tenant_id AND job.id=candidate.id
     AND job.state IN ('succeeded','failed','cancelled','dead_letter') AND job.cleanup_after IS NULL
   RETURNING job.updated_at
)
SELECT count(*),coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(updated_at))),0),
       to_regclass(format('%I.semantic_generations',current_schema())) IS NOT NULL
  FROM marked`, limit, retention.String()).Scan(&marked, &markedOldestSeconds, &semanticInstalled); err != nil {
		return housekeeping.Result{}, translateError("mark terminal jobs for cleanup", err)
	}
	// A completed generation retains its Job identity until derived cleanup
	// releases it. Do not let one reference abort unrelated terminal cleanup.
	// Resolve optional presence in the existing marking query so core-only
	// installations never parse a reference to an absent table.
	semanticReference := ""
	if semanticInstalled {
		semanticReference = ` AND NOT EXISTS (SELECT 1 FROM semantic_generations generation WHERE generation.tenant_id=job.tenant_id AND generation.id=job.id) `
	}
	deleted, err := scanHousekeepingResult(tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT job.tenant_id,job.id,job.cleanup_after
    FROM jobs job
   WHERE job.state IN ('succeeded','failed','cancelled','dead_letter')
     AND job.cleanup_after<=clock_timestamp()
     AND job.lease_owner IS NULL AND job.lease_expires_at IS NULL `+semanticReference+`
     AND NOT EXISTS (
       SELECT 1 FROM jobs child
        WHERE child.tenant_id=job.tenant_id AND child.parent_job_id=job.id
     )
   ORDER BY job.cleanup_after,job.tenant_id,job.id
   FOR UPDATE OF job SKIP LOCKED LIMIT $1
), deleted AS (
  DELETE FROM jobs job USING candidate
   WHERE job.tenant_id=candidate.tenant_id AND job.id=candidate.id
     AND job.state IN ('succeeded','failed','cancelled','dead_letter')
     AND job.cleanup_after<=clock_timestamp()
     AND job.lease_owner IS NULL AND job.lease_expires_at IS NULL `+semanticReference+`
     AND NOT EXISTS (
       SELECT 1 FROM jobs child
        WHERE child.tenant_id=job.tenant_id AND child.parent_job_id=job.id
     )
   RETURNING job.cleanup_after
)
SELECT count(*),0::bigint,count(*)=$1,
       coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(cleanup_after))),0)
  FROM deleted`, limit), "reclaim terminal jobs")
	if err != nil {
		return housekeeping.Result{}, err
	}
	return combineHousekeepingProgress(deleted, marked, markedOldestSeconds, limit), nil
}

// ReclaimAcknowledgedOutbox marks and removes eligible acknowledged, unleased
// events only when no webhook delivery still references them.
func (p *PostgresHousekeepingStore) ReclaimAcknowledgedOutbox(ctx context.Context, retention time.Duration, limit int) (housekeeping.Result, error) {
	if err := validHousekeepingTiming(retention, limit); err != nil {
		return housekeeping.Result{}, err
	}
	return withHousekeepingTx(ctx, p.repository.pool, func(tx pgx.Tx) (housekeeping.Result, error) {
		return reclaimAcknowledgedOutboxTx(ctx, tx, retention, limit)
	})
}

// reclaimAcknowledgedOutboxTx applies validated limits in the caller's bounded transaction.
// Progress is provisional until that transaction commits.
func reclaimAcknowledgedOutboxTx(ctx context.Context, tx pgx.Tx, retention time.Duration, limit int) (housekeeping.Result, error) {
	var marked int64
	var markedOldestSeconds float64
	if err := tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT event.tenant_id,event.id
    FROM outbox_events event
   WHERE event.published_at IS NOT NULL AND event.cleanup_after IS NULL
     AND event.lease_owner IS NULL AND event.lease_expires_at IS NULL
     AND NOT EXISTS (
       SELECT 1 FROM webhook_deliveries delivery
        WHERE delivery.tenant_id=event.tenant_id AND delivery.event_id=event.id
     )
   ORDER BY event.published_at,event.tenant_id,event.id
   FOR UPDATE OF event SKIP LOCKED LIMIT $1
), marked AS (
  UPDATE outbox_events event SET cleanup_after=clock_timestamp()+$2::interval
    FROM candidate
   WHERE event.tenant_id=candidate.tenant_id AND event.id=candidate.id
     AND event.published_at IS NOT NULL AND event.cleanup_after IS NULL
     AND event.lease_owner IS NULL AND event.lease_expires_at IS NULL
     AND NOT EXISTS (
       SELECT 1 FROM webhook_deliveries delivery
        WHERE delivery.tenant_id=event.tenant_id AND delivery.event_id=event.id
     )
   RETURNING event.published_at
)
SELECT count(*),coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(published_at))),0)
  FROM marked`, limit, retention.String()).Scan(&marked, &markedOldestSeconds); err != nil {
		return housekeeping.Result{}, translateError("mark acknowledged outbox events for cleanup", err)
	}
	deleted, err := scanHousekeepingResult(tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT event.tenant_id,event.id,event.cleanup_after
    FROM outbox_events event
   WHERE event.published_at IS NOT NULL AND event.cleanup_after<=clock_timestamp()
     AND event.lease_owner IS NULL AND event.lease_expires_at IS NULL
     AND NOT EXISTS (
       SELECT 1 FROM webhook_deliveries delivery
        WHERE delivery.tenant_id=event.tenant_id AND delivery.event_id=event.id
     )
   ORDER BY event.cleanup_after,event.tenant_id,event.id
   FOR UPDATE OF event SKIP LOCKED LIMIT $1
), deleted AS (
  DELETE FROM outbox_events event USING candidate
   WHERE event.tenant_id=candidate.tenant_id AND event.id=candidate.id
     AND event.published_at IS NOT NULL AND event.cleanup_after<=clock_timestamp()
     AND event.lease_owner IS NULL AND event.lease_expires_at IS NULL
     AND NOT EXISTS (
       SELECT 1 FROM webhook_deliveries delivery
        WHERE delivery.tenant_id=event.tenant_id AND delivery.event_id=event.id
     )
   RETURNING event.cleanup_after
)
SELECT count(*),0::bigint,count(*)=$1,
       coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(cleanup_after))),0)
  FROM deleted`, limit), "reclaim acknowledged outbox events")
	if err != nil {
		return housekeeping.Result{}, err
	}
	return combineHousekeepingProgress(deleted, marked, markedOldestSeconds, limit), nil
}

const sourceSyncCleanupBlocker = `
AND NOT EXISTS (
  SELECT 1 FROM jobs job
   WHERE job.tenant_id=state.tenant_id AND job.kind='source.sync'
     AND job.state IN ('queued','running','waiting')
     AND CASE
       WHEN job.payload_version=2
        AND jsonb_typeof(job.payload)='object'
        AND job.payload-ARRAY['sourceId','sourceRevision','trigger']='{}'::jsonb
        AND jsonb_typeof(job.payload->'sourceId')='string'
        AND coalesce(job.payload->>'sourceId','') ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND jsonb_typeof(job.payload->'sourceRevision')='number'
        AND coalesce(job.payload->>'sourceRevision','') ~ '^[1-9][0-9]{0,17}$'
        AND jsonb_typeof(job.payload->'trigger')='string'
        AND job.payload->>'trigger' IN ('manual','automatic')
       THEN job.payload->>'sourceId'=state.source_id::text
         AND (job.payload->>'sourceRevision')::bigint=state.source_revision
       ELSE true
     END
)`

const sourceRetrievalCleanupSQL = `
WITH candidate AS (
  SELECT state.tenant_id,state.source_id,state.source_revision,state.request_key,state.updated_at
    FROM source_retrieval_states state
    JOIN federation_sources source
      ON source.tenant_id=state.tenant_id AND source.id=state.source_id
   WHERE (source.status='removed' OR source.revision<>state.source_revision)
` + sourceSyncCleanupBlocker + `
   ORDER BY state.updated_at,state.tenant_id,state.source_id,state.source_revision,state.request_key
   FOR UPDATE OF state,source SKIP LOCKED LIMIT $1
), deleted AS (
  DELETE FROM source_retrieval_states state USING candidate
   WHERE state.tenant_id=candidate.tenant_id AND state.source_id=candidate.source_id
     AND state.source_revision=candidate.source_revision AND state.request_key=candidate.request_key
   RETURNING state.updated_at
)
SELECT count(*),0::bigint,count(*)=$1,
       coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(updated_at))),0)
  FROM deleted`

const sourceMaintenanceCleanupSQL = `
WITH candidate AS (
  SELECT state.tenant_id,state.source_id,state.source_revision,state.last_admitted_at
    FROM source_maintenance_state state
    JOIN federation_sources source
      ON source.tenant_id=state.tenant_id AND source.id=state.source_id
   WHERE (source.status='removed' OR source.revision<>state.source_revision)
` + sourceSyncCleanupBlocker + `
   ORDER BY state.last_admitted_at,state.tenant_id,state.source_id,state.source_revision
   FOR UPDATE OF state,source SKIP LOCKED LIMIT $1
), deleted AS (
  DELETE FROM source_maintenance_state state USING candidate
   WHERE state.tenant_id=candidate.tenant_id AND state.source_id=candidate.source_id
     AND state.source_revision=candidate.source_revision
   RETURNING state.last_admitted_at
)
SELECT count(*),0::bigint,count(*)=$1,
       coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(last_admitted_at))),0)
  FROM deleted`

// validHousekeepingLimit requires a positive batch no larger than the worker's
// fixed ceiling.
func validHousekeepingLimit(limit int) error {
	if limit < 1 || limit > housekeeping.BatchLimit {
		return catalog.E(catalog.CodeInvalidArgument, "housekeeping batch limit is invalid")
	}
	return nil
}

// validHousekeepingTiming validates the batch and requires positive retention
// before creating cleanup horizons.
func validHousekeepingTiming(retention time.Duration, limit int) error {
	if err := validHousekeepingLimit(limit); err != nil {
		return err
	}
	if retention <= 0 {
		return catalog.E(catalog.CodeInvalidArgument, "housekeeping retention is invalid")
	}
	return nil
}

// withHousekeepingTx bounds every worker cleanup statement and lock wait,
// committing only a successful class operation.
func withHousekeepingTx(ctx context.Context, connection interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}, operation func(pgx.Tx) (housekeeping.Result, error)) (housekeeping.Result, error) {
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return housekeeping.Result{}, translateError("begin housekeeping transaction", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '2s'`); err != nil {
		return housekeeping.Result{}, translateError("bound housekeeping statement time", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '250ms'`); err != nil {
		return housekeeping.Result{}, translateError("bound housekeeping lock time", err)
	}
	result, err := operation(tx)
	if err != nil {
		return housekeeping.Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return housekeeping.Result{}, translateError("commit housekeeping transaction", err)
	}
	return result, nil
}

// combineHousekeepingProgress merges successful marking and deletion projections
// under a validated limit. Either page can saturate independently; age reflects
// the oldest selected row across both pages, not a whole-table backlog.
func combineHousekeepingProgress(deleted housekeeping.Result, marked int64, markedOldestSeconds float64, limit int) housekeeping.Result {
	deleted.Marked = marked
	deleted.Saturated = deleted.Saturated || marked == int64(limit)
	deleted.OldestEligibleAge = max(deleted.OldestEligibleAge, boundedHousekeepingAge(markedOldestSeconds))
	return deleted
}

// scanHousekeepingResult decodes aggregate progress and safely converts the oldest
// eligible age without exposing private row contents.
func scanHousekeepingResult(row pgx.Row, operation string) (housekeeping.Result, error) {
	var result housekeeping.Result
	var oldestSeconds float64
	if err := row.Scan(&result.Reclaimed, &result.Marked, &result.Saturated, &oldestSeconds); err != nil {
		return housekeeping.Result{}, translateError(operation, err)
	}
	result.OldestEligibleAge = boundedHousekeepingAge(oldestSeconds)
	return result, nil
}

// boundedHousekeepingAge clamps negative ages to zero and prevents duration
// overflow from large database intervals.
func boundedHousekeepingAge(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	maximum := time.Duration(1<<63 - 1)
	if seconds >= maximum.Seconds() {
		return maximum
	}
	return time.Duration(seconds * float64(time.Second))
}
