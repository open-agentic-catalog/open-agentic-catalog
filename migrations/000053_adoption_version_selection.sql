-- Bound selected adoption lookups before projecting retained native evidence.
-- No runtime privileges change: these are indexes on existing retained columns.
CREATE INDEX resource_version_revisions_adoption_source
  ON resource_version_revisions (tenant_id, (adoption_provenance->>'sourceId'), resource_version_id)
  WHERE adoption_provenance IS NOT NULL;
