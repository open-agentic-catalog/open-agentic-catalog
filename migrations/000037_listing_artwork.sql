-- Presentation authority is separate from retained remote evidence. Source
-- generations live in immutable source configuration; old configurations with
-- no generation mean the pre-feature generation one.
CREATE FUNCTION guard_source_presentation_generation() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $function$
DECLARE old_generation bigint; new_generation bigint; changed boolean;
BEGIN
  new_generation := coalesce((NEW.configuration->>'presentationGeneration')::bigint,1);
  IF new_generation<1 THEN RAISE EXCEPTION 'invalid source presentation generation'; END IF;
  IF TG_OP='INSERT' THEN
    IF new_generation<>1 THEN RAISE EXCEPTION 'initial source presentation generation must be one'; END IF;
  ELSE
    old_generation:=coalesce((OLD.configuration->>'presentationGeneration')::bigint,1);
    changed:=(NEW.source_type,NEW.endpoint,NEW.configuration->>'profileId',NEW.configuration->>'registrySearchUrl',NEW.configuration->>'registryListUrl') IS DISTINCT FROM
             (OLD.source_type,OLD.endpoint,OLD.configuration->>'profileId',OLD.configuration->>'registrySearchUrl',OLD.configuration->>'registryListUrl');
    IF new_generation<>old_generation+(CASE WHEN changed THEN 1 ELSE 0 END) THEN RAISE EXCEPTION 'invalid source presentation generation transition'; END IF;
  END IF;
  RETURN NEW;
END;
$function$;
REVOKE ALL ON FUNCTION guard_source_presentation_generation() FROM PUBLIC;
CREATE TRIGGER federation_sources_presentation_generation BEFORE INSERT OR UPDATE ON federation_sources FOR EACH ROW EXECUTE FUNCTION guard_source_presentation_generation();

CREATE TABLE source_artwork_revisions (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL,
 source_id uuid NOT NULL,
 presentation_generation bigint NOT NULL CHECK(presentation_generation>0),
 target_key text NOT NULL CHECK(octet_length(target_key)<=256),
 identifier text NOT NULL CHECK(octet_length(identifier)<=512),
 version_present boolean NOT NULL,
 version text NOT NULL CHECK(octet_length(version)<=128 AND (version_present OR version='')),
 kind text NOT NULL CHECK(kind IN ('agent','mcp_server','skill','plugin')),
 revision bigint NOT NULL CHECK(revision>0),
 document jsonb NOT NULL CHECK(jsonb_typeof(document)='object' AND octet_length(document::text)<=49152),
 digest text NOT NULL CHECK(digest ~ '^sha256:[0-9a-f]{64}$'),
 created_by text NOT NULL,
 created_at timestamptz NOT NULL,
 CHECK((document->>'id'=id::text AND document->>'tenantId'=tenant_id::text AND document->>'revision'=revision::text AND document->>'digest'=digest AND document->>'createdBy'=created_by AND document#>>'{target,sourceId}'=source_id::text AND document#>>'{target,presentationGeneration}'=presentation_generation::text AND document#>>'{target,identifier}'=identifier AND document#>>'{target,kind}'=kind AND (document#>>'{target,versionPresent}')::boolean=version_present AND coalesce(document#>>'{target,version}','')=version) IS TRUE),
 PRIMARY KEY(tenant_id,id),
 UNIQUE(tenant_id,source_id,presentation_generation,target_key,revision),
 UNIQUE(tenant_id,source_id,presentation_generation,target_key,id),
 FOREIGN KEY(tenant_id,source_id) REFERENCES federation_sources(tenant_id,id)
);
CREATE TABLE source_artwork_heads (
 tenant_id uuid NOT NULL,
 source_id uuid NOT NULL,
 presentation_generation bigint NOT NULL CHECK(presentation_generation>0),
 target_key text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0),
 current_revision_id uuid NOT NULL,
 PRIMARY KEY(tenant_id,source_id,presentation_generation,target_key),
 FOREIGN KEY(tenant_id,source_id) REFERENCES federation_sources(tenant_id,id),
 FOREIGN KEY(tenant_id,source_id,presentation_generation,target_key,current_revision_id) REFERENCES source_artwork_revisions(tenant_id,source_id,presentation_generation,target_key,id)
);
ALTER TABLE source_artwork_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE source_artwork_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON source_artwork_revisions USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
ALTER TABLE source_artwork_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE source_artwork_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON source_artwork_heads USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE TRIGGER source_artwork_revisions_immutable BEFORE UPDATE OR DELETE ON source_artwork_revisions FOR EACH ROW EXECUTE FUNCTION reject_federation_source_history_mutation();
CREATE FUNCTION guard_source_artwork_head() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $function$
DECLARE matched boolean;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'artwork history cannot be deleted'; END IF;
 IF TG_OP='UPDATE' AND ((NEW.tenant_id,NEW.source_id,NEW.presentation_generation,NEW.target_key) IS DISTINCT FROM (OLD.tenant_id,OLD.source_id,OLD.presentation_generation,OLD.target_key) OR NEW.revision<>OLD.revision+1) THEN RAISE EXCEPTION 'invalid artwork head transition'; END IF;
 IF TG_OP='INSERT' AND NEW.revision<>1 THEN RAISE EXCEPTION 'invalid initial artwork revision'; END IF;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.source_artwork_revisions WHERE tenant_id=$1 AND source_id=$2 AND presentation_generation=$3 AND target_key=$4 AND id=$5 AND revision=$6)',TG_TABLE_SCHEMA)
 INTO matched USING NEW.tenant_id,NEW.source_id,NEW.presentation_generation,NEW.target_key,NEW.current_revision_id,NEW.revision;
 IF NOT matched THEN RAISE EXCEPTION 'artwork revision binding mismatch'; END IF;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.federation_sources WHERE tenant_id=$1 AND id=$2 AND status<>''removed'' AND coalesce((configuration->>''presentationGeneration'')::bigint,1)=$3)',TG_TABLE_SCHEMA)
 INTO matched USING NEW.tenant_id,NEW.source_id,NEW.presentation_generation;
 IF NOT matched THEN RAISE EXCEPTION 'artwork source generation mismatch'; END IF;
 RETURN NEW;
END;
$function$;
REVOKE ALL ON FUNCTION guard_source_artwork_head() FROM PUBLIC;
CREATE TRIGGER source_artwork_heads_lifecycle BEFORE INSERT OR UPDATE OR DELETE ON source_artwork_heads FOR EACH ROW EXECUTE FUNCTION guard_source_artwork_head();

-- Rebuildable compact candidates are derived during existing normalization.
-- No collection read needs to hydrate an entire retained native document.
ALTER TABLE observations ADD COLUMN artwork_metadata jsonb CHECK(artwork_metadata IS NULL OR (jsonb_typeof(artwork_metadata)='object' AND octet_length(artwork_metadata::text)<=49152));
