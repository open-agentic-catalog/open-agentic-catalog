// Resource authoring retains stable identities, version revisions and
// representations with parent checks and atomic mutation evidence.

package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// CreateResource inserts a stable tenant-owned resource and exactly bound creation
// audit/outbox evidence in one transaction.
func (p *PostgresRepository) CreateResource(ctx context.Context, params catalog.CreateResourceParams) (catalog.Resource, error) {
	return withTenantTx(ctx, p.pool, params.Resource.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.Resource, error) {
		if _, err := tx.Exec(ctx, `INSERT INTO resources(id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, params.Resource.ID, params.Resource.TenantID, params.Resource.NamespaceID, params.Resource.CanonicalIdentifier, params.Resource.Kind, params.Resource.CreatedBy, params.Resource.Revision, params.Resource.CreatedAt, params.Resource.UpdatedAt); err != nil {
			return catalog.Resource{}, translateError("create resource", err)
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.Resource.TenantID, "resource", params.Resource.ID, "resource.created")); err != nil {
			return catalog.Resource{}, err
		}
		return params.Resource, nil
	})
}

// GetResource reads one tenant-owned resource and treats soft-deleted identities
// as absent.
func (p *PostgresRepository) GetResource(ctx context.Context, tenantID, idValue string) (catalog.Resource, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.Resource, error) {
		return scanResource(tx.QueryRow(ctx, `SELECT id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at FROM resources WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`, tenantID, idValue))
	})
}

// GetResourceByIdentifier resolves the exact tenant-scoped canonical identifier,
// excluding soft-deleted resources.
func (p *PostgresRepository) GetResourceByIdentifier(ctx context.Context, tenantID, identifier string) (catalog.Resource, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.Resource, error) {
		return scanResource(tx.QueryRow(ctx, `SELECT id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at FROM resources WHERE tenant_id=$1 AND canonical_identifier=$2 AND deleted_at IS NULL`, tenantID, identifier))
	})
}

// ListResources applies kind and descending update-time/ID continuation in SQL
// before returning a bounded page of active resources.
func (p *PostgresRepository) ListResources(ctx context.Context, tenantID string, request catalog.ResourceListRequest) ([]catalog.Resource, error) {
	identifierFilter := ""
	arguments := []any{tenantID, request.Kind, request.AfterUpdatedAt, request.AfterID, request.Limit}
	if request.CanonicalIdentifier != "" {
		identifierFilter = " AND canonical_identifier=$6"
		arguments = append(arguments, request.CanonicalIdentifier)
	}
	return withTenantCollectionRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.Resource, error) {
		rows, err := tx.Query(ctx, collectionStatement(ctx, `
			SELECT id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at
			FROM resources
			WHERE tenant_id=$1 AND deleted_at IS NULL AND `+collectionPermissionSQL("resource.read", "resources.namespace_id")+` AND ($2='' OR kind=$2)
			  AND ($3::timestamptz IS NULL OR updated_at<$3 OR (updated_at=$3 AND id<nullif($4,'')::uuid))
			`+identifierFilter+`
			ORDER BY updated_at DESC,id DESC LIMIT $5`), arguments...)
		if err != nil {
			return nil, translateError("list resources", err)
		}
		defer rows.Close()
		items := make([]catalog.Resource, 0)
		for rows.Next() {
			item, err := scanResource(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, translateError("iterate resources", rows.Err())
	})
}

// DeleteResource revision-checks and soft-deletes an active resource only when no
// adoption, review or release history requires retention, committing evidence
// atomically.
func (p *PostgresRepository) DeleteResource(ctx context.Context, params catalog.DeleteResourceParams) (catalog.Resource, error) {
	return withTenantTx(ctx, p.pool, params.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.Resource, error) {
		resource, err := scanResource(tx.QueryRow(ctx, `SELECT id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at FROM resources WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE`, params.TenantID, params.ResourceID))
		if err != nil {
			return catalog.Resource{}, err
		}
		if resource.Revision != params.ExpectedRevision {
			return catalog.Resource{}, catalog.E(catalog.CodePreconditionFailed, "resource revision does not match")
		}
		var adopted, reviewed, released bool
		if err := tx.QueryRow(ctx, `
			SELECT
			  EXISTS(SELECT 1 FROM resource_versions version JOIN resource_version_revisions revision ON revision.tenant_id=version.tenant_id AND revision.resource_version_id=version.id WHERE version.tenant_id=$1 AND version.resource_id=$2 AND revision.adoption_provenance IS NOT NULL),
			  EXISTS(SELECT 1 FROM catalog_listings listing JOIN review_submissions submission ON submission.tenant_id=listing.tenant_id AND submission.listing_id=listing.id WHERE listing.tenant_id=$1 AND listing.resource_id=$2),
			  EXISTS(SELECT 1 FROM catalog_listings listing JOIN listing_releases release ON release.tenant_id=listing.tenant_id AND release.listing_id=listing.id WHERE listing.tenant_id=$1 AND listing.resource_id=$2)`, params.TenantID, params.ResourceID).Scan(&adopted, &reviewed, &released); err != nil {
			return catalog.Resource{}, translateError("check resource deletion dependencies", err)
		}
		switch {
		case adopted:
			return catalog.Resource{}, catalog.E(catalog.CodeConflict, "an adopted resource retains provenance and cannot be deleted")
		case reviewed:
			return catalog.Resource{}, catalog.E(catalog.CodeConflict, "a reviewed resource cannot be deleted")
		case released:
			return catalog.Resource{}, catalog.E(catalog.CodeConflict, "a resource with release history cannot be deleted")
		}
		resource.Revision++
		resource.UpdatedAt = params.At
		resource.DeletedBy = params.Actor
		resource.DeleteReason = params.Reason
		resource.DeletedAt = &params.At
		if _, err := tx.Exec(ctx, `UPDATE resources SET revision=$3,updated_at=$4,deleted_by=$5,delete_reason=$6,deleted_at=$4 WHERE tenant_id=$1 AND id=$2`, params.TenantID, resource.ID, resource.Revision, resource.UpdatedAt, resource.DeletedBy, resource.DeleteReason); err != nil {
			return catalog.Resource{}, translateError("soft delete resource", err)
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.TenantID, "resource", params.ResourceID, "resource.deleted")); err != nil {
			return catalog.Resource{}, err
		}
		return resource, nil
	})
}

// lockActiveResource locks an undeleted tenant-owned parent so concurrent soft
// deletion cannot race a child mutation.
func lockActiveResource(ctx context.Context, tx pgx.Tx, tenantID, resourceID string) error {
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM resources WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE`, tenantID, resourceID).Scan(&locked); err != nil {
		return translateError("lock active resource", err)
	}
	return nil
}

type scanner interface {
	Scan(...any) error
}

// scanResource decodes the stable resource projection and translates missing or
// invalid database rows into domain errors.
func scanResource(row scanner) (catalog.Resource, error) {
	var item catalog.Resource
	if err := row.Scan(&item.ID, &item.TenantID, &item.NamespaceID, &item.CanonicalIdentifier, &item.Kind, &item.CreatedBy, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return catalog.Resource{}, translateError("scan resource", err)
	}
	return item, nil
}

// CreateVersion locks the active resource, inserts the version and first immutable
// revision/evidence, then selects its candidate pointer atomically.
func (p *PostgresRepository) CreateVersion(ctx context.Context, params catalog.CreateVersionParams) (catalog.ResourceVersion, catalog.VersionRevision, catalog.EvidenceSet, error) {
	bundle, err := withTenantTx(ctx, p.pool, params.Version.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (versionBundle, error) {
		if err := lockActiveResource(ctx, tx, params.Version.TenantID, params.Version.ResourceID); err != nil {
			return versionBundle{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO resource_versions(id,tenant_id,resource_id,version,revision,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, params.Version.ID, params.Version.TenantID, params.Version.ResourceID, params.Version.Version, params.Version.Revision, params.Version.CreatedAt, params.Version.UpdatedAt); err != nil {
			return versionBundle{}, translateError("create resource version", err)
		}
		if err := insertVersionRevision(ctx, tx, params.VersionRevision, params.Evidence, params.Documents); err != nil {
			return versionBundle{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE resource_versions SET candidate_revision_id=$3 WHERE tenant_id=$1 AND id=$2`, params.Version.TenantID, params.Version.ID, params.VersionRevision.ID); err != nil {
			return versionBundle{}, translateError("select version candidate", err)
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.Version.TenantID, "resource_version", params.Version.ID, "resource_version.created")); err != nil {
			return versionBundle{}, err
		}
		return versionBundle{version: params.Version, revision: params.VersionRevision, evidence: params.Evidence}, nil
	})
	return bundle.version, bundle.revision, bundle.evidence, err
}

// CreateVersionRevision rejects sealed labels and stale revisions before inserting
// immutable evidence and advancing the current candidate pointer.
func (p *PostgresRepository) CreateVersionRevision(ctx context.Context, params catalog.CreateVersionParams) (catalog.ResourceVersion, catalog.VersionRevision, catalog.EvidenceSet, error) {
	bundle, err := withTenantTx(ctx, p.pool, params.Version.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (versionBundle, error) {
		current, err := scanVersion(tx.QueryRow(ctx, `SELECT id,tenant_id,resource_id,version,coalesce(candidate_revision_id::text,''),coalesce(sealed_revision_id::text,''),revision,created_at,updated_at FROM resource_versions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, params.Version.TenantID, params.Version.ID))
		if err != nil {
			return versionBundle{}, err
		}
		if current.SealedRevisionID != "" {
			return versionBundle{}, catalog.E(catalog.CodeVersionSealed, "the version label is sealed")
		}
		if err := lockActiveResource(ctx, tx, params.Version.TenantID, current.ResourceID); err != nil {
			return versionBundle{}, err
		}
		if current.Revision != params.ExpectedRevision {
			return versionBundle{}, catalog.E(catalog.CodePreconditionFailed, "resource version revision does not match")
		}
		if current.Revision+1 != params.Version.Revision {
			return versionBundle{}, catalog.E(catalog.CodePreconditionFailed, "resource version revision does not match")
		}
		if err := insertVersionRevision(ctx, tx, params.VersionRevision, params.Evidence, params.Documents); err != nil {
			return versionBundle{}, err
		}
		command, err := tx.Exec(ctx, `UPDATE resource_versions SET candidate_revision_id=$3,revision=$4,updated_at=$5 WHERE tenant_id=$1 AND id=$2 AND revision=$6 AND sealed_revision_id IS NULL`, params.Version.TenantID, params.Version.ID, params.VersionRevision.ID, params.Version.Revision, params.Version.UpdatedAt, current.Revision)
		if err != nil {
			return versionBundle{}, translateError("update version candidate", err)
		}
		if command.RowsAffected() != 1 {
			return versionBundle{}, catalog.E(catalog.CodePreconditionFailed, "resource version changed concurrently")
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.Version.TenantID, "resource_version", params.Version.ID, "resource_version.revised")); err != nil {
			return versionBundle{}, err
		}
		return versionBundle{version: params.Version, revision: params.VersionRevision, evidence: params.Evidence}, nil
	})
	return bundle.version, bundle.revision, bundle.evidence, err
}

// insertVersionRevision revalidates all immutable package, retrieval, and
// document bindings before inserting the revision and its first evidence run.
func insertVersionRevision(ctx context.Context, tx pgx.Tx, revision catalog.VersionRevision, evidence catalog.EvidenceSet, documents []catalog.DocumentBlob) error {
	for _, representation := range revision.Representations {
		if err := catalog.ValidatePackageRepresentation(representation); err != nil {
			return err
		}
	}
	if err := catalog.ValidatePackageRevisionDigest(revision); err != nil {
		return err
	}
	if err := catalog.ValidateExternalRetrievalEvidence(revision, evidence); err != nil {
		return err
	}
	documentIDs, err := insertDocumentBlobs(ctx, tx, revision, documents)
	if err != nil {
		return err
	}
	if err := insertValidationEvidenceSet(ctx, tx, evidence); err != nil {
		return err
	}
	revisionFindings, err := marshalJSON(revision.Findings)
	if err != nil {
		return err
	}
	var adoption any
	if revision.Adoption != nil {
		adoption, err = marshalJSON(revision.Adoption)
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO resource_version_revisions(id,tenant_id,resource_version_id,revision_number,content_set_digest,findings,evidence_set_id,adoption_provenance,created_by,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, revision.ID, revision.TenantID, revision.ResourceVersionID, revision.RevisionNumber, revision.ContentSetDigest, revisionFindings, revision.EvidenceSetID, adoption, revision.CreatedBy, revision.CreatedAt); err != nil {
		return translateError("insert version revision", err)
	}
	if err := insertVersionValidationRun(ctx, tx, revision.TenantID, revision.ID, 1, evidence); err != nil {
		return err
	}
	for _, representation := range revision.Representations {
		findings, err := marshalJSON(representation.Findings)
		if err != nil {
			return err
		}
		facts, err := marshalJSON(representation.NormalizedFacts)
		if err != nil {
			return err
		}
		unknown, err := marshalJSON(representation.UnknownProperties)
		if err != nil {
			return err
		}
		var inline any
		if len(representation.Data) > 0 {
			inline = representation.Data
		}
		var external any
		if representation.URL != "" {
			external = representation.URL
		}
		var document any
		if representation.ArtifactID != "" {
			if _, ok := documentIDs[representation.ArtifactID]; ok {
				document = representation.ArtifactID
			}
		}
		var plugin any
		if representation.Plugin != nil {
			encoded, err := marshalJSON(representation.Plugin)
			if err != nil {
				return err
			}
			plugin = encoded
		}
		externalReferenceOnly := representation.Package != nil
		var packageState, packageRoot any
		var packageExpandedSize, packageFileCount any
		var packageMalwareState, packageSecretsState, packageLicenseState, packageSBOMState, packageProvenanceState any
		if representation.Package != nil {
			packageState = representation.Package.StructureState
			packageRoot = representation.Package.RootDirectory
			packageExpandedSize = representation.Package.ExpandedSize
			packageFileCount = representation.Package.FileCount
			packageMalwareState = representation.Package.Assessments.Malware
			packageSecretsState = representation.Package.Assessments.Secrets
			packageLicenseState = representation.Package.Assessments.License
			packageSBOMState = representation.Package.Assessments.SBOM
			packageProvenanceState = representation.Package.Assessments.Provenance
		}
		if _, err := tx.Exec(ctx, `INSERT INTO representations(id,tenant_id,version_revision_id,role,media_type,profile_id,external_url,inline_document,content_digest,size_bytes,document_blob_id,validation_state,findings,normalized_facts,unknown_properties,external_reference_only,skill_package_structure_state,skill_package_root,skill_package_expanded_size,skill_package_file_count,skill_package_malware_state,skill_package_secrets_state,skill_package_license_state,skill_package_sbom_state,skill_package_provenance_state,plugin_inspection) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`, representation.ID, revision.TenantID, revision.ID, representation.Role, representation.MediaType, representation.ProfileID, external, inline, representation.ContentDigest, representation.Size, document, representation.ValidationState, findings, facts, unknown, externalReferenceOnly, packageState, packageRoot, packageExpandedSize, packageFileCount, packageMalwareState, packageSecretsState, packageLicenseState, packageSBOMState, packageProvenanceState, plugin); err != nil {
			return translateError("insert representation", err)
		}
		if err := insertSkillPackageMetadata(ctx, tx, revision.TenantID, representation); err != nil {
			return err
		}
	}
	return nil
}

// insertValidationEvidenceSet validates retrieval provenance and persists the
// evidence document with explicit empty retrieval arrays.
func insertValidationEvidenceSet(ctx context.Context, tx pgx.Tx, evidence catalog.EvidenceSet) error {
	if err := catalog.ValidateExternalRetrievals(evidence.ExternalRetrievals); err != nil {
		return err
	}
	findings, err := marshalJSON(evidence.Findings)
	if err != nil {
		return err
	}
	legalRights, err := marshalJSON(evidence.LegalRights)
	if err != nil {
		return err
	}
	trust, err := marshalJSON(evidence.Trust)
	if err != nil {
		return err
	}
	retrievalValues := evidence.ExternalRetrievals
	if retrievalValues == nil {
		retrievalValues = []catalog.ExternalRetrieval{}
	}
	externalRetrievals, err := marshalJSON(retrievalValues)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO evidence_sets(id,tenant_id,subject_digest,digest,state,findings,validation_profile,policy_version,rights,trust,external_retrievals,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, evidence.ID, evidence.TenantID, evidence.SubjectDigest, evidence.Digest, evidence.State, findings, evidence.ValidationProfile, evidence.PolicyVersion, legalRights, trust, externalRetrievals, evidence.CreatedAt); err != nil {
		return translateError("insert evidence set", err)
	}
	return nil
}

// insertVersionValidationRun binds an evidence set to one numbered revision
// validation run, preserving optional expiry.
func insertVersionValidationRun(ctx context.Context, tx pgx.Tx, tenantID, revisionID string, runNumber int, evidence catalog.EvidenceSet) error {
	var expiresAt any
	if !evidence.ExpiresAt.IsZero() {
		expiresAt = evidence.ExpiresAt
	}
	if _, err := tx.Exec(ctx, `INSERT INTO version_validation_runs(tenant_id,version_revision_id,run_number,evidence_set_id,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, tenantID, revisionID, runNumber, evidence.ID, expiresAt, evidence.CreatedAt); err != nil {
		return translateError("insert version validation run", err)
	}
	return nil
}

// insertSkillPackageMetadata retains immutable Git/OCI handoff references and file
// inventory for inspected Skill packages, without retaining or executing package
// bytes.
func insertSkillPackageMetadata(ctx context.Context, tx pgx.Tx, tenantID string, representation catalog.Representation) error {
	if representation.Package == nil {
		return nil
	}
	if representation.Handoff.Git != nil {
		git := representation.Handoff.Git
		if _, err := tx.Exec(ctx, `INSERT INTO artifact_references(tenant_id,representation_id,reference_kind,git_repository_url,git_commit,git_directory) VALUES ($1,$2,'git',$3,$4,$5)`, tenantID, representation.ID, git.Repository, git.Commit, git.Directory); err != nil {
			return translateError("insert Git artifact reference", err)
		}
	}
	if representation.Handoff.OCI != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO artifact_references(tenant_id,representation_id,reference_kind,oci_reference) VALUES ($1,$2,'oci',$3)`, tenantID, representation.ID, representation.Handoff.OCI.Reference); err != nil {
			return translateError("insert OCI artifact reference", err)
		}
	}
	if len(representation.Package.Files) == 0 {
		return nil
	}
	paths := make([]string, 0, len(representation.Package.Files))
	sizes := make([]int64, 0, len(representation.Package.Files))
	mediaTypes := make([]string, 0, len(representation.Package.Files))
	digests := make([]string, 0, len(representation.Package.Files))
	executable := make([]bool, 0, len(representation.Package.Files))
	for _, file := range representation.Package.Files {
		paths = append(paths, file.Path)
		sizes = append(sizes, file.Size)
		mediaTypes = append(mediaTypes, file.MediaType)
		digests = append(digests, file.Digest)
		executable = append(executable, file.Executable)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO skill_file_inventory(tenant_id,representation_id,path,size_bytes,media_type,content_digest,executable) SELECT $1,$2,file.path,file.size_bytes,file.media_type,file.content_digest,file.executable FROM unnest($3::text[],$4::bigint[],$5::text[],$6::text[],$7::boolean[]) AS file(path,size_bytes,media_type,content_digest,executable)`, tenantID, representation.ID, paths, sizes, mediaTypes, digests, executable); err != nil {
		return translateError("insert Skill package file inventory", err)
	}
	return nil
}

// insertDocumentBlobs validates all representation/document bindings and aggregate
// bytes before insertion, accepting duplicate IDs only when exact bytes and
// metadata agree.
func insertDocumentBlobs(ctx context.Context, tx pgx.Tx, revision catalog.VersionRevision, documents []catalog.DocumentBlob) (map[string]struct{}, error) {
	documentIDs := make(map[string]struct{}, len(documents))
	documentsByID := make(map[string]catalog.DocumentBlob, len(documents))
	var documentBytes int64
	for _, document := range documents {
		if err := catalog.ValidateDocumentBlob(revision.TenantID, document); err != nil {
			return nil, err
		}
		if int64(len(document.Content)) > catalog.MaxCatalogDocumentBytes-documentBytes {
			return nil, catalog.E(catalog.CodeInvalidArgument, "bounded representation documents exceed the 16 MiB revision limit")
		}
		documentBytes += int64(len(document.Content))
		if existing, ok := documentsByID[document.ID]; ok && !sameCatalogDocument(existing, document) {
			return nil, catalog.E(catalog.CodeConflict, "catalog document bytes already use another media type")
		}
		documentsByID[document.ID] = document
	}
	referenced := make(map[string]bool, len(documentsByID))
	for _, representation := range revision.Representations {
		if representation.ArtifactID == "" {
			continue
		}
		document, ok := documentsByID[representation.ArtifactID]
		if !ok {
			return nil, catalog.E(catalog.CodeInvalidArgument, "catalog document is missing for its representation")
		}
		if representation.MediaType != document.MediaType || representation.ContentDigest != document.Digest || representation.Size != document.Size {
			return nil, catalog.E(catalog.CodeInvalidArgument, "catalog document metadata does not match its representation")
		}
		referenced[document.ID] = true
	}
	for idValue := range documentsByID {
		if !referenced[idValue] {
			return nil, catalog.E(catalog.CodeInvalidArgument, "catalog document is not referenced by the version revision")
		}
	}
	for _, document := range documents {
		command, err := tx.Exec(ctx, `INSERT INTO document_blobs(tenant_id,id,media_type,content_digest,size_bytes,content,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (tenant_id,id) DO NOTHING`, document.TenantID, document.ID, document.MediaType, document.Digest, document.Size, document.Content, document.CreatedAt)
		if err != nil {
			return nil, translateError("insert catalog document", err)
		}
		if command.RowsAffected() == 0 {
			var mediaType, digest string
			var size int64
			var content []byte
			if err := tx.QueryRow(ctx, `SELECT media_type,content_digest,size_bytes,content FROM document_blobs WHERE tenant_id=$1 AND id=$2`, document.TenantID, document.ID).Scan(&mediaType, &digest, &size, &content); err != nil {
				return nil, translateError("read existing catalog document", err)
			}
			if mediaType != document.MediaType || digest != document.Digest || size != document.Size || !bytes.Equal(content, document.Content) {
				return nil, catalog.E(catalog.CodeConflict, "catalog document bytes already exist with different metadata")
			}
		}
		documentIDs[document.ID] = struct{}{}
	}
	return documentIDs, nil
}

// sameCatalogDocument compares retained identity, tenant, digest, media type, size
// and bytes while deliberately excluding creation time.
func sameCatalogDocument(left, right catalog.DocumentBlob) bool {
	return left.ID == right.ID && left.TenantID == right.TenantID && left.Digest == right.Digest && left.MediaType == right.MediaType && left.Size == right.Size && bytes.Equal(left.Content, right.Content)
}

// GetVersion reads a tenant-owned version only while its parent resource remains
// active.
func (p *PostgresRepository) GetVersion(ctx context.Context, tenantID, idValue string) (catalog.ResourceVersion, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.ResourceVersion, error) {
		return scanVersion(tx.QueryRow(ctx, `SELECT version.id,version.tenant_id,version.resource_id,version.version,coalesce(version.candidate_revision_id::text,''),coalesce(version.sealed_revision_id::text,''),version.revision,version.created_at,version.updated_at FROM resource_versions version JOIN resources resource ON resource.tenant_id=version.tenant_id AND resource.id=version.resource_id AND resource.deleted_at IS NULL WHERE version.tenant_id=$1 AND version.id=$2`, tenantID, idValue))
	})
}

// GetVersionByLabel resolves an exact version label within the specified
// tenant/resource boundary.
func (p *PostgresRepository) GetVersionByLabel(ctx context.Context, tenantID, resourceID, versionLabel string) (catalog.ResourceVersion, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.ResourceVersion, error) {
		return scanVersion(tx.QueryRow(ctx, `SELECT version.id,version.tenant_id,version.resource_id,version.version,coalesce(version.candidate_revision_id::text,''),coalesce(version.sealed_revision_id::text,''),version.revision,version.created_at,version.updated_at FROM resource_versions version JOIN resources resource ON resource.tenant_id=version.tenant_id AND resource.id=version.resource_id AND resource.deleted_at IS NULL WHERE version.tenant_id=$1 AND version.resource_id=$2 AND version.version=$3`, tenantID, resourceID, versionLabel))
	})
}

// scanVersion decodes current candidate/sealed pointers and revision metadata
// without hydrating the immutable revision.
func scanVersion(row scanner) (catalog.ResourceVersion, error) {
	var item catalog.ResourceVersion
	if err := row.Scan(&item.ID, &item.TenantID, &item.ResourceID, &item.Version, &item.CandidateRevisionID, &item.SealedRevisionID, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return catalog.ResourceVersion{}, translateError("scan resource version", err)
	}
	return item, nil
}

// GetVersionRevision returns the retained revision, representations and latest
// validation evidence for an active parent resource.
func (p *PostgresRepository) GetVersionRevision(ctx context.Context, tenantID, idValue string) (catalog.VersionRevision, catalog.EvidenceSet, error) {
	bundle, err := withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (versionBundle, error) {
		revision, evidence, err := readVersionRevision(ctx, tx, tenantID, idValue)
		return versionBundle{revision: revision, evidence: evidence}, err
	})
	return bundle.revision, bundle.evidence, err
}

// GetValidationEvidence reads one evidence set only when its validation-run
// relation binds it to the specified tenant/revision.
func (p *PostgresRepository) GetValidationEvidence(ctx context.Context, tenantID, versionRevisionID, evidenceID string) (catalog.EvidenceSet, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.EvidenceSet, error) {
		return readValidationEvidence(ctx, tx, tenantID, versionRevisionID, evidenceID)
	})
}

// CreateValidationRun serializes validation numbering, verifies subject/retrieval
// binding and appends a new evidence run without rewriting the revision.
func (p *PostgresRepository) CreateValidationRun(ctx context.Context, params catalog.CreateValidationRunParams) (catalog.EvidenceSet, error) {
	return withTenantTx(ctx, p.pool, params.TenantID, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) (catalog.EvidenceSet, error) {
		var subjectDigest, resourceID string
		if err := tx.QueryRow(ctx, `SELECT revision.content_set_digest,version.resource_id::text FROM resource_version_revisions revision JOIN resource_versions version ON version.tenant_id=revision.tenant_id AND version.id=revision.resource_version_id WHERE revision.id=$1 AND revision.tenant_id=$2`, params.VersionRevisionID, params.TenantID).Scan(&subjectDigest, &resourceID); err != nil {
			return catalog.EvidenceSet{}, translateError("read version revision for validation", err)
		}
		if err := lockActiveResource(ctx, tx, params.TenantID, resourceID); err != nil {
			return catalog.EvidenceSet{}, err
		}
		if err := acquireCatalogStateLock(ctx, tx, "version-validation", params.TenantID+"\x00"+params.VersionRevisionID); err != nil {
			return catalog.EvidenceSet{}, translateError("lock version revision for validation", err)
		}
		if params.Evidence.TenantID != params.TenantID || params.Evidence.SubjectDigest != subjectDigest {
			return catalog.EvidenceSet{}, catalog.E(catalog.CodeInvalidArgument, "version validation evidence does not match its revision")
		}
		revision, _, err := readVersionRevision(ctx, tx, params.TenantID, params.VersionRevisionID)
		if err != nil {
			return catalog.EvidenceSet{}, err
		}
		if err := catalog.ValidateExternalRetrievalEvidence(revision, params.Evidence); err != nil {
			return catalog.EvidenceSet{}, err
		}
		var runNumber int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(run_number),0)+1 FROM version_validation_runs WHERE version_revision_id=$1 AND tenant_id=$2`, params.VersionRevisionID, params.TenantID).Scan(&runNumber); err != nil {
			return catalog.EvidenceSet{}, translateError("select next version validation run", err)
		}
		if err := insertValidationEvidenceSet(ctx, tx, params.Evidence); err != nil {
			return catalog.EvidenceSet{}, err
		}
		if err := insertVersionValidationRun(ctx, tx, params.TenantID, params.VersionRevisionID, runNumber, params.Evidence); err != nil {
			return catalog.EvidenceSet{}, err
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.TenantID, "version_revision", params.VersionRevisionID, "version_revision.validated")); err != nil {
			return catalog.EvidenceSet{}, err
		}
		return params.Evidence, nil
	})
}

