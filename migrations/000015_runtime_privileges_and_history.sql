CREATE FUNCTION reject_catalog_immutable_history_mutation() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  RAISE EXCEPTION 'catalog retained history is immutable';
END
$function$;

ALTER TABLE federation_sources
  ADD COLUMN last_error text NOT NULL DEFAULT ''
  CHECK (length(last_error) <= 512 AND last_error = btrim(last_error) AND last_error !~ '[[:cntrl:]]');

ALTER TABLE federation_sources
  ADD CONSTRAINT federation_sources_type_check
    CHECK (source_type IN ('ard_manifest','ai_catalog','ard_registry','hybrid')),
  ADD CONSTRAINT federation_sources_status_check
    CHECK (status IN ('active','paused','error','removed'));

ALTER TABLE source_snapshots
  ADD CONSTRAINT source_snapshots_state_check
    CHECK (state IN ('complete','incomplete','failed')),
  ADD CONSTRAINT source_snapshots_member_count_check
    CHECK (member_count >= 0),
  ADD CONSTRAINT source_snapshots_completion_check
    CHECK (completed_at IS NOT NULL AND completed_at >= started_at),
  ADD CONSTRAINT source_snapshots_authority_check CHECK (
    CASE state
      WHEN 'complete' THEN enumeration_complete
      WHEN 'incomplete' THEN NOT enumeration_complete AND NOT withdrawal_authority
      WHEN 'failed' THEN NOT enumeration_complete AND NOT withdrawal_authority AND member_count = 0
    END
  );

ALTER TABLE observations
  ADD CONSTRAINT observations_completeness_check
    CHECK (completeness IN ('complete','projection','unresolved'));

-- Older workers retained the preceding attempt's safe error while reclaiming
-- a waiting job. A running attempt has no completed failure yet, so discard
-- only those stale aggregate fields before enforcing the current state shape.
UPDATE jobs
   SET safe_error='',failure_class=''
 WHERE state='running' AND (safe_error<>'' OR failure_class<>'');

ALTER TABLE jobs
  ADD CONSTRAINT jobs_attempt_bounds_check
    CHECK (attempt >= 0 AND attempt_limit BETWEEN 1 AND 20),
  ADD CONSTRAINT jobs_time_order_check
    CHECK (updated_at >= created_at AND not_before >= created_at),
  ADD CONSTRAINT jobs_failure_shape_check CHECK (
    (safe_error = '') = (failure_class = '') AND
    (failure_class = '' OR failure_class IN (
      'invalid_input','policy_denied','authentication','rate_limited','transient_upstream',
      'permanent_upstream','dependency','internal','cancelled'
    )) AND
    (state NOT IN ('waiting','failed','dead_letter') OR safe_error <> '') AND
    (state NOT IN ('queued','running') OR safe_error = '')
  ),
  ADD CONSTRAINT jobs_result_shape_check
    CHECK ((result IS NOT NULL) = (state = 'succeeded') AND (state <> 'succeeded' OR progress = 100)),
  ADD CONSTRAINT jobs_lease_shape_check CHECK (
    (state = 'running' AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL) OR
    (state <> 'running' AND lease_owner IS NULL AND lease_expires_at IS NULL)
  );

ALTER TABLE webhook_deliveries
  ADD CONSTRAINT webhook_deliveries_attempt_check CHECK (attempt >= 0),
  ADD CONSTRAINT webhook_deliveries_response_check
    CHECK (response_status IS NULL OR response_status BETWEEN 100 AND 599),
  ADD CONSTRAINT webhook_deliveries_time_order_check CHECK (updated_at >= created_at),
  ADD CONSTRAINT webhook_deliveries_state_shape_check CHECK (
    CASE state
      WHEN 'pending' THEN attempt = 0 AND response_status IS NULL AND safe_error = '' AND next_attempt_at IS NULL
      WHEN 'succeeded' THEN attempt > 0 AND response_status BETWEEN 200 AND 299 AND safe_error = '' AND next_attempt_at IS NULL
      WHEN 'waiting' THEN attempt > 0 AND safe_error <> '' AND next_attempt_at IS NOT NULL AND next_attempt_at > updated_at
      WHEN 'dead_letter' THEN attempt > 0 AND safe_error <> '' AND next_attempt_at IS NULL
    END
  );

UPDATE federation_sources
   SET last_error=configuration->>'lastError'
 WHERE jsonb_typeof(configuration->'lastError')='string'
   AND length(configuration->>'lastError') <= 512
   AND configuration->>'lastError'=btrim(configuration->>'lastError')
   AND configuration->>'lastError' !~ '[[:cntrl:]]';

CREATE UNIQUE INDEX version_revisions_tenant_version_id_uq
  ON resource_version_revisions (tenant_id,resource_version_id,id);

