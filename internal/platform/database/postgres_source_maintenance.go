// Automatic maintenance selects due approved source revisions with deterministic
// jitter and admits bounded work under a dedicated lock.

package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

const sourceMaintenanceAdmissionLock int64 = 880185172406

// PostgresSourceMaintenanceStore provides worker-only cross-tenant scheduling
// and transactional admission for approved automatic federation sources.
type PostgresSourceMaintenanceStore struct{ repository *PostgresRepository }

// NewPostgresSourceMaintenanceStore binds worker admission to an existing
// repository, rejecting nil without taking ownership of its pool.
func NewPostgresSourceMaintenanceStore(repository *PostgresRepository) (*PostgresSourceMaintenanceStore, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL repository is required")
	}
	return &PostgresSourceMaintenanceStore{repository: repository}, nil
}

const automaticSourceDueCTE = `
WITH policy_sources AS (
SELECT source.tenant_id,source.id,source.revision,source.status,source.source_type,coalesce(settings.automatic_source_sync_interval_ns/1000000000.0,extract(epoch FROM $1::interval)) AS interval_seconds
FROM federation_sources source LEFT JOIN tenant_settings settings ON settings.tenant_id=source.tenant_id
WHERE coalesce(settings.automatic_source_sync_enabled,true)
), eligible AS (
  SELECT source.tenant_id,source.id AS source_id,source.revision,revision.recorded_at,
         (SELECT max(snapshot.completed_at) FROM source_snapshots snapshot
           WHERE snapshot.tenant_id=source.tenant_id AND snapshot.source_id=source.id
             AND snapshot.source_revision=source.revision AND snapshot.state='complete' AND snapshot.enumeration_complete) AS last_success_at,
         maintenance.last_admitted_at,make_interval(secs=>source.interval_seconds::double precision) AS effective_interval,
         make_interval(secs => (
           (('x' || substr(md5(source.id::text || ':' || source.revision::text),1,8))::bit(32)::bigint
             % greatest(1,floor(source.interval_seconds*100)::bigint)) / 1000.0
         )) AS jitter
    FROM policy_sources source
    JOIN federation_source_revisions revision
      ON revision.tenant_id=source.tenant_id AND revision.source_id=source.id AND revision.revision=source.revision
    LEFT JOIN source_maintenance_state maintenance
      ON maintenance.tenant_id=source.tenant_id AND maintenance.source_id=source.id AND maintenance.source_revision=source.revision
   WHERE source.status IN ('active','error') AND revision.update_mode='automatic'
     AND revision.retrieval_mode IN ('indexed','hybrid') AND revision.policy_digest=$2
     AND (source.source_type<>'ard_registry' OR revision.registry_list_url<>'')
), due AS (
  SELECT tenant_id,source_id,revision,
         CASE WHEN last_success_at IS NULL AND last_admitted_at IS NULL
           THEN recorded_at+jitter
           ELSE greatest(coalesce(last_success_at,'-infinity'::timestamptz),coalesce(last_admitted_at,'-infinity'::timestamptz))+effective_interval+jitter
         END AS due_at
    FROM eligible
)
`

// ListDueAutomaticSources selects a SQL-limited cross-tenant worker batch using
// database time and deterministic revision jitter. Only currently approved
// automatic indexed or hybrid sources matching the policy are eligible; the
// supplied time is ignored.
func (p *PostgresSourceMaintenanceStore) ListDueAutomaticSources(ctx context.Context, _ time.Time, interval time.Duration, policy string, limit int) ([]federation.MaintenanceCandidate, error) {
	if limit < 1 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "automatic source selection limit must be positive")
	}
	return listDueAutomaticSources(ctx, p.repository.pool, interval, policy, limit)
}

// listDueAutomaticSources reads a validated bounded batch and closes its rows.
// Callers must check the error before using any candidates returned on iteration failure.
func listDueAutomaticSources(ctx context.Context, query interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, interval time.Duration, policy string, limit int) ([]federation.MaintenanceCandidate, error) {
	rows, err := query.Query(ctx, automaticSourceDueCTE+`
SELECT tenant_id::text,source_id::text,revision,due_at FROM due
 WHERE due_at<=clock_timestamp() ORDER BY due_at,tenant_id,source_id LIMIT $3`, interval.String(), policy, limit)
	if err != nil {
		return nil, translateError("list due automatic federation sources", err)
	}
	defer rows.Close()
	items := make([]federation.MaintenanceCandidate, 0)
	for rows.Next() {
		var item federation.MaintenanceCandidate
		if err := rows.Scan(&item.TenantID, &item.SourceID, &item.SourceRevision, &item.DueAt); err != nil {
			return nil, translateError("scan due automatic federation source", err)
		}
		items = append(items, item)
	}
	return items, translateError("iterate due automatic federation sources", rows.Err())
}

