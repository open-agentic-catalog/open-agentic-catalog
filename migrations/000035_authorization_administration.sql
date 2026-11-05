-- Extend the staged core without resetting retained assignments or authority.
SET LOCAL row_security=off;
ALTER TABLE role_definitions ALTER COLUMN key DROP NOT NULL;
ALTER TABLE role_definitions ALTER COLUMN builtin SET DEFAULT false;
ALTER TABLE role_definitions ALTER COLUMN state SET DEFAULT 'active';
ALTER TABLE role_definitions ALTER COLUMN revision SET DEFAULT 1;
ALTER TABLE role_definitions DROP CONSTRAINT role_definitions_key_check;
ALTER TABLE role_definitions DROP CONSTRAINT role_definitions_builtin_check;
ALTER TABLE role_definitions DROP CONSTRAINT role_definitions_state_check;
ALTER TABLE role_definitions DROP CONSTRAINT role_definitions_revision_check;
ALTER TABLE role_definitions ADD COLUMN name text;
ALTER TABLE role_definitions ADD COLUMN description text NOT NULL DEFAULT '' CHECK (length(description)<=2000);
ALTER TABLE role_definitions ADD COLUMN created_at timestamptz NOT NULL DEFAULT clock_timestamp();
ALTER TABLE role_definitions ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp();
ALTER TABLE role_definitions ADD CONSTRAINT role_definition_identity CHECK (
 (builtin AND key IS NOT NULL AND key IN ('reader','contributor','reviewer','publisher','discovery_reader','publisher_creator','publication_admin','source_admin','security_reviewer','webhook_admin','feedback_member','tenant_admin','service_client') AND name IS NULL)
 OR (NOT builtin AND key IS NULL AND name IS NOT NULL AND length(name) BETWEEN 1 AND 120 AND name=btrim(name)));
ALTER TABLE role_definitions ADD CONSTRAINT role_definition_state CHECK (state IN ('active','archived') AND revision>0 AND updated_at>=created_at);

ALTER TABLE tenant_memberships ADD CONSTRAINT tenant_membership_realm_identity UNIQUE (tenant_id,id,realm_id);
CREATE TABLE access_groups (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id), realm_id uuid NOT NULL,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120 AND name=btrim(name)),
 description text NOT NULL DEFAULT '' CHECK (length(description)<=2000),
 state text NOT NULL DEFAULT 'active' CHECK (state IN ('active','suspended','archived')),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL CHECK (updated_at>=created_at),
 UNIQUE (tenant_id,id), UNIQUE (tenant_id,id,realm_id),
 FOREIGN KEY (tenant_id,realm_id) REFERENCES tenant_identity_realms(tenant_id,id)
);
CREATE TABLE access_group_members (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL, realm_id uuid NOT NULL, group_id uuid NOT NULL, membership_id uuid NOT NULL,
 state text NOT NULL DEFAULT 'active' CHECK (state IN ('active','revoked')),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
 added_by text NOT NULL, add_reason text NOT NULL CHECK (length(add_reason) BETWEEN 1 AND 2000), added_at timestamptz NOT NULL,
 revoked_by text, revoke_reason text NOT NULL DEFAULT '', revoked_at timestamptz,
 CHECK ((state='active' AND revoked_by IS NULL AND revoked_at IS NULL AND revoke_reason='') OR
 (state='revoked' AND revoked_by IS NOT NULL AND revoked_at IS NOT NULL AND revoked_at>=added_at AND revoke_reason<>'')),
 UNIQUE (tenant_id,id),
 FOREIGN KEY (tenant_id,group_id,realm_id) REFERENCES access_groups(tenant_id,id,realm_id),
 FOREIGN KEY (tenant_id,membership_id,realm_id) REFERENCES tenant_memberships(tenant_id,id,realm_id)
);
CREATE UNIQUE INDEX access_group_members_one_active ON access_group_members(tenant_id,group_id,membership_id) WHERE state='active';
CREATE INDEX access_group_members_current ON access_group_members(tenant_id,membership_id,group_id) WHERE state='active';
ALTER TABLE access_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE access_groups FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON access_groups USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
ALTER TABLE access_group_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE access_group_members FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON access_group_members USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