// GetDocument requires an active resource reference and verifies retained bytes
// against ID, digest and size; inactive documents remain undisclosed as not found.
func (p *PostgresRepository) GetDocument(ctx context.Context, tenantID, idValue string) (catalog.DocumentBlob, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.DocumentBlob, error) {
		var document catalog.DocumentBlob
		var active bool
		err := tx.QueryRow(ctx, `
			SELECT document.id,document.tenant_id,document.content_digest,document.media_type,document.size_bytes,document.content,document.created_at,
			       EXISTS(
			         SELECT 1
			           FROM representations representation
			           JOIN resource_version_revisions revision ON revision.tenant_id=representation.tenant_id AND revision.id=representation.version_revision_id
			           JOIN resource_versions version ON version.tenant_id=revision.tenant_id AND version.id=revision.resource_version_id
			           JOIN resources resource ON resource.tenant_id=version.tenant_id AND resource.id=version.resource_id AND resource.deleted_at IS NULL
			          WHERE representation.tenant_id=document.tenant_id AND representation.document_blob_id=document.id)
			  FROM document_blobs document
			 WHERE document.tenant_id=$1 AND document.id=$2`, tenantID, idValue).Scan(&document.ID, &document.TenantID, &document.Digest, &document.MediaType, &document.Size, &document.Content, &document.CreatedAt, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			var referenced bool
			if referenceErr := tx.QueryRow(ctx, `
				SELECT EXISTS(
				         SELECT 1
				           FROM representations representation
				          WHERE representation.tenant_id=$1
				            AND representation.document_blob_id=$2),
				       EXISTS(
				         SELECT 1
				           FROM representations representation
				           JOIN resource_version_revisions revision ON revision.tenant_id=representation.tenant_id AND revision.id=representation.version_revision_id
				           JOIN resource_versions version ON version.tenant_id=revision.tenant_id AND version.id=revision.resource_version_id
				           JOIN resources resource ON resource.tenant_id=version.tenant_id AND resource.id=version.resource_id AND resource.deleted_at IS NULL
				          WHERE representation.tenant_id=$1
				            AND representation.document_blob_id=$2)`, tenantID, idValue).Scan(&referenced, &active); referenceErr != nil {
				return catalog.DocumentBlob{}, translateError("inspect catalog document references", referenceErr)
			}
			if referenced && !active {
				return catalog.DocumentBlob{}, catalog.Wrap(catalog.CodeNotFound, "catalog document was not found", catalog.ErrInactiveDocument)
			}
			return catalog.DocumentBlob{}, translateError("read catalog document", err)
		}
		if err != nil {
			return catalog.DocumentBlob{}, translateError("read catalog document", err)
		}
		if !active {
			return catalog.DocumentBlob{}, catalog.Wrap(catalog.CodeNotFound, "catalog document was not found", catalog.ErrInactiveDocument)
		}
		digest := sha256.Sum256(document.Content)
		id := hex.EncodeToString(digest[:])
		if document.ID != id || document.Digest != "sha256:"+id || document.Size != int64(len(document.Content)) {
			return catalog.DocumentBlob{}, catalog.E(catalog.CodeDependencyUnavailable, "catalog document integrity check failed")
		}
		return document, nil
	})
}