ALTER TABLE resource_versions
  ADD CONSTRAINT resource_versions_candidate_parent_fk
    FOREIGN KEY (tenant_id,id,candidate_revision_id)
    REFERENCES resource_version_revisions(tenant_id,resource_version_id,id)
    DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT resource_versions_sealed_parent_fk
    FOREIGN KEY (tenant_id,id,sealed_revision_id)
    REFERENCES resource_version_revisions(tenant_id,resource_version_id,id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE UNIQUE INDEX listing_revisions_tenant_listing_id_uq
  ON listing_revisions (tenant_id,listing_id,id);

CREATE UNIQUE INDEX listing_revisions_submission_identity_uq
  ON listing_revisions (tenant_id,listing_id,id,version_revision_id);

ALTER TABLE catalog_listings
  ADD CONSTRAINT catalog_listings_candidate_parent_fk
    FOREIGN KEY (tenant_id,id,candidate_revision_id)
    REFERENCES listing_revisions(tenant_id,listing_id,id)
    DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE review_submissions
  ADD CONSTRAINT submissions_listing_revision_identity_fk
    FOREIGN KEY (tenant_id,listing_id,listing_revision_id,version_revision_id)
    REFERENCES listing_revisions(tenant_id,listing_id,id,version_revision_id);

CREATE UNIQUE INDEX validation_runs_submission_evidence_uq
  ON version_validation_runs (tenant_id,version_revision_id,evidence_set_id);

ALTER TABLE review_submissions
  ADD CONSTRAINT submissions_validation_evidence_fk
    FOREIGN KEY (tenant_id,version_revision_id,evidence_set_id)
    REFERENCES version_validation_runs(tenant_id,version_revision_id,evidence_set_id),
  ADD CONSTRAINT submissions_decision_state_check CHECK (
    jsonb_typeof(decisions) = 'array' AND
    CASE state
      WHEN 'submitted' THEN decided_at IS NULL AND jsonb_array_length(decisions) = 0
      ELSE decided_at IS NOT NULL AND jsonb_array_length(decisions) > 0
    END
  );

CREATE UNIQUE INDEX evidence_sets_submission_identity_uq
  ON evidence_sets (tenant_id,id,digest,policy_version);

ALTER TABLE review_submissions
  ADD CONSTRAINT submissions_evidence_identity_fk
    FOREIGN KEY (tenant_id,evidence_set_id,evidence_digest,policy_version)
    REFERENCES evidence_sets(tenant_id,id,digest,policy_version);

CREATE UNIQUE INDEX submissions_release_identity_uq
  ON review_submissions (tenant_id,id,listing_id,version_revision_id,listing_revision_id);

ALTER TABLE listing_releases
  ADD CONSTRAINT releases_submission_identity_fk
    FOREIGN KEY (tenant_id,submission_id,listing_id,version_revision_id,listing_revision_id)
    REFERENCES review_submissions(tenant_id,id,listing_id,version_revision_id,listing_revision_id);

CREATE UNIQUE INDEX listing_releases_tenant_listing_id_uq
  ON listing_releases (tenant_id,listing_id,id);

ALTER TABLE catalog_listings
  ADD CONSTRAINT catalog_listings_release_parent_fk
    FOREIGN KEY (tenant_id,id,current_release_id)
    REFERENCES listing_releases(tenant_id,listing_id,id)
    DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE listing_releases
  ADD CONSTRAINT listing_releases_guidance_check CHECK (
    replacement_release_id IS DISTINCT FROM id AND
    CASE state
      WHEN 'deprecated' THEN sunset_at IS NULL OR sunset_at > updated_at
      WHEN 'superseded' THEN replacement_release_id IS NOT NULL AND sunset_at IS NULL
      ELSE replacement_release_id IS NULL AND sunset_at IS NULL
    END
  );

ALTER TABLE federation_sources
  ADD CONSTRAINT federation_sources_snapshot_parent_fk
    FOREIGN KEY (tenant_id,id,last_complete_snapshot_id)
    REFERENCES source_snapshots(tenant_id,source_id,id)
    DEFERRABLE INITIALLY DEFERRED;

DO $integrity$
BEGIN
  IF EXISTS (
    SELECT 1
      FROM catalog_listings listing
      JOIN listing_releases release
        ON release.tenant_id=listing.tenant_id AND release.listing_id=listing.id AND release.id=listing.current_release_id
     WHERE release.state NOT IN ('published','deprecated')
  ) OR EXISTS (
    SELECT 1
      FROM listing_releases release
      JOIN catalog_listings listing
        ON listing.tenant_id=release.tenant_id AND listing.id=release.listing_id
     WHERE release.state IN ('published','deprecated') AND listing.current_release_id IS DISTINCT FROM release.id
  ) THEN
    RAISE EXCEPTION 'existing Catalog release state is inconsistent with its current pointer';
  END IF;
  IF EXISTS (
    SELECT 1
      FROM listing_releases release
      JOIN review_submissions submission
        ON submission.tenant_id=release.tenant_id AND submission.id=release.submission_id
     WHERE submission.state <> 'approved'
  ) THEN
    RAISE EXCEPTION 'existing Catalog release does not reference an approved review submission';
  END IF;
  IF EXISTS (
    SELECT 1
      FROM federation_sources source
      JOIN source_snapshots snapshot
        ON snapshot.tenant_id=source.tenant_id AND snapshot.source_id=source.id AND snapshot.id=source.last_complete_snapshot_id
     WHERE snapshot.state <> 'complete'
  ) THEN
    RAISE EXCEPTION 'existing federation source points to a snapshot that is not complete';
  END IF;
END
$integrity$;

DO $triggers$
DECLARE
  table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'tenants', 'evidence_sets', 'resource_version_revisions', 'document_blobs',
    'artifact_references', 'skill_file_inventory', 'listing_revisions',
    'audit_events', 'source_inspections', 'source_change_previews', 'source_snapshots'
  ] LOOP
    EXECUTE format(
      'CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION reject_catalog_immutable_history_mutation()',
      table_name || '_immutable_history', table_name
    );
  END LOOP;
END
$triggers$;

CREATE FUNCTION guard_representation_history() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.external_reference_only OR NEW.inline_document IS NOT NULL OR NEW.document_blob_id IS NOT NULL THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'new bounded representation documents must be authoritative in PostgreSQL';
  END IF;
  IF TG_OP = 'UPDATE' AND
     CURRENT_USER = pg_catalog.pg_get_userbyid((SELECT class.relowner FROM pg_catalog.pg_class class WHERE class.oid=TG_RELID)) AND
     OLD.document_blob_id IS NULL AND OLD.artifact_id IS NOT NULL AND
     NEW.document_blob_id IS NOT DISTINCT FROM OLD.artifact_id AND
     (to_jsonb(NEW)-'document_blob_id') = (to_jsonb(OLD)-'document_blob_id') THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'catalog retained history is immutable';
END
$function$;

CREATE TRIGGER representations_immutable_history
  BEFORE INSERT OR UPDATE OR DELETE ON representations
  FOR EACH ROW EXECUTE FUNCTION guard_representation_history();

DO $triggers$
DECLARE
  table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'namespaces', 'publishers', 'resources', 'resource_versions', 'catalog_listings',
    'review_submissions', 'listing_releases', 'federation_sources', 'tenant_identity_realms',
    'tenant_memberships', 'namespace_memberships', 'role_assignments', 'observations',
    'publication_snapshots', 'jobs', 'outbox_events', 'webhook_endpoints', 'webhook_deliveries'
  ] LOOP
    EXECUTE format(
      'CREATE TRIGGER %I BEFORE DELETE ON %I FOR EACH ROW EXECUTE FUNCTION reject_catalog_immutable_history_mutation()',
      table_name || '_retained_row', table_name
    );
  END LOOP;
END
$triggers$;

CREATE FUNCTION guard_namespace_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(NEW.id,NEW.tenant_id,NEW.slug,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.slug,OLD.created_at) OR
     NEW.revision <> OLD.revision+1 OR NEW.updated_at < OLD.updated_at OR
     ROW(NEW.display_name,NEW.description) IS NOT DISTINCT FROM ROW(OLD.display_name,OLD.description) THEN
    RAISE EXCEPTION 'namespace lifecycle transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER namespaces_lifecycle
  BEFORE UPDATE ON namespaces
  FOR EACH ROW EXECUTE FUNCTION guard_namespace_lifecycle();

CREATE FUNCTION guard_publisher_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(NEW.id,NEW.tenant_id,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.created_at) OR
     NEW.revision <> OLD.revision+1 OR NEW.updated_at < OLD.updated_at OR
     ROW(NEW.display_name,NEW.domains,NEW.verified_domains,NEW.approved_publication_domains)
       IS NOT DISTINCT FROM ROW(OLD.display_name,OLD.domains,OLD.verified_domains,OLD.approved_publication_domains) THEN
    RAISE EXCEPTION 'publisher lifecycle transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER publishers_lifecycle
  BEFORE UPDATE ON publishers
  FOR EACH ROW EXECUTE FUNCTION guard_publisher_lifecycle();

CREATE FUNCTION guard_resource_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(NEW.id,NEW.tenant_id,NEW.namespace_id,NEW.canonical_identifier,NEW.kind,NEW.created_by,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.namespace_id,OLD.canonical_identifier,OLD.kind,OLD.created_by,OLD.created_at) THEN
    RAISE EXCEPTION 'resource identity is immutable';
  END IF;
  IF OLD.deleted_at IS NOT NULL OR NEW.deleted_at IS NULL OR NEW.revision <> OLD.revision+1 OR NEW.updated_at < OLD.updated_at THEN
    RAISE EXCEPTION 'resource lifecycle transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER resources_lifecycle
  BEFORE UPDATE ON resources
  FOR EACH ROW EXECUTE FUNCTION guard_resource_lifecycle();