ALTER TABLE role_assignments ALTER COLUMN membership_id DROP NOT NULL;
ALTER TABLE role_assignments ADD COLUMN group_id uuid;
ALTER TABLE role_assignments ADD COLUMN realm_id uuid;
ALTER TABLE role_assignments DISABLE TRIGGER role_assignments_lifecycle;
UPDATE role_assignments a SET realm_id=m.realm_id FROM tenant_memberships m WHERE m.tenant_id=a.tenant_id AND m.id=a.membership_id;
ALTER TABLE role_assignments ENABLE TRIGGER role_assignments_lifecycle;
ALTER TABLE role_assignments ALTER COLUMN realm_id SET NOT NULL;
ALTER TABLE role_assignments DROP CONSTRAINT role_assignments_role_check;
ALTER TABLE role_assignments DROP CONSTRAINT role_assignments_check;
ALTER TABLE role_assignments DROP CONSTRAINT role_assignment_definition;
ALTER TABLE role_assignments ADD CONSTRAINT role_assignment_definition FOREIGN KEY (tenant_id,role_definition_id) REFERENCES role_definitions(tenant_id,id);
ALTER TABLE role_assignments ADD CONSTRAINT role_assignment_one_subject CHECK ((membership_id IS NOT NULL)<>(group_id IS NOT NULL));
ALTER TABLE role_assignments ADD CONSTRAINT role_assignment_member_realm FOREIGN KEY (tenant_id,membership_id,realm_id) REFERENCES tenant_memberships(tenant_id,id,realm_id);
ALTER TABLE role_assignments ADD CONSTRAINT role_assignment_group_realm FOREIGN KEY (tenant_id,group_id,realm_id) REFERENCES access_groups(tenant_id,id,realm_id);
DROP INDEX role_assignments_one_active;
CREATE UNIQUE INDEX role_assignments_one_active ON role_assignments(tenant_id,membership_id,coalesce(namespace_id,'00000000-0000-0000-0000-000000000000'::uuid),role) WHERE state='active';
CREATE UNIQUE INDEX role_assignments_group_active ON role_assignments(tenant_id,group_id,coalesce(namespace_id,'00000000-0000-0000-0000-000000000000'::uuid),role_definition_id) WHERE state='active' AND group_id IS NOT NULL;
CREATE INDEX role_assignments_definition_active ON role_assignments(tenant_id,role_definition_id,namespace_id) WHERE state='active';

