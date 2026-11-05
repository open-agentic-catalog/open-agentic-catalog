-- Exact administration filters precede permission selection and page limits.
-- Retain bytewise display-name and slug ordering, including duplicate names.
CREATE INDEX namespaces_display_name_page
  ON namespaces (tenant_id,(display_name COLLATE "C"),(slug COLLATE "C"),id);
