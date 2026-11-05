CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS catalog_schema_migrations (
  version bigint PRIMARY KEY,
  checksum text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE tenants (
  id uuid PRIMARY KEY,
  display_name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE namespaces (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  slug text NOT NULL,
  display_name text NOT NULL,
  description text NOT NULL DEFAULT '',
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, slug)
);

CREATE TABLE publishers (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  display_name text NOT NULL,
  domains text[] NOT NULL DEFAULT '{}',
  verified_domains text[] NOT NULL DEFAULT '{}',
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE resources (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  namespace_id uuid NOT NULL REFERENCES namespaces(id),
  canonical_identifier text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('agent', 'mcp_server', 'skill')),
  created_by text NOT NULL,
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, canonical_identifier)
);

CREATE TABLE resource_versions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  resource_id uuid NOT NULL REFERENCES resources(id),
  version text NOT NULL CHECK (length(version) > 0),
  candidate_revision_id uuid,
  sealed_revision_id uuid,
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, resource_id, version)
);

CREATE TABLE evidence_sets (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  subject_digest text NOT NULL,
  digest text NOT NULL,
  state text NOT NULL CHECK (state IN ('complete', 'blocked')),
  findings jsonb NOT NULL DEFAULT '[]',
  validation_profile text NOT NULL,
  policy_version text NOT NULL,
  rights jsonb NOT NULL,
  trust jsonb NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  UNIQUE (tenant_id, digest)
);

CREATE TABLE resource_version_revisions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  resource_version_id uuid NOT NULL REFERENCES resource_versions(id),
  revision_number integer NOT NULL CHECK (revision_number > 0),
  content_set_digest text NOT NULL,
  findings jsonb NOT NULL DEFAULT '[]',
  evidence_set_id uuid NOT NULL REFERENCES evidence_sets(id),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  UNIQUE (tenant_id, resource_version_id, revision_number),
  UNIQUE (tenant_id, resource_version_id, content_set_digest)
);

ALTER TABLE resource_versions ADD CONSTRAINT resource_versions_candidate_fk FOREIGN KEY (candidate_revision_id) REFERENCES resource_version_revisions(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE resource_versions ADD CONSTRAINT resource_versions_sealed_fk FOREIGN KEY (sealed_revision_id) REFERENCES resource_version_revisions(id) DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE representations (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  version_revision_id uuid NOT NULL REFERENCES resource_version_revisions(id) ON DELETE CASCADE,
  role text NOT NULL,
  media_type text NOT NULL,
  profile_id text NOT NULL,
  external_url text,
  inline_document jsonb,
  content_digest text NOT NULL,
  size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
  artifact_id text,
  validation_state text NOT NULL,
  findings jsonb NOT NULL DEFAULT '[]',
  normalized_facts jsonb NOT NULL DEFAULT '{}',
  unknown_properties jsonb NOT NULL DEFAULT '{}',
  CHECK (num_nonnulls(external_url, inline_document, artifact_id) = 1),
  UNIQUE (tenant_id, version_revision_id, media_type, profile_id, content_digest)
);

CREATE TABLE catalog_listings (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  namespace_id uuid NOT NULL REFERENCES namespaces(id),
  resource_id uuid NOT NULL REFERENCES resources(id),
  candidate_revision_id uuid,
  current_release_id uuid,
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, namespace_id, resource_id)
);

CREATE TABLE listing_revisions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  listing_id uuid NOT NULL REFERENCES catalog_listings(id),
  revision_number integer NOT NULL CHECK (revision_number > 0),
  version_revision_id uuid NOT NULL REFERENCES resource_version_revisions(id),
  display_name text NOT NULL,
  description text NOT NULL,
  tags text[] NOT NULL DEFAULT '{}',
  categories text[] NOT NULL DEFAULT '{}',
  representative_queries text[] NOT NULL DEFAULT '{}',
  publisher_id uuid NOT NULL REFERENCES publishers(id),
  documentation_url text,
  support_url text,
  license text NOT NULL DEFAULT '',
  terms_url text,
  visibility text NOT NULL CHECK (visibility IN ('private', 'tenant', 'public')),
  relationships jsonb NOT NULL DEFAULT '[]',
  extensions jsonb NOT NULL DEFAULT '{}',
  digest text NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  UNIQUE (tenant_id, listing_id, revision_number),
  UNIQUE (tenant_id, listing_id, digest)
);