CREATE FUNCTION guard_resource_version_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  old_candidate_number integer;
  new_candidate_number integer;
BEGIN
  IF ROW(NEW.id,NEW.tenant_id,NEW.resource_id,NEW.version,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.resource_id,OLD.version,OLD.created_at) OR
     NEW.updated_at < OLD.updated_at OR OLD.sealed_revision_id IS NOT NULL THEN
    RAISE EXCEPTION 'resource version identity or sealed state is immutable';
  END IF;
  IF OLD.candidate_revision_id IS NULL AND NEW.candidate_revision_id IS NOT NULL AND
     OLD.revision = NEW.revision AND NEW.sealed_revision_id IS NULL AND NEW.updated_at = OLD.updated_at THEN
    RETURN NEW;
  END IF;
  IF NEW.revision = OLD.revision+1 AND NEW.candidate_revision_id IS NOT NULL AND (
       NEW.sealed_revision_id IS NULL AND NEW.candidate_revision_id IS DISTINCT FROM OLD.candidate_revision_id OR
       NEW.sealed_revision_id IS NOT NULL AND
         NEW.candidate_revision_id IS NOT DISTINCT FROM OLD.candidate_revision_id AND
         NEW.sealed_revision_id IS NOT DISTINCT FROM OLD.candidate_revision_id
     ) THEN
    IF NEW.sealed_revision_id IS NULL AND OLD.candidate_revision_id IS NOT NULL THEN
      EXECUTE format(
        'SELECT revision_number FROM %I.resource_version_revisions WHERE tenant_id=$1 AND resource_version_id=$2 AND id=$3',
        TG_TABLE_SCHEMA
      ) INTO old_candidate_number USING OLD.tenant_id,OLD.id,OLD.candidate_revision_id;
      EXECUTE format(
        'SELECT revision_number FROM %I.resource_version_revisions WHERE tenant_id=$1 AND resource_version_id=$2 AND id=$3',
        TG_TABLE_SCHEMA
      ) INTO new_candidate_number USING NEW.tenant_id,NEW.id,NEW.candidate_revision_id;
      IF old_candidate_number IS NOT NULL AND new_candidate_number IS NOT NULL AND new_candidate_number <= old_candidate_number THEN
        RAISE EXCEPTION 'resource version candidate revision must move forward';
      END IF;
    END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'resource version pointer transition is invalid';
END
$function$;

CREATE TRIGGER resource_versions_lifecycle
  BEFORE UPDATE ON resource_versions
  FOR EACH ROW EXECUTE FUNCTION guard_resource_version_lifecycle();

CREATE FUNCTION guard_catalog_listing_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  pointer_changes integer;
  old_candidate_number integer;
  new_candidate_number integer;
BEGIN
  IF ROW(NEW.id,NEW.tenant_id,NEW.namespace_id,NEW.resource_id,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.namespace_id,OLD.resource_id,OLD.created_at) OR
     NEW.revision <> OLD.revision+1 OR NEW.updated_at < OLD.updated_at THEN
    RAISE EXCEPTION 'Catalog listing identity or revision transition is invalid';
  END IF;
  pointer_changes :=
    CASE WHEN NEW.candidate_revision_id IS DISTINCT FROM OLD.candidate_revision_id THEN 1 ELSE 0 END +
    CASE WHEN NEW.current_release_id IS DISTINCT FROM OLD.current_release_id THEN 1 ELSE 0 END;
  IF pointer_changes <> 1 OR NEW.candidate_revision_id IS NULL THEN
    RAISE EXCEPTION 'Catalog listing update must change exactly one current pointer';
  END IF;
  IF NEW.candidate_revision_id IS DISTINCT FROM OLD.candidate_revision_id AND OLD.candidate_revision_id IS NOT NULL THEN
    EXECUTE format(
      'SELECT revision_number FROM %I.listing_revisions WHERE tenant_id=$1 AND listing_id=$2 AND id=$3',
      TG_TABLE_SCHEMA
    ) INTO old_candidate_number USING OLD.tenant_id,OLD.id,OLD.candidate_revision_id;
    EXECUTE format(
      'SELECT revision_number FROM %I.listing_revisions WHERE tenant_id=$1 AND listing_id=$2 AND id=$3',
      TG_TABLE_SCHEMA
    ) INTO new_candidate_number USING NEW.tenant_id,NEW.id,NEW.candidate_revision_id;
    IF old_candidate_number IS NOT NULL AND new_candidate_number IS NOT NULL AND new_candidate_number <= old_candidate_number THEN
      RAISE EXCEPTION 'Catalog listing candidate revision must move forward';
    END IF;
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER catalog_listings_lifecycle
  BEFORE UPDATE ON catalog_listings
  FOR EACH ROW EXECUTE FUNCTION guard_catalog_listing_lifecycle();

CREATE FUNCTION enforce_catalog_current_release_state() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  checked_tenant uuid;
  checked_listing uuid;
  invalid_pointer boolean;
  unselected_current_state boolean := false;
BEGIN
  checked_tenant := NEW.tenant_id;
  IF TG_TABLE_NAME = 'catalog_listings' THEN
    checked_listing := NEW.id;
  ELSE
    checked_listing := NEW.listing_id;
  END IF;
  EXECUTE format(
    'SELECT EXISTS (
       SELECT 1
         FROM %I.catalog_listings listing
         JOIN %I.listing_releases release
           ON release.tenant_id=listing.tenant_id AND release.listing_id=listing.id AND release.id=listing.current_release_id
        WHERE listing.tenant_id=$1 AND listing.id=$2 AND release.state NOT IN (''published'',''deprecated'')
     )',
    TG_TABLE_SCHEMA,TG_TABLE_SCHEMA
  ) INTO invalid_pointer USING checked_tenant,checked_listing;
  EXECUTE format(
    'SELECT EXISTS (
       SELECT 1
         FROM %I.listing_releases release
         JOIN %I.catalog_listings listing
           ON listing.tenant_id=release.tenant_id AND listing.id=release.listing_id
        WHERE release.tenant_id=$1 AND release.listing_id=$2
          AND release.state IN (''published'',''deprecated'')
          AND listing.current_release_id IS DISTINCT FROM release.id
     )',
    TG_TABLE_SCHEMA,TG_TABLE_SCHEMA
  ) INTO unselected_current_state USING checked_tenant,checked_listing;
  IF invalid_pointer THEN
    RAISE EXCEPTION 'Catalog listing current release must remain published or deprecated';
  END IF;
  IF unselected_current_state THEN
    RAISE EXCEPTION 'published or deprecated listing release must be the current release';
  END IF;
  RETURN NULL;
END
$function$;

CREATE CONSTRAINT TRIGGER catalog_listings_current_release_state
  AFTER INSERT OR UPDATE ON catalog_listings
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION enforce_catalog_current_release_state();

