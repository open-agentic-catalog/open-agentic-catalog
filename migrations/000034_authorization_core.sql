-- Refuse policy-filtered migration reads; this setting does not bypass RLS.
SET LOCAL row_security=off;

-- Permission core. This staged schema does not expose custom-role administration.
CREATE TABLE tenant_authorization_state (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id), revision bigint NOT NULL CHECK (revision > 0)
);
CREATE TABLE role_definitions (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id),
 key text NOT NULL CHECK (key IN ('reader','contributor','reviewer','publisher','discovery_reader','publisher_creator','publication_admin','source_admin','security_reviewer','webhook_admin','feedback_member','tenant_admin','service_client')),
 builtin boolean NOT NULL CHECK (builtin),
 state text NOT NULL CHECK (state='active'), revision bigint NOT NULL CHECK (revision=1),
 UNIQUE (tenant_id,id), UNIQUE (tenant_id,key), UNIQUE (tenant_id,id,key)
);
CREATE TABLE role_permissions (
 tenant_id uuid NOT NULL, role_definition_id uuid NOT NULL, permission text NOT NULL CHECK (permission IN ('resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage','namespace.create','publisher.read','publisher.create','publisher.manage','publisher.approve_domain','federation.discover','source.read','source.inspect','source.manage','source.sync','source.read_evidence','publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','discovery.read','job.read','job.cancel','job.retry','audit.read','audit.export','webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence','feedback.own','feedback.moderate','operations.reindex','access.manage')),
 PRIMARY KEY (tenant_id,role_definition_id,permission),
 FOREIGN KEY (tenant_id,role_definition_id) REFERENCES role_definitions(tenant_id,id)
);
ALTER TABLE tenant_authorization_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_authorization_state FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_authorization_state USING (tenant_id = nullif(current_setting('app.tenant_id', true),'')::uuid) WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true),'')::uuid);
ALTER TABLE role_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_definitions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON role_definitions USING (tenant_id = nullif(current_setting('app.tenant_id', true),'')::uuid) WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true),'')::uuid);
ALTER TABLE role_permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_permissions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON role_permissions USING (tenant_id = nullif(current_setting('app.tenant_id', true),'')::uuid) WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true),'')::uuid);
CREATE FUNCTION initialize_tenant_authorization() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 EXECUTE format($query$INSERT INTO %1$I.tenant_authorization_state(tenant_id,revision) VALUES ($1,1)$query$,TG_TABLE_SCHEMA) USING NEW.id;
 EXECUTE format($query$INSERT INTO %1$I.role_definitions(id,tenant_id,key,builtin,state,revision)
 SELECT md5($1::text || ':' || key)::uuid,$1,key,true,'active',1 FROM (VALUES
('reader'),
('contributor'),
('reviewer'),
('publisher'),
('discovery_reader'),
('publisher_creator'),
('publication_admin'),
('source_admin'),
('security_reviewer'),
('webhook_admin'),
('feedback_member'),
('tenant_admin'),
('service_client')) AS templates(key)$query$,TG_TABLE_SCHEMA) USING NEW.id;
 EXECUTE format($query$INSERT INTO %1$I.role_permissions(tenant_id,role_definition_id,permission)
 SELECT $1,md5($1::text || ':' || key)::uuid,permission FROM (VALUES
('reader','resource.read'),
('reader','resource.read_history'),
('reader','resource.read_content'),
('reader','namespace.read'),
('contributor','resource.read'),
('contributor','resource.read_history'),
('contributor','resource.read_content'),
('contributor','namespace.read'),
('contributor','resource.create'),
('contributor','resource.edit'),
('contributor','resource.delete_draft'),
('contributor','resource.validate'),
('contributor','review.submit'),
('reviewer','resource.read'),
('reviewer','resource.read_history'),
('reviewer','resource.read_content'),
('reviewer','namespace.read'),
('reviewer','review.decide'),
('publisher','resource.read'),
('publisher','resource.read_history'),
('publisher','resource.read_content'),
('publisher','namespace.read'),
('publisher','release.publish'),
('publisher','release.transition'),
('discovery_reader','federation.discover'),
('discovery_reader','discovery.read'),
('discovery_reader','publisher.read'),
('discovery_reader','job.read'),
('publisher_creator','publisher.read'),
('publisher_creator','publisher.create'),
('publication_admin','publication.read'),
('publication_admin','publication.generate'),
('publication_admin','publication.activate'),
('publication_admin','publication.rollback'),
('publication_admin','publication.unpublish'),
('publication_admin','job.read'),
('publication_admin','job.cancel'),
('publication_admin','job.retry'),
('source_admin','source.read'),
('source_admin','source.inspect'),
('source_admin','source.manage'),
('source_admin','source.sync'),
('source_admin','source.read_evidence'),
('source_admin','federation.discover'),
('source_admin','job.read'),
('source_admin','job.cancel'),
('source_admin','job.retry'),
('security_reviewer','audit.read'),
('security_reviewer','audit.export'),
('webhook_admin','webhook.read'),
('webhook_admin','webhook.manage'),
('webhook_admin','webhook.test'),
('webhook_admin','webhook.rotate'),
('webhook_admin','webhook.read_evidence'),
('feedback_member','feedback.own'),
('tenant_admin','resource.read'),
('tenant_admin','resource.read_history'),
('tenant_admin','resource.read_content'),
('tenant_admin','resource.create'),
('tenant_admin','resource.edit'),
('tenant_admin','resource.delete_draft'),
('tenant_admin','resource.validate'),
('tenant_admin','review.submit'),
('tenant_admin','review.decide'),
('tenant_admin','release.publish'),
('tenant_admin','release.transition'),
('tenant_admin','namespace.read'),
('tenant_admin','namespace.manage'),
('tenant_admin','namespace.create'),
('tenant_admin','publisher.read'),
('tenant_admin','publisher.create'),
('tenant_admin','publisher.manage'),
('tenant_admin','publisher.approve_domain'),
('tenant_admin','federation.discover'),
('tenant_admin','source.read'),
('tenant_admin','source.inspect'),
('tenant_admin','source.manage'),
('tenant_admin','source.sync'),
('tenant_admin','source.read_evidence'),
('tenant_admin','publication.read'),
('tenant_admin','publication.generate'),
('tenant_admin','publication.activate'),
('tenant_admin','publication.rollback'),
('tenant_admin','publication.unpublish'),
('tenant_admin','discovery.read'),
('tenant_admin','job.read'),
('tenant_admin','job.cancel'),
('tenant_admin','job.retry'),
('tenant_admin','audit.read'),
('tenant_admin','audit.export'),
('tenant_admin','webhook.read'),
('tenant_admin','webhook.manage'),
('tenant_admin','webhook.test'),
('tenant_admin','webhook.rotate'),
('tenant_admin','webhook.read_evidence'),
('tenant_admin','feedback.own'),
('tenant_admin','feedback.moderate'),
('tenant_admin','operations.reindex'),
('tenant_admin','access.manage')) AS templates(key,permission)$query$,TG_TABLE_SCHEMA) USING NEW.id;
 RETURN NEW;
