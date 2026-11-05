// Exact manifests define column-level runtime access and extension exceptions;
// reconciliation rejects excess authority as well as omissions.

package database

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// The exact manifest includes conditional derived-semantic privileges without
// requiring optional tables on core installations. Generated lexical fields stay
// read-only, feedback remains API-only and vectors cannot be rewritten. Outbox
// causal context stays worker-readable; notification-counter writes remain
// confined to fixed owner-authorized triggers, never runtime DML or EXECUTE.
const runtimePrivilegeManifestVersion = 27

// RuntimePrivilegeManifestVersion identifies the exact role grant contract
// embedded in this binary for bounded operator diagnostics.
func RuntimePrivilegeManifestVersion() int { return runtimePrivilegeManifestVersion }

const (
	runtimeCryptoExtension          = "pgcrypto"
	runtimeCryptoByteDigestFunction = "digest(bytea, text)"
	runtimeCryptoTextDigestFunction = "digest(text, text)"
)

type tablePrivilege struct {
	selectColumns []string
	insertColumns []string
	updateColumns []string
	deleteRows    bool
}

type runtimePrivilegeManifest struct {
	tables    map[string]tablePrivilege
	functions []string
}

type extensionFunctionPrivilege struct {
	extension     string
	schema        string
	name          string
	arguments     string
	signature     string
	publicExecute bool
	roleExecute   bool
	roleGrantable bool
}

// RuntimePrivilegeReport is a bounded, secret-free description of one
// application role's effective privileges in the active Catalog schema.
type RuntimePrivilegeReport struct {
	ManifestVersion int      `json:"manifestVersion"`
	Schema          string   `json:"schema"`
	RuntimeRole     string   `json:"runtimeRole"`
	DatabaseRole    string   `json:"databaseRole"`
	Compliant       bool     `json:"compliant"`
	Differences     []string `json:"differences,omitempty"`
}

const maxPrivilegeDifferences = 50

const maxPostgresRoleNameBytes = 63

var catalogTableNames = []string{
	"access_group_members",
	"access_groups",
	"artifact_references",
	"audit_events",
	"catalog_listings",
	"catalog_schema_migrations",
	"catalog_search_generations",
	"document_blobs",
	"evidence_sets",
	"federation_source_audiences",
	"federation_source_referrals",
	"federation_source_revisions",
	"federation_sources",
	"idempotency_keys",
	"item_feedback",
	"item_rating_revisions",
	"item_ratings",
	"jobs",
	"listing_releases",
	"listing_revisions",
	"namespace_memberships",
	"namespaces",
	"observation_occurrences",
	"observations",
	"outbox_events",
	"personal_items",
	"publication_snapshots",
	"publisher_domain_verifications",
	"publishers",
	"rating_sharing",
	"rating_targets",
	"representations",
	"resource_version_revisions",
	"resource_versions",
	"resources",
	"review_submissions",
	"role_assignments",
	"role_definitions",
	"role_permissions",
	"skill_file_inventory",
	"source_artwork_heads",
	"source_artwork_revisions",
	"source_change_previews",
	"source_inspections",
	"source_maintenance_state",
	"source_retrieval_attempts",
	"source_retrieval_documents",
	"source_retrieval_states",
	"source_snapshots",
	"tenant_authorization_state",
	"tenant_identity_realms",
	"tenant_memberships",
	"tenant_settings",
	"tenants",
	"version_validation_runs",
	"webhook_deliveries",
	"webhook_endpoints",
	"webhook_notification_state",
}

// catalogTableColumns is the reviewed schema inventory used to make runtime
// reads column-explicit. A migration that changes a table must update this
// inventory deliberately before reconciliation can grant access to the new
// column; PostgreSQL otherwise leaves it unavailable to both runtime roles.
var catalogTableColumns = map[string][]string{
	"personal_items":                 {"tenant_id", "membership_id", "realm_id", "id", "input", "revision", "created_at", "updated_at"},
	"tenant_settings":                {"tenant_id", "revision", "automatic_source_sync_enabled", "automatic_source_sync_interval_ns", "updated_at", "new_job_max_attempts"},
	"rating_targets":                 {"tenant_id", "target_key", "target", "resource_id", "version_revision_id", "source_id", "vote_count", "score_sum", "shared_count", "shared_sum"},
	"item_ratings":                   {"tenant_id", "id", "target_key", "principal_key", "submitted_by", "score", "consent", "state", "release_id", "entry_digest", "revision", "created_at", "updated_at"},
	"item_rating_revisions":          {"tenant_id", "rating_id", "revision", "score", "consent", "state", "entry_digest", "recorded_at"},
	"rating_sharing":                 {"tenant_id", "id", "host", "audience", "enabled", "revision", "updated_at"},
	"source_artwork_heads":           {"tenant_id", "source_id", "presentation_generation", "target_key", "revision", "current_revision_id"},
	"source_artwork_revisions":       {"tenant_id", "id", "source_id", "presentation_generation", "target_key", "identifier", "version_present", "version", "kind", "revision", "document", "digest", "created_by", "created_at"},
	"access_groups":                  {"id", "tenant_id", "realm_id", "name", "description", "state", "revision", "created_at", "updated_at"},
	"access_group_members":           {"id", "tenant_id", "realm_id", "group_id", "membership_id", "state", "revision", "added_by", "add_reason", "added_at", "revoked_by", "revoke_reason", "revoked_at"},
	"role_definitions":               {"id", "tenant_id", "key", "builtin", "state", "revision", "name", "description", "created_at", "updated_at"},
	"role_permissions":               {"tenant_id", "role_definition_id", "permission"},
	"tenant_authorization_state":     {"tenant_id", "revision"},
	"item_feedback":                  {"id", "tenant_id", "membership_id", "resource_id", "release_id", "version_revision_id", "category", "comment", "response", "state", "revision", "created_at", "updated_at"},
	"webhook_notification_state":     {"tenant_id", "admission_revision", "capacity_loss_count", "last_capacity_loss_at", "delivery_loss_count", "last_delivery_loss_at"},
	"artifact_references":            {"tenant_id", "representation_id", "reference_kind", "git_repository_url", "git_commit", "git_directory", "oci_reference"},
	"audit_events":                   {"id", "tenant_id", "actor", "action", "target_type", "target_id", "reason", "request_id", "before_digest", "after_digest", "details", "occurred_at", "format_version"},
	"catalog_listings":               {"id", "tenant_id", "namespace_id", "resource_id", "candidate_revision_id", "current_release_id", "revision", "created_at", "updated_at"},
	"catalog_schema_migrations":      {"version", "checksum", "applied_at"},
	"catalog_search_generations":     {"tenant_id", "generation"},
	"document_blobs":                 {"tenant_id", "id", "media_type", "content_digest", "size_bytes", "content", "created_at"},
	"evidence_sets":                  {"id", "tenant_id", "subject_digest", "digest", "state", "findings", "validation_profile", "policy_version", "rights", "trust", "created_at", "external_retrievals"},
	"federation_source_audiences":    {"tenant_id", "source_id", "source_revision", "discovery_host", "audience"},
	"federation_source_referrals":    {"tenant_id", "source_id", "source_revision", "target_source_id"},
	"federation_source_revisions":    {"tenant_id", "source_id", "revision", "retrieval_mode", "policy_id", "policy_revision", "policy_digest", "credential_revision", "configuration_digest", "configuration", "recorded_at", "update_mode", "registry_list_url", "inspection_id"},
	"federation_sources":             {"id", "tenant_id", "display_name", "source_type", "endpoint", "status", "revision", "configuration", "last_complete_snapshot_id", "last_sync_at", "created_by", "created_at", "updated_at", "last_error"},
	"idempotency_keys":               {"tenant_id", "actor", "route", "idempotency_key", "request_digest", "expires_at", "created_at", "reservation_id", "protocol_version", "authorization_digest", "execution_token", "started_at", "completed_at", "atomic_response_status", "atomic_response_headers", "atomic_response_body", "released_at", "repair_reason", "replay_authorization"},
	"jobs":                           {"id", "tenant_id", "kind", "actor", "parent_job_id", "payload_version", "payload", "state", "priority", "progress", "not_before", "attempt", "attempt_limit", "lease_owner", "lease_expires_at", "cancellation_requested", "deduplication_key", "result", "safe_error", "failure_class", "created_at", "updated_at", "cleanup_after", "causal_trace"},
	"listing_releases":               {"id", "tenant_id", "listing_id", "submission_id", "version_revision_id", "listing_revision_id", "state", "replacement_release_id", "disposition_reason", "sunset_at", "activated_by", "revision", "activated_at", "updated_at"},
	"listing_revisions":              {"id", "tenant_id", "listing_id", "revision_number", "version_revision_id", "display_name", "description", "tags", "categories", "representative_queries", "publisher_id", "documentation_url", "support_url", "license", "terms_url", "visibility", "relationships", "extensions", "digest", "created_by", "created_at", "search_display_vector", "search_description_vector", "search_tags_vector", "search_queries_vector", "search_sort_name"},
	"namespace_memberships":          {"id", "tenant_id", "membership_id", "namespace_id", "state", "revision", "created_by", "created_at", "revoked_by", "revoke_reason", "revoked_at"},
	"namespaces":                     {"id", "tenant_id", "slug", "display_name", "description", "revision", "created_at", "updated_at"},
	"observation_occurrences":        {"tenant_id", "observation_id", "source_id", "snapshot_id", "source_revision", "retrieval_digest", "entry_location", "exact_entry_digest", "normalized_entry_digest", "evidence_complete", "observed_at", "normalization_input_digest"},
	"observations":                   {"id", "tenant_id", "source_id", "snapshot_id", "source_key", "identifier", "version", "version_present", "kind", "media_type", "completeness", "compatibility", "semantic_digest", "freshness", "first_seen_at", "last_seen_at", "exact_entry_digest", "normalized_entry_digest", "normalization_profile", "normalized_entry_bytes", "raw_entry_bytes", "search_display_name", "search_description", "search_tags", "search_representative_queries", "search_publisher", "search_vector", "identity_digest", "search_vector_version", "search_identifier_vector", "search_display_vector", "search_description_vector", "search_tags_vector", "search_queries_vector", "artwork_metadata", "ratings_metadata", "search_license", "search_categories", "search_filter_version"},
	"outbox_events":                  {"id", "tenant_id", "event_type", "subject", "data", "created_at", "published_at", "lease_owner", "lease_expires_at", "is_internal", "cleanup_after", "dispatch_after", "causal_trace"},
	"publication_snapshots":          {"id", "tenant_id", "host", "document_family", "profile_id", "generation", "state", "etag", "document", "created_at", "activated_at"},
	"publisher_domain_verifications": {"id", "tenant_id", "publisher_id", "domain", "revision", "challenge_id", "nonce", "challenge_issued_at", "challenge_expires_at", "challenge_consumed", "verified_challenge_id", "verified_at", "proof_expires_at", "last_check_at", "last_check_challenge_id", "last_check_outcome", "created_at", "updated_at"},
	"publishers":                     {"id", "tenant_id", "display_name", "domains", "revision", "created_at", "updated_at", "approved_publication_domains"},
	"representations":                {"id", "tenant_id", "version_revision_id", "role", "media_type", "profile_id", "external_url", "inline_document", "content_digest", "size_bytes", "validation_state", "findings", "normalized_facts", "unknown_properties", "document_blob_id", "external_reference_only", "skill_package_structure_state", "skill_package_root", "skill_package_expanded_size", "skill_package_file_count", "skill_package_malware_state", "skill_package_secrets_state", "skill_package_license_state", "skill_package_sbom_state", "skill_package_provenance_state", "plugin_inspection"},
	"resource_version_revisions":     {"id", "tenant_id", "resource_version_id", "revision_number", "content_set_digest", "findings", "evidence_set_id", "created_by", "created_at", "adoption_provenance"},
	"resource_versions":              {"id", "tenant_id", "resource_id", "version", "candidate_revision_id", "sealed_revision_id", "revision", "created_at", "updated_at"},
	"resources":                      {"id", "tenant_id", "namespace_id", "canonical_identifier", "kind", "created_by", "revision", "created_at", "updated_at", "deleted_by", "delete_reason", "deleted_at", "search_identifier_vector"},
	"review_submissions":             {"id", "tenant_id", "listing_id", "version_revision_id", "listing_revision_id", "evidence_set_id", "evidence_digest", "policy_version", "state", "submitted_by", "decisions", "revision", "submitted_at", "decided_at"},
	"role_assignments":               {"id", "tenant_id", "membership_id", "namespace_id", "role", "state", "revision", "granted_by", "grant_reason", "granted_at", "revoked_by", "revoke_reason", "revoked_at", "role_definition_id", "group_id", "realm_id"},
	"skill_file_inventory":           {"tenant_id", "representation_id", "path", "size_bytes", "media_type", "content_digest", "executable"},
	"source_change_previews":         {"id", "tenant_id", "source_id", "digest", "document", "expires_at", "created_at"},
	"source_inspections":             {"id", "tenant_id", "document", "expires_at", "created_at"},
	"source_maintenance_state":       {"tenant_id", "source_id", "source_revision", "last_admitted_at"},
	"source_retrieval_attempts":      {"tenant_id", "id", "source_id", "source_revision", "snapshot_id", "request_key", "requested_url_digest", "final_url_digest", "profile_id", "status", "received_body", "reused", "representation_digest", "etag_digest", "last_modified_digest", "fetched_at"},
	"source_retrieval_documents":     {"tenant_id", "source_id", "source_revision", "representation_digest", "body", "created_at"},
	"source_retrieval_states":        {"tenant_id", "source_id", "source_revision", "request_key", "requested_url", "final_url", "request_identity_digest", "profile_id", "media_type", "policy_digest", "credential_revision", "representation_digest", "etag", "last_modified", "vary_names", "vary_selection_digest", "list_terminal", "updated_at"},
	"source_snapshots":               {"id", "tenant_id", "source_id", "state", "enumeration_complete", "withdrawal_authority", "member_count", "body_digest", "details", "started_at", "completed_at", "source_revision"},
	"tenant_identity_realms":         {"id", "tenant_id", "issuer", "tenancy_mode", "external_organization_key", "state", "revision", "created_at", "updated_at"},
	"tenant_memberships":             {"id", "tenant_id", "realm_id", "subject", "actor_type", "state", "revision", "created_at", "updated_at"},
	"tenants":                        {"id", "display_name", "created_at"},
	"version_validation_runs":        {"tenant_id", "version_revision_id", "run_number", "evidence_set_id", "expires_at", "created_at"},
	"webhook_deliveries":             {"id", "tenant_id", "endpoint_id", "event_id", "event_type", "state", "attempt", "response_status", "safe_error", "next_attempt_at", "created_at", "updated_at", "cleanup_after"},
	"webhook_endpoints":              {"id", "tenant_id", "name", "url", "event_types", "status", "revision", "secret_version", "encrypted_secret", "created_by", "created_at", "updated_at"},
}

