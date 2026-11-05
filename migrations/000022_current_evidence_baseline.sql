-- First-release storage baseline. Incompatible development evidence is not
-- converted, backfilled, discarded, or served through an alternate reader.
DO $current$
BEGIN
  IF EXISTS (SELECT 1 FROM audit_events WHERE format_version IS DISTINCT FROM 1
      OR action='repair.search_projection.rebuilt' AND (details->'facts' ? 'remaining' OR NOT (details->'facts' ? 'hasMore'))) THEN
    RAISE EXCEPTION 'unsupported pre-release audit state; keep runtimes stopped';
  END IF;
  IF EXISTS (SELECT 1 FROM evidence_sets evidence WHERE validation_profile<>'catalog-validation-v7'
      OR rights IS DISTINCT FROM '{"state":"not_assessed"}'::jsonb
      OR NOT EXISTS (SELECT 1 FROM version_validation_runs run WHERE run.tenant_id=evidence.tenant_id AND run.evidence_set_id=evidence.id)) THEN
    RAISE EXCEPTION 'unsupported pre-release validation evidence; keep runtimes stopped';
  END IF;
  IF EXISTS (SELECT 1 FROM representations WHERE NOT external_reference_only AND inline_document IS NULL AND document_blob_id IS NULL) THEN
    RAISE EXCEPTION 'unsupported pre-release representation state; keep runtimes stopped';
  END IF;
  IF EXISTS (SELECT 1 FROM observations WHERE identity_digest IS NULL OR raw_entry_bytes IS NULL)
      OR EXISTS (SELECT 1 FROM observation_occurrences WHERE source_revision IS NULL OR NOT evidence_complete)
      OR EXISTS (SELECT 1 FROM source_snapshots WHERE source_revision IS NULL OR details ? 'possiblyWithdrawn')
      OR EXISTS (SELECT 1 FROM federation_source_revisions WHERE update_mode IS NULL OR registry_list_url IS NULL OR policy_id='' OR policy_revision=0 OR policy_digest='') THEN
    RAISE EXCEPTION 'unsupported pre-release federation evidence; keep runtimes stopped';
  END IF;
  IF EXISTS (SELECT 1 FROM source_inspections WHERE cleanup_tracking_version IS DISTINCT FROM 1) THEN
    RAISE EXCEPTION 'unsupported pre-release inspection tracking; keep runtimes stopped';
  END IF;
END
$current$;