END $$;
CREATE TRIGGER tenants_authorization_initialization AFTER INSERT ON tenants FOR EACH ROW EXECUTE FUNCTION initialize_tenant_authorization();
-- Retained tenants get exactly one initialized state and deterministic built-ins.
INSERT INTO tenant_authorization_state(tenant_id,revision) SELECT id,1 FROM tenants;
INSERT INTO role_definitions(id,tenant_id,key,builtin,state,revision)
SELECT md5(t.id::text || ':' || key)::uuid,t.id,key,true,'active',1 FROM tenants t CROSS JOIN (VALUES
('reader'),
('contributor'),
('reviewer'),
('publisher'),
('discovery_reader'),
('publisher_creator'),
('publication_admin'),
('source_admin'),
('security_reviewer'),
('webhook_admin'),
('feedback_member'),
('tenant_admin'),
('service_client')) AS templates(key);
INSERT INTO role_permissions(tenant_id,role_definition_id,permission)
SELECT t.id,md5(t.id::text || ':' || key)::uuid,permission FROM tenants t CROSS JOIN (VALUES
('reader','resource.read'),
('reader','resource.read_history'),
('reader','resource.read_content'),
('reader','namespace.read'),
('contributor','resource.read'),
('contributor','resource.read_history'),
('contributor','resource.read_content'),
('contributor','namespace.read'),
('contributor','resource.create'),
('contributor','resource.edit'),
('contributor','resource.delete_draft'),
('contributor','resource.validate'),
('contributor','review.submit'),
('reviewer','resource.read'),
('reviewer','resource.read_history'),
('reviewer','resource.read_content'),
('reviewer','namespace.read'),
('reviewer','review.decide'),
('publisher','resource.read'),
('publisher','resource.read_history'),
('publisher','resource.read_content'),
('publisher','namespace.read'),
('publisher','release.publish'),
('publisher','release.transition'),
('discovery_reader','federation.discover'),
('discovery_reader','discovery.read'),
('discovery_reader','publisher.read'),
('discovery_reader','job.read'),
('publisher_creator','publisher.read'),
('publisher_creator','publisher.create'),
('publication_admin','publication.read'),
('publication_admin','publication.generate'),
('publication_admin','publication.activate'),
('publication_admin','publication.rollback'),
('publication_admin','publication.unpublish'),
('publication_admin','job.read'),
('publication_admin','job.cancel'),
('publication_admin','job.retry'),
('source_admin','source.read'),
('source_admin','source.inspect'),
('source_admin','source.manage'),
('source_admin','source.sync'),
('source_admin','source.read_evidence'),
('source_admin','federation.discover'),
('source_admin','job.read'),
('source_admin','job.cancel'),
('source_admin','job.retry'),
('security_reviewer','audit.read'),
('security_reviewer','audit.export'),
('webhook_admin','webhook.read'),
('webhook_admin','webhook.manage'),
('webhook_admin','webhook.test'),
('webhook_admin','webhook.rotate'),
('webhook_admin','webhook.read_evidence'),
('feedback_member','feedback.own'),
('tenant_admin','resource.read'),
('tenant_admin','resource.read_history'),
('tenant_admin','resource.read_content'),
('tenant_admin','resource.create'),
('tenant_admin','resource.edit'),
('tenant_admin','resource.delete_draft'),
('tenant_admin','resource.validate'),
('tenant_admin','review.submit'),
('tenant_admin','review.decide'),
('tenant_admin','release.publish'),
('tenant_admin','release.transition'),
('tenant_admin','namespace.read'),
('tenant_admin','namespace.manage'),
('tenant_admin','namespace.create'),
('tenant_admin','publisher.read'),
('tenant_admin','publisher.create'),
('tenant_admin','publisher.manage'),
('tenant_admin','publisher.approve_domain'),
('tenant_admin','federation.discover'),
('tenant_admin','source.read'),
('tenant_admin','source.inspect'),
('tenant_admin','source.manage'),
('tenant_admin','source.sync'),
('tenant_admin','source.read_evidence'),
('tenant_admin','publication.read'),
('tenant_admin','publication.generate'),
('tenant_admin','publication.activate'),
('tenant_admin','publication.rollback'),
('tenant_admin','publication.unpublish'),
('tenant_admin','discovery.read'),
('tenant_admin','job.read'),
('tenant_admin','job.cancel'),
('tenant_admin','job.retry'),
('tenant_admin','audit.read'),
('tenant_admin','audit.export'),
('tenant_admin','webhook.read'),
('tenant_admin','webhook.manage'),
('tenant_admin','webhook.test'),
('tenant_admin','webhook.rotate'),
('tenant_admin','webhook.read_evidence'),
('tenant_admin','feedback.own'),
('tenant_admin','feedback.moderate'),
('tenant_admin','operations.reindex'),
('tenant_admin','access.manage')) AS templates(key,permission);
-- Refuse a scoped assignment whose legacy gate contradicts its active state.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM role_assignments a WHERE a.state='active' AND a.namespace_id IS NOT NULL AND NOT EXISTS
 (SELECT 1 FROM namespace_memberships n WHERE n.tenant_id=a.tenant_id AND n.membership_id=a.membership_id AND n.namespace_id=a.namespace_id AND n.state='active'))
 THEN RAISE EXCEPTION 'active namespace assignment has no active membership gate'; END IF;
