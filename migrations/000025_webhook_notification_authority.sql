-- Drain both runtime roles and reconcile exact manifest v10 before restart.
-- Trigger depth is a lifecycle check, not write authorization: PostgreSQL
-- permits runtime identities to own temporary tables and trigger functions.
-- Remove their state DML in the manifest. Only these two fixed, non-callable
-- trigger entry points may update the state with the migration owner's
-- authority. Both already pin pg_catalog and qualify every relation through
-- the invoking Catalog table's schema, with explicit NEW.tenant_id predicates.
-- Do not revoke shared database TEMP or alter unrelated schema privileges.
ALTER FUNCTION admit_webhook_notification() SECURITY DEFINER;
ALTER FUNCTION record_webhook_delivery_loss() SECURITY DEFINER;
REVOKE ALL ON FUNCTION admit_webhook_notification() FROM PUBLIC;
REVOKE ALL ON FUNCTION record_webhook_delivery_loss() FROM PUBLIC;
COMMENT ON FUNCTION admit_webhook_notification() IS 'Fixed owner-authorized outbox INSERT trigger; no PUBLIC or runtime EXECUTE. Serializes tenant admission and records capacity loss without runtime state DML.';
COMMENT ON FUNCTION record_webhook_delivery_loss() IS 'Fixed owner-authorized delivery transition trigger; no PUBLIC or runtime EXECUTE. Records first terminal recipient loss without runtime state DML.';
COMMENT ON FUNCTION guard_webhook_notification_state() IS 'Lifecycle shape and nesting guard only, not an authorization boundary. Runtime notification state DML is unavailable under manifest v10.';
