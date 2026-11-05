CREATE TABLE document_blobs (
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  id text NOT NULL,
  media_type text NOT NULL,
  content_digest text NOT NULL,
  size_bytes bigint NOT NULL,
  content bytea NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT document_blobs_id_valid CHECK (id ~ '^[0-9a-f]{64}$'),
  CONSTRAINT document_blobs_media_type_valid CHECK (length(media_type) BETWEEN 1 AND 255 AND media_type = btrim(media_type) AND media_type !~ '[[:cntrl:]]'),
  CONSTRAINT document_blobs_digest_valid CHECK (content_digest = 'sha256:' || id),
  CONSTRAINT document_blobs_size_valid CHECK (size_bytes = octet_length(content) AND size_bytes BETWEEN 1 AND 16777216),
  CONSTRAINT document_blobs_content_valid CHECK (encode(digest(content, 'sha256'), 'hex') = id),
  CONSTRAINT document_blobs_utf8_valid CHECK (convert_from(content, 'UTF8') IS NOT NULL),
  CONSTRAINT document_blobs_reference_identity UNIQUE (tenant_id, id, media_type, content_digest, size_bytes)
);

ALTER TABLE document_blobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_blobs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON document_blobs
  USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE representations ADD COLUMN document_blob_id text;

ALTER TABLE representations
  ADD CONSTRAINT representations_document_compatibility_check
  CHECK (document_blob_id IS NULL OR (artifact_id IS NOT NULL AND document_blob_id = artifact_id)) NOT VALID;
ALTER TABLE representations VALIDATE CONSTRAINT representations_document_compatibility_check;

ALTER TABLE representations
  ADD CONSTRAINT representations_document_metadata_fk
  FOREIGN KEY (tenant_id, document_blob_id, media_type, content_digest, size_bytes)
  REFERENCES document_blobs(tenant_id, id, media_type, content_digest, size_bytes) NOT VALID;
ALTER TABLE representations VALIDATE CONSTRAINT representations_document_metadata_fk;

COMMENT ON TABLE document_blobs IS 'Tenant-scoped, content-addressed exact Catalog documents. The 16 MiB per-row ceiling is also a combined per-revision service budget and may be narrowed by the request limit.';
COMMENT ON COLUMN representations.artifact_id IS 'Legacy rolling-compatibility identifier. New document rows set this to the same value as document_blob_id so old replicas fail closed or use their configured compatibility copy.';
COMMENT ON COLUMN representations.document_blob_id IS 'Authoritative bounded Catalog document retained transactionally with its immutable version revision.';