END $$;
ALTER TABLE role_assignments ADD COLUMN role_definition_id uuid;
ALTER TABLE role_assignments DISABLE TRIGGER role_assignments_lifecycle;
UPDATE role_assignments SET role_definition_id=md5(tenant_id::text || ':' || role)::uuid;
ALTER TABLE role_assignments ENABLE TRIGGER role_assignments_lifecycle;
ALTER TABLE role_assignments ALTER COLUMN role_definition_id SET NOT NULL;
ALTER TABLE role_assignments ADD CONSTRAINT role_assignment_definition FOREIGN KEY (tenant_id,role_definition_id,role) REFERENCES role_definitions(tenant_id,id,key);
CREATE FUNCTION bind_role_assignment_definition() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='INSERT' AND NEW.role_definition_id IS NULL THEN
 EXECUTE format('SELECT id FROM %I.role_definitions WHERE tenant_id=$1 AND key=$2',TG_TABLE_SCHEMA) INTO NEW.role_definition_id USING NEW.tenant_id,NEW.role;
 ELSIF TG_OP='UPDATE' AND NEW.role_definition_id IS DISTINCT FROM OLD.role_definition_id THEN
 RAISE EXCEPTION 'assignment definition is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER role_assignments_definition BEFORE INSERT OR UPDATE ON role_assignments FOR EACH ROW EXECUTE FUNCTION bind_role_assignment_definition();
