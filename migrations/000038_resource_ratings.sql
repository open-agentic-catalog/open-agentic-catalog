-- Human opinions are tenant-local. Votes never change discovery relevance.
CREATE TABLE rating_targets (
 tenant_id uuid NOT NULL REFERENCES tenants(id), target_key text NOT NULL CHECK(target_key ~ '^sha256:[0-9a-f]{64}$'),
 target jsonb NOT NULL CHECK(jsonb_typeof(target)='object' AND octet_length(target::text)<=2048),
 resource_id uuid, version_revision_id uuid, source_id uuid,
 vote_count bigint NOT NULL DEFAULT 0, score_sum bigint NOT NULL DEFAULT 0,
 shared_count bigint NOT NULL DEFAULT 0, shared_sum bigint NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,target_key),
 FOREIGN KEY(tenant_id,resource_id) REFERENCES resources(tenant_id,id),
 FOREIGN KEY(tenant_id,version_revision_id) REFERENCES resource_version_revisions(tenant_id,id),
 FOREIGN KEY(tenant_id,source_id) REFERENCES federation_sources(tenant_id,id),
 CHECK((target->>'origin'='local' AND resource_id IS NOT NULL AND version_revision_id IS NOT NULL AND source_id IS NULL AND target->>'resourceId'=resource_id::text AND target->>'versionRevisionId'=version_revision_id::text AND (target->>'versionPresent')::boolean=true) OR (target->>'origin'='foreign' AND resource_id IS NULL AND version_revision_id IS NULL AND source_id IS NOT NULL AND target->>'sourceId'=source_id::text AND (target->>'presentationGeneration')::bigint>0)),
 CHECK(vote_count BETWEEN 0 AND 1801439850948198 AND score_sum BETWEEN vote_count AND 5*vote_count),
 CHECK(shared_count BETWEEN 0 AND vote_count AND shared_sum BETWEEN shared_count AND 5*shared_count AND shared_sum<=score_sum)
);
CREATE UNIQUE INDEX rating_targets_local ON rating_targets(tenant_id,resource_id,version_revision_id) WHERE resource_id IS NOT NULL;
CREATE UNIQUE INDEX rating_targets_foreign ON rating_targets(tenant_id,source_id,(target->>'presentationGeneration'),(target->>'identifier'),(target->>'versionPresent'),(coalesce(target->>'version','')),(target->>'kind')) WHERE source_id IS NOT NULL;
CREATE TABLE item_ratings (
 tenant_id uuid NOT NULL, id uuid NOT NULL, target_key text NOT NULL,
 principal_key text NOT NULL CHECK(principal_key ~ '^sha256:[0-9a-f]{64}$'), submitted_by uuid NOT NULL,
 score integer NOT NULL, consent boolean NOT NULL, state text NOT NULL CHECK(state IN ('active','withdrawn','removed')),
 release_id uuid, entry_digest text NOT NULL DEFAULT '' CHECK(entry_digest='' OR entry_digest ~ '^sha256:[0-9a-f]{64}$'),
 revision bigint NOT NULL CHECK(revision>0), created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL CHECK(updated_at>=created_at),
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,target_key,principal_key),
 FOREIGN KEY(tenant_id,target_key) REFERENCES rating_targets(tenant_id,target_key),
 FOREIGN KEY(tenant_id,submitted_by) REFERENCES tenant_memberships(tenant_id,id),
 FOREIGN KEY(tenant_id,release_id) REFERENCES listing_releases(tenant_id,id),
 CHECK((state='active' AND score BETWEEN 1 AND 5) OR (state<>'active' AND score=0 AND NOT consent))
);
CREATE INDEX item_ratings_own_page ON item_ratings(tenant_id,principal_key,id);
CREATE INDEX item_ratings_state_page ON item_ratings(tenant_id,state,id);
CREATE TABLE item_rating_revisions (
 tenant_id uuid NOT NULL, rating_id uuid NOT NULL, revision bigint NOT NULL,
 score integer NOT NULL, consent boolean NOT NULL, state text NOT NULL, entry_digest text NOT NULL, recorded_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,rating_id,revision), FOREIGN KEY(tenant_id,rating_id) REFERENCES item_ratings(tenant_id,id)
);
CREATE TABLE rating_sharing (
 tenant_id uuid NOT NULL REFERENCES tenants(id), id uuid NOT NULL, host text NOT NULL CHECK(length(host) BETWEEN 1 AND 253),
 audience text NOT NULL CHECK(audience IN ('anonymous','authenticated')), enabled boolean NOT NULL,
 revision bigint NOT NULL CHECK(revision>0), updated_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,host,audience)
);
DO $$ DECLARE relation text; BEGIN
 FOREACH relation IN ARRAY ARRAY['rating_targets','item_ratings','item_rating_revisions','rating_sharing'] LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',relation);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',relation);
  EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id=nullif(current_setting(''app.tenant_id'',true),'''')::uuid) WITH CHECK (tenant_id=nullif(current_setting(''app.tenant_id'',true),'''')::uuid)',relation);
 END LOOP;
