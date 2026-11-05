#!/bin/sh
set -eu

: "${CATALOG_FUNCTIONAL_PROJECT:?functional project is required}"
: "${CATALOG_FUNCTIONAL_IMAGE:?functional image is required}"
: "${CATALOG_FUNCTIONAL_FIXTURE_IMAGE:?functional fixture image is required}"
: "${CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY:?functional fixture directory is required}"
: "${CATALOG_FUNCTIONAL_SUBNET:?functional subnet is required}"
: "${CATALOG_FUNCTIONAL_HTTP_PORT:?functional HTTP port is required}"
: "${CATALOG_FUNCTIONAL_FIXTURE_PORT:?functional fixture port is required}"

compose_command=${CATALOG_FUNCTIONAL_COMPOSE_COMMAND:-docker compose}
tenant_id=00000000-0000-7000-8000-000000000001

catalog_compose() {
	# Intentional splitting preserves the supported multi-word COMPOSE override.
	# shellcheck disable=SC2086
	IMAGE="$CATALOG_FUNCTIONAL_IMAGE" VERSION=functional COMMIT="${CATALOG_FUNCTIONAL_COMMIT:?}" \
		CATALOG_COMPOSE_HTTP_PORT="$CATALOG_FUNCTIONAL_HTTP_PORT" CATALOG_COMPOSE_POSTGRES_PORT=0 \
		CATALOG_COMPOSE_FIXTURE_PORT="$CATALOG_FUNCTIONAL_FIXTURE_PORT" CATALOG_COMPOSE_SUBNET="$CATALOG_FUNCTIONAL_SUBNET" \
		CATALOG_COMPOSE_FEDERATION_POLICY_FILE="$CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY/policy.json" \
		CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY="$CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY/trust" \
		CATALOG_HTTP_REQUEST_TIMEOUT=3s CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL=1m \
		CATALOG_WORKER_HOUSEKEEPING_INTERVAL=1m CATALOG_WORKER_TERMINAL_JOB_RETENTION=24h \
		CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION=1h \
		$compose_command --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml \
		-p "$CATALOG_FUNCTIONAL_PROJECT" "$@"
}

psql_catalog() {
	catalog_compose exec -T postgres psql --username catalog --dbname catalog --set ON_ERROR_STOP=1 --quiet "$@"
}

scalar() {
	psql_catalog --tuples-only --no-align --command "$1"
}

# Stop the worker before constructing deliberately aged disposable fixtures so
# this test proves that its next process startup performs a complete fair cycle.
catalog_compose stop --timeout 20 worker >/dev/null

psql_catalog <<'SQL'
INSERT INTO idempotency_keys(
  tenant_id,actor,route,idempotency_key,request_digest,atomic_response_status,atomic_response_headers,atomic_response_body,
  expires_at,created_at,protocol_version,authorization_digest,execution_token,started_at,completed_at
) VALUES (
  '00000000-0000-7000-8000-000000000001','functional-housekeeping','POST /functional-housekeeping',
  'expired-completed','sha256:functional-housekeeping',204,'{}','',
  clock_timestamp()-interval '2 hours',clock_timestamp()-interval '3 hours',2,'sha256:'||repeat('b',64),uuidv7(),
  clock_timestamp()-interval '3 hours',clock_timestamp()-interval '2 hours'
);
INSERT INTO idempotency_keys(
  tenant_id,actor,route,idempotency_key,request_digest,
  expires_at,created_at,protocol_version,authorization_digest,execution_token,started_at
) VALUES (
  '00000000-0000-7000-8000-000000000001','functional-housekeeping','POST /functional-housekeeping',
  'expired-pending','sha256:functional-housekeeping',
  clock_timestamp()-interval '2 hours',clock_timestamp()-interval '3 hours',2,'sha256:'||repeat('b',64),uuidv7(),
  clock_timestamp()-interval '3 hours'
);

