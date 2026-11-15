-- Materialize only declared A2A bindings, without changing retained documents.
-- Drain runtimes: PostgreSQL computes the stored column for existing blobs.
CREATE FUNCTION catalog_agent_protocol_bindings_v1(media text, content bytea, inline_document jsonb) RETURNS text[]
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE SET search_path = pg_catalog AS $function$
DECLARE
  document json;
  item json;
  interfaces json;
  binding_document json;
  binding text;
  bindings text[] := ARRAY[]::text[];
BEGIN
  IF lower(btrim(split_part(media,';',1))) <> 'application/a2a-agent-card+json' THEN
    RETURN bindings;
  END IF;
  BEGIN
    -- PostgreSQL's JSON selectors decode every string, including unrelated
    -- escaped NUL that the native card can legitimately retain. Normalize
    -- escaped backslashes first, then mark NUL as a literal escape. The raw
    -- binding token below distinguishes that marker from a declared literal
    -- backslash followed by u0000. Authoritative bytes remain unchanged.
    document := replace(replace(coalesce(inline_document::text,convert_from(content,'UTF8')), E'\\\\', E'\\u005c'), E'\\u0000', E'\\\\u0000')::json;
  EXCEPTION WHEN invalid_text_representation OR untranslatable_character OR character_not_in_repertoire THEN
    RETURN bindings;
  END;
  interfaces := coalesce(document->'supportedInterfaces',document->'supported_interfaces');
  IF json_typeof(interfaces) IS DISTINCT FROM 'array' THEN
    RETURN bindings;
  END IF;
  FOR item IN SELECT value FROM json_array_elements(interfaces) LOOP
    binding_document := coalesce(item->'protocolBinding',item->'protocol_binding');
    IF json_typeof(binding_document) IS DISTINCT FROM 'string' THEN CONTINUE; END IF;
    IF strpos(binding_document::text, E'\\\\u0000') > 0 THEN CONTINUE; END IF;
    binding := binding_document #>> '{}';
    IF binding <> '' THEN bindings := array_append(bindings,binding); END IF;
  END LOOP;
  RETURN ARRAY(SELECT DISTINCT value COLLATE "C" FROM unnest(bindings) value ORDER BY value COLLATE "C");
EXCEPTION WHEN data_exception OR program_limit_exceeded THEN
  RETURN ARRAY[]::text[];
END
$function$;
REVOKE ALL ON FUNCTION catalog_agent_protocol_bindings_v1(text,bytea,jsonb) FROM PUBLIC;

ALTER TABLE document_blobs ADD COLUMN agent_protocol_bindings text[]
  GENERATED ALWAYS AS (catalog_agent_protocol_bindings_v1(media_type,content,NULL)) STORED NOT NULL;
COMMENT ON COLUMN document_blobs.agent_protocol_bindings IS 'Rebuildable exact A2A interface bindings. Only valid released primary Agent representations with current content authority may contribute to discovery; bytes and digests remain authoritative.';

-- Native JSON supplied inline remains in representations; URL/content inputs
-- retain exact bytes in document_blobs. Materialize both existing forms.
ALTER TABLE representations ADD COLUMN agent_protocol_bindings text[]
  GENERATED ALWAYS AS (catalog_agent_protocol_bindings_v1(media_type,NULL,inline_document)) STORED NOT NULL;
COMMENT ON COLUMN representations.agent_protocol_bindings IS 'Rebuildable bindings from inline A2A JSON; select only released valid primary Agent evidence under current content authority.';