ALTER TABLE catalog_listings ADD CONSTRAINT catalog_listings_candidate_fk FOREIGN KEY (candidate_revision_id) REFERENCES listing_revisions(id) DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE review_submissions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  listing_id uuid NOT NULL REFERENCES catalog_listings(id),
  version_revision_id uuid NOT NULL REFERENCES resource_version_revisions(id),
  listing_revision_id uuid NOT NULL REFERENCES listing_revisions(id),
  evidence_set_id uuid NOT NULL REFERENCES evidence_sets(id),
  evidence_digest text NOT NULL,
  policy_version text NOT NULL,
  state text NOT NULL CHECK (state IN ('submitted', 'approved', 'changes_required', 'rejected')),
  submitted_by text NOT NULL,
  decisions jsonb NOT NULL DEFAULT '[]',
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  submitted_at timestamptz NOT NULL,
  decided_at timestamptz
);

CREATE UNIQUE INDEX one_active_submission_per_listing ON review_submissions (tenant_id, listing_id) WHERE state = 'submitted';

CREATE TABLE listing_releases (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  listing_id uuid NOT NULL REFERENCES catalog_listings(id),
  submission_id uuid NOT NULL REFERENCES review_submissions(id),
  version_revision_id uuid NOT NULL REFERENCES resource_version_revisions(id),
  listing_revision_id uuid NOT NULL REFERENCES listing_revisions(id),
  state text NOT NULL CHECK (state IN ('published', 'deprecated', 'retired', 'superseded', 'revoked')),
  replacement_release_id uuid REFERENCES listing_releases(id),
  disposition_reason text NOT NULL DEFAULT '',
  sunset_at timestamptz,
  activated_by text NOT NULL,
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  activated_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

ALTER TABLE catalog_listings ADD CONSTRAINT catalog_listings_release_fk FOREIGN KEY (current_release_id) REFERENCES listing_releases(id) DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE audit_events (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  actor text NOT NULL,
  action text NOT NULL,
  target_type text NOT NULL,
  target_id text NOT NULL,
  reason text NOT NULL DEFAULT '',
  request_id text NOT NULL DEFAULT '',
  before_digest text NOT NULL DEFAULT '',
  after_digest text NOT NULL DEFAULT '',
  details jsonb NOT NULL DEFAULT '{}',
  occurred_at timestamptz NOT NULL
);

CREATE TABLE outbox_events (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  event_type text NOT NULL,
  subject text NOT NULL,
  data jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  published_at timestamptz,
  lease_owner text,
  lease_expires_at timestamptz,
  CHECK ((lease_owner IS NULL) = (lease_expires_at IS NULL))
);

CREATE INDEX outbox_unpublished ON outbox_events (coalesce(lease_expires_at, '-infinity'::timestamptz), created_at, id) WHERE published_at IS NULL;

CREATE TABLE webhook_endpoints (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  name text NOT NULL,
  url text NOT NULL,
  event_types text[] NOT NULL,
  status text NOT NULL CHECK (status IN ('active', 'disabled')),
  revision bigint NOT NULL DEFAULT 1,
  secret_version integer NOT NULL DEFAULT 1 CHECK (secret_version > 0),
  encrypted_secret bytea NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX webhook_endpoints_active_url ON webhook_endpoints (tenant_id, url) WHERE status = 'active';

CREATE TABLE webhook_deliveries (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  endpoint_id uuid NOT NULL REFERENCES webhook_endpoints(id),
  event_id uuid NOT NULL,
  event_type text NOT NULL,
  state text NOT NULL CHECK (state IN ('pending', 'succeeded', 'waiting', 'dead_letter')),
  attempt integer NOT NULL DEFAULT 0,
  response_status integer,
  safe_error text NOT NULL DEFAULT '',
  next_attempt_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, endpoint_id, event_id)
);

CREATE INDEX webhook_deliveries_retry ON webhook_deliveries (next_attempt_at, updated_at) WHERE state = 'waiting';

CREATE TABLE idempotency_keys (
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  actor text NOT NULL,
  route text NOT NULL,
  idempotency_key text NOT NULL,
  request_digest text NOT NULL,
  response_status integer,
  response_headers jsonb,
  response_body bytea,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, actor, route, idempotency_key)
);

