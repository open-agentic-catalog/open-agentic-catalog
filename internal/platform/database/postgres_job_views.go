package database

import "github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"

// Keep the existing Go decoder authoritative for result semantics. PostgreSQL
// transfers only recognized keys and bounded scalar values, even for old Jobs
// retaining entire snapshots. Wrong types become false (a decoder error), not
// coerced strings/numbers. Oversized used strings become empty (invalid), while
// unused strings need only retain their type. Null preserves decoder behavior.
// Preserve case aliases and JSONB object ordering, including Go's long-s fold;
// otherwise a malformed unused field or a later alias could change the result.
// This bounds returned bytes, not PostgreSQL's work inspecting retained JSONB.
const jobResultProjectionSQL = `(SELECT jsonb_object_agg(key,
 CASE
  WHEN value='null'::jsonb THEN value
  WHEN name='generation' AND kind<>'semantic.reindex' OR name='members' THEN CASE
    WHEN jsonb_typeof(value)='number' AND octet_length(value::text)<=20 THEN value
    ELSE 'false'::jsonb END
  WHEN jsonb_typeof(value)<>'string' THEN 'false'::jsonb
  WHEN name='host' AND kind='source.sync' OR name='sourceid' AND kind='publication.generate' THEN '""'::jsonb
  WHEN octet_length(value #>> '{}') <= CASE name WHEN 'host' THEN 253 WHEN 'state' THEN 10 ELSE 36 END THEN value
  ELSE '""'::jsonb
 END)
 FROM (
  SELECT key,value,translate(key,U&'ABCDEFGHIJKLMNOPQRSTUVWXYZ\017F','abcdefghijklmnopqrstuvwxyzs') AS name
  FROM jsonb_each(CASE WHEN kind IN ('source.sync','publication.generate','semantic.reindex') AND jsonb_typeof(result)='object' THEN result ELSE '{}'::jsonb END)
 ) fields
 WHERE (kind='semantic.reindex' AND name IN ('generation','members')) OR
       (kind<>'semantic.reindex' AND name IN ('id','sourceid','host','generation','state')))`

const jobViewColumns = `id,tenant_id,kind,coalesce(parent_job_id::text,''),state,priority,progress,not_before,attempt,attempt_limit,cancellation_requested,` + jobResultProjectionSQL + `,safe_error,failure_class,created_at,updated_at`

// readJobView never returns an incomplete execution Job to callers. Its local
// metadata-only value reuses the same PublicView validator as command replies.
func readJobView(row scanner) (jobs.View, error) {
	var job jobs.Job
	if err := row.Scan(&job.ID, &job.TenantID, &job.Kind, &job.ParentJobID, &job.State, &job.Priority, &job.Progress, &job.NotBefore, &job.Attempt, &job.AttemptLimit, &job.CancellationRequested, &job.Result, &job.SafeError, &job.FailureClass, &job.CreatedAt, &job.UpdatedAt); err != nil {
		return jobs.View{}, translateError("read job metadata", err)
	}
	return jobs.PublicView(job), nil
}