END $$;
CREATE FUNCTION guard_rating_target() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE related boolean;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'rating target deletion is not permitted'; END IF;
 IF TG_OP='UPDATE' THEN
  IF (NEW.tenant_id,NEW.target_key,NEW.target,NEW.resource_id,NEW.version_revision_id,NEW.source_id) IS DISTINCT FROM (OLD.tenant_id,OLD.target_key,OLD.target,OLD.resource_id,OLD.version_revision_id,OLD.source_id) THEN RAISE EXCEPTION 'rating target identity is immutable'; END IF;
  RETURN NEW;
 END IF;
 IF (NEW.vote_count,NEW.score_sum,NEW.shared_count,NEW.shared_sum)<>(0,0,0,0) THEN RAISE EXCEPTION 'rating target starts empty'; END IF;
 IF NEW.resource_id IS NOT NULL THEN
  EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.resource_version_revisions revision JOIN %I.resource_versions version ON version.tenant_id=revision.tenant_id AND version.id=revision.resource_version_id JOIN %I.resources resource ON resource.tenant_id=version.tenant_id AND resource.id=version.resource_id WHERE revision.tenant_id=$1 AND revision.id=$2 AND resource.id=$3 AND resource.canonical_identifier=$4 AND resource.kind=$5 AND version.version=$6)',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
  INTO related USING NEW.tenant_id,NEW.version_revision_id,NEW.resource_id,NEW.target->>'identifier',NEW.target->>'kind',coalesce(NEW.target->>'version','');
  IF NOT related THEN RAISE EXCEPTION 'rating resource version mismatch'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER rating_targets_guard BEFORE INSERT OR UPDATE OR DELETE ON rating_targets FOR EACH ROW EXECUTE FUNCTION guard_rating_target();
CREATE FUNCTION guard_item_rating() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE related boolean;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'rating deletion is not permitted'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.revision<>1 OR NEW.state<>'active' OR NEW.created_at<>NEW.updated_at THEN RAISE EXCEPTION 'invalid rating creation'; END IF;
 ELSE
  IF (NEW.tenant_id,NEW.id,NEW.target_key,NEW.principal_key,NEW.created_at) IS DISTINCT FROM (OLD.tenant_id,OLD.id,OLD.target_key,OLD.principal_key,OLD.created_at) OR NEW.revision<>OLD.revision+1 OR NEW.updated_at<OLD.updated_at OR (OLD.state='removed' AND NEW.state<>'withdrawn') THEN RAISE EXCEPTION 'invalid rating transition'; END IF;
  IF NEW.state<>'active' AND (NEW.release_id,NEW.entry_digest,NEW.submitted_by) IS DISTINCT FROM (OLD.release_id,OLD.entry_digest,OLD.submitted_by) THEN RAISE EXCEPTION 'rating removal cannot rewrite context'; END IF;
 END IF;
 IF NEW.state='active' THEN
  EXECUTE format('SELECT (resource_id IS NOT NULL) = ($3::uuid IS NOT NULL) FROM %I.rating_targets WHERE tenant_id=$1 AND target_key=$2',TG_TABLE_SCHEMA) INTO related USING NEW.tenant_id,NEW.target_key,NEW.release_id;
  IF related IS DISTINCT FROM true THEN RAISE EXCEPTION 'rating release context mismatch'; END IF;
  EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.tenant_memberships WHERE tenant_id=$1 AND id=$2 AND state=''active'' AND actor_type=''human'')',TG_TABLE_SCHEMA) INTO related USING NEW.tenant_id,NEW.submitted_by;
  IF NOT related THEN RAISE EXCEPTION 'rating requires an active human'; END IF;
  IF NEW.release_id IS NOT NULL THEN
   EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.rating_targets target JOIN %I.listing_releases release ON release.tenant_id=target.tenant_id AND release.version_revision_id=target.version_revision_id JOIN %I.catalog_listings listing ON listing.tenant_id=release.tenant_id AND listing.id=release.listing_id AND listing.current_release_id=release.id WHERE target.tenant_id=$1 AND target.target_key=$2 AND release.id=$3 AND release.state IN (''published'',''deprecated''))',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA) INTO related USING NEW.tenant_id,NEW.target_key,NEW.release_id;
   IF NOT related THEN RAISE EXCEPTION 'rating release is not current'; END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER item_ratings_guard BEFORE INSERT OR UPDATE OR DELETE ON item_ratings FOR EACH ROW EXECUTE FUNCTION guard_item_rating();