CREATE FUNCTION guard_review_submission_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.state <> 'submitted' OR NEW.revision <> 1 OR NEW.decided_at IS NOT NULL OR
       NEW.decisions IS DISTINCT FROM '[]'::jsonb THEN
      RAISE EXCEPTION 'new review submission must be undecided and submitted';
    END IF;
    RETURN NEW;
  END IF;
  IF ROW(
       NEW.id,NEW.tenant_id,NEW.listing_id,NEW.version_revision_id,NEW.listing_revision_id,
       NEW.evidence_set_id,NEW.evidence_digest,NEW.policy_version,NEW.submitted_by,NEW.submitted_at
     ) IS DISTINCT FROM ROW(
       OLD.id,OLD.tenant_id,OLD.listing_id,OLD.version_revision_id,OLD.listing_revision_id,
       OLD.evidence_set_id,OLD.evidence_digest,OLD.policy_version,OLD.submitted_by,OLD.submitted_at
     ) OR OLD.state <> 'submitted' OR NEW.state NOT IN ('approved','changes_required','rejected') OR
     NEW.revision <> OLD.revision+1 OR NEW.decided_at IS NULL OR NEW.decided_at < NEW.submitted_at OR
     jsonb_typeof(NEW.decisions) <> 'array' OR
     jsonb_array_length(NEW.decisions) <>
       (CASE WHEN jsonb_typeof(OLD.decisions) = 'array' THEN jsonb_array_length(OLD.decisions)+1 ELSE 1 END) OR
     NEW.decisions - (jsonb_array_length(NEW.decisions)-1) IS DISTINCT FROM
       (CASE WHEN jsonb_typeof(OLD.decisions) = 'array' THEN OLD.decisions ELSE '[]'::jsonb END) THEN
    RAISE EXCEPTION 'review submission lifecycle transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER review_submissions_lifecycle
  BEFORE INSERT OR UPDATE ON review_submissions
  FOR EACH ROW EXECUTE FUNCTION guard_review_submission_lifecycle();

CREATE FUNCTION guard_listing_release_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  replacement_state text;
  submission_state text;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.state <> 'published' OR NEW.revision <> 1 OR NEW.replacement_release_id IS NOT NULL OR
       NEW.sunset_at IS NOT NULL OR NEW.updated_at IS DISTINCT FROM NEW.activated_at THEN
      RAISE EXCEPTION 'new listing release must be an active initial publication';
    END IF;
    EXECUTE format(
      'SELECT state FROM %I.review_submissions WHERE tenant_id=$1 AND id=$2',
      TG_TABLE_SCHEMA
    ) INTO submission_state USING NEW.tenant_id,NEW.submission_id;
    IF submission_state IS NOT NULL AND submission_state <> 'approved' THEN
      RAISE EXCEPTION 'new listing release requires an approved review submission';
    END IF;
    RETURN NEW;
  END IF;
  IF ROW(
       NEW.id,NEW.tenant_id,NEW.listing_id,NEW.submission_id,NEW.version_revision_id,
       NEW.listing_revision_id,NEW.activated_by,NEW.activated_at
     ) IS DISTINCT FROM ROW(
       OLD.id,OLD.tenant_id,OLD.listing_id,OLD.submission_id,OLD.version_revision_id,
       OLD.listing_revision_id,OLD.activated_by,OLD.activated_at
     ) OR NEW.revision <> OLD.revision+1 OR NEW.updated_at < OLD.updated_at OR NOT (
       OLD.state = 'published' AND NEW.state IN ('deprecated','retired','superseded','revoked') OR
       OLD.state = 'deprecated' AND NEW.state IN ('retired','superseded','revoked') OR
       OLD.state IN ('retired','superseded') AND NEW.state = 'revoked'
     ) THEN
    RAISE EXCEPTION 'listing release lifecycle transition is invalid';
  END IF;
  IF NEW.replacement_release_id IS NOT NULL THEN
    EXECUTE format(
      'SELECT state FROM %I.listing_releases WHERE tenant_id=$1 AND id=$2',
      TG_TABLE_SCHEMA
    ) INTO replacement_state USING NEW.tenant_id,NEW.replacement_release_id;
    IF replacement_state IS NOT NULL AND replacement_state NOT IN ('published','deprecated') THEN
      RAISE EXCEPTION 'listing release replacement must remain discoverable';
    END IF;
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER listing_releases_lifecycle
  BEFORE INSERT OR UPDATE ON listing_releases
  FOR EACH ROW EXECUTE FUNCTION guard_listing_release_lifecycle();

CREATE CONSTRAINT TRIGGER listing_releases_current_release_state
  AFTER INSERT OR UPDATE ON listing_releases
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION enforce_catalog_current_release_state();

ALTER TABLE federation_sources
  ADD CONSTRAINT federation_sources_current_revision_fk
  FOREIGN KEY (tenant_id,id,revision)
  REFERENCES federation_source_revisions(tenant_id,source_id,revision)
  DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION guard_source_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  old_snapshot_completed_at timestamptz;
  new_snapshot_completed_at timestamptz;
  new_snapshot_state text;
BEGIN
  IF ROW(NEW.id,NEW.tenant_id,NEW.created_by,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.created_by,OLD.created_at) THEN
    RAISE EXCEPTION 'federation source identity is immutable';
  END IF;
  IF OLD.status = 'removed' THEN
    RAISE EXCEPTION 'removed federation source is terminal';
  END IF;
  IF NEW.revision NOT IN (OLD.revision,OLD.revision+1) OR NEW.updated_at < OLD.updated_at THEN
    RAISE EXCEPTION 'federation source revision transition is invalid';
  END IF;
  IF OLD.last_sync_at IS NOT NULL AND
     (NEW.last_sync_at IS NULL OR NEW.last_sync_at < OLD.last_sync_at) THEN
    RAISE EXCEPTION 'federation source synchronization time cannot move backwards';
  END IF;
  IF NEW.revision = OLD.revision AND
     ROW(NEW.display_name,NEW.source_type,NEW.endpoint,NEW.configuration)
       IS DISTINCT FROM ROW(OLD.display_name,OLD.source_type,OLD.endpoint,OLD.configuration) THEN
    RAISE EXCEPTION 'federation source configuration requires a new revision';
  END IF;
  IF NEW.revision = OLD.revision AND NEW.status IS DISTINCT FROM OLD.status AND
     NOT (OLD.status IN ('active','error') AND NEW.status IN ('active','error')) THEN
    RAISE EXCEPTION 'federation source administrative status requires a new revision';
  END IF;
  IF NEW.revision = OLD.revision+1 AND NEW.status = 'error' AND OLD.status <> 'error' THEN
    RAISE EXCEPTION 'federation source error state requires synchronization evidence';
  END IF;
  IF NEW.revision = OLD.revision+1 AND
     ROW(NEW.last_complete_snapshot_id,NEW.last_sync_at,NEW.last_error)
       IS DISTINCT FROM ROW(OLD.last_complete_snapshot_id,OLD.last_sync_at,OLD.last_error) THEN
    RAISE EXCEPTION 'federation source configuration cannot rewrite synchronization evidence';
  END IF;
  IF NEW.last_complete_snapshot_id IS DISTINCT FROM OLD.last_complete_snapshot_id THEN
    IF NEW.last_complete_snapshot_id IS NULL THEN
      RAISE EXCEPTION 'federation source complete snapshot cannot be cleared';
    END IF;
    EXECUTE format(
      'SELECT completed_at,state FROM %I.source_snapshots WHERE tenant_id=$1 AND source_id=$2 AND id=$3',
      TG_TABLE_SCHEMA
    ) INTO new_snapshot_completed_at,new_snapshot_state USING NEW.tenant_id,NEW.id,NEW.last_complete_snapshot_id;
    -- Missing, cross-source, or incomplete targets are rejected by the exact
    -- deferred foreign key and complete-snapshot constraint trigger. Apply
    -- ordering checks only to a valid complete target so those errors remain
    -- specific and a transaction may establish its parent before commit.
    IF new_snapshot_state = 'complete' THEN
      IF NEW.last_sync_at IS NULL OR NEW.last_sync_at < new_snapshot_completed_at THEN
        RAISE EXCEPTION 'federation source complete snapshot requires matching synchronization time';
      END IF;
      IF OLD.last_complete_snapshot_id IS NOT NULL THEN
        EXECUTE format(
          'SELECT completed_at FROM %I.source_snapshots WHERE tenant_id=$1 AND source_id=$2 AND id=$3',
          TG_TABLE_SCHEMA
        ) INTO old_snapshot_completed_at USING OLD.tenant_id,OLD.id,OLD.last_complete_snapshot_id;
        IF old_snapshot_completed_at IS NOT NULL AND new_snapshot_completed_at < old_snapshot_completed_at THEN
          RAISE EXCEPTION 'federation source complete snapshot cannot move backwards';
        END IF;
      END IF;
    END IF;
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER federation_sources_lifecycle
  BEFORE UPDATE ON federation_sources
  FOR EACH ROW EXECUTE FUNCTION guard_source_lifecycle();

