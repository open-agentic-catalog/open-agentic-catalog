CREATE INDEX observations_catalog_search_projection_missing
  ON observations (tenant_id,id)
  WHERE normalized_entry_digest IS NOT NULL
    AND (search_display_name IS NULL OR search_description IS NULL OR search_tags IS NULL
      OR search_representative_queries IS NULL OR search_publisher IS NULL OR search_vector IS NULL);

COMMENT ON INDEX observations_catalog_search_projection_missing IS 'Sparse restore and upgrade repair inventory for normalized observations whose rebuildable Catalog search projection is incomplete.';
