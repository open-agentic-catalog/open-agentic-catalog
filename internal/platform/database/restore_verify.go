// Restore verification coordinates bounded resumable read-only checks and safe
// findings against an explicitly selected recovery target.

package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const (
	RestoreVerificationFormatVersion   = 1
	RestoreVerificationCheckSetVersion = 7
	DefaultRestoreVerificationLimit    = 100
	MaxRestoreVerificationLimit        = 1000
	MaxRestoreVerificationCursorBytes  = 4096
	MaxRestoreVerificationDecodedBytes = 80 << 20
	MaxRestoreVerificationFindings     = 256
	MaxRestoreVerificationOutputBytes  = 256 << 10
	RestoreVerificationTimeout         = time.Minute
)

const (
	restoreIntegrityValid   = "valid"
	restoreIntegrityInvalid = "invalid"
	restoreTraversalDone    = "complete"
	restoreTraversalMore    = "continued"
	restoreTraversalBlocked = "blocked"
)

// RestoreVerificationOptions identifies one externally selected tenant and
// recovery point. The recovery-point value is correlation metadata, not proof
// that the connected database came from that backup.
type RestoreVerificationOptions struct {
	TenantID      string
	RecoveryPoint string
	APIRole       string
	WorkerRole    string
	Cursor        string
	Limit         int
}

// RestoreFinding is deliberately safe and closed. It never contains retained
// documents, payloads, URLs, credentials, validators, queries, or SQL errors.
type RestoreFinding struct {
	Code        string `json:"code"`
	Class       string `json:"class"`
	Severity    string `json:"severity"`
	Domain      string `json:"domain"`
	RecordID    string `json:"recordId,omitempty"`
	Disposition string `json:"disposition"`
}

// RestoreVerificationReport is one deterministic page of a gap-free logical
// integrity scan. Traversal and integrity are independent conclusions.
type RestoreVerificationReport struct {
	FormatVersion            int              `json:"formatVersion"`
	CheckSetVersion          int              `json:"checkSetVersion"`
	RecoveryPoint            string           `json:"recoveryPoint"`
	TenantID                 string           `json:"tenantId"`
	SchemaVersion            int64            `json:"schemaVersion"`
	PrivilegeManifestVersion int              `json:"privilegeManifestVersion"`
	Traversal                string           `json:"traversal"`
	Integrity                string           `json:"integrity"`
	Processed                int              `json:"processed"`
	DecodedBytes             int64            `json:"decodedBytes"`
	Findings                 []RestoreFinding `json:"findings"`
	Continuation             string           `json:"continuation,omitempty"`
	AssuranceBoundary        string           `json:"assuranceBoundary"`
}

type restoreCursor struct {
	FormatVersion            int    `json:"formatVersion"`
	CheckSetVersion          int    `json:"checkSetVersion"`
	BindingDigest            string `json:"bindingDigest"`
	SchemaVersion            int64  `json:"schemaVersion"`
	PrivilegeManifestVersion int    `json:"privilegeManifestVersion"`
	Limit                    int    `json:"limit"`
	Stage                    string `json:"stage"`
	After                    string `json:"after,omitempty"`
}

type restoreRecord struct {
	key          string
	decodedBytes int64
	finding      *RestoreFinding
}

type restoreStage struct {
	name string
	scan func(context.Context, pgx.Tx, string, string, int) ([]restoreRecord, bool, error)
}

type expectedRestoreTrigger struct {
	table      string
	function   string
	definition []string
}

