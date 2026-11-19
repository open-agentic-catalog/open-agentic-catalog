-- Rebuildable private card facts. Drain runtimes before the stored-column rewrite.
-- No authoritative native bytes, digests, revisions or publication are changed.
CREATE FUNCTION catalog_native_capabilities_v1(media text, content bytea, inline_document jsonb) RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE SET search_path=pg_catalog AS $function$
DECLARE
  base text := lower(btrim(split_part(media,';',1)));
  document json;
  source_text text;
  members json;
  member json;
  value json;
  parts json[];
  part json;
  field text;
  fields text[];
  declared_values text[];
  literal text;
  result jsonb := '{}'::jsonb;
  total_bytes integer := 0;
  count_values integer;
  unavailable constant jsonb := '{"unavailable":true}'::jsonb;
BEGIN
  IF base NOT IN ('application/a2a-agent-card+json','application/mcp-server-card+json') THEN RETURN result; END IF;
  source_text := coalesce(inline_document::text,convert_from(content,'UTF8'));
  IF source_text IS NULL OR octet_length(source_text)>16777216 THEN RETURN unavailable; END IF;
  -- As in the retained Agent protocol projection, protect unrelated escaped NUL
  -- without converting opaque extension numbers or the complete card to jsonb.
  document := replace(replace(source_text,E'\\\\',E'\\u005c'),E'\\u0000',E'\\\\u0000')::json;
  IF json_typeof(document) IS DISTINCT FROM 'object' THEN RETURN unavailable; END IF;
  IF base='application/a2a-agent-card+json' THEN
    fields := ARRAY['agentSkillIds','agentSkillTags','agentDefaultInputModes','agentDefaultOutputModes'];
  ELSE
    fields := ARRAY['mcpTransports','mcpProtocolVersions'];
  END IF;
  FOREACH field IN ARRAY fields LOOP
    parts := ARRAY[]::json[];
    IF field IN ('agentSkillIds','agentSkillTags','mcpTransports','mcpProtocolVersions') THEN
      members := CASE WHEN base='application/a2a-agent-card+json' THEN document->'skills' ELSE document->'remotes' END;
      IF members IS NOT NULL AND json_typeof(members)<>'null' THEN
        IF json_typeof(members) IS DISTINCT FROM 'array' THEN RETURN unavailable; END IF;
        IF json_array_length(members)>256 THEN RETURN unavailable; END IF;
        FOR member IN SELECT item FROM json_array_elements(members) item LOOP
          IF json_typeof(member) IS DISTINCT FROM 'object' THEN RETURN unavailable; END IF;
          value := CASE field WHEN 'agentSkillIds' THEN member->'id' WHEN 'agentSkillTags' THEN member->'tags'
                    WHEN 'mcpTransports' THEN member->'type' ELSE member->'supportedProtocolVersions' END;
          IF value IS NULL OR json_typeof(value)='null' THEN CONTINUE; END IF;
          IF field IN ('agentSkillIds','mcpTransports') THEN value := json_build_array(value); END IF;
          parts := array_append(parts,value);
        END LOOP;
      END IF;
    ELSE
      value := CASE field WHEN 'agentDefaultInputModes' THEN coalesce(document->'defaultInputModes',document->'default_input_modes')
               ELSE coalesce(document->'defaultOutputModes',document->'default_output_modes') END;
      IF value IS NOT NULL AND json_typeof(value)<>'null' THEN parts := ARRAY[value]; END IF;
    END IF;
    declared_values := ARRAY[]::text[];
    count_values := 0;
    FOREACH part IN ARRAY parts LOOP
      IF json_typeof(part) IS DISTINCT FROM 'array' THEN RETURN unavailable; END IF;
      IF json_array_length(part)>256 THEN RETURN unavailable; END IF;
      FOR value IN SELECT item FROM json_array_elements(part) item LOOP
        count_values := count_values+1;
        IF count_values>256 OR json_typeof(value) IS DISTINCT FROM 'string' THEN RETURN unavailable; END IF;
        -- The marker identifies an actual escaped NUL, not a literal \u0000.
        IF strpos(value::text,E'\\\\u0000')>0 THEN RETURN unavailable; END IF;
        literal := value #>> '{}';
        total_bytes := total_bytes+octet_length(literal);
        IF length(literal)>512 OR total_bytes>32768 THEN RETURN unavailable; END IF;
        declared_values := array_append(declared_values,literal);
      END LOOP;
    END LOOP;
    IF cardinality(declared_values)>0 THEN
      result := result || jsonb_build_object(field,to_jsonb(ARRAY(SELECT DISTINCT item COLLATE "C" FROM unnest(declared_values) item ORDER BY item COLLATE "C")));
    END IF;
  END LOOP;
  IF base='application/a2a-agent-card+json' THEN
    members := document->'capabilities';
    IF members IS NOT NULL AND json_typeof(members)<>'null' THEN
      IF json_typeof(members) IS DISTINCT FROM 'object' THEN RETURN unavailable; END IF;
      value := members->'streaming';
      IF value IS NOT NULL AND json_typeof(value)<>'null' THEN
        IF json_typeof(value) IS DISTINCT FROM 'boolean' THEN RETURN unavailable; END IF;
        result := result || jsonb_build_object('agentStreaming',value::text::jsonb);
      END IF;
    END IF;
  END IF;
  RETURN result;
EXCEPTION WHEN data_exception OR program_limit_exceeded THEN RETURN unavailable;
END
$function$;
REVOKE ALL ON FUNCTION catalog_native_capabilities_v1(text,bytea,jsonb) FROM PUBLIC;

ALTER TABLE document_blobs ADD COLUMN native_capabilities jsonb
  GENERATED ALWAYS AS (catalog_native_capabilities_v1(media_type,content,NULL)) STORED NOT NULL;
ALTER TABLE representations ADD COLUMN native_capabilities jsonb
  GENERATED ALWAYS AS (catalog_native_capabilities_v1(media_type,NULL,inline_document)) STORED NOT NULL;
COMMENT ON COLUMN document_blobs.native_capabilities IS 'Bounded private native card declarations. Source bytes remain authoritative; generated on insertion, drained migration and logical restore. Query only through released validated primary evidence under current content authority.';
COMMENT ON COLUMN representations.native_capabilities IS 'Bounded private inline card declarations; unavailable evidence never supplies partial matches. No discovery, taxonomy, invocation or trust authority.';