var catalogFunctionNames = []string{
	"admit_webhook_notification()",
	"advance_catalog_search_generation()",
	"advance_tenant_authorization()",
	"bind_role_assignment_definition()",
	"catalog_control_term_v1(text)",
	"catalog_core_lexical_array_vector_v1(text[])",
	"catalog_core_lexical_query_v1(text)",
	"catalog_core_lexical_terms_v1(text)",
	"catalog_core_lexical_vector_v1(text)",
	"catalog_core_sort_name_v1(text)",
	"catalog_observation_categories_v1(jsonb)",
	"catalog_observation_license_v1(jsonb)",
	"clear_removed_publisher_domains()",
	"enforce_catalog_current_release_state()",
	"enforce_source_complete_snapshot()",
	"fence_tenant_authorization()",
	"guard_access_group()",
	"guard_access_group_member()",
	"guard_audit_event_insert()",
	"guard_authorization_history()",
	"guard_builtin_permission_insert()",
	"guard_catalog_listing_lifecycle()",
	"guard_idempotency_key_delete()",
	"guard_idempotency_key_update()",
	"guard_item_rating()",
	"guard_job_delete()",
	"guard_job_lifecycle()",
	"guard_listing_release_lifecycle()",
	"guard_namespace_lifecycle()",
	"guard_namespace_membership_lifecycle()",
	"guard_observation_lifecycle()",
	"guard_outbox_event_delete()",
	"guard_outbox_event_lifecycle()",
	"guard_personal_item()",
	"guard_private_trace_context()",
	"guard_publication_snapshot_lifecycle()",
	"guard_publisher_domain_verification()",
	"guard_publisher_lifecycle()",
	"guard_rating_sharing()",
	"guard_rating_target()",
	"guard_representation_history()",
	"guard_resource_lifecycle()",
	"guard_resource_version_lifecycle()",
	"guard_review_submission_lifecycle()",
	"guard_role_assignment_lifecycle()",
	"guard_source_artwork_head()",
	"guard_source_change_preview_history()",
	"guard_source_inspection_history()",
	"guard_source_lifecycle()",
	"guard_source_maintenance_lifecycle()",
	"guard_source_maintenance_state_delete()",
	"guard_source_presentation_generation()",
	"guard_source_retrieval_state_lifecycle()",
	"guard_tenant_identity_realm_lifecycle()",
	"guard_tenant_membership_lifecycle()",
	"guard_tenant_settings()",
	"guard_webhook_delivery_lifecycle()",
	"guard_webhook_endpoint_lifecycle()",
	"guard_webhook_notification_state()",
	"initialize_tenant_authorization()",
	"project_observation_catalog_search()",
	"project_observation_control_filters()",
	"record_item_rating()",
	"record_webhook_delivery_loss()",
	"reject_catalog_immutable_history_mutation()",
	"reject_federation_source_history_mutation()",
	"reject_version_validation_run_mutation()",
	"validate_role_permission_set()",
}

