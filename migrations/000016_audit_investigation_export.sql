ALTER TABLE audit_events
  ADD COLUMN format_version integer,
  ADD CONSTRAINT audit_events_format_version CHECK (format_version IS NULL OR format_version = 1);

CREATE TABLE audit_legacy_inventory (
  event_id uuid PRIMARY KEY REFERENCES audit_events(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  exception_codes text[] NOT NULL CHECK (cardinality(exception_codes) > 0 AND cardinality(exception_codes) <= 16),
  estimated_record_bytes bigint NOT NULL CHECK (estimated_record_bytes >= 0),
  inventoried_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
  UNIQUE (tenant_id,event_id)
);

INSERT INTO audit_legacy_inventory(event_id,tenant_id,exception_codes,estimated_record_bytes)
SELECT event.id,event.tenant_id,
       array_remove(ARRAY[
         CASE WHEN octet_length(event.actor) > 512 THEN 'actor_bytes' END,
         CASE WHEN octet_length(event.action) > 160 THEN 'action_bytes' END,
         CASE WHEN octet_length(event.target_type) > 160 THEN 'target_type_bytes' END,
         CASE WHEN octet_length(event.target_id) > 512 THEN 'target_id_bytes' END,
         CASE WHEN char_length(event.reason) > 2000 OR event.reason <> btrim(event.reason) OR event.reason ~ '[[:cntrl:]]' THEN 'reason_shape' END,
         CASE WHEN octet_length(event.request_id) > 128 THEN 'request_id_bytes' END,
         CASE WHEN event.before_digest <> '' AND event.before_digest !~ '^sha256:[0-9a-f]{64}$' THEN 'before_digest_shape' END,
         CASE WHEN event.after_digest <> '' AND event.after_digest !~ '^sha256:[0-9a-f]{64}$' THEN 'after_digest_shape' END,
         CASE WHEN jsonb_typeof(event.details) <> 'object' OR octet_length(event.details::text) > 16384 THEN 'details_shape' END,
         CASE WHEN 512::bigint + octet_length(event.actor) + octet_length(event.action) + octet_length(event.target_type) + octet_length(event.target_id) + octet_length(event.reason) + octet_length(event.request_id) + octet_length(event.before_digest) + octet_length(event.after_digest) + octet_length(event.details::text) > 65536 THEN 'estimated_record_bytes' END
       ],NULL),
       512::bigint + octet_length(event.actor) + octet_length(event.action) + octet_length(event.target_type) + octet_length(event.target_id) + octet_length(event.reason) + octet_length(event.request_id) + octet_length(event.before_digest) + octet_length(event.after_digest) + octet_length(event.details::text)
  FROM audit_events event
 WHERE event.format_version IS NULL
   AND (
     octet_length(event.actor) > 512 OR octet_length(event.action) > 160 OR octet_length(event.target_type) > 160 OR octet_length(event.target_id) > 512 OR
     char_length(event.reason) > 2000 OR event.reason <> btrim(event.reason) OR event.reason ~ '[[:cntrl:]]' OR octet_length(event.request_id) > 128 OR
     (event.before_digest <> '' AND event.before_digest !~ '^sha256:[0-9a-f]{64}$') OR (event.after_digest <> '' AND event.after_digest !~ '^sha256:[0-9a-f]{64}$') OR
     jsonb_typeof(event.details) <> 'object' OR octet_length(event.details::text) > 16384 OR
     512::bigint + octet_length(event.actor) + octet_length(event.action) + octet_length(event.target_type) + octet_length(event.target_id) + octet_length(event.reason) + octet_length(event.request_id) + octet_length(event.before_digest) + octet_length(event.after_digest) + octet_length(event.details::text) > 65536
   );

ALTER TABLE audit_legacy_inventory ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_legacy_inventory FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_legacy_inventory
  USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

CREATE OR REPLACE FUNCTION guard_audit_event_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF NEW.format_version IS DISTINCT FROM 1 THEN
    RAISE EXCEPTION 'new audit event must use format version 1' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_format_version_required';
  END IF;
  IF octet_length(NEW.actor) NOT BETWEEN 1 AND 512 OR NEW.actor <> btrim(NEW.actor) OR NEW.actor ~ '[[:cntrl:]]' OR
     octet_length(NEW.action) NOT BETWEEN 1 AND 160 OR NEW.action <> btrim(NEW.action) OR NEW.action ~ '[[:cntrl:]]' OR
     octet_length(NEW.target_type) NOT BETWEEN 1 AND 160 OR NEW.target_type <> btrim(NEW.target_type) OR NEW.target_type ~ '[[:cntrl:]]' OR
     octet_length(NEW.target_id) NOT BETWEEN 1 AND 512 OR NEW.target_id <> btrim(NEW.target_id) OR NEW.target_id ~ '[[:cntrl:]]' THEN
    RAISE EXCEPTION 'new audit event indexed text is invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_indexed_text_shape';
  END IF;
  IF char_length(NEW.reason) > 2000 OR NEW.reason <> btrim(NEW.reason) OR NEW.reason ~ '[[:cntrl:]]' THEN
    RAISE EXCEPTION 'new audit event reason is invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_reason_shape';
  END IF;
  IF octet_length(NEW.request_id) > 128 OR NEW.request_id ~ '[^A-Za-z0-9._-]' THEN
    RAISE EXCEPTION 'new audit event request identifier is invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_request_id_shape';
  END IF;
  IF (NEW.before_digest <> '' AND NEW.before_digest !~ '^sha256:[0-9a-f]{64}$') OR (NEW.after_digest <> '' AND NEW.after_digest !~ '^sha256:[0-9a-f]{64}$') THEN
    RAISE EXCEPTION 'new audit event digest is invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_digest_shape';
  END IF;
  IF jsonb_typeof(NEW.details) <> 'object' OR octet_length(NEW.details::text) > 16384 OR NOT (NEW.details ? 'principalType') OR
     jsonb_typeof(NEW.details->'principalType') <> 'string' OR
     (NEW.details ? 'actorType' AND jsonb_typeof(NEW.details->'actorType') <> 'string') OR
     (NEW.details ? 'realmId' AND jsonb_typeof(NEW.details->'realmId') <> 'string') OR
     (NEW.details ? 'authorizationDigest' AND jsonb_typeof(NEW.details->'authorizationDigest') <> 'string') OR
     (NEW.details ? 'commandId' AND jsonb_typeof(NEW.details->'commandId') <> 'string') OR
     (NEW.details ? 'currentUser' AND jsonb_typeof(NEW.details->'currentUser') <> 'string') OR
     (NEW.details ? 'operatorLabel' AND jsonb_typeof(NEW.details->'operatorLabel') <> 'string') OR
     NEW.details - ARRAY['principalType','actorType','realmId','authorizationDigest','commandId','currentUser','operatorLabel','facts']::text[] <> '{}'::jsonb OR
     (NEW.details ? 'facts' AND jsonb_typeof(NEW.details->'facts') <> 'object') THEN
    RAISE EXCEPTION 'new audit event details are invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_details_shape';
  END IF;
  IF NEW.details->>'principalType' = 'catalog_membership' AND
     (NEW.actor !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' OR
      coalesce(NEW.details->>'realmId','') !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' OR
      coalesce(NEW.details->>'actorType','') NOT IN ('human','service') OR
      NEW.details ?| ARRAY['currentUser','operatorLabel']) THEN
    RAISE EXCEPTION 'new Catalog-member audit attribution is invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_membership_attribution';
  END IF;
  IF NEW.details->>'principalType' = 'service_executor' AND
     (NEW.actor !~ '^[A-Za-z0-9._:@/-]+$' OR NEW.details ?| ARRAY['actorType','realmId','currentUser','operatorLabel']) THEN
    RAISE EXCEPTION 'new service-executor audit attribution is invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_service_attribution';
  END IF;
  IF NEW.details->>'principalType' = 'database_login' AND
     (coalesce(NEW.details->>'currentUser','') = '' OR octet_length(NEW.details->>'currentUser') > 512 OR
      NEW.details->>'currentUser' <> btrim(NEW.details->>'currentUser') OR NEW.details->>'currentUser' ~ '[[:cntrl:]]' OR
      NEW.details ?| ARRAY['actorType','realmId','authorizationDigest','commandId']) THEN
    RAISE EXCEPTION 'new database audit attribution is invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_database_attribution';
  END IF;
  IF NEW.details ? 'operatorLabel' AND
     (coalesce(NEW.details->>'operatorLabel','') = '' OR octet_length(NEW.details->>'operatorLabel') > 512 OR
      NEW.details->>'operatorLabel' <> btrim(NEW.details->>'operatorLabel') OR NEW.details->>'operatorLabel' ~ '[[:cntrl:]]') THEN
    RAISE EXCEPTION 'new audit operator label is invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_operator_label';
  END IF;
  IF NEW.details->>'principalType' NOT IN ('catalog_membership','service_executor','database_login') THEN
    RAISE EXCEPTION 'new audit principal type is invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_principal_type';
  END IF;
  IF (NEW.details ? 'authorizationDigest' AND NEW.details->>'authorizationDigest' !~ '^sha256:[0-9a-f]{64}$') OR
     (NEW.details ? 'commandId' AND NEW.details->>'commandId' !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') THEN
    RAISE EXCEPTION 'new audit execution attribution is invalid' USING ERRCODE = '23514', CONSTRAINT = 'audit_events_execution_attribution';
  END IF;
  RETURN NEW;
END
$function$;

REVOKE ALL ON FUNCTION guard_audit_event_insert() FROM PUBLIC;

CREATE TRIGGER audit_event_insert_guard
BEFORE INSERT ON audit_events
FOR EACH ROW EXECUTE FUNCTION guard_audit_event_insert();

-- Legacy rows predate the version-1 text ceilings and must not be rewritten.
-- A value outside the matching API filter ceiling cannot equal a valid filter,
-- so partial indexes safely omit it while retaining native exact equality.
CREATE INDEX audit_tenant_actor_time ON audit_events (tenant_id,actor,occurred_at DESC,id DESC) WHERE octet_length(actor) <= 512;
CREATE INDEX audit_tenant_action_time ON audit_events (tenant_id,action,occurred_at DESC,id DESC) WHERE octet_length(action) <= 160;
CREATE INDEX audit_tenant_request_time ON audit_events (tenant_id,request_id,occurred_at DESC,id DESC) WHERE request_id <> '' AND octet_length(request_id) <= 128;
CREATE INDEX audit_tenant_target_time ON audit_events (tenant_id,target_type,target_id,occurred_at DESC,id DESC) WHERE octet_length(target_type) <= 160 AND octet_length(target_id) <= 512;
