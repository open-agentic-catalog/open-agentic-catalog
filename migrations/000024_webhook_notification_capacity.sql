-- One coalesced loss indication per tenant, never a second missed-event queue.
-- Drain both runtime roles before applying and reconciling manifest v9.
CREATE TABLE webhook_notification_state (
  tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
  admission_revision bigint NOT NULL DEFAULT 0 CHECK (admission_revision>=0),
  capacity_loss_count bigint NOT NULL DEFAULT 0 CHECK (capacity_loss_count>=0),
  last_capacity_loss_at timestamptz,
  delivery_loss_count bigint NOT NULL DEFAULT 0 CHECK (delivery_loss_count>=0),
  last_delivery_loss_at timestamptz,
  CHECK ((capacity_loss_count=0)=(last_capacity_loss_at IS NULL)),
  CHECK (capacity_loss_count<=admission_revision),
  CHECK ((delivery_loss_count=0)=(last_delivery_loss_at IS NULL))
);
ALTER TABLE webhook_notification_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_notification_state FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_notification_state
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE INDEX outbox_events_webhook_pending_tenant
  ON outbox_events(tenant_id) WHERE published_at IS NULL AND NOT is_internal;

CREATE FUNCTION guard_webhook_notification_state() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  -- Runtime identities cannot create triggers or execute these functions.
  -- These exact invoker grants are usable only by the reviewed nested
  -- outbox/delivery triggers; no SECURITY DEFINER or PUBLIC bypass is needed.
  IF pg_trigger_depth()<2 OR TG_OP='DELETE' THEN
    RAISE EXCEPTION 'notification state is maintained only by classified event transitions';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.capacity_loss_count=0 AND
       ((NEW.admission_revision=1 AND NEW.delivery_loss_count=0) OR
        (NEW.admission_revision=0 AND NEW.delivery_loss_count=1)) THEN
      RETURN NEW;
    END IF;
  ELSIF (NEW.admission_revision=OLD.admission_revision+1 AND
         to_jsonb(NEW)-'admission_revision'=to_jsonb(OLD)-'admission_revision') OR
        (NEW.capacity_loss_count=OLD.capacity_loss_count+1 AND
         NEW.last_capacity_loss_at>=coalesce(OLD.last_capacity_loss_at,NEW.last_capacity_loss_at) AND
         to_jsonb(NEW)-ARRAY['capacity_loss_count','last_capacity_loss_at']=to_jsonb(OLD)-ARRAY['capacity_loss_count','last_capacity_loss_at']) OR
        (NEW.delivery_loss_count=OLD.delivery_loss_count+1 AND
         NEW.last_delivery_loss_at>=coalesce(OLD.last_delivery_loss_at,NEW.last_delivery_loss_at) AND
         to_jsonb(NEW)-ARRAY['delivery_loss_count','last_delivery_loss_at']=to_jsonb(OLD)-ARRAY['delivery_loss_count','last_delivery_loss_at']) THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'notification state transition is invalid';
END
$function$;
CREATE TRIGGER webhook_notification_state_lifecycle
  BEFORE INSERT OR UPDATE OR DELETE ON webhook_notification_state
  FOR EACH ROW EXECUTE FUNCTION guard_webhook_notification_state();

CREATE FUNCTION admit_webhook_notification() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  pending bigint;
BEGIN
  IF NEW.is_internal OR NEW.published_at IS NOT NULL THEN
    RETURN NEW;
  END IF;
  -- This writes, not merely advisory-locks, the tenant admission row. A
  -- concurrent stale repeatable-read/serializable transaction must abort
  -- rather than over-admit using its old snapshot. At read committed the
  -- following volatile query takes a fresh snapshot after the write wait.
  EXECUTE format(
    'INSERT INTO %I.webhook_notification_state AS state(tenant_id,admission_revision) VALUES ($1,1) ON CONFLICT (tenant_id) DO UPDATE SET admission_revision=state.admission_revision+1',
    TG_TABLE_SCHEMA
  ) USING NEW.tenant_id;
  EXECUTE format(
    'SELECT count(*) FROM (SELECT 1 FROM %I.outbox_events WHERE tenant_id=$1 AND published_at IS NULL AND NOT is_internal LIMIT 1000) bounded',
    TG_TABLE_SCHEMA
  ) INTO pending USING NEW.tenant_id;
  IF pending>=1000 THEN
    EXECUTE format(
      'UPDATE %I.webhook_notification_state SET capacity_loss_count=capacity_loss_count+1,last_capacity_loss_at=greatest(last_capacity_loss_at,clock_timestamp()) WHERE tenant_id=$1',
      TG_TABLE_SCHEMA
    ) USING NEW.tenant_id;
    -- Preserve the independently required immutable outbox record while
    -- retiring optional delivery, in the same domain/audit transaction.
    NEW.published_at := NEW.created_at;
  END IF;
  RETURN NEW;
END
$function$;
CREATE TRIGGER outbox_events_notification_admission BEFORE INSERT ON outbox_events
  FOR EACH ROW EXECUTE FUNCTION admit_webhook_notification();

CREATE FUNCTION record_webhook_delivery_loss() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF NEW.state<>'dead_letter' OR NEW.event_type='com.open.agentic.catalog.webhook.test.v1' OR
     (TG_OP='UPDATE' AND OLD.state='dead_letter') THEN
    RETURN NEW;
  END IF;
  EXECUTE format(
    'INSERT INTO %I.webhook_notification_state AS state(tenant_id,delivery_loss_count,last_delivery_loss_at) VALUES ($1,1,clock_timestamp()) ON CONFLICT (tenant_id) DO UPDATE SET delivery_loss_count=state.delivery_loss_count+1,last_delivery_loss_at=greatest(state.last_delivery_loss_at,clock_timestamp())',
    TG_TABLE_SCHEMA
  ) USING NEW.tenant_id;
  RETURN NEW;
END
$function$;
CREATE TRIGGER webhook_deliveries_notification_loss AFTER INSERT OR UPDATE ON webhook_deliveries
  FOR EACH ROW EXECUTE FUNCTION record_webhook_delivery_loss();

REVOKE ALL ON FUNCTION guard_webhook_notification_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION admit_webhook_notification() FROM PUBLIC;
REVOKE ALL ON FUNCTION record_webhook_delivery_loss() FROM PUBLIC;
COMMENT ON TABLE webhook_notification_state IS 'Coalesced monotonic per-tenant notification loss counters; receivers reconcile current authorized state. No reset, historical replay or per-loss row.';
