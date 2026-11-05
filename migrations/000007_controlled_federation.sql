CREATE TABLE federation_source_revisions (
  tenant_id uuid NOT NULL,
  source_id uuid NOT NULL,
  revision bigint NOT NULL CHECK (revision > 0),
  retrieval_mode text NOT NULL CHECK (retrieval_mode IN ('indexed', 'live', 'hybrid')),
  policy_id text NOT NULL DEFAULT '',
  policy_revision bigint NOT NULL DEFAULT 0 CHECK (policy_revision >= 0),
  policy_digest text NOT NULL DEFAULT '' CHECK (policy_digest = '' OR policy_digest ~ '^sha256:[0-9a-f]{64}$'),
  credential_revision text NOT NULL DEFAULT '' CHECK (length(credential_revision) <= 128 AND credential_revision = btrim(credential_revision) AND credential_revision !~ '[[:cntrl:]]'),
  configuration_digest text NOT NULL CHECK (configuration_digest ~ '^sha256:[0-9a-f]{64}$'),
  configuration jsonb NOT NULL CHECK (jsonb_typeof(configuration) = 'object'),
  recorded_at timestamptz NOT NULL,
  CHECK (
    (policy_id = '' AND policy_revision = 0 AND policy_digest = '')
    OR (length(policy_id) <= 63 AND policy_id ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND policy_revision > 0 AND policy_digest ~ '^sha256:[0-9a-f]{64}$')
  ),
  PRIMARY KEY (tenant_id, source_id, revision),
  FOREIGN KEY (tenant_id, source_id) REFERENCES federation_sources(tenant_id, id)
);

CREATE TABLE federation_source_audiences (
  tenant_id uuid NOT NULL,
  source_id uuid NOT NULL,
  source_revision bigint NOT NULL,
  discovery_host text NOT NULL CHECK (discovery_host = lower(btrim(discovery_host)) AND length(discovery_host) BETWEEN 1 AND 253),
  audience text NOT NULL CHECK (audience IN ('anonymous', 'authenticated')),
  PRIMARY KEY (tenant_id, source_id, source_revision, discovery_host, audience),
  FOREIGN KEY (tenant_id, source_id, source_revision) REFERENCES federation_source_revisions(tenant_id, source_id, revision)
);

CREATE TABLE federation_source_referrals (
  tenant_id uuid NOT NULL,
  source_id uuid NOT NULL,
  source_revision bigint NOT NULL,
  target_source_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, source_id, source_revision, target_source_id),
  FOREIGN KEY (tenant_id, source_id, source_revision) REFERENCES federation_source_revisions(tenant_id, source_id, revision),
  FOREIGN KEY (tenant_id, target_source_id) REFERENCES federation_sources(tenant_id, id),
  CHECK (source_id <> target_source_id)
);

CREATE INDEX federation_source_audiences_lookup
  ON federation_source_audiences (tenant_id, discovery_host, audience, source_id, source_revision);

CREATE INDEX federation_source_referrals_target
  ON federation_source_referrals (tenant_id, target_source_id, source_id, source_revision);