CREATE FUNCTION fence_tenant_authorization() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE current_revision bigint; target uuid := nullif(current_setting('app.tenant_id',true),'')::uuid;
BEGIN
 IF target IS NULL AND current_user=pg_get_userbyid((SELECT relowner FROM pg_class WHERE oid=TG_RELID)) THEN RETURN NULL; END IF;
 IF target IS NULL THEN RAISE EXCEPTION 'tenant authorization context is required' USING ERRCODE='42501'; END IF;
 EXECUTE format('SELECT revision FROM %I.tenant_authorization_state WHERE tenant_id=$1 FOR NO KEY UPDATE',TG_TABLE_SCHEMA) INTO current_revision USING target;
 IF current_revision IS NULL THEN RAISE EXCEPTION 'tenant authorization state is missing' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE FUNCTION advance_tenant_authorization() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE affected bigint;
BEGIN
 IF TG_OP='UPDATE' THEN
 IF TG_TABLE_NAME='tenant_memberships' AND NEW.state=OLD.state THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='tenant_identity_realms' AND NEW.state=OLD.state THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='namespace_memberships' AND NEW.state=OLD.state THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='role_assignments' AND NEW.state=OLD.state THEN RETURN NEW; END IF;
 END IF;
 IF current_user<>pg_get_userbyid((SELECT relowner FROM pg_class WHERE oid=TG_RELID)) AND coalesce(NEW.tenant_id,OLD.tenant_id) IS DISTINCT FROM nullif(current_setting('app.tenant_id',true),'')::uuid THEN
 RAISE EXCEPTION 'authority writer crossed tenant context' USING ERRCODE='42501'; END IF;
 EXECUTE format('UPDATE %I.tenant_authorization_state SET revision=revision+1 WHERE tenant_id=$1',TG_TABLE_SCHEMA) USING coalesce(NEW.tenant_id,OLD.tenant_id);
 GET DIAGNOSTICS affected=ROW_COUNT;
 IF affected<>1 THEN RAISE EXCEPTION 'tenant authorization state is missing' USING ERRCODE='23514'; END IF;
 RETURN coalesce(NEW,OLD);