// readVersionRevision hydrates the retained revision through the active-parent
// path and includes its latest evidence run.
func readVersionRevision(ctx context.Context, tx pgx.Tx, tenantID, idValue string) (catalog.VersionRevision, catalog.EvidenceSet, error) {
	return readVersionRevisionIncludingDeleted(ctx, tx, tenantID, idValue, false)
}

const versionRevisionReadSQL = `
    SELECT vr.id,vr.tenant_id,vr.resource_version_id,vr.revision_number,vr.content_set_digest,CASE WHEN octet_length(vr.findings::text) <= 33554432 THEN vr.findings ELSE '"diagnostics_read_limit"'::jsonb END,vr.evidence_set_id,vr.adoption_provenance,vr.created_by,vr.created_at,
           e.id,e.tenant_id,e.subject_digest,e.digest,e.state,CASE WHEN octet_length(e.findings::text) <= 33554432 THEN e.findings ELSE '"diagnostics_read_limit"'::jsonb END,e.validation_profile,e.policy_version,e.rights,e.trust,e.external_retrievals,
           validation.expires_at,e.created_at
      FROM resource_version_revisions vr
	  JOIN resource_versions version ON version.tenant_id=vr.tenant_id AND version.id=vr.resource_version_id
	  JOIN resources resource ON resource.tenant_id=version.tenant_id AND resource.id=version.resource_id
      JOIN LATERAL (
        SELECT run.evidence_set_id,run.expires_at
        FROM version_validation_runs run
        WHERE run.tenant_id=vr.tenant_id AND run.version_revision_id=vr.id
        ORDER BY run.run_number DESC
        LIMIT 1
      ) validation ON true
      JOIN evidence_sets e ON e.tenant_id=vr.tenant_id AND e.id=validation.evidence_set_id
	  WHERE vr.tenant_id=$1 AND vr.id=ANY($2::uuid[]) AND ($3 OR resource.deleted_at IS NULL)`

