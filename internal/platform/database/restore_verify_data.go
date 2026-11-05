// Restore data checks validate paged retained documents, digests, same-tenant
// relationships and lifecycle metadata within fixed limits.

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

const (
	maxRestoreSmallDocumentBytes = 1 << 20
	maxRestoreSmallRecordBytes   = 2 << 20
	maxRestoreOutboxDataBytes    = 64 << 10
	maxRestoreRevisionMetaBytes  = maxRestoreSmallRecordBytes
	maxRestoreEvidenceMetaBytes  = maxRestoreSmallRecordBytes
	maxRestorePackageMetaBytes   = 32 << 20
	maxRestoreObservationBytes   = 34 << 20
	maxRestoreRetrievalBodyBytes = 64 << 20
	maxRestoreSnapshotRetrievals = 32
	maxRestoreRevisionFiles      = 16 * catalog.MaxSkillPackageFiles
)

// restoreVerificationStages returns the fixed scan order whose names and
// positions form the restore continuation protocol.
func restoreVerificationStages() []restoreStage {
	return []restoreStage{
		{"tenant-state", scanRestoreTenantState},
		{"tenant-settings", scanRestoreTenantSettings},
		{"publisher-verification", scanRestorePublisherVerification},
		{"resources", scanRestoreResources},
		{"memberships", scanRestoreMemberships},
		{"feedback", scanRestoreFeedback},
		{"personal-library", scanRestorePersonal},
		{"ratings", scanRestoreRatings},
		{"documents", scanRestoreDocuments},
		{"version-revisions", scanRestoreVersionRevisions},
		{"validation-evidence", scanRestoreValidationEvidence},
		{"listing-revisions", scanRestoreListingRevisions},
		{"source-inspections", scanRestoreSourceInspections},
		{"source-previews", scanRestoreSourcePreviews},
		{"source-revisions", scanRestoreSourceRevisions},
		{"source-artwork", scanRestoreSourceArtwork},
		{"source-snapshots", scanRestoreSourceSnapshots},
		{"source-retrieval-states", scanRestoreSourceRetrievalStates},
		{"observations", scanRestoreObservations},
		{"observation-occurrences", scanRestoreObservationOccurrences},
		{"publication-snapshots", scanRestorePublicationSnapshots},
		{"audit-events", scanRestoreAuditEvents},
		{"jobs", scanRestoreJobs},
		{"outbox", scanRestoreOutbox},
		{"idempotency-v2", scanRestoreIdempotencyV2},
		{"webhook-deliveries", scanRestoreWebhookDeliveries},
	}
}

// verifyRestoreData resumes tenant-scoped scans and accumulates findings until
// traversal completes or a row, decoded-byte or finding budget requires
// continuation. It does not repair retained state.
func verifyRestoreData(ctx context.Context, tx pgx.Tx, options RestoreVerificationOptions, cursor *restoreCursor, report *RestoreVerificationReport) error {
	stages := restoreVerificationStages()
	stageIndex := 0
	after := ""
	if cursor != nil {
		stageIndex = slices.IndexFunc(stages, func(stage restoreStage) bool { return stage.name == cursor.Stage })
		if stageIndex < 0 {
			return errors.New("restore verification cursor names an unknown stage")
		}
		after = cursor.After
	}
	for ; stageIndex < len(stages); stageIndex++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := options.Limit - report.Processed
		if remaining <= 0 {
			return continueRestoreReport(options, report, stages[stageIndex].name, after)
		}
		records, more, err := stages[stageIndex].scan(ctx, tx, options.TenantID, after, remaining)
		if err != nil {
			return fmt.Errorf("scan restored %s: %w", stages[stageIndex].name, err)
		}
		last := after
		for _, record := range records {
			if report.DecodedBytes+record.decodedBytes > MaxRestoreVerificationDecodedBytes {
				return continueRestoreReport(options, report, stages[stageIndex].name, last)
			}
			report.Processed++
			report.DecodedBytes += record.decodedBytes
			last = record.key
			if record.finding != nil {
				appendRestoreFinding(report, *record.finding)
			}
			if len(report.Findings) >= MaxRestoreVerificationFindings {
				return continueRestoreReport(options, report, stages[stageIndex].name, last)
			}
		}
		if more || report.Processed >= options.Limit {
			return continueRestoreReport(options, report, stages[stageIndex].name, last)
		}
		after = ""
	}
	return nil
}

// continueRestoreReport marks traversal incomplete and records a bounded cursor
// after the last accounted-for record.
func continueRestoreReport(options RestoreVerificationOptions, report *RestoreVerificationReport, stage, after string) error {
	cursor, err := encodeRestoreCursor(options, report.SchemaVersion, stage, after)
	if err != nil {
		return err
	}
	report.Traversal = restoreTraversalMore
	report.Continuation = cursor
	return nil
}

// boundedStageLimit caps the remaining row allowance at a scanner's lower
// per-page ceiling.
func boundedStageLimit(limit, maximum int) int {
	if limit < maximum {
		return limit
	}
	return maximum
}

// scanRestoreTenantState emits one tenant-level record warning when no active
// realm, membership and tenant-administrator grant can authorize recovery
// administration.
func scanRestoreTenantState(ctx context.Context, tx pgx.Tx, tenantID, after string, _ int) ([]restoreRecord, bool, error) {
	if after != "" {
		return nil, false, nil
	}
	var activeAdministrator bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1
    FROM tenant_memberships membership
    JOIN tenant_identity_realms realm ON realm.tenant_id=membership.tenant_id AND realm.id=membership.realm_id AND realm.state='active'
    JOIN role_assignments assignment ON assignment.tenant_id=membership.tenant_id AND assignment.membership_id=membership.id
   WHERE membership.tenant_id=$1 AND membership.state='active' AND assignment.state='active'
     AND assignment.namespace_id IS NULL AND assignment.role='tenant_admin')`, tenantID).Scan(&activeAdministrator); err != nil {
		return nil, false, err
	}
	record := restoreRecord{key: tenantID}
	if !activeAdministrator {
		record.finding = operationalFinding("RESTORE_NO_ACTIVE_TENANT_ADMIN", "identity", tenantID, "review administrator recovery before enabling traffic; retained data integrity is unaffected")
	}
	return []restoreRecord{record}, false, nil
}

// scanRestoreResources pages tenant resources and flags inconsistent tombstone
// markers or deletion of resources with adoption, review or release history.
func scanRestoreResources(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	rows, err := tx.Query(ctx, `
SELECT resource.id::text,resource.deleted_at IS NOT NULL,
       resource.deleted_at IS NULL AND (resource.deleted_by IS NOT NULL OR resource.delete_reason<>''),
       resource.deleted_at IS NOT NULL AND (resource.deleted_by IS NULL OR resource.delete_reason=''),
       EXISTS(SELECT 1 FROM resource_versions version JOIN resource_version_revisions revision ON revision.tenant_id=version.tenant_id AND revision.resource_version_id=version.id WHERE version.tenant_id=resource.tenant_id AND version.resource_id=resource.id AND revision.adoption_provenance IS NOT NULL),
       EXISTS(SELECT 1 FROM catalog_listings listing JOIN review_submissions submission ON submission.tenant_id=listing.tenant_id AND submission.listing_id=listing.id WHERE listing.tenant_id=resource.tenant_id AND listing.resource_id=resource.id),
       EXISTS(SELECT 1 FROM catalog_listings listing JOIN listing_releases release ON release.tenant_id=listing.tenant_id AND release.listing_id=listing.id WHERE listing.tenant_id=resource.tenant_id AND listing.resource_id=resource.id)
  FROM resources resource
 WHERE resource.tenant_id=$1 AND ($2='' OR resource.id>nullif($2,'')::uuid)
 ORDER BY resource.id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var id string
		var deleted, liveMarkers, incompleteMarkers, adopted, reviewed, released bool
		if err := rows.Scan(&id, &deleted, &liveMarkers, &incompleteMarkers, &adopted, &reviewed, &released); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: id}
		if liveMarkers || incompleteMarkers || deleted && (adopted || reviewed || released) {
			record.finding = integrityFinding("RESTORE_RESOURCE_TOMBSTONE_INVALID", "catalog", id, "investigate the retained resource lifecycle evidence; do not expose or delete the row")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}