WITH source_state AS (
  SELECT
    source.*,
    length(coalesce(configuration->>'policyId','')) BETWEEN 1 AND 63
      AND coalesce(configuration->>'policyId','') ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
      AND coalesce(configuration->>'policyRevision','') ~ '^[1-9][0-9]{0,17}$'
      AND coalesce(configuration->>'policyDigest','') ~ '^sha256:[0-9a-f]{64}$' AS has_complete_policy_evidence
  FROM federation_sources source
)
INSERT INTO federation_source_revisions(
  tenant_id,source_id,revision,retrieval_mode,policy_id,policy_revision,policy_digest,credential_revision,
  configuration_digest,configuration,recorded_at
)
SELECT
  tenant_id,
  id,
  revision,
  CASE
    WHEN coalesce(configuration->>'retrievalMode','') IN ('indexed','live','hybrid') THEN configuration->>'retrievalMode'
    WHEN source_type = 'ard_registry' THEN 'live'
    WHEN configuration#>>'{live,enabled}' = 'true' THEN 'hybrid'
    ELSE 'indexed'
  END,
  CASE WHEN has_complete_policy_evidence THEN configuration->>'policyId' ELSE '' END,
  CASE WHEN has_complete_policy_evidence THEN (configuration->>'policyRevision')::bigint ELSE 0 END,
  CASE WHEN has_complete_policy_evidence THEN configuration->>'policyDigest' ELSE '' END,
  CASE
    WHEN length(coalesce(configuration->>'credentialRevision','')) <= 128
      AND coalesce(configuration->>'credentialRevision','') = btrim(coalesce(configuration->>'credentialRevision',''))
      AND coalesce(configuration->>'credentialRevision','') !~ '[[:cntrl:]]'
    THEN coalesce(configuration->>'credentialRevision','')
    ELSE ''
  END,
  'sha256:' || encode(digest(configuration::text,'sha256'),'hex'),
  configuration,
  updated_at
FROM source_state;

INSERT INTO federation_source_audiences(tenant_id,source_id,source_revision,discovery_host,audience)
SELECT source.tenant_id,source.id,source.revision,assignment->>'host',assignment->>'audience'
FROM federation_sources source
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(source.configuration->'discoveryAssignments') = 'array'
    THEN source.configuration->'discoveryAssignments' ELSE '[]'::jsonb END
) assignment
WHERE jsonb_typeof(assignment) = 'object'
  AND assignment->>'host' = lower(btrim(assignment->>'host'))
  AND length(assignment->>'host') BETWEEN 1 AND 253
  AND assignment->>'audience' IN ('anonymous','authenticated')
ON CONFLICT DO NOTHING;

INSERT INTO federation_source_referrals(tenant_id,source_id,source_revision,target_source_id)
SELECT source.tenant_id,source.id,source.revision,referred.id
FROM federation_sources source
CROSS JOIN LATERAL jsonb_array_elements_text(
  CASE WHEN jsonb_typeof(source.configuration#>'{referrals,approvedSourceIds}') = 'array'
    THEN source.configuration#>'{referrals,approvedSourceIds}' ELSE '[]'::jsonb END
) target(value)
JOIN federation_sources referred ON referred.tenant_id=source.tenant_id AND referred.id=CASE
  WHEN target.value ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN target.value::uuid
END
WHERE source.id <> referred.id
ON CONFLICT DO NOTHING;

DO $policies$
DECLARE
  table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['federation_source_revisions', 'federation_source_audiences', 'federation_source_referrals'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid) WITH CHECK (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)',
      table_name
    );
  END LOOP;
END
$policies$;

CREATE FUNCTION reject_federation_source_history_mutation() RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION 'federation source history is immutable';
END
$function$;

CREATE TRIGGER federation_source_revisions_immutable
  BEFORE UPDATE OR DELETE ON federation_source_revisions
  FOR EACH ROW EXECUTE FUNCTION reject_federation_source_history_mutation();

CREATE TRIGGER federation_source_audiences_immutable
  BEFORE UPDATE OR DELETE ON federation_source_audiences
  FOR EACH ROW EXECUTE FUNCTION reject_federation_source_history_mutation();

CREATE TRIGGER federation_source_referrals_immutable
  BEFORE UPDATE OR DELETE ON federation_source_referrals
  FOR EACH ROW EXECUTE FUNCTION reject_federation_source_history_mutation();

COMMENT ON TABLE federation_source_revisions IS 'Immutable administrator-owned source configuration revisions and exact tested policy evidence.';
COMMENT ON TABLE federation_source_audiences IS 'Exact discovery-host and audience assignments attached to one immutable source revision.';
COMMENT ON TABLE federation_source_referrals IS 'Administrator-approved source-to-source referral edges attached to one immutable source revision.';
