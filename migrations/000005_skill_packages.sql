ALTER TABLE representations ADD COLUMN external_reference_only boolean NOT NULL DEFAULT false;
ALTER TABLE representations ADD COLUMN skill_package_structure_state text;
ALTER TABLE representations ADD COLUMN skill_package_root text;
ALTER TABLE representations ADD COLUMN skill_package_expanded_size bigint;
ALTER TABLE representations ADD COLUMN skill_package_file_count integer;
ALTER TABLE representations ADD COLUMN skill_package_malware_state text;
ALTER TABLE representations ADD COLUMN skill_package_secrets_state text;
ALTER TABLE representations ADD COLUMN skill_package_license_state text;
ALTER TABLE representations ADD COLUMN skill_package_sbom_state text;
ALTER TABLE representations ADD COLUMN skill_package_provenance_state text;

ALTER TABLE representations DROP CONSTRAINT representations_check;
ALTER TABLE representations
  ADD CONSTRAINT representations_delivery_check
  CHECK (
    (NOT external_reference_only AND num_nonnulls(external_url, inline_document, artifact_id) = 1) OR
    (external_reference_only AND num_nonnulls(external_url, inline_document, artifact_id) = 0 AND media_type IN ('application/agent-skills+zip', 'application/agent-skills+gzip'))
  ) NOT VALID;
ALTER TABLE representations VALIDATE CONSTRAINT representations_delivery_check;

ALTER TABLE representations
  ADD CONSTRAINT representations_skill_package_summary_check
  CHECK (
    (NOT external_reference_only AND num_nonnulls(skill_package_structure_state, skill_package_root, skill_package_expanded_size, skill_package_file_count) = 0) OR
    (external_reference_only AND num_nonnulls(skill_package_structure_state, skill_package_root, skill_package_expanded_size, skill_package_file_count) = 4 AND size_bytes BETWEEN 1 AND 16777216 AND skill_package_structure_state = 'passed' AND skill_package_root ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(skill_package_root) <= 64 AND skill_package_expanded_size BETWEEN 1 AND 67108864 AND skill_package_expanded_size <= size_bytes * 100 AND skill_package_file_count BETWEEN 1 AND 1024) OR
    (external_reference_only AND num_nonnulls(skill_package_structure_state, skill_package_root, skill_package_expanded_size, skill_package_file_count) = 4 AND size_bytes BETWEEN 1 AND 16777216 AND skill_package_structure_state = 'failed' AND skill_package_root = '' AND skill_package_expanded_size = 0 AND skill_package_file_count = 0)
  ) NOT VALID;
ALTER TABLE representations VALIDATE CONSTRAINT representations_skill_package_summary_check;

ALTER TABLE representations
  ADD CONSTRAINT representations_skill_package_assessments_check
  CHECK (
    (NOT external_reference_only AND num_nonnulls(skill_package_malware_state, skill_package_secrets_state, skill_package_license_state, skill_package_sbom_state, skill_package_provenance_state) = 0) OR
    (external_reference_only AND num_nonnulls(skill_package_malware_state, skill_package_secrets_state, skill_package_license_state, skill_package_sbom_state, skill_package_provenance_state) = 5 AND skill_package_malware_state IN ('not_assessed', 'passed', 'failed', 'unavailable') AND skill_package_secrets_state IN ('not_assessed', 'passed', 'failed', 'unavailable') AND skill_package_license_state IN ('not_assessed', 'passed', 'failed', 'unavailable') AND skill_package_sbom_state IN ('not_assessed', 'passed', 'failed', 'unavailable') AND skill_package_provenance_state IN ('not_assessed', 'passed', 'failed', 'unavailable'))
  ) NOT VALID;
ALTER TABLE representations VALIDATE CONSTRAINT representations_skill_package_assessments_check;

ALTER TABLE representations ADD CONSTRAINT representations_tenant_identity UNIQUE (tenant_id, id);

