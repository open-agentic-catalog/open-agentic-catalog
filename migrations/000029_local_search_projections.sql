-- Compute immutable local search fields once on write, not for every candidate
-- in every count/page/facet query. STORED is deliberate: virtual columns would
-- repeat the measured normalization cost. Original metadata/digests stay intact.
-- Apply with runtimes drained; PostgreSQL materializes existing rows under the
-- table lock. No runtime DML permission is granted on these derived columns.
DO $definition$
BEGIN
  EXECUTE format($ddl$
    CREATE FUNCTION catalog_core_lexical_array_vector_v1(value text[]) RETURNS tsvector
    LANGUAGE sql IMMUTABLE PARALLEL SAFE SET search_path = pg_catalog
    AS $function$ SELECT %I.catalog_core_lexical_vector_v1(pg_catalog.array_to_string(value,' ')) $function$
  $ddl$, current_schema());
END
$definition$;
REVOKE ALL ON FUNCTION catalog_core_lexical_array_vector_v1(text[]) FROM PUBLIC;

ALTER TABLE resources
  ADD COLUMN search_identifier_vector tsvector GENERATED ALWAYS AS (catalog_core_lexical_vector_v1(canonical_identifier)) STORED NOT NULL;

ALTER TABLE listing_revisions
  ADD COLUMN search_display_vector tsvector GENERATED ALWAYS AS (catalog_core_lexical_vector_v1(display_name)) STORED NOT NULL,
  ADD COLUMN search_description_vector tsvector GENERATED ALWAYS AS (catalog_core_lexical_vector_v1(description)) STORED NOT NULL,
  ADD COLUMN search_tags_vector tsvector GENERATED ALWAYS AS (catalog_core_lexical_array_vector_v1(tags)) STORED NOT NULL,
  ADD COLUMN search_queries_vector tsvector GENERATED ALWAYS AS (catalog_core_lexical_array_vector_v1(representative_queries)) STORED NOT NULL,
  ADD COLUMN search_sort_name text GENERATED ALWAYS AS (catalog_core_sort_name_v1(display_name)) STORED NOT NULL;

COMMENT ON COLUMN resources.search_identifier_vector IS 'Database-maintained positionless core-lexical-v1 projection; immutable identifier remains authoritative.';
COMMENT ON COLUMN listing_revisions.search_display_vector IS 'Database-maintained core-lexical-v1 display-name projection for matching and unchanged field weights.';
COMMENT ON COLUMN listing_revisions.search_description_vector IS 'Database-maintained core-lexical-v1 description projection.';
COMMENT ON COLUMN listing_revisions.search_tags_vector IS 'Database-maintained core-lexical-v1 space-separated tags projection.';
COMMENT ON COLUMN listing_revisions.search_queries_vector IS 'Database-maintained core-lexical-v1 representative-query projection.';
COMMENT ON COLUMN listing_revisions.search_sort_name IS 'Database-maintained sort-name normalization; queries retain C-collation ordering.';