WITH stamp AS (
  SELECT clock_timestamp()-interval '2 hours' AS created_at,
         clock_timestamp()-interval '1 hour' AS expires_at
), fixtures(id) AS (
  VALUES
    ('00000000-0000-7000-8000-00000000f181'::uuid),
    ('00000000-0000-7000-8000-00000000f182'::uuid)
)
INSERT INTO source_inspections(id,tenant_id,document,expires_at,created_at)
SELECT id,'00000000-0000-7000-8000-000000000001',
       jsonb_build_object(
         'id',id::text,
         'tenantId','00000000-0000-7000-8000-000000000001',
         'requestedUrl','https://source.example/ard.json',
         'resolvedUrl','https://source.example/ard.json',
         'sourceType','ard_manifest',
         'profileId','ard-0.91-aa3e598',
         'mediaType','application/json',
         'bodyDigest','sha256:'||repeat('a',64),
         'entryCount',0,
         'kindCounts','{}'::jsonb,
         'publishers','[]'::jsonb,
         'tags','[]'::jsonb,
         'capabilities','{}'::jsonb,
         'policyId','functional-housekeeping',
         'policyRevision',1,
         'policyDigest','sha256:'||repeat('a',64),
         'expiresAt',CASE WHEN id='00000000-0000-7000-8000-00000000f182'::uuid THEN statement_timestamp()+interval '1 hour' ELSE expires_at END,
         'createdAt',created_at
       ),
       CASE WHEN id='00000000-0000-7000-8000-00000000f182'::uuid THEN statement_timestamp()+interval '1 hour' ELSE expires_at END,created_at
  FROM fixtures CROSS JOIN stamp;

INSERT INTO source_change_previews(id,tenant_id,source_id,digest,document,expires_at,created_at)
SELECT '00000000-0000-7000-8000-00000000f183',tenant_id,id,'functional-housekeeping-expired-preview','{}',
       clock_timestamp()-interval '1 hour',clock_timestamp()-interval '2 hours'
  FROM federation_sources
 WHERE tenant_id='00000000-0000-7000-8000-000000000001'
 ORDER BY created_at DESC LIMIT 1;

WITH stamp AS (SELECT clock_timestamp()-interval '3 hours' AS value)
INSERT INTO jobs(
  id,tenant_id,kind,actor,payload_version,payload,state,priority,progress,not_before,
  attempt,attempt_limit,deduplication_key,created_at,updated_at
) SELECT
  '00000000-0000-7000-8000-00000000f184','00000000-0000-7000-8000-000000000001',
  'publication.generate','functional-housekeeping',1,'{}','queued',0,0,
  value,0,1,'functional-housekeeping-terminal-job',value,value
FROM stamp;
UPDATE jobs SET state='cancelled',updated_at=clock_timestamp()-interval '2 hours'
 WHERE id='00000000-0000-7000-8000-00000000f184';

INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,is_internal,created_at,published_at)
VALUES (
  '00000000-0000-7000-8000-00000000f185','00000000-0000-7000-8000-000000000001',
  'com.open.agentic.catalog.source.removed.v1','urn:functional:housekeeping','{}',true,
  clock_timestamp()-interval '3 hours',clock_timestamp()-interval '2 hours'
);

DO $block$
DECLARE
  source_uuid uuid;
  old_revision bigint;
  next_configuration jsonb;
  transitioned_at timestamptz;
