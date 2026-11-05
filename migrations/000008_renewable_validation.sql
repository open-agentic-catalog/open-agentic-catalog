CREATE TABLE version_validation_runs (
  tenant_id uuid NOT NULL,
  version_revision_id uuid NOT NULL,
  run_number integer NOT NULL CHECK (run_number > 0),
  evidence_set_id uuid NOT NULL,
  expires_at timestamptz,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, version_revision_id, run_number),
  UNIQUE (tenant_id, evidence_set_id),
  FOREIGN KEY (tenant_id, version_revision_id) REFERENCES resource_version_revisions(tenant_id, id),
  FOREIGN KEY (tenant_id, evidence_set_id) REFERENCES evidence_sets(tenant_id, id),
  CHECK (expires_at IS NULL OR expires_at > created_at)
);

INSERT INTO version_validation_runs(tenant_id,version_revision_id,run_number,evidence_set_id,expires_at,created_at)
SELECT revision.tenant_id,revision.id,1,revision.evidence_set_id,NULL,evidence.created_at
FROM resource_version_revisions revision
JOIN evidence_sets evidence ON evidence.tenant_id=revision.tenant_id AND evidence.id=revision.evidence_set_id;

CREATE INDEX version_validation_runs_latest
  ON version_validation_runs (tenant_id, version_revision_id, run_number DESC);

ALTER TABLE version_validation_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE version_validation_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON version_validation_runs
  USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

CREATE FUNCTION reject_version_validation_run_mutation() RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION 'version validation runs are immutable';
END
$function$;

CREATE TRIGGER version_validation_runs_immutable
  BEFORE UPDATE OR DELETE ON version_validation_runs
  FOR EACH ROW EXECUTE FUNCTION reject_version_validation_run_mutation();

COMMENT ON TABLE version_validation_runs IS 'Append-only validation runs for one exact immutable VersionRevision; a null expiry means deterministic evidence does not expire with wall-clock time.';
COMMENT ON COLUMN evidence_sets.expires_at IS 'Legacy mixed-version compatibility expiry. Version validation freshness is authoritative in version_validation_runs.expires_at after migration 000008.';
