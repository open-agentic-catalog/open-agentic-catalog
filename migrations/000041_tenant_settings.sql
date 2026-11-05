-- Tenant scheduling policy is application data; startup trust and connectivity remain external.
CREATE TABLE tenant_settings (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id), revision bigint NOT NULL CHECK(revision>0),
 automatic_source_sync_enabled boolean,
 automatic_source_sync_interval_ns bigint CHECK(automatic_source_sync_interval_ns BETWEEN 60000000000 AND 86400000000000),
 updated_at timestamptz NOT NULL
);
ALTER TABLE tenant_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_settings USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE FUNCTION guard_tenant_settings() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'tenant settings deletion is not permitted'; END IF;
 IF TG_OP='INSERT' AND NEW.revision<>1 THEN RAISE EXCEPTION 'invalid tenant settings creation'; END IF;
 IF TG_OP='UPDATE' AND (NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.revision<>OLD.revision+1 OR NEW.updated_at<OLD.updated_at) THEN RAISE EXCEPTION 'invalid tenant settings transition'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER tenant_settings_guard BEFORE INSERT OR UPDATE OR DELETE ON tenant_settings FOR EACH ROW EXECUTE FUNCTION guard_tenant_settings();
REVOKE ALL ON FUNCTION guard_tenant_settings() FROM PUBLIC;
ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_permission_check;
ALTER TABLE role_permissions ADD CONSTRAINT role_permissions_permission_check CHECK(permission IN ('resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage','namespace.create','publisher.read','publisher.create','publisher.manage','publisher.approve_domain','federation.discover','source.read','source.inspect','source.manage','source.sync','source.read_evidence','publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','discovery.read','job.read','job.cancel','job.retry','audit.read','audit.export','webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence','feedback.own','feedback.moderate','operations.reindex','access.manage','ratings.own','ratings.moderate','ratings.sharing.manage','settings.read','settings.manage'));
CREATE OR REPLACE FUNCTION guard_builtin_permission_insert() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE role_key text; is_builtin boolean; role_state text;
BEGIN
 EXECUTE format('SELECT key,builtin,state FROM %I.role_definitions WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA) INTO role_key,is_builtin,role_state USING NEW.tenant_id,NEW.role_definition_id;
 IF is_builtin=false THEN
  IF role_state<>'active' THEN RAISE EXCEPTION 'role is archived' USING ERRCODE='23514'; END IF;
  IF NEW.permission='access.manage' THEN RAISE EXCEPTION 'administrator authority is protected' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF NOT coalesce(((role_key='reader' AND NEW.permission IN ('ratings.own','resource.read','resource.read_history','resource.read_content','namespace.read')) OR
 (role_key='contributor' AND NEW.permission IN ('ratings.own','resource.read','resource.read_history','resource.read_content','namespace.read','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit')) OR
 (role_key='reviewer' AND NEW.permission IN ('ratings.own','resource.read','resource.read_history','resource.read_content','namespace.read','review.decide')) OR
 (role_key='publisher' AND NEW.permission IN ('ratings.own','resource.read','resource.read_history','resource.read_content','namespace.read','release.publish','release.transition')) OR
 (role_key='discovery_reader' AND NEW.permission IN ('ratings.own','federation.discover','discovery.read','publisher.read','job.read')) OR
 (role_key='publisher_creator' AND NEW.permission IN ('publisher.read','publisher.create')) OR
 (role_key='publication_admin' AND NEW.permission IN ('publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','job.read','job.cancel','job.retry')) OR
 (role_key='source_admin' AND NEW.permission IN ('source.read','source.inspect','source.manage','source.sync','source.read_evidence','federation.discover','job.read','job.cancel','job.retry')) OR
 (role_key='security_reviewer' AND NEW.permission IN ('audit.read','audit.export')) OR
 (role_key='webhook_admin' AND NEW.permission IN ('webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence')) OR
 (role_key='feedback_member' AND NEW.permission IN ('feedback.own')) OR
 (role_key='tenant_admin' AND NEW.permission IN ('settings.read','settings.manage','ratings.moderate','ratings.sharing.manage','ratings.own','resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage','namespace.create','publisher.read','publisher.create','publisher.manage','publisher.approve_domain','federation.discover','source.read','source.inspect','source.manage','source.sync','source.read_evidence','publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','discovery.read','job.read','job.cancel','job.retry','audit.read','audit.export','webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence','feedback.own','feedback.moderate','operations.reindex','access.manage'))),false) THEN RAISE EXCEPTION 'built-in permission set is service-owned' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION initialize_tenant_authorization() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
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
('reader','ratings.own'),
('reader','resource.read'),
('reader','resource.read_history'),
('reader','resource.read_content'),
('reader','namespace.read'),
('contributor','ratings.own'),
('contributor','resource.read'),
('contributor','resource.read_history'),
('contributor','resource.read_content'),
('contributor','namespace.read'),
('contributor','resource.create'),
('contributor','resource.edit'),
('contributor','resource.delete_draft'),
('contributor','resource.validate'),
('contributor','review.submit'),
('reviewer','ratings.own'),
('reviewer','resource.read'),
('reviewer','resource.read_history'),
('reviewer','resource.read_content'),
('reviewer','namespace.read'),
('reviewer','review.decide'),
('publisher','ratings.own'),
('publisher','resource.read'),
('publisher','resource.read_history'),
('publisher','resource.read_content'),
('publisher','namespace.read'),
('publisher','release.publish'),
('publisher','release.transition'),
('discovery_reader','ratings.own'),
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
('tenant_admin','ratings.own'),
('tenant_admin','ratings.moderate'),
('tenant_admin','ratings.sharing.manage'),
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
('tenant_admin','access.manage'),('tenant_admin','settings.read'),('tenant_admin','settings.manage')) AS templates(key,permission)$query$,TG_TABLE_SCHEMA) USING NEW.id;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION validate_role_permission_set() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE tenant uuid; role_id uuid; is_builtin boolean; invalid_set boolean; invalid_scope boolean;
BEGIN
 tenant:=coalesce(NEW.tenant_id,OLD.tenant_id);
 IF TG_TABLE_NAME='role_definitions' THEN role_id:=coalesce(NEW.id,OLD.id); ELSE role_id:=coalesce(NEW.role_definition_id,OLD.role_definition_id); END IF;
 EXECUTE format('SELECT builtin FROM %I.role_definitions WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA) INTO is_builtin USING tenant,role_id;
 IF is_builtin=false THEN
  EXECUTE format($query$SELECT NOT EXISTS(SELECT 1 FROM %1$I.role_permissions WHERE tenant_id=$1 AND role_definition_id=$2)
   OR EXISTS(SELECT 1 FROM %1$I.role_permissions p JOIN (VALUES
    ('settings.manage','settings.read'),('resource.create','namespace.read'),('resource.edit','resource.read'),('resource.edit','resource.read_history'),('resource.edit','resource.read_content'),
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
  ('ratings.own','resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage'))$query$,TG_TABLE_SCHEMA) INTO invalid_scope USING tenant,role_id;
 IF invalid_scope THEN RAISE EXCEPTION 'role permissions conflict with active namespace assignments' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
INSERT INTO role_permissions(tenant_id,role_definition_id,permission)
 SELECT role.tenant_id,role.id,permission FROM role_definitions role CROSS JOIN (VALUES ('settings.read'),('settings.manage')) AS added(permission) WHERE role.builtin AND role.key='tenant_admin';
