-- Disposable last-attempt evidence has a different lifetime from the audit
-- record. Never remove recipient suppression while the parent can run again.
ALTER TABLE webhook_deliveries ADD COLUMN cleanup_after timestamptz;
CREATE INDEX webhook_deliveries_cleanup_mark_idx
  ON webhook_deliveries(updated_at,tenant_id,id) WHERE cleanup_after IS NULL;
CREATE INDEX webhook_deliveries_cleanup_delete_idx
  ON webhook_deliveries(cleanup_after,tenant_id,id) WHERE cleanup_after IS NOT NULL;

CREATE OR REPLACE FUNCTION guard_webhook_delivery_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  disposable boolean;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.cleanup_after IS NOT NULL THEN
      RAISE EXCEPTION 'new webhook delivery cannot have a cleanup marker';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP = 'DELETE' OR NEW.cleanup_after IS DISTINCT FROM OLD.cleanup_after THEN
    -- Published outbox state is terminal. A retained delivery also prevents
    -- its parent from being reclaimed, so this cannot race parent removal.
    EXECUTE format(
      'SELECT EXISTS (SELECT 1 FROM %I.outbox_events WHERE tenant_id=$1 AND id=$2 AND event_type=$3 AND published_at IS NOT NULL AND lease_owner IS NULL AND lease_expires_at IS NULL) OR ($3=''com.open.agentic.catalog.webhook.test.v1'' AND $4 IN (''succeeded'',''dead_letter'') AND NOT EXISTS (SELECT 1 FROM %I.outbox_events WHERE tenant_id=$1 AND id=$2))',
      TG_TABLE_SCHEMA,TG_TABLE_SCHEMA
    ) INTO disposable USING OLD.tenant_id,OLD.event_id,OLD.event_type,OLD.state;
    IF NOT disposable THEN
      RAISE EXCEPTION 'webhook delivery remains required by its event';
    END IF;
    IF TG_OP = 'DELETE' THEN
      IF OLD.cleanup_after IS NOT NULL AND OLD.cleanup_after<=clock_timestamp() THEN
        RETURN OLD;
      END IF;
      RAISE EXCEPTION 'webhook delivery retention has not elapsed';
    END IF;
    IF OLD.cleanup_after IS NULL AND NEW.cleanup_after>clock_timestamp()
       AND to_jsonb(NEW)-'cleanup_after'=to_jsonb(OLD)-'cleanup_after' THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'webhook delivery cleanup marker is immutable';
  END IF;

  IF ROW(NEW.id,NEW.tenant_id,NEW.endpoint_id,NEW.event_id,NEW.event_type,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.endpoint_id,OLD.event_id,OLD.event_type,OLD.created_at) OR
     OLD.cleanup_after IS NOT NULL OR OLD.state IN ('succeeded','dead_letter') OR
     NEW.state NOT IN ('waiting','succeeded','dead_letter') OR
     NEW.attempt <> OLD.attempt+1 OR NEW.updated_at < OLD.updated_at THEN
    RAISE EXCEPTION 'webhook delivery lifecycle transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

DROP TRIGGER webhook_deliveries_retained_row ON webhook_deliveries;
CREATE TRIGGER webhook_deliveries_delete_lifecycle BEFORE DELETE ON webhook_deliveries
  FOR EACH ROW EXECUTE FUNCTION guard_webhook_delivery_lifecycle();
REVOKE ALL ON FUNCTION guard_webhook_delivery_lifecycle() FROM PUBLIC;
COMMENT ON COLUMN webhook_deliveries.cleanup_after IS 'Immutable prospective cleanup marker for an acknowledged unleased matching parent or an exact terminal synthetic webhook test; audit and endpoint state are not reclaimed.';
