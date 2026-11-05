ALTER TABLE publishers ADD COLUMN approved_publication_domains text[] NOT NULL DEFAULT '{}';

UPDATE publishers
   SET approved_publication_domains = verified_domains;

ALTER TABLE publishers
  ADD CONSTRAINT publishers_approved_publication_domains_declared
  CHECK (approved_publication_domains <@ domains);

COMMENT ON COLUMN publishers.verified_domains IS 'Legacy rolling-compatibility storage. Remove only in a later contract migration after the supported mixed-version interval.';
COMMENT ON COLUMN publishers.approved_publication_domains IS 'Tenant-administrator-approved publication scope; not evidence of external publisher authority or domain control.';