// apiPrivilegeManifest constructs the reviewed column-explicit API authority,
// including authoring and activation but excluding worker lease and cleanup
// authority.
func apiPrivilegeManifest() runtimePrivilegeManifest {
	manifest := runtimePrivilegeManifest{tables: map[string]tablePrivilege{}, functions: []string{
		"catalog_control_term_v1(text)",
		"catalog_observation_license_v1(jsonb)",
		"catalog_observation_categories_v1(jsonb)",
		"catalog_core_lexical_array_vector_v1(text[])",
		"catalog_core_lexical_query_v1(text)",
		"catalog_core_lexical_terms_v1(text)",
		"catalog_core_lexical_vector_v1(text)",
		"catalog_core_sort_name_v1(text)",
	}}
	manifest.tables["personal_items"] = tablePrivilege{selectColumns: catalogTableColumns["personal_items"], insertColumns: catalogTableColumns["personal_items"], updateColumns: []string{"input", "revision", "updated_at"}, deleteRows: true}
	manifest.tables["item_feedback"] = tablePrivilege{selectColumns: catalogTableColumns["item_feedback"], insertColumns: catalogTableColumns["item_feedback"], updateColumns: []string{"category", "comment", "response", "state", "revision", "updated_at"}}
	for _, table := range []string{
		"tenant_settings", "rating_targets", "rating_sharing", "item_ratings", "item_rating_revisions",
		"access_groups", "access_group_members",
		"artifact_references", "audit_events", "catalog_listings", "catalog_search_generations", "document_blobs",
		"evidence_sets", "federation_source_audiences", "federation_source_referrals", "federation_source_revisions",
		"federation_sources", "idempotency_keys", "jobs", "listing_releases", "listing_revisions",
		"namespace_memberships", "namespaces", "observation_occurrences", "observations", "publication_snapshots",
		"publishers", "representations", "resource_version_revisions", "resource_versions", "resources",
		"source_artwork_heads", "source_artwork_revisions", "review_submissions", "role_assignments", "role_definitions", "role_permissions", "tenant_authorization_state", "skill_file_inventory", "source_change_previews", "source_inspections",
		"source_snapshots", "tenant_identity_realms", "tenant_memberships", "tenants", "version_validation_runs",
		"webhook_deliveries", "webhook_endpoints",
		"webhook_notification_state",
	} {
		privilege := manifest.tables[table]
		privilege.selectColumns = append([]string(nil), catalogTableColumns[table]...)
		manifest.tables[table] = privilege
	}
	for table, columns := range map[string][]string{
		"federation_source_revisions": columnsExcept(catalogTableColumns["federation_source_revisions"], "inspection_id"),
		"jobs":                        columnsExcept(catalogTableColumns["jobs"], "cleanup_after"),
		"webhook_deliveries":          columnsExcept(catalogTableColumns["webhook_deliveries"], "cleanup_after"),
		"outbox_events":               {"tenant_id", "is_internal", "published_at"},
	} {
		privilege := manifest.tables[table]
		privilege.selectColumns = columns
		manifest.tables[table] = privilege
	}
	setInsert := func(table string, columns ...string) {
		privilege := manifest.tables[table]
		privilege.insertColumns = columns
		manifest.tables[table] = privilege
	}
	setUpdate := func(table string, columns ...string) {
		privilege := manifest.tables[table]
		privilege.updateColumns = columns
		manifest.tables[table] = privilege
	}
	// Identity-only UPDATE permits row locking; the trigger forbids identity changes.
	setUpdate("rating_targets", "target_key")
	setInsert("rating_targets", "tenant_id", "target_key", "target", "resource_id", "version_revision_id", "source_id")
	setInsert("item_ratings", catalogTableColumns["item_ratings"]...)
	setUpdate("item_ratings", "submitted_by", "score", "consent", "state", "release_id", "entry_digest", "revision", "updated_at")
	setInsert("tenant_settings", catalogTableColumns["tenant_settings"]...)
	setUpdate("tenant_settings", "revision", "automatic_source_sync_enabled", "automatic_source_sync_interval_ns", "updated_at", "new_job_max_attempts")
	setInsert("rating_sharing", catalogTableColumns["rating_sharing"]...)
	setUpdate("rating_sharing", "enabled", "revision", "updated_at")
	setInsert("namespaces", "id", "tenant_id", "slug", "display_name", "description", "revision", "created_at", "updated_at")
	setInsert("publishers", "id", "tenant_id", "display_name", "domains", "approved_publication_domains", "revision", "created_at", "updated_at")
	setInsert("resources", "id", "tenant_id", "namespace_id", "canonical_identifier", "kind", "created_by", "revision", "created_at", "updated_at")
	setInsert("resource_versions", "id", "tenant_id", "resource_id", "version", "candidate_revision_id", "revision", "created_at", "updated_at")
	setInsert("evidence_sets", "id", "tenant_id", "subject_digest", "digest", "state", "findings", "validation_profile", "policy_version", "rights", "trust", "external_retrievals", "created_at")
	setInsert("resource_version_revisions", "id", "tenant_id", "resource_version_id", "revision_number", "content_set_digest", "findings", "evidence_set_id", "adoption_provenance", "created_by", "created_at")
	setInsert("representations", "id", "tenant_id", "version_revision_id", "role", "media_type", "profile_id", "external_url", "inline_document", "content_digest", "size_bytes", "document_blob_id", "validation_state", "findings", "normalized_facts", "unknown_properties", "external_reference_only", "skill_package_structure_state", "skill_package_root", "skill_package_expanded_size", "skill_package_file_count", "skill_package_malware_state", "skill_package_secrets_state", "skill_package_license_state", "skill_package_sbom_state", "skill_package_provenance_state", "plugin_inspection")
	setInsert("document_blobs", "tenant_id", "id", "media_type", "content_digest", "size_bytes", "content", "created_at")
	setInsert("artifact_references", "tenant_id", "representation_id", "reference_kind", "git_repository_url", "git_commit", "git_directory", "oci_reference")
	setInsert("skill_file_inventory", "tenant_id", "representation_id", "path", "size_bytes", "media_type", "content_digest", "executable")
	setInsert("catalog_listings", "id", "tenant_id", "namespace_id", "resource_id", "candidate_revision_id", "revision", "created_at", "updated_at")
	setInsert("listing_revisions", "id", "tenant_id", "listing_id", "revision_number", "version_revision_id", "display_name", "description", "tags", "categories", "representative_queries", "publisher_id", "documentation_url", "support_url", "license", "terms_url", "visibility", "relationships", "extensions", "digest", "created_by", "created_at")
	setInsert("review_submissions", "id", "tenant_id", "listing_id", "version_revision_id", "listing_revision_id", "evidence_set_id", "evidence_digest", "policy_version", "state", "submitted_by", "decisions", "revision", "submitted_at")
	setInsert("listing_releases", "id", "tenant_id", "listing_id", "submission_id", "version_revision_id", "listing_revision_id", "state", "disposition_reason", "activated_by", "revision", "activated_at", "updated_at")
	setInsert("audit_events", "format_version", "id", "tenant_id", "actor", "action", "target_type", "target_id", "reason", "request_id", "before_digest", "after_digest", "details", "occurred_at")
	setInsert("outbox_events", "causal_trace", "id", "tenant_id", "event_type", "subject", "data", "is_internal", "created_at", "published_at")
	setInsert("webhook_endpoints", "id", "tenant_id", "name", "url", "event_types", "status", "revision", "secret_version", "encrypted_secret", "created_by", "created_at", "updated_at")
	setInsert("webhook_deliveries", "id", "tenant_id", "endpoint_id", "event_id", "event_type", "state", "attempt", "response_status", "safe_error", "next_attempt_at", "created_at", "updated_at")
	setInsert("idempotency_keys", "tenant_id", "actor", "route", "idempotency_key", "request_digest", "expires_at", "created_at", "protocol_version", "authorization_digest", "execution_token", "started_at")
	setInsert("jobs", "causal_trace", "id", "tenant_id", "kind", "actor", "parent_job_id", "payload_version", "payload", "state", "priority", "progress", "not_before", "attempt", "attempt_limit", "deduplication_key", "created_at", "updated_at")
	setInsert("federation_sources", "id", "tenant_id", "display_name", "source_type", "endpoint", "status", "revision", "configuration", "created_by", "created_at", "updated_at")
	setInsert("source_artwork_heads", catalogTableColumns["source_artwork_heads"]...)
	setInsert("source_artwork_revisions", catalogTableColumns["source_artwork_revisions"]...)
	setUpdate("source_artwork_heads", "revision", "current_revision_id")
	setInsert("source_inspections", "id", "tenant_id", "document", "expires_at", "created_at")
	setInsert("source_change_previews", "id", "tenant_id", "source_id", "digest", "document", "expires_at", "created_at")
	setInsert("federation_source_revisions", "tenant_id", "source_id", "revision", "retrieval_mode", "update_mode", "registry_list_url", "policy_id", "policy_revision", "policy_digest", "credential_revision", "configuration_digest", "configuration", "recorded_at", "inspection_id")
	setInsert("federation_source_audiences", "tenant_id", "source_id", "source_revision", "discovery_host", "audience")
	setInsert("federation_source_referrals", "tenant_id", "source_id", "source_revision", "target_source_id")
	setInsert("publication_snapshots", "id", "tenant_id", "host", "document_family", "profile_id", "generation", "state", "etag", "document", "created_at", "activated_at")
	setInsert("tenant_memberships", "id", "tenant_id", "realm_id", "subject", "actor_type", "state", "revision", "created_at", "updated_at")
	setInsert("namespace_memberships", "id", "tenant_id", "membership_id", "namespace_id", "state", "revision", "created_by", "created_at")
	setInsert("role_assignments", "id", "tenant_id", "membership_id", "namespace_id", "role", "state", "revision", "granted_by", "grant_reason", "granted_at", "role_definition_id", "group_id", "realm_id")
	setInsert("role_definitions", "id", "tenant_id", "name", "description", "created_at", "updated_at")
	setInsert("role_permissions", "tenant_id", "role_definition_id", "permission")
	setInsert("access_groups", "id", "tenant_id", "realm_id", "name", "description", "created_at", "updated_at")
	setInsert("access_group_members", "id", "tenant_id", "realm_id", "group_id", "membership_id", "added_by", "add_reason", "added_at")
	setInsert("version_validation_runs", "tenant_id", "version_revision_id", "run_number", "evidence_set_id", "expires_at", "created_at")
	setUpdate("namespaces", "display_name", "description", "revision", "updated_at")
	setUpdate("publishers", "display_name", "domains", "approved_publication_domains", "revision", "updated_at")
	setUpdate("resources", "revision", "updated_at", "deleted_by", "delete_reason", "deleted_at")
	setUpdate("resource_versions", "candidate_revision_id", "sealed_revision_id", "revision", "updated_at")
	setUpdate("catalog_listings", "candidate_revision_id", "current_release_id", "revision", "updated_at")
	setUpdate("review_submissions", "state", "decisions", "revision", "decided_at")
	setUpdate("listing_releases", "state", "replacement_release_id", "disposition_reason", "sunset_at", "revision", "updated_at")
	setUpdate("webhook_endpoints", "name", "url", "event_types", "status", "revision", "secret_version", "encrypted_secret", "updated_at")
	setUpdate("idempotency_keys", "request_digest", "expires_at", "created_at", "protocol_version", "authorization_digest", "reservation_id", "execution_token", "started_at", "completed_at", "atomic_response_status", "atomic_response_headers", "atomic_response_body", "released_at", "repair_reason", "replay_authorization")
	setUpdate("jobs", "state", "cancellation_requested", "updated_at")
	setUpdate("federation_sources", "display_name", "source_type", "endpoint", "status", "revision", "configuration", "updated_at")
	setUpdate("observations", "freshness")
	setUpdate("publication_snapshots", "state", "document", "activated_at")
	setUpdate("tenant_memberships", "state", "revision", "updated_at")
	setUpdate("namespace_memberships", "state", "revision", "created_by", "created_at", "revoked_by", "revoke_reason", "revoked_at")
	setUpdate("tenant_authorization_state", "revision")
	setUpdate("role_definitions", "name", "description", "state", "revision", "updated_at")
	setUpdate("access_groups", "name", "description", "state", "revision", "updated_at")
	setUpdate("access_group_members", "state", "revision", "revoked_by", "revoke_reason", "revoked_at")
	setUpdate("role_assignments", "state", "revision", "revoked_by", "revoke_reason", "revoked_at")
	setUpdate("webhook_deliveries", "state", "attempt", "response_status", "safe_error", "next_attempt_at", "updated_at")
	// PostgreSQL row-locking clauses require UPDATE on at least one column.
	// The history guard rejects every actual inspection update; this grant only
	// lets source creation hold the row while it records the immutable use link.
	setUpdate("source_inspections", "id")
	idempotency := manifest.tables["idempotency_keys"]
	idempotency.deleteRows = true
	manifest.tables["idempotency_keys"] = idempotency
	permissions := manifest.tables["role_permissions"]
	permissions.deleteRows = true
	manifest.tables["role_permissions"] = permissions

	verification := tablePrivilege{selectColumns: append([]string(nil), catalogTableColumns["publisher_domain_verifications"]...), insertColumns: append([]string(nil), catalogTableColumns["publisher_domain_verifications"]...), updateColumns: columnsExcept(catalogTableColumns["publisher_domain_verifications"], "id", "tenant_id", "publisher_id", "domain", "created_at"), deleteRows: true}
	manifest.tables["publisher_domain_verifications"] = verification
	return addSemanticPrivileges(manifest, false)
}

