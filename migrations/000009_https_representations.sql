ALTER TABLE evidence_sets
  ADD COLUMN external_retrievals jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE evidence_sets
  ADD CONSTRAINT evidence_sets_external_retrievals_check
  CHECK (
    jsonb_typeof(external_retrievals) = 'array' AND
    jsonb_array_length(external_retrievals) <= 16 AND
    octet_length(external_retrievals::text) <= 131072
  ) NOT VALID;
ALTER TABLE evidence_sets VALIDATE CONSTRAINT evidence_sets_external_retrievals_check;

ALTER TABLE representations DROP CONSTRAINT representations_delivery_check;
ALTER TABLE representations
  ADD CONSTRAINT representations_delivery_check
  CHECK (
    (
      NOT external_reference_only AND
      (
        (external_url IS NULL AND num_nonnulls(inline_document, artifact_id) = 1) OR
        (
          external_url IS NOT NULL AND
          inline_document IS NULL AND
          (
            (artifact_id IS NULL AND document_blob_id IS NULL) OR
            (artifact_id IS NOT NULL AND document_blob_id IS NOT NULL AND document_blob_id = artifact_id)
          )
        )
      )
    ) OR
    (
      external_reference_only AND
      num_nonnulls(external_url, inline_document, artifact_id) = 0 AND
      media_type IN ('application/agent-skills+zip', 'application/agent-skills+gzip')
    )
  ) NOT VALID;
ALTER TABLE representations VALIDATE CONSTRAINT representations_delivery_check;

COMMENT ON COLUMN evidence_sets.external_retrievals IS 'Bounded immutable anonymous HTTPS retrieval evidence for the exact decoded bytes and deployment-owned outbound policy used by this validation run.';
COMMENT ON COLUMN representations.external_url IS 'Credential-free HTTPS delivery URL. New profile-v6 rows also bind exact validation bytes through document_blob_id; legacy URL-only rows remain blocked until replaced by a new VersionRevision.';