CREATE TABLE artifact_references (
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  representation_id uuid NOT NULL,
  reference_kind text NOT NULL CHECK (reference_kind IN ('git', 'oci')),
  git_repository_url text,
  git_commit text,
  git_directory text,
  oci_reference text,
  PRIMARY KEY (tenant_id, representation_id, reference_kind),
  CONSTRAINT artifact_references_representation_fk FOREIGN KEY (tenant_id, representation_id) REFERENCES representations(tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT artifact_references_shape_check CHECK (
    (reference_kind = 'git' AND git_repository_url IS NOT NULL AND git_commit IS NOT NULL AND git_directory IS NOT NULL AND oci_reference IS NULL) OR
    (reference_kind = 'oci' AND git_repository_url IS NULL AND git_commit IS NULL AND git_directory IS NULL AND oci_reference IS NOT NULL)
  ),
  CONSTRAINT artifact_references_git_check CHECK (
    reference_kind <> 'git' OR (
      length(git_repository_url) BETWEEN 9 AND 2048 AND git_repository_url LIKE 'https://%' AND git_repository_url !~ '[?#[:cntrl:]]' AND
      git_commit ~ '^([a-f0-9]{40}|[a-f0-9]{64})$' AND
      length(git_directory) BETWEEN 1 AND 1024 AND left(git_directory, 1) <> '/' AND position(E'\\' in git_directory) = 0 AND git_directory !~ '(^|/)\.\.(/|$)' AND git_directory !~ '[[:cntrl:]]'
    )
  ),
  CONSTRAINT artifact_references_oci_check CHECK (
    reference_kind <> 'oci' OR (
      length(oci_reference) BETWEEN 73 AND 2048 AND oci_reference ~ '^[^/@[:space:]]+/[^@[:space:]]+@sha256:[a-f0-9]{64}$' AND oci_reference !~ '[?#[:cntrl:]]' AND
      right(split_part(oci_reference, '/', 1), 1) <> ':' AND
      (split_part(oci_reference, '/', 1) = 'localhost' OR split_part(oci_reference, '/', 1) ~ '[.:]')
    )
  )
);

CREATE TABLE skill_file_inventory (
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  representation_id uuid NOT NULL,
  path text NOT NULL,
  size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 0 AND 16777216),
  media_type text NOT NULL,
  content_digest text NOT NULL CHECK (content_digest ~ '^sha256:[a-f0-9]{64}$'),
  executable boolean NOT NULL,
  PRIMARY KEY (tenant_id, representation_id, path),
  CONSTRAINT skill_file_inventory_representation_fk FOREIGN KEY (tenant_id, representation_id) REFERENCES representations(tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT skill_file_inventory_path_check CHECK (length(path) BETWEEN 1 AND 1024 AND left(path, 1) <> '/' AND position(E'\\' in path) = 0 AND path !~ '(^|/)\.\.(/|$)' AND path !~ '[[:cntrl:]]'),
  CONSTRAINT skill_file_inventory_media_type_check CHECK (length(media_type) BETWEEN 1 AND 255 AND media_type = btrim(media_type) AND media_type !~ '[[:cntrl:]]')
);

ALTER TABLE artifact_references ENABLE ROW LEVEL SECURITY;
ALTER TABLE artifact_references FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON artifact_references
  USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE skill_file_inventory ENABLE ROW LEVEL SECURITY;
ALTER TABLE skill_file_inventory FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON skill_file_inventory
  USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

COMMENT ON COLUMN representations.external_reference_only IS 'True only for an inspected Agent Skill package whose bytes were transient validation input and whose durable delivery is an immutable Git or OCI handoff.';
COMMENT ON TABLE artifact_references IS 'Tenant-scoped immutable Git and OCI handoff references. The Catalog retains locators and never clones, pulls, installs, or executes their content.';
COMMENT ON TABLE skill_file_inventory IS 'Complete normalized regular-file inventory derived by bounded, non-executing Agent Skill package inspection.';
