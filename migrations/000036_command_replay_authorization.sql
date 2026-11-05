-- Pre-release authority cutover cannot reinterpret an outstanding command.
-- Drain runtimes, resolve unfinished reservations through supported repair,
-- and wait for completed/released keys to expire before applying this migration.
DO $cutover$
BEGIN
 IF EXISTS (SELECT 1 FROM idempotency_keys
            WHERE expires_at > clock_timestamp()
               OR (atomic_response_status IS NULL AND released_at IS NULL)) THEN
   RAISE EXCEPTION 'authorization cutover requires expired completed or released command reservations; drain runtimes and resolve unfinished reservations first';
 END IF;
END
$cutover$;

-- Retained replay disclosure is server-owned and completes atomically with bytes.
ALTER TABLE idempotency_keys ADD COLUMN replay_authorization jsonb;
ALTER TABLE idempotency_keys ADD CONSTRAINT replay_authorization_bounded CHECK (
 replay_authorization IS NULL OR (jsonb_typeof(replay_authorization)='object' AND octet_length(replay_authorization::text)<=16384));
COMMENT ON COLUMN idempotency_keys.replay_authorization IS 'Bounded original permission/scope checks for fresh replay disclosure; never retry-body authority.';

CREATE OR REPLACE FUNCTION guard_idempotency_key_update() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(NEW.tenant_id,NEW.actor,NEW.route,NEW.idempotency_key)
       IS DISTINCT FROM ROW(OLD.tenant_id,OLD.actor,OLD.route,OLD.idempotency_key) THEN
    RAISE EXCEPTION 'idempotency key scope is immutable';
  END IF;

  IF OLD.protocol_version = 2 AND OLD.atomic_response_status IS NULL AND OLD.released_at IS NULL AND
     NEW.atomic_response_status IS NOT NULL AND
     (to_jsonb(NEW) - ARRAY['atomic_response_status','atomic_response_headers','atomic_response_body','replay_authorization','completed_at']) =
       (to_jsonb(OLD) - ARRAY['atomic_response_status','atomic_response_headers','atomic_response_body','replay_authorization','completed_at']) THEN
    RETURN NEW;
  END IF;

  IF OLD.completed_at IS NOT NULL AND OLD.expires_at <= clock_timestamp() AND
     NEW.protocol_version = 2 AND NEW.atomic_response_status IS NULL AND
     NEW.completed_at IS NULL AND NEW.released_at IS NULL AND NEW.repair_reason IS NULL AND
     NEW.authorization_digest IS NOT NULL AND NEW.execution_token IS NOT NULL AND
     (to_jsonb(NEW) - ARRAY[
       'request_digest','expires_at','created_at',
       'protocol_version','authorization_digest','reservation_id','execution_token','started_at','completed_at',
       'atomic_response_status','atomic_response_headers','atomic_response_body','replay_authorization'
     ]) = (to_jsonb(OLD) - ARRAY[
       'request_digest','expires_at','created_at',
       'protocol_version','authorization_digest','reservation_id','execution_token','started_at','completed_at',
       'atomic_response_status','atomic_response_headers','atomic_response_body','replay_authorization'
     ]) AND
     ROW(NEW.request_digest,NEW.expires_at,NEW.created_at,NEW.reservation_id,NEW.authorization_digest,NEW.execution_token,NEW.started_at)
       IS DISTINCT FROM ROW(OLD.request_digest,OLD.expires_at,OLD.created_at,OLD.reservation_id,OLD.authorization_digest,OLD.execution_token,OLD.started_at) THEN
    RETURN NEW;
  END IF;

  IF OLD.protocol_version=2 AND OLD.released_at IS NOT NULL AND OLD.atomic_response_status IS NULL AND
     NEW.protocol_version=2 AND NEW.atomic_response_status IS NULL AND
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


-- Current-format cutover preserves the old tenant-wide authority as explicit
-- companion assignments. A namespace-only grant never receives these bundles.
INSERT INTO role_assignments(id,tenant_id,membership_id,role,namespace_id,state,revision,granted_by,grant_reason,granted_at,role_definition_id,realm_id)
SELECT uuidv7(),a.tenant_id,a.membership_id,d.key,NULL,'active',1,a.granted_by,
       'Explicit authorization bundle split',a.granted_at,d.id,a.realm_id
FROM role_assignments a
CROSS JOIN LATERAL unnest(CASE a.role
 WHEN 'reader' THEN ARRAY['discovery_reader','feedback_member']
 WHEN 'contributor' THEN ARRAY['discovery_reader','feedback_member','publisher_creator']
 WHEN 'publisher' THEN ARRAY['discovery_reader','feedback_member','publication_admin']
 ELSE ARRAY[]::text[] END) AS companion(key)
JOIN role_definitions d ON d.tenant_id=a.tenant_id AND d.key=companion.key AND d.builtin
WHERE a.state='active' AND a.namespace_id IS NULL AND a.membership_id IS NOT NULL
ON CONFLICT DO NOTHING;
