-- Current-format-only first-release command persistence. No conversion or deletion.
DO $current$
BEGIN
  IF EXISTS (SELECT 1 FROM idempotency_keys WHERE protocol_version <> 2) THEN
    RAISE EXCEPTION 'unsupported pre-release idempotency state; keep runtimes stopped and use a fresh explicitly selected database';
  END IF;
END
$current$;

DROP INDEX idempotency_pending_repair;
DROP INDEX idempotency_housekeeping;
ALTER TABLE idempotency_keys
  DROP CONSTRAINT idempotency_protocol_version_valid,
  DROP CONSTRAINT idempotency_authorization_digest_valid,
  DROP CONSTRAINT idempotency_execution_token_required,
  DROP CONSTRAINT idempotency_response_protocol_consistent,
  DROP CONSTRAINT idempotency_completion_consistent,
  DROP CONSTRAINT idempotency_release_consistent,
  DROP COLUMN response_status,
  DROP COLUMN response_headers,
  DROP COLUMN response_body,
  ALTER COLUMN protocol_version SET DEFAULT 2,
  ALTER COLUMN authorization_digest SET NOT NULL,
  ADD CONSTRAINT idempotency_protocol_version_valid CHECK (protocol_version = 2),
  ADD CONSTRAINT idempotency_authorization_digest_valid CHECK (authorization_digest ~ '^sha256:[0-9a-f]{64}$'),
  ADD CONSTRAINT idempotency_execution_token_required CHECK ((execution_token IS NOT NULL) = (released_at IS NULL)),
  ADD CONSTRAINT idempotency_completion_consistent CHECK ((atomic_response_status IS NULL) = (completed_at IS NULL)),
  ADD CONSTRAINT idempotency_release_consistent CHECK (
    released_at IS NULL AND repair_reason IS NULL
    OR released_at IS NOT NULL AND execution_token IS NULL AND atomic_response_status IS NULL
       AND repair_reason IS NOT NULL AND length(btrim(repair_reason)) BETWEEN 1 AND 1000
  );
CREATE INDEX idempotency_pending_repair
  ON idempotency_keys (tenant_id, started_at, reservation_id)
  WHERE released_at IS NULL AND protocol_version = 2 AND atomic_response_status IS NULL;
CREATE INDEX idempotency_housekeeping
  ON idempotency_keys (expires_at, tenant_id, reservation_id)
  WHERE protocol_version = 2 AND (atomic_response_status IS NOT NULL OR released_at IS NOT NULL);

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
     (to_jsonb(NEW) - ARRAY['atomic_response_status','atomic_response_headers','atomic_response_body','completed_at']) =
       (to_jsonb(OLD) - ARRAY['atomic_response_status','atomic_response_headers','atomic_response_body','completed_at']) THEN
    RETURN NEW;
  END IF;

  IF OLD.completed_at IS NOT NULL AND OLD.expires_at <= clock_timestamp() AND
     NEW.protocol_version = 2 AND NEW.atomic_response_status IS NULL AND
     NEW.completed_at IS NULL AND NEW.released_at IS NULL AND NEW.repair_reason IS NULL AND
     NEW.authorization_digest IS NOT NULL AND NEW.execution_token IS NOT NULL AND
     (to_jsonb(NEW) - ARRAY[
       'request_digest','expires_at','created_at',
       'protocol_version','authorization_digest','reservation_id','execution_token','started_at','completed_at',
       'atomic_response_status','atomic_response_headers','atomic_response_body'
     ]) = (to_jsonb(OLD) - ARRAY[
       'request_digest','expires_at','created_at',
       'protocol_version','authorization_digest','reservation_id','execution_token','started_at','completed_at',
       'atomic_response_status','atomic_response_headers','atomic_response_body'
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

CREATE OR REPLACE FUNCTION guard_idempotency_key_delete() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF OLD.expires_at<=clock_timestamp() AND (
       OLD.protocol_version=2 AND (OLD.atomic_response_status IS NOT NULL OR OLD.released_at IS NOT NULL)
     ) THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'idempotency key cannot be deleted while replay or recovery remains authoritative';
END
$function$;

COMMENT ON COLUMN idempotency_keys.protocol_version IS 'Current atomic command protocol; unsupported development protocols are refused.';
COMMENT ON COLUMN idempotency_keys.atomic_response_status IS 'Response committed atomically with every PostgreSQL command mutation.';
