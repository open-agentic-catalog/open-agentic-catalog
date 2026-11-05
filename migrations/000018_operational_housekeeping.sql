ALTER TABLE source_inspections
  ADD COLUMN cleanup_tracking_version smallint,
  ADD CONSTRAINT source_inspections_cleanup_tracking_check
    CHECK (cleanup_tracking_version IS NULL OR cleanup_tracking_version = 1);

CREATE UNIQUE INDEX source_inspections_tenant_id_id_uq
  ON source_inspections (tenant_id,id);

ALTER TABLE federation_source_revisions
  ADD COLUMN inspection_id uuid,
  ADD CONSTRAINT federation_source_revisions_inspection_fk
    FOREIGN KEY (tenant_id,inspection_id)
    REFERENCES source_inspections(tenant_id,id);

ALTER TABLE jobs
  ADD COLUMN cleanup_after timestamptz,
  ADD CONSTRAINT jobs_cleanup_state_check
    CHECK (cleanup_after IS NULL OR state IN ('succeeded','failed','cancelled','dead_letter'));

ALTER TABLE outbox_events
  ADD COLUMN cleanup_after timestamptz,
  ADD CONSTRAINT outbox_events_cleanup_state_check
    CHECK (cleanup_after IS NULL OR published_at IS NOT NULL AND lease_owner IS NULL AND lease_expires_at IS NULL);

ALTER TABLE jobs DROP CONSTRAINT jobs_parent_job_id_fkey;

CREATE INDEX idempotency_housekeeping
  ON idempotency_keys (expires_at,tenant_id,reservation_id)
  WHERE protocol_version=1 AND response_status IS NOT NULL
     OR protocol_version=2 AND (atomic_response_status IS NOT NULL OR released_at IS NOT NULL);

CREATE INDEX source_change_previews_housekeeping
  ON source_change_previews (expires_at,tenant_id,id);

CREATE INDEX source_inspections_housekeeping
  ON source_inspections (expires_at,tenant_id,id)
  WHERE cleanup_tracking_version=1;

CREATE INDEX source_retrieval_states_housekeeping
  ON source_retrieval_states (updated_at,tenant_id,source_id,source_revision,request_key);

CREATE INDEX source_maintenance_state_housekeeping
  ON source_maintenance_state (last_admitted_at,tenant_id,source_id,source_revision);

CREATE INDEX jobs_housekeeping_mark
  ON jobs (updated_at,tenant_id,id)
  WHERE cleanup_after IS NULL AND state IN ('succeeded','failed','cancelled','dead_letter');

CREATE INDEX jobs_housekeeping_delete
  ON jobs (cleanup_after,tenant_id,id)
  WHERE cleanup_after IS NOT NULL;

CREATE INDEX outbox_events_housekeeping_mark
  ON outbox_events (published_at,tenant_id,id)
  WHERE published_at IS NOT NULL AND cleanup_after IS NULL;

CREATE INDEX outbox_events_housekeeping_delete
  ON outbox_events (cleanup_after,tenant_id,id)
  WHERE cleanup_after IS NOT NULL;

CREATE INDEX jobs_live_source_revision
  ON jobs (tenant_id,(payload->>'sourceId'),(payload->>'sourceRevision'),created_at,id)
  WHERE kind='source.sync' AND state IN ('queued','running','waiting');

DROP TRIGGER source_inspections_immutable_history ON source_inspections;
DROP TRIGGER source_change_previews_immutable_history ON source_change_previews;
DROP TRIGGER jobs_retained_row ON jobs;
DROP TRIGGER outbox_events_retained_row ON outbox_events;