// AdmitAutomaticSync rechecks the source revision and due time under a global
// admission lock, enforces the live-job ceiling and atomically records admission.
// The boolean reports reuse of an existing deduplicated job.
func (p *PostgresSourceMaintenanceStore) AdmitAutomaticSync(ctx context.Context, candidate federation.MaintenanceCandidate, job jobs.Job, interval time.Duration, policy string, maxLive int) (jobs.Job, bool, error) {
	tx, err := p.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return jobs.Job{}, false, translateError("begin automatic source admission", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	if err := lockTenantSettings(ctx, tx, candidate.TenantID, true); err != nil {
		return jobs.Job{}, false, err
	}
	attempts, err := resolveNewJobAttempts(ctx, tx, candidate.TenantID, job.AttemptLimit)
	if err != nil {
		return jobs.Job{}, false, err
	}
	job.AttemptLimit = attempts
	job.DefaultAttemptLimit = false
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, sourceMaintenanceAdmissionLock); err != nil {
		return jobs.Job{}, false, translateError("lock automatic source admission", err)
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return jobs.Job{}, false, translateError("read automatic source admission time", err)
	}
	admitted, existing, err := admitAutomaticSyncTx(ctx, tx, candidate, job, interval, policy, maxLive, databaseNow)
	if err != nil {
		return jobs.Job{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return jobs.Job{}, false, translateError("commit automatic source admission", err)
	}
	return admitted, existing, nil
}

// admitAutomaticSyncTx rechecks and records admission under the caller's global
// admission lock, using its database timestamp. The result is provisional until
// the caller commits; this operation does not own transaction setup or cleanup.
func admitAutomaticSyncTx(ctx context.Context, tx pgx.Tx, candidate federation.MaintenanceCandidate, job jobs.Job, interval time.Duration, policy string, maxLive int, databaseNow time.Time) (jobs.Job, bool, error) {
	job.CreatedAt = databaseNow
	job.UpdatedAt = databaseNow
	job.NotBefore = databaseNow
	var locked int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM federation_sources WHERE tenant_id=$1 AND id=$2 AND revision=$3 FOR UPDATE`, candidate.TenantID, candidate.SourceID, candidate.SourceRevision).Scan(&locked); err != nil {
		return jobs.Job{}, false, translateError("lock automatic federation source", err)
	}
	var dueAt time.Time
	err := tx.QueryRow(ctx, automaticSourceDueCTE+`
SELECT due_at FROM due WHERE tenant_id=$3 AND source_id=$4 AND revision=$5 AND due_at<=clock_timestamp()`,
		interval.String(), policy, candidate.TenantID, candidate.SourceID, candidate.SourceRevision).Scan(&dueAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, false, catalog.E(catalog.CodePreconditionFailed, "automatic source is no longer due or eligible")
	}
	if err != nil {
		return jobs.Job{}, false, translateError("recheck automatic source admission", err)
	}
	var live int
	if err := tx.QueryRow(ctx, `
      SELECT count(*) FROM jobs
       WHERE kind='source.sync' AND payload_version=2 AND payload->>'trigger'='automatic'
         AND state IN ('queued','running','waiting')`).Scan(&live); err != nil {
		return jobs.Job{}, false, translateError("count live automatic source jobs", err)
	}
	if live >= maxLive {
		return jobs.Job{}, false, catalog.E(catalog.CodeConflict, "automatic source synchronization capacity is full")
	}
	var insertedID string
	err = tx.QueryRow(ctx, `
      INSERT INTO jobs(id,tenant_id,kind,actor,parent_job_id,payload_version,payload,state,priority,progress,not_before,attempt,attempt_limit,deduplication_key,created_at,updated_at)
      VALUES ($1,$2,$3,$4,nullif($5,'')::uuid,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
      ON CONFLICT (tenant_id,kind,deduplication_key) WHERE state IN ('queued','running','waiting') DO NOTHING
      RETURNING id::text`, job.ID, job.TenantID, job.Kind, job.Actor, job.ParentJobID, job.PayloadVersion, job.Payload, job.State, job.Priority, job.Progress, job.NotBefore, job.Attempt, job.AttemptLimit, job.DeduplicationKey, job.CreatedAt, job.UpdatedAt).Scan(&insertedID)
	existing := false
	admitted := job
	if errors.Is(err, pgx.ErrNoRows) {
		existing = true
		admitted, err = readJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE tenant_id=$1 AND kind=$2 AND deduplication_key=$3 AND state IN ('queued','running','waiting')`, job.TenantID, job.Kind, job.DeduplicationKey))
	}
	if err != nil {
		return jobs.Job{}, false, translateError("enqueue automatic source synchronization", err)
	}
	if _, err := tx.Exec(ctx, `
      INSERT INTO source_maintenance_state(tenant_id,source_id,source_revision,last_admitted_at)
      VALUES ($1,$2,$3,$4)
      ON CONFLICT (tenant_id,source_id,source_revision) DO UPDATE SET last_admitted_at=excluded.last_admitted_at`,
		candidate.TenantID, candidate.SourceID, candidate.SourceRevision, databaseNow); err != nil {
		return jobs.Job{}, false, translateError("record automatic source admission", err)
	}
	return admitted, existing, nil
}
