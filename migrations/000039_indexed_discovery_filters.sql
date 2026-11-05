-- Derived optional declarations only. Drain runtimes, migrate/reconcile, then
-- run the existing bounded owner repair per tenant before restarting.
-- NULL means unavailable evidence; an empty topic array is known-empty.
ALTER TABLE observations
  ADD COLUMN search_license text,
  ADD COLUMN search_categories text[],
  ADD COLUMN search_filter_version integer NOT NULL DEFAULT 0 CHECK (search_filter_version IN (0,1));

CREATE FUNCTION catalog_control_term_v1(value text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT SET search_path = pg_catalog
AS $function$
  -- Same TrimSpace and simple lowercase as Go; no inner whitespace collapse.
  SELECT translate(lower(btrim(value, U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000') COLLATE pg_catalog.pg_c_utf8),
    U&'\A7CE\A7D2\A7D4\+016EA0\+016EA1\+016EA2\+016EA3\+016EA4\+016EA5\+016EA6\+016EA7\+016EA8\+016EA9\+016EAA\+016EAB\+016EAC\+016EAD\+016EAE\+016EAF\+016EB0\+016EB1\+016EB2\+016EB3\+016EB4\+016EB5\+016EB6\+016EB7\+016EB8', U&'\A7CF\A7D3\A7D5\+016EBB\+016EBC\+016EBD\+016EBE\+016EBF\+016EC0\+016EC1\+016EC2\+016EC3\+016EC4\+016EC5\+016EC6\+016EC7\+016EC8\+016EC9\+016ECA\+016ECB\+016ECC\+016ECD\+016ECE\+016ECF\+016ED0\+016ED1\+016ED2\+016ED3')
$function$;
REVOKE ALL ON FUNCTION catalog_control_term_v1(text) FROM PUBLIC;

DO $definitions$
DECLARE schema_name text := '"' || replace(current_schema(), '"', '""') || '"';
BEGIN
  EXECUTE format($ddl$
    CREATE FUNCTION catalog_observation_license_v1(value jsonb) RETURNS text
    LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE SET search_path = pg_catalog
    AS $function$
    DECLARE declaration text;
    BEGIN
      IF jsonb_typeof(value) IS DISTINCT FROM 'string' THEN RETURN NULL; END IF;
      declaration := value #>> '{}';
      IF char_length(declaration)>256 OR %s.catalog_control_term_v1(declaration)='' THEN RETURN NULL; END IF;
      RETURN declaration; -- Exact case and whitespace, not a rights assessment.
    END
    $function$
  $ddl$, schema_name);
  EXECUTE format($ddl$
    CREATE FUNCTION catalog_observation_categories_v1(value jsonb) RETURNS text[]
    LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE SET search_path = pg_catalog
    AS $function$
    DECLARE labels jsonb; label jsonb; declaration text; projected text[] := ARRAY[]::text[];
    BEGIN
      IF jsonb_typeof(value)='string' THEN labels := jsonb_build_array(value);
      ELSIF jsonb_typeof(value)='array' THEN labels := value;
      ELSE RETURN NULL;
      END IF;
      -- Validate raw counts/lengths/shapes before normalization or deduplication.
      IF jsonb_array_length(labels)>16 THEN RETURN NULL; END IF;
      FOR label IN SELECT jsonb_array_elements(labels) LOOP
        IF jsonb_typeof(label) IS DISTINCT FROM 'string' THEN RETURN NULL; END IF;
        declaration := label #>> '{}';
        IF char_length(declaration)>80 THEN RETURN NULL; END IF;
        declaration := %s.catalog_control_term_v1(declaration);
        IF declaration='' THEN RETURN NULL; END IF;
        projected := array_append(projected,declaration);
      END LOOP;
      RETURN ARRAY(SELECT DISTINCT term COLLATE "C" FROM unnest(projected) AS terms(term) ORDER BY term COLLATE "C");
    END
    $function$
  $ddl$, schema_name);
  EXECUTE format($ddl$
    CREATE FUNCTION project_observation_control_filters() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog
    AS $function$
    DECLARE document jsonb;
    BEGIN
      NEW.search_filter_version := 1;
      NEW.search_license := NULL;
      NEW.search_categories := NULL;
      IF NEW.normalized_entry_digest IS NOT NULL AND NEW.normalized_entry_bytes IS NOT NULL THEN
        document := convert_from(NEW.normalized_entry_bytes,'UTF8')::jsonb;
        NEW.search_license := %s.catalog_observation_license_v1(document#>'{metadata,license}');
        NEW.search_categories := %s.catalog_observation_categories_v1(document#>'{unknownProperties,http://purl.org/dc/terms/subject}');
      END IF;
      RETURN NEW;
    END
    $function$
  $ddl$, schema_name, schema_name);
END
$definitions$;
REVOKE ALL ON FUNCTION catalog_observation_license_v1(jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION catalog_observation_categories_v1(jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION project_observation_control_filters() FROM PUBLIC;
CREATE TRIGGER observations_control_filter_projection
  BEFORE INSERT OR UPDATE ON observations FOR EACH ROW EXECUTE FUNCTION project_observation_control_filters();

DROP INDEX observations_catalog_search_projection_missing;
CREATE INDEX observations_catalog_search_projection_missing ON observations (tenant_id,id)
  WHERE normalized_entry_digest IS NOT NULL
    AND (search_display_name IS NULL OR search_description IS NULL OR search_tags IS NULL
      OR search_representative_queries IS NULL OR search_publisher IS NULL OR search_vector IS NULL
      OR search_vector_version<>2 OR search_filter_version<>1);
COMMENT ON COLUMN observations.search_license IS 'Exact bounded ARD metadata license string; NULL means absent or unusable optional evidence.';
COMMENT ON COLUMN observations.search_categories IS 'Bounded literal Dublin Core subjects normalized for Control category matching; NULL is unavailable, empty is known-empty.';
COMMENT ON COLUMN observations.search_filter_version IS 'Optional Control filter projection: 0 requires bounded owner repair; 1 has independent nullable license/categories.';