BEGIN
  SELECT id,revision,updated_at INTO STRICT source_uuid,old_revision,transitioned_at
    FROM federation_sources
   WHERE tenant_id='00000000-0000-7000-8000-000000000001'
     AND display_name LIKE 'Functional automatic Registry functional-%'
     AND status<>'removed'
   ORDER BY created_at DESC LIMIT 1 FOR UPDATE;

  -- The worker has drained, so make any previously admitted synchronization
  -- work terminal before turning its exact revision into obsolete test state.
  -- This keeps the fixture deterministic without bypassing a live lease.
  UPDATE jobs
     SET state='cancelled',lease_owner=NULL,lease_expires_at=NULL,updated_at=clock_timestamp()
   WHERE tenant_id='00000000-0000-7000-8000-000000000001'
     AND kind='source.sync' AND state IN ('queued','running','waiting')
     AND payload->>'sourceId'=source_uuid::text;

  -- Container and application clocks need not agree. Preserve the retained
  -- monotonic timestamp rather than weakening the source lifecycle guard.
  transitioned_at := GREATEST(clock_timestamp(),transitioned_at);
  SELECT jsonb_set(
           jsonb_set(
             jsonb_set(source.configuration,'{revision}',to_jsonb(old_revision+1),false),
             '{status}',to_jsonb('removed'::text),false
           ),
           '{updatedAt}',to_jsonb(transitioned_at),false
         )
    INTO STRICT next_configuration
    FROM federation_sources source
   WHERE source.tenant_id='00000000-0000-7000-8000-000000000001'
     AND source.id=source_uuid;

  INSERT INTO federation_source_revisions(
    tenant_id,source_id,revision,retrieval_mode,update_mode,registry_list_url,policy_id,policy_revision,policy_digest,
    credential_revision,configuration_digest,configuration,recorded_at,inspection_id
  )
  SELECT revision.tenant_id,revision.source_id,old_revision+1,revision.retrieval_mode,revision.update_mode,revision.registry_list_url,
         revision.policy_id,revision.policy_revision,revision.policy_digest,credential_revision,
         'sha256:'||encode(digest(next_configuration::text,'sha256'),'hex'),next_configuration,transitioned_at,NULL
    FROM federation_source_revisions revision
   WHERE revision.tenant_id='00000000-0000-7000-8000-000000000001'
     AND revision.source_id=source_uuid AND revision.revision=old_revision;

  INSERT INTO federation_source_audiences(tenant_id,source_id,source_revision,discovery_host,audience)
  SELECT audience_row.tenant_id,audience_row.source_id,old_revision+1,audience_row.discovery_host,audience_row.audience
    FROM federation_source_audiences audience_row
   WHERE audience_row.tenant_id='00000000-0000-7000-8000-000000000001'
     AND audience_row.source_id=source_uuid AND audience_row.source_revision=old_revision;

  INSERT INTO federation_source_referrals(tenant_id,source_id,source_revision,target_source_id)
  SELECT referral.tenant_id,referral.source_id,old_revision+1,referral.target_source_id
    FROM federation_source_referrals referral
   WHERE referral.tenant_id='00000000-0000-7000-8000-000000000001'
     AND referral.source_id=source_uuid AND referral.source_revision=old_revision;

  UPDATE federation_sources
     SET status='removed',revision=old_revision+1,configuration=next_configuration,updated_at=transitioned_at
   WHERE tenant_id='00000000-0000-7000-8000-000000000001' AND id=source_uuid;

  UPDATE observations
     SET freshness='source_removed'
   WHERE tenant_id='00000000-0000-7000-8000-000000000001'
     AND source_id=source_uuid AND freshness<>'source_removed';
END
$block$;
SQL

