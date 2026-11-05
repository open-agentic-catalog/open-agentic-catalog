-- Exact audit attribution is selection evidence, never an alternate work status.
CREATE INDEX audit_events_personal_work ON audit_events(tenant_id,actor,(details->>'realmId'),target_type,target_id,action,(details->'facts'->>'revisionId')) WHERE format_version=1 AND details->>'principalType'='catalog_membership' AND details->>'actorType'='human';
CREATE TABLE personal_items (
 tenant_id uuid NOT NULL REFERENCES tenants(id), membership_id uuid NOT NULL, realm_id uuid NOT NULL,
 id uuid NOT NULL, input jsonb NOT NULL CHECK(jsonb_typeof(input)='object' AND octet_length(input::text)<=16384 AND input->>'type' IN ('resource','search') AND char_length(input->>'label') BETWEEN 1 AND 120),
 revision bigint NOT NULL CHECK(revision>0), created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL CHECK(updated_at>=created_at),
 PRIMARY KEY(tenant_id,id), FOREIGN KEY(tenant_id,membership_id,realm_id) REFERENCES tenant_memberships(tenant_id,id,realm_id)
);
CREATE INDEX personal_items_owner_page ON personal_items(tenant_id,membership_id,realm_id,id);
ALTER TABLE personal_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE personal_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON personal_items USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY personal_owner ON personal_items AS RESTRICTIVE USING(membership_id=nullif(current_setting('app.membership_id',true),'')::uuid) WITH CHECK(membership_id=nullif(current_setting('app.membership_id',true),'')::uuid);
CREATE FUNCTION guard_personal_item() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $function$
DECLARE human_owner boolean; item_count integer;
BEGIN
 IF TG_OP='UPDATE' AND (ROW(NEW.tenant_id,NEW.id,NEW.membership_id,NEW.realm_id,NEW.created_at) IS DISTINCT FROM ROW(OLD.tenant_id,OLD.id,OLD.membership_id,OLD.realm_id,OLD.created_at) OR NEW.revision<>OLD.revision+1 OR NEW.updated_at<OLD.updated_at) THEN RAISE EXCEPTION 'invalid personal item transition'; END IF;
 IF TG_OP='INSERT' THEN
  EXECUTE format('SELECT true FROM %I.tenant_memberships WHERE tenant_id=$1 AND id=$2 AND realm_id=$3 AND state=''active'' AND actor_type=''human'' FOR UPDATE',TG_TABLE_SCHEMA) INTO human_owner USING NEW.tenant_id,NEW.membership_id,NEW.realm_id;
  IF human_owner IS DISTINCT FROM true THEN RAISE EXCEPTION 'personal library requires an active human'; END IF;
  EXECUTE format('SELECT count(*) FROM (SELECT id FROM %I.personal_items WHERE tenant_id=$1 AND membership_id=$2 AND realm_id=$3 LIMIT 200) bounded',TG_TABLE_SCHEMA) INTO item_count USING NEW.tenant_id,NEW.membership_id,NEW.realm_id;
  IF item_count>=200 THEN RAISE EXCEPTION 'personal library quota exceeded'; END IF;
  IF NEW.revision<>1 THEN RAISE EXCEPTION 'invalid personal item initial revision'; END IF;
 END IF;
 RETURN NEW;
END $function$;
CREATE TRIGGER personal_items_guard BEFORE INSERT OR UPDATE ON personal_items FOR EACH ROW EXECUTE FUNCTION guard_personal_item();
REVOKE ALL ON FUNCTION guard_personal_item() FROM PUBLIC;

ALTER TABLE role_definitions DROP CONSTRAINT role_definition_identity;
ALTER TABLE role_definitions ADD CONSTRAINT role_definition_identity CHECK (
 (builtin AND key IS NOT NULL AND key IN ('reader','contributor','reviewer','publisher','discovery_reader','publisher_creator','publication_admin','source_admin','security_reviewer','webhook_admin','feedback_member','library_member','tenant_admin','service_client') AND name IS NULL)
 OR (NOT builtin AND key IS NULL AND name IS NOT NULL AND length(name) BETWEEN 1 AND 120 AND name=btrim(name)));
ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_permission_check;
ALTER TABLE role_permissions ADD CONSTRAINT role_permissions_permission_check CHECK(permission IN ('resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage','namespace.create','publisher.read','publisher.create','publisher.manage','publisher.approve_domain','federation.discover','source.read','source.inspect','source.manage','source.sync','source.read_evidence','publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','discovery.read','job.read','job.cancel','job.retry','audit.read','audit.export','webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence','feedback.own','feedback.moderate','operations.reindex','access.manage','ratings.own','ratings.moderate','ratings.sharing.manage','settings.read','settings.manage','library.own'));
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
 (role_key='discovery_reader' AND NEW.permission IN ('library.own','ratings.own','federation.discover','discovery.read','publisher.read','job.read')) OR
 (role_key='publisher_creator' AND NEW.permission IN ('publisher.read','publisher.create')) OR
 (role_key='publication_admin' AND NEW.permission IN ('publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','job.read','job.cancel','job.retry')) OR
 (role_key='source_admin' AND NEW.permission IN ('source.read','source.inspect','source.manage','source.sync','source.read_evidence','federation.discover','job.read','job.cancel','job.retry')) OR
 (role_key='security_reviewer' AND NEW.permission IN ('audit.read','audit.export')) OR
 (role_key='webhook_admin' AND NEW.permission IN ('webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence')) OR
 (role_key='library_member' AND NEW.permission IN ('library.own')) OR
 (role_key='feedback_member' AND NEW.permission IN ('feedback.own')) OR
 (role_key='tenant_admin' AND NEW.permission IN ('library.own','settings.read','settings.manage','ratings.moderate','ratings.sharing.manage','ratings.own','resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage','namespace.create','publisher.read','publisher.create','publisher.manage','publisher.approve_domain','federation.discover','source.read','source.inspect','source.manage','source.sync','source.read_evidence','publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','discovery.read','job.read','job.cancel','job.retry','audit.read','audit.export','webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence','feedback.own','feedback.moderate','operations.reindex','access.manage'))),false) THEN RAISE EXCEPTION 'built-in permission set is service-owned' USING ERRCODE='23514'; END IF;
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
('library_member'),
('tenant_admin'),
('service_client')) AS templates(key)$query$,TG_TABLE_SCHEMA) USING NEW.id;
 EXECUTE format($query$INSERT INTO %1$I.role_permissions(tenant_id,role_definition_id,permission)
 SELECT $1,md5($1::text || ':' || key)::uuid,permission FROM (VALUES
('library_member','library.own'),
('discovery_reader','library.own'),
('tenant_admin','library.own'),
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

INSERT INTO role_definitions(id,tenant_id,key,builtin,state,revision) SELECT md5(id::text||':library_member')::uuid,id,'library_member',true,'active',1 FROM tenants;
INSERT INTO role_permissions(tenant_id,role_definition_id,permission) SELECT tenant_id,id,'library.own' FROM role_definitions WHERE builtin AND key IN ('library_member','discovery_reader','tenant_admin');
