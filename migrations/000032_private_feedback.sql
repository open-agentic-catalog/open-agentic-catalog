-- Private feedback is mutable; resource and version evidence remain immutable.
CREATE TABLE item_feedback (
  id uuid NOT NULL,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  membership_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  release_id uuid,
  version_revision_id uuid,
  category text NOT NULL CHECK (category IN ('problem','documentation','suggestion','positive','other')),
  comment text NOT NULL,
  response text NOT NULL DEFAULT '',
  state text NOT NULL CHECK (state IN ('open','addressed','withdrawn','removed')),
  revision bigint NOT NULL CHECK (revision > 0),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL CHECK (updated_at >= created_at),
  PRIMARY KEY (tenant_id,id),
  FOREIGN KEY (tenant_id,membership_id) REFERENCES tenant_memberships(tenant_id,id),
  FOREIGN KEY (tenant_id,resource_id) REFERENCES resources(tenant_id,id),
  FOREIGN KEY (tenant_id,release_id) REFERENCES listing_releases(tenant_id,id),
  FOREIGN KEY (tenant_id,version_revision_id) REFERENCES resource_version_revisions(tenant_id,id),
  CHECK (release_id IS NULL OR version_revision_id IS NOT NULL),
  CHECK (char_length(comment) <= 4000 AND char_length(response) <= 4000),
  CHECK ((state IN ('withdrawn','removed') AND comment='' AND response='') OR (state IN ('open','addressed') AND length(btrim(comment)) > 0)),
  CHECK (state <> 'open' OR response='')
);
CREATE INDEX item_feedback_member_page ON item_feedback(tenant_id,membership_id,id);
CREATE INDEX item_feedback_resource_page ON item_feedback(tenant_id,resource_id,id);
CREATE INDEX item_feedback_state_page ON item_feedback(tenant_id,state,id);
CREATE INDEX item_feedback_category_page ON item_feedback(tenant_id,category,id);
ALTER TABLE item_feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE item_feedback FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON item_feedback
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

-- Invoker rights deliberately require the API's existing same-tenant context reads.
CREATE FUNCTION catalog_feedback_guard() RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE related boolean;
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'feedback deletion is not permitted'; END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.revision<>1 OR NEW.state<>'open' OR NEW.created_at<>NEW.updated_at THEN RAISE EXCEPTION 'invalid feedback creation'; END IF;
    EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.tenant_memberships WHERE tenant_id=$1 AND id=$2 AND actor_type=''human'' AND state=''active'')',TG_TABLE_SCHEMA)
      INTO related USING NEW.tenant_id,NEW.membership_id;
    IF NOT related THEN RAISE EXCEPTION 'feedback requires an active human membership'; END IF;
    IF NEW.version_revision_id IS NOT NULL THEN
      EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.resource_version_revisions r JOIN %I.resource_versions v ON v.tenant_id=r.tenant_id AND v.id=r.resource_version_id WHERE r.tenant_id=$1 AND r.id=$2 AND v.resource_id=$3)',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
        INTO related USING NEW.tenant_id,NEW.version_revision_id,NEW.resource_id;
      IF NOT related THEN RAISE EXCEPTION 'feedback version context mismatch'; END IF;
    END IF;
    IF NEW.release_id IS NOT NULL THEN
      EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.listing_releases WHERE tenant_id=$1 AND id=$2 AND version_revision_id=$3)',TG_TABLE_SCHEMA)
        INTO related USING NEW.tenant_id,NEW.release_id,NEW.version_revision_id;
      IF NOT related THEN RAISE EXCEPTION 'feedback release context mismatch'; END IF;
    END IF;
  ELSE
    IF OLD.state IN ('withdrawn','removed') OR
       (NEW.tenant_id,NEW.id,NEW.membership_id,NEW.resource_id,NEW.release_id,NEW.version_revision_id,NEW.created_at) IS DISTINCT FROM
       (OLD.tenant_id,OLD.id,OLD.membership_id,OLD.resource_id,OLD.release_id,OLD.version_revision_id,OLD.created_at) OR
       NEW.revision<>OLD.revision+1 OR NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'feedback context or terminal state is immutable'; END IF;
    IF (NEW.comment,NEW.category) IS DISTINCT FROM (OLD.comment,OLD.category) AND NEW.state NOT IN ('open','withdrawn','removed') THEN RAISE EXCEPTION 'edited feedback must reopen'; END IF;
  END IF;
  RETURN NEW;
END
$function$;
REVOKE ALL ON FUNCTION catalog_feedback_guard() FROM PUBLIC;
CREATE TRIGGER item_feedback_guard BEFORE INSERT OR UPDATE OR DELETE ON item_feedback FOR EACH ROW EXECUTE FUNCTION catalog_feedback_guard();
