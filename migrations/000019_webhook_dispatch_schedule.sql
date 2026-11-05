-- Drain old API and worker binaries before reconciling manifest v5.
-- Payload and acknowledgement history remain immutable.
ALTER TABLE outbox_events ADD COLUMN dispatch_after timestamptz;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_dispatch_after_check
  CHECK (dispatch_after IS NULL OR dispatch_after>=created_at);
CREATE INDEX outbox_events_dispatch_ready
  ON outbox_events ((coalesce(dispatch_after,created_at)),id)
  WHERE published_at IS NULL;

CREATE OR REPLACE FUNCTION guard_outbox_event_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  has_delivery boolean;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL OR NEW.cleanup_after IS NOT NULL OR NEW.dispatch_after IS NOT NULL OR
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
  IF NEW.dispatch_after IS DISTINCT FROM OLD.dispatch_after AND NOT (
    OLD.lease_owner IS NOT NULL AND NEW.lease_owner IS NULL AND NEW.published_at IS NULL AND
    NEW.dispatch_after IS NOT NULL AND NEW.dispatch_after>=OLD.created_at AND
    NEW.dispatch_after<=OLD.lease_expires_at+interval '1 hour'
  ) THEN
    RAISE EXCEPTION 'outbox dispatch scheduling transition is invalid';
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