const representationReadSQL = `SELECT version_revision_id,id,role,media_type,profile_id,coalesce(external_url,''),inline_document,content_digest,size_bytes,coalesce(document_blob_id,''),validation_state,CASE WHEN octet_length(findings::text) <= 33554432 THEN findings ELSE '"diagnostics_read_limit"'::jsonb END,normalized_facts,unknown_properties,external_reference_only,coalesce(skill_package_structure_state,''),coalesce(skill_package_root,''),coalesce(skill_package_expanded_size,0),coalesce(skill_package_file_count,0),coalesce(skill_package_malware_state,''),coalesce(skill_package_secrets_state,''),coalesce(skill_package_license_state,''),coalesce(skill_package_sbom_state,''),coalesce(skill_package_provenance_state,''),plugin_inspection FROM representations WHERE tenant_id=$1 AND version_revision_id=ANY($2::uuid[]) ORDER BY id`

// readVersionRevisionIncludingDeleted optionally includes deleted parents for
// retained-history callers, then validates package and external-retrieval
// integrity.
func readVersionRevisionIncludingDeleted(ctx context.Context, tx pgx.Tx, tenantID, idValue string, includeDeleted bool) (catalog.VersionRevision, catalog.EvidenceSet, error) {
	revision, evidence, err := scanVersionRevision(tx.QueryRow(ctx, versionRevisionReadSQL, tenantID, []string{idValue}, includeDeleted))
	if err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, err
	}
	rows, err := tx.Query(ctx, representationReadSQL, tenantID, []string{idValue})
	if err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, translateError("read representations", err)
	}
	defer rows.Close()
	for rows.Next() {
		var revisionID string
		representation, err := scanRepresentation(rows, &revisionID)
		if err != nil {
			return catalog.VersionRevision{}, catalog.EvidenceSet{}, err
		}
		revision.Representations = append(revision.Representations, representation)
	}
	if err := rows.Err(); err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, translateError("iterate representations", err)
	}
	rows.Close()
	if err := readSkillPackageMetadata(ctx, tx, tenantID, []string{idValue}, map[string]*catalog.VersionRevision{idValue: &revision}); err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, err
	}
	if err := catalog.ValidateExternalRetrievalEvidence(revision, evidence); err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "stored external retrieval evidence does not match its version revision", err)
	}
	return revision, evidence, nil
}

