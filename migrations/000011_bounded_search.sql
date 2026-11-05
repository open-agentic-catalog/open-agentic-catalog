CREATE TABLE catalog_search_generations (
  tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  generation bigint NOT NULL CHECK (generation > 0)
);

INSERT INTO catalog_search_generations(tenant_id,generation)
SELECT id,1 FROM tenants;

ALTER TABLE catalog_search_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog_search_generations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON catalog_search_generations
  USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

CREATE FUNCTION catalog_core_lexical_vector_v1(value text) RETURNS tsvector
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
SET search_path = pg_catalog
AS $function$
  SELECT to_tsvector('simple',regexp_replace(lower(coalesce(value,'')),'[^[:alnum:]]+',' ','g'))
$function$;

CREATE FUNCTION catalog_core_lexical_query_v1(value text) RETURNS tsquery
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
SET search_path = pg_catalog
AS $function$
  SELECT plainto_tsquery('simple',regexp_replace(lower(coalesce(value,'')),'[^[:alnum:]]+',' ','g'))
$function$;

CREATE FUNCTION advance_catalog_search_generation() RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $function$
DECLARE
  affected_tenant uuid;
BEGIN
  IF TG_OP = 'DELETE' THEN
    affected_tenant := OLD.tenant_id;
  ELSE
    affected_tenant := NEW.tenant_id;
  END IF;
  EXECUTE format(
    'INSERT INTO %I.catalog_search_generations AS current_generation(tenant_id,generation)
     VALUES ($1,1)
     ON CONFLICT (tenant_id) DO UPDATE
     SET generation = current_generation.generation + 1',
    TG_TABLE_SCHEMA
  ) USING affected_tenant;
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$function$;

ALTER TABLE observations
  ADD COLUMN search_display_name text,
  ADD COLUMN search_description text,
  ADD COLUMN search_tags text[],
  ADD COLUMN search_representative_queries text[],
  ADD COLUMN search_publisher text,
  ADD COLUMN search_vector tsvector;

CREATE FUNCTION project_observation_catalog_search() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  document jsonb;
BEGIN
  IF NEW.normalized_entry_bytes IS NULL THEN
    NEW.search_display_name := NULL;
    NEW.search_description := NULL;
    NEW.search_tags := NULL;
    NEW.search_representative_queries := NULL;
    NEW.search_publisher := NULL;
    NEW.search_vector := NULL;
    RETURN NEW;
  END IF;

  document := convert_from(NEW.normalized_entry_bytes,'UTF8')::jsonb;
  NEW.search_display_name := coalesce(document->>'displayName','');
  NEW.search_description := coalesce(document->>'description','');
  SELECT coalesce(array_agg(tag.value ORDER BY tag.value),ARRAY[]::text[])
    INTO NEW.search_tags
    FROM (
      SELECT DISTINCT lower(btrim(value)) AS value
      FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(document->'tags')='array' THEN document->'tags' ELSE '[]'::jsonb END)
      WHERE btrim(value) <> ''
    ) tag;
  SELECT coalesce(array_agg(query.value ORDER BY query.ordinality),ARRAY[]::text[])
    INTO NEW.search_representative_queries
    FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(document->'representativeQueries')='array' THEN document->'representativeQueries' ELSE '[]'::jsonb END) WITH ORDINALITY AS query(value,ordinality);
  NEW.search_publisher := split_part(NEW.identifier,':',3);
  NEW.search_vector :=
    to_tsvector('simple',regexp_replace(lower(coalesce(NEW.identifier,'')),'[^[:alnum:]]+',' ','g')) ||
    to_tsvector('simple',regexp_replace(lower(coalesce(NEW.search_display_name,'')),'[^[:alnum:]]+',' ','g')) ||
    to_tsvector('simple',regexp_replace(lower(coalesce(NEW.search_description,'')),'[^[:alnum:]]+',' ','g')) ||
    to_tsvector('simple',regexp_replace(lower(coalesce(array_to_string(NEW.search_tags,' '),'')),'[^[:alnum:]]+',' ','g')) ||
    to_tsvector('simple',regexp_replace(lower(coalesce(array_to_string(NEW.search_representative_queries,' '),'')),'[^[:alnum:]]+',' ','g'));
  RETURN NEW;
END
$function$;

CREATE TRIGGER observations_catalog_search_projection
  BEFORE INSERT OR UPDATE ON observations
  FOR EACH ROW EXECUTE FUNCTION project_observation_catalog_search();

ALTER TABLE observations
  ADD CONSTRAINT observations_search_display_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_display_name IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_description_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_description IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_tags_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_tags IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_queries_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_representative_queries IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_publisher_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_publisher IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_vector_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_vector IS NULL)) NOT VALID;

CREATE INDEX catalog_search_current_releases
  ON listing_releases (tenant_id,state,id)
  WHERE state IN ('published','deprecated');

CREATE INDEX observations_catalog_search_identity
  ON observations (tenant_id,identifier,version_present,version,normalized_entry_digest,source_id)
  WHERE freshness='current' AND normalized_entry_digest IS NOT NULL;

CREATE INDEX observations_catalog_search_vector
  ON observations USING gin (search_vector)
  WHERE freshness='current' AND normalized_entry_digest IS NOT NULL;

CREATE INDEX observations_catalog_search_tags
  ON observations USING gin (search_tags)
  WHERE freshness='current' AND normalized_entry_digest IS NOT NULL;

-- Install invalidation after the schema expansion. Migration execution has no
-- application tenant context, while normal writers and the bounded repair
-- command set one before a discovery-affecting transaction.
DO $triggers$
DECLARE
  table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'resources', 'resource_versions', 'resource_version_revisions', 'evidence_sets',
    'version_validation_runs', 'catalog_listings', 'listing_revisions', 'listing_releases',
    'federation_sources', 'observations'
  ] LOOP
    EXECUTE format(
      'CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION advance_catalog_search_generation()',
      table_name || '_search_generation', table_name
    );
  END LOOP;
END
$triggers$;

COMMENT ON TABLE catalog_search_generations IS 'Conservative tenant generation invalidating local and indexed Catalog cursors after discovery-affecting writes.';
COMMENT ON COLUMN observations.search_vector IS 'Rebuildable core-lexical-v1 projection over allowlisted normalized observation fields.';
COMMENT ON FUNCTION catalog_core_lexical_vector_v1(text) IS 'Versioned core-lexical-v1 Unicode alphanumeric token projection shared by bounded PostgreSQL search paths.';
COMMENT ON FUNCTION catalog_core_lexical_query_v1(text) IS 'Versioned core-lexical-v1 Unicode alphanumeric query projection shared by bounded PostgreSQL search paths.';