CREATE OR REPLACE FUNCTION bind_role_assignment_definition() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE definition_key text; definition_state text; definition_builtin boolean; target_realm uuid; target_state text; target_role text;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF ROW(NEW.role_definition_id,NEW.realm_id,NEW.group_id) IS DISTINCT FROM ROW(OLD.role_definition_id,OLD.realm_id,OLD.group_id) THEN RAISE EXCEPTION 'assignment identity is immutable' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF NEW.role_definition_id IS NULL THEN
  EXECUTE format('SELECT id FROM %I.role_definitions WHERE tenant_id=$1 AND key=$2',TG_TABLE_SCHEMA) INTO NEW.role_definition_id USING NEW.tenant_id,NEW.role;
 END IF;
 EXECUTE format('SELECT key,state,builtin FROM %I.role_definitions WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA) INTO definition_key,definition_state,definition_builtin USING NEW.tenant_id,NEW.role_definition_id;
 IF definition_state IS DISTINCT FROM 'active' THEN RAISE EXCEPTION 'active role is required' USING ERRCODE='23514'; END IF;
 target_role:=coalesce(definition_key,'custom:' || NEW.role_definition_id::text);
 IF NEW.role='' THEN NEW.role:=target_role; ELSIF NEW.role IS DISTINCT FROM target_role THEN RAISE EXCEPTION 'assignment role identity mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.membership_id IS NOT NULL AND NEW.group_id IS NULL THEN
  EXECUTE format('SELECT realm_id,state FROM %I.tenant_memberships WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA) INTO target_realm,target_state USING NEW.tenant_id,NEW.membership_id;
 ELSIF NEW.group_id IS NOT NULL AND NEW.membership_id IS NULL THEN
  IF definition_key='tenant_admin' THEN RAISE EXCEPTION 'administrator assignment must be direct' USING ERRCODE='23514'; END IF;
  EXECUTE format('SELECT realm_id,state FROM %I.access_groups WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA) INTO target_realm,target_state USING NEW.tenant_id,NEW.group_id;
 ELSE RAISE EXCEPTION 'exactly one assignment subject is required' USING ERRCODE='23514'; END IF;
 IF target_state IS DISTINCT FROM 'active' THEN RAISE EXCEPTION 'active assignment subject is required' USING ERRCODE='23514'; END IF;
 IF NEW.realm_id IS NULL THEN NEW.realm_id:=target_realm; ELSIF NEW.realm_id IS DISTINCT FROM target_realm THEN RAISE EXCEPTION 'assignment realm mismatch' USING ERRCODE='23514'; END IF;
 -- The deferred scope/set constraint below includes role edits in this transaction.
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION guard_authorization_history() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE active_assignment boolean; role_builtin boolean; role_state text;
BEGIN
 IF TG_TABLE_NAME='tenant_authorization_state' THEN
  IF TG_OP='UPDATE' AND NEW.tenant_id=OLD.tenant_id AND NEW.revision=OLD.revision+1 THEN RETURN NEW; END IF;
 ELSIF TG_TABLE_NAME='role_permissions' THEN
  IF TG_OP='DELETE' THEN
   EXECUTE format('SELECT builtin,state FROM %I.role_definitions WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA) INTO role_builtin,role_state USING OLD.tenant_id,OLD.role_definition_id;
   IF role_builtin=false AND role_state='active' THEN RETURN OLD; END IF;
  END IF;
 ELSIF TG_TABLE_NAME='role_definitions' THEN
  IF TG_OP='UPDATE' THEN
   IF NOT OLD.builtin AND OLD.state='active' AND
    ROW(NEW.id,NEW.tenant_id,NEW.key,NEW.builtin,NEW.created_at) IS NOT DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.key,OLD.builtin,OLD.created_at) AND NEW.revision=OLD.revision+1 AND NEW.updated_at>=OLD.updated_at THEN
    IF NEW.state='archived' THEN
     EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.role_assignments WHERE tenant_id=$1 AND role_definition_id=$2 AND state=''active'')',TG_TABLE_SCHEMA) INTO active_assignment USING NEW.tenant_id,NEW.id;
     IF active_assignment THEN RAISE EXCEPTION 'revoke active assignments before archival' USING ERRCODE='23514'; END IF;
    END IF;
    RETURN NEW;
   END IF;
  END IF;
 END IF;
 RAISE EXCEPTION 'authorization history is immutable' USING ERRCODE='23514';
END $$;

CREATE FUNCTION guard_access_group() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE active_assignment boolean;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'group history cannot be deleted' USING ERRCODE='23514'; END IF;
 IF ROW(NEW.id,NEW.tenant_id,NEW.realm_id,NEW.created_at) IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.realm_id,OLD.created_at) OR OLD.state='archived' OR NEW.revision<>OLD.revision+1 OR NEW.updated_at<OLD.updated_at OR NOT (
  NEW.state=OLD.state OR OLD.state='active' AND NEW.state IN ('suspended','archived') OR OLD.state='suspended' AND NEW.state IN ('active','archived')) THEN RAISE EXCEPTION 'invalid group transition' USING ERRCODE='23514'; END IF;
 IF NEW.state='archived' THEN
  EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.role_assignments WHERE tenant_id=$1 AND group_id=$2 AND state=''active'')',TG_TABLE_SCHEMA) INTO active_assignment USING NEW.tenant_id,NEW.id;
  IF active_assignment THEN RAISE EXCEPTION 'revoke group assignments before archival' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION guard_access_group_member() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE active_subject boolean;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'group membership history cannot be deleted' USING ERRCODE='23514'; END IF;
 IF TG_OP='INSERT' THEN
  EXECUTE format('SELECT EXISTS(SELECT 1 FROM %1$I.access_groups g JOIN %1$I.tenant_memberships m ON m.tenant_id=g.tenant_id AND m.realm_id=g.realm_id JOIN %1$I.tenant_identity_realms r ON r.tenant_id=m.tenant_id AND r.id=m.realm_id WHERE g.tenant_id=$1 AND g.id=$2 AND m.id=$3 AND g.state=''active'' AND m.state=''active'' AND r.state=''active'')',TG_TABLE_SCHEMA) INTO active_subject USING NEW.tenant_id,NEW.group_id,NEW.membership_id;
  IF NOT active_subject OR NEW.state<>'active' OR NEW.revision<>1 THEN RAISE EXCEPTION 'active same-realm group membership is required' USING ERRCODE='23514'; END IF;
 ELSIF ROW(NEW.id,NEW.tenant_id,NEW.realm_id,NEW.group_id,NEW.membership_id,NEW.added_by,NEW.add_reason,NEW.added_at) IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.realm_id,OLD.group_id,OLD.membership_id,OLD.added_by,OLD.add_reason,OLD.added_at) OR OLD.state<>'active' OR NEW.state<>'revoked' OR NEW.revision<>OLD.revision+1 THEN RAISE EXCEPTION 'invalid group membership transition' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;