CREATE FUNCTION guard_source_inspection_history() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  referenced boolean;
BEGIN
  IF TG_OP = 'UPDATE' THEN
    RAISE EXCEPTION 'source inspection history is immutable';
  END IF;
  EXECUTE format(
    'SELECT EXISTS (SELECT 1 FROM %I.federation_source_revisions WHERE tenant_id=$1 AND inspection_id=$2)',
    TG_TABLE_SCHEMA
  ) INTO referenced USING OLD.tenant_id,OLD.id;
  IF OLD.cleanup_tracking_version=1 AND OLD.expires_at<=clock_timestamp() AND NOT referenced THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'source inspection remains retained or authoritative';
END
$function$;

CREATE TRIGGER source_inspections_history
  BEFORE UPDATE OR DELETE ON source_inspections
  FOR EACH ROW EXECUTE FUNCTION guard_source_inspection_history();

CREATE FUNCTION guard_source_change_preview_history() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF TG_OP = 'DELETE' AND OLD.expires_at<=clock_timestamp() THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'source change preview remains authoritative or immutable';
END
$function$;

CREATE TRIGGER source_change_previews_history
  BEFORE UPDATE OR DELETE ON source_change_previews
  FOR EACH ROW EXECUTE FUNCTION guard_source_change_preview_history();

CREATE FUNCTION guard_source_maintenance_state_delete() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  obsolete boolean;
  blocked boolean;