// scanVersionRevision joins immutable revision metadata with the selected
// validation run, decodes evidence and rejects invalid retained retrieval
// provenance.
func scanVersionRevision(row pgx.Row) (catalog.VersionRevision, catalog.EvidenceSet, error) {
	var revision catalog.VersionRevision
	var evidence catalog.EvidenceSet
	var revisionFindings, adoption, evidenceFindings, legalRights, trust, externalRetrievals []byte
	var expiresAt *time.Time
	err := row.Scan(
		&revision.ID, &revision.TenantID, &revision.ResourceVersionID, &revision.RevisionNumber, &revision.ContentSetDigest, &revisionFindings, &revision.EvidenceSetID, &adoption, &revision.CreatedBy, &revision.CreatedAt,
		&evidence.ID, &evidence.TenantID, &evidence.SubjectDigest, &evidence.Digest, &evidence.State, &evidenceFindings, &evidence.ValidationProfile, &evidence.PolicyVersion, &legalRights, &trust, &externalRetrievals, &expiresAt, &evidence.CreatedAt)
	if err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, translateError("read version revision", err)
	}
	revision.EvidenceSetID = evidence.ID
	if expiresAt != nil {
		evidence.ExpiresAt = *expiresAt
	}
	if err := unmarshalRetainedFindings(revisionFindings, &revision.Findings); err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, err
	}
	if len(adoption) > 0 && string(adoption) != "null" {
		revision.Adoption = &catalog.AdoptionProvenance{}
		if err := unmarshalJSON(adoption, revision.Adoption); err != nil {
			return catalog.VersionRevision{}, catalog.EvidenceSet{}, err
		}
	}
	if err := unmarshalRetainedFindings(evidenceFindings, &evidence.Findings); err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, err
	}
	if err := unmarshalJSON(legalRights, &evidence.LegalRights); err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, err
	}
	if err := unmarshalJSON(trust, &evidence.Trust); err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, err
	}
	if err := unmarshalJSON(externalRetrievals, &evidence.ExternalRetrievals); err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, err
	}
	if err := catalog.ValidateExternalRetrievals(evidence.ExternalRetrievals); err != nil {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "stored external retrieval evidence failed its integrity check", err)
	}
	return revision, evidence, nil
}