-- Advance only effective authority; cosmetic definition/group edits retain it.
CREATE OR REPLACE FUNCTION advance_tenant_authorization() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE affected bigint;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF TG_TABLE_NAME IN ('tenant_memberships','tenant_identity_realms','namespace_memberships','role_assignments','role_definitions','access_groups','access_group_members') THEN
   IF NEW.state=OLD.state THEN RETURN NEW; END IF;
  END IF;
 END IF;
 IF current_user<>pg_get_userbyid((SELECT relowner FROM pg_class WHERE oid=TG_RELID)) AND coalesce(NEW.tenant_id,OLD.tenant_id) IS DISTINCT FROM nullif(current_setting('app.tenant_id',true),'')::uuid THEN RAISE EXCEPTION 'authority writer crossed tenant context' USING ERRCODE='42501'; END IF;
 EXECUTE format('UPDATE %I.tenant_authorization_state SET revision=revision+1 WHERE tenant_id=$1',TG_TABLE_SCHEMA) USING coalesce(NEW.tenant_id,OLD.tenant_id);
 GET DIAGNOSTICS affected=ROW_COUNT;IF affected<>1 THEN RAISE EXCEPTION 'tenant authorization state is missing' USING ERRCODE='23514'; END IF;
 RETURN coalesce(NEW,OLD);
END $$;

CREATE TRIGGER access_groups_authorization_fence BEFORE INSERT OR UPDATE OR DELETE ON access_groups FOR EACH STATEMENT EXECUTE FUNCTION fence_tenant_authorization();
CREATE TRIGGER access_groups_authorization_revision AFTER INSERT OR UPDATE OR DELETE ON access_groups FOR EACH ROW EXECUTE FUNCTION advance_tenant_authorization();
CREATE TRIGGER access_group_members_authorization_fence BEFORE INSERT OR UPDATE OR DELETE ON access_group_members FOR EACH STATEMENT EXECUTE FUNCTION fence_tenant_authorization();
CREATE TRIGGER access_group_members_authorization_revision AFTER INSERT OR UPDATE OR DELETE ON access_group_members FOR EACH ROW EXECUTE FUNCTION advance_tenant_authorization();
CREATE TRIGGER access_groups_lifecycle BEFORE UPDATE OR DELETE ON access_groups FOR EACH ROW EXECUTE FUNCTION guard_access_group();
CREATE TRIGGER access_group_members_lifecycle BEFORE INSERT OR UPDATE OR DELETE ON access_group_members FOR EACH ROW EXECUTE FUNCTION guard_access_group_member();

REVOKE ALL ON access_groups,access_group_members FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_access_group() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_access_group_member() FROM PUBLIC;

