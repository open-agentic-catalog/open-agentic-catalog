-- Stopped-runtime operator maintenance may rewrap signing material without
-- changing endpoint identity, revision, signer version, or lifecycle history.
-- Runtime roles retain their existing exact manifest v5 grants. Neither owns
-- this table, and neither may take this exception.
CREATE OR REPLACE FUNCTION guard_webhook_endpoint_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF NEW.encrypted_secret IS DISTINCT FROM OLD.encrypted_secret AND
     octet_length(NEW.encrypted_secret) BETWEEN 29 AND 4096 AND
     (to_jsonb(NEW) - 'encrypted_secret') = (to_jsonb(OLD) - 'encrypted_secret') AND
     current_user = (SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid = TG_RELID) THEN
    RETURN NEW;
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.created_by,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.created_by,OLD.created_at) OR
     NEW.revision <> OLD.revision+1 OR NEW.updated_at < OLD.updated_at OR
     NEW.secret_version NOT IN (OLD.secret_version,OLD.secret_version+1) OR
     (NEW.secret_version = OLD.secret_version) <> (NEW.encrypted_secret = OLD.encrypted_secret) OR
     ROW(NEW.name,NEW.url,NEW.event_types,NEW.status,NEW.secret_version,NEW.encrypted_secret)
       IS NOT DISTINCT FROM ROW(OLD.name,OLD.url,OLD.event_types,OLD.status,OLD.secret_version,OLD.encrypted_secret) THEN
    RAISE EXCEPTION 'webhook endpoint lifecycle transition is invalid';
  END IF;
  IF OLD.status = 'disabled' AND NEW.status <> OLD.status THEN
    RAISE EXCEPTION 'disabled webhook endpoint is terminal';
  END IF;
  RETURN NEW;
END
$function$;
