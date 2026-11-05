CREATE TABLE tenant_identity_realms (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  issuer text NOT NULL CHECK (issuer = btrim(issuer) AND length(issuer) BETWEEN 1 AND 2048),
  tenancy_mode text NOT NULL CHECK (tenancy_mode IN ('dedicated', 'shared')),
  external_organization_key text NOT NULL DEFAULT '' CHECK (external_organization_key = btrim(external_organization_key) AND length(external_organization_key) <= 512),
  state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'suspended')),
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CHECK ((tenancy_mode = 'dedicated' AND external_organization_key = '') OR (tenancy_mode = 'shared' AND external_organization_key <> '')),
  UNIQUE (tenant_id, id)
);

CREATE UNIQUE INDEX tenant_identity_realms_dedicated_mapping
  ON tenant_identity_realms (issuer, tenant_id)
  WHERE tenancy_mode = 'dedicated';

CREATE UNIQUE INDEX tenant_identity_realms_shared_mapping
  ON tenant_identity_realms (issuer, external_organization_key)
  WHERE tenancy_mode = 'shared';

CREATE TABLE tenant_memberships (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  realm_id uuid NOT NULL,
  subject text NOT NULL CHECK (subject = btrim(subject) AND length(subject) BETWEEN 1 AND 512),
  actor_type text NOT NULL CHECK (actor_type IN ('human', 'service')),
  state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'suspended')),
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, realm_id, subject),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, realm_id) REFERENCES tenant_identity_realms(tenant_id, id)
);

CREATE TABLE namespace_memberships (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  membership_id uuid NOT NULL,
  namespace_id uuid NOT NULL,
  state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'revoked')),
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  revoked_by text,
  revoke_reason text NOT NULL DEFAULT '',
  revoked_at timestamptz,
  CHECK ((state = 'active' AND revoked_by IS NULL AND revoked_at IS NULL AND revoke_reason = '') OR (state = 'revoked' AND revoked_by IS NOT NULL AND revoked_at IS NOT NULL AND revoke_reason <> '')),
  UNIQUE (tenant_id, membership_id, namespace_id),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, membership_id) REFERENCES tenant_memberships(tenant_id, id),
  FOREIGN KEY (tenant_id, namespace_id) REFERENCES namespaces(tenant_id, id)
);

CREATE TABLE role_assignments (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  membership_id uuid NOT NULL,
  namespace_id uuid,
  role text NOT NULL CHECK (role IN ('reader', 'contributor', 'reviewer', 'publisher', 'source_admin', 'security_reviewer', 'tenant_admin', 'service_client')),
  state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'revoked')),
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  granted_by text NOT NULL,
  grant_reason text NOT NULL CHECK (length(grant_reason) BETWEEN 1 AND 2000),
  granted_at timestamptz NOT NULL,
  revoked_by text,
  revoke_reason text NOT NULL DEFAULT '',
  revoked_at timestamptz,
  CHECK (namespace_id IS NULL OR role IN ('reader', 'contributor', 'reviewer', 'publisher')),
  CHECK ((state = 'active' AND revoked_by IS NULL AND revoked_at IS NULL AND revoke_reason = '') OR (state = 'revoked' AND revoked_by IS NOT NULL AND revoked_at IS NOT NULL AND revoke_reason <> '')),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, membership_id) REFERENCES tenant_memberships(tenant_id, id),
  FOREIGN KEY (tenant_id, namespace_id) REFERENCES namespaces(tenant_id, id)
);

CREATE UNIQUE INDEX role_assignments_one_active
  ON role_assignments (tenant_id, membership_id, coalesce(namespace_id, '00000000-0000-0000-0000-000000000000'::uuid), role)
  WHERE state = 'active';

CREATE INDEX tenant_memberships_resolution
  ON tenant_memberships (tenant_id, realm_id, subject)
  WHERE state = 'active';

CREATE INDEX role_assignments_resolution
  ON role_assignments (tenant_id, membership_id, namespace_id, role)
  WHERE state = 'active';

DO $policies$
DECLARE
  table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['tenant_identity_realms', 'tenant_memberships', 'namespace_memberships', 'role_assignments'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid) WITH CHECK (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)',
      table_name
    );
  END LOOP;
END
$policies$;

CREATE POLICY verified_identity_realm_resolution ON tenant_identity_realms
  FOR SELECT
  USING (
    state = 'active'
    AND issuer = nullif(current_setting('app.identity_issuer', true), '')
    AND (
      (
        nullif(current_setting('app.identity_tenant_id', true), '') IS NOT NULL
        AND tenant_id = nullif(current_setting('app.identity_tenant_id', true), '')::uuid
      )
      OR (
        external_organization_key <> ''
        AND external_organization_key = nullif(current_setting('app.identity_organization', true), '')
      )
    )
  );

COMMENT ON TABLE tenant_identity_realms IS 'Operator-provisioned exact issuer and external-organization mappings; never self-provisioned from token claims.';
COMMENT ON TABLE tenant_memberships IS 'Catalog-owned human and service memberships resolved only after a verified realm-to-tenant mapping.';
COMMENT ON TABLE role_assignments IS 'Catalog-authoritative tenant or exact-namespace roles; upstream scopes may only narrow these grants.';
