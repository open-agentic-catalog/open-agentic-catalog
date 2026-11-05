// Job persistence admits durable work, captures causal context and fences worker
// transitions by tenant identity and current lease ownership.

package database

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

type PostgresJobStore struct {
	repository       *PostgresRepository
	semanticProfile  string
	semanticExcluded []string
}

// NewPostgresSemanticJobStore applies deployment exclusions at claim selection,
// before a worker can read eligible metadata or call the embedding provider.
func NewPostgresSemanticJobStore(repository *PostgresRepository, profile string, excluded []string) (*PostgresJobStore, error) {
	store, err := NewPostgresJobStore(repository, profile)
	if err != nil {
		return nil, err
	}
	if len(excluded) > 256 {
		return nil, errors.New("too many semantic tenant exclusions")
	}
	for _, tenant := range excluded {
		if !identity.ValidTenantID(tenant) {
			return nil, errors.New("invalid semantic tenant exclusion")
		}
	}
	store.semanticExcluded = append([]string{}, excluded...)
	return store, nil
}

// NewPostgresJobStore binds durable job operations to a nonnil admitted
// repository. The optional profile admits only matching semantic Jobs; an
// omitted profile leaves semantic work untouched for a compatible worker.
func NewPostgresJobStore(repository *PostgresRepository, semanticProfile ...string) (*PostgresJobStore, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL repository is required")
	}
	profile := ""
	if len(semanticProfile) > 1 || len(semanticProfile) == 1 && semanticProfile[0] != "" && !validSemanticDigest(semanticProfile[0]) {
		return nil, errors.New("at most one valid semantic profile is permitted")
	}
	if len(semanticProfile) == 1 {
		profile = semanticProfile[0]
	}
	return &PostgresJobStore{repository: repository, semanticProfile: profile}, nil
}