BEGIN
  EXECUTE format(
    'SELECT EXISTS (SELECT 1 FROM %I.federation_sources WHERE tenant_id=$1 AND id=$2 AND (status=''removed'' OR revision<>$3))',
    TG_TABLE_SCHEMA
  ) INTO obsolete USING OLD.tenant_id,OLD.source_id,OLD.source_revision;
  EXECUTE format(
    'SELECT EXISTS (
       SELECT 1 FROM %I.jobs job
        WHERE job.tenant_id=$1 AND job.kind=''source.sync'' AND job.state IN (''queued'',''running'',''waiting'')
          AND CASE
            WHEN job.payload_version IN (1,2)
             AND jsonb_typeof(job.payload)=''object''
             AND job.payload-ARRAY[''sourceId'',''sourceRevision'',''trigger'']=''{}''::jsonb
             AND jsonb_typeof(job.payload->''sourceId'')=''string''
             AND coalesce(job.payload->>''sourceId'','''') ~ ''^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$''
             AND jsonb_typeof(job.payload->''sourceRevision'')=''number''
             AND coalesce(job.payload->>''sourceRevision'','''') ~ ''^[1-9][0-9]{0,17}$''
             AND ((job.payload_version=1 AND coalesce(job.payload->>''trigger'','''') IN ('''',''manual''))
               OR (job.payload_version=2 AND jsonb_typeof(job.payload->''trigger'')=''string''
                 AND job.payload->>''trigger'' IN (''manual'',''automatic'')))
            THEN job.payload->>''sourceId''=$2::text AND (job.payload->>''sourceRevision'')::bigint=$3
            ELSE true
          END
     )',
    TG_TABLE_SCHEMA
  ) INTO blocked USING OLD.tenant_id,OLD.source_id,OLD.source_revision;
  IF obsolete AND NOT blocked THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'source maintenance state remains current or in use';
END
$function$;

CREATE TRIGGER source_maintenance_state_delete_lifecycle
  BEFORE DELETE ON source_maintenance_state
  FOR EACH ROW EXECUTE FUNCTION guard_source_maintenance_state_delete();

CREATE OR REPLACE FUNCTION guard_job_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.state <> 'queued' OR NEW.progress <> 0 OR NEW.attempt <> 0 OR
       NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL OR
       NEW.cancellation_requested OR NEW.result IS NOT NULL OR NEW.safe_error <> '' OR NEW.failure_class <> '' OR
       NEW.cleanup_after IS NOT NULL THEN
      RAISE EXCEPTION 'new job must be an unleased queued job';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.state IN ('succeeded','failed','cancelled','dead_letter') AND
     OLD.cleanup_after IS NULL AND NEW.cleanup_after>clock_timestamp() AND
     (to_jsonb(NEW)-'cleanup_after')=(to_jsonb(OLD)-'cleanup_after') THEN
    RETURN NEW;
  END IF;
  IF NEW.cleanup_after IS DISTINCT FROM OLD.cleanup_after THEN
    RAISE EXCEPTION 'job cleanup eligibility transition is invalid';
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.kind,NEW.actor,NEW.parent_job_id,NEW.payload_version,NEW.payload,
         NEW.priority,NEW.attempt_limit,NEW.deduplication_key,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.kind,OLD.actor,OLD.parent_job_id,OLD.payload_version,OLD.payload,
         OLD.priority,OLD.attempt_limit,OLD.deduplication_key,OLD.created_at) THEN
    RAISE EXCEPTION 'job identity and input are immutable';
  END IF;
  IF OLD.state IN ('succeeded','failed','cancelled','dead_letter') THEN
    RAISE EXCEPTION 'terminal job is immutable';
  END IF;
  IF NOT (
    OLD.state IN ('queued','waiting') AND NEW.state IN ('running','cancelled') OR
    OLD.state = 'running' AND NEW.state IN ('running','waiting','succeeded','failed','cancelled','dead_letter')
  ) THEN
    RAISE EXCEPTION 'job state transition is invalid';
  END IF;
  IF NEW.updated_at < OLD.updated_at OR NEW.progress < OLD.progress OR
     OLD.cancellation_requested AND NOT NEW.cancellation_requested THEN
    RAISE EXCEPTION 'job progress transition is invalid';
  END IF;
  IF OLD.state IN ('queued','waiting') AND NEW.state = 'running' THEN
    IF NEW.attempt <> OLD.attempt+1 OR NEW.lease_expires_at <= NEW.updated_at THEN
      RAISE EXCEPTION 'job claim attempt transition is invalid';
    END IF;
  ELSIF OLD.state = 'running' AND NEW.state = 'running' AND NEW.attempt = OLD.attempt+1 THEN
    IF OLD.lease_expires_at IS NULL OR OLD.lease_expires_at > NEW.updated_at OR
       NEW.lease_expires_at <= NEW.updated_at THEN
      RAISE EXCEPTION 'job lease cannot be reclaimed before expiry';
    END IF;
  ELSIF NEW.attempt <> OLD.attempt THEN
    RAISE EXCEPTION 'job attempt transition is invalid';
  END IF;
  IF OLD.state = 'running' AND NEW.state = 'running' AND NEW.attempt = OLD.attempt AND
     NEW.lease_owner IS DISTINCT FROM OLD.lease_owner THEN
    RAISE EXCEPTION 'job lease owner cannot change during an active attempt';
  END IF;
  IF OLD.state = 'running' AND NEW.state = 'running' AND NEW.attempt = OLD.attempt AND
     NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at AND
     (NEW.lease_expires_at < OLD.lease_expires_at OR NEW.lease_expires_at <= NEW.updated_at) THEN
    RAISE EXCEPTION 'job lease expiry cannot move backwards';
  END IF;
  IF NEW.not_before IS DISTINCT FROM OLD.not_before AND NOT (OLD.state = 'running' AND NEW.state = 'waiting') THEN
    RAISE EXCEPTION 'job schedule transition is invalid';
  END IF;
  IF NEW.result IS DISTINCT FROM OLD.result AND NOT (
       OLD.state = 'running' AND NEW.state = 'succeeded' AND OLD.result IS NULL AND NEW.result IS NOT NULL
     ) THEN
    RAISE EXCEPTION 'job result transition is invalid';
  END IF;
  IF ROW(NEW.safe_error,NEW.failure_class) IS DISTINCT FROM ROW(OLD.safe_error,OLD.failure_class) AND NOT (
       NEW.state = 'running' AND NEW.safe_error = '' AND NEW.failure_class = '' OR
       OLD.state = 'running' AND NEW.state IN ('waiting','failed','cancelled','dead_letter')
     ) THEN
    RAISE EXCEPTION 'job failure transition is invalid';
  END IF;
  IF NEW.state = 'succeeded' THEN
    IF NEW.progress <> 100 THEN
      RAISE EXCEPTION 'successful job progress is invalid';
    END IF;
  ELSIF NOT (OLD.state = 'running' AND NEW.state = 'running' AND NEW.attempt = OLD.attempt) AND
        NEW.progress <> OLD.progress THEN
    RAISE EXCEPTION 'job progress can change only during a heartbeat or completion';
  END IF;
  IF NOT OLD.cancellation_requested AND NEW.cancellation_requested AND NOT (
       OLD.state = 'running' AND NEW.state = 'running' AND NEW.attempt = OLD.attempt
     ) THEN
    RAISE EXCEPTION 'job cancellation request transition is invalid';
  END IF;
  IF NEW.state = 'running' AND (NEW.lease_owner IS NULL OR NEW.lease_expires_at IS NULL) OR
     NEW.state <> 'running' AND (NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL) THEN
    RAISE EXCEPTION 'job lease does not match its state';
  END IF;
  RETURN NEW;
END
$function$;

CREATE FUNCTION guard_job_delete() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  has_child boolean;
BEGIN
  EXECUTE format(
    'SELECT EXISTS (SELECT 1 FROM %I.jobs WHERE tenant_id=$1 AND parent_job_id=$2)',
    TG_TABLE_SCHEMA
  ) INTO has_child USING OLD.tenant_id,OLD.id;
  IF OLD.state IN ('succeeded','failed','cancelled','dead_letter') AND
     OLD.cleanup_after IS NOT NULL AND OLD.cleanup_after<=clock_timestamp() AND
     OLD.lease_owner IS NULL AND OLD.lease_expires_at IS NULL AND NOT has_child THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'job remains active, retained, or referenced';
END
$function$;

CREATE TRIGGER jobs_delete_lifecycle
  BEFORE DELETE ON jobs
  FOR EACH ROW EXECUTE FUNCTION guard_job_delete();

CREATE OR REPLACE FUNCTION guard_outbox_event_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  has_delivery boolean;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL OR NEW.cleanup_after IS NOT NULL OR
       NEW.published_at IS NOT NULL AND NEW.published_at < NEW.created_at THEN
      RAISE EXCEPTION 'new outbox event has invalid delivery state';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.published_at IS NOT NULL AND OLD.cleanup_after IS NULL AND
     NEW.cleanup_after>clock_timestamp() AND
     (to_jsonb(NEW)-'cleanup_after')=(to_jsonb(OLD)-'cleanup_after') THEN
    EXECUTE format(
      'SELECT EXISTS (SELECT 1 FROM %I.webhook_deliveries WHERE tenant_id=$1 AND event_id=$2)',
      TG_TABLE_SCHEMA
    ) INTO has_delivery USING OLD.tenant_id,OLD.id;
    IF NOT has_delivery THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'outbox event with delivery evidence remains retained';
  END IF;
  IF NEW.cleanup_after IS DISTINCT FROM OLD.cleanup_after THEN
    RAISE EXCEPTION 'outbox cleanup eligibility transition is invalid';
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.event_type,NEW.subject,NEW.data,NEW.is_internal,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.event_type,OLD.subject,OLD.data,OLD.is_internal,OLD.created_at) THEN
    RAISE EXCEPTION 'outbox event payload is immutable';
  END IF;
  IF OLD.published_at IS NOT NULL THEN
    RAISE EXCEPTION 'published outbox event is immutable';
  END IF;
  IF (NEW.lease_owner IS NULL) <> (NEW.lease_expires_at IS NULL) THEN
    RAISE EXCEPTION 'outbox lease owner and expiry must change together';
  END IF;
  IF NEW.published_at IS NOT NULL THEN
    IF NEW.published_at < NEW.created_at OR NEW.lease_owner IS NOT NULL OR
       OLD.lease_owner IS NULL OR OLD.lease_expires_at <= NEW.published_at THEN
      RAISE EXCEPTION 'outbox event publication transition is invalid';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.lease_owner IS NULL AND NEW.lease_owner IS NOT NULL AND NEW.lease_expires_at > NEW.created_at THEN
    RETURN NEW;
  END IF;
  IF OLD.lease_owner IS NOT NULL AND NEW.lease_owner IS NULL THEN
    RETURN NEW;
  END IF;
  IF OLD.lease_owner IS NOT NULL AND NEW.lease_owner = OLD.lease_owner AND
     NEW.lease_expires_at >= OLD.lease_expires_at THEN
    RETURN NEW;
  END IF;
  IF OLD.lease_owner IS NOT NULL AND NEW.lease_owner IS DISTINCT FROM OLD.lease_owner AND
     NEW.lease_expires_at > OLD.lease_expires_at THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'outbox event delivery transition is invalid';