CREATE FUNCTION enforce_source_complete_snapshot() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
DECLARE
  invalid_pointer boolean;
BEGIN
  EXECUTE format(
    'SELECT EXISTS (
       SELECT 1
         FROM %I.federation_sources source
         JOIN %I.source_snapshots snapshot
           ON snapshot.tenant_id=source.tenant_id AND snapshot.source_id=source.id AND snapshot.id=source.last_complete_snapshot_id
        WHERE source.tenant_id=$1 AND source.id=$2 AND snapshot.state<>''complete''
     )',
    TG_TABLE_SCHEMA,TG_TABLE_SCHEMA
  ) INTO invalid_pointer USING NEW.tenant_id,NEW.id;
  IF invalid_pointer THEN
    RAISE EXCEPTION 'federation source last complete snapshot must be complete';
  END IF;
  RETURN NULL;
END
$function$;

CREATE CONSTRAINT TRIGGER federation_sources_complete_snapshot
  AFTER INSERT OR UPDATE ON federation_sources
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION enforce_source_complete_snapshot();

CREATE FUNCTION guard_tenant_identity_realm_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF to_jsonb(NEW) = to_jsonb(OLD) THEN
    RETURN NEW;
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.issuer,NEW.tenancy_mode,NEW.external_organization_key,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.issuer,OLD.tenancy_mode,OLD.external_organization_key,OLD.created_at) OR
     NEW.revision <> OLD.revision+1 OR NEW.updated_at < OLD.updated_at OR
     NOT (OLD.state = 'active' AND NEW.state = 'suspended' OR OLD.state = 'suspended' AND NEW.state = 'active') THEN
    RAISE EXCEPTION 'identity realm lifecycle transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER tenant_identity_realms_lifecycle
  BEFORE UPDATE ON tenant_identity_realms
  FOR EACH ROW EXECUTE FUNCTION guard_tenant_identity_realm_lifecycle();

CREATE FUNCTION guard_tenant_membership_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF to_jsonb(NEW) = to_jsonb(OLD) THEN
    RETURN NEW;
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.realm_id,NEW.subject,NEW.actor_type,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.realm_id,OLD.subject,OLD.actor_type,OLD.created_at) OR
     NEW.revision <> OLD.revision+1 OR NEW.updated_at < OLD.updated_at OR OLD.state = 'offboarded' OR NOT (
       OLD.state = 'active' AND NEW.state IN ('suspended','offboarded') OR
       OLD.state = 'suspended' AND NEW.state IN ('active','offboarded')
     ) THEN
    RAISE EXCEPTION 'tenant membership lifecycle transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER tenant_memberships_lifecycle
  BEFORE UPDATE ON tenant_memberships
  FOR EACH ROW EXECUTE FUNCTION guard_tenant_membership_lifecycle();

CREATE FUNCTION guard_namespace_membership_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF to_jsonb(NEW) = to_jsonb(OLD) THEN
    RETURN NEW;
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.membership_id,NEW.namespace_id)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.membership_id,OLD.namespace_id) OR
     NEW.revision <> OLD.revision+1 OR NOT (
       OLD.state = 'active' AND NEW.state = 'revoked' AND
         ROW(NEW.created_by,NEW.created_at) IS NOT DISTINCT FROM ROW(OLD.created_by,OLD.created_at) OR
       OLD.state = 'revoked' AND NEW.state = 'active' AND NEW.created_at >= OLD.created_at
     ) THEN
    RAISE EXCEPTION 'namespace membership lifecycle transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER namespace_memberships_lifecycle
  BEFORE UPDATE ON namespace_memberships
  FOR EACH ROW EXECUTE FUNCTION guard_namespace_membership_lifecycle();

CREATE FUNCTION guard_role_assignment_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF to_jsonb(NEW) = to_jsonb(OLD) THEN
    RETURN NEW;
  END IF;
  IF ROW(
       NEW.id,NEW.tenant_id,NEW.membership_id,NEW.namespace_id,NEW.role,
       NEW.granted_by,NEW.grant_reason,NEW.granted_at
     ) IS DISTINCT FROM ROW(
       OLD.id,OLD.tenant_id,OLD.membership_id,OLD.namespace_id,OLD.role,
       OLD.granted_by,OLD.grant_reason,OLD.granted_at
     ) OR OLD.state <> 'active' OR NEW.state <> 'revoked' OR NEW.revision <> OLD.revision+1 THEN
    RAISE EXCEPTION 'role assignment lifecycle transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER role_assignments_lifecycle
  BEFORE UPDATE ON role_assignments
  FOR EACH ROW EXECUTE FUNCTION guard_role_assignment_lifecycle();

