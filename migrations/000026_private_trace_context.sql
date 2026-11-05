-- Drain API and worker processes, apply, and reconcile manifest v11 before restart.
-- Optional causal metadata is private and immutable; existing rows need no backfill.
ALTER TABLE jobs ADD COLUMN causal_trace text;
ALTER TABLE outbox_events ADD COLUMN causal_trace text;

ALTER TABLE jobs ADD CONSTRAINT jobs_causal_trace_check CHECK (
  causal_trace IS NULL OR (causal_trace ~ '^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$'
    AND substring(causal_trace FROM 4 FOR 32) <> repeat('0',32)
    AND substring(causal_trace FROM 37 FOR 16) <> repeat('0',16)));
ALTER TABLE outbox_events ADD CONSTRAINT outbox_causal_trace_check CHECK (
  causal_trace IS NULL OR (causal_trace ~ '^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$'
    AND substring(causal_trace FROM 4 FOR 32) <> repeat('0',32)
    AND substring(causal_trace FROM 37 FOR 16) <> repeat('0',16)));

CREATE FUNCTION guard_private_trace_context() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $function$
BEGIN
  IF NEW.causal_trace IS DISTINCT FROM OLD.causal_trace THEN
    RAISE EXCEPTION 'private trace context is immutable';
  END IF;
  RETURN NEW;
END;
$function$;
REVOKE ALL ON FUNCTION guard_private_trace_context() FROM PUBLIC;
CREATE TRIGGER jobs_private_trace_context BEFORE UPDATE ON jobs
  FOR EACH ROW EXECUTE FUNCTION guard_private_trace_context();
CREATE TRIGGER outbox_private_trace_context BEFORE UPDATE ON outbox_events
  FOR EACH ROW EXECUTE FUNCTION guard_private_trace_context();
COMMENT ON COLUMN jobs.causal_trace IS 'Optional private creating span context. Never part of Job payload, public API, audit or authorization.';
COMMENT ON COLUMN outbox_events.causal_trace IS 'Optional private creating span context for dispatch links. Never part of CloudEvents or delivery bodies.';