END
$function$;

CREATE FUNCTION guard_outbox_event_delete() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  has_delivery boolean;
BEGIN
  EXECUTE format(
    'SELECT EXISTS (SELECT 1 FROM %I.webhook_deliveries WHERE tenant_id=$1 AND event_id=$2)',
    TG_TABLE_SCHEMA
  ) INTO has_delivery USING OLD.tenant_id,OLD.id;
  IF OLD.published_at IS NOT NULL AND OLD.cleanup_after IS NOT NULL AND
     OLD.cleanup_after<=clock_timestamp() AND OLD.lease_owner IS NULL AND
     OLD.lease_expires_at IS NULL AND NOT has_delivery THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'outbox event remains active, retained, or referenced';
END
$function$;

CREATE TRIGGER outbox_events_delete_lifecycle
  BEFORE DELETE ON outbox_events
  FOR EACH ROW EXECUTE FUNCTION guard_outbox_event_delete();

CREATE OR REPLACE FUNCTION guard_idempotency_key_update() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(NEW.tenant_id,NEW.actor,NEW.route,NEW.idempotency_key)
       IS DISTINCT FROM ROW(OLD.tenant_id,OLD.actor,OLD.route,OLD.idempotency_key) THEN
    RAISE EXCEPTION 'idempotency key scope is immutable';
  END IF;

  IF OLD.protocol_version = 1 AND OLD.response_status IS NULL AND NEW.response_status IS NOT NULL AND
     (to_jsonb(NEW) - ARRAY['response_status','response_headers','response_body','completed_at']) =
       (to_jsonb(OLD) - ARRAY['response_status','response_headers','response_body','completed_at']) THEN
    RETURN NEW;
  END IF;

  IF OLD.protocol_version = 2 AND OLD.atomic_response_status IS NULL AND OLD.released_at IS NULL AND
     NEW.atomic_response_status IS NOT NULL AND
     (to_jsonb(NEW) - ARRAY['atomic_response_status','atomic_response_headers','atomic_response_body','completed_at']) =
       (to_jsonb(OLD) - ARRAY['atomic_response_status','atomic_response_headers','atomic_response_body','completed_at']) THEN
    RETURN NEW;
  END IF;

  IF OLD.completed_at IS NOT NULL AND OLD.expires_at <= clock_timestamp() AND
     NEW.protocol_version = 2 AND NEW.response_status IS NULL AND NEW.atomic_response_status IS NULL AND
     NEW.completed_at IS NULL AND NEW.released_at IS NULL AND NEW.repair_reason IS NULL AND
     NEW.authorization_digest IS NOT NULL AND NEW.execution_token IS NOT NULL AND
     (to_jsonb(NEW) - ARRAY[
       'request_digest','response_status','response_headers','response_body','expires_at','created_at',
       'protocol_version','authorization_digest','reservation_id','execution_token','started_at','completed_at',
       'atomic_response_status','atomic_response_headers','atomic_response_body'
     ]) = (to_jsonb(OLD) - ARRAY[
       'request_digest','response_status','response_headers','response_body','expires_at','created_at',
       'protocol_version','authorization_digest','reservation_id','execution_token','started_at','completed_at',
       'atomic_response_status','atomic_response_headers','atomic_response_body'
     ]) AND
     ROW(NEW.request_digest,NEW.expires_at,NEW.created_at,NEW.reservation_id,NEW.authorization_digest,NEW.execution_token,NEW.started_at)
       IS DISTINCT FROM ROW(OLD.request_digest,OLD.expires_at,OLD.created_at,OLD.reservation_id,OLD.authorization_digest,OLD.execution_token,OLD.started_at) THEN
    RETURN NEW;
  END IF;

  IF OLD.protocol_version=2 AND OLD.released_at IS NOT NULL AND OLD.atomic_response_status IS NULL AND
     NEW.protocol_version=2 AND NEW.response_status IS NULL AND NEW.atomic_response_status IS NULL AND
     NEW.completed_at IS NULL AND NEW.released_at IS NULL AND NEW.repair_reason IS NULL AND
     NEW.authorization_digest IS NOT NULL AND NEW.execution_token IS NOT NULL AND
     (OLD.expires_at<=clock_timestamp() OR
       ROW(NEW.request_digest,NEW.authorization_digest) IS NOT DISTINCT FROM ROW(OLD.request_digest,OLD.authorization_digest)) AND
     (to_jsonb(NEW) - ARRAY[
       'request_digest','expires_at','created_at','authorization_digest','reservation_id','execution_token','started_at','released_at','repair_reason'
     ]) = (to_jsonb(OLD) - ARRAY[
       'request_digest','expires_at','created_at','authorization_digest','reservation_id','execution_token','started_at','released_at','repair_reason'
     ]) THEN
    RETURN NEW;
  END IF;

  IF OLD.protocol_version = 2 AND OLD.atomic_response_status IS NULL AND OLD.released_at IS NULL AND
     NEW.released_at IS NOT NULL AND NEW.repair_reason <> '' AND NEW.execution_token IS NULL AND
     current_user=pg_get_userbyid((SELECT relowner FROM pg_class WHERE oid=TG_RELID)) AND
     (to_jsonb(NEW) - ARRAY['released_at','repair_reason','execution_token']) =
       (to_jsonb(OLD) - ARRAY['released_at','repair_reason','execution_token']) THEN
    RETURN NEW;
  END IF;

  RAISE EXCEPTION 'idempotency key lifecycle transition is invalid';