// Enqueue validates retry/source parent state and atomically inserts a job with
// causal context or returns existing nonterminal deduplicated work; the boolean
// reports reuse.
func (p *PostgresJobStore) Enqueue(ctx context.Context, job jobs.Job, evidence ...audit.Record) (jobs.Job, bool, error) {
	job.CausalTrace = catalog.CausalTrace(ctx)
	type result struct {
		job      jobs.Job
		existing bool
	}
	bundle, err := withTenantTx(ctx, p.repository.pool, job.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (result, error) {
		if job.DefaultAttemptLimit && job.ParentJobID == "" {
			if err := lockTenantSettings(ctx, tx, job.TenantID, true); err != nil {
				return result{}, err
			}
			attempts, err := resolveNewJobAttempts(ctx, tx, job.TenantID, job.AttemptLimit)
			if err != nil {
				return result{}, err
			}
			job.AttemptLimit = attempts
		}
		job.DefaultAttemptLimit = false
		if job.Kind == jobs.TrustKind {
			if err := lockTrustSource(ctx, tx, job); err != nil {
				return result{}, err
			}
		}
		if job.ParentJobID != "" {
			var parentState jobs.State
			if err := tx.QueryRow(ctx, `SELECT state FROM jobs WHERE tenant_id=$1 AND id=$2 FOR KEY SHARE`, job.TenantID, job.ParentJobID).Scan(&parentState); err != nil {
				return result{}, translateError("lock retry parent job", err)
			}
			if parentState != jobs.StateFailed && parentState != jobs.StateDeadLetter {
				return result{}, catalog.E(catalog.CodeInvalidTransition, "only a failed or dead-letter job can be retried")
			}
		}
		if sourceID, sourceRevision, valid := sourceSyncJobTarget(job); valid {
			var locked int
			if err := tx.QueryRow(ctx, `
        SELECT 1 FROM federation_sources
         WHERE tenant_id=$1 AND id=$2 AND revision=$3 AND status<>'removed'
         FOR KEY SHARE`, job.TenantID, sourceID, sourceRevision).Scan(&locked); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return result{}, catalog.E(catalog.CodePreconditionFailed, "source synchronization revision is no longer current")
				}
				return result{}, translateError("lock source synchronization revision", err)
			}
		}
		var insertedID string
		err := tx.QueryRow(ctx, `
      INSERT INTO jobs(id,tenant_id,kind,actor,parent_job_id,payload_version,payload,state,priority,progress,not_before,attempt,attempt_limit,deduplication_key,created_at,updated_at,causal_trace)
      VALUES ($1,$2,$3,$4,nullif($5,'')::uuid,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,nullif($17,''))
      ON CONFLICT (tenant_id,kind,deduplication_key) WHERE state IN ('queued','running','waiting') DO NOTHING
      RETURNING id::text`, job.ID, job.TenantID, job.Kind, job.Actor, job.ParentJobID, job.PayloadVersion, job.Payload, job.State, job.Priority, job.Progress, job.NotBefore, job.Attempt, job.AttemptLimit, job.DeduplicationKey, job.CreatedAt, job.UpdatedAt, job.CausalTrace).Scan(&insertedID)
		if err == nil {
			if evidenceErr := insertJobEnqueueEvidence(ctx, tx, evidence, job, false); evidenceErr != nil {
				return result{}, evidenceErr
			}
			return result{job: job}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return result{}, translateError("enqueue job", err)
		}
		existing, readErr := readJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE tenant_id=$1 AND kind=$2 AND deduplication_key=$3 AND state IN ('queued','running','waiting')`, job.TenantID, job.Kind, job.DeduplicationKey))
		if readErr != nil {
			return result{}, readErr
		}
		if evidenceErr := insertJobEnqueueEvidence(ctx, tx, evidence, existing, true); evidenceErr != nil {
			return result{}, evidenceErr
		}
		return result{job: existing, existing: true}, nil
	})
	return bundle.job, bundle.existing, err
}

// sourceSyncJobTarget extracts the exact source/revision only from a current
// actionable source.sync payload.
func sourceSyncJobTarget(job jobs.Job) (string, int64, bool) {
	if job.Kind != "source.sync" || jobs.ValidateActionablePayload(job) != nil {
		return "", 0, false
	}
	var payload struct {
		SourceID       string `json:"sourceId"`
		SourceRevision int64  `json:"sourceRevision"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil {
		return "", 0, false
	}
	return payload.SourceID, payload.SourceRevision, true
}