// scanRepresentation decodes raw document and inspection metadata; Skill
// references and file inventory are attached separately before full integrity
// validation.
func scanRepresentation(row pgx.Row, revisionID *string) (catalog.Representation, error) {
	var representation catalog.Representation
	var data, findings, facts, unknown, plugin []byte
	var externalReferenceOnly bool
	var packageState, packageRoot string
	var packageExpandedSize int64
	var packageFileCount int
	var packageMalwareState, packageSecretsState, packageLicenseState, packageSBOMState, packageProvenanceState string
	if err := row.Scan(revisionID, &representation.ID, &representation.Role, &representation.MediaType, &representation.ProfileID, &representation.URL, &data, &representation.ContentDigest, &representation.Size, &representation.ArtifactID, &representation.ValidationState, &findings, &facts, &unknown, &externalReferenceOnly, &packageState, &packageRoot, &packageExpandedSize, &packageFileCount, &packageMalwareState, &packageSecretsState, &packageLicenseState, &packageSBOMState, &packageProvenanceState, &plugin); err != nil {
		return catalog.Representation{}, translateError("scan representation", err)
	}
	if len(data) > 0 && string(data) != "null" {
		representation.Data = json.RawMessage(data)
	}
	if err := unmarshalRetainedFindings(findings, &representation.Findings); err != nil {
		return catalog.Representation{}, err
	}
	if err := unmarshalJSON(facts, &representation.NormalizedFacts); err != nil {
		return catalog.Representation{}, err
	}
	if err := unmarshalJSON(unknown, &representation.UnknownProperties); err != nil {
		return catalog.Representation{}, err
	}
	if len(plugin) > 0 && string(plugin) != "null" {
		if err := unmarshalJSON(plugin, &representation.Plugin); err != nil {
			return catalog.Representation{}, err
		}
	}
	if externalReferenceOnly {
		representation.Package = &catalog.SkillPackageInspection{
			StructureState: catalog.PackageAssessmentState(packageState),
			RootDirectory:  packageRoot,
			ExpandedSize:   packageExpandedSize,
			FileCount:      packageFileCount,
			Assessments: catalog.SkillPackageAssessments{
				Malware:    catalog.PackageAssessmentState(packageMalwareState),
				Secrets:    catalog.PackageAssessmentState(packageSecretsState),
				License:    catalog.PackageAssessmentState(packageLicenseState),
				SBOM:       catalog.PackageAssessmentState(packageSBOMState),
				Provenance: catalog.PackageAssessmentState(packageProvenanceState),
			},
		}
	}
	return representation, nil
}