// scanRestoreMemberships pages tenant memberships and flags active direct,
// group or namespace grants retained by an offboarded membership.
func scanRestoreMemberships(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	rows, err := tx.Query(ctx, `
SELECT membership.id::text,membership.state,
       (EXISTS(SELECT 1 FROM role_assignments assignment WHERE assignment.tenant_id=membership.tenant_id AND assignment.membership_id=membership.id AND assignment.state='active')
        OR EXISTS(SELECT 1 FROM access_group_members assignment WHERE assignment.tenant_id=membership.tenant_id AND assignment.membership_id=membership.id AND assignment.state='active')),
       EXISTS(SELECT 1 FROM namespace_memberships assignment WHERE assignment.tenant_id=membership.tenant_id AND assignment.membership_id=membership.id AND assignment.state='active')
  FROM tenant_memberships membership
 WHERE membership.tenant_id=$1 AND ($2='' OR membership.id>nullif($2,'')::uuid)
 ORDER BY membership.id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var id, state string
		var activeRole, activeNamespace bool
		if err := rows.Scan(&id, &state, &activeRole, &activeNamespace); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: id}
		if state == "offboarded" && (activeRole || activeNamespace) {
			record.finding = integrityFinding("RESTORE_OFFBOARDED_MEMBERSHIP_HAS_ACTIVE_GRANTS", "identity", id, "keep traffic disabled and investigate the atomic offboarding transition")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}

// scanRestoreDocuments verifies a bounded page of authoritative document bytes,
// sizes and digests without loading oversized blobs.
func scanRestoreDocuments(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 4)
	rows, err := tx.Query(ctx, `
SELECT id,tenant_id::text,content_digest,media_type,size_bytes,octet_length(content),
       CASE WHEN octet_length(content)<=$4 THEN content ELSE NULL END,created_at
  FROM document_blobs
 WHERE tenant_id=$1 AND ($2='' OR id>$2)
 ORDER BY id LIMIT $3`, tenantID, after, limit+1, catalog.MaxCatalogDocumentBytes)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var document catalog.DocumentBlob
		var storedSize int64
		if err := rows.Scan(&document.ID, &document.TenantID, &document.Digest, &document.MediaType, &document.Size, &storedSize, &document.Content, &document.CreatedAt); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: document.ID, decodedBytes: int64(len(document.Content))}
		if storedSize > catalog.MaxCatalogDocumentBytes || catalog.ValidateDocumentBlob(tenantID, document) != nil {
			record.finding = integrityFinding("RESTORE_CATALOG_DOCUMENT_INVALID", "catalog_document", document.ID, "do not serve the document; investigate the authoritative bytes and their immutable references")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}

// scanRestoreVersionRevisions bounds representation and package metadata in SQL
// before checking immutable revision, evidence and document bindings, including
// deleted parents.
func scanRestoreVersionRevisions(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	// A valid revision can carry up to 32 MiB of decoded representation data
	// plus bounded revision and evidence metadata. Two rows leave room for the
	// package inventory while keeping the scanner page below 80 MiB.
	limit = boundedStageLimit(limit, 2)
	rows, err := tx.Query(ctx, `SELECT id::text FROM resource_version_revisions WHERE tenant_id=$1 AND ($2='' OR id>nullif($2,'')::uuid) ORDER BY id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	records := make([]restoreRecord, 0, len(ids))
	for _, id := range ids {
		var representationCount, inventoryCount, referenceCount int
		var representationBytes, inventoryBytes, referenceBytes, revisionBytes, evidenceBytes int64
		var missingDocument bool
		if err := tx.QueryRow(ctx, `
SELECT (SELECT count(*) FROM (SELECT 1 FROM representations WHERE tenant_id=$1 AND version_revision_id=$2 LIMIT 17) bounded),
	   (SELECT coalesce(sum(
	       1024
	       + octet_length(bounded.role)
	       + octet_length(bounded.media_type)
	       + octet_length(bounded.profile_id)
	       + coalesce(octet_length(bounded.external_url),0)
	       + coalesce(octet_length(bounded.inline_document::text),0)
	       + coalesce(octet_length(bounded.plugin_inspection::text),0)
	       + octet_length(bounded.content_digest)
	       + coalesce(octet_length(bounded.document_blob_id),0)
	       + octet_length(bounded.validation_state)
	       + octet_length(bounded.findings::text)
	       + octet_length(bounded.normalized_facts::text)
	       + octet_length(bounded.unknown_properties::text)
	       + coalesce(octet_length(bounded.skill_package_structure_state),0)
	       + coalesce(octet_length(bounded.skill_package_root),0)
	       + coalesce(octet_length(bounded.skill_package_malware_state),0)
	       + coalesce(octet_length(bounded.skill_package_secrets_state),0)
	       + coalesce(octet_length(bounded.skill_package_license_state),0)
	       + coalesce(octet_length(bounded.skill_package_sbom_state),0)
	       + coalesce(octet_length(bounded.skill_package_provenance_state),0)
	     ),0)
	      FROM (SELECT role,media_type,profile_id,external_url,inline_document,content_digest,document_blob_id,validation_state,
	                   findings,normalized_facts,unknown_properties,skill_package_structure_state,skill_package_root,
	                   skill_package_malware_state,skill_package_secrets_state,skill_package_license_state,
	                   skill_package_sbom_state,skill_package_provenance_state,plugin_inspection
	              FROM representations
	             WHERE tenant_id=$1 AND version_revision_id=$2
	             LIMIT 17) bounded),
	   (SELECT count(*) FROM (SELECT 1 FROM skill_file_inventory inventory JOIN representations representation ON representation.tenant_id=inventory.tenant_id AND representation.id=inventory.representation_id WHERE representation.tenant_id=$1 AND representation.version_revision_id=$2 LIMIT $3) bounded),
	   (SELECT coalesce(sum(256 + octet_length(path) + octet_length(media_type) + octet_length(content_digest)),0) FROM (SELECT inventory.path,inventory.media_type,inventory.content_digest FROM skill_file_inventory inventory JOIN representations representation ON representation.tenant_id=inventory.tenant_id AND representation.id=inventory.representation_id WHERE representation.tenant_id=$1 AND representation.version_revision_id=$2 LIMIT $3) bounded),
	   (SELECT count(*) FROM (SELECT 1 FROM artifact_references reference JOIN representations representation ON representation.tenant_id=reference.tenant_id AND representation.id=reference.representation_id WHERE representation.tenant_id=$1 AND representation.version_revision_id=$2 LIMIT $4) bounded),
	   (SELECT coalesce(sum(256 + octet_length(reference_kind) + coalesce(octet_length(git_repository_url),0) + coalesce(octet_length(git_commit),0) + coalesce(octet_length(git_directory),0) + coalesce(octet_length(oci_reference),0)),0) FROM (SELECT reference.reference_kind,reference.git_repository_url,reference.git_commit,reference.git_directory,reference.oci_reference FROM artifact_references reference JOIN representations representation ON representation.tenant_id=reference.tenant_id AND representation.id=reference.representation_id WHERE representation.tenant_id=$1 AND representation.version_revision_id=$2 LIMIT $4) bounded),
	   EXISTS(SELECT 1 FROM representations WHERE tenant_id=$1 AND version_revision_id=$2 AND NOT external_reference_only AND plugin_inspection IS NULL AND inline_document IS NULL AND document_blob_id IS NULL),
	   coalesce((
	     SELECT 512::bigint + octet_length(revision.content_set_digest) + octet_length(revision.findings::text)
	          + coalesce(octet_length(revision.adoption_provenance::text),0) + octet_length(revision.created_by)
	       FROM resource_version_revisions revision
	      WHERE revision.tenant_id=$1 AND revision.id=$2
	   ),0),
	   coalesce((
	     SELECT 1024::bigint + octet_length(evidence.subject_digest) + octet_length(evidence.digest)
	          + octet_length(evidence.state) + octet_length(evidence.findings::text)
	          + octet_length(evidence.validation_profile) + octet_length(evidence.policy_version)
	          + octet_length(evidence.rights::text) + octet_length(evidence.trust::text)
	          + octet_length(evidence.external_retrievals::text)
	       FROM resource_version_revisions revision
	       JOIN LATERAL (
	         SELECT run.evidence_set_id
	           FROM version_validation_runs run
	          WHERE run.tenant_id=revision.tenant_id AND run.version_revision_id=revision.id
	          ORDER BY run.run_number DESC
	          LIMIT 1
	       ) validation ON true
	       JOIN evidence_sets evidence
	         ON evidence.tenant_id=revision.tenant_id
	        AND evidence.id=validation.evidence_set_id
	      WHERE revision.tenant_id=$1 AND revision.id=$2
	   ),0)`, tenantID, id, maxRestoreRevisionFiles+1, 33).Scan(&representationCount, &representationBytes, &inventoryCount, &inventoryBytes, &referenceCount, &referenceBytes, &missingDocument, &revisionBytes, &evidenceBytes); err != nil {
			return nil, false, err
		}
		packageBytes := inventoryBytes + referenceBytes
		record := restoreRecord{key: id, decodedBytes: representationBytes + packageBytes + revisionBytes + evidenceBytes}
		if representationCount < 1 || representationCount > 16 || inventoryCount > maxRestoreRevisionFiles || referenceCount > 32 || representationBytes > catalog.MaxPublicationProjectionBytes || packageBytes > maxRestorePackageMetaBytes || revisionBytes > maxRestoreRevisionMetaBytes || evidenceBytes > maxRestoreEvidenceMetaBytes {
			// Oversized corrupt metadata was measured in PostgreSQL but was not
			// decoded into the process. Count no bytes here so the record can be
			// reported instead of repeating an unchanged continuation forever.
			record.decodedBytes = 0
			record.finding = integrityFinding("RESTORE_VERSION_REVISION_BOUNDS_INVALID", "version_revision", id, "investigate the retained revision before loading or serving its representation set")
			records = append(records, record)
			continue
		}
		revision, evidence, readErr := readVersionRevisionIncludingDeleted(ctx, tx, tenantID, id, true)
		if readErr != nil {
			record.finding = integrityFinding("RESTORE_VERSION_REVISION_UNREADABLE", "version_revision", id, "investigate the retained revision, evidence, package inventory, and document bindings")
		} else if evidenceErr := catalog.ValidateEvidenceSetIntegrity(evidence); evidenceErr != nil {
			record.finding = integrityFinding("RESTORE_VALIDATION_EVIDENCE_INVALID", "validation_evidence", evidence.ID, "investigate the immutable validation evidence and digest")
		} else if integrityErr := catalog.ValidateVersionRevisionIntegrity(revision, evidence); integrityErr != nil {
			record.finding = integrityFinding("RESTORE_VERSION_REVISION_INVALID", "version_revision", id, "investigate the immutable representation set, package handoff, document, and retrieval bindings")
		} else if missingDocument {
			record.finding = integrityFinding("RESTORE_VERSION_REVISION_DOCUMENT_INVALID", "version_revision", id, "the retained representation has no supported current document binding")
		}
		records = append(records, record)
	}
	return records, more, nil
}

// scanRestoreValidationEvidence checks bounded immutable evidence against its
// validation run, subject digest and creation time.
func scanRestoreValidationEvidence(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 8)
	rows, err := tx.Query(ctx, `
SELECT evidence.id::text,
       1024::bigint + octet_length(evidence.subject_digest) + octet_length(evidence.digest)
       + octet_length(evidence.state) + octet_length(evidence.findings::text)
       + octet_length(evidence.validation_profile) + octet_length(evidence.policy_version)
       + octet_length(evidence.rights::text) + octet_length(evidence.trust::text)
       + octet_length(evidence.external_retrievals::text)
  FROM evidence_sets evidence
 WHERE evidence.tenant_id=$1 AND ($2='' OR evidence.id>nullif($2,'')::uuid)
 ORDER BY evidence.id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	type evidencePosition struct {
		id   string
		size int64
	}
	positions := make([]evidencePosition, 0, limit+1)
	for rows.Next() {
		var position evidencePosition
		if err := rows.Scan(&position.id, &position.size); err != nil {
			rows.Close()
			return nil, false, err
		}
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	more := len(positions) > limit
	if more {
		positions = positions[:limit]
	}
	records := make([]restoreRecord, 0, len(positions))
	for _, position := range positions {
		record := restoreRecord{key: position.id, decodedBytes: position.size}
		if position.size > maxRestoreEvidenceMetaBytes {
			record.decodedBytes = 0
			record.finding = integrityFinding("RESTORE_VALIDATION_EVIDENCE_INVALID", "validation_evidence", position.id, "investigate the oversized immutable validation run before loading its evidence")
			records = append(records, record)
			continue
		}
		var evidence catalog.EvidenceSet
		var rawFindings, rawRights, rawTrust, rawRetrievals []byte
		var revisionID, revisionDigest *string
		var expiresAt, runCreatedAt *time.Time
		if err := tx.QueryRow(ctx, `
SELECT evidence.id::text,evidence.tenant_id::text,evidence.subject_digest,evidence.digest,evidence.state,
       evidence.validation_profile,evidence.policy_version,evidence.created_at,
       evidence.findings,evidence.rights,evidence.trust,evidence.external_retrievals,
       run.version_revision_id::text,run.expires_at,run.created_at,revision.content_set_digest
  FROM evidence_sets evidence
  LEFT JOIN version_validation_runs run
    ON run.tenant_id=evidence.tenant_id AND run.evidence_set_id=evidence.id
  LEFT JOIN resource_version_revisions revision
    ON revision.tenant_id=run.tenant_id AND revision.id=run.version_revision_id
 WHERE evidence.tenant_id=$1 AND evidence.id=$2`, tenantID, position.id).Scan(&evidence.ID, &evidence.TenantID, &evidence.SubjectDigest, &evidence.Digest, &evidence.State, &evidence.ValidationProfile, &evidence.PolicyVersion, &evidence.CreatedAt, &rawFindings, &rawRights, &rawTrust, &rawRetrievals, &revisionID, &expiresAt, &runCreatedAt, &revisionDigest); err != nil {
			return nil, false, err
		}
		decodeErr := json.Unmarshal(rawFindings, &evidence.Findings)
		if decodeErr == nil {
			decodeErr = json.Unmarshal(rawRights, &evidence.LegalRights)
		}
		if decodeErr == nil {
			decodeErr = json.Unmarshal(rawTrust, &evidence.Trust)
		}
		if decodeErr == nil {
			decodeErr = json.Unmarshal(rawRetrievals, &evidence.ExternalRetrievals)
		}
		if expiresAt != nil {
			evidence.ExpiresAt = *expiresAt
		}
		validationErr := catalog.ValidateEvidenceSetIntegrity(evidence)
		if decodeErr != nil || revisionID == nil || revisionDigest == nil || runCreatedAt == nil || !samePostgresTime(*runCreatedAt, evidence.CreatedAt) || *revisionDigest != evidence.SubjectDigest || validationErr != nil {
			record.finding = integrityFinding("RESTORE_VALIDATION_EVIDENCE_INVALID", "validation_evidence", evidence.ID, "investigate the immutable validation run, subject binding, and evidence digest")
		}
		records = append(records, record)
	}
	return records, more, nil
}

// scanRestoreListingRevisions bounds metadata before loading immutable listing
// revisions and checking their retained digests, including deleted parents.
func scanRestoreListingRevisions(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	// Each retained listing revision is limited to 1 MiB before decoding.
	limit = boundedStageLimit(limit, 64)
	rows, err := tx.Query(ctx, `
SELECT id::text,
       1024::bigint + octet_length(display_name) + octet_length(description)
       + octet_length(tags::text) + octet_length(categories::text)
       + octet_length(representative_queries::text) + octet_length(relationships::text)
       + octet_length(extensions::text) + octet_length(coalesce(documentation_url,''))
       + octet_length(coalesce(support_url,'')) + octet_length(license)
       + octet_length(coalesce(terms_url,'')) + octet_length(visibility)
       + octet_length(digest) + octet_length(created_by) AS decoded_bytes
  FROM listing_revisions
 WHERE tenant_id=$1 AND ($2='' OR id>nullif($2,'')::uuid)
 ORDER BY id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	type listingPosition struct {
		id   string
		size int64
	}
	positions := make([]listingPosition, 0, limit+1)
	for rows.Next() {
		var position listingPosition
		if err := rows.Scan(&position.id, &position.size); err != nil {
			rows.Close()
			return nil, false, err
		}
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	more := len(positions) > limit
	if more {
		positions = positions[:limit]
	}
	records := make([]restoreRecord, 0, len(positions))
	for _, position := range positions {
		record := restoreRecord{key: position.id, decodedBytes: position.size}
		if position.size > maxRestoreSmallRecordBytes {
			record.decodedBytes = 0
			record.finding = integrityFinding("RESTORE_LISTING_REVISION_INVALID", "listing_revision", position.id, "investigate the oversized immutable listing revision before loading or serving it")
			records = append(records, record)
			continue
		}
		revision, readErr := readListingRevisionIncludingDeleted(ctx, tx, tenantID, position.id, true)
		if readErr != nil {
			record.finding = integrityFinding("RESTORE_LISTING_REVISION_UNREADABLE", "listing_revision", position.id, "investigate the immutable listing revision and its retained JSON fields")
		} else if catalog.ValidateListingRevisionDigest(revision) != nil {
			record.finding = integrityFinding("RESTORE_LISTING_REVISION_INVALID", "listing_revision", position.id, "investigate the immutable listing revision and digest")
		}
		records = append(records, record)
	}
	return records, more, nil
}

// scanRestoreSourceInspections checks bounded inspection documents against
// their retained identity, tenant and timestamp columns without contacting the
// source.
func scanRestoreSourceInspections(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	return scanRestoreJSONDocuments(ctx, tx, tenantID, after, limit, "source_inspections", func(id, tenant string, raw []byte, expires, created time.Time) *RestoreFinding {
		var inspection federation.Inspection
		if json.Unmarshal(raw, &inspection) != nil {
			return integrityFinding("RESTORE_SOURCE_INSPECTION_INVALID", "source_inspection", id, "retain but do not authorize a source from this inspection; investigate its immutable document binding")
		}
		validationErr := federation.ValidateRetainedInspectionIntegrity(inspection)
		if validationErr != nil || inspection.ID != id || inspection.TenantID != tenant || !samePostgresTime(inspection.ExpiresAt, expires) || !samePostgresTime(inspection.CreatedAt, created) {
			return integrityFinding("RESTORE_SOURCE_INSPECTION_INVALID", "source_inspection", id, "retain but do not authorize a source from this inspection; investigate its immutable document binding")
		}

		return nil
	})
}

// scanRestoreJSONDocuments pages a caller-selected source-evidence table,
// rejects oversized or invalid JSON before validation, and passes row bindings
// to validate.
func scanRestoreJSONDocuments(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int, table string, validate func(string, string, []byte, time.Time, time.Time) *RestoreFinding) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 16)
	query := `SELECT id::text,tenant_id::text,octet_length(document::text),CASE WHEN octet_length(document::text)<=$4 THEN document ELSE NULL END,expires_at,created_at FROM ` + pgx.Identifier{table}.Sanitize() + ` WHERE tenant_id=$1 AND ($2='' OR id>nullif($2,'')::uuid) ORDER BY id LIMIT $3`
	rows, err := tx.Query(ctx, query, tenantID, after, limit+1, maxRestoreSmallDocumentBytes)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var id, rowTenant string
		var size int64
		var raw []byte
		var expires, created time.Time
		if err := rows.Scan(&id, &rowTenant, &size, &raw, &expires, &created); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: id, decodedBytes: int64(len(raw))}
		if size > maxRestoreSmallDocumentBytes || jsondoc.Validate(raw) != nil {
			record.finding = integrityFinding("RESTORE_BOUNDED_DOCUMENT_INVALID", "source_evidence", id, "retain and investigate the oversized or invalid immutable source evidence")
		} else {
			record.finding = validate(id, rowTenant, raw, expires, created)
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}