CREATE FUNCTION guard_observation_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(
       NEW.id,NEW.tenant_id,NEW.source_id,NEW.snapshot_id,NEW.source_key,NEW.identifier,NEW.version,
       NEW.version_present,NEW.kind,NEW.media_type,NEW.completeness,NEW.compatibility,NEW.raw_entry,
       NEW.semantic_digest,NEW.exact_entry_digest,NEW.first_seen_at
     ) IS DISTINCT FROM ROW(
       OLD.id,OLD.tenant_id,OLD.source_id,OLD.snapshot_id,OLD.source_key,OLD.identifier,OLD.version,
       OLD.version_present,OLD.kind,OLD.media_type,OLD.completeness,OLD.compatibility,OLD.raw_entry,
       OLD.semantic_digest,OLD.exact_entry_digest,OLD.first_seen_at
     ) THEN
    RAISE EXCEPTION 'observation identity and original evidence are immutable';
  END IF;
  IF OLD.raw_entry_bytes IS NOT NULL AND NEW.raw_entry_bytes IS DISTINCT FROM OLD.raw_entry_bytes THEN
    RAISE EXCEPTION 'exact observation bytes cannot be replaced';
  END IF;
  IF OLD.normalized_entry_digest IS NOT NULL AND ROW(
       NEW.normalized_entry_digest,NEW.identity_digest,NEW.normalization_profile,NEW.normalized_entry_bytes
     ) IS DISTINCT FROM ROW(
       OLD.normalized_entry_digest,OLD.identity_digest,OLD.normalization_profile,OLD.normalized_entry_bytes
     ) THEN
    RAISE EXCEPTION 'normalized observation evidence cannot be replaced';
  END IF;
  IF OLD.identity_digest IS NOT NULL AND NEW.identity_digest IS DISTINCT FROM OLD.identity_digest THEN
    RAISE EXCEPTION 'observation identity digest cannot be replaced';
  END IF;
  IF OLD.freshness = 'source_removed' AND NEW.freshness <> OLD.freshness THEN
    RAISE EXCEPTION 'source-removed observation freshness is terminal';
  END IF;
  IF NEW.last_seen_at < OLD.last_seen_at THEN
    RAISE EXCEPTION 'observation last-seen time cannot move backwards';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER observations_lifecycle
  BEFORE UPDATE ON observations
  FOR EACH ROW EXECUTE FUNCTION guard_observation_lifecycle();

CREATE FUNCTION guard_source_retrieval_state_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(NEW.tenant_id,NEW.source_id,NEW.source_revision,NEW.request_key)
       IS DISTINCT FROM ROW(OLD.tenant_id,OLD.source_id,OLD.source_revision,OLD.request_key) OR
     NEW.updated_at < OLD.updated_at THEN
    RAISE EXCEPTION 'source retrieval state transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER source_retrieval_states_lifecycle
  BEFORE UPDATE ON source_retrieval_states
  FOR EACH ROW EXECUTE FUNCTION guard_source_retrieval_state_lifecycle();

CREATE FUNCTION guard_source_maintenance_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(NEW.tenant_id,NEW.source_id,NEW.source_revision)
       IS DISTINCT FROM ROW(OLD.tenant_id,OLD.source_id,OLD.source_revision) OR
     NEW.last_admitted_at < OLD.last_admitted_at THEN
    RAISE EXCEPTION 'source maintenance state transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER source_maintenance_state_lifecycle
  BEFORE UPDATE ON source_maintenance_state
  FOR EACH ROW EXECUTE FUNCTION guard_source_maintenance_lifecycle();

CREATE FUNCTION guard_publication_snapshot_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.state <> 'candidate' OR NEW.activated_at IS NOT NULL OR
       NEW.document->>'state' IS DISTINCT FROM 'candidate' OR NEW.document ? 'activatedAt' THEN
      RAISE EXCEPTION 'new publication snapshot must be a candidate';
    END IF;
    IF NEW.document->>'id' IS DISTINCT FROM NEW.id::text OR
       NEW.document->>'tenantId' IS DISTINCT FROM NEW.tenant_id::text OR
       NEW.document->>'host' IS DISTINCT FROM NEW.host OR
       NEW.document->'generation' IS DISTINCT FROM to_jsonb(NEW.generation) OR
       (NEW.document->>'ardProfile') || '+' || (NEW.document->>'aiCatalogProfile') IS DISTINCT FROM NEW.profile_id OR
       NEW.document->>'etag' IS DISTINCT FROM NEW.etag OR
       (NEW.document->>'createdAt')::timestamptz IS DISTINCT FROM NEW.created_at THEN
      RAISE EXCEPTION 'publication snapshot row and immutable document metadata are inconsistent';
    END IF;
    RETURN NEW;
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.host,NEW.document_family,NEW.profile_id,NEW.generation,NEW.etag,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.host,OLD.document_family,OLD.profile_id,OLD.generation,OLD.etag,OLD.created_at) THEN
    RAISE EXCEPTION 'publication snapshot identity is immutable';
  END IF;
  IF NOT (
    OLD.state = 'candidate' AND NEW.state = 'active' OR
    OLD.state = 'active' AND NEW.state = 'retired' OR
    OLD.state = 'retired' AND NEW.state = 'active'
  ) THEN
    RAISE EXCEPTION 'publication snapshot state transition is invalid';
  END IF;
  IF NEW.document->>'state' IS DISTINCT FROM NEW.state OR
     (NEW.document - ARRAY['state','activatedAt']) IS DISTINCT FROM (OLD.document - ARRAY['state','activatedAt']) THEN
    RAISE EXCEPTION 'publication snapshot document mutation is invalid';
  END IF;
  IF NEW.state = 'active' AND (
       NEW.activated_at IS NULL OR NEW.document->>'activatedAt' IS NULL OR
       (NEW.document->>'activatedAt')::timestamptz IS DISTINCT FROM NEW.activated_at OR
       NEW.activated_at < NEW.created_at
     ) THEN
    RAISE EXCEPTION 'active publication snapshot activation time is inconsistent';
  END IF;
  IF NEW.state = 'active' AND OLD.activated_at IS NOT NULL AND NEW.activated_at <= OLD.activated_at THEN
    RAISE EXCEPTION 'publication snapshot activation time cannot move backwards';
  END IF;
  IF NEW.state = 'retired' AND (
       NEW.activated_at IS DISTINCT FROM OLD.activated_at OR
       NEW.document->'activatedAt' IS DISTINCT FROM OLD.document->'activatedAt'
     ) THEN
    RAISE EXCEPTION 'retiring a publication snapshot cannot rewrite its activation time';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER publication_snapshots_lifecycle
  BEFORE INSERT OR UPDATE ON publication_snapshots
  FOR EACH ROW EXECUTE FUNCTION guard_publication_snapshot_lifecycle();