DROP TABLE audit_legacy_inventory;
ALTER TABLE audit_events
  ALTER COLUMN format_version SET NOT NULL,
  DROP CONSTRAINT audit_events_format_version,
  ADD CONSTRAINT audit_events_format_version CHECK (format_version = 1),
  ADD CONSTRAINT audit_events_search_repair_current CHECK (
    action<>'repair.search_projection.rebuilt' OR
    coalesce(NOT (details->'facts' ? 'remaining') AND jsonb_typeof(details#>'{facts,hasMore}')='boolean',false)
  );

ALTER TABLE representations
  DROP CONSTRAINT representations_delivery_check,
  DROP CONSTRAINT representations_document_compatibility_check,
  DROP COLUMN artifact_id,
  ADD CONSTRAINT representations_delivery_check CHECK (
    NOT external_reference_only AND (
      external_url IS NULL AND num_nonnulls(inline_document,document_blob_id)=1
      OR external_url IS NOT NULL AND inline_document IS NULL AND document_blob_id IS NOT NULL
    )
    OR external_reference_only AND num_nonnulls(external_url,inline_document,document_blob_id)=0
       AND media_type IN ('application/agent-skills+zip','application/agent-skills+gzip')
  );
CREATE OR REPLACE FUNCTION guard_representation_history() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog
AS $function$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.external_reference_only OR NEW.inline_document IS NOT NULL OR NEW.document_blob_id IS NOT NULL THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'new bounded representation documents must be authoritative in PostgreSQL';
  END IF;
  RAISE EXCEPTION 'catalog retained history is immutable';
END
$function$;
COMMENT ON FUNCTION guard_representation_history() IS 'Current PostgreSQL document authority; retained representations cannot be updated or deleted.';
COMMENT ON COLUMN representations.external_url IS 'Credential-free HTTPS delivery URL bound to exact validation bytes in document_blob_id.';

ALTER TABLE evidence_sets
  DROP COLUMN expires_at,
  ADD CONSTRAINT evidence_sets_current_profile CHECK (validation_profile='catalog-validation-v7'),
  ADD CONSTRAINT evidence_sets_current_rights CHECK (rights='{"state":"not_assessed"}'::jsonb);
COMMENT ON TABLE version_validation_runs IS 'Immutable current validation runs; nullable expires_at is the sole expiry authority, including deterministic non-expiring evidence.';

ALTER TABLE observations
  DROP COLUMN raw_entry,
  ALTER COLUMN raw_entry_bytes SET NOT NULL,
  ALTER COLUMN identity_digest SET NOT NULL;
CREATE OR REPLACE FUNCTION guard_observation_lifecycle() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(
       NEW.id,NEW.tenant_id,NEW.source_id,NEW.snapshot_id,NEW.source_key,NEW.identifier,NEW.version,
       NEW.version_present,NEW.kind,NEW.media_type,NEW.completeness,NEW.compatibility,
       NEW.semantic_digest,NEW.exact_entry_digest,NEW.first_seen_at,NEW.raw_entry_bytes,
       NEW.normalized_entry_digest,NEW.identity_digest,NEW.normalization_profile,NEW.normalized_entry_bytes
     ) IS DISTINCT FROM ROW(
       OLD.id,OLD.tenant_id,OLD.source_id,OLD.snapshot_id,OLD.source_key,OLD.identifier,OLD.version,
       OLD.version_present,OLD.kind,OLD.media_type,OLD.completeness,OLD.compatibility,
       OLD.semantic_digest,OLD.exact_entry_digest,OLD.first_seen_at,OLD.raw_entry_bytes,
       OLD.normalized_entry_digest,OLD.identity_digest,OLD.normalization_profile,OLD.normalized_entry_bytes
     ) THEN
    RAISE EXCEPTION 'observation identity and original evidence are immutable';
  END IF;
  IF OLD.freshness='source_removed' AND NEW.freshness<>OLD.freshness THEN
    RAISE EXCEPTION 'source-removed observation freshness is terminal';
  END IF;
  IF NEW.last_seen_at<OLD.last_seen_at THEN
    RAISE EXCEPTION 'observation last-seen time cannot move backwards';
  END IF;
  RETURN NEW;
END
$function$;

ALTER TABLE source_snapshots ALTER COLUMN source_revision SET NOT NULL;
ALTER TABLE source_snapshots ADD CONSTRAINT source_snapshots_current_document CHECK (NOT (details ? 'possiblyWithdrawn'));
ALTER TABLE observation_occurrences
  ALTER COLUMN source_revision SET NOT NULL,
  ADD CONSTRAINT observation_occurrences_current_provenance CHECK (evidence_complete);
ALTER TABLE federation_source_revisions
  ALTER COLUMN update_mode SET NOT NULL,
  ALTER COLUMN registry_list_url SET NOT NULL,
  ADD CONSTRAINT federation_source_revisions_current_policy CHECK (policy_id<>'' AND policy_revision>0 AND policy_digest<>'');
DROP INDEX federation_source_revisions_automatic;
CREATE INDEX federation_source_revisions_automatic
  ON federation_source_revisions (policy_digest,retrieval_mode,recorded_at,tenant_id,source_id,revision)
  WHERE update_mode='automatic';

-- No unvalidated predecessor exception remains in the resulting schema.
ALTER TABLE federation_source_revisions VALIDATE CONSTRAINT federation_source_revisions_update_mode_check;
ALTER TABLE federation_source_revisions VALIDATE CONSTRAINT federation_source_revisions_registry_list_url_check;
ALTER TABLE observation_occurrences VALIDATE CONSTRAINT observation_occurrences_complete_location_check;
ALTER TABLE observation_occurrences VALIDATE CONSTRAINT observation_occurrences_normalization_input_digest_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_identity_digest_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_semantic_digest_format_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_display_pair_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_description_pair_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_tags_pair_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_queries_pair_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_publisher_pair_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_vector_pair_check;

DROP INDEX source_inspections_housekeeping;
ALTER TABLE source_inspections DROP COLUMN cleanup_tracking_version;
CREATE INDEX source_inspections_housekeeping ON source_inspections (expires_at,tenant_id,id);

CREATE OR REPLACE FUNCTION guard_source_inspection_history() RETURNS trigger
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
  IF OLD.expires_at<=clock_timestamp() AND NOT referenced THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'source inspection remains retained or authoritative';
END
$function$;


CREATE OR REPLACE FUNCTION guard_source_maintenance_state_delete() RETURNS trigger
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
            WHEN job.payload_version=2
             AND jsonb_typeof(job.payload)=''object''
             AND job.payload-ARRAY[''sourceId'',''sourceRevision'',''trigger'']=''{}''::jsonb
             AND jsonb_typeof(job.payload->''sourceId'')=''string''
             AND coalesce(job.payload->>''sourceId'','''') ~ ''^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$''
             AND jsonb_typeof(job.payload->''sourceRevision'')=''number''
             AND coalesce(job.payload->>''sourceRevision'','''') ~ ''^[1-9][0-9]{0,17}$''
             AND jsonb_typeof(job.payload->''trigger'')=''string''
             AND job.payload->>''trigger'' IN (''manual'',''automatic'')
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


ALTER TABLE jobs ADD CONSTRAINT jobs_source_sync_current_payload CHECK (
  kind<>'source.sync' OR coalesce(
    payload_version=2 AND jsonb_typeof(payload)='object'
    AND payload-ARRAY['sourceId','sourceRevision','trigger']='{}'::jsonb
    AND jsonb_typeof(payload->'sourceId')='string'
    AND payload->>'sourceId' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    AND jsonb_typeof(payload->'sourceRevision')='number'
    AND payload->>'sourceRevision' ~ '^[1-9][0-9]{0,17}$'
    AND jsonb_typeof(payload->'trigger')='string'
    AND payload->>'trigger' IN ('manual','automatic'),false)
);
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_current_public_reference CHECK (
  is_internal OR coalesce(
    jsonb_typeof(data)='object' AND data-ARRAY['action','targetType','targetId','digest','host']='{}'::jsonb
    AND jsonb_typeof(data->'action')='string'
    AND event_type='com.open.agentic.catalog.'||(data->>'action')||'.v1'
    AND jsonb_typeof(data->'targetType')='string' AND length(data->>'targetType') BETWEEN 1 AND 160
    AND jsonb_typeof(data->'targetId')='string' AND data->>'targetId'=subject
    AND (NOT (data ? 'digest') OR jsonb_typeof(data->'digest')='string' AND data->>'digest' ~ '^sha256:[0-9a-f]{64}$')
    AND (NOT (data ? 'host') OR jsonb_typeof(data->'host')='string' AND length(data->>'host') BETWEEN 1 AND 253),false)
);