var expectedRestoreTriggers = map[string]expectedRestoreTrigger{
	"publisher_domain_verification_guard": {"publisher_domain_verifications", "guard_publisher_domain_verification", []string{"before insert or delete or update"}},
	"publishers_verification_domains":     {"publishers", "clear_removed_publisher_domains", []string{"after update"}},
	"personal_items_guard":                {"personal_items", "guard_personal_item", []string{"before insert or update"}},

	"rating_targets_guard":                          {"rating_targets", "guard_rating_target", []string{"before insert or delete or update"}},
	"item_ratings_guard":                            {"item_ratings", "guard_item_rating", []string{"before insert or delete or update"}},
	"item_ratings_record":                           {"item_ratings", "record_item_rating", []string{"after insert or update"}},
	"item_rating_revisions_immutable":               {"item_rating_revisions", "reject_federation_source_history_mutation", []string{"before delete or update"}},
	"tenant_settings_guard":                         {"tenant_settings", "guard_tenant_settings", []string{"before insert or delete or update"}},
	"rating_sharing_guard":                          {"rating_sharing", "guard_rating_sharing", []string{"before insert or delete or update"}},
	"access_groups_authorization_fence":             {"access_groups", "fence_tenant_authorization", []string{"before insert or delete or update", "for each statement"}},
	"access_groups_authorization_revision":          {"access_groups", "advance_tenant_authorization", []string{"after insert or delete or update"}},
	"access_group_members_authorization_fence":      {"access_group_members", "fence_tenant_authorization", []string{"before insert or delete or update", "for each statement"}},
	"access_group_members_authorization_revision":   {"access_group_members", "advance_tenant_authorization", []string{"after insert or delete or update"}},
	"access_groups_lifecycle":                       {"access_groups", "guard_access_group", []string{"before delete or update"}},
	"access_group_members_lifecycle":                {"access_group_members", "guard_access_group_member", []string{"before insert or delete or update"}},
	"role_permissions_complete":                     {"role_permissions", "validate_role_permission_set", []string{"after insert or delete", "deferrable initially deferred"}},
	"role_definitions_complete":                     {"role_definitions", "validate_role_permission_set", []string{"after insert or update", "deferrable initially deferred"}},
	"role_assignments_complete":                     {"role_assignments", "validate_role_permission_set", []string{"after insert", "deferrable initially deferred"}},
	"role_permissions_builtin_insert":               {"role_permissions", "guard_builtin_permission_insert", []string{"before insert"}},
	"tenant_identity_realms_authorization_fence":    {"tenant_identity_realms", "fence_tenant_authorization", []string{"before insert or delete or update", "for each statement"}},
	"tenant_identity_realms_authorization_revision": {"tenant_identity_realms", "advance_tenant_authorization", []string{"after insert or delete or update"}},
	"tenant_memberships_authorization_fence":        {"tenant_memberships", "fence_tenant_authorization", []string{"before insert or delete or update", "for each statement"}},
	"tenant_memberships_authorization_revision":     {"tenant_memberships", "advance_tenant_authorization", []string{"after insert or delete or update"}},
	"namespace_memberships_authorization_fence":     {"namespace_memberships", "fence_tenant_authorization", []string{"before insert or delete or update", "for each statement"}},
	"namespace_memberships_authorization_revision":  {"namespace_memberships", "advance_tenant_authorization", []string{"after insert or delete or update"}},
	"role_assignments_authorization_fence":          {"role_assignments", "fence_tenant_authorization", []string{"before insert or delete or update", "for each statement"}},
	"role_assignments_authorization_revision":       {"role_assignments", "advance_tenant_authorization", []string{"after insert or delete or update"}},
	"role_definitions_authorization_fence":          {"role_definitions", "fence_tenant_authorization", []string{"before insert or delete or update", "for each statement"}},
	"role_definitions_authorization_revision":       {"role_definitions", "advance_tenant_authorization", []string{"after insert or delete or update"}},
	"role_permissions_authorization_fence":          {"role_permissions", "fence_tenant_authorization", []string{"before insert or delete or update", "for each statement"}},
	"role_permissions_authorization_revision":       {"role_permissions", "advance_tenant_authorization", []string{"after insert or delete or update"}},
	"tenants_authorization_initialization":          {"tenants", "initialize_tenant_authorization", []string{"after insert"}},
	"role_assignments_definition":                   {"role_assignments", "bind_role_assignment_definition", []string{"before insert or update"}},
	"tenant_authorization_state_history":            {"tenant_authorization_state", "guard_authorization_history", []string{"before delete or update"}},
	"role_definitions_history":                      {"role_definitions", "guard_authorization_history", []string{"before delete or update"}},
	"role_permissions_history":                      {"role_permissions", "guard_authorization_history", []string{"before delete or update"}},
	"item_feedback_guard":                           {"item_feedback", "catalog_feedback_guard", []string{"before insert or delete or update"}},
	"jobs_private_trace_context":                    {"jobs", "guard_private_trace_context", []string{"before update"}},
	"outbox_private_trace_context":                  {"outbox_events", "guard_private_trace_context", []string{"before update"}},
	"outbox_events_notification_admission":          {"outbox_events", "admit_webhook_notification", []string{"before insert"}},
	"webhook_deliveries_notification_loss":          {"webhook_deliveries", "record_webhook_delivery_loss", []string{"after insert or update"}},
	"webhook_notification_state_lifecycle":          {"webhook_notification_state", "guard_webhook_notification_state", []string{"before insert or delete or update"}},
	"tenants_immutable_history":                     {"tenants", "reject_catalog_immutable_history_mutation", []string{"before delete or update"}},
	"audit_event_insert_guard":                      {"audit_events", "guard_audit_event_insert", []string{"before insert"}},
	"representations_immutable_history":             {"representations", "guard_representation_history", []string{"before insert or delete or update"}},
	"namespaces_lifecycle":                          {"namespaces", "guard_namespace_lifecycle", []string{"before update"}},
	"publishers_lifecycle":                          {"publishers", "guard_publisher_lifecycle", []string{"before update"}},
	"resources_lifecycle":                           {"resources", "guard_resource_lifecycle", []string{"before update"}},
	"resource_versions_lifecycle":                   {"resource_versions", "guard_resource_version_lifecycle", []string{"before update"}},
	"catalog_listings_lifecycle":                    {"catalog_listings", "guard_catalog_listing_lifecycle", []string{"before update"}},
	"catalog_listings_current_release_state":        {"catalog_listings", "enforce_catalog_current_release_state", []string{"constraint trigger", "after insert or update", "deferrable initially deferred"}},
	"review_submissions_lifecycle":                  {"review_submissions", "guard_review_submission_lifecycle", []string{"before insert or update"}},
	"listing_releases_lifecycle":                    {"listing_releases", "guard_listing_release_lifecycle", []string{"before insert or update"}},
	"listing_releases_current_release_state":        {"listing_releases", "enforce_catalog_current_release_state", []string{"constraint trigger", "after insert or update", "deferrable initially deferred"}},
	"federation_sources_presentation_generation":    {"federation_sources", "guard_source_presentation_generation", []string{"before insert or update"}},
	"source_artwork_heads_lifecycle":                {"source_artwork_heads", "guard_source_artwork_head", []string{"before insert or delete or update"}},
	"source_artwork_revisions_immutable":            {"source_artwork_revisions", "reject_federation_source_history_mutation", []string{"before delete or update"}},
	"federation_sources_lifecycle":                  {"federation_sources", "guard_source_lifecycle", []string{"before update"}},
	"federation_sources_complete_snapshot":          {"federation_sources", "enforce_source_complete_snapshot", []string{"constraint trigger", "after insert or update", "deferrable initially deferred"}},
	"tenant_identity_realms_lifecycle":              {"tenant_identity_realms", "guard_tenant_identity_realm_lifecycle", []string{"before update"}},
	"tenant_memberships_lifecycle":                  {"tenant_memberships", "guard_tenant_membership_lifecycle", []string{"before update"}},
	"namespace_memberships_lifecycle":               {"namespace_memberships", "guard_namespace_membership_lifecycle", []string{"before update"}},
	"role_assignments_lifecycle":                    {"role_assignments", "guard_role_assignment_lifecycle", []string{"before update"}},
	"observations_lifecycle":                        {"observations", "guard_observation_lifecycle", []string{"before update"}},
	"source_retrieval_states_lifecycle":             {"source_retrieval_states", "guard_source_retrieval_state_lifecycle", []string{"before update"}},
	"source_maintenance_state_lifecycle":            {"source_maintenance_state", "guard_source_maintenance_lifecycle", []string{"before update"}},
	"source_maintenance_state_delete_lifecycle":     {"source_maintenance_state", "guard_source_maintenance_state_delete", []string{"before delete"}},
	"source_change_previews_history":                {"source_change_previews", "guard_source_change_preview_history", []string{"before delete or update"}},
	"source_inspections_history":                    {"source_inspections", "guard_source_inspection_history", []string{"before delete or update"}},
	"publication_snapshots_lifecycle":               {"publication_snapshots", "guard_publication_snapshot_lifecycle", []string{"before insert or update"}},
	"jobs_lifecycle":                                {"jobs", "guard_job_lifecycle", []string{"before insert or update"}},
	"jobs_delete_lifecycle":                         {"jobs", "guard_job_delete", []string{"before delete"}},
	"webhook_endpoints_lifecycle":                   {"webhook_endpoints", "guard_webhook_endpoint_lifecycle", []string{"before update"}},
	"webhook_deliveries_lifecycle":                  {"webhook_deliveries", "guard_webhook_delivery_lifecycle", []string{"before insert or update"}},
	"webhook_deliveries_delete_lifecycle":           {"webhook_deliveries", "guard_webhook_delivery_lifecycle", []string{"before delete"}},
	"outbox_events_lifecycle":                       {"outbox_events", "guard_outbox_event_lifecycle", []string{"before insert or update"}},
	"outbox_events_delete_lifecycle":                {"outbox_events", "guard_outbox_event_delete", []string{"before delete"}},
	"idempotency_keys_update_lifecycle":             {"idempotency_keys", "guard_idempotency_key_update", []string{"before update"}},
	"idempotency_keys_delete_lifecycle":             {"idempotency_keys", "guard_idempotency_key_delete", []string{"before delete"}},
	"observations_control_filter_projection":        {"observations", "project_observation_control_filters", []string{"before insert or update"}},
	"observations_catalog_search_projection":        {"observations", "project_observation_catalog_search", []string{"before insert or update"}},
	"observation_occurrences_immutable":             {"observation_occurrences", "reject_federation_source_history_mutation", []string{"before delete or update"}},
	"version_validation_runs_immutable":             {"version_validation_runs", "reject_version_validation_run_mutation", []string{"before delete or update"}},
	"federation_source_revisions_immutable":         {"federation_source_revisions", "reject_federation_source_history_mutation", []string{"before delete or update"}},
	"federation_source_audiences_immutable":         {"federation_source_audiences", "reject_federation_source_history_mutation", []string{"before delete or update"}},
	"federation_source_referrals_immutable":         {"federation_source_referrals", "reject_federation_source_history_mutation", []string{"before delete or update"}},
	"source_retrieval_documents_immutable":          {"source_retrieval_documents", "reject_federation_source_history_mutation", []string{"before delete or update"}},
	"source_retrieval_attempts_immutable":           {"source_retrieval_attempts", "reject_federation_source_history_mutation", []string{"before delete or update"}},
}

