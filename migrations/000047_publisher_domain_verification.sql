-- Publication approval remains administrative. Old approvals never become DNS evidence.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM publishers WHERE NOT ((approved_publication_domains || verified_domains) <@ domains)
  OR array_position(approved_publication_domains || verified_domains,NULL) IS NOT NULL) THEN
  RAISE EXCEPTION 'publisher approval cleanup requires consistent declared domains';
 END IF;
END $$;
DROP TRIGGER publishers_lifecycle ON publishers;
UPDATE publishers SET approved_publication_domains=ARRAY(SELECT DISTINCT d FROM unnest(approved_publication_domains || verified_domains) d ORDER BY d)
 WHERE approved_publication_domains IS DISTINCT FROM ARRAY(SELECT DISTINCT d FROM unnest(approved_publication_domains || verified_domains) d ORDER BY d);
ALTER TABLE publishers DROP COLUMN verified_domains;
CREATE OR REPLACE FUNCTION guard_publisher_lifecycle() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF ROW(NEW.id,NEW.tenant_id,NEW.created_at) IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.created_at)
 OR NEW.revision<>OLD.revision+1 OR NEW.updated_at<OLD.updated_at
 OR ROW(NEW.display_name,NEW.domains,NEW.approved_publication_domains) IS NOT DISTINCT FROM ROW(OLD.display_name,OLD.domains,OLD.approved_publication_domains) THEN
  RAISE EXCEPTION 'publisher lifecycle transition is invalid';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER publishers_lifecycle BEFORE UPDATE ON publishers FOR EACH ROW EXECUTE FUNCTION guard_publisher_lifecycle();

CREATE TABLE publisher_domain_verifications (
 id uuid NOT NULL,
 tenant_id uuid NOT NULL,
 publisher_id uuid NOT NULL,
 domain text NOT NULL CHECK(length(domain) BETWEEN 1 AND 221 AND domain=lower(domain)),
 revision bigint NOT NULL CHECK(revision>0),
 challenge_id uuid,
 nonce text NOT NULL DEFAULT '',
 challenge_issued_at timestamptz,
 challenge_expires_at timestamptz,
 challenge_consumed boolean NOT NULL DEFAULT false,
 verified_challenge_id uuid,
 verified_at timestamptz,
 proof_expires_at timestamptz,
 last_check_at timestamptz,
 last_check_challenge_id uuid,
 last_check_outcome text NOT NULL DEFAULT '' CHECK(last_check_outcome IN ('','matched','mismatch','not_found','unavailable','response_limit')),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL CHECK(updated_at>=created_at),
 PRIMARY KEY(tenant_id,publisher_id,domain),
 UNIQUE(tenant_id,id),
 FOREIGN KEY(tenant_id,publisher_id) REFERENCES publishers(tenant_id,id),
 CHECK((challenge_id IS NULL AND nonce='' AND challenge_issued_at IS NULL AND challenge_expires_at IS NULL AND NOT challenge_consumed)
 OR (challenge_id IS NOT NULL AND nonce ~ '^[A-Za-z0-9_-]{43}$' AND challenge_issued_at IS NOT NULL AND challenge_expires_at IS NOT NULL AND challenge_expires_at>challenge_issued_at AND challenge_issued_at>=created_at AND challenge_issued_at<=updated_at)),
 CHECK((verified_at IS NULL AND verified_challenge_id IS NULL AND proof_expires_at IS NULL)
 OR (verified_at IS NOT NULL AND verified_challenge_id IS NOT NULL AND proof_expires_at IS NOT NULL AND proof_expires_at>verified_at AND verified_at>=created_at AND verified_at<=updated_at)),
 CHECK((last_check_at IS NULL AND last_check_challenge_id IS NULL AND last_check_outcome='')
 OR (last_check_at IS NOT NULL AND last_check_challenge_id IS NOT NULL AND last_check_outcome<>'' AND last_check_at>=created_at AND last_check_at<=updated_at)),
 CHECK(NOT challenge_consumed OR (challenge_id IS NOT NULL AND verified_challenge_id IS NOT NULL AND challenge_id=verified_challenge_id))
);
ALTER TABLE publisher_domain_verifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE publisher_domain_verifications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON publisher_domain_verifications USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

CREATE FUNCTION guard_publisher_domain_verification() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE declared boolean;
BEGIN
 IF TG_OP='DELETE' THEN
  EXECUTE format('SELECT $3=ANY(domains) FROM %I.publishers WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA)
  INTO declared USING OLD.tenant_id,OLD.publisher_id,OLD.domain;
  IF declared IS DISTINCT FROM false THEN RAISE EXCEPTION 'verification deletion requires domain removal'; END IF;
  RETURN OLD;
 END IF;
 EXECUTE format('SELECT $3=ANY(domains) FROM %I.publishers WHERE tenant_id=$1 AND id=$2',TG_TABLE_SCHEMA)
 INTO declared USING NEW.tenant_id,NEW.publisher_id,NEW.domain;
 IF declared IS DISTINCT FROM true THEN RAISE EXCEPTION 'publisher verification domain is not declared'; END IF;
 IF TG_OP='INSERT' AND NEW.revision<>1 THEN RAISE EXCEPTION 'invalid publisher verification creation'; END IF;
 IF TG_OP='UPDATE' AND (ROW(NEW.id,NEW.tenant_id,NEW.publisher_id,NEW.domain,NEW.created_at) IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.publisher_id,OLD.domain,OLD.created_at)
 OR NEW.revision<>OLD.revision+1 OR NEW.updated_at<OLD.updated_at) THEN RAISE EXCEPTION 'invalid publisher verification transition'; END IF;
 IF TG_OP='UPDATE' AND NEW.challenge_id=OLD.challenge_id AND ROW(NEW.nonce,NEW.challenge_issued_at,NEW.challenge_expires_at) IS DISTINCT FROM ROW(OLD.nonce,OLD.challenge_issued_at,OLD.challenge_expires_at) THEN
 RAISE EXCEPTION 'publisher challenge is immutable'; END IF;
 IF TG_OP='UPDATE' AND NEW.challenge_id=OLD.challenge_id AND OLD.challenge_consumed AND NOT NEW.challenge_consumed THEN RAISE EXCEPTION 'publisher challenge is already consumed'; END IF;
 IF TG_OP='UPDATE' AND NEW.verified_challenge_id=OLD.verified_challenge_id AND ROW(NEW.verified_at,NEW.proof_expires_at) IS DISTINCT FROM ROW(OLD.verified_at,OLD.proof_expires_at) THEN RAISE EXCEPTION 'publisher proof is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER publisher_domain_verification_guard BEFORE INSERT OR UPDATE OR DELETE ON publisher_domain_verifications FOR EACH ROW EXECUTE FUNCTION guard_publisher_domain_verification();

CREATE FUNCTION clear_removed_publisher_domains() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 EXECUTE format('DELETE FROM %I.publisher_domain_verifications WHERE tenant_id=$1 AND publisher_id=$2 AND NOT(domain=ANY($3))',TG_TABLE_SCHEMA)
 USING NEW.tenant_id,NEW.id,NEW.domains;
 RETURN NEW;
END $$;
CREATE TRIGGER publishers_verification_domains AFTER UPDATE ON publishers FOR EACH ROW EXECUTE FUNCTION clear_removed_publisher_domains();
REVOKE ALL ON FUNCTION guard_publisher_domain_verification() FROM PUBLIC;
REVOKE ALL ON FUNCTION clear_removed_publisher_domains() FROM PUBLIC;
