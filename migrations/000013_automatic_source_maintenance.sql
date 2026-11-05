ALTER TABLE federation_source_revisions
  ADD COLUMN update_mode text,
  ADD COLUMN registry_list_url text,
  ADD CONSTRAINT federation_source_revisions_update_mode_check CHECK (update_mode IS NULL OR update_mode IN ('manual_review','automatic')) NOT VALID,
  ADD CONSTRAINT federation_source_revisions_registry_list_url_check CHECK (
    registry_list_url IS NULL OR length(registry_list_url) <= 2048 AND registry_list_url = btrim(registry_list_url) AND registry_list_url !~ '[[:cntrl:]]'
  ) NOT VALID;

CREATE INDEX federation_source_revisions_automatic
  ON federation_source_revisions (policy_digest,retrieval_mode,recorded_at,tenant_id,source_id,revision)
  WHERE coalesce(update_mode,configuration->>'updateMode','manual_review')='automatic';

CREATE INDEX source_snapshots_automatic_maintenance
  ON source_snapshots (tenant_id,source_id,source_revision,completed_at DESC)
  WHERE state='complete' AND enumeration_complete;

ALTER TABLE observations
  ADD COLUMN identity_digest text,
  ADD CONSTRAINT observations_semantic_digest_format_check CHECK (
    semantic_digest ~ '^sha256:[0-9a-f]{64}$'
  ) NOT VALID,
  ADD CONSTRAINT observations_identity_digest_check CHECK (
    identity_digest IS NULL OR identity_digest = semantic_digest AND identity_digest ~ '^sha256:[0-9a-f]{64}$'
  ) NOT VALID;

DO $constraints$
DECLARE
  old_constraint text;
  match_count integer;
BEGIN
  SELECT count(*), min(constraint_record.conname)
  INTO match_count, old_constraint
  FROM pg_constraint constraint_record
  WHERE constraint_record.conrelid = 'observation_occurrences'::regclass
    AND constraint_record.contype = 'c'
    AND pg_get_constraintdef(constraint_record.oid) LIKE '%evidence_complete%'
    AND pg_get_constraintdef(constraint_record.oid) LIKE '%^/entries/%';

  IF match_count <> 1 THEN
    RAISE EXCEPTION 'expected one pre-000013 observation entry-location constraint, found %', match_count;
  END IF;

  EXECUTE format('ALTER TABLE observation_occurrences DROP CONSTRAINT %I', old_constraint);
END
$constraints$;

ALTER TABLE observation_occurrences
  ADD COLUMN normalization_input_digest text,
  ADD CONSTRAINT observation_occurrences_normalization_input_digest_check CHECK (
    normalization_input_digest IS NULL OR normalization_input_digest ~ '^sha256:[0-9a-f]{64}$'
  ) NOT VALID,
  ADD CONSTRAINT observation_occurrences_complete_location_check CHECK (
    NOT evidence_complete OR entry_location ~ '^/(entries|items)/(0|[1-9][0-9]*)$'
  ) NOT VALID;

CREATE TABLE source_retrieval_documents (
  tenant_id uuid NOT NULL,
  source_id uuid NOT NULL,
  source_revision bigint NOT NULL CHECK (source_revision > 0),
  representation_digest text NOT NULL CHECK (representation_digest ~ '^sha256:[0-9a-f]{64}$'),
  body bytea NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 67108864),
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,source_id,source_revision,representation_digest),
  FOREIGN KEY (tenant_id,source_id,source_revision) REFERENCES federation_source_revisions(tenant_id,source_id,revision),
  CHECK (representation_digest = 'sha256:' || encode(digest(body,'sha256'),'hex'))
);

CREATE TABLE source_retrieval_states (
  tenant_id uuid NOT NULL,
  source_id uuid NOT NULL,
  source_revision bigint NOT NULL CHECK (source_revision > 0),
  request_key text NOT NULL CHECK (request_key ~ '^sha256:[0-9a-f]{64}$'),
  requested_url text NOT NULL CHECK (length(requested_url) BETWEEN 1 AND 2048 AND requested_url = btrim(requested_url) AND requested_url !~ '[[:cntrl:]]'),
  final_url text NOT NULL CHECK (length(final_url) BETWEEN 1 AND 2048 AND final_url = btrim(final_url) AND final_url !~ '[[:cntrl:]]'),
  request_identity_digest text NOT NULL CHECK (request_identity_digest ~ '^sha256:[0-9a-f]{64}$'),
  profile_id text NOT NULL CHECK (length(profile_id) BETWEEN 1 AND 128 AND profile_id = btrim(profile_id) AND profile_id !~ '[[:cntrl:]]'),
  media_type text NOT NULL CHECK (length(media_type) BETWEEN 1 AND 255 AND media_type = lower(btrim(media_type)) AND media_type !~ '[[:cntrl:]]'),
  policy_digest text NOT NULL CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
  credential_revision text NOT NULL DEFAULT '' CHECK (length(credential_revision) <= 128 AND credential_revision = btrim(credential_revision) AND credential_revision !~ '[[:cntrl:]]'),
  representation_digest text NOT NULL CHECK (representation_digest ~ '^sha256:[0-9a-f]{64}$'),
  etag text NOT NULL DEFAULT '' CHECK (length(etag) <= 512 AND etag !~ '[[:cntrl:]]'),
  last_modified text NOT NULL DEFAULT '' CHECK (length(last_modified) <= 128 AND last_modified !~ '[[:cntrl:]]'),
  vary_names text[] NOT NULL DEFAULT ARRAY[]::text[] CHECK (
    vary_names IN (
      ARRAY[]::text[],
      ARRAY['accept']::text[],
      ARRAY['accept-encoding']::text[],
      ARRAY['accept','accept-encoding']::text[]
    )
  ),
  vary_selection_digest text NOT NULL CHECK (vary_selection_digest ~ '^sha256:[0-9a-f]{64}$'),
  list_terminal boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,source_id,source_revision,request_key),
  FOREIGN KEY (tenant_id,source_id,source_revision,representation_digest)
    REFERENCES source_retrieval_documents(tenant_id,source_id,source_revision,representation_digest),
  CHECK (request_identity_digest = request_key),
  CHECK (etag <> '' OR last_modified <> '')
);

