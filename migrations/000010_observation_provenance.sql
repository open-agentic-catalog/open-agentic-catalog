ALTER TABLE source_snapshots
  ADD COLUMN source_revision bigint,
  ADD CONSTRAINT source_snapshots_source_revision_check CHECK (source_revision IS NULL OR source_revision > 0),
  ADD CONSTRAINT source_snapshots_source_revision_fk FOREIGN KEY (tenant_id, source_id, source_revision) REFERENCES federation_source_revisions(tenant_id, source_id, revision),
  ADD CONSTRAINT source_snapshots_tenant_source_id_unique UNIQUE (tenant_id, source_id, id),
  ADD CONSTRAINT source_snapshots_tenant_source_id_revision_unique UNIQUE (tenant_id, source_id, id, source_revision);

ALTER TABLE observations
  ADD COLUMN exact_entry_digest text,
  ADD COLUMN normalized_entry_digest text,
  ADD COLUMN normalization_profile text,
  ADD COLUMN normalized_entry_bytes bytea,
  ADD COLUMN raw_entry_bytes bytea;

UPDATE observations
SET exact_entry_digest = semantic_digest,
    freshness = CASE WHEN freshness = 'possibly_withdrawn' THEN 'withdrawn' ELSE freshness END;

WITH legacy_identities AS (
  SELECT tenant_id,id,
         'urn:air:' || lower(rtrim(split_part(identifier,':',3),'.')) || ':' || regexp_replace(identifier,'^[^:]+:[^:]+:[^:]+:','') AS normalized_identifier,
         version_present,coalesce(version,'') AS version
  FROM observations
  WHERE nullif(identifier,'') IS NOT NULL
    AND NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(coalesce(compatibility->'findings','[]'::jsonb)) finding
      WHERE finding->>'code' = 'ARD_IDENTIFIER_COMPATIBILITY'
    )
)
UPDATE observations observation
SET identifier = identity.normalized_identifier,
    source_key = 'identity:v1:sha256:' || encode(digest(
        convert_to(identity.normalized_identifier,'UTF8') || decode('ff','hex') ||
        CASE WHEN identity.version_present THEN decode('01','hex') ELSE decode('00','hex') END ||
        decode('ff','hex') || convert_to(identity.version,'UTF8'),
        'sha256'
      ),'hex')
FROM legacy_identities identity
WHERE observation.tenant_id=identity.tenant_id AND observation.id=identity.id;

ALTER TABLE observations
  ALTER COLUMN exact_entry_digest SET NOT NULL,
  ADD CONSTRAINT observations_exact_entry_digest_check CHECK (exact_entry_digest ~ '^sha256:[0-9a-f]{64}$'),
  ADD CONSTRAINT observations_freshness_check CHECK (freshness IN ('current', 'stale', 'withdrawn', 'source_removed')),
  ADD CONSTRAINT observations_normalized_entry_digest_check CHECK (normalized_entry_digest IS NULL OR normalized_entry_digest ~ '^sha256:[0-9a-f]{64}$'),
  ADD CONSTRAINT observations_normalization_pair_check CHECK ((normalized_entry_digest IS NULL) = (normalization_profile IS NULL)),
  ADD CONSTRAINT observations_normalization_profile_check CHECK (normalization_profile IS NULL OR normalization_profile = 'catalog-observation-v1'),
  ADD CONSTRAINT observations_normalized_entry_bytes_pair_check CHECK ((normalized_entry_digest IS NULL) = (normalized_entry_bytes IS NULL)),
  ADD CONSTRAINT observations_normalized_entry_digest_bytes_check CHECK (normalized_entry_bytes IS NULL OR normalized_entry_digest = 'sha256:' || encode(digest(normalized_entry_bytes,'sha256'),'hex')),
  ADD CONSTRAINT observations_raw_entry_digest_check CHECK (raw_entry_bytes IS NULL OR exact_entry_digest = 'sha256:' || encode(digest(raw_entry_bytes,'sha256'),'hex')),
  ADD CONSTRAINT observations_tenant_id_unique UNIQUE (tenant_id, id),
  ADD CONSTRAINT observations_tenant_source_id_digest_unique UNIQUE (tenant_id, source_id, id, exact_entry_digest),
  ADD CONSTRAINT observations_tenant_id_normalized_digest_unique UNIQUE (tenant_id, id, normalized_entry_digest),
  ADD CONSTRAINT observations_source_snapshot_fk FOREIGN KEY (tenant_id, source_id, snapshot_id) REFERENCES source_snapshots(tenant_id, source_id, id);