END $$;
CREATE FUNCTION guard_authorization_history() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_TABLE_NAME='tenant_authorization_state' AND TG_OP='UPDATE' AND NEW.tenant_id=OLD.tenant_id AND NEW.revision=OLD.revision+1 THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'authorization history is immutable' USING ERRCODE='23514';
END $$;
CREATE TRIGGER tenant_authorization_state_history BEFORE UPDATE OR DELETE ON tenant_authorization_state FOR EACH ROW EXECUTE FUNCTION guard_authorization_history();
CREATE TRIGGER role_definitions_history BEFORE UPDATE OR DELETE ON role_definitions FOR EACH ROW EXECUTE FUNCTION guard_authorization_history();
CREATE TRIGGER role_permissions_history BEFORE UPDATE OR DELETE ON role_permissions FOR EACH ROW EXECUTE FUNCTION guard_authorization_history();
CREATE TRIGGER tenant_identity_realms_authorization_fence BEFORE INSERT OR UPDATE OR DELETE ON tenant_identity_realms FOR EACH STATEMENT EXECUTE FUNCTION fence_tenant_authorization();
CREATE TRIGGER tenant_identity_realms_authorization_revision AFTER INSERT OR UPDATE OR DELETE ON tenant_identity_realms FOR EACH ROW EXECUTE FUNCTION advance_tenant_authorization();
CREATE TRIGGER tenant_memberships_authorization_fence BEFORE INSERT OR UPDATE OR DELETE ON tenant_memberships FOR EACH STATEMENT EXECUTE FUNCTION fence_tenant_authorization();
CREATE TRIGGER tenant_memberships_authorization_revision AFTER INSERT OR UPDATE OR DELETE ON tenant_memberships FOR EACH ROW EXECUTE FUNCTION advance_tenant_authorization();
CREATE TRIGGER namespace_memberships_authorization_fence BEFORE INSERT OR UPDATE OR DELETE ON namespace_memberships FOR EACH STATEMENT EXECUTE FUNCTION fence_tenant_authorization();
CREATE TRIGGER namespace_memberships_authorization_revision AFTER INSERT OR UPDATE OR DELETE ON namespace_memberships FOR EACH ROW EXECUTE FUNCTION advance_tenant_authorization();
CREATE TRIGGER role_assignments_authorization_fence BEFORE INSERT OR UPDATE OR DELETE ON role_assignments FOR EACH STATEMENT EXECUTE FUNCTION fence_tenant_authorization();
CREATE TRIGGER role_assignments_authorization_revision AFTER INSERT OR UPDATE OR DELETE ON role_assignments FOR EACH ROW EXECUTE FUNCTION advance_tenant_authorization();
CREATE TRIGGER role_definitions_authorization_fence BEFORE INSERT OR UPDATE OR DELETE ON role_definitions FOR EACH STATEMENT EXECUTE FUNCTION fence_tenant_authorization();
CREATE TRIGGER role_definitions_authorization_revision AFTER INSERT OR UPDATE OR DELETE ON role_definitions FOR EACH ROW EXECUTE FUNCTION advance_tenant_authorization();
CREATE TRIGGER role_permissions_authorization_fence BEFORE INSERT OR UPDATE OR DELETE ON role_permissions FOR EACH STATEMENT EXECUTE FUNCTION fence_tenant_authorization();
CREATE TRIGGER role_permissions_authorization_revision AFTER INSERT OR UPDATE OR DELETE ON role_permissions FOR EACH ROW EXECUTE FUNCTION advance_tenant_authorization();
REVOKE ALL ON tenant_authorization_state,role_definitions,role_permissions FROM PUBLIC;
REVOKE ALL ON FUNCTION initialize_tenant_authorization() FROM PUBLIC;
REVOKE ALL ON FUNCTION bind_role_assignment_definition() FROM PUBLIC;
REVOKE ALL ON FUNCTION fence_tenant_authorization() FROM PUBLIC;
REVOKE ALL ON FUNCTION advance_tenant_authorization() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_authorization_history() FROM PUBLIC;
CREATE FUNCTION guard_builtin_permission_insert() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE role_key text;
BEGIN
 EXECUTE format('SELECT key FROM %I.role_definitions WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA) INTO role_key USING NEW.tenant_id,NEW.role_definition_id;
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
CREATE TRIGGER role_permissions_builtin_insert BEFORE INSERT ON role_permissions FOR EACH ROW EXECUTE FUNCTION guard_builtin_permission_insert();
REVOKE ALL ON FUNCTION guard_builtin_permission_insert() FROM PUBLIC;