-- Exact set replacement may temporarily remove companions. Validate the final
-- set at commit, including assignments created in the same transaction.
CREATE FUNCTION validate_role_permission_set() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE tenant uuid; role_id uuid; is_builtin boolean; invalid_set boolean; invalid_scope boolean;
BEGIN
 tenant:=coalesce(NEW.tenant_id,OLD.tenant_id);
 IF TG_TABLE_NAME='role_definitions' THEN role_id:=coalesce(NEW.id,OLD.id); ELSE role_id:=coalesce(NEW.role_definition_id,OLD.role_definition_id); END IF;
 EXECUTE format('SELECT builtin FROM %I.role_definitions WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA) INTO is_builtin USING tenant,role_id;
 IF is_builtin=false THEN
  EXECUTE format($query$SELECT NOT EXISTS(SELECT 1 FROM %1$I.role_permissions WHERE tenant_id=$1 AND role_definition_id=$2)
   OR EXISTS(SELECT 1 FROM %1$I.role_permissions p JOIN (VALUES
    ('resource.create','namespace.read'),('resource.edit','resource.read'),('resource.edit','resource.read_history'),('resource.edit','resource.read_content'),
    ('resource.delete_draft','resource.read'),('resource.validate','resource.read_content'),('review.submit','resource.read_history'),('review.submit','resource.read_content'),
    ('review.decide','resource.read_history'),('review.decide','resource.read_content'),('release.publish','resource.read_history'),('release.publish','resource.read_content'),
    ('release.transition','resource.read'),('namespace.manage','namespace.read'),('publisher.create','publisher.read'),('publisher.manage','publisher.read'),('publisher.approve_domain','publisher.read'),
    ('source.inspect','source.read'),('source.manage','source.read'),('source.manage','source.inspect'),('source.sync','source.read'),('source.read_evidence','source.read'),
    ('publication.generate','publication.read'),('publication.activate','publication.read'),('publication.rollback','publication.read'),('publication.unpublish','publication.read'),
    ('job.cancel','job.read'),('job.retry','job.read'),('audit.export','audit.read'),('webhook.manage','webhook.read'),('webhook.test','webhook.read'),('webhook.rotate','webhook.read'),('webhook.read_evidence','webhook.read')
   ) requirements(permission,companion) ON requirements.permission=p.permission WHERE p.tenant_id=$1 AND p.role_definition_id=$2 AND
    NOT EXISTS(SELECT 1 FROM %1$I.role_permissions c WHERE c.tenant_id=p.tenant_id AND c.role_definition_id=p.role_definition_id AND c.permission=requirements.companion))$query$,TG_TABLE_SCHEMA) INTO invalid_set USING tenant,role_id;
  IF invalid_set THEN RAISE EXCEPTION 'custom role requires a complete nonempty permission set' USING ERRCODE='23514'; END IF;
 END IF;
 EXECUTE format($query$SELECT EXISTS(SELECT 1 FROM %1$I.role_assignments a JOIN %1$I.role_permissions p ON p.tenant_id=a.tenant_id AND p.role_definition_id=a.role_definition_id
  WHERE a.tenant_id=$1 AND a.role_definition_id=$2 AND a.state='active' AND a.namespace_id IS NOT NULL AND p.permission NOT IN
  ('resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage'))$query$,TG_TABLE_SCHEMA) INTO invalid_scope USING tenant,role_id;
 IF invalid_scope THEN RAISE EXCEPTION 'role permissions conflict with active namespace assignments' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER role_permissions_complete AFTER INSERT OR DELETE ON role_permissions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_role_permission_set();
CREATE CONSTRAINT TRIGGER role_definitions_complete AFTER INSERT OR UPDATE ON role_definitions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_role_permission_set();
CREATE CONSTRAINT TRIGGER role_assignments_complete AFTER INSERT ON role_assignments DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_role_permission_set();
REVOKE ALL ON FUNCTION validate_role_permission_set() FROM PUBLIC;

CREATE OR REPLACE FUNCTION guard_builtin_permission_insert() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE role_key text; is_builtin boolean; role_state text;
BEGIN
 EXECUTE format('SELECT key,builtin,state FROM %I.role_definitions WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA) INTO role_key,is_builtin,role_state USING NEW.tenant_id,NEW.role_definition_id;
 IF is_builtin=false THEN
  IF role_state<>'active' THEN RAISE EXCEPTION 'role is archived' USING ERRCODE='23514'; END IF;
  IF NEW.permission='access.manage' THEN RAISE EXCEPTION 'administrator authority is protected' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF NOT coalesce(((role_key='reader' AND NEW.permission IN ('resource.read','resource.read_history','resource.read_content','namespace.read')) OR
 (role_key='contributor' AND NEW.permission IN ('resource.read','resource.read_history','resource.read_content','namespace.read','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit')) OR
 (role_key='reviewer' AND NEW.permission IN ('resource.read','resource.read_history','resource.read_content','namespace.read','review.decide')) OR
 (role_key='publisher' AND NEW.permission IN ('resource.read','resource.read_history','resource.read_content','namespace.read','release.publish','release.transition')) OR
 (role_key='discovery_reader' AND NEW.permission IN ('federation.discover','discovery.read','publisher.read','job.read')) OR
 (role_key='publisher_creator' AND NEW.permission IN ('publisher.read','publisher.create')) OR
 (role_key='publication_admin' AND NEW.permission IN ('publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','job.read','job.cancel','job.retry')) OR
 (role_key='source_admin' AND NEW.permission IN ('source.read','source.inspect','source.manage','source.sync','source.read_evidence','federation.discover','job.read','job.cancel','job.retry')) OR
 (role_key='security_reviewer' AND NEW.permission IN ('audit.read','audit.export')) OR
 (role_key='webhook_admin' AND NEW.permission IN ('webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence')) OR
 (role_key='feedback_member' AND NEW.permission IN ('feedback.own')) OR
 (role_key='tenant_admin' AND NEW.permission IN ('resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage','namespace.create','publisher.read','publisher.create','publisher.manage','publisher.approve_domain','federation.discover','source.read','source.inspect','source.manage','source.sync','source.read_evidence','publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','discovery.read','job.read','job.cancel','job.retry','audit.read','audit.export','webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence','feedback.own','feedback.moderate','operations.reindex','access.manage'))),false) THEN RAISE EXCEPTION 'built-in permission set is service-owned' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
