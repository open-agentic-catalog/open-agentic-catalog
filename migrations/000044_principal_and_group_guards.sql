-- Exact retained creation/submission attribution; existing evidence is unchanged.
CREATE INDEX audit_events_originating_principal ON audit_events(tenant_id,target_type,target_id,action)
 WHERE action IN ('resource.created','observation.adopted','review_submission.submitted');

-- Group relations must be explicitly revoked before the terminal transition.
CREATE OR REPLACE FUNCTION guard_access_group() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE active_assignment boolean; active_member boolean;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'group history cannot be deleted' USING ERRCODE='23514'; END IF;
 IF ROW(NEW.id,NEW.tenant_id,NEW.realm_id,NEW.created_at) IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.realm_id,OLD.created_at) OR OLD.state='archived' OR NEW.revision<>OLD.revision+1 OR NEW.updated_at<OLD.updated_at OR NOT (
  NEW.state=OLD.state OR OLD.state='active' AND NEW.state IN ('suspended','archived') OR OLD.state='suspended' AND NEW.state IN ('active','archived')) THEN RAISE EXCEPTION 'invalid group transition' USING ERRCODE='23514'; END IF;
 IF NEW.state='archived' THEN
  EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.role_assignments WHERE tenant_id=$1 AND group_id=$2 AND state=''active'')',TG_TABLE_SCHEMA) INTO active_assignment USING NEW.tenant_id,NEW.id;
  EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.access_group_members WHERE tenant_id=$1 AND group_id=$2 AND state=''active'')',TG_TABLE_SCHEMA) INTO active_member USING NEW.tenant_id,NEW.id;
  IF active_assignment OR active_member THEN RAISE EXCEPTION 'revoke group assignments and members before archival' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