// Get reads one job under its explicit tenant boundary, including retained
// terminal state.
func (p *PostgresJobStore) Get(ctx context.Context, tenantID, jobID string) (jobs.Job, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (jobs.Job, error) {
		return readJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE tenant_id=$1 AND id=$2`, tenantID, jobID))
	})
}

// GetView reads only public metadata and a bounded result projection.
func (p *PostgresJobStore) GetView(ctx context.Context, tenantID, jobID string) (jobs.View, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (jobs.View, error) {
		return readJobView(tx.QueryRow(ctx, `SELECT `+jobViewColumns+` FROM jobs WHERE tenant_id=$1 AND id=$2`, tenantID, jobID))
	})
}

// ListViews selects bounded public metadata in descending creation-time/ID order.
func (p *PostgresJobStore) ListViews(ctx context.Context, tenantID string, request jobs.ListRequest) ([]jobs.View, error) {
	return withTenantCollectionRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]jobs.View, error) {
		rows, err := tx.Query(ctx, collectionStatement(ctx, `SELECT `+jobViewColumns+` FROM jobs WHERE tenant_id=$1 AND `+jobCollectionSQL+` AND `+jobTargetCollectionSQL+` AND ($9::text='' OR kind=$9) AND ($10::text='' OR state=$10) AND (nullif($11,'')::uuid IS NULL OR parent_job_id=nullif($11,'')::uuid) AND ($2::timestamptz IS NULL OR created_at<$2 OR (created_at=$2 AND id<nullif($3,'')::uuid)) ORDER BY created_at DESC,id DESC LIMIT $4`), tenantID, request.AfterCreatedAt, request.AfterID, request.Limit, request.Targets.Enforced, request.Targets.PublicationHosts, request.Targets.DefaultPublicationHost, request.Targets.SemanticProfile, request.Kind, request.State, request.ParentJobID)
		if err != nil {
			return nil, translateError("list jobs", err)
		}
		defer rows.Close()
		items := make([]jobs.View, 0)
		for rows.Next() {
			item, err := readJobView(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, translateError("iterate jobs", rows.Err())
	})
}

// List retains the full internal page contract used by maintenance admission.
func (p *PostgresJobStore) List(ctx context.Context, tenantID string, request jobs.ListRequest) ([]jobs.Job, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]jobs.Job, error) {
		rows, err := tx.Query(ctx, `SELECT `+jobColumns+` FROM jobs WHERE tenant_id=$1 AND ($5::text='' OR kind=$5) AND ($6::text='' OR state=$6) AND (nullif($7,'')::uuid IS NULL OR parent_job_id=nullif($7,'')::uuid) AND ($2::timestamptz IS NULL OR created_at<$2 OR (created_at=$2 AND id<nullif($3,'')::uuid)) ORDER BY created_at DESC,id DESC LIMIT $4`, tenantID, request.AfterCreatedAt, request.AfterID, request.Limit, request.Kind, request.State, request.ParentJobID)
		if err != nil {
			return nil, translateError("list jobs", err)
		}
		defer rows.Close()
		items := make([]jobs.Job, 0)
		for rows.Next() {
			item, err := readJob(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, translateError("iterate jobs", rows.Err())
	})
}

// Claim is a worker-wide operation. Production wiring verifies that the worker
// database role has BYPASSRLS while the API role does not.
func (p *PostgresJobStore) Claim(ctx context.Context, owner string, now time.Time, lease time.Duration) (jobs.Job, error) {
	tx, err := p.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return jobs.Job{}, translateError("begin job claim", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	expires := now.Add(lease)
	job, err := readJob(tx.QueryRow(ctx, `
    WITH candidate AS (
      SELECT id FROM jobs
       WHERE ((state IN ('queued','waiting') AND not_before <= $1) OR (state='running' AND lease_expires_at <= $1))
         AND (kind<>'semantic.reindex' OR ($4<>'' AND payload_version=1 AND payload->>'profile'=$4 AND NOT (tenant_id=ANY(coalesce($5::uuid[],'{}'::uuid[])))))
       ORDER BY priority DESC,not_before,created_at,id
       FOR UPDATE SKIP LOCKED LIMIT 1
    )
    UPDATE jobs j SET state='running',attempt=j.attempt+1,lease_owner=$2,lease_expires_at=$3,
                      safe_error='',failure_class='',updated_at=$1
      FROM candidate WHERE j.id=candidate.id
    RETURNING `+prefixedJobColumns("j"), now, owner, expires, p.semanticProfile, p.semanticExcluded))
	if err != nil {
		return jobs.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return jobs.Job{}, translateError("commit job claim", err)
	}
	return job, nil
}

// Heartbeat extends an owned unexpired running lease and records progress; lost or
// expired claims return conflict.
func (p *PostgresJobStore) Heartbeat(ctx context.Context, tenantID, jobID, owner string, now time.Time, lease time.Duration, progress int) (jobs.Job, error) {
	return p.updateLease(ctx, tenantID, jobID, owner, now, `progress=$5,lease_expires_at=greatest(lease_expires_at,$6)`, progress, now.Add(lease))
}

// Complete marks an owned running job succeeded, retains its result and clears its
// lease in the same update.
func (p *PostgresJobStore) Complete(ctx context.Context, tenantID, jobID, owner string, now time.Time, result json.RawMessage, evidence ...audit.Record) (jobs.Job, error) {
	if len(evidence) > 0 {
		return withTenantTx(ctx, p.repository.pool, tenantID, pgx.TxOptions{}, func(tx pgx.Tx) (jobs.Job, error) {
			job, err := readJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE tenant_id=$1 AND id=$2`, tenantID, jobID))
			if err != nil {
				return jobs.Job{}, err
			}
			if job.Kind != jobs.TrustKind || len(evidence) != 1 {
				return jobs.Job{}, catalog.E(catalog.CodeInternal, "unexpected Job completion evidence")
			}
			if err = lockTrustSource(ctx, tx, job); err != nil {
				return jobs.Job{}, err
			}
			if err = jobs.ValidateTrustCompletion(job, result, evidence[0]); err != nil {
				return jobs.Job{}, err
			}
			completed, err := readJob(tx.QueryRow(ctx, `UPDATE jobs SET state='succeeded',progress=100,result=$5,lease_owner=NULL,lease_expires_at=NULL,updated_at=greatest(updated_at,$4) WHERE tenant_id=$1 AND id=$2 AND state='running' AND lease_owner=$3 AND lease_expires_at>$4 AND cancellation_requested=false RETURNING `+jobColumns, tenantID, jobID, owner, now, result))
			if err != nil {
				return jobs.Job{}, err
			}
			if err = insertAuditEvent(ctx, tx, evidence[0]); err != nil {
				return jobs.Job{}, err
			}
			return completed, nil
		})
	}
	return p.updateLease(ctx, tenantID, jobID, owner, now, `state='succeeded',progress=100,result=$5,lease_owner=NULL,lease_expires_at=NULL`, result)

}