// VerifyRestoredCatalog performs one bounded, read-only page of logical
// restore qualification. It never starts runtimes, resolves secrets, performs
// network requests, claims work, repairs data, or changes publication state.
func VerifyRestoredCatalog(ctx context.Context, pool *pgxpool.Pool, options RestoreVerificationOptions) (RestoreVerificationReport, error) {
	if err := validateRestoreVerificationOptions(options); err != nil {
		return RestoreVerificationReport{}, err
	}
	if pool == nil {
		return RestoreVerificationReport{}, errors.New("PostgreSQL pool is required")
	}
	ctx, cancel := context.WithTimeout(ctx, RestoreVerificationTimeout)
	defer cancel()
	parsedCursor, err := parseRestoreCursor(options)
	if err != nil {
		return RestoreVerificationReport{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return RestoreVerificationReport{}, fmt.Errorf("begin read-only restore verification: %w", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	if err := setTenantContext(ctx, tx, options.TenantID); err != nil {
		return RestoreVerificationReport{}, err
	}
	var readOnly string
	if err := tx.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readOnly); err != nil || readOnly != "on" {
		return RestoreVerificationReport{}, errors.New("restore verification did not obtain a read-only transaction")
	}
	report := RestoreVerificationReport{
		FormatVersion:            RestoreVerificationFormatVersion,
		CheckSetVersion:          RestoreVerificationCheckSetVersion,
		RecoveryPoint:            options.RecoveryPoint,
		TenantID:                 options.TenantID,
		PrivilegeManifestVersion: runtimePrivilegeManifestVersion,
		Traversal:                restoreTraversalDone,
		Integrity:                restoreIntegrityValid,
		Findings:                 []RestoreFinding{},
		AssuranceBoundary:        "logical integrity for one operator-selected tenant in an externally isolated and quiescent restore; not backup provenance, completeness, tamper evidence, tenant discovery, network-isolation proof, or current-policy qualification",
	}
	schemaUsable, err := verifyRestoreSchema(ctx, tx, options, &report)
	if err != nil {
		return RestoreVerificationReport{}, err
	}
	if err := verifyRestorePage(ctx, tx, options, parsedCursor, schemaUsable, &report); err != nil {
		return RestoreVerificationReport{}, err
	}
	if err := ensureRestoreReportBound(report); err != nil {
		return RestoreVerificationReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RestoreVerificationReport{}, fmt.Errorf("complete read-only restore verification: %w", err)
	}
	return report, nil
}

// verifyRestorePage checks the parsed cursor against the inspected schema before
// traversing safe tenant data, then classifies this page's findings. The caller
// bounds the report and commits; it must discard the report on error.
func verifyRestorePage(ctx context.Context, tx pgx.Tx, options RestoreVerificationOptions, parsedCursor *restoreCursor, schemaUsable bool, report *RestoreVerificationReport) error {
	if parsedCursor != nil && (parsedCursor.SchemaVersion != report.SchemaVersion || parsedCursor.PrivilegeManifestVersion != report.PrivilegeManifestVersion) {
		return errors.New("restore verification cursor does not match the active schema or privilege manifest")
	}
	if schemaUsable {
		if err := verifyRestoreData(ctx, tx, options, parsedCursor, report); err != nil {
			return err
		}
	} else {
		report.Traversal = restoreTraversalBlocked
	}
	classifyRestorePage(report)
	return nil
}

// classifyRestorePage marks integrity findings without changing traversal or
// treating repairable and operational warnings as invalid retained evidence.
func classifyRestorePage(report *RestoreVerificationReport) {
	if len(report.Findings) > 0 {
		for _, finding := range report.Findings {
			if finding.Class == "integrity" {
				report.Integrity = restoreIntegrityInvalid
				break
			}
		}
	}
}

// validateRestoreVerificationOptions checks the tenant, recovery-point
// identifier, distinct runtime roles and caller-supplied row and cursor bounds
// before database access.
func validateRestoreVerificationOptions(options RestoreVerificationOptions) error {
	if !identity.ValidTenantID(options.TenantID) || audit.ValidateRecoveryPointID(options.RecoveryPoint) != nil || ValidateRuntimeDatabaseRoles(options.APIRole, options.WorkerRole) != nil || options.Limit < 1 || options.Limit > MaxRestoreVerificationLimit || len(options.Cursor) > MaxRestoreVerificationCursorBytes {
		return errors.New("restore verification requires a tenant UUID, bounded recovery point, distinct runtime roles, limit from 1 through 1000, and a bounded cursor")
	}
	return nil
}

// ValidateRestoreVerificationOptions validates the complete operator input
// without opening PostgreSQL.
func ValidateRestoreVerificationOptions(options RestoreVerificationOptions) error {
	return validateRestoreVerificationOptions(options)
}

// restoreBindingDigest detects accidental edits and continuation rebinding. It
// is deliberately not a MAC: the isolated workflow trusts its operator and
// retained report chain rather than treating a continuation as tamper proof.
func restoreBindingDigest(options RestoreVerificationOptions, schemaVersion int64, privilegeManifestVersion int, stage, after string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		options.RecoveryPoint,
		options.TenantID,
		options.APIRole,
		options.WorkerRole,
		strconv.Itoa(options.Limit),
		strconv.FormatInt(schemaVersion, 10),
		strconv.Itoa(privilegeManifestVersion),
		stage,
		after,
	}, "\x00")))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// parseRestoreCursor decodes a closed, bounded continuation and verifies its
