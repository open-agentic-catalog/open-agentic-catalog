-- Shared current-revision completed-attempt selection for Overview and directory.
CREATE INDEX source_snapshots_completed_attempt_page
    ON source_snapshots(tenant_id,source_id,source_revision,completed_at DESC,id DESC)
    WHERE completed_at IS NOT NULL;