// scanRestoreSourcePreviews verifies bounded source-change documents against
// their immutable digest, source and timestamp columns.
func scanRestoreSourcePreviews(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 16)
	rows, err := tx.Query(ctx, `SELECT id::text,1024::bigint+octet_length(digest)+octet_length(document::text) FROM source_change_previews WHERE tenant_id=$1 AND ($2='' OR id>nullif($2,'')::uuid) ORDER BY id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	type previewPosition struct {
		id   string
		size int64
	}
	positions := make([]previewPosition, 0, limit+1)
	for rows.Next() {
		var position previewPosition
		if err := rows.Scan(&position.id, &position.size); err != nil {
			rows.Close()
			return nil, false, err
		}
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	more := len(positions) > limit
	if more {
		positions = positions[:limit]
	}
	records := make([]restoreRecord, 0, len(positions))
	for _, position := range positions {
		record := restoreRecord{key: position.id, decodedBytes: position.size}
		if position.size > maxRestoreSmallRecordBytes {
			record.decodedBytes = 0
			record.finding = integrityFinding("RESTORE_SOURCE_PREVIEW_INVALID", "source_change_preview", position.id, "do not use this oversized preview for a source transition; investigate its immutable row")
			records = append(records, record)
			continue
		}
		var id, rowTenant, sourceID, digest string
		var raw []byte
		var expires, created time.Time
		if err := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,source_id::text,digest,document,expires_at,created_at FROM source_change_previews WHERE tenant_id=$1 AND id=$2`, tenantID, position.id).Scan(&id, &rowTenant, &sourceID, &digest, &raw, &expires, &created); err != nil {
			return nil, false, err
		}
		var preview federation.ChangePreview
		if jsondoc.Validate(raw) != nil || json.Unmarshal(raw, &preview) != nil || preview.ID != id || preview.TenantID != rowTenant || preview.SourceID != sourceID || preview.Digest != digest || !samePostgresTime(preview.ExpiresAt, expires) || !samePostgresTime(preview.CreatedAt, created) || federation.ValidateChangePreviewIntegrity(preview) != nil {
			record.finding = integrityFinding("RESTORE_SOURCE_PREVIEW_INVALID", "source_change_preview", id, "do not use this preview for a source transition; investigate its immutable digest and row binding")
		}
		records = append(records, record)
	}
	return records, more, nil
}