// workerPrivilegeManifest constructs the reviewed worker authority for durable
// jobs, observations, outbox and cleanup without granting publication activation
// or domain authoring updates.
func workerPrivilegeManifest() runtimePrivilegeManifest {
	manifest := runtimePrivilegeManifest{tables: map[string]tablePrivilege{}, functions: []string{
		"catalog_control_term_v1(text)",
		"catalog_observation_license_v1(jsonb)",
		"catalog_observation_categories_v1(jsonb)",
		"catalog_core_lexical_array_vector_v1(text[])",
		"catalog_core_lexical_terms_v1(text)",
		"catalog_core_lexical_vector_v1(text)",
	}}
	for _, table := range []string{
		"tenant_settings", "rating_targets", "rating_sharing", "artifact_references", "catalog_listings", "catalog_search_generations", "document_blobs", "evidence_sets", "federation_source_audiences",
		"federation_source_referrals", "federation_source_revisions", "federation_sources", "jobs", "listing_releases",
		"listing_revisions", "observation_occurrences", "observations", "outbox_events", "publication_snapshots", "publishers",
		"representations", "resource_version_revisions", "resource_versions", "resources", "skill_file_inventory",
		"source_maintenance_state", "source_retrieval_attempts", "source_retrieval_documents", "source_retrieval_states",
		"source_snapshots", "version_validation_runs", "webhook_deliveries", "webhook_endpoints",
		"webhook_notification_state",
	} {
		privilege := manifest.tables[table]
		privilege.selectColumns = append([]string(nil), catalogTableColumns[table]...)
		manifest.tables[table] = privilege
	}
	for table, excluded := range map[string][]string{
		"resources":         {"search_identifier_vector"},
		"observations":      {"search_license", "search_categories", "search_identifier_vector", "search_display_vector", "search_description_vector", "search_tags_vector", "search_queries_vector"},
		"listing_revisions": {"search_display_vector", "search_description_vector", "search_tags_vector", "search_queries_vector", "search_sort_name"},
	} {
		privilege := manifest.tables[table]
		privilege.selectColumns = columnsExcept(privilege.selectColumns, excluded...)
		manifest.tables[table] = privilege
	}
	for table, columns := range map[string][]string{
		"idempotency_keys":       {"tenant_id", "reservation_id", "protocol_version", "expires_at", "atomic_response_status", "released_at"},
		"source_change_previews": {"id", "tenant_id", "expires_at"},
		"source_inspections":     {"id", "tenant_id", "expires_at"},
	} {
		privilege := manifest.tables[table]
		privilege.selectColumns = columns
		manifest.tables[table] = privilege
	}
	setInsert := func(table string, columns ...string) {
		privilege := manifest.tables[table]
		privilege.insertColumns = columns
		manifest.tables[table] = privilege
	}
	setUpdate := func(table string, columns ...string) {
		privilege := manifest.tables[table]
		privilege.updateColumns = columns
		manifest.tables[table] = privilege
	}
	setInsert("jobs", "causal_trace", "id", "tenant_id", "kind", "actor", "parent_job_id", "payload_version", "payload", "state", "priority", "progress", "not_before", "attempt", "attempt_limit", "deduplication_key", "created_at", "updated_at")
	setInsert("audit_events", "format_version", "id", "tenant_id", "actor", "action", "target_type", "target_id", "reason", "request_id", "before_digest", "after_digest", "details", "occurred_at")
	setInsert("outbox_events", "causal_trace", "id", "tenant_id", "event_type", "subject", "data", "is_internal", "created_at", "published_at")
	setInsert("source_maintenance_state", "tenant_id", "source_id", "source_revision", "last_admitted_at")
	setInsert("source_snapshots", "id", "tenant_id", "source_id", "source_revision", "state", "enumeration_complete", "withdrawal_authority", "member_count", "body_digest", "details", "started_at", "completed_at")
	setInsert("observations", "artwork_metadata", "ratings_metadata", "id", "tenant_id", "source_id", "snapshot_id", "source_key", "identifier", "version", "version_present", "kind", "media_type", "completeness", "compatibility", "semantic_digest", "identity_digest", "exact_entry_digest", "normalized_entry_digest", "normalization_profile", "raw_entry_bytes", "normalized_entry_bytes", "freshness", "first_seen_at", "last_seen_at", "search_display_name", "search_description", "search_tags", "search_representative_queries", "search_publisher", "search_vector")
	setInsert("observation_occurrences", "tenant_id", "observation_id", "source_id", "snapshot_id", "source_revision", "retrieval_digest", "entry_location", "exact_entry_digest", "normalized_entry_digest", "evidence_complete", "observed_at", "normalization_input_digest")
	setInsert("source_retrieval_documents", "tenant_id", "source_id", "source_revision", "representation_digest", "body", "created_at")
	setInsert("source_retrieval_states", "tenant_id", "source_id", "source_revision", "request_key", "requested_url", "final_url", "request_identity_digest", "profile_id", "media_type", "policy_digest", "credential_revision", "representation_digest", "etag", "last_modified", "vary_names", "vary_selection_digest", "list_terminal", "updated_at")
	setInsert("source_retrieval_attempts", "tenant_id", "id", "source_id", "source_revision", "snapshot_id", "request_key", "requested_url_digest", "final_url_digest", "profile_id", "status", "received_body", "reused", "representation_digest", "etag_digest", "last_modified_digest", "fetched_at")
	setInsert("publication_snapshots", "id", "tenant_id", "host", "document_family", "profile_id", "generation", "state", "etag", "document", "created_at", "activated_at")
	setInsert("webhook_deliveries", "id", "tenant_id", "endpoint_id", "event_id", "event_type", "state", "attempt", "response_status", "safe_error", "next_attempt_at", "created_at", "updated_at")
	setUpdate("jobs", "state", "progress", "not_before", "attempt", "lease_owner", "lease_expires_at", "result", "safe_error", "failure_class", "updated_at", "cleanup_after")
	setUpdate("source_maintenance_state", "last_admitted_at")
	setUpdate("federation_sources", "status", "last_complete_snapshot_id", "last_sync_at", "last_error", "updated_at")
	setUpdate("observations", "freshness", "last_seen_at", "artwork_metadata", "ratings_metadata")
	setUpdate("source_retrieval_states", "requested_url", "final_url", "request_identity_digest", "profile_id", "media_type", "policy_digest", "credential_revision", "representation_digest", "etag", "last_modified", "vary_names", "vary_selection_digest", "list_terminal", "updated_at")
	setUpdate("outbox_events", "published_at", "lease_owner", "lease_expires_at", "cleanup_after", "dispatch_after")
	setUpdate("webhook_deliveries", "state", "attempt", "response_status", "safe_error", "next_attempt_at", "updated_at", "cleanup_after")
	// These single-column grants permit FOR UPDATE SKIP LOCKED. Their lifecycle
	// guards reject an actual update, and the worker lacks every companion
	// column needed by an allowed idempotency transition.
	setUpdate("idempotency_keys", "reservation_id")
	setUpdate("source_change_previews", "id")
	setUpdate("source_inspections", "id")
	retrievalState := manifest.tables["source_retrieval_states"]
	retrievalState.deleteRows = true
	manifest.tables["source_retrieval_states"] = retrievalState
	for _, table := range []string{"idempotency_keys", "source_change_previews", "source_inspections", "source_maintenance_state", "jobs", "outbox_events", "webhook_deliveries"} {
		privilege := manifest.tables[table]
		privilege.deleteRows = true
		manifest.tables[table] = privilege
	}
	return addSemanticPrivileges(manifest, true)
}

// columnsExcept copies a column inventory in its original order while omitting
// explicitly excluded names.
func columnsExcept(columns []string, excluded ...string) []string {
	result := make([]string, 0, len(columns))
	for _, column := range columns {
		if !slices.Contains(excluded, column) {
			result = append(result, column)
		}
	}
	return result
}

// manifestForRuntimeRole selects only the API or worker manifest; unknown process
// roles receive no inferred authority.
func manifestForRuntimeRole(runtimeRole string) (runtimePrivilegeManifest, error) {
	switch runtimeRole {
	case RuntimeRoleAPI:
		return apiPrivilegeManifest(), nil
	case RuntimeRoleWorker:
		return workerPrivilegeManifest(), nil
	default:
		return runtimePrivilegeManifest{}, fmt.Errorf("unsupported PostgreSQL runtime role %q", runtimeRole)
	}
}