CREATE TABLE source_retrieval_attempts (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  source_id uuid NOT NULL,
  source_revision bigint NOT NULL CHECK (source_revision > 0),
  snapshot_id uuid NOT NULL,
  request_key text NOT NULL CHECK (request_key ~ '^sha256:[0-9a-f]{64}$'),
  requested_url_digest text NOT NULL CHECK (requested_url_digest ~ '^sha256:[0-9a-f]{64}$'),
  final_url_digest text NOT NULL CHECK (final_url_digest ~ '^sha256:[0-9a-f]{64}$'),
  profile_id text NOT NULL CHECK (length(profile_id) BETWEEN 1 AND 128 AND profile_id = btrim(profile_id) AND profile_id !~ '[[:cntrl:]]'),
  status integer NOT NULL CHECK (status BETWEEN 100 AND 599),
  received_body boolean NOT NULL,
  reused boolean NOT NULL,
  representation_digest text CHECK (representation_digest IS NULL OR representation_digest ~ '^sha256:[0-9a-f]{64}$'),
  etag_digest text CHECK (etag_digest IS NULL OR etag_digest ~ '^sha256:[0-9a-f]{64}$'),
  last_modified_digest text CHECK (last_modified_digest IS NULL OR last_modified_digest ~ '^sha256:[0-9a-f]{64}$'),
  fetched_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,id),
  FOREIGN KEY (tenant_id,source_id,snapshot_id,source_revision)
    REFERENCES source_snapshots(tenant_id,source_id,id,source_revision),
  CHECK (NOT reused OR status = 304 AND NOT received_body AND representation_digest IS NOT NULL),
  CHECK (status <> 304 OR NOT received_body),
  CHECK (NOT received_body OR representation_digest IS NOT NULL),
  CHECK (received_body OR reused OR representation_digest IS NULL)
);

CREATE TABLE source_maintenance_state (
  tenant_id uuid NOT NULL,
  source_id uuid NOT NULL,
  source_revision bigint NOT NULL CHECK (source_revision > 0),
  last_admitted_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,source_id,source_revision),
  FOREIGN KEY (tenant_id,source_id,source_revision) REFERENCES federation_source_revisions(tenant_id,source_id,revision)
);

CREATE INDEX source_retrieval_attempts_snapshot
  ON source_retrieval_attempts (tenant_id,snapshot_id,fetched_at,id);

CREATE INDEX jobs_live_automatic_source_sync
  ON jobs (created_at,id)
  WHERE kind='source.sync' AND payload_version=2 AND payload->>'trigger'='automatic' AND state IN ('queued','running','waiting');

DO $policies$
DECLARE
  table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'source_retrieval_documents', 'source_retrieval_states', 'source_retrieval_attempts', 'source_maintenance_state'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid) WITH CHECK (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)',
      table_name
    );
  END LOOP;
END
$policies$;

CREATE TRIGGER source_retrieval_documents_immutable
  BEFORE UPDATE OR DELETE ON source_retrieval_documents
  FOR EACH ROW EXECUTE FUNCTION reject_federation_source_history_mutation();

CREATE TRIGGER source_retrieval_attempts_immutable
  BEFORE UPDATE OR DELETE ON source_retrieval_attempts
  FOR EACH ROW EXECUTE FUNCTION reject_federation_source_history_mutation();

COMMENT ON COLUMN federation_source_revisions.update_mode IS 'Typed projection populated by migration-000013 writers; NULL readers fall back to immutable configuration during the mixed-version interval.';
COMMENT ON COLUMN federation_source_revisions.registry_list_url IS 'Inspected Registry List endpoint populated by migration-000013 writers; NULL readers fall back to immutable configuration during the mixed-version interval.';
COMMENT ON COLUMN observations.semantic_digest IS 'Compatibility storage for observation identity during the mixed-version interval; migration-000013 writers store the context-sensitive identity value here.';
COMMENT ON COLUMN observations.identity_digest IS 'Collision-safe identity populated by migration-000013 writers; NULL readers fall back to semantic_digest for older rows.';
COMMENT ON COLUMN observation_occurrences.normalization_input_digest IS 'Versioned digest of exact remote-context URL and body-digest inputs used by this normalization occurrence.';
COMMENT ON TABLE source_retrieval_documents IS 'Bounded exact decoded source documents retained privately for validated conditional reuse.';
COMMENT ON TABLE source_retrieval_states IS 'Current tenant-private validator state bound to one source revision and safe request identity.';
COMMENT ON TABLE source_retrieval_attempts IS 'Immutable safe per-snapshot source retrieval evidence without raw URLs or validators.';
COMMENT ON TABLE source_maintenance_state IS 'Crash-safe automatic synchronization admission marker for one immutable source revision.';
