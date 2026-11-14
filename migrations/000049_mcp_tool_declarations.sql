-- Private declarations remain authoritative immutable representation content.
-- Drain runtimes before adding the stored derived columns/table rewrites.
CREATE FUNCTION catalog_mcp_tools_v1(media text, content bytea, inline_document jsonb) RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE SET search_path=pg_catalog AS $function$
DECLARE document jsonb;
BEGIN
  IF lower(btrim(split_part(media,';',1))) <> 'application/vnd.open-agentic-catalog.mcp-tools+json' THEN RETURN '[]'::jsonb; END IF;
  IF inline_document IS NULL AND octet_length(content) > 65536 THEN RETURN '[]'::jsonb; END IF;
  document := coalesce(inline_document,convert_from(content,'UTF8')::jsonb);
  IF jsonb_typeof(document->'tools') IS DISTINCT FROM 'array' THEN RETURN '[]'::jsonb; END IF;
  IF jsonb_array_length(document->'tools') > 64 THEN RETURN '[]'::jsonb; END IF;
  RETURN document->'tools';
EXCEPTION WHEN data_exception OR program_limit_exceeded THEN RETURN '[]'::jsonb;
END
$function$;
REVOKE ALL ON FUNCTION catalog_mcp_tools_v1(text,bytea,jsonb) FROM PUBLIC;

DO $definition$
BEGIN
 EXECUTE format($ddl$
 CREATE FUNCTION catalog_mcp_tools_vector_v1(tools jsonb) RETURNS tsvector
 LANGUAGE sql IMMUTABLE PARALLEL SAFE SET search_path=pg_catalog AS $function$
 SELECT %I.catalog_core_lexical_array_vector_v1(ARRAY(SELECT coalesce(tool->>'name','') || ' ' || coalesce(tool->>'description','') FROM jsonb_array_elements(tools) tool))
 $function$
 $ddl$,current_schema());
END
$definition$;
REVOKE ALL ON FUNCTION catalog_mcp_tools_vector_v1(jsonb) FROM PUBLIC;

ALTER TABLE representations ADD COLUMN mcp_tools jsonb GENERATED ALWAYS AS (catalog_mcp_tools_v1(media_type,NULL,inline_document)) STORED NOT NULL;
ALTER TABLE document_blobs ADD COLUMN mcp_tools jsonb GENERATED ALWAYS AS (catalog_mcp_tools_v1(media_type,content,NULL)) STORED NOT NULL;
COMMENT ON COLUMN representations.mcp_tools IS 'Private derived MCP declarations; only released validated metadata and current content authority may contribute to explicit local lexical search.';
COMMENT ON COLUMN document_blobs.mcp_tools IS 'Private derived MCP declarations; source bytes/digests remain authoritative. Generated on insertion and logical restore.';
CREATE INDEX representations_mcp_tools_search_idx ON representations USING gin(catalog_mcp_tools_vector_v1(mcp_tools));
CREATE INDEX document_blobs_mcp_tools_search_idx ON document_blobs USING gin(catalog_mcp_tools_vector_v1(mcp_tools));
CREATE UNIQUE INDEX representations_one_mcp_tools_idx ON representations(tenant_id,version_revision_id)
 WHERE profile_id='open-agentic-catalog-mcp-tools-1' AND role='metadata' AND validation_state='valid';