// reconcileRuntimePrivileges starts from revocation, then grants each exact
// column/function manifest in one owner transaction before verifying the result.
func reconcileRuntimePrivileges(ctx context.Context, database migrationDatabase, apiRole, workerRole string, acquireLock bool) ([]RuntimePrivilegeReport, error) {
	if err := ValidateRuntimeDatabaseRoles(apiRole, workerRole); err != nil {
		return nil, err
	}
	if err := validateRuntimeRoleBindings(ctx, database, apiRole, workerRole); err != nil {
		return nil, err
	}
	tx, err := database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin runtime privilege reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if acquireLock {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationAdvisoryLockKey); err != nil {
			return nil, fmt.Errorf("lock runtime privilege reconciliation: %w", err)
		}
	}
	schema, migrationRole, err := currentCatalogSchema(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := requireCatalogSchemaOwnership(ctx, tx, schema, migrationRole); err != nil {
		return nil, err
	}
	if err := requireCatalogTables(ctx, tx, schema, migrationRole); err != nil {
		return nil, err
	}
	roles := pgx.Identifier{apiRole}.Sanitize() + `,` + pgx.Identifier{workerRole}.Sanitize()
	publicAndRoles := `PUBLIC,` + roles
	qualifiedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, `REVOKE ALL PRIVILEGES ON SCHEMA `+qualifiedSchema+` FROM PUBLIC,`+roles); err != nil {
		return nil, fmt.Errorf("revoke runtime schema privileges: %w", err)
	}
	if _, err := tx.Exec(ctx, `GRANT USAGE ON SCHEMA `+qualifiedSchema+` TO `+roles); err != nil {
		return nil, fmt.Errorf("grant runtime schema usage: %w", err)
	}
	if _, err := tx.Exec(ctx, `REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA `+qualifiedSchema+` FROM `+publicAndRoles); err != nil {
		return nil, fmt.Errorf("revoke runtime table privileges: %w", err)
	}
	if _, err := tx.Exec(ctx, `REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA `+qualifiedSchema+` FROM `+publicAndRoles); err != nil {
		return nil, fmt.Errorf("revoke runtime sequence privileges: %w", err)
	}
	functionRows, err := tx.Query(ctx, `
SELECT procedure.proname || '(' || pg_catalog.oidvectortypes(procedure.proargtypes) || ')'
  FROM pg_catalog.pg_proc procedure
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=procedure.pronamespace
  LEFT JOIN pg_catalog.pg_depend dependency
    ON dependency.classid='pg_catalog.pg_proc'::pg_catalog.regclass
   AND dependency.objid=procedure.oid
   AND dependency.deptype='e'
 WHERE namespace.nspname=$1 AND dependency.objid IS NULL
 ORDER BY 1`, schema)
	if err != nil {
		return nil, fmt.Errorf("list Catalog-owned functions: %w", err)
	}
	functions := make([]string, 0, len(catalogFunctionNames))
	for functionRows.Next() {
		var function string
		if err := functionRows.Scan(&function); err != nil {
			functionRows.Close()
			return nil, fmt.Errorf("scan Catalog-owned function: %w", err)
		}
		functions = append(functions, function)
	}
	if err := functionRows.Err(); err != nil {
		functionRows.Close()
		return nil, fmt.Errorf("iterate Catalog-owned functions: %w", err)
	}
	functionRows.Close()
	for _, function := range functions {
		qualifiedFunction := pgx.Identifier{schema}.Sanitize() + `.` + function
		if _, err := tx.Exec(ctx, `REVOKE ALL PRIVILEGES ON FUNCTION `+qualifiedFunction+` FROM PUBLIC, `+roles); err != nil {
			return nil, fmt.Errorf("revoke runtime execution on %s: %w", function, err)
		}
	}
	extensionFunctions, err := inspectExtensionFunctionPrivileges(ctx, tx, apiRole, workerRole)
	if err != nil {
		return nil, err
	}
	requiredExtensionFunctionNames := []string{runtimeCryptoByteDigestFunction, runtimeCryptoTextDigestFunction}
	requiredExtensionFunctions := make(map[string]bool, len(requiredExtensionFunctionNames))
	for _, function := range requiredExtensionFunctionNames {
		requiredExtensionFunctions[function] = false
	}
	extensionSchemas := make(map[string]struct{})
	for _, function := range extensionFunctions {
		if function.extension == runtimeCryptoExtension || function.extension == "vector" {
			extensionSchemas[function.schema] = struct{}{}
		}
		qualifiedFunction := pgx.Identifier{function.schema, function.name}.Sanitize() + `(` + function.arguments + `)`
		if _, err := tx.Exec(ctx, `REVOKE ALL PRIVILEGES ON FUNCTION `+qualifiedFunction+` FROM PUBLIC, `+roles); err != nil {
			return nil, fmt.Errorf("revoke runtime execution on %s function %s: %w", function.extension, function.signature, err)
		}
		grantRoles := ""
		switch {
		case function.extension == runtimeCryptoExtension && function.signature == runtimeCryptoByteDigestFunction:
			requiredExtensionFunctions[function.signature] = true
			grantRoles = roles
		case function.extension == runtimeCryptoExtension && function.signature == runtimeCryptoTextDigestFunction:
			requiredExtensionFunctions[function.signature] = true
			grantRoles = pgx.Identifier{apiRole}.Sanitize()
		case function.extension == "vector":
			if vectorFunctionAllowed(RuntimeRoleAPI, function.signature) {
				grantRoles = pgx.Identifier{apiRole}.Sanitize()
			}
			if vectorFunctionAllowed(RuntimeRoleWorker, function.signature) {
				if grantRoles != "" {
					grantRoles += ","
				}
				grantRoles += pgx.Identifier{workerRole}.Sanitize()
			}
		}
		if grantRoles != "" {
			statement := `GRANT EXECUTE ON FUNCTION ` + qualifiedFunction + ` TO ` + grantRoles
			if function.extension == "vector" {
				statement = optionalSemanticGrant(statement, pgx.Identifier{schema, "catalog_semantic_schema"}.Sanitize())
			}
			if _, err := tx.Exec(ctx, statement); err != nil {
				return nil, fmt.Errorf("grant runtime execution on %s function %s: %w", function.extension, function.signature, err)
			}
		}
	}
	for _, function := range requiredExtensionFunctionNames {
		if !requiredExtensionFunctions[function] {
			return nil, fmt.Errorf("required %s function %s is missing", runtimeCryptoExtension, function)
		}
	}
	for extensionSchema := range extensionSchemas {
		if extensionSchema == "pg_catalog" || extensionSchema == schema {
			continue
		}
		qualifiedExtensionSchema := pgx.Identifier{extensionSchema}.Sanitize()
		if _, err := tx.Exec(ctx, `REVOKE CREATE ON SCHEMA `+qualifiedExtensionSchema+` FROM `+roles); err != nil {
			return nil, fmt.Errorf("revoke runtime creation in %s extension schema %s: %w", runtimeCryptoExtension, extensionSchema, err)
		}
		if _, err := tx.Exec(ctx, `GRANT USAGE ON SCHEMA `+qualifiedExtensionSchema+` TO `+roles); err != nil {
			return nil, fmt.Errorf("grant runtime usage on %s extension schema %s: %w", runtimeCryptoExtension, extensionSchema, err)
		}
	}
	for _, statement := range []string{
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + pgx.Identifier{migrationRole}.Sanitize() + ` REVOKE ALL PRIVILEGES ON TABLES FROM ` + publicAndRoles,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + pgx.Identifier{migrationRole}.Sanitize() + ` REVOKE ALL PRIVILEGES ON SEQUENCES FROM ` + publicAndRoles,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + pgx.Identifier{migrationRole}.Sanitize() + ` REVOKE ALL PRIVILEGES ON FUNCTIONS FROM ` + publicAndRoles,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + pgx.Identifier{migrationRole}.Sanitize() + ` REVOKE ALL PRIVILEGES ON TYPES FROM ` + publicAndRoles,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + pgx.Identifier{migrationRole}.Sanitize() + ` IN SCHEMA ` + qualifiedSchema + ` REVOKE ALL PRIVILEGES ON TABLES FROM ` + publicAndRoles,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + pgx.Identifier{migrationRole}.Sanitize() + ` IN SCHEMA ` + qualifiedSchema + ` REVOKE ALL PRIVILEGES ON SEQUENCES FROM ` + publicAndRoles,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + pgx.Identifier{migrationRole}.Sanitize() + ` IN SCHEMA ` + qualifiedSchema + ` REVOKE ALL PRIVILEGES ON FUNCTIONS FROM ` + publicAndRoles,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + pgx.Identifier{migrationRole}.Sanitize() + ` IN SCHEMA ` + qualifiedSchema + ` REVOKE ALL PRIVILEGES ON TYPES FROM ` + publicAndRoles,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return nil, fmt.Errorf("remove runtime default privileges: %w", err)
		}
	}
	for _, binding := range []struct {
		name     string
		manifest runtimePrivilegeManifest
	}{{apiRole, apiPrivilegeManifest()}, {workerRole, workerPrivilegeManifest()}} {
		if err := grantRuntimeManifest(ctx, tx, schema, binding.name, binding.manifest); err != nil {
			return nil, err
		}
	}
	reports := make([]RuntimePrivilegeReport, 0, 2)
	for _, binding := range []struct {
		runtimeRole  string
		databaseRole string
	}{{RuntimeRoleAPI, apiRole}, {RuntimeRoleWorker, workerRole}} {
		report, err := inspectRuntimePrivileges(ctx, tx, schema, binding.runtimeRole, binding.databaseRole)
		if err != nil {
			return nil, err
		}
		if !report.Compliant {
			return nil, fmt.Errorf("PostgreSQL %s role %q privilege reconciliation is incomplete: %s", binding.runtimeRole, binding.databaseRole, strings.Join(report.Differences, "; "))
		}
		reports = append(reports, report)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit runtime privilege reconciliation: %w", err)
	}
	return reports, nil
}