CREATE TABLE observation_occurrences (
  tenant_id uuid NOT NULL,
  observation_id uuid NOT NULL,
  source_id uuid NOT NULL,
  snapshot_id uuid NOT NULL,
  source_revision bigint,
  retrieval_digest text NOT NULL CHECK (retrieval_digest ~ '^sha256:[0-9a-f]{64}$'),
  entry_location text NOT NULL CHECK (length(entry_location) BETWEEN 1 AND 1024 AND entry_location = btrim(entry_location) AND entry_location !~ '[[:cntrl:]]'),
  exact_entry_digest text NOT NULL CHECK (exact_entry_digest ~ '^sha256:[0-9a-f]{64}$'),
  normalized_entry_digest text CHECK (normalized_entry_digest IS NULL OR normalized_entry_digest ~ '^sha256:[0-9a-f]{64}$'),
  evidence_complete boolean NOT NULL,
  observed_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, snapshot_id, retrieval_digest, entry_location),
  FOREIGN KEY (tenant_id, source_id, observation_id, exact_entry_digest) REFERENCES observations(tenant_id, source_id, id, exact_entry_digest),
  FOREIGN KEY (tenant_id, observation_id, normalized_entry_digest) REFERENCES observations(tenant_id, id, normalized_entry_digest),
  FOREIGN KEY (tenant_id, source_id, snapshot_id) REFERENCES source_snapshots(tenant_id, source_id, id),
  FOREIGN KEY (tenant_id, source_id, snapshot_id, source_revision) REFERENCES source_snapshots(tenant_id, source_id, id, source_revision),
  FOREIGN KEY (tenant_id, source_id, source_revision) REFERENCES federation_source_revisions(tenant_id, source_id, revision),
  CHECK (source_revision IS NOT NULL OR NOT evidence_complete),
  CHECK (NOT evidence_complete OR entry_location ~ '^/entries/(0|[1-9][0-9]*)$')
);

INSERT INTO observation_occurrences(
  tenant_id,observation_id,source_id,snapshot_id,source_revision,retrieval_digest,entry_location,
  exact_entry_digest,normalized_entry_digest,evidence_complete,observed_at
)
SELECT
  observation.tenant_id,
  observation.id,
  observation.source_id,
  observation.snapshot_id,
  NULL,
  CASE
    WHEN snapshot.body_digest ~ '^sha256:[0-9a-f]{64}$' THEN snapshot.body_digest
    ELSE observation.exact_entry_digest
  END,
  '/legacy/observations/' || observation.id::text,
  observation.exact_entry_digest,
  NULL,
  false,
  observation.last_seen_at
FROM observations observation
JOIN source_snapshots snapshot
  ON snapshot.tenant_id=observation.tenant_id AND snapshot.id=observation.snapshot_id;

CREATE INDEX observation_occurrences_observation_time
  ON observation_occurrences (tenant_id, observation_id, observed_at DESC, snapshot_id DESC);

CREATE INDEX observations_normalized_identity
  ON observations (tenant_id, identifier, version_present, version, source_id, normalized_entry_digest)
  WHERE normalized_entry_digest IS NOT NULL;

ALTER TABLE observation_occurrences ENABLE ROW LEVEL SECURITY;
ALTER TABLE observation_occurrences FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON observation_occurrences
  USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

CREATE TRIGGER observation_occurrences_immutable
  BEFORE UPDATE OR DELETE ON observation_occurrences
  FOR EACH ROW EXECUTE FUNCTION reject_federation_source_history_mutation();

ALTER TABLE resource_version_revisions
  ADD COLUMN adoption_provenance jsonb,
  ADD CONSTRAINT resource_version_revisions_adoption_object_check CHECK (adoption_provenance IS NULL OR jsonb_typeof(adoption_provenance) = 'object');

COMMENT ON TABLE observation_occurrences IS 'Immutable tenant-scoped evidence for each exact observation appearance in a source snapshot.';
COMMENT ON COLUMN observations.raw_entry_bytes IS 'Exact decoded entry bytes from a provenance-aware writer; NULL means no such exact bytes have been observed.';
COMMENT ON COLUMN resource_version_revisions.adoption_provenance IS 'Immutable exact external observation occurrence selected when this local draft was created.';
