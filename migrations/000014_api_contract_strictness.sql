ALTER TABLE outbox_events
  ADD COLUMN is_internal boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN outbox_events.is_internal IS 'Internal operational evidence that must never be delivered through public webhook subscriptions, including wildcard subscriptions.';

CREATE INDEX namespaces_admin_page
  ON namespaces (tenant_id,(slug COLLATE "C"),id);

CREATE INDEX publishers_admin_page
  ON publishers (tenant_id,(display_name COLLATE "C"),id);

CREATE INDEX tenant_memberships_admin_page
  ON tenant_memberships (tenant_id,(subject COLLATE "C"),id);

CREATE INDEX role_assignments_admin_page
  ON role_assignments (tenant_id,membership_id,(coalesce(namespace_id::text,'') COLLATE "C"),(role COLLATE "C"),id);

CREATE INDEX federation_sources_admin_page
  ON federation_sources (tenant_id,(display_name COLLATE "C"),id)
  WHERE status<>'removed';

CREATE INDEX source_snapshots_admin_page
  ON source_snapshots (tenant_id,source_id,started_at DESC,id DESC);

CREATE INDEX observations_admin_page
  ON observations (tenant_id,source_id,freshness,(coalesce(identifier,'') COLLATE "C"),version_present DESC,(coalesce(version,'') COLLATE "C"),id);

CREATE INDEX observation_occurrences_admin_page
  ON observation_occurrences (tenant_id,observation_id,observed_at DESC,snapshot_id DESC,(retrieval_digest COLLATE "C"),(entry_location COLLATE "C"));

CREATE INDEX publication_snapshots_admin_page
  ON publication_snapshots (tenant_id,host,document_family,generation DESC,id DESC);

CREATE INDEX jobs_admin_page
  ON jobs (tenant_id,created_at DESC,id DESC);

CREATE INDEX webhook_endpoints_admin_page
  ON webhook_endpoints (tenant_id,(name COLLATE "C"),id);

CREATE INDEX webhook_deliveries_admin_page
  ON webhook_deliveries (tenant_id,endpoint_id,updated_at DESC,id DESC);