// Fail selects cancelled, waiting, failed or dead-letter disposition from
// cancellation, retry time and attempt limits, then applies it only under the live
// lease fence.
func (p *PostgresJobStore) Fail(ctx context.Context, tenantID, jobID, owner string, now time.Time, class jobs.FailureClass, safe string, retryAt time.Time) (jobs.Job, error) {
	job, err := readJob(p.repository.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE tenant_id=$1 AND id=$2`, tenantID, jobID))
	if err != nil {
		if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
			return jobs.Job{}, catalog.E(catalog.CodeConflict, "job lease is not owned or has expired")
		}
		return jobs.Job{}, err
	}
	state, notBefore := jobFailureDisposition(job, class, retryAt)
	return p.updateLease(ctx, tenantID, jobID, owner, now, `state=$5,safe_error=$6,failure_class=$7,not_before=CASE WHEN $5='waiting' THEN greatest($8,updated_at) ELSE $8 END,lease_owner=NULL,lease_expires_at=NULL`, state, safe, class, notBefore)
}

// jobFailureDisposition gives cancellation precedence over retry and exhaustion.
// The worker supplies retry policy; only a scheduled retry changes NotBefore.
func jobFailureDisposition(job jobs.Job, class jobs.FailureClass, retryAt time.Time) (jobs.State, time.Time) {
	state := jobs.StateFailed
	notBefore := job.NotBefore
	switch {
	case class == jobs.FailureCancelled || job.CancellationRequested:
		state = jobs.StateCancelled
	case !retryAt.IsZero() && job.Attempt < job.AttemptLimit:
		state = jobs.StateWaiting
		notBefore = retryAt
	case job.Attempt >= job.AttemptLimit:
		state = jobs.StateDeadLetter
	}
	return state, notBefore
}

// RequestCancellation immediately cancels queued/waiting work or flags running
// work, preserving terminal state and appending finalized audit evidence.
func (p *PostgresJobStore) RequestCancellation(ctx context.Context, tenantID, jobID string, now time.Time, evidence ...audit.Record) (jobs.Job, error) {
	return withTenantTx(ctx, p.repository.pool, tenantID, pgx.TxOptions{}, func(tx pgx.Tx) (jobs.Job, error) {
		before, err := readJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, jobID))
		if err != nil {
			return jobs.Job{}, err
		}
		after := before
		if now.Before(before.UpdatedAt) {
			now = before.UpdatedAt
		}
		switch before.State {
		case jobs.StateQueued, jobs.StateWaiting:
			after.State = jobs.StateCancelled
			after.UpdatedAt = now
		case jobs.StateRunning:
			after.CancellationRequested = true
			after.UpdatedAt = now
		case jobs.StateSucceeded, jobs.StateFailed, jobs.StateCancelled, jobs.StateDeadLetter:
		default:
			return jobs.Job{}, catalog.E(catalog.CodeInvalidTransition, "job state cannot be cancelled")
		}
		if after.State != before.State || after.CancellationRequested != before.CancellationRequested || !after.UpdatedAt.Equal(before.UpdatedAt) {
			after, err = readJob(tx.QueryRow(ctx, `
      UPDATE jobs SET state=$3,cancellation_requested=$4,updated_at=$5
       WHERE tenant_id=$1 AND id=$2 RETURNING `+jobColumns, tenantID, jobID, after.State, after.CancellationRequested, after.UpdatedAt))
			if err != nil {
				return jobs.Job{}, err
			}
		}
		if err := insertJobCancellationEvidence(ctx, tx, evidence, before, after); err != nil {
			return jobs.Job{}, err
		}
		return after, nil
	})
}

// insertJobEnqueueEvidence binds an optional single audit record to the actually
// inserted or deduplicated job before persistence.
func insertJobEnqueueEvidence(ctx context.Context, tx pgx.Tx, records []audit.Record, job jobs.Job, existing bool) error {
	if len(records) == 0 {
		return nil
	}
	if len(records) != 1 {
		return catalog.E(catalog.CodeInternal, "Job enqueue evidence is ambiguous")
	}
	record, err := jobs.FinalizeEnqueueEvidence(records[0], job, existing)
	if err != nil {
		return err
	}
	return insertAuditEvent(ctx, tx, record)
}

// insertJobCancellationEvidence derives the actual before/after cancellation
// result for an optional single audit record.
func insertJobCancellationEvidence(ctx context.Context, tx pgx.Tx, records []audit.Record, before, after jobs.Job) error {
	if len(records) == 0 {
		return nil
	}
	if len(records) != 1 {
		return catalog.E(catalog.CodeInternal, "Job cancellation evidence is ambiguous")
	}
	record, err := jobs.FinalizeCancellationEvidence(records[0], before, after)
	if err != nil {
		return err
	}
	return insertAuditEvent(ctx, tx, record)
}

// updateLease applies code-owned SQL assignments only to an explicitly
// tenant-scoped running job whose owner and unexpired lease still match.
func (p *PostgresJobStore) updateLease(ctx context.Context, tenantID, jobID, owner string, now time.Time, assignments string, values ...any) (jobs.Job, error) {
	return updateJobLease(ctx, p.repository.pool, tenantID, jobID, owner, now, assignments, values...)
}

// updateJobLease binds a worker update and translates a missing owned lease to
// conflict. Assignments are code-owned SQL supplied by the lease methods above.
func updateJobLease(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenantID, jobID, owner string, now time.Time, assignments string, values ...any) (jobs.Job, error) {
	arguments := make([]any, 0, 4+len(values))
	arguments = append(arguments, tenantID, jobID, owner, now)
	arguments = append(arguments, values...)
	// Clamp retained ordering, never the wall time used by the lease fence.
	extraFence := ""
	if strings.HasPrefix(assignments, "state='succeeded'") {
		extraFence = " AND kind<>'source.trust_verify'"
	}
	statement := `UPDATE jobs SET ` + assignments + `,updated_at=greatest(updated_at,$4) WHERE tenant_id=$1 AND id=$2 AND state='running' AND lease_owner=$3 AND lease_expires_at>$4` + extraFence + ` RETURNING ` + jobColumns
	job, err := readJob(query.QueryRow(ctx, statement, arguments...))
	if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
		return jobs.Job{}, catalog.E(catalog.CodeConflict, "job lease is not owned or has expired")
	}
	return job, err
}

const jobColumns = `id,tenant_id,kind,actor,coalesce(parent_job_id::text,''),payload_version,payload,state,priority,progress,not_before,attempt,attempt_limit,coalesce(lease_owner,''),lease_expires_at,cancellation_requested,deduplication_key,result,safe_error,failure_class,created_at,updated_at,coalesce(causal_trace,'')`

// prefixedJobColumns qualifies the fixed scanner projection with a code-owned
// table alias for claim updates.
func prefixedJobColumns(prefix string) string {
	return prefix + `.id,` + prefix + `.tenant_id,` + prefix + `.kind,` + prefix + `.actor,coalesce(` + prefix + `.parent_job_id::text,''),` + prefix + `.payload_version,` + prefix + `.payload,` + prefix + `.state,` + prefix + `.priority,` + prefix + `.progress,` + prefix + `.not_before,` + prefix + `.attempt,` + prefix + `.attempt_limit,coalesce(` + prefix + `.lease_owner,''),` + prefix + `.lease_expires_at,` + prefix + `.cancellation_requested,` + prefix + `.deduplication_key,` + prefix + `.result,` + prefix + `.safe_error,` + prefix + `.failure_class,` + prefix + `.created_at,` + prefix + `.updated_at,coalesce(` + prefix + `.causal_trace,'')`
}

// readJob decodes the fixed job projection, including lease and private causal
// context, translating scan failures to domain errors.
func readJob(row scanner) (jobs.Job, error) {
	var job jobs.Job
	if err := row.Scan(&job.ID, &job.TenantID, &job.Kind, &job.Actor, &job.ParentJobID, &job.PayloadVersion, &job.Payload, &job.State, &job.Priority, &job.Progress, &job.NotBefore, &job.Attempt, &job.AttemptLimit, &job.LeaseOwner, &job.LeaseExpiresAt, &job.CancellationRequested, &job.DeduplicationKey, &job.Result, &job.SafeError, &job.FailureClass, &job.CreatedAt, &job.UpdatedAt, &job.CausalTrace); err != nil {
		return jobs.Job{}, translateError("read job", err)
	}
	return job, nil
}

// Unknown families have no public authority. Family predicates precede LIMIT.
var jobCollectionSQL = `(nullif(current_setting('app.collection_permissions',true),'') IS NULL OR (` +
	`(kind='source.trust_verify' AND ` + collectionPermissionSQL("source.read_evidence", "") + `) OR ` +
	`(kind='source.sync' AND ` + collectionPermissionSQL("source.read", "") + `) OR ` +
	`(kind='publication.generate' AND ` + collectionPermissionSQL("publication.read", "") + `) OR ` +
	`(kind='semantic.reindex' AND ` + collectionPermissionSQL("operations.reindex", "") + `)))`

// Compare source IDs as text so malformed retained input cannot make a cast
// fail the whole page. Unknown/deleted/unconfigured targets are omitted before
// ordering. Generic owner fixtures opt out; runtime always enforces targets.
const jobTargetCollectionSQL = `($5=false OR (
 jsonb_typeof(payload)='object' AND (
 (kind='source.trust_verify' AND payload_version=1 AND EXISTS(SELECT 1 FROM federation_sources fs WHERE fs.tenant_id=jobs.tenant_id AND fs.id::text=jobs.payload->>'sourceId')) OR
 (kind='source.sync' AND payload_version=2 AND EXISTS(SELECT 1 FROM federation_sources fs WHERE fs.tenant_id=jobs.tenant_id AND fs.id::text=jobs.payload->>'sourceId' AND fs.status<>'removed')) OR
 (kind='publication.generate' AND payload_version=1 AND coalesce(nullif(payload->>'host',''),$7)=ANY($6::text[])) OR
 (kind='semantic.reindex' AND payload_version=1 AND $8<>'' AND payload->>'profile'=$8 AND (payload->>'scope'='control' OR payload->>'scope' ~ '^public:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'))
 )))`

// The source lock covers enqueue or terminal completion, never evidence I/O.
func lockTrustSource(ctx context.Context, tx pgx.Tx, job jobs.Job) error {
	p, err := jobs.DecodeTrustPayload(job)
	if err != nil {
		return err
	}
	source, err := readFederationSource(ctx, tx, job.TenantID, p.SourceID, true)
	if err != nil {
		return err
	}
	if !federation.TrustSourceMatches(source, p) {
		return catalog.E(catalog.CodePreconditionFailed, "trust assessment source changed")
	}
	return nil
}