// validateRuntimeRoleBindings requires two existing direct-login runtime
// identities with the exact role attributes and no memberships, distinct from the
// migration identity.
func validateRuntimeRoleBindings(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, apiRole, workerRole string) error {
	var migrationRole string
	if err := query.QueryRow(ctx, `SELECT current_user`).Scan(&migrationRole); err != nil {
		return fmt.Errorf("inspect PostgreSQL migration role: %w", err)
	}
	for _, binding := range []struct {
		runtimeRole  string
		databaseRole string
	}{{RuntimeRoleAPI, apiRole}, {RuntimeRoleWorker, workerRole}} {
		runtimeRole, databaseRole := binding.runtimeRole, binding.databaseRole
		if databaseRole == migrationRole {
			return fmt.Errorf("PostgreSQL %s role %q must be distinct from the migration role", runtimeRole, databaseRole)
		}
		attributes, err := readRoleAttributes(ctx, query, databaseRole)
		if err != nil {
			return err
		}
		if err := validateRuntimeRoleAttributes(attributes, runtimeRole); err != nil {
			return err
		}
		if err := validateRuntimeRoleMembership(ctx, query, databaseRole, runtimeRole); err != nil {
			return err
		}
	}
	return nil
}

// ValidateRuntimeDatabaseRoles validates the deployment-selected PostgreSQL
// role names before they are used as quoted identifiers.
func ValidateRuntimeDatabaseRoles(apiRole, workerRole string) error {
	for _, binding := range []struct {
		runtimeRole  string
		databaseRole string
	}{{RuntimeRoleAPI, apiRole}, {RuntimeRoleWorker, workerRole}} {
		runtimeRole, databaseRole := binding.runtimeRole, binding.databaseRole
		if databaseRole == "" || len(databaseRole) > maxPostgresRoleNameBytes || strings.ContainsRune(databaseRole, '\x00') || strings.IndexFunc(databaseRole, unicode.IsSpace) >= 0 {
			return fmt.Errorf("PostgreSQL %s role name must contain 1 through %d non-whitespace bytes without NUL", runtimeRole, maxPostgresRoleNameBytes)
		}
	}
	if apiRole == workerRole {
		return errors.New("distinct PostgreSQL API and worker role names are required")
	}
	return nil
}

// grantRuntimeManifest applies only reviewed column grants, explicit deletes and
// exact function overloads, quoting schema, table, column and role identifiers.
func grantRuntimeManifest(ctx context.Context, tx pgx.Tx, schema, role string, manifest runtimePrivilegeManifest) error {
	roleIdentifier := pgx.Identifier{role}.Sanitize()
	tables := make([]string, 0, len(manifest.tables))
	for table := range manifest.tables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		privilege := manifest.tables[table]
		qualifiedTable := pgx.Identifier{schema, table}.Sanitize()
		statements := make([]string, 0, 4)
		if len(privilege.selectColumns) > 0 {
			statements = append(statements, `GRANT SELECT (`+quotedColumns(privilege.selectColumns)+`) ON TABLE `+qualifiedTable+` TO `+roleIdentifier)
		}
		if len(privilege.insertColumns) > 0 {
			statements = append(statements, `GRANT INSERT (`+quotedColumns(privilege.insertColumns)+`) ON TABLE `+qualifiedTable+` TO `+roleIdentifier)
		}
		if len(privilege.updateColumns) > 0 {
			statements = append(statements, `GRANT UPDATE (`+quotedColumns(privilege.updateColumns)+`) ON TABLE `+qualifiedTable+` TO `+roleIdentifier)
		}
		if privilege.deleteRows {
			statements = append(statements, `GRANT DELETE ON TABLE `+qualifiedTable+` TO `+roleIdentifier)
		}
		for _, statement := range statements {
			if _, optional := semanticTableColumns[table]; optional {
				statement = optionalSemanticGrant(statement, qualifiedTable)
			}
			if _, err := tx.Exec(ctx, statement); err != nil {
				return fmt.Errorf("grant PostgreSQL %s role privileges on %s: %w", role, table, err)
			}
		}
	}
	for _, function := range manifest.functions {
		if _, err := tx.Exec(ctx, `GRANT EXECUTE ON FUNCTION `+pgx.Identifier{schema}.Sanitize()+`.`+function+` TO `+roleIdentifier); err != nil {
			return fmt.Errorf("grant PostgreSQL %s role execution on %s: %w", role, function, err)
		}
	}
	return nil
}

// quotedColumns joins individually quoted PostgreSQL column identifiers for
// column-explicit GRANT statements.
func quotedColumns(columns []string) string {
	result := make([]string, 0, len(columns))
	for _, column := range columns {
		result = append(result, pgx.Identifier{column}.Sanitize())
	}
	return strings.Join(result, `,`)
}