fixtures_ready=$(scalar "
SELECT
  EXISTS (SELECT 1 FROM idempotency_keys WHERE tenant_id='$tenant_id' AND idempotency_key='expired-completed')
  AND EXISTS (SELECT 1 FROM idempotency_keys WHERE tenant_id='$tenant_id' AND idempotency_key='expired-pending')
  AND EXISTS (SELECT 1 FROM source_inspections WHERE tenant_id='$tenant_id' AND id='00000000-0000-7000-8000-00000000f181' AND expires_at<clock_timestamp())
  AND EXISTS (SELECT 1 FROM source_inspections WHERE tenant_id='$tenant_id' AND id='00000000-0000-7000-8000-00000000f182' AND expires_at>clock_timestamp())
  AND EXISTS (SELECT 1 FROM source_change_previews WHERE tenant_id='$tenant_id' AND id='00000000-0000-7000-8000-00000000f183')
  AND EXISTS (SELECT 1 FROM jobs WHERE tenant_id='$tenant_id' AND id='00000000-0000-7000-8000-00000000f184' AND state='cancelled' AND cleanup_after IS NULL)
  AND EXISTS (SELECT 1 FROM outbox_events WHERE tenant_id='$tenant_id' AND id='00000000-0000-7000-8000-00000000f185' AND published_at IS NOT NULL AND cleanup_after IS NULL)")
[ "$fixtures_ready" = t ] || { printf '%s\n' 'Functional housekeeping fixtures were not constructed exactly.' >&2; exit 1; }

obsolete_before=$(scalar "
SELECT
  (SELECT count(*) FROM source_retrieval_states state JOIN federation_sources source ON source.tenant_id=state.tenant_id AND source.id=state.source_id WHERE state.tenant_id='$tenant_id' AND (source.status='removed' OR source.revision<>state.source_revision)) +
  (SELECT count(*) FROM source_maintenance_state state JOIN federation_sources source ON source.tenant_id=state.tenant_id AND source.id=state.source_id WHERE state.tenant_id='$tenant_id' AND (source.status='removed' OR source.revision<>state.source_revision))")
[ "$obsolete_before" -gt 0 ] || { printf '%s\n' 'Functional federation workflow produced no obsolete coordination state.' >&2; exit 1; }

audit_before=$(scalar "
SELECT count(*) FROM audit_events
 WHERE tenant_id='$tenant_id'
   AND (
     actor='functional-housekeeping'
     OR action LIKE '%housekeep%'
     OR target_id IN (
       '00000000-0000-7000-8000-00000000f181',
       '00000000-0000-7000-8000-00000000f182',
       '00000000-0000-7000-8000-00000000f183',
       '00000000-0000-7000-8000-00000000f184',
       '00000000-0000-7000-8000-00000000f185'
     )
   )")
outbox_before=$(scalar "SELECT count(*) FROM outbox_events WHERE tenant_id='$tenant_id' AND subject='urn:functional:housekeeping'")
catalog_compose start worker >/dev/null
attempt=0
while [ "$attempt" -lt 60 ]; do
	complete=$(scalar "
SELECT
  NOT EXISTS (SELECT 1 FROM idempotency_keys WHERE tenant_id='$tenant_id' AND idempotency_key='expired-completed')
  AND EXISTS (SELECT 1 FROM idempotency_keys WHERE tenant_id='$tenant_id' AND idempotency_key='expired-pending')
  AND NOT EXISTS (SELECT 1 FROM source_inspections WHERE tenant_id='$tenant_id' AND id='00000000-0000-7000-8000-00000000f181')
  AND EXISTS (SELECT 1 FROM source_inspections WHERE tenant_id='$tenant_id' AND id='00000000-0000-7000-8000-00000000f182')
  AND NOT EXISTS (SELECT 1 FROM source_change_previews WHERE tenant_id='$tenant_id' AND id='00000000-0000-7000-8000-00000000f183')
  AND EXISTS (SELECT 1 FROM jobs WHERE tenant_id='$tenant_id' AND id='00000000-0000-7000-8000-00000000f184' AND cleanup_after BETWEEN clock_timestamp()+interval '23 hours' AND clock_timestamp()+interval '25 hours')
  AND EXISTS (SELECT 1 FROM outbox_events WHERE tenant_id='$tenant_id' AND id='00000000-0000-7000-8000-00000000f185' AND cleanup_after BETWEEN clock_timestamp()+interval '50 minutes' AND clock_timestamp()+interval '70 minutes')
  AND NOT EXISTS (
    SELECT 1 FROM source_retrieval_states state JOIN federation_sources source ON source.tenant_id=state.tenant_id AND source.id=state.source_id
     WHERE state.tenant_id='$tenant_id' AND (source.status='removed' OR source.revision<>state.source_revision)
  )
  AND NOT EXISTS (
    SELECT 1 FROM source_maintenance_state state JOIN federation_sources source ON source.tenant_id=state.tenant_id AND source.id=state.source_id
     WHERE state.tenant_id='$tenant_id' AND (source.status='removed' OR source.revision<>state.source_revision)
  )")
	if [ "$complete" = t ]; then
		break
	fi
	attempt=$((attempt + 1))
	sleep 1
done
[ "${complete:-f}" = t ] || { printf '%s\n' 'Automatic housekeeping did not complete every functional fixture class.' >&2; exit 1; }

audit_after=$(scalar "
SELECT count(*) FROM audit_events
 WHERE tenant_id='$tenant_id'
   AND (
     actor='functional-housekeeping'
     OR action LIKE '%housekeep%'
     OR target_id IN (
       '00000000-0000-7000-8000-00000000f181',
       '00000000-0000-7000-8000-00000000f182',
       '00000000-0000-7000-8000-00000000f183',
       '00000000-0000-7000-8000-00000000f184',
       '00000000-0000-7000-8000-00000000f185'
     )
   )")
[ "$audit_after" = "$audit_before" ] || { printf '%s\n' "Routine housekeeping added audit evidence for a cleanup fixture." >&2; exit 1; }
outbox_after=$(scalar "SELECT count(*) FROM outbox_events WHERE tenant_id='$tenant_id' AND subject='urn:functional:housekeeping'")
[ "$outbox_after" = "$outbox_before" ] || { printf '%s\n' "Routine housekeeping added an outbox event for a cleanup fixture." >&2; exit 1; }
catalog_compose exec -T worker /usr/local/bin/catalogctl probe --url http://127.0.0.1:9090/readyz >/dev/null

printf '%s\n' 'Automatic bounded housekeeping reclaimed disposable fixtures and retained protected state.'