// scanRestoreSourceRevisions pages source/revision pairs and checks immutable
// configuration digests against typed policy, credential and topology
// projections.
func scanRestoreSourceRevisions(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	afterSource, afterRevision, err := decodeSourceRevisionKey(after)
	if err != nil {
		return nil, false, err
	}
	limit = boundedStageLimit(limit, 16)
	rows, err := tx.Query(ctx, `
SELECT source_id::text,revision,
       2048::bigint + octet_length(configuration::text) + octet_length(configuration_digest)
       + octet_length(coalesce(retrieval_mode,'')) + octet_length(coalesce(update_mode,''))
       + octet_length(coalesce(registry_list_url,'')) + octet_length(coalesce(policy_id,''))
       + octet_length(coalesce(policy_digest,'')) + octet_length(coalesce(credential_revision,''))
  FROM federation_source_revisions revision_record
 WHERE tenant_id=$1 AND ($2='' OR (source_id,revision)>(nullif($2,'')::uuid,$3))
 ORDER BY source_id,revision LIMIT $4`, tenantID, afterSource, afterRevision, limit+1)
	if err != nil {
		return nil, false, err
	}
	type revisionPosition struct {
		sourceID string
		revision int64
		size     int64
	}
	positions := make([]revisionPosition, 0, limit+1)
	for rows.Next() {
		var position revisionPosition
		if err := rows.Scan(&position.sourceID, &position.revision, &position.size); err != nil {
			rows.Close()
			return nil, false, err
		}
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	more := len(positions) > limit
	if more {
		positions = positions[:limit]
	}
	records := make([]restoreRecord, 0, len(positions))
	for _, position := range positions {
		key := encodeSourceRevisionKey(position.sourceID, position.revision)
		record := restoreRecord{key: key, decodedBytes: position.size}
		if position.size > maxRestoreSmallRecordBytes {
			record.decodedBytes = 0
			record.finding = integrityFinding("RESTORE_SOURCE_REVISION_INVALID", "source_revision", key, "pause the source and investigate its oversized immutable configuration")
			records = append(records, record)
			continue
		}
		var sourceID, retrievalMode, updateMode, registryListURL, policyID, policyDigest, credentialRevision string
		var revision, policyRevision int64
		var digestValid bool
		var raw, assignmentsRaw, referralsRaw []byte
		if err := tx.QueryRow(ctx, `
SELECT source_id::text,revision,configuration_digest='sha256:' || encode(digest(configuration::text,'sha256'),'hex'),configuration,
       coalesce(retrieval_mode,''),coalesce(update_mode,''),coalesce(registry_list_url,''),coalesce(policy_id,''),coalesce(policy_revision,0),coalesce(policy_digest,''),coalesce(credential_revision,''),
	       coalesce((SELECT jsonb_agg(jsonb_build_object('host',bounded_audience.discovery_host,'audience',bounded_audience.audience) ORDER BY bounded_audience.discovery_host,bounded_audience.audience)
	                   FROM (SELECT audience.discovery_host,audience.audience
	                           FROM federation_source_audiences audience
	                          WHERE audience.tenant_id=revision_record.tenant_id AND audience.source_id=revision_record.source_id AND audience.source_revision=revision_record.revision
	                          ORDER BY audience.discovery_host,audience.audience LIMIT 17) bounded_audience),'[]'::jsonb),
	       coalesce((SELECT jsonb_agg(bounded_referral.target_source_id::text ORDER BY bounded_referral.target_source_id)
	                   FROM (SELECT referral.target_source_id
	                           FROM federation_source_referrals referral
	                          WHERE referral.tenant_id=revision_record.tenant_id AND referral.source_id=revision_record.source_id AND referral.source_revision=revision_record.revision
	                          ORDER BY referral.target_source_id LIMIT 13) bounded_referral),'[]'::jsonb)
  FROM federation_source_revisions revision_record
 WHERE tenant_id=$1 AND source_id=$2 AND revision=$3`, tenantID, position.sourceID, position.revision).Scan(&sourceID, &revision, &digestValid, &raw, &retrievalMode, &updateMode, &registryListURL, &policyID, &policyRevision, &policyDigest, &credentialRevision, &assignmentsRaw, &referralsRaw); err != nil {
			return nil, false, err
		}
		var source federation.Source
		incomplete := retrievalMode == "" || updateMode == "" || policyID == "" || policyRevision == 0 || policyDigest == ""
		switch {
		case !digestValid || jsondoc.Validate(raw) != nil || json.Unmarshal(raw, &source) != nil || source.ID != sourceID || source.TenantID != tenantID || source.Revision != revision || len(source.DiscoveryAssignments) > 16 || len(source.Referrals.ApprovedSourceIDs) > federation.MaxFederatedSources:
			record.finding = integrityFinding("RESTORE_SOURCE_REVISION_INVALID", "source_revision", key, "pause the source and investigate its immutable configuration and digest")
		case incomplete || (string(source.RetrievalMode) != retrievalMode || string(source.UpdateMode) != updateMode || source.RegistryListURL != registryListURL || source.PolicyID != policyID || source.PolicyRevision != policyRevision || source.PolicyDigest != policyDigest || source.CredentialRevision != credentialRevision || !sameSourceRevisionTopology(source, assignmentsRaw, referralsRaw)):
			record.finding = integrityFinding("RESTORE_SOURCE_REVISION_PROJECTION_INVALID", "source_revision", key, "pause the source and investigate its typed configuration projections")
		}
		records = append(records, record)
	}
	return records, more, nil
}

// encodeSourceRevisionKey formats the source UUID and zero-padded revision used
// to resume the source-revision scan.
func encodeSourceRevisionKey(sourceID string, revision int64) string {
	return sourceID + "/" + fmt.Sprintf("%020d", revision)
}

// decodeSourceRevisionKey parses a source UUID and positive revision; empty
// input represents the beginning of the scan.
func decodeSourceRevisionKey(value string) (string, int64, error) {
	if value == "" {
		return "", 0, nil
	}
	source, revisionText, ok := strings.Cut(value, "/")
	if !ok || !identity.ValidTenantID(source) {
		return "", 0, errors.New("restore source-revision cursor position is invalid")
	}
	revision, err := strconv.ParseInt(revisionText, 10, 64)
	if err != nil || revision < 1 {
		return "", 0, errors.New("restore source-revision cursor position is invalid")
	}
	return source, revision, nil
}

// sameSourceRevisionTopology compares retained audience and referral
// projections with configuration as order-independent lists without modifying
// the source.
func sameSourceRevisionTopology(source federation.Source, assignmentsRaw, referralsRaw []byte) bool {
	var assignments []federation.DiscoveryAssignment
	var referrals []string
	if json.Unmarshal(assignmentsRaw, &assignments) != nil || json.Unmarshal(referralsRaw, &referrals) != nil {
		return false
	}
	sort.Slice(assignments, func(i, j int) bool {
		if assignments[i].Host != assignments[j].Host {
			return assignments[i].Host < assignments[j].Host
		}
		return assignments[i].Audience < assignments[j].Audience
	})
	expectedAssignments := append([]federation.DiscoveryAssignment(nil), source.DiscoveryAssignments...)
	sort.Slice(expectedAssignments, func(i, j int) bool {
		if expectedAssignments[i].Host != expectedAssignments[j].Host {
			return expectedAssignments[i].Host < expectedAssignments[j].Host
		}
		return expectedAssignments[i].Audience < expectedAssignments[j].Audience
	})
	sort.Strings(referrals)
	expectedReferrals := append([]string(nil), source.Referrals.ApprovedSourceIDs...)
	sort.Strings(expectedReferrals)
	return slices.Equal(assignments, expectedAssignments) && slices.Equal(referrals, expectedReferrals)
}

// scanRestoreSourceSnapshots checks bounded snapshot documents, row metadata,
// retrieval attempts and occurrence counts without changing the last complete
// source pointer.
func scanRestoreSourceSnapshots(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 16)
	rows, err := tx.Query(ctx, `
SELECT snapshot.id::text,1024::bigint+octet_length(snapshot.state)+octet_length(snapshot.body_digest)+octet_length(snapshot.details::text)
  FROM source_snapshots snapshot
 WHERE snapshot.tenant_id=$1 AND ($2='' OR snapshot.id>nullif($2,'')::uuid)
 ORDER BY snapshot.id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	type snapshotPosition struct {
		id   string
		size int64
	}
	positions := make([]snapshotPosition, 0, limit+1)
	for rows.Next() {
		var position snapshotPosition
		if err := rows.Scan(&position.id, &position.size); err != nil {
			rows.Close()
			return nil, false, err
		}
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	more := len(positions) > limit
	if more {
		positions = positions[:limit]
	}
	records := make([]restoreRecord, 0, len(positions))
	for _, position := range positions {
		record := restoreRecord{key: position.id, decodedBytes: position.size}
		if position.size > maxRestoreSmallRecordBytes {
			record.decodedBytes = 0
			record.finding = integrityFinding("RESTORE_SOURCE_SNAPSHOT_INVALID", "source_snapshot", position.id, "preserve the last complete source pointer and investigate the oversized immutable snapshot evidence")
			records = append(records, record)
			continue
		}
		var id, sourceID, state, bodyDigest string
		var sourceRevision *int64
		var enumerationComplete, withdrawalAuthority bool
		var memberCount, occurrenceCount int
		var raw []byte
		var started time.Time
		var completed *time.Time
		if err := tx.QueryRow(ctx, `
SELECT snapshot.id::text,snapshot.source_id::text,snapshot.source_revision,snapshot.state,snapshot.enumeration_complete,snapshot.withdrawal_authority,snapshot.member_count,snapshot.body_digest,
       snapshot.details,snapshot.started_at,snapshot.completed_at,
       (SELECT count(*) FROM (SELECT 1 FROM observation_occurrences occurrence WHERE occurrence.tenant_id=snapshot.tenant_id AND occurrence.snapshot_id=snapshot.id LIMIT $3) bounded_members)
  FROM source_snapshots snapshot
 WHERE snapshot.tenant_id=$1 AND snapshot.id=$2`, tenantID, position.id, federation.MaxSynchronizationEntries+1).Scan(&id, &sourceID, &sourceRevision, &state, &enumerationComplete, &withdrawalAuthority, &memberCount, &bodyDigest, &raw, &started, &completed, &occurrenceCount); err != nil {
			return nil, false, err
		}
		var snapshot federation.Snapshot
		rowRevision := int64(0)
		if sourceRevision != nil {
			rowRevision = *sourceRevision
		}
		var validationErr error
		if jsondoc.Validate(raw) != nil || json.Unmarshal(raw, &snapshot) != nil {
			validationErr = errors.New("snapshot document is invalid")
		} else {
			validationErr = federation.ValidateRetainedSnapshotIntegrity(snapshot)
		}
		attemptsMatch, attemptsErr := retainedSnapshotAttemptsMatch(ctx, tx, tenantID, id, snapshot.Retrievals)
		if attemptsErr != nil {
			return nil, false, attemptsErr
		}
		rowMatches := snapshot.ID == id && snapshot.TenantID == tenantID && snapshot.SourceID == sourceID && snapshot.SourceRevision == rowRevision && string(snapshot.State) == state && snapshot.EnumerationComplete == enumerationComplete && snapshot.WithdrawalAuthority == withdrawalAuthority && snapshot.MemberCount == memberCount && snapshot.BodyDigest == bodyDigest && samePostgresTime(snapshot.StartedAt, started) && sameOptionalPostgresTime(snapshot.CompletedAt, completed)
		switch {
		case validationErr != nil:
			record.finding = integrityFinding("RESTORE_SOURCE_SNAPSHOT_INVALID", "source_snapshot", id, "preserve the last complete source pointer and investigate the retained snapshot document")
		case !attemptsMatch:
			record.finding = integrityFinding("RESTORE_SOURCE_SNAPSHOT_INVALID", "source_snapshot", id, "preserve the last complete source pointer and reconcile the snapshot retrieval-attempt evidence")
		case occurrenceCount > federation.MaxSynchronizationEntries || occurrenceCount != memberCount:
			record.finding = integrityFinding("RESTORE_SOURCE_SNAPSHOT_INVALID", "source_snapshot", id, "preserve the last complete source pointer and reconcile the snapshot member evidence")
		case !rowMatches:
			record.finding = integrityFinding("RESTORE_SOURCE_SNAPSHOT_INVALID", "source_snapshot", id, "preserve the last complete source pointer and reconcile the snapshot row with its immutable document")
		}

		records = append(records, record)
	}
	return records, more, nil
}

// retainedSnapshotAttemptsMatch compares a bounded snapshot's retrieval
// evidence with same-tenant attempt rows by ID, using PostgreSQL timestamp
// precision.
func retainedSnapshotAttemptsMatch(ctx context.Context, tx pgx.Tx, tenantID, snapshotID string, expected []federation.RetrievalEvidence) (bool, error) {
	if len(expected) > maxRestoreSnapshotRetrievals {
		return false, nil
	}
	rows, err := tx.Query(ctx, `
SELECT id::text,request_key,requested_url_digest,final_url_digest,profile_id,status,received_body,reused,
       coalesce(representation_digest,''),coalesce(etag_digest,''),coalesce(last_modified_digest,''),fetched_at
  FROM source_retrieval_attempts
 WHERE tenant_id=$1 AND snapshot_id=$2
 ORDER BY id LIMIT $3`, tenantID, snapshotID, maxRestoreSnapshotRetrievals+1)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	actual := make(map[string]federation.RetrievalEvidence, len(expected))
	for rows.Next() {
		var attempt federation.RetrievalEvidence
		if err := rows.Scan(&attempt.ID, &attempt.RequestKey, &attempt.RequestedURLDigest, &attempt.FinalURLDigest, &attempt.ProfileID, &attempt.Status, &attempt.ReceivedBody, &attempt.Reused, &attempt.RepresentationDigest, &attempt.ETagDigest, &attempt.LastModifiedDigest, &attempt.FetchedAt); err != nil {
			return false, err
		}
		if len(actual) == maxRestoreSnapshotRetrievals {
			return false, nil
		}
		actual[attempt.ID] = attempt
	}
	if err := rows.Err(); err != nil || len(actual) != len(expected) {
		return false, err
	}
	for _, retained := range expected {
		attempt, ok := actual[retained.ID]
		if !ok || attempt.ID != retained.ID || attempt.RequestKey != retained.RequestKey || attempt.RequestedURLDigest != retained.RequestedURLDigest || attempt.FinalURLDigest != retained.FinalURLDigest || attempt.ProfileID != retained.ProfileID || attempt.Status != retained.Status || attempt.ReceivedBody != retained.ReceivedBody || attempt.Reused != retained.Reused || attempt.RepresentationDigest != retained.RepresentationDigest || attempt.ETagDigest != retained.ETagDigest || attempt.LastModifiedDigest != retained.LastModifiedDigest || !samePostgresTime(attempt.FetchedAt, retained.FetchedAt) {
			return false, nil
		}
	}
	return true, nil
}

// scanRestoreSourceRetrievalStates checks one bounded conditional-retrieval
// state and its retained body against the source revision's policy and
// credential bindings.
func scanRestoreSourceRetrievalStates(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	afterSource, afterRevision, afterRequest, err := decodeRetrievalStateKey(after)
	if err != nil {
		return nil, false, err
	}
	// One valid retained body can consume the complete 64 MiB synchronization
	// byte budget, so this stage deliberately inspects one state per page.
	limit = boundedStageLimit(limit, 1)
	rows, err := tx.Query(ctx, `
SELECT state.source_id::text,state.source_revision,state.request_key,
       4096::bigint+octet_length(state.requested_url)+octet_length(state.final_url)
       +octet_length(state.profile_id)+octet_length(state.media_type)+octet_length(state.credential_revision)
       +octet_length(state.etag)+octet_length(state.last_modified)+octet_length(state.vary_names::text)
       +coalesce(octet_length(document.body),0)
  FROM source_retrieval_states state
  LEFT JOIN source_retrieval_documents document
    ON document.tenant_id=state.tenant_id AND document.source_id=state.source_id
   AND document.source_revision=state.source_revision AND document.representation_digest=state.representation_digest
 WHERE state.tenant_id=$1
   AND ($2::uuid IS NULL OR (state.source_id,state.source_revision,state.request_key)>($2,$3,$4))
 ORDER BY state.source_id,state.source_revision,state.request_key LIMIT $5`, tenantID, afterSource, afterRevision, afterRequest, limit+1)
	if err != nil {
		return nil, false, err
	}
	type retrievalPosition struct {
		sourceID   string
		revision   int64
		requestKey string
		size       int64
	}
	positions := make([]retrievalPosition, 0, limit+1)
	for rows.Next() {
		var position retrievalPosition
		if err := rows.Scan(&position.sourceID, &position.revision, &position.requestKey, &position.size); err != nil {
			rows.Close()
			return nil, false, err
		}
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	more := len(positions) > limit
	if more {
		positions = positions[:limit]
	}
	records := make([]restoreRecord, 0, len(positions))
	for _, position := range positions {
		key := encodeRetrievalStateKey(position.sourceID, position.revision, position.requestKey)
		record := restoreRecord{key: key, decodedBytes: position.size}
		if position.size > maxRestoreRetrievalBodyBytes+maxRestoreSmallRecordBytes {
			record.decodedBytes = 0
			record.finding = integrityFinding("RESTORE_SOURCE_RETRIEVAL_STATE_INVALID", "source_retrieval_state", position.sourceID, "discard the unsafe conditional state before source synchronization; preserve immutable retrieval evidence")
			records = append(records, record)
			continue
		}
		state := federation.RetrievalState{TenantID: tenantID, SourceID: position.sourceID, SourceRevision: position.revision, RequestKey: position.requestKey}
		var expectedPolicyDigest, expectedCredentialRevision string
		var bodySize int64
		var documentCreatedAt *time.Time
		err := tx.QueryRow(ctx, `
SELECT state.requested_url,state.final_url,state.request_identity_digest,state.profile_id,state.policy_digest,
       state.credential_revision,state.representation_digest,state.media_type,
       CASE WHEN octet_length(document.body)<=$5 THEN document.body ELSE NULL END,
       coalesce(octet_length(document.body),0),document.created_at,state.etag,state.last_modified,state.vary_names,
       state.vary_selection_digest,state.list_terminal,state.updated_at,
       coalesce(revision.policy_digest,''),coalesce(revision.credential_revision,'')
  FROM source_retrieval_states state
  LEFT JOIN source_retrieval_documents document
    ON document.tenant_id=state.tenant_id AND document.source_id=state.source_id
   AND document.source_revision=state.source_revision AND document.representation_digest=state.representation_digest
  LEFT JOIN federation_source_revisions revision
    ON revision.tenant_id=state.tenant_id AND revision.source_id=state.source_id AND revision.revision=state.source_revision
 WHERE state.tenant_id=$1 AND state.source_id=$2 AND state.source_revision=$3 AND state.request_key=$4`,
			tenantID, position.sourceID, position.revision, position.requestKey, maxRestoreRetrievalBodyBytes).Scan(
			&state.RequestedURL, &state.FinalURL, &state.RequestIdentityDigest, &state.ProfileID, &state.PolicyDigest,
			&state.CredentialRevision, &state.RepresentationDigest, &state.MediaType, &state.Body,
			&bodySize, &documentCreatedAt, &state.ETag, &state.LastModified, &state.VaryNames,
			&state.VarySelectionDigest, &state.ListTerminal, &state.UpdatedAt,
			&expectedPolicyDigest, &expectedCredentialRevision)
		if err != nil {
			return nil, false, err
		}
		if bodySize < 1 || bodySize > maxRestoreRetrievalBodyBytes || documentCreatedAt == nil || documentCreatedAt.After(state.UpdatedAt) || federation.ValidateRetainedRetrievalState(state, expectedPolicyDigest, expectedCredentialRevision) != nil {
			record.finding = integrityFinding("RESTORE_SOURCE_RETRIEVAL_STATE_INVALID", "source_retrieval_state", position.sourceID, "discard the unsafe conditional state before source synchronization; preserve immutable retrieval evidence")
		}
		records = append(records, record)
	}
	return records, more, nil
}

// encodeRetrievalStateKey formats the source, revision and request-digest tuple
// used to resume conditional-state verification.
func encodeRetrievalStateKey(sourceID string, revision int64, requestKey string) string {
	return sourceID + "/" + strconv.FormatInt(revision, 10) + "/" + requestKey
}

// decodeRetrievalStateKey validates the source UUID, positive revision and
// request digest; empty input returns a nil source boundary.
func decodeRetrievalStateKey(value string) (*string, int64, string, error) {
	if value == "" {
		return nil, 0, "", nil
	}
	sourceID, tail, ok := strings.Cut(value, "/")
	if !ok || !identity.ValidTenantID(sourceID) {
		return nil, 0, "", errors.New("restore retrieval-state cursor position is invalid")
	}
	revisionText, requestKey, ok := strings.Cut(tail, "/")
	revision, err := strconv.ParseInt(revisionText, 10, 64)
	if !ok || err != nil || revision < 1 || !validRestoreDigest(requestKey) {
		return nil, 0, "", errors.New("restore retrieval-state cursor position is invalid")
	}
	return &sourceID, revision, requestKey, nil
}

// scanRestoreObservations verifies bounded observation bytes and occurrence
// evidence, distinguishing invalid authority from repairable missing search
// projections.
func scanRestoreObservations(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 2)
	rows, err := tx.Query(ctx, `
SELECT observation.id::text,
       2048::bigint + octet_length(observation.source_key)
       + octet_length(coalesce(observation.identifier,'')) + octet_length(coalesce(observation.version,''))
       + octet_length(coalesce(observation.kind,'')) + octet_length(coalesce(observation.media_type,''))
       + octet_length(observation.completeness) + octet_length(observation.compatibility::text)
       + octet_length(observation.exact_entry_digest) + octet_length(coalesce(observation.identity_digest,''))
       + octet_length(coalesce(observation.normalized_entry_digest,'')) + octet_length(coalesce(observation.normalization_profile,''))
       + coalesce(octet_length(observation.raw_entry_bytes),0) + coalesce(octet_length(observation.normalized_entry_bytes),0)
       + octet_length(observation.freshness) + octet_length(coalesce(observation.search_display_name,''))
       + octet_length(coalesce(observation.search_description,'')) + coalesce(octet_length(observation.search_tags::text),0)
       + coalesce(octet_length(observation.search_representative_queries::text),0) + octet_length(coalesce(observation.search_publisher,''))
       + coalesce(octet_length(observation.search_license),0) + coalesce(octet_length(observation.search_categories::text),0)
  FROM observations observation
 WHERE observation.tenant_id=$1 AND ($2='' OR observation.id>nullif($2,'')::uuid)
 ORDER BY observation.id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	type observationPosition struct {
		id   string
		size int64
	}
	positions := make([]observationPosition, 0, limit+1)
	for rows.Next() {
		var position observationPosition
		if err := rows.Scan(&position.id, &position.size); err != nil {
			rows.Close()
			return nil, false, err
		}
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	more := len(positions) > limit
	if more {
		positions = positions[:limit]
	}
	records := make([]restoreRecord, 0, len(positions))
	for _, position := range positions {
		record := restoreRecord{key: position.id, decodedBytes: position.size}
		if position.size > maxRestoreObservationBytes {
			record.decodedBytes = 0
			record.finding = integrityFinding("RESTORE_OBSERVATION_EVIDENCE_INVALID", "observation", position.id, "exclude the observation from discovery and investigate its oversized retained evidence")
			records = append(records, record)
			continue
		}
		var observation federation.Observation
		var compatibilityRaw []byte
		var projectionComplete, occurrenceExists bool
		var projectedLicense *string
		var projectedCategories []string
		var normalizationInput string
		if err := tx.QueryRow(ctx, `
SELECT observation.id::text,observation.source_id::text,observation.snapshot_id::text,observation.source_key,
       coalesce(observation.identifier,''),coalesce(observation.version,''),observation.version_present,coalesce(observation.kind,''),coalesce(observation.media_type,''),observation.completeness,
       observation.compatibility,observation.exact_entry_digest,coalesce(observation.identity_digest,''),coalesce(observation.normalized_entry_digest,''),coalesce(observation.normalization_profile,''),
       observation.raw_entry_bytes,observation.normalized_entry_bytes,
       observation.freshness,observation.first_seen_at,observation.last_seen_at,
       coalesce(observation.search_display_name,''),coalesce(observation.search_description,''),coalesce(observation.search_tags,ARRAY[]::text[]),coalesce(observation.search_representative_queries,ARRAY[]::text[]),coalesce(observation.search_publisher,''),
       observation.normalized_entry_digest IS NOT NULL AND observation.search_display_name IS NOT NULL AND observation.search_description IS NOT NULL AND observation.search_tags IS NOT NULL AND observation.search_representative_queries IS NOT NULL AND observation.search_publisher IS NOT NULL AND observation.search_vector IS NOT NULL AND observation.search_vector_version=2 AND observation.search_filter_version=1,observation.search_license,observation.search_categories,
	       coalesce((SELECT coalesce(occurrence.normalization_input_digest,'')
	                   FROM observation_occurrences occurrence
	                  WHERE occurrence.tenant_id=observation.tenant_id AND occurrence.observation_id=observation.id
	                  ORDER BY occurrence.observed_at DESC,occurrence.snapshot_id DESC,occurrence.retrieval_digest,occurrence.entry_location
	                  LIMIT 1),''),
	       EXISTS(SELECT 1 FROM observation_occurrences occurrence WHERE occurrence.tenant_id=observation.tenant_id AND occurrence.observation_id=observation.id LIMIT 1)
  FROM observations observation
 WHERE observation.tenant_id=$1 AND observation.id=$2`, tenantID, position.id).Scan(&observation.ID, &observation.SourceID, &observation.SnapshotID, &observation.SourceKey, &observation.Identifier, &observation.Version, &observation.VersionPresent, &observation.Kind, &observation.MediaType, &observation.Completeness, &compatibilityRaw, &observation.ExactEntryDigest, &observation.IdentityDigest, &observation.NormalizedEntryDigest, &observation.NormalizationProfile, &observation.RawEntry, &observation.NormalizedEntry, &observation.Freshness, &observation.FirstSeenAt, &observation.LastSeenAt, &observation.SearchDisplayName, &observation.SearchDescription, &observation.SearchTags, &observation.SearchQueries, &observation.SearchPublisher, &projectionComplete, &projectedLicense, &projectedCategories, &normalizationInput, &occurrenceExists); err != nil {
			return nil, false, err
		}
		observation.TenantID = tenantID
		compatibilityErr := json.Unmarshal(compatibilityRaw, &observation.Compatibility)
		validationErr := federation.ValidateRetainedObservationIntegrity(observation, normalizationInput, projectionComplete)
		filters := federation.ProjectObservationControlFilters(observation.NormalizedEntry)
		filterComplete := (filters.License == nil && projectedLicense == nil || filters.License != nil && projectedLicense != nil && *filters.License == *projectedLicense) &&
			(filters.Categories == nil) == (projectedCategories == nil) && slices.Equal(filters.Categories, projectedCategories)
		switch {
		case compatibilityErr != nil || !occurrenceExists || validationErr != nil:
			record.finding = integrityFinding("RESTORE_OBSERVATION_EVIDENCE_INVALID", "observation", observation.ID, "exclude the observation from discovery and investigate its immutable occurrence and byte evidence")
		case observation.NormalizedEntryDigest != "" && projectionComplete && !filterComplete:
			record.finding = integrityFinding("RESTORE_SEARCH_FILTER_PROJECTION_INVALID", "observation", observation.ID, "keep runtimes stopped; investigate current derived declaration drift before selecting a reviewed repair")
		case observation.NormalizedEntryDigest != "" && !projectionComplete:
			record.finding = repairableFinding("RESTORE_SEARCH_PROJECTION_DRIFT", "observation", observation.ID, "run the bounded tenant search-projection repair after all read-only checks, then reverify")
		}
		records = append(records, record)
	}
	return records, more, nil
}

// scanRestoreObservationOccurrences checks a bounded page of immutable
// occurrences against same-tenant observation and snapshot identities, digests
// and times.
func scanRestoreObservationOccurrences(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	afterSnapshot, afterRetrieval, afterLocation, err := decodeOccurrenceKey(after)
	if err != nil {
		return nil, false, err
	}
	limit = boundedStageLimit(limit, 64)
	rows, err := tx.Query(ctx, `
SELECT occurrence.observation_id::text,occurrence.source_id::text,occurrence.snapshot_id::text,
       occurrence.source_revision,occurrence.retrieval_digest,occurrence.entry_location,
       occurrence.exact_entry_digest,coalesce(occurrence.normalized_entry_digest,''),coalesce(occurrence.normalization_input_digest,''),occurrence.evidence_complete,occurrence.observed_at,
       observation.tenant_id::text,observation.source_id::text,observation.exact_entry_digest,coalesce(observation.normalized_entry_digest,''),coalesce(observation.identity_digest,''),observation.first_seen_at,observation.last_seen_at,
       snapshot.tenant_id::text,snapshot.source_id::text,snapshot.source_revision,snapshot.state,snapshot.started_at,snapshot.completed_at
  FROM observation_occurrences occurrence
  JOIN observations observation
    ON observation.tenant_id=occurrence.tenant_id AND observation.id=occurrence.observation_id
  JOIN source_snapshots snapshot
    ON snapshot.tenant_id=occurrence.tenant_id AND snapshot.id=occurrence.snapshot_id
 WHERE occurrence.tenant_id=$1
   AND ($2::uuid IS NULL OR occurrence.snapshot_id>$2
        OR occurrence.snapshot_id=$2 AND occurrence.retrieval_digest>$3
        OR occurrence.snapshot_id=$2 AND occurrence.retrieval_digest=$3 AND occurrence.entry_location>$4)
 ORDER BY occurrence.snapshot_id,occurrence.retrieval_digest,occurrence.entry_location
 LIMIT $5`, tenantID, afterSnapshot, afterRetrieval, afterLocation, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var occurrence federation.ObservationOccurrence
		var observation federation.Observation
		var snapshot federation.Snapshot
		var occurrenceRevision, snapshotRevision *int64
		if err := rows.Scan(&occurrence.ObservationID, &occurrence.SourceID, &occurrence.SnapshotID, &occurrenceRevision, &occurrence.RetrievalDigest, &occurrence.EntryLocation, &occurrence.ExactEntryDigest, &occurrence.NormalizedEntryDigest, &occurrence.NormalizationInputDigest, &occurrence.EvidenceComplete, &occurrence.ObservedAt, &observation.TenantID, &observation.SourceID, &observation.ExactEntryDigest, &observation.NormalizedEntryDigest, &observation.IdentityDigest, &observation.FirstSeenAt, &observation.LastSeenAt, &snapshot.TenantID, &snapshot.SourceID, &snapshotRevision, &snapshot.State, &snapshot.StartedAt, &snapshot.CompletedAt); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		observation.ID = occurrence.ObservationID
		snapshot.ID = occurrence.SnapshotID
		if occurrenceRevision != nil {
			occurrence.SourceRevision = *occurrenceRevision
		}
		if snapshotRevision != nil {
			snapshot.SourceRevision = *snapshotRevision
		}
		record := restoreRecord{key: encodeOccurrenceKey(occurrence.SnapshotID, occurrence.RetrievalDigest, occurrence.EntryLocation)}
		validationErr := federation.ValidateRetainedOccurrenceIntegrity(occurrence, observation, snapshot)
		if validationErr != nil {
			record.finding = integrityFinding("RESTORE_OBSERVATION_OCCURRENCE_INVALID", "observation_occurrence", occurrence.ObservationID, "exclude the observation from discovery and investigate the immutable occurrence binding")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}

// encodeOccurrenceKey formats the snapshot, retrieval digest and entry-location
// tuple used to resume occurrence verification.
func encodeOccurrenceKey(snapshotID, retrievalDigest, entryLocation string) string {
	return snapshotID + "/" + retrievalDigest + "/" + entryLocation
}

// decodeOccurrenceKey validates a snapshot UUID, retrieval digest and bounded
// nonblank entry location; empty input returns a nil snapshot boundary.
func decodeOccurrenceKey(value string) (*string, string, string, error) {
	if value == "" {
		return nil, "", "", nil
	}
	snapshotID, tail, ok := strings.Cut(value, "/")
	if !ok || !identity.ValidTenantID(snapshotID) {
		return nil, "", "", errors.New("restore occurrence cursor position is invalid")
	}
	retrievalDigest, entryLocation, ok := strings.Cut(tail, "/")
	if !ok || !validRestoreDigest(retrievalDigest) || len(entryLocation) == 0 || len(entryLocation) > 1024 || entryLocation != strings.TrimSpace(entryLocation) || strings.IndexFunc(entryLocation, func(character rune) bool { return character < 0x20 || character == 0x7f }) >= 0 {
		return nil, "", "", errors.New("restore occurrence cursor position is invalid")
	}
	return &snapshotID, retrievalDigest, entryLocation, nil
}

// validRestoreDigest reports whether value is a SHA-256 digest with the
// required prefix and 64 lowercase hexadecimal digits.
func validRestoreDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

// scanRestorePublicationSnapshots checks one bounded immutable snapshot against
// its stored identity, profiles, digest and lifecycle columns without
// generating or activating publication.
func scanRestorePublicationSnapshots(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 1)
	rows, err := tx.Query(ctx, `SELECT id::text,2048::bigint+octet_length(host)+octet_length(document_family)+octet_length(profile_id)+octet_length(etag)+octet_length(state)+octet_length(document::text) FROM publication_snapshots WHERE tenant_id=$1 AND ($2='' OR id>nullif($2,'')::uuid) ORDER BY id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	type publicationPosition struct {
		id   string
		size int64
	}
	positions := make([]publicationPosition, 0, limit+1)
	for rows.Next() {
		var position publicationPosition
		if err := rows.Scan(&position.id, &position.size); err != nil {
			rows.Close()
			return nil, false, err
		}
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	more := len(positions) > limit
	if more {
		positions = positions[:limit]
	}
	records := make([]restoreRecord, 0, len(positions))
	for _, position := range positions {
		record := restoreRecord{key: position.id, decodedBytes: position.size}
		if position.size > publication.MaxPublicationSnapshotBytes+maxRestoreSmallDocumentBytes {
			record.decodedBytes = 0
			record.finding = integrityFinding("RESTORE_PUBLICATION_SNAPSHOT_INVALID", "publication_snapshot", position.id, "keep publication disabled and investigate the oversized retained snapshot")
			records = append(records, record)
			continue
		}
		var id, rowTenant, host, profileID, etag string
		var generation int64
		var raw []byte
		var state publication.SnapshotState
		var created time.Time
		var activated *time.Time
		if err := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,host,profile_id,generation,etag,document,state,created_at,activated_at FROM publication_snapshots WHERE tenant_id=$1 AND id=$2`, tenantID, position.id).Scan(&id, &rowTenant, &host, &profileID, &generation, &etag, &raw, &state, &created, &activated); err != nil {
			return nil, false, err
		}
		var snapshot publication.Snapshot
		if publication.ValidateEncodedSnapshotBounds(raw) != nil || jsondoc.Validate(raw) != nil || json.Unmarshal(raw, &snapshot) != nil || snapshot.ID != id || snapshot.TenantID != rowTenant || snapshot.Host != host || snapshot.ARDProfile+"+"+snapshot.AICatalogProfile != profileID || snapshot.Generation != generation || snapshot.ETag != etag || snapshot.State != state || !samePostgresTime(snapshot.CreatedAt, created) || !sameOptionalPostgresTime(snapshot.ActivatedAt, activated) || publication.ValidateSnapshotIntegrity(snapshot) != nil {
			record.finding = integrityFinding("RESTORE_PUBLICATION_SNAPSHOT_INVALID", "publication_snapshot", id, "keep publication disabled; investigate or select another recovery point rather than generating a replacement candidate")
		}
		records = append(records, record)
	}
	return records, more, nil
}

// scanRestoreAuditEvents pages retained version-1 audit records, suppressing
// oversized fields in SQL before validating attribution and action contracts.
func scanRestoreAuditEvents(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 16)
	rows, err := tx.Query(ctx, `
WITH candidate AS (
	  SELECT event.format_version,event.id,event.tenant_id,event.actor,event.action,event.target_type,event.target_id,
	         event.reason,event.request_id,event.before_digest,event.after_digest,event.details,event.occurred_at,
         512::bigint + octet_length(event.actor) + octet_length(event.action) + octet_length(event.target_type) + octet_length(event.target_id) + octet_length(event.reason) + octet_length(event.request_id) + octet_length(event.before_digest) + octet_length(event.after_digest) + octet_length(event.details::text) AS estimated_bytes
    FROM audit_events event
   WHERE event.tenant_id=$1 AND ($2='' OR event.id>nullif($2,'')::uuid)
   ORDER BY event.id LIMIT $3
)
SELECT candidate.format_version,candidate.id::text,candidate.tenant_id::text,
       CASE WHEN candidate.format_version=1 AND candidate.estimated_bytes<=65536 THEN candidate.actor END,
       CASE WHEN candidate.format_version=1 AND candidate.estimated_bytes<=65536 THEN candidate.action END,
       CASE WHEN candidate.format_version=1 AND candidate.estimated_bytes<=65536 THEN candidate.target_type END,
       CASE WHEN candidate.format_version=1 AND candidate.estimated_bytes<=65536 THEN candidate.target_id END,
       CASE WHEN candidate.format_version=1 AND candidate.estimated_bytes<=65536 THEN candidate.reason END,
       CASE WHEN candidate.format_version=1 AND candidate.estimated_bytes<=65536 THEN candidate.request_id END,
       CASE WHEN candidate.format_version=1 AND candidate.estimated_bytes<=65536 THEN candidate.before_digest END,
       CASE WHEN candidate.format_version=1 AND candidate.estimated_bytes<=65536 THEN candidate.after_digest END,
       CASE WHEN candidate.format_version=1 AND candidate.estimated_bytes<=65536 THEN candidate.details END,
       candidate.occurred_at,candidate.estimated_bytes
  FROM candidate
 ORDER BY candidate.id`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var event audit.Record
		var actor, action, targetType, targetID, reason, requestID, beforeDigest, afterDigest *string
		var detailsRaw []byte
		var estimated int64
		if err := rows.Scan(&event.FormatVersion, &event.ID, &event.TenantID, &actor, &action, &targetType, &targetID, &reason, &requestID, &beforeDigest, &afterDigest, &detailsRaw, &event.At, &estimated); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: event.ID, decodedBytes: int64(len(detailsRaw))}
		switch {
		case event.FormatVersion == nil || *event.FormatVersion != audit.RecordFormatV1 || estimated > audit.MaxEncodedRecordBytes || actor == nil || action == nil || targetType == nil || targetID == nil || reason == nil || requestID == nil || beforeDigest == nil || afterDigest == nil || len(detailsRaw) == 0 || decodeRestoreAuditDetails(detailsRaw, &event.Details) != nil:
			record.finding = integrityFinding("RESTORE_AUDIT_RECORD_INVALID", "audit_event", event.ID, "preserve the row and investigate the version-1 audit invariant failure")
		default:
			event.Actor, event.Action, event.TargetType, event.TargetID = *actor, *action, *targetType, *targetID
			event.Reason, event.RequestID, event.BeforeDigest, event.AfterDigest = *reason, *requestID, *beforeDigest, *afterDigest
			event.At = event.At.UTC().Truncate(time.Microsecond)
			if audit.ValidateNewRecord(event) != nil {
				record.finding = integrityFinding("RESTORE_AUDIT_RECORD_INVALID", "audit_event", event.ID, "preserve the row and investigate the version-1 audit invariant failure")
			}
		}
		if event.FormatVersion != nil && *event.FormatVersion != audit.RecordFormatV1 {
			record.finding = integrityFinding("RESTORE_AUDIT_RECORD_INVALID", "audit_event", event.ID, "preserve the row and investigate the version-1 audit invariant failure")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}

// decodeRestoreAuditDetails rejects invalid or duplicate-key JSON and preserves
// numeric values as json.Number for audit validation.
func decodeRestoreAuditDetails(raw []byte, destination *map[string]any) error {
	if jsondoc.Validate(raw) != nil {
		return errors.New("audit details are invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(destination)
}

// scanRestoreJobs validates bounded actionable Job payloads and reports
// retained work or expired leases for review without claiming or modifying
// jobs.
func scanRestoreJobs(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 32)
	rows, err := tx.Query(ctx, `
SELECT bounded.id::text,
       CASE WHEN bounded.decoded_bytes<=$4 THEN bounded.kind ELSE '' END,
       bounded.payload_version,bounded.decoded_bytes,
       CASE WHEN bounded.decoded_bytes<=$4 THEN bounded.payload ELSE NULL END,
       bounded.state,bounded.lease_expires_at,transaction_timestamp()
  FROM (
    SELECT id,kind,payload_version,payload,state,lease_expires_at,
           1024::bigint+octet_length(kind)+octet_length(payload::text) AS decoded_bytes
      FROM jobs
     WHERE tenant_id=$1 AND state IN ('queued','running','waiting')
       AND ($2='' OR id>nullif($2,'')::uuid)
     ORDER BY id LIMIT $3
  ) bounded
 ORDER BY bounded.id`, tenantID, after, limit+1, maxRestoreSmallRecordBytes)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var job jobs.Job
		var decodedSize int64
		var transactionTime time.Time
		if err := rows.Scan(&job.ID, &job.Kind, &job.PayloadVersion, &decodedSize, &job.Payload, &job.State, &job.LeaseExpiresAt, &transactionTime); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: job.ID, decodedBytes: int64(len(job.Payload))}
		if decodedSize > maxRestoreSmallRecordBytes || jobs.ValidateActionablePayload(job) != nil {
			record.finding = integrityFinding("RESTORE_ACTIONABLE_JOB_PAYLOAD_INVALID", "job", job.ID, "keep workers stopped and investigate the actionable Job payload")
		} else {
			code := "RESTORE_ACTIONABLE_JOB_RETAINED"
			disposition := "review before starting workers; normal claim and retry behavior recovers valid retained work"
			if job.State == jobs.StateRunning && job.LeaseExpiresAt != nil && !job.LeaseExpiresAt.After(transactionTime) {
				code = "RESTORE_EXPIRED_JOB_LEASE"
				disposition = "leave the lease unchanged; a compatible worker may reclaim it after recovery review"
			}
			record.finding = operationalFinding(code, "job", job.ID, disposition)
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}

// scanRestoreOutbox checks bounded unacknowledged public event references and
// reports pending public or internal work without dispatching it.
func scanRestoreOutbox(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 32)
	rows, err := tx.Query(ctx, `
SELECT id::text,tenant_id::text,is_internal,octet_length(event_type),octet_length(subject),octet_length(data::text),
       CASE WHEN NOT is_internal AND octet_length(event_type)<=160 THEN event_type END,
       CASE WHEN NOT is_internal AND octet_length(subject)<=512 THEN subject END,
       CASE WHEN NOT is_internal AND octet_length(data::text)<=$4 THEN data END,
       created_at
  FROM outbox_events
 WHERE tenant_id=$1 AND published_at IS NULL AND ($2='' OR id>nullif($2,'')::uuid)
 ORDER BY id LIMIT $3`, tenantID, after, limit+1, maxRestoreOutboxDataBytes)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var event catalog.OutboxEvent
		var eventType, subject *string
		var eventTypeSize, subjectSize, dataSize int64
		if err := rows.Scan(&event.ID, &event.TenantID, &event.Internal, &eventTypeSize, &subjectSize, &dataSize, &eventType, &subject, &event.Data, &event.CreatedAt); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: event.ID, decodedBytes: int64(len(event.Data))}
		if !event.Internal {
			if eventTypeSize > 160 || subjectSize > 512 || dataSize > maxRestoreOutboxDataBytes || eventType == nil || subject == nil {
				record.finding = integrityFinding("RESTORE_PUBLIC_OUTBOX_REFERENCE_INVALID", "outbox_event", event.ID, "keep dispatch disabled and investigate the bounded public reference")
				records = append(records, record)
				continue
			}
			event.Type, event.Subject = *eventType, *subject
			if !events.IsPublicType(event.Type) {
				record.finding = integrityFinding("RESTORE_PUBLIC_OUTBOX_TYPE_INVALID", "outbox_event", event.ID, "keep dispatch disabled and investigate the unacknowledged public event classification")
			} else if _, err := events.MarshalCloudEvent(event.ID, event.TenantID, event.Type, event.Subject, event.CreatedAt, event.Data); err != nil {
				record.finding = integrityFinding("RESTORE_PUBLIC_OUTBOX_REFERENCE_INVALID", "outbox_event", event.ID, "keep dispatch disabled and investigate the closed public reference")
			} else {
				record.finding = operationalFinding("RESTORE_UNACKNOWLEDGED_OUTBOX_EVENT", "outbox_event", event.ID, "review duplicate-delivery consequences before starting the dispatcher")
			}
		} else {
			record.finding = operationalFinding("RESTORE_PENDING_INTERNAL_OUTBOX_EVENT", "outbox_event", event.ID, "review the internal retained work before starting workers")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}

// scanRestoreIdempotencyV2 pages unfinished, unreleased command reservations
// and distinguishes invalid state from interrupted work requiring focused
// recovery review.
func scanRestoreIdempotencyV2(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	afterStarted, afterReservation, err := decodeTimeUUIDKey(after)
	if err != nil {
		return nil, false, err
	}
	limit = boundedStageLimit(limit, 32)
	rows, err := tx.Query(ctx, `
SELECT reservation_id::text,execution_token IS NOT NULL,started_at,expires_at
  FROM idempotency_keys
 WHERE tenant_id=$1 AND protocol_version=2 AND atomic_response_status IS NULL AND released_at IS NULL
   AND ($2::timestamptz IS NULL OR (started_at,reservation_id)>($2,$3::uuid))
 ORDER BY started_at,reservation_id LIMIT $4`, tenantID, afterStarted, afterReservation, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var reservation string
		var hasToken bool
		var started, expires time.Time
		if err := rows.Scan(&reservation, &hasToken, &started, &expires); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: encodeTimeUUIDKey(started, reservation)}
		switch {
		case reservation == "" || started.IsZero() || !hasToken:
			record.finding = integrityFinding("RESTORE_IDEMPOTENCY_STATE_INVALID", "idempotency", reservation, "keep traffic disabled and investigate the command reservation state")
		case expires.Before(started):
			record.finding = integrityFinding("RESTORE_IDEMPOTENCY_TIME_INVALID", "idempotency", reservation, "investigate the retained command timing evidence")
		default:
			record.finding = operationalFinding("RESTORE_PENDING_IDEMPOTENCY_V2", "idempotency", reservation, "use the focused plan-bound repair only after proving the request was interrupted")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}

// encodeTimeUUIDKey formats a UTC timestamp and UUID as a restore scan
// position.
func encodeTimeUUIDKey(at time.Time, id string) string {
	return at.UTC().Format(time.RFC3339Nano) + "/" + id
}

// decodeTimeUUIDKey validates and normalizes a timestamp/UUID position; empty
// input returns a nil time and zero UUID for the initial SQL boundary.
func decodeTimeUUIDKey(value string) (*time.Time, string, error) {
	if value == "" {
		return nil, "00000000-0000-0000-0000-000000000000", nil
	}
	atText, id, ok := strings.Cut(value, "/")
	if !ok || !identity.ValidTenantID(id) {
		return nil, "", errors.New("restore time cursor position is invalid")
	}
	at, err := time.Parse(time.RFC3339Nano, atText)
	if err != nil {
		return nil, "", errors.New("restore time cursor position is invalid")
	}
	at = at.UTC()
	return &at, id, nil
}

// scanRestoreWebhookDeliveries checks tenant-local event bindings and cleanup
// eligibility, allowing retained terminal test deliveries whose outbox evidence
// was removed, without decrypting secrets.
func scanRestoreWebhookDeliveries(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]restoreRecord, bool, error) {
	rows, err := tx.Query(ctx, `
SELECT delivery.id::text,delivery.event_id::text,octet_length(delivery.event_type),
       CASE WHEN octet_length(delivery.event_type)<=160 THEN delivery.event_type ELSE '' END,
       (EXISTS(SELECT 1 FROM outbox_events event WHERE event.tenant_id=delivery.tenant_id AND event.id=delivery.event_id AND event.event_type=delivery.event_type)
        OR (delivery.event_type='com.open.agentic.catalog.webhook.test.v1' AND delivery.state IN ('succeeded','dead_letter')
            AND NOT EXISTS(SELECT 1 FROM outbox_events event WHERE event.tenant_id=delivery.tenant_id AND event.id=delivery.event_id))),
       (delivery.cleanup_after IS NULL OR `+disposableWebhookDelivery+`)
  FROM webhook_deliveries delivery
 WHERE delivery.tenant_id=$1 AND ($2='' OR delivery.id>nullif($2,'')::uuid)
 ORDER BY delivery.id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var id, eventID, eventType string
		var eventTypeBytes int64
		var bound, cleanupValid bool
		if err := rows.Scan(&id, &eventID, &eventTypeBytes, &eventType, &bound, &cleanupValid); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: id}
		if eventTypeBytes > 160 || !bound {
			record.finding = integrityFinding("RESTORE_WEBHOOK_DELIVERY_BINDING_INVALID", "webhook_delivery", id, "keep webhook delivery disabled and investigate the retained event binding without decrypting secrets")
		} else if !cleanupValid {
			record.finding = integrityFinding("RESTORE_WEBHOOK_DELIVERY_CLEANUP_INVALID", "webhook_delivery", id, "keep workers stopped and investigate the retained delivery cleanup marker")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}