// readValidationEvidence proves the evidence-to-revision run relation and active
// parent before decoding and validating retained evidence.
func readValidationEvidence(ctx context.Context, tx pgx.Tx, tenantID, versionRevisionID, evidenceID string) (catalog.EvidenceSet, error) {
	var evidence catalog.EvidenceSet
	var findings, legalRights, trust, externalRetrievals []byte
	var expiresAt *time.Time
	if err := tx.QueryRow(ctx, `
    SELECT evidence.id,evidence.tenant_id,evidence.subject_digest,evidence.digest,evidence.state,CASE WHEN octet_length(evidence.findings::text) <= 33554432 THEN evidence.findings ELSE '"diagnostics_read_limit"'::jsonb END,
           evidence.validation_profile,evidence.policy_version,evidence.rights,evidence.trust,evidence.external_retrievals,
           run.expires_at,evidence.created_at
    FROM resource_version_revisions revision
	JOIN resource_versions version ON version.tenant_id=revision.tenant_id AND version.id=revision.resource_version_id
	JOIN resources resource ON resource.tenant_id=version.tenant_id AND resource.id=version.resource_id AND resource.deleted_at IS NULL
    JOIN evidence_sets evidence ON evidence.tenant_id=revision.tenant_id AND evidence.id=$3
    JOIN version_validation_runs run ON run.tenant_id=revision.tenant_id AND run.version_revision_id=revision.id AND run.evidence_set_id=evidence.id
    WHERE revision.tenant_id=$1 AND revision.id=$2`, tenantID, versionRevisionID, evidenceID).Scan(
		&evidence.ID, &evidence.TenantID, &evidence.SubjectDigest, &evidence.Digest, &evidence.State, &findings,
		&evidence.ValidationProfile, &evidence.PolicyVersion, &legalRights, &trust, &externalRetrievals, &expiresAt, &evidence.CreatedAt); err != nil {
		return catalog.EvidenceSet{}, translateError("read version validation evidence", err)
	}
	if expiresAt != nil {
		evidence.ExpiresAt = *expiresAt
	}
	if err := unmarshalRetainedFindings(findings, &evidence.Findings); err != nil {
		return catalog.EvidenceSet{}, err
	}
	if err := unmarshalJSON(legalRights, &evidence.LegalRights); err != nil {
		return catalog.EvidenceSet{}, err
	}
	if err := unmarshalJSON(trust, &evidence.Trust); err != nil {
		return catalog.EvidenceSet{}, err
	}
	if err := unmarshalJSON(externalRetrievals, &evidence.ExternalRetrievals); err != nil {
		return catalog.EvidenceSet{}, err
	}
	if err := catalog.ValidateExternalRetrievals(evidence.ExternalRetrievals); err != nil {
		return catalog.EvidenceSet{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "stored external retrieval evidence failed its integrity check", err)
	}
	return evidence, nil
}

