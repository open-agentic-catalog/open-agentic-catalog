ALTER TABLE tenant_memberships
  DROP CONSTRAINT tenant_memberships_state_check,
  ADD CONSTRAINT tenant_memberships_state_check CHECK (state IN ('active','suspended','offboarded'));

ALTER TABLE resources
  ADD COLUMN deleted_by text,
  ADD COLUMN delete_reason text NOT NULL DEFAULT '',
  ADD COLUMN deleted_at timestamptz,
  ADD CONSTRAINT resources_soft_delete_check CHECK (
    (deleted_at IS NULL AND deleted_by IS NULL AND delete_reason='') OR
    (deleted_at IS NOT NULL AND deleted_by IS NOT NULL AND btrim(deleted_by)<>'' AND length(delete_reason) BETWEEN 1 AND 2000)
  );

CREATE INDEX resources_active_browse
  ON resources (tenant_id,updated_at DESC,id DESC)
  WHERE deleted_at IS NULL;

COMMENT ON COLUMN resources.deleted_at IS 'Soft-deletion marker for an unpublished local draft whose retained dependencies were checked atomically.';
COMMENT ON COLUMN tenant_memberships.state IS 'Active memberships resolve; suspended memberships can be reactivated; offboarded memberships are terminal.';
