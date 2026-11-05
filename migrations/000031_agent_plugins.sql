-- Plugin archives retain bounded inspection documents, not archive bytes.
ALTER TABLE resources DROP CONSTRAINT resources_kind_check;
ALTER TABLE resources ADD CONSTRAINT resources_kind_check CHECK (kind IN ('agent','mcp_server','skill','plugin'));
ALTER TABLE representations ADD COLUMN plugin_inspection jsonb;
ALTER TABLE representations DROP CONSTRAINT representations_delivery_check;
ALTER TABLE representations ADD CONSTRAINT representations_delivery_check CHECK (
  plugin_inspection IS NULL AND media_type NOT IN ('application/agent-plugins+zip','application/agent-plugins+gzip') AND (
    NOT external_reference_only AND (
      external_url IS NULL AND num_nonnulls(inline_document,document_blob_id)=1
      OR external_url IS NOT NULL AND inline_document IS NULL AND document_blob_id IS NOT NULL
    ) OR external_reference_only AND num_nonnulls(external_url,inline_document,document_blob_id)=0
      AND media_type IN ('application/agent-skills+zip','application/agent-skills+gzip')
  ) OR plugin_inspection IS NOT NULL AND NOT external_reference_only
    AND media_type IN ('application/agent-plugins+zip','application/agent-plugins+gzip')
    AND profile_id='agent-plugins-1.0.0-1fc1b627'
    AND external_url IS NOT NULL AND external_url LIKE 'https://%' AND inline_document IS NULL AND document_blob_id IS NULL
    AND size_bytes BETWEEN 1 AND 16777216
);
ALTER TABLE representations ADD CONSTRAINT representations_plugin_inspection_check CHECK (
  plugin_inspection IS NULL OR coalesce(
    jsonb_typeof(plugin_inspection)='object'
    AND octet_length(plugin_inspection::text)<=20971520
    AND jsonb_typeof(plugin_inspection->'rootDirectory')='string'
    AND jsonb_typeof(plugin_inspection->'files')='array'
    AND jsonb_array_length(plugin_inspection->'files') BETWEEN 1 AND 1024
    AND jsonb_typeof(plugin_inspection->'documents')='array'
    AND jsonb_array_length(plugin_inspection->'documents') BETWEEN 1 AND 130
    AND (plugin_inspection->>'expandedSize')::bigint BETWEEN 1 AND 67108864
    AND (plugin_inspection->>'expandedSize')::bigint<=size_bytes*100
    AND plugin_inspection->'assessments'='{"malware":"not_assessed","secrets":"not_assessed","license":"not_assessed","sbom":"not_assessed","provenance":"not_assessed"}'::jsonb,false)
);
CREATE OR REPLACE FUNCTION guard_representation_history() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog
AS $function$
DECLARE parent_is_plugin boolean;
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.plugin_inspection IS NOT NULL THEN
      EXECUTE format('SELECT EXISTS (
        SELECT 1 FROM %I.resource_version_revisions vr
        JOIN %I.resource_versions v ON v.tenant_id=vr.tenant_id AND v.id=vr.resource_version_id
        JOIN %I.resources r ON r.tenant_id=v.tenant_id AND r.id=v.resource_id
        WHERE vr.tenant_id=$1 AND vr.id=$2 AND r.kind=''plugin'')',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
        INTO parent_is_plugin USING NEW.tenant_id,NEW.version_revision_id;
      IF NOT parent_is_plugin THEN RAISE EXCEPTION 'Plugin inspection requires a same-tenant Plugin resource'; END IF;
      RETURN NEW;
    END IF;
    IF NEW.external_reference_only OR NEW.inline_document IS NOT NULL OR NEW.document_blob_id IS NOT NULL THEN RETURN NEW; END IF;
    RAISE EXCEPTION 'new bounded representation documents must be authoritative in PostgreSQL';
  END IF;
  RAISE EXCEPTION 'catalog retained history is immutable';
END
$function$;
COMMENT ON COLUMN representations.plugin_inspection IS 'Immutable bounded Plugin documents and inventory; content_digest and size_bytes identify the externally hosted archive.';
COMMENT ON COLUMN representations.external_url IS 'Anonymous HTTPS delivery bound to retained document bytes, or the Plugin archive digest and immutable inspection.';