// currentCatalogSchema resolves the current schema and role, rejecting PostgreSQL
// system schemas as the Catalog target.
func currentCatalogSchema(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (string, string, error) {
	var schema, migrationRole string
	if err := query.QueryRow(ctx, `
SELECT current_schema(),current_user
 WHERE current_schema() NOT IN ('pg_catalog','information_schema')`).Scan(&schema, &migrationRole); err != nil {
		return "", "", fmt.Errorf("resolve active Catalog schema: %w", err)
	}
	return schema, migrationRole, nil
}

// requireCatalogSchemaOwnership requires direct migration-role schema ownership or
// the equivalent pg_database_owner ownership of its dedicated database.
func requireCatalogSchemaOwnership(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, schema, migrationRole string) error {
	var schemaOwner, databaseOwner string
	if err := query.QueryRow(ctx, `
SELECT pg_catalog.pg_get_userbyid(namespace.nspowner),
       pg_catalog.pg_get_userbyid(database_record.datdba)
  FROM pg_catalog.pg_namespace namespace
  JOIN pg_catalog.pg_database database_record ON database_record.datname=current_database()
 WHERE namespace.nspname=$1`, schema).Scan(&schemaOwner, &databaseOwner); err != nil {
		return fmt.Errorf("inspect Catalog schema owner for %s: %w", schema, err)
	}
	if schemaOwner != migrationRole && !(schemaOwner == "pg_database_owner" && databaseOwner == migrationRole) {
		return fmt.Errorf("catalog schema %s must be owned by migration role %q or its dedicated database, found schema owner %q and database owner %q", schema, migrationRole, schemaOwner, databaseOwner)
	}
	return nil
}

// requireCatalogTables verifies every maintained table exists in the selected
// schema and is owned by the migration identity before reconciliation.
func requireCatalogTables(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, schema, migrationRole string) error {
	for _, table := range catalogTableNames {
		var tableOwner string
		if err := query.QueryRow(ctx, `
SELECT pg_get_userbyid(class.relowner)
  FROM pg_catalog.pg_class class
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=class.relnamespace
 WHERE namespace.nspname=$1 AND class.relname=$2 AND class.relkind IN ('r','p')`, schema, table).Scan(&tableOwner); err != nil {
			return fmt.Errorf("catalog table %s.%s is missing or unreadable: %w", schema, table, err)
		}
		if tableOwner != migrationRole {
			return fmt.Errorf("catalog table %s.%s must be owned by migration role %q, found %q", schema, table, migrationRole, tableOwner)
		}
	}
	return nil
}

// inspectRuntimePrivileges compares effective authority, including PUBLIC and
// grant options, against one checked-in role manifest and returns bounded drift.
func inspectRuntimePrivileges(ctx context.Context, query interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, schema, runtimeRole, databaseRole string) (RuntimePrivilegeReport, error) {
	manifest, err := manifestForRuntimeRole(runtimeRole)
	if err != nil {
		return RuntimePrivilegeReport{}, err
	}
	report := RuntimePrivilegeReport{ManifestVersion: runtimePrivilegeManifestVersion, Schema: schema, RuntimeRole: runtimeRole, DatabaseRole: databaseRole}
	addDifference := func(value string) {
		if len(report.Differences) < maxPrivilegeDifferences {
			report.Differences = append(report.Differences, value)
		}
	}
	var schemaUsage, schemaCreate, publicSchemaPrivilege, schemaGrantable bool
	if err := query.QueryRow(ctx, `
SELECT pg_catalog.has_schema_privilege($1,namespace.oid,'USAGE'),
       pg_catalog.has_schema_privilege($1,namespace.oid,'CREATE'),
       EXISTS (
         SELECT 1
          FROM pg_catalog.aclexplode(coalesce(namespace.nspacl,pg_catalog.acldefault('n',namespace.nspowner))) privilege
         WHERE privilege.grantee=0
       ),
       EXISTS (
         SELECT 1
           FROM pg_catalog.aclexplode(coalesce(namespace.nspacl,pg_catalog.acldefault('n',namespace.nspowner))) privilege
          WHERE privilege.grantee=(SELECT inspected_role.oid FROM pg_catalog.pg_roles inspected_role WHERE inspected_role.rolname=$1)
            AND privilege.is_grantable
       )
  FROM pg_catalog.pg_namespace namespace
 WHERE namespace.nspname=$2`, databaseRole, schema).Scan(&schemaUsage, &schemaCreate, &publicSchemaPrivilege, &schemaGrantable); err != nil {
		return RuntimePrivilegeReport{}, fmt.Errorf("inspect PostgreSQL %s schema privileges: %w", runtimeRole, err)
	}
	if !schemaUsage {
		addDifference("schema USAGE is missing")
	}
	if schemaCreate {
		addDifference("schema CREATE is not permitted")
	}
	if publicSchemaPrivilege {
		addDifference("unexpected PUBLIC privilege on Catalog schema")
	}
	if schemaGrantable {
		addDifference("unexpected grant option on Catalog schema")
	}
	rows, err := query.Query(ctx, `
SELECT class.relname,attribute.attname,
       has_column_privilege($1,format('%I.%I',$2::text,class.relname),attribute.attname,'SELECT'),
       has_column_privilege($1,format('%I.%I',$2::text,class.relname),attribute.attname,'INSERT'),
       has_column_privilege($1,format('%I.%I',$2::text,class.relname),attribute.attname,'UPDATE')
  FROM pg_catalog.pg_class class
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=class.relnamespace
  JOIN pg_catalog.pg_attribute attribute ON attribute.attrelid=class.oid
 WHERE namespace.nspname=$2::text AND class.relkind IN ('r','p','v','m','f')
   AND attribute.attnum>0 AND NOT attribute.attisdropped
 ORDER BY class.relname,attribute.attnum`, databaseRole, schema)
	if err != nil {
		return RuntimePrivilegeReport{}, fmt.Errorf("inspect PostgreSQL %s column privileges: %w", runtimeRole, err)
	}
	seenTables := make(map[string]bool)
	seenColumns := make(map[string]bool)
	for rows.Next() {
		var table, column string
		var actualSelect, actualInsert, actualUpdate bool
		if err := rows.Scan(&table, &column, &actualSelect, &actualInsert, &actualUpdate); err != nil {
			rows.Close()
			return RuntimePrivilegeReport{}, fmt.Errorf("scan PostgreSQL %s column privileges: %w", runtimeRole, err)
		}
		seenTables[table] = true
		seenColumns[table+"\x00"+column] = true
		expected := manifest.tables[table]
		for privilege, values := range map[string][2]bool{
			"SELECT": {actualSelect, containsString(expected.selectColumns, column)},
			"INSERT": {actualInsert, containsString(expected.insertColumns, column)},
			"UPDATE": {actualUpdate, containsString(expected.updateColumns, column)},
		} {
			if values[0] != values[1] {
				state := "unexpected"
				if values[1] {
					state = "missing"
				}
				addDifference(fmt.Sprintf("%s %s on %s.%s", state, privilege, table, column))
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RuntimePrivilegeReport{}, fmt.Errorf("iterate PostgreSQL %s column privileges: %w", runtimeRole, err)
	}
	rows.Close()
	rows, err = query.Query(ctx, `
SELECT class.relname,
       EXISTS (
         SELECT 1
           FROM pg_catalog.aclexplode(coalesce(class.relacl,pg_catalog.acldefault('r',class.relowner))) privilege
          WHERE privilege.grantee=0
       ) OR EXISTS (
         SELECT 1
           FROM pg_catalog.pg_attribute attribute
           CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) privilege
          WHERE attribute.attrelid=class.oid AND attribute.attnum>0 AND NOT attribute.attisdropped
            AND privilege.grantee=0
       ),
       EXISTS (
         SELECT 1
           FROM pg_catalog.aclexplode(coalesce(class.relacl,pg_catalog.acldefault('r',class.relowner))) privilege
          WHERE privilege.grantee=(SELECT inspected_role.oid FROM pg_catalog.pg_roles inspected_role WHERE inspected_role.rolname=$2)
            AND privilege.is_grantable
       ) OR EXISTS (
         SELECT 1
           FROM pg_catalog.pg_attribute attribute
           CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) privilege
          WHERE attribute.attrelid=class.oid AND attribute.attnum>0 AND NOT attribute.attisdropped
            AND privilege.grantee=(SELECT inspected_role.oid FROM pg_catalog.pg_roles inspected_role WHERE inspected_role.rolname=$2)
            AND privilege.is_grantable
       )
  FROM pg_catalog.pg_class class
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=class.relnamespace
 WHERE namespace.nspname=$1 AND class.relkind IN ('r','p','v','m','f')
 ORDER BY class.relname`, schema, databaseRole)
	if err != nil {
		return RuntimePrivilegeReport{}, fmt.Errorf("inspect PostgreSQL %s PUBLIC table privileges: %w", runtimeRole, err)
	}
	for rows.Next() {
		var table string
		var publicPrivilege, grantable bool
		if err := rows.Scan(&table, &publicPrivilege, &grantable); err != nil {
			rows.Close()
			return RuntimePrivilegeReport{}, fmt.Errorf("scan PostgreSQL %s PUBLIC table privileges: %w", runtimeRole, err)
		}
		if publicPrivilege {
			addDifference("unexpected PUBLIC privilege on table " + table)
		}
		if grantable {
			addDifference("unexpected grant option on table " + table)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RuntimePrivilegeReport{}, fmt.Errorf("iterate PostgreSQL %s PUBLIC table privileges: %w", runtimeRole, err)
	}
	rows.Close()
	for _, table := range catalogTableNames {
		if !seenTables[table] {
			addDifference("missing Catalog table " + table)
			continue
		}
		for _, column := range catalogTableColumns[table] {
			if !seenColumns[table+"\x00"+column] {
				addDifference("missing Catalog column " + table + "." + column)
			}
		}
	}
	if seenTables["catalog_semantic_schema"] || seenTables["semantic_generations"] || seenTables["semantic_vectors"] || seenTables["semantic_members"] {
		for table, columns := range semanticTableColumns {
			if !seenTables[table] {
				addDifference("missing optional Catalog table " + table)
			}
			for _, column := range columns {
				if !seenColumns[table+"\x00"+column] {
					addDifference("missing optional Catalog column " + table + "." + column)
				}
			}
		}
	}
	tables := make([]string, 0, len(seenTables))
	for table := range seenTables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		var selectTable, insertTable, updateTable, deleteRows, truncateRows, references, trigger bool
		qualified := pgx.Identifier{schema, table}.Sanitize()
		if err := query.QueryRow(ctx, `SELECT has_table_privilege($1,$2,'SELECT'),has_table_privilege($1,$2,'INSERT'),has_table_privilege($1,$2,'UPDATE'),has_table_privilege($1,$2,'DELETE'),has_table_privilege($1,$2,'TRUNCATE'),has_table_privilege($1,$2,'REFERENCES'),has_table_privilege($1,$2,'TRIGGER')`, databaseRole, qualified).Scan(&selectTable, &insertTable, &updateTable, &deleteRows, &truncateRows, &references, &trigger); err != nil {
			return RuntimePrivilegeReport{}, fmt.Errorf("inspect PostgreSQL %s table privileges on %s: %w", runtimeRole, table, err)
		}
		expectedTablePrivilege := manifest.tables[table]
		if selectTable {
			addDifference("unexpected table SELECT on " + table)
		}
		if insertTable {
			addDifference("unexpected table INSERT on " + table)
		}
		if updateTable {
			addDifference("unexpected table UPDATE on " + table)
		}
		expectedDelete := expectedTablePrivilege.deleteRows
		if deleteRows != expectedDelete {
			state := "unexpected"
			if expectedDelete {
				state = "missing"
			}
			addDifference(fmt.Sprintf("%s DELETE on %s", state, table))
		}
		if truncateRows {
			addDifference("unexpected TRUNCATE on " + table)
		}
		if references {
			addDifference("unexpected REFERENCES on " + table)
		}
		if trigger {
			addDifference("unexpected TRIGGER on " + table)
		}
	}
	rows, err = query.Query(ctx, `
SELECT procedure.proname || '(' || pg_catalog.oidvectortypes(procedure.proargtypes) || ')',
       pg_catalog.has_function_privilege($1,procedure.oid,'EXECUTE'),
       EXISTS (
         SELECT 1
          FROM pg_catalog.aclexplode(coalesce(procedure.proacl,pg_catalog.acldefault('f',procedure.proowner))) privilege
         WHERE privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
       ),
       EXISTS (
         SELECT 1
           FROM pg_catalog.aclexplode(coalesce(procedure.proacl,pg_catalog.acldefault('f',procedure.proowner))) privilege
          WHERE privilege.grantee=(SELECT inspected_role.oid FROM pg_catalog.pg_roles inspected_role WHERE inspected_role.rolname=$1)
            AND privilege.is_grantable
       )
  FROM pg_catalog.pg_proc procedure
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=procedure.pronamespace
  LEFT JOIN pg_catalog.pg_depend dependency
    ON dependency.classid='pg_catalog.pg_proc'::pg_catalog.regclass
   AND dependency.objid=procedure.oid
   AND dependency.deptype='e'
 WHERE namespace.nspname=$2 AND dependency.objid IS NULL
 ORDER BY 1`, databaseRole, schema)
	if err != nil {
		return RuntimePrivilegeReport{}, fmt.Errorf("inspect PostgreSQL %s function privileges: %w", runtimeRole, err)
	}
	seenFunctions := make(map[string]bool)
	for rows.Next() {
		var function string
		var execute, publicExecute, grantable bool
		if err := rows.Scan(&function, &execute, &publicExecute, &grantable); err != nil {
			rows.Close()
			return RuntimePrivilegeReport{}, fmt.Errorf("scan PostgreSQL %s function privileges: %w", runtimeRole, err)
		}
		seenFunctions[function] = true
		expected := containsString(manifest.functions, function)
		if execute != expected {
			state := "unexpected"
			if expected {
				state = "missing"
			}
			addDifference(fmt.Sprintf("%s EXECUTE on %s", state, function))
		}
		if publicExecute {
			addDifference("unexpected PUBLIC EXECUTE on " + function)
		}
		if grantable {
			addDifference("unexpected grant option on function " + function)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RuntimePrivilegeReport{}, fmt.Errorf("iterate PostgreSQL %s function privileges: %w", runtimeRole, err)
	}
	rows.Close()
	for _, function := range catalogFunctionNames {
		if !seenFunctions[function] {
			addDifference("missing Catalog function " + function)
		}
	}
	extensionFunctions, err := inspectExtensionFunctionPrivileges(ctx, query, databaseRole, databaseRole)
	if err != nil {
		return RuntimePrivilegeReport{}, err
	}
	requiredExtensionFunctionNames := []string{runtimeCryptoByteDigestFunction}
	if runtimeRole == RuntimeRoleAPI {
		requiredExtensionFunctionNames = append(requiredExtensionFunctionNames, runtimeCryptoTextDigestFunction)
	}
	requiredExtensionFunctions := make(map[string]bool, len(requiredExtensionFunctionNames))
	for _, function := range requiredExtensionFunctionNames {
		requiredExtensionFunctions[function] = false
	}
	checkedExtensionSchemas := make(map[string]bool)
	for _, function := range extensionFunctions {
		_, expected := requiredExtensionFunctions[function.signature]
		expected = expected && function.extension == runtimeCryptoExtension
		if expected {
			requiredExtensionFunctions[function.signature] = true
		}
		if function.extension == "vector" {
			expected = seenTables["catalog_semantic_schema"] && vectorFunctionAllowed(runtimeRole, function.signature)
		}
		if function.roleExecute != expected {
			state := "unexpected"
			if expected {
				state = "missing"
			}
			addDifference(fmt.Sprintf("%s EXECUTE on %s function %s", state, function.extension, function.signature))
		}
		if function.publicExecute {
			addDifference(fmt.Sprintf("unexpected PUBLIC EXECUTE on %s function %s", function.extension, function.signature))
		}
		if function.roleGrantable {
			addDifference(fmt.Sprintf("unexpected grant option on %s function %s", function.extension, function.signature))
		}
		if (function.extension == runtimeCryptoExtension || function.extension == "vector") && function.schema != "pg_catalog" && function.schema != schema && !checkedExtensionSchemas[function.schema] {
			checkedExtensionSchemas[function.schema] = true
			var usage, create, grantable bool
			if err := query.QueryRow(ctx, `
SELECT has_schema_privilege($1,$2,'USAGE'),has_schema_privilege($1,$2,'CREATE'),
       EXISTS (
         SELECT 1
           FROM pg_catalog.pg_namespace namespace
           CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(namespace.nspacl,pg_catalog.acldefault('n',namespace.nspowner))) privilege
          WHERE namespace.nspname=$2
            AND privilege.grantee=(SELECT inspected_role.oid FROM pg_catalog.pg_roles inspected_role WHERE inspected_role.rolname=$1)
            AND privilege.is_grantable
       )`, databaseRole, function.schema).Scan(&usage, &create, &grantable); err != nil {
				return RuntimePrivilegeReport{}, fmt.Errorf("inspect PostgreSQL %s %s extension schema privileges: %w", runtimeRole, function.extension, err)
			}
			if !usage {
				addDifference("missing schema USAGE on " + function.extension + " extension schema " + function.schema)
			}
			if create {
				addDifference("unexpected schema CREATE on " + function.extension + " extension schema " + function.schema)
			}
			if grantable {
				addDifference("unexpected grant option on " + function.extension + " extension schema " + function.schema)
			}
		}
	}
	for _, function := range requiredExtensionFunctionNames {
		if !requiredExtensionFunctions[function] {
			addDifference("missing " + runtimeCryptoExtension + " function " + function)
		}
	}
	rows, err = query.Query(ctx, `
SELECT class.relname,
       pg_catalog.has_sequence_privilege($1,class.oid,'USAGE'),
       pg_catalog.has_sequence_privilege($1,class.oid,'SELECT'),
       pg_catalog.has_sequence_privilege($1,class.oid,'UPDATE'),
       EXISTS (
         SELECT 1
           FROM pg_catalog.aclexplode(coalesce(class.relacl,pg_catalog.acldefault('S',class.relowner))) privilege
          WHERE privilege.grantee=(SELECT inspected_role.oid FROM pg_catalog.pg_roles inspected_role WHERE inspected_role.rolname=$1)
            AND privilege.is_grantable
       )
  FROM pg_catalog.pg_class class
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=class.relnamespace
 WHERE namespace.nspname=$2 AND class.relkind='S'
 ORDER BY class.relname`, databaseRole, schema)
	if err != nil {
		return RuntimePrivilegeReport{}, fmt.Errorf("inspect PostgreSQL %s sequence privileges: %w", runtimeRole, err)
	}
	for rows.Next() {
		var sequence string
		var usage, selectValues, updateValues, grantable bool
		if err := rows.Scan(&sequence, &usage, &selectValues, &updateValues, &grantable); err != nil {
			rows.Close()
			return RuntimePrivilegeReport{}, fmt.Errorf("scan PostgreSQL %s sequence privileges: %w", runtimeRole, err)
		}
		if usage || selectValues || updateValues {
			addDifference("unexpected privilege on sequence " + sequence)
		}
		if grantable {
			addDifference("unexpected grant option on sequence " + sequence)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RuntimePrivilegeReport{}, fmt.Errorf("iterate PostgreSQL %s sequence privileges: %w", runtimeRole, err)
	}
	rows.Close()
	var unsafeDefaults int
	if err := query.QueryRow(ctx, `
WITH catalog_owner AS (
  SELECT class.relowner AS owner_oid
    FROM pg_catalog.pg_class class
    JOIN pg_catalog.pg_namespace namespace ON namespace.oid=class.relnamespace
   WHERE namespace.nspname=$1 AND class.relname='catalog_schema_migrations' AND class.relkind IN ('r','p')
), object_types(object_type) AS (
  VALUES (CAST('r' AS "char")),(CAST('S' AS "char")),(CAST('f' AS "char")),(CAST('T' AS "char"))
), effective_defaults AS (
  SELECT catalog_owner.owner_oid,
         coalesce(defaults.defaclacl,pg_catalog.acldefault(object_types.object_type,catalog_owner.owner_oid)) AS privileges
    FROM catalog_owner
    CROSS JOIN object_types
    LEFT JOIN pg_catalog.pg_default_acl defaults
      ON defaults.defaclrole=catalog_owner.owner_oid
     AND defaults.defaclobjtype=object_types.object_type
     AND defaults.defaclnamespace=0
  UNION ALL
  SELECT catalog_owner.owner_oid,defaults.defaclacl
    FROM catalog_owner
    JOIN pg_catalog.pg_default_acl defaults ON defaults.defaclrole=catalog_owner.owner_oid
   WHERE defaults.defaclnamespace=to_regnamespace($1)
)
SELECT count(*)
  FROM effective_defaults
  CROSS JOIN LATERAL pg_catalog.aclexplode(effective_defaults.privileges) privilege
 WHERE (privilege.grantee=0 OR privilege.grantee=(SELECT inspected_role.oid FROM pg_catalog.pg_roles inspected_role WHERE inspected_role.rolname=$2))
   AND privilege.grantee<>effective_defaults.owner_oid`, schema, databaseRole).Scan(&unsafeDefaults); err != nil {
		return RuntimePrivilegeReport{}, fmt.Errorf("inspect PostgreSQL %s default privileges: %w", runtimeRole, err)
	}
	if unsafeDefaults > 0 {
		addDifference("unexpected future-object default privileges")
	}
	report.Compliant = len(report.Differences) == 0
	return report, nil
}

// inspectExtensionFunctionPrivileges inventories every pgcrypto overload plus
// other application-visible extension functions except plpgsql. It reports PUBLIC,
// effective role and grant-option execution separately.
func inspectExtensionFunctionPrivileges(ctx context.Context, query interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, databaseRole, additionalDatabaseRole string) ([]extensionFunctionPrivilege, error) {
	rows, err := query.Query(ctx, `
SELECT extension.extname,namespace.nspname,procedure.proname,
       pg_catalog.oidvectortypes(procedure.proargtypes),
       EXISTS (
         SELECT 1
           FROM pg_catalog.aclexplode(coalesce(procedure.proacl,pg_catalog.acldefault('f',procedure.proowner))) privilege
          WHERE privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
       ),
       pg_catalog.has_function_privilege($2,procedure.oid,'EXECUTE'),
       EXISTS (
         SELECT 1
           FROM pg_catalog.aclexplode(coalesce(procedure.proacl,pg_catalog.acldefault('f',procedure.proowner))) privilege
          WHERE privilege.grantee=(SELECT inspected_role.oid FROM pg_catalog.pg_roles inspected_role WHERE inspected_role.rolname=$2)
            AND privilege.is_grantable
       )
  FROM pg_catalog.pg_extension extension
  JOIN pg_catalog.pg_depend dependency
    ON dependency.refclassid='pg_catalog.pg_extension'::pg_catalog.regclass
   AND dependency.refobjid=extension.oid
   AND dependency.classid='pg_catalog.pg_proc'::pg_catalog.regclass
   AND dependency.deptype='e'
  JOIN pg_catalog.pg_proc procedure ON procedure.oid=dependency.objid
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=procedure.pronamespace
	WHERE extension.extname=$1 OR (
	        extension.extname<>'plpgsql' AND (
	          namespace.nspname='pg_catalog' OR (
	            namespace.nspname<>'information_schema' AND
	            (pg_catalog.has_schema_privilege($2,namespace.oid,'USAGE') OR
	             pg_catalog.has_schema_privilege($3,namespace.oid,'USAGE'))
	          )
	        )
	      )
 ORDER BY extension.extname,namespace.nspname,procedure.proname,pg_catalog.oidvectortypes(procedure.proargtypes)`, runtimeCryptoExtension, databaseRole, additionalDatabaseRole)
	if err != nil {
		return nil, fmt.Errorf("inspect installed extension functions: %w", err)
	}
	defer rows.Close()
	functions := make([]extensionFunctionPrivilege, 0)
	for rows.Next() {
		var function extensionFunctionPrivilege
		if err := rows.Scan(&function.extension, &function.schema, &function.name, &function.arguments, &function.publicExecute, &function.roleExecute, &function.roleGrantable); err != nil {
			return nil, fmt.Errorf("scan installed extension function: %w", err)
		}
		function.signature = function.name + "(" + function.arguments + ")"
		functions = append(functions, function)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate installed extension functions: %w", err)
	}
	return functions, nil
}

// containsString tests exact membership without normalizing privilege or
// identifier names.
func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
