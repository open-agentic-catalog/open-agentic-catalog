-- Materialize the field-specific ranking vectors used by indexed Search.
-- The existing combined vector remains the matching/repair boundary. Raw and
-- normalized evidence, source identity and conflict semantics stay unchanged.
-- Apply with runtimes drained; PostgreSQL computes existing values under lock.
ALTER TABLE observations
  ADD COLUMN search_identifier_vector tsvector GENERATED ALWAYS AS (catalog_core_lexical_vector_v1(identifier)) STORED NOT NULL,
  ADD COLUMN search_display_vector tsvector GENERATED ALWAYS AS (catalog_core_lexical_vector_v1(search_display_name)) STORED NOT NULL,
  ADD COLUMN search_description_vector tsvector GENERATED ALWAYS AS (catalog_core_lexical_vector_v1(search_description)) STORED NOT NULL,
  ADD COLUMN search_tags_vector tsvector GENERATED ALWAYS AS (catalog_core_lexical_array_vector_v1(search_tags)) STORED NOT NULL,
  ADD COLUMN search_queries_vector tsvector GENERATED ALWAYS AS (catalog_core_lexical_array_vector_v1(search_representative_queries)) STORED NOT NULL;

COMMENT ON COLUMN observations.search_identifier_vector IS 'Database-maintained core-lexical-v1 identifier ranking projection.';
COMMENT ON COLUMN observations.search_display_vector IS 'Database-maintained raw display-name ranking projection; identifier fallback does not increase relevance.';
COMMENT ON COLUMN observations.search_description_vector IS 'Database-maintained core-lexical-v1 description ranking projection.';
COMMENT ON COLUMN observations.search_tags_vector IS 'Database-maintained core-lexical-v1 space-separated tags ranking projection.';
COMMENT ON COLUMN observations.search_queries_vector IS 'Database-maintained core-lexical-v1 representative-query ranking projection.';