// readSkillPackageMetadata attaches only same-tenant references and inventory to
// selected revisions, then revalidates package metadata and content-set digests.
func readSkillPackageMetadata(ctx context.Context, tx pgx.Tx, tenantID string, revisionIDs []string, revisions map[string]*catalog.VersionRevision) error {
	indices := make(map[string]*catalog.Representation)
	for _, revision := range revisions {
		for index := range revision.Representations {
			representation := &revision.Representations[index]
			indices[representation.ID] = representation
		}
	}
	referenceRows, err := tx.Query(ctx, `SELECT a.representation_id,a.reference_kind,coalesce(a.git_repository_url,''),coalesce(a.git_commit,''),coalesce(a.git_directory,''),coalesce(a.oci_reference,'') FROM artifact_references a JOIN representations r ON r.tenant_id=a.tenant_id AND r.id=a.representation_id WHERE r.tenant_id=$1 AND r.version_revision_id=ANY($2::uuid[]) ORDER BY a.representation_id,a.reference_kind`, tenantID, revisionIDs)
	if err != nil {
		return translateError("read artifact references", err)
	}
	for referenceRows.Next() {
		var representationID, kind, repository, commit, directory, ociReference string
		if err := referenceRows.Scan(&representationID, &kind, &repository, &commit, &directory, &ociReference); err != nil {
			referenceRows.Close()
			return translateError("scan artifact reference", err)
		}
		representation, ok := indices[representationID]
		if !ok || representation.Package == nil {
			referenceRows.Close()
			return catalog.E(catalog.CodeDependencyUnavailable, "stored Skill package reference integrity check failed")
		}
		if representation.Handoff == nil {
			representation.Handoff = &catalog.SkillHandoff{}
		}
		switch kind {
		case "git":
			representation.Handoff.Git = &catalog.GitHandoffReference{Repository: repository, Commit: commit, Directory: directory}
		case "oci":
			representation.Handoff.OCI = &catalog.OCIHandoffReference{Reference: ociReference}
		default:
			referenceRows.Close()
			return catalog.E(catalog.CodeDependencyUnavailable, "stored Skill package reference kind is invalid")
		}
	}
	if err := referenceRows.Err(); err != nil {
		referenceRows.Close()
		return translateError("iterate artifact references", err)
	}
	referenceRows.Close()

	fileRows, err := tx.Query(ctx, `SELECT f.representation_id,f.path,f.size_bytes,f.media_type,f.content_digest,f.executable FROM skill_file_inventory f JOIN representations r ON r.tenant_id=f.tenant_id AND r.id=f.representation_id WHERE r.tenant_id=$1 AND r.version_revision_id=ANY($2::uuid[]) ORDER BY f.representation_id,f.path COLLATE "C"`, tenantID, revisionIDs)
	if err != nil {
		return translateError("read Skill package file inventory", err)
	}
	for fileRows.Next() {
		var representationID string
		var file catalog.SkillPackageFile
		if err := fileRows.Scan(&representationID, &file.Path, &file.Size, &file.MediaType, &file.Digest, &file.Executable); err != nil {
			fileRows.Close()
			return translateError("scan Skill package file inventory", err)
		}
		representation, ok := indices[representationID]
		if !ok || representation.Package == nil {
			fileRows.Close()
			return catalog.E(catalog.CodeDependencyUnavailable, "stored Skill package inventory integrity check failed")
		}
		representation.Package.Files = append(representation.Package.Files, file)
	}
	if err := fileRows.Err(); err != nil {
		fileRows.Close()
		return translateError("iterate Skill package file inventory", err)
	}
	fileRows.Close()
	for _, revisionID := range revisionIDs {
		revision := revisions[revisionID]
		for _, representation := range revision.Representations {
			if err := catalog.ValidatePackageRepresentation(representation); err != nil {
				return catalog.Wrap(catalog.CodeDependencyUnavailable, "stored Skill package metadata integrity check failed", err)
			}
		}
		if err := catalog.ValidatePackageRevisionDigest(*revision); err != nil {
			return catalog.Wrap(catalog.CodeDependencyUnavailable, "stored Skill package revision digest integrity check failed", err)
		}
	}
	return nil
}

// nullTime maps an absent timestamp pointer to SQL NULL without substituting a
// zero instant.
func nullTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

// unmarshalRetainedFindings recognizes the SQL size sentinel before decoding.
// The fixed historical-read ceiling is independent of new validation budgets.
// JSONB null/empty findings keep their existing decoding behavior.
func unmarshalRetainedFindings(raw []byte, target *[]catalog.Finding) error {
	if string(raw) == `"diagnostics_read_limit"` {
		return catalog.E(catalog.CodeWorkLimitExceeded, "retained validation findings exceed the 32 MiB read limit")
	}
	return unmarshalJSON(raw, target)
}