CREATE TABLE jobs (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  kind text NOT NULL,
  actor text NOT NULL,
  parent_job_id uuid REFERENCES jobs(id),
  payload_version integer NOT NULL DEFAULT 1,
  payload jsonb NOT NULL DEFAULT '{}',
  state text NOT NULL CHECK (state IN ('queued', 'running', 'waiting', 'succeeded', 'failed', 'cancelled', 'dead_letter')),
  priority integer NOT NULL DEFAULT 0,
  progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  not_before timestamptz NOT NULL,
  attempt integer NOT NULL DEFAULT 0,
  attempt_limit integer NOT NULL DEFAULT 5,
  lease_owner text,
  lease_expires_at timestamptz,
  cancellation_requested boolean NOT NULL DEFAULT false,
  deduplication_key text NOT NULL,
  result jsonb,
  safe_error text NOT NULL DEFAULT '',
  failure_class text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX jobs_live_deduplication ON jobs (tenant_id, kind, deduplication_key) WHERE state IN ('queued', 'running', 'waiting');
CREATE INDEX jobs_claim ON jobs (priority DESC, not_before, created_at, id) WHERE state IN ('queued', 'waiting');

CREATE TABLE federation_sources (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  display_name text NOT NULL,
  source_type text NOT NULL,
  endpoint text NOT NULL,
  status text NOT NULL,
  revision bigint NOT NULL DEFAULT 1,
  configuration jsonb NOT NULL,
  last_complete_snapshot_id uuid,
  last_sync_at timestamptz,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX federation_sources_connected_endpoint ON federation_sources (tenant_id, endpoint) WHERE status <> 'removed';

CREATE TABLE source_inspections (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  document jsonb NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL
);

CREATE TABLE source_change_previews (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  source_id uuid NOT NULL REFERENCES federation_sources(id),
  digest text NOT NULL,
  document jsonb NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  UNIQUE (tenant_id, digest)
);

CREATE TABLE source_snapshots (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  source_id uuid NOT NULL REFERENCES federation_sources(id),
  state text NOT NULL,
  enumeration_complete boolean NOT NULL DEFAULT false,
  withdrawal_authority boolean NOT NULL DEFAULT false,
  member_count integer NOT NULL DEFAULT 0,
  body_digest text NOT NULL DEFAULT '',
  details jsonb NOT NULL,
  started_at timestamptz NOT NULL,
  completed_at timestamptz
);

ALTER TABLE federation_sources ADD CONSTRAINT federation_sources_snapshot_fk FOREIGN KEY (last_complete_snapshot_id) REFERENCES source_snapshots(id) DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE observations (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  source_id uuid NOT NULL REFERENCES federation_sources(id),
  snapshot_id uuid NOT NULL REFERENCES source_snapshots(id),
  source_key text NOT NULL,
  identifier text,
  version text,
  version_present boolean NOT NULL,
  kind text,
  media_type text,
  completeness text NOT NULL,
  compatibility jsonb NOT NULL,
  raw_entry jsonb NOT NULL,
  semantic_digest text NOT NULL,
  freshness text NOT NULL,
  first_seen_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL,
  UNIQUE (tenant_id, source_id, source_key, semantic_digest)
);

CREATE TABLE publication_snapshots (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  host text NOT NULL,
  document_family text NOT NULL,
  profile_id text NOT NULL,
  generation bigint NOT NULL,
  state text NOT NULL CHECK (state IN ('candidate', 'active', 'retired', 'failed')),
  etag text NOT NULL,
  document jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  activated_at timestamptz,
  UNIQUE (tenant_id, host, document_family, generation)
);

CREATE UNIQUE INDEX publication_one_active ON publication_snapshots (tenant_id, host, document_family) WHERE state = 'active';

CREATE UNIQUE INDEX namespaces_tenant_id_id_uq ON namespaces (tenant_id, id);
CREATE UNIQUE INDEX publishers_tenant_id_id_uq ON publishers (tenant_id, id);
CREATE UNIQUE INDEX resources_tenant_id_id_uq ON resources (tenant_id, id);
CREATE UNIQUE INDEX resources_tenant_namespace_id_uq ON resources (tenant_id, namespace_id, id);
CREATE UNIQUE INDEX resource_versions_tenant_id_id_uq ON resource_versions (tenant_id, id);
CREATE UNIQUE INDEX evidence_sets_tenant_id_id_uq ON evidence_sets (tenant_id, id);
CREATE UNIQUE INDEX version_revisions_tenant_id_id_uq ON resource_version_revisions (tenant_id, id);
CREATE UNIQUE INDEX catalog_listings_tenant_id_id_uq ON catalog_listings (tenant_id, id);
CREATE UNIQUE INDEX listing_revisions_tenant_id_id_uq ON listing_revisions (tenant_id, id);
CREATE UNIQUE INDEX review_submissions_tenant_id_id_uq ON review_submissions (tenant_id, id);
CREATE UNIQUE INDEX listing_releases_tenant_id_id_uq ON listing_releases (tenant_id, id);
CREATE UNIQUE INDEX webhook_endpoints_tenant_id_id_uq ON webhook_endpoints (tenant_id, id);
CREATE UNIQUE INDEX jobs_tenant_id_id_uq ON jobs (tenant_id, id);
CREATE UNIQUE INDEX federation_sources_tenant_id_id_uq ON federation_sources (tenant_id, id);
CREATE UNIQUE INDEX source_snapshots_tenant_id_id_uq ON source_snapshots (tenant_id, id);

ALTER TABLE resources ADD CONSTRAINT resources_namespace_tenant_fk FOREIGN KEY (tenant_id, namespace_id) REFERENCES namespaces(tenant_id, id);
ALTER TABLE resource_versions ADD CONSTRAINT versions_resource_tenant_fk FOREIGN KEY (tenant_id, resource_id) REFERENCES resources(tenant_id, id);
ALTER TABLE resource_version_revisions ADD CONSTRAINT revisions_version_tenant_fk FOREIGN KEY (tenant_id, resource_version_id) REFERENCES resource_versions(tenant_id, id);
ALTER TABLE resource_version_revisions ADD CONSTRAINT revisions_evidence_tenant_fk FOREIGN KEY (tenant_id, evidence_set_id) REFERENCES evidence_sets(tenant_id, id);
ALTER TABLE representations ADD CONSTRAINT representations_revision_tenant_fk FOREIGN KEY (tenant_id, version_revision_id) REFERENCES resource_version_revisions(tenant_id, id) ON DELETE CASCADE;
ALTER TABLE catalog_listings ADD CONSTRAINT listings_namespace_tenant_fk FOREIGN KEY (tenant_id, namespace_id) REFERENCES namespaces(tenant_id, id);
ALTER TABLE catalog_listings ADD CONSTRAINT listings_resource_tenant_fk FOREIGN KEY (tenant_id, resource_id) REFERENCES resources(tenant_id, id);
ALTER TABLE catalog_listings ADD CONSTRAINT listings_resource_namespace_fk FOREIGN KEY (tenant_id, namespace_id, resource_id) REFERENCES resources(tenant_id, namespace_id, id);
ALTER TABLE catalog_listings ADD CONSTRAINT listings_candidate_tenant_fk FOREIGN KEY (tenant_id, candidate_revision_id) REFERENCES listing_revisions(tenant_id, id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE catalog_listings ADD CONSTRAINT listings_release_tenant_fk FOREIGN KEY (tenant_id, current_release_id) REFERENCES listing_releases(tenant_id, id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE listing_revisions ADD CONSTRAINT listing_revisions_listing_tenant_fk FOREIGN KEY (tenant_id, listing_id) REFERENCES catalog_listings(tenant_id, id);
ALTER TABLE listing_revisions ADD CONSTRAINT listing_revisions_version_tenant_fk FOREIGN KEY (tenant_id, version_revision_id) REFERENCES resource_version_revisions(tenant_id, id);
ALTER TABLE listing_revisions ADD CONSTRAINT listing_revisions_publisher_tenant_fk FOREIGN KEY (tenant_id, publisher_id) REFERENCES publishers(tenant_id, id);
ALTER TABLE review_submissions ADD CONSTRAINT submissions_listing_tenant_fk FOREIGN KEY (tenant_id, listing_id) REFERENCES catalog_listings(tenant_id, id);
ALTER TABLE review_submissions ADD CONSTRAINT submissions_version_tenant_fk FOREIGN KEY (tenant_id, version_revision_id) REFERENCES resource_version_revisions(tenant_id, id);
ALTER TABLE review_submissions ADD CONSTRAINT submissions_listing_revision_tenant_fk FOREIGN KEY (tenant_id, listing_revision_id) REFERENCES listing_revisions(tenant_id, id);
ALTER TABLE review_submissions ADD CONSTRAINT submissions_evidence_tenant_fk FOREIGN KEY (tenant_id, evidence_set_id) REFERENCES evidence_sets(tenant_id, id);
ALTER TABLE listing_releases ADD CONSTRAINT releases_listing_tenant_fk FOREIGN KEY (tenant_id, listing_id) REFERENCES catalog_listings(tenant_id, id);
ALTER TABLE listing_releases ADD CONSTRAINT releases_submission_tenant_fk FOREIGN KEY (tenant_id, submission_id) REFERENCES review_submissions(tenant_id, id);
ALTER TABLE listing_releases ADD CONSTRAINT releases_version_tenant_fk FOREIGN KEY (tenant_id, version_revision_id) REFERENCES resource_version_revisions(tenant_id, id);
ALTER TABLE listing_releases ADD CONSTRAINT releases_listing_revision_tenant_fk FOREIGN KEY (tenant_id, listing_revision_id) REFERENCES listing_revisions(tenant_id, id);
ALTER TABLE listing_releases ADD CONSTRAINT releases_replacement_tenant_fk FOREIGN KEY (tenant_id, replacement_release_id) REFERENCES listing_releases(tenant_id, id);
ALTER TABLE webhook_deliveries ADD CONSTRAINT deliveries_endpoint_tenant_fk FOREIGN KEY (tenant_id, endpoint_id) REFERENCES webhook_endpoints(tenant_id, id);
ALTER TABLE jobs ADD CONSTRAINT jobs_parent_tenant_fk FOREIGN KEY (tenant_id, parent_job_id) REFERENCES jobs(tenant_id, id);
ALTER TABLE source_change_previews ADD CONSTRAINT previews_source_tenant_fk FOREIGN KEY (tenant_id, source_id) REFERENCES federation_sources(tenant_id, id);
ALTER TABLE source_snapshots ADD CONSTRAINT snapshots_source_tenant_fk FOREIGN KEY (tenant_id, source_id) REFERENCES federation_sources(tenant_id, id);
ALTER TABLE federation_sources ADD CONSTRAINT sources_snapshot_tenant_fk FOREIGN KEY (tenant_id, last_complete_snapshot_id) REFERENCES source_snapshots(tenant_id, id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE observations ADD CONSTRAINT observations_source_tenant_fk FOREIGN KEY (tenant_id, source_id) REFERENCES federation_sources(tenant_id, id);
ALTER TABLE observations ADD CONSTRAINT observations_snapshot_tenant_fk FOREIGN KEY (tenant_id, snapshot_id) REFERENCES source_snapshots(tenant_id, id);

CREATE INDEX resources_browse ON resources (tenant_id, updated_at DESC, id DESC);
CREATE INDEX listings_active ON catalog_listings (tenant_id, current_release_id) WHERE current_release_id IS NOT NULL;
CREATE INDEX listing_revisions_search ON listing_revisions USING gin (to_tsvector('simple'::regconfig, display_name || ' ' || description));
CREATE INDEX listing_revisions_tags ON listing_revisions USING gin (tags);
CREATE INDEX listing_revisions_queries ON listing_revisions USING gin (representative_queries);
CREATE INDEX observations_identity ON observations (tenant_id, identifier, version_present, version);
CREATE INDEX audit_tenant_time ON audit_events (tenant_id, occurred_at DESC, id DESC);

ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenants
  USING (id = nullif(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (id = nullif(current_setting('app.tenant_id', true), '')::uuid);

DO $policies$
DECLARE
  table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'namespaces', 'publishers', 'resources', 'resource_versions', 'evidence_sets',
    'resource_version_revisions', 'representations', 'catalog_listings',
    'listing_revisions', 'review_submissions', 'listing_releases', 'audit_events',
    'outbox_events', 'webhook_endpoints', 'webhook_deliveries', 'idempotency_keys', 'jobs', 'federation_sources', 'source_inspections',
    'source_change_previews', 'source_snapshots', 'observations', 'publication_snapshots'
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