// checkset, options binding and stage. An empty input starts a new traversal.
func parseRestoreCursor(options RestoreVerificationOptions) (*restoreCursor, error) {
	if options.Cursor == "" {
		//nolint:nilnil // A missing optional cursor starts the first page.
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(options.Cursor)
	if err != nil || len(raw) == 0 || len(raw) > MaxRestoreVerificationCursorBytes || jsondoc.Validate(raw) != nil {
		return nil, errors.New("restore verification cursor is malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cursor restoreCursor
	if err := decoder.Decode(&cursor); err != nil || cursor.FormatVersion != RestoreVerificationFormatVersion || cursor.CheckSetVersion != RestoreVerificationCheckSetVersion || cursor.BindingDigest != restoreBindingDigest(options, cursor.SchemaVersion, cursor.PrivilegeManifestVersion, cursor.Stage, cursor.After) || cursor.Limit != options.Limit || !validRestoreStage(cursor.Stage) || len(cursor.After) > 2048 {
		return nil, errors.New("restore verification cursor is malformed or rebound")
	}
	return &cursor, nil
}

// validRestoreStage reports whether name belongs to the fixed restore traversal
// sequence.
func validRestoreStage(name string) bool {
	for _, stage := range restoreVerificationStages() {
		if stage.name == name {
			return true
		}
	}
	return false
}

// encodeRestoreCursor binds a stage position to the verification options,
// schema and privilege manifest and enforces the encoded cursor limit.
func encodeRestoreCursor(options RestoreVerificationOptions, schemaVersion int64, stage, after string) (string, error) {
	cursor := restoreCursor{FormatVersion: RestoreVerificationFormatVersion, CheckSetVersion: RestoreVerificationCheckSetVersion, SchemaVersion: schemaVersion, PrivilegeManifestVersion: runtimePrivilegeManifestVersion, Limit: options.Limit, Stage: stage, After: after}
	cursor.BindingDigest = restoreBindingDigest(options, cursor.SchemaVersion, cursor.PrivilegeManifestVersion, cursor.Stage, cursor.After)
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode restore verification cursor: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > MaxRestoreVerificationCursorBytes {
		return "", errors.New("restore verification cursor exceeds its byte limit")
	}
	return encoded, nil
}

// ensureRestoreReportBound rejects reports that exceed the finding,
// decoded-byte or encoded-output ceiling.
func ensureRestoreReportBound(report RestoreVerificationReport) error {
	if len(report.Findings) > MaxRestoreVerificationFindings || report.DecodedBytes > MaxRestoreVerificationDecodedBytes {
		return errors.New("restore verification exceeded its result bounds")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode restore verification report: %w", err)
	}
	if len(raw) > MaxRestoreVerificationOutputBytes {
		return errors.New("restore verification report exceeds its encoded-byte limit")
	}
	return nil
}

// appendRestoreFinding appends a finding only while the report has room;
// traversal separately creates a continuation when that ceiling is reached.
func appendRestoreFinding(report *RestoreVerificationReport, finding RestoreFinding) {
	if len(report.Findings) >= MaxRestoreVerificationFindings {
		return
	}
	report.Findings = append(report.Findings, finding)
}

// integrityFinding constructs an error-class finding for retained evidence or
// schema that fails verification.
func integrityFinding(code, domain, recordID, disposition string) *RestoreFinding {
	return &RestoreFinding{Code: code, Class: "integrity", Severity: "error", Domain: domain, RecordID: recordID, Disposition: disposition}
}

// repairableFinding constructs a warning-class finding for derived state that
// an explicit repair can rebuild.
func repairableFinding(code, domain, recordID, disposition string) *RestoreFinding {
	return &RestoreFinding{Code: code, Class: "repairable", Severity: "warning", Domain: domain, RecordID: recordID, Disposition: disposition}
}

// operationalFinding constructs a warning-class finding that requires recovery
// review without declaring retained data invalid.
func operationalFinding(code, domain, recordID, disposition string) *RestoreFinding {
	return &RestoreFinding{Code: code, Class: "operational", Severity: "warning", Domain: domain, RecordID: recordID, Disposition: disposition}
}