CREATE FUNCTION guard_job_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.state <> 'queued' OR NEW.progress <> 0 OR NEW.attempt <> 0 OR
       NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL OR
       NEW.cancellation_requested OR NEW.result IS NOT NULL OR NEW.safe_error <> '' OR NEW.failure_class <> '' THEN
      RAISE EXCEPTION 'new job must be an unleased queued job';
    END IF;
    RETURN NEW;
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.kind,NEW.actor,NEW.parent_job_id,NEW.payload_version,NEW.payload,
         NEW.priority,NEW.attempt_limit,NEW.deduplication_key,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.kind,OLD.actor,OLD.parent_job_id,OLD.payload_version,OLD.payload,
         OLD.priority,OLD.attempt_limit,OLD.deduplication_key,OLD.created_at) THEN
    RAISE EXCEPTION 'job identity and input are immutable';
  END IF;
  IF OLD.state IN ('succeeded','failed','cancelled','dead_letter') THEN
    RAISE EXCEPTION 'terminal job is immutable';
  END IF;
  IF NOT (
    OLD.state IN ('queued','waiting') AND NEW.state IN ('running','cancelled') OR
    OLD.state = 'running' AND NEW.state IN ('running','waiting','succeeded','failed','cancelled','dead_letter')
  ) THEN
    RAISE EXCEPTION 'job state transition is invalid';
  END IF;
  IF NEW.updated_at < OLD.updated_at OR NEW.progress < OLD.progress OR
     OLD.cancellation_requested AND NOT NEW.cancellation_requested THEN
    RAISE EXCEPTION 'job progress transition is invalid';
  END IF;
  IF OLD.state IN ('queued','waiting') AND NEW.state = 'running' THEN
    IF NEW.attempt <> OLD.attempt+1 OR NEW.lease_expires_at <= NEW.updated_at THEN
      RAISE EXCEPTION 'job claim attempt transition is invalid';
    END IF;
  ELSIF OLD.state = 'running' AND NEW.state = 'running' AND NEW.attempt = OLD.attempt+1 THEN
    IF OLD.lease_expires_at IS NULL OR OLD.lease_expires_at > NEW.updated_at OR
       NEW.lease_expires_at <= NEW.updated_at THEN
      RAISE EXCEPTION 'job lease cannot be reclaimed before expiry';
    END IF;
  ELSIF NEW.attempt <> OLD.attempt THEN
    RAISE EXCEPTION 'job attempt transition is invalid';
  END IF;
  IF OLD.state = 'running' AND NEW.state = 'running' AND NEW.attempt = OLD.attempt AND
     NEW.lease_owner IS DISTINCT FROM OLD.lease_owner THEN
    RAISE EXCEPTION 'job lease owner cannot change during an active attempt';
  END IF;
  IF OLD.state = 'running' AND NEW.state = 'running' AND NEW.attempt = OLD.attempt AND
     NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at AND
     (NEW.lease_expires_at < OLD.lease_expires_at OR NEW.lease_expires_at <= NEW.updated_at) THEN
    RAISE EXCEPTION 'job lease expiry cannot move backwards';
  END IF;
  IF NEW.not_before IS DISTINCT FROM OLD.not_before AND NOT (OLD.state = 'running' AND NEW.state = 'waiting') THEN
    RAISE EXCEPTION 'job schedule transition is invalid';
  END IF;
  IF NEW.result IS DISTINCT FROM OLD.result AND NOT (
       OLD.state = 'running' AND NEW.state = 'succeeded' AND OLD.result IS NULL AND NEW.result IS NOT NULL
     ) THEN
    RAISE EXCEPTION 'job result transition is invalid';
  END IF;
  IF ROW(NEW.safe_error,NEW.failure_class) IS DISTINCT FROM ROW(OLD.safe_error,OLD.failure_class) AND NOT (
       NEW.state = 'running' AND NEW.safe_error = '' AND NEW.failure_class = '' OR
       OLD.state = 'running' AND NEW.state IN ('waiting','failed','cancelled','dead_letter')
     ) THEN
    RAISE EXCEPTION 'job failure transition is invalid';
  END IF;
  IF NEW.state = 'succeeded' THEN
    IF NEW.progress <> 100 THEN
      RAISE EXCEPTION 'successful job progress is invalid';
    END IF;
  ELSIF NOT (OLD.state = 'running' AND NEW.state = 'running' AND NEW.attempt = OLD.attempt) AND
        NEW.progress <> OLD.progress THEN
    RAISE EXCEPTION 'job progress can change only during a heartbeat or completion';
  END IF;
  IF NOT OLD.cancellation_requested AND NEW.cancellation_requested AND NOT (
       OLD.state = 'running' AND NEW.state = 'running' AND NEW.attempt = OLD.attempt
     ) THEN
    RAISE EXCEPTION 'job cancellation request transition is invalid';
  END IF;
  IF NEW.state = 'running' AND (NEW.lease_owner IS NULL OR NEW.lease_expires_at IS NULL) OR
     NEW.state <> 'running' AND (NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL) THEN
    RAISE EXCEPTION 'job lease does not match its state';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER jobs_lifecycle
  BEFORE INSERT OR UPDATE ON jobs
  FOR EACH ROW EXECUTE FUNCTION guard_job_lifecycle();

CREATE FUNCTION guard_webhook_endpoint_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(NEW.id,NEW.tenant_id,NEW.created_by,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.created_by,OLD.created_at) OR
     NEW.revision <> OLD.revision+1 OR NEW.updated_at < OLD.updated_at OR
     NEW.secret_version NOT IN (OLD.secret_version,OLD.secret_version+1) OR
     (NEW.secret_version = OLD.secret_version) <> (NEW.encrypted_secret = OLD.encrypted_secret) OR
     ROW(NEW.name,NEW.url,NEW.event_types,NEW.status,NEW.secret_version,NEW.encrypted_secret)
       IS NOT DISTINCT FROM ROW(OLD.name,OLD.url,OLD.event_types,OLD.status,OLD.secret_version,OLD.encrypted_secret) THEN
    RAISE EXCEPTION 'webhook endpoint lifecycle transition is invalid';
  END IF;
  IF OLD.status = 'disabled' AND NEW.status <> OLD.status THEN
    RAISE EXCEPTION 'disabled webhook endpoint is terminal';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER webhook_endpoints_lifecycle
  BEFORE UPDATE ON webhook_endpoints
  FOR EACH ROW EXECUTE FUNCTION guard_webhook_endpoint_lifecycle();

CREATE FUNCTION guard_webhook_delivery_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF TG_OP = 'INSERT' THEN
    RETURN NEW;
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.endpoint_id,NEW.event_id,NEW.event_type,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.endpoint_id,OLD.event_id,OLD.event_type,OLD.created_at) OR
     OLD.state IN ('succeeded','dead_letter') OR
     NEW.state NOT IN ('waiting','succeeded','dead_letter') OR
     NEW.attempt <> OLD.attempt+1 OR NEW.updated_at < OLD.updated_at THEN
    RAISE EXCEPTION 'webhook delivery lifecycle transition is invalid';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER webhook_deliveries_lifecycle
  BEFORE INSERT OR UPDATE ON webhook_deliveries
  FOR EACH ROW EXECUTE FUNCTION guard_webhook_delivery_lifecycle();

