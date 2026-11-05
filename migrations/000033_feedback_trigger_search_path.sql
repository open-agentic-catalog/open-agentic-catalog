-- Pin built-in resolution; tenant tables are always explicitly schema-qualified.
ALTER FUNCTION catalog_feedback_guard() SET search_path TO pg_catalog;