-- Only this fixed owner function can maintain rollups and immutable minimal history.
CREATE FUNCTION record_item_rating() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE old_count bigint:=0; old_sum bigint:=0; old_shared_count bigint:=0; old_shared_sum bigint:=0; new_count bigint:=0; new_sum bigint:=0; new_shared_count bigint:=0; new_shared_sum bigint:=0;
BEGIN
 IF TG_OP='UPDATE' AND OLD.state='active' THEN old_count:=1;old_sum:=OLD.score;IF OLD.consent THEN old_shared_count:=1;old_shared_sum:=OLD.score;END IF;END IF;
 IF NEW.state='active' THEN new_count:=1;new_sum:=NEW.score;IF NEW.consent THEN new_shared_count:=1;new_shared_sum:=NEW.score;END IF;END IF;
 EXECUTE format('UPDATE %I.rating_targets SET vote_count=vote_count+$3,score_sum=score_sum+$4,shared_count=shared_count+$5,shared_sum=shared_sum+$6 WHERE tenant_id=$1 AND target_key=$2',TG_TABLE_SCHEMA) USING NEW.tenant_id,NEW.target_key,new_count-old_count,new_sum-old_sum,new_shared_count-old_shared_count,new_shared_sum-old_shared_sum;
 EXECUTE format('INSERT INTO %I.item_rating_revisions(tenant_id,rating_id,revision,score,consent,state,entry_digest,recorded_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)',TG_TABLE_SCHEMA) USING NEW.tenant_id,NEW.id,NEW.revision,NEW.score,NEW.consent,NEW.state,NEW.entry_digest,NEW.updated_at;
 RETURN NEW;
END $$;
CREATE TRIGGER item_ratings_record AFTER INSERT OR UPDATE ON item_ratings FOR EACH ROW EXECUTE FUNCTION record_item_rating();
CREATE TRIGGER item_rating_revisions_immutable BEFORE UPDATE OR DELETE ON item_rating_revisions FOR EACH ROW EXECUTE FUNCTION reject_federation_source_history_mutation();
CREATE FUNCTION guard_rating_sharing() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'rating sharing deletion is not permitted'; END IF;
 IF TG_OP='INSERT' AND NEW.revision<>1 THEN RAISE EXCEPTION 'invalid rating sharing creation'; END IF;
 IF TG_OP='UPDATE' AND ((NEW.tenant_id,NEW.id,NEW.host,NEW.audience) IS DISTINCT FROM (OLD.tenant_id,OLD.id,OLD.host,OLD.audience) OR NEW.revision<>OLD.revision+1 OR NEW.updated_at<OLD.updated_at) THEN RAISE EXCEPTION 'invalid rating sharing transition'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER rating_sharing_guard BEFORE INSERT OR UPDATE OR DELETE ON rating_sharing FOR EACH ROW EXECUTE FUNCTION guard_rating_sharing();
REVOKE ALL ON FUNCTION guard_rating_target(),guard_item_rating(),record_item_rating(),guard_rating_sharing() FROM PUBLIC;

ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_permission_check;
ALTER TABLE role_permissions ADD CONSTRAINT role_permissions_permission_check CHECK(permission IN ('ratings.own','ratings.moderate','ratings.sharing.manage','resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage','namespace.create','publisher.read','publisher.create','publisher.manage','publisher.approve_domain','federation.discover','source.read','source.inspect','source.manage','source.sync','source.read_evidence','publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','discovery.read','job.read','job.cancel','job.retry','audit.read','audit.export','webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence','feedback.own','feedback.moderate','operations.reindex','access.manage'));
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
 (role_key='tenant_admin' AND NEW.permission IN ('ratings.moderate','ratings.sharing.manage','ratings.own','resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage','namespace.create','publisher.read','publisher.create','publisher.manage','publisher.approve_domain','federation.discover','source.read','source.inspect','source.manage','source.sync','source.read_evidence','publication.read','publication.generate','publication.activate','publication.rollback','publication.unpublish','discovery.read','job.read','job.cancel','job.retry','audit.read','audit.export','webhook.read','webhook.manage','webhook.test','webhook.rotate','webhook.read_evidence','feedback.own','feedback.moderate','operations.reindex','access.manage'))),false) THEN RAISE EXCEPTION 'built-in permission set is service-owned' USING ERRCODE='23514'; END IF;
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
('tenant_admin','access.manage')) AS templates(key,permission)$query$,TG_TABLE_SCHEMA) USING NEW.id;
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
  ('ratings.own','resource.read','resource.read_history','resource.read_content','resource.create','resource.edit','resource.delete_draft','resource.validate','review.submit','review.decide','release.publish','release.transition','namespace.read','namespace.manage'))$query$,TG_TABLE_SCHEMA) INTO invalid_scope USING tenant,role_id;
 IF invalid_scope THEN RAISE EXCEPTION 'role permissions conflict with active namespace assignments' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
INSERT INTO role_permissions(tenant_id,role_definition_id,permission)
 SELECT tenant_id,id,'ratings.own' FROM role_definitions WHERE builtin AND key IN ('reader','contributor','reviewer','publisher','discovery_reader','tenant_admin');
INSERT INTO role_permissions(tenant_id,role_definition_id,permission)
 SELECT role.tenant_id,role.id,permission FROM role_definitions role CROSS JOIN (VALUES ('ratings.moderate'),('ratings.sharing.manage')) AS added(permission) WHERE role.builtin AND role.key='tenant_admin';

ALTER TABLE observations ADD COLUMN ratings_metadata jsonb CHECK(ratings_metadata IS NULL OR (jsonb_typeof(ratings_metadata)='object' AND octet_length(ratings_metadata::text)<=2048));