CREATE FUNCTION guard_outbox_event_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL OR
       NEW.published_at IS NOT NULL AND NEW.published_at < NEW.created_at THEN
      RAISE EXCEPTION 'new outbox event has invalid delivery state';
    END IF;
    RETURN NEW;
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.event_type,NEW.subject,NEW.data,NEW.is_internal,NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.event_type,OLD.subject,OLD.data,OLD.is_internal,OLD.created_at) THEN
    RAISE EXCEPTION 'outbox event payload is immutable';
  END IF;
  IF OLD.published_at IS NOT NULL THEN
    RAISE EXCEPTION 'published outbox event is immutable';
  END IF;
  IF (NEW.lease_owner IS NULL) <> (NEW.lease_expires_at IS NULL) THEN
    RAISE EXCEPTION 'outbox lease owner and expiry must change together';
  END IF;
  IF NEW.published_at IS NOT NULL THEN
    IF NEW.published_at < NEW.created_at OR NEW.lease_owner IS NOT NULL OR
       OLD.lease_owner IS NULL OR OLD.lease_expires_at <= NEW.published_at THEN
      RAISE EXCEPTION 'outbox event publication transition is invalid';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.lease_owner IS NULL AND NEW.lease_owner IS NOT NULL AND NEW.lease_expires_at > NEW.created_at THEN
    RETURN NEW;
  END IF;
  IF OLD.lease_owner IS NOT NULL AND NEW.lease_owner IS NULL THEN
    RETURN NEW;
  END IF;
  IF OLD.lease_owner IS NOT NULL AND NEW.lease_owner = OLD.lease_owner AND
     NEW.lease_expires_at >= OLD.lease_expires_at THEN
    RETURN NEW;
  END IF;
  IF OLD.lease_owner IS NOT NULL AND NEW.lease_owner IS DISTINCT FROM OLD.lease_owner AND
     NEW.lease_expires_at > OLD.lease_expires_at THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'outbox event delivery transition is invalid';
END
$function$;

CREATE TRIGGER outbox_events_lifecycle
  BEFORE INSERT OR UPDATE ON outbox_events
  FOR EACH ROW EXECUTE FUNCTION guard_outbox_event_lifecycle();

CREATE FUNCTION guard_idempotency_key_update() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF ROW(NEW.tenant_id,NEW.actor,NEW.route,NEW.idempotency_key)
       IS DISTINCT FROM ROW(OLD.tenant_id,OLD.actor,OLD.route,OLD.idempotency_key) THEN
    RAISE EXCEPTION 'idempotency key scope is immutable';
  END IF;

  IF OLD.protocol_version = 1 AND OLD.response_status IS NULL AND NEW.response_status IS NOT NULL AND
     (to_jsonb(NEW) - ARRAY['response_status','response_headers','response_body','completed_at']) =
       (to_jsonb(OLD) - ARRAY['response_status','response_headers','response_body','completed_at']) THEN
    RETURN NEW;
  END IF;

  IF OLD.protocol_version = 2 AND OLD.atomic_response_status IS NULL AND OLD.released_at IS NULL AND
     NEW.atomic_response_status IS NOT NULL AND
     (to_jsonb(NEW) - ARRAY['atomic_response_status','atomic_response_headers','atomic_response_body','completed_at']) =
       (to_jsonb(OLD) - ARRAY['atomic_response_status','atomic_response_headers','atomic_response_body','completed_at']) THEN
    RETURN NEW;
  END IF;

  IF OLD.completed_at IS NOT NULL AND OLD.expires_at <= clock_timestamp() AND
     NEW.protocol_version = 2 AND NEW.response_status IS NULL AND NEW.atomic_response_status IS NULL AND
     NEW.completed_at IS NULL AND NEW.released_at IS NULL AND NEW.repair_reason IS NULL AND
     NEW.authorization_digest IS NOT NULL AND NEW.execution_token IS NOT NULL AND
     (to_jsonb(NEW) - ARRAY[
       'request_digest','response_status','response_headers','response_body','expires_at','created_at',
       'protocol_version','authorization_digest','reservation_id','execution_token','started_at','completed_at',
       'atomic_response_status','atomic_response_headers','atomic_response_body'
     ]) = (to_jsonb(OLD) - ARRAY[
       'request_digest','response_status','response_headers','response_body','expires_at','created_at',
       'protocol_version','authorization_digest','reservation_id','execution_token','started_at','completed_at',
       'atomic_response_status','atomic_response_headers','atomic_response_body'
     ]) AND
     ROW(NEW.request_digest,NEW.expires_at,NEW.created_at,NEW.reservation_id,NEW.authorization_digest,NEW.execution_token,NEW.started_at)
       IS DISTINCT FROM ROW(OLD.request_digest,OLD.expires_at,OLD.created_at,OLD.reservation_id,OLD.authorization_digest,OLD.execution_token,OLD.started_at) THEN
    RETURN NEW;
  END IF;

  IF OLD.protocol_version = 2 AND OLD.atomic_response_status IS NULL AND OLD.released_at IS NULL AND
     NEW.released_at IS NOT NULL AND NEW.repair_reason <> '' AND NEW.execution_token IS NULL AND
     (to_jsonb(NEW) - ARRAY['released_at','repair_reason','execution_token']) =
       (to_jsonb(OLD) - ARRAY['released_at','repair_reason','execution_token']) THEN
    RETURN NEW;
  END IF;

  RAISE EXCEPTION 'idempotency key lifecycle transition is invalid';
END
$function$;

CREATE TRIGGER idempotency_keys_update_lifecycle
  BEFORE UPDATE ON idempotency_keys
  FOR EACH ROW EXECUTE FUNCTION guard_idempotency_key_update();

CREATE FUNCTION guard_idempotency_key_delete() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
  IF (OLD.protocol_version = 1 AND OLD.response_status IS NOT NULL AND OLD.expires_at <= clock_timestamp()) OR
     (OLD.protocol_version = 2 AND OLD.released_at IS NOT NULL AND OLD.atomic_response_status IS NULL) THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'idempotency key cannot be deleted while replay or recovery remains authoritative';
END
$function$;

CREATE TRIGGER idempotency_keys_delete_lifecycle
  BEFORE DELETE ON idempotency_keys
  FOR EACH ROW EXECUTE FUNCTION guard_idempotency_key_delete();

REVOKE ALL ON FUNCTION reject_federation_source_history_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION reject_version_validation_run_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION reject_catalog_immutable_history_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_resource_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_resource_version_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_catalog_listing_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION enforce_catalog_current_release_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_namespace_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_publisher_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_review_submission_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_listing_release_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_source_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION enforce_source_complete_snapshot() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_tenant_identity_realm_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_tenant_membership_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_namespace_membership_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_role_assignment_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_observation_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_source_retrieval_state_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_source_maintenance_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_publication_snapshot_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_job_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_representation_history() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_webhook_endpoint_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_webhook_delivery_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_outbox_event_lifecycle() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_idempotency_key_update() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_idempotency_key_delete() FROM PUBLIC;
REVOKE ALL ON FUNCTION advance_catalog_search_generation() FROM PUBLIC;
REVOKE ALL ON FUNCTION project_observation_catalog_search() FROM PUBLIC;
REVOKE ALL ON FUNCTION catalog_core_lexical_vector_v1(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION catalog_core_lexical_query_v1(text) FROM PUBLIC;

COMMENT ON FUNCTION reject_catalog_immutable_history_mutation() IS 'Rejects ordinary update and deletion of explicitly enumerated retained Catalog history tables.';
COMMENT ON FUNCTION guard_representation_history() IS 'Requires new bounded documents to be authoritative in PostgreSQL inline or document storage and keeps representation history immutable except for the exact one-way legacy artifact-to-document link established by a reviewed migration-owner backfill.';
COMMENT ON CONSTRAINT federation_sources_current_revision_fk ON federation_sources IS 'The mutable source head must identify an immutable configuration revision committed in the same transaction.';
