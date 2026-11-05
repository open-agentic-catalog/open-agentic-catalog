-- Bounded parent-scoped authoring histories and the tenant review inbox.
-- Additive indexes only; existing column-level runtime privileges suffice.
CREATE INDEX resource_versions_history ON resource_versions (tenant_id, resource_id, id);
CREATE INDEX resource_version_revisions_history ON resource_version_revisions (tenant_id, resource_version_id, id);
CREATE INDEX catalog_listings_history ON catalog_listings (tenant_id, resource_id, id);
CREATE INDEX listing_revisions_history ON listing_revisions (tenant_id, listing_id, id);
CREATE INDEX review_submissions_history ON review_submissions (tenant_id, listing_id, id);
CREATE INDEX review_submissions_inbox ON review_submissions (tenant_id, state, id);
CREATE INDEX listing_releases_history ON listing_releases (tenant_id, listing_id, id);