END
$function$;

CREATE OR REPLACE FUNCTION guard_idempotency_key_delete() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF OLD.expires_at<=clock_timestamp() AND (
       OLD.protocol_version=1 AND OLD.response_status IS NOT NULL OR
       OLD.protocol_version=2 AND (OLD.atomic_response_status IS NOT NULL OR OLD.released_at IS NOT NULL)
     ) THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'idempotency key cannot be deleted while replay or recovery remains authoritative';
END
$function$;

REVOKE ALL ON FUNCTION guard_source_inspection_history() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_source_change_preview_history() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_source_maintenance_state_delete() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_job_delete() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_outbox_event_delete() FROM PUBLIC;

COMMENT ON COLUMN source_inspections.cleanup_tracking_version IS 'Prospective marker proving that every consumer records use through the tenant-inclusive source-revision link; NULL pre-dates cleanup tracking and remains retained.';
COMMENT ON COLUMN federation_source_revisions.inspection_id IS 'Optional tenant-inclusive link to the unexpired inspection transactionally consumed for connection or connectivity revalidation.';
COMMENT ON COLUMN jobs.cleanup_after IS 'Immutable worker-assigned earliest cleanup eligibility; NULL remains retained and configuration changes never rewrite an existing marker.';
COMMENT ON COLUMN outbox_events.cleanup_after IS 'Immutable worker-assigned earliest cleanup eligibility for a published unleased event without delivery evidence.';
