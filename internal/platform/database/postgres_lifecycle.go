// Listing persistence coordinates candidate, review and release transitions with
// current-pointer checks and immutable history.

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// CreateListing locks the active resource and inserts its tenant-owned listing
// with atomic creation evidence.
func (p *PostgresRepository) CreateListing(ctx context.Context, params catalog.CreateListingParams) (catalog.CatalogListing, error) {
	return withTenantTx(ctx, p.pool, params.Listing.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.CatalogListing, error) {
		if err := lockActiveResource(ctx, tx, params.Listing.TenantID, params.Listing.ResourceID); err != nil {
			return catalog.CatalogListing{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO catalog_listings(id,tenant_id,namespace_id,resource_id,revision,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, params.Listing.ID, params.Listing.TenantID, params.Listing.NamespaceID, params.Listing.ResourceID, params.Listing.Revision, params.Listing.CreatedAt, params.Listing.UpdatedAt); err != nil {
			return catalog.CatalogListing{}, translateError("create catalog listing", err)
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.Listing.TenantID, "catalog_listing", params.Listing.ID, "catalog_listing.created")); err != nil {
			return catalog.CatalogListing{}, err
		}
		return params.Listing, nil
	})
}

// GetListing resolves the tenant-owned listing only while its parent resource
// remains active.
func (p *PostgresRepository) GetListing(ctx context.Context, tenantID, idValue string) (catalog.CatalogListing, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.CatalogListing, error) {
		return scanListing(tx.QueryRow(ctx, `SELECT listing.id,listing.tenant_id,listing.namespace_id,listing.resource_id,coalesce(listing.candidate_revision_id::text,''),coalesce(listing.current_release_id::text,''),listing.revision,listing.created_at,listing.updated_at FROM catalog_listings listing JOIN resources resource ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id AND resource.deleted_at IS NULL WHERE listing.tenant_id=$1 AND listing.id=$2`, tenantID, idValue))
	})
}

// scanListing decodes stable listing identity and current candidate/release
// pointers without hydrating their documents.
func scanListing(row scanner) (catalog.CatalogListing, error) {
	var item catalog.CatalogListing
	if err := row.Scan(&item.ID, &item.TenantID, &item.NamespaceID, &item.ResourceID, &item.CandidateRevisionID, &item.CurrentReleaseID, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return catalog.CatalogListing{}, translateError("scan catalog listing", err)
	}
	return item, nil
}

// CreateListingRevision locks the listing and active parent, checks the expected
// revision and atomically appends immutable metadata and advances the candidate
// pointer.
func (p *PostgresRepository) CreateListingRevision(ctx context.Context, params catalog.CreateListingRevisionParams) (catalog.CatalogListing, catalog.ListingRevision, error) {
	bundle, err := withTenantTx(ctx, p.pool, params.Revision.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (listingRevisionBundle, error) {
		listing, err := scanListing(tx.QueryRow(ctx, `SELECT id,tenant_id,namespace_id,resource_id,coalesce(candidate_revision_id::text,''),coalesce(current_release_id::text,''),revision,created_at,updated_at FROM catalog_listings WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, params.Revision.TenantID, params.Revision.ListingID))
		if err != nil {
			return listingRevisionBundle{}, err
		}
		if err := lockActiveResource(ctx, tx, params.Revision.TenantID, listing.ResourceID); err != nil {
			return listingRevisionBundle{}, err
		}
		if listing.Revision != params.ExpectedRevision {
			return listingRevisionBundle{}, catalog.E(catalog.CodePreconditionFailed, "catalog listing revision does not match")
		}
		relationships, err := marshalJSON(params.Revision.Relationships)
		if err != nil {
			return listingRevisionBundle{}, err
		}
		extensions, err := marshalJSON(params.Revision.Extensions)
		if err != nil {
			return listingRevisionBundle{}, err
		}
		if _, err := tx.Exec(ctx, `
      INSERT INTO listing_revisions(id,tenant_id,listing_id,revision_number,version_revision_id,display_name,description,tags,categories,representative_queries,publisher_id,documentation_url,support_url,license,terms_url,visibility,relationships,extensions,digest,created_by,created_at)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,nullif($12,''),nullif($13,''),$14,nullif($15,''),$16,$17,$18,$19,$20,$21)`,
			params.Revision.ID, params.Revision.TenantID, params.Revision.ListingID, params.Revision.RevisionNumber, params.Revision.VersionRevisionID, params.Revision.DisplayName, params.Revision.Description, params.Revision.Tags, params.Revision.Categories, params.Revision.RepresentativeQueries, params.Revision.PublisherID, params.Revision.DocumentationURL, params.Revision.SupportURL, params.Revision.License, params.Revision.TermsURL, params.Revision.Visibility, relationships, extensions, params.Revision.Digest, params.Revision.CreatedBy, params.Revision.CreatedAt); err != nil {
			return listingRevisionBundle{}, translateError("create listing revision", err)
		}
		listing.CandidateRevisionID = params.Revision.ID
		listing.Revision++
		listing.UpdatedAt = params.Revision.CreatedAt
		command, err := tx.Exec(ctx, `UPDATE catalog_listings SET candidate_revision_id=$3,revision=$4,updated_at=$5 WHERE tenant_id=$1 AND id=$2 AND revision=$6`, params.Revision.TenantID, listing.ID, listing.CandidateRevisionID, listing.Revision, listing.UpdatedAt, params.ExpectedRevision)
		if err != nil {
			return listingRevisionBundle{}, translateError("update listing candidate", err)
		}
		if command.RowsAffected() != 1 {
			return listingRevisionBundle{}, catalog.E(catalog.CodePreconditionFailed, "catalog listing changed concurrently")
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.Revision.TenantID, "catalog_listing", params.Revision.ListingID, "catalog_listing.revised")); err != nil {
			return listingRevisionBundle{}, err
		}
		return listingRevisionBundle{listing: listing, revision: params.Revision}, nil
	})
	return bundle.listing, bundle.revision, err
}

// AdoptExternalObservation rechecks the exact current occurrence and its source
// conflict set while atomically creating a separate local draft lineage.
func (p *PostgresRepository) AdoptExternalObservation(ctx context.Context, params catalog.AdoptExternalObservationParams) (catalog.ExternalObservationAdoption, error) {
	return withTenantTx(ctx, p.pool, params.Resource.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.ExternalObservationAdoption, error) {
		if params.VersionRevision.Adoption == nil || params.Resource.TenantID != params.Version.TenantID || params.Resource.TenantID != params.VersionRevision.TenantID || params.Resource.TenantID != params.Listing.TenantID || params.Resource.TenantID != params.ListingRevision.TenantID {
			return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodeInvalidArgument, "adopted draft tenant or provenance is invalid")
		}
		provenance := params.VersionRevision.Adoption
		var sourceRevision int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM federation_sources WHERE tenant_id=$1 AND id=$2 FOR SHARE`, params.Resource.TenantID, provenance.SourceID).Scan(&sourceRevision); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodePreconditionFailed, "selected observation source is no longer available")
			}
			return catalog.ExternalObservationAdoption{}, translateError("lock selected observation source", err)
		}
		if params.SourceAdmissionRevision < 1 || sourceRevision != params.SourceAdmissionRevision {
			return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodePreconditionFailed, "source policy changed during adoption")
		}
		var sourceConflict bool
		if err := tx.QueryRow(ctx, `
      SELECT EXISTS (
        SELECT 1
          FROM observations conflicting
         WHERE conflicting.tenant_id=observation.tenant_id
           AND conflicting.source_id=observation.source_id
           AND conflicting.identifier=observation.identifier
           AND conflicting.version_present=observation.version_present
           AND conflicting.version IS NOT DISTINCT FROM observation.version
           AND conflicting.freshness='current'
           AND conflicting.normalized_entry_digest IS NOT NULL
           AND conflicting.normalized_entry_digest<>observation.normalized_entry_digest
      )
      FROM observations observation
      JOIN observation_occurrences occurrence
        ON occurrence.tenant_id=observation.tenant_id AND occurrence.observation_id=observation.id
      JOIN source_snapshots snapshot
        ON snapshot.tenant_id=occurrence.tenant_id AND snapshot.source_id=occurrence.source_id AND snapshot.id=occurrence.snapshot_id
      WHERE observation.tenant_id=$1 AND observation.id=$2 AND observation.source_id=$3
        AND observation.freshness='current' AND observation.exact_entry_digest=$8
        AND observation.normalized_entry_digest=$9 AND observation.normalization_profile=$10
        AND occurrence.snapshot_id=$4 AND occurrence.source_revision=$5
        AND occurrence.retrieval_digest=$6 AND occurrence.entry_location=$7
        AND occurrence.exact_entry_digest=$8 AND occurrence.normalized_entry_digest=$9 AND occurrence.evidence_complete
        AND snapshot.state='complete' AND snapshot.enumeration_complete AND snapshot.source_revision=$5
      FOR SHARE OF observation`,
			params.Resource.TenantID, provenance.ObservationID, provenance.SourceID, provenance.SnapshotID, provenance.SourceRevision,
			provenance.RetrievalDigest, provenance.EntryLocation, provenance.ExactEntryDigest, provenance.NormalizedEntryDigest, provenance.NormalizationProfile).Scan(&sourceConflict); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodePreconditionFailed, "selected observation occurrence is no longer current and complete")
			}
			return catalog.ExternalObservationAdoption{}, translateError("lock selected observation occurrence", err)
		}
		if sourceConflict {
			return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodeConflict, "selected source has conflicting current observations for this exact identity and version")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO resources(id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, params.Resource.ID, params.Resource.TenantID, params.Resource.NamespaceID, params.Resource.CanonicalIdentifier, params.Resource.Kind, params.Resource.CreatedBy, params.Resource.Revision, params.Resource.CreatedAt, params.Resource.UpdatedAt); err != nil {
			return catalog.ExternalObservationAdoption{}, translateError("create adopted resource", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO resource_versions(id,tenant_id,resource_id,version,candidate_revision_id,revision,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, params.Version.ID, params.Version.TenantID, params.Version.ResourceID, params.Version.Version, params.VersionRevision.ID, params.Version.Revision, params.Version.CreatedAt, params.Version.UpdatedAt); err != nil {
			return catalog.ExternalObservationAdoption{}, translateError("create adopted resource version", err)
		}
		if err := insertVersionRevision(ctx, tx, params.VersionRevision, params.Evidence, params.Documents); err != nil {
			return catalog.ExternalObservationAdoption{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO catalog_listings(id,tenant_id,namespace_id,resource_id,candidate_revision_id,revision,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, params.Listing.ID, params.Listing.TenantID, params.Listing.NamespaceID, params.Listing.ResourceID, params.ListingRevision.ID, params.Listing.Revision, params.Listing.CreatedAt, params.Listing.UpdatedAt); err != nil {
			return catalog.ExternalObservationAdoption{}, translateError("create adopted catalog listing", err)
		}
		relationships, err := marshalJSON(params.ListingRevision.Relationships)
		if err != nil {
			return catalog.ExternalObservationAdoption{}, err
		}
		extensions, err := marshalJSON(params.ListingRevision.Extensions)
		if err != nil {
			return catalog.ExternalObservationAdoption{}, err
		}
		if _, err := tx.Exec(ctx, `
      INSERT INTO listing_revisions(id,tenant_id,listing_id,revision_number,version_revision_id,display_name,description,tags,categories,representative_queries,publisher_id,documentation_url,support_url,license,terms_url,visibility,relationships,extensions,digest,created_by,created_at)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,nullif($12,''),nullif($13,''),$14,nullif($15,''),$16,$17,$18,$19,$20,$21)`,
			params.ListingRevision.ID, params.ListingRevision.TenantID, params.ListingRevision.ListingID, params.ListingRevision.RevisionNumber, params.ListingRevision.VersionRevisionID, params.ListingRevision.DisplayName, params.ListingRevision.Description, params.ListingRevision.Tags, params.ListingRevision.Categories, params.ListingRevision.RepresentativeQueries, params.ListingRevision.PublisherID, params.ListingRevision.DocumentationURL, params.ListingRevision.SupportURL, params.ListingRevision.License, params.ListingRevision.TermsURL, params.ListingRevision.Visibility, relationships, extensions, params.ListingRevision.Digest, params.ListingRevision.CreatedBy, params.ListingRevision.CreatedAt); err != nil {
			return catalog.ExternalObservationAdoption{}, translateError("create adopted listing revision", err)
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.Resource.TenantID, "resource", params.Resource.ID, "observation.adopted")); err != nil {
			return catalog.ExternalObservationAdoption{}, err
		}
		return catalog.ExternalObservationAdoption{Resource: params.Resource, Version: params.Version, VersionRevision: params.VersionRevision, Evidence: params.Evidence, Listing: params.Listing, ListingRevision: params.ListingRevision}, nil
	})
}

// GetListingRevision reads immutable tenant-owned metadata through the
// active-parent disclosure path.
func (p *PostgresRepository) GetListingRevision(ctx context.Context, tenantID, idValue string) (catalog.ListingRevision, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.ListingRevision, error) {
		return readListingRevision(ctx, tx, tenantID, idValue)
	})
}

// readListingRevision uses the normal active-parent path for retained listing
// metadata.
func readListingRevision(ctx context.Context, tx pgx.Tx, tenantID, idValue string) (catalog.ListingRevision, error) {
	return readListingRevisionIncludingDeleted(ctx, tx, tenantID, idValue, false)
}

const listingRevisionReadSQL = `
	SELECT revision.id,revision.tenant_id,revision.listing_id,revision.revision_number,revision.version_revision_id,revision.display_name,revision.description,revision.tags,revision.categories,revision.representative_queries,revision.publisher_id,
	       coalesce(revision.documentation_url,''),coalesce(revision.support_url,''),revision.license,coalesce(revision.terms_url,''),revision.visibility,revision.relationships,revision.extensions,revision.digest,revision.created_by,revision.created_at
	  FROM listing_revisions revision
	  JOIN catalog_listings listing ON listing.tenant_id=revision.tenant_id AND listing.id=revision.listing_id
	  JOIN resources resource ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id
	 WHERE revision.tenant_id=$1 AND revision.id=ANY($2::uuid[]) AND ($3 OR resource.deleted_at IS NULL)`

// readListingRevisionIncludingDeleted optionally permits a deleted parent for
// retained-history callers while preserving the explicit tenant predicate.
func readListingRevisionIncludingDeleted(ctx context.Context, tx pgx.Tx, tenantID, idValue string, includeDeleted bool) (catalog.ListingRevision, error) {
	return scanListingRevision(tx.QueryRow(ctx, listingRevisionReadSQL, tenantID, []string{idValue}, includeDeleted))
}

// scanListingRevision decodes retained relationships and extension maps alongside
// the fixed immutable metadata projection.
func scanListingRevision(row pgx.Row) (catalog.ListingRevision, error) {
	var item catalog.ListingRevision
	var relationships, extensions []byte
	err := row.Scan(&item.ID, &item.TenantID, &item.ListingID, &item.RevisionNumber, &item.VersionRevisionID, &item.DisplayName, &item.Description, &item.Tags, &item.Categories, &item.RepresentativeQueries, &item.PublisherID, &item.DocumentationURL, &item.SupportURL, &item.License, &item.TermsURL, &item.Visibility, &relationships, &extensions, &item.Digest, &item.CreatedBy, &item.CreatedAt)
	if err != nil {
		return catalog.ListingRevision{}, translateError("read listing revision", err)
	}
	if err := unmarshalJSON(relationships, &item.Relationships); err != nil {
		return catalog.ListingRevision{}, err
	}
	if err := unmarshalListingExtensions(extensions, &item.Extensions); err != nil {
		return catalog.ListingRevision{}, err
	}
	return item, nil
}

// Submit locks the current listing candidate and validation pointer before
// retaining a review submission bound to exactly those revisions and evidence.
func (p *PostgresRepository) Submit(ctx context.Context, params catalog.SubmitParams) (catalog.ReviewSubmission, error) {
	return withTenantTx(ctx, p.pool, params.Submission.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.ReviewSubmission, error) {
		listing, err := scanListing(tx.QueryRow(ctx, `SELECT id,tenant_id,namespace_id,resource_id,coalesce(candidate_revision_id::text,''),coalesce(current_release_id::text,''),revision,created_at,updated_at FROM catalog_listings WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, params.Submission.TenantID, params.Submission.ListingID))
		if err != nil {
			return catalog.ReviewSubmission{}, err
		}
		if err := lockActiveResource(ctx, tx, params.Submission.TenantID, listing.ResourceID); err != nil {
			return catalog.ReviewSubmission{}, err
		}
		if listing.CandidateRevisionID != params.Submission.ListingRevisionID {
			return catalog.ReviewSubmission{}, catalog.E(catalog.CodePreconditionFailed, "listing candidate changed before submission")
		}
		_, currentEvidenceID, err := lockVersionValidationState(ctx, tx, params.Submission.TenantID, params.Submission.VersionRevisionID)
		if err != nil {
			return catalog.ReviewSubmission{}, err
		}
		if currentEvidenceID != params.Submission.EvidenceSetID {
			return catalog.ReviewSubmission{}, catalog.E(catalog.CodePreconditionFailed, "validation evidence changed before submission")
		}
		decisionsValue := params.Submission.Decisions
		if decisionsValue == nil {
			decisionsValue = []catalog.ReviewDecision{}
		}
		decisions, err := marshalJSON(decisionsValue)
		if err != nil {
			return catalog.ReviewSubmission{}, err
		}
		if _, err := tx.Exec(ctx, `
      INSERT INTO review_submissions(id,tenant_id,listing_id,version_revision_id,listing_revision_id,evidence_set_id,evidence_digest,policy_version,state,submitted_by,decisions,revision,submitted_at)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, params.Submission.ID, params.Submission.TenantID, params.Submission.ListingID, params.Submission.VersionRevisionID, params.Submission.ListingRevisionID, params.Submission.EvidenceSetID, params.Submission.EvidenceDigest, params.Submission.PolicyVersion, params.Submission.State, params.Submission.SubmittedBy, decisions, params.Submission.Revision, params.Submission.SubmittedAt); err != nil {
			return catalog.ReviewSubmission{}, translateError("create review submission", err)
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.Submission.TenantID, "review_submission", params.Submission.ID, "review_submission.submitted")); err != nil {
			return catalog.ReviewSubmission{}, err
		}
		return params.Submission, nil
	})
}

// GetSubmission returns one tenant-owned review submission whose parent resource
// remains active.
func (p *PostgresRepository) GetSubmission(ctx context.Context, tenantID, idValue string) (catalog.ReviewSubmission, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.ReviewSubmission, error) {
		return readSubmission(ctx, tx, tenantID, idValue, false)
	})
}

// readSubmission joins the active parent and optionally locks only the submission
// row for a decision.
func readSubmission(ctx context.Context, tx pgx.Tx, tenantID, idValue string, lock bool) (catalog.ReviewSubmission, error) {
	query := `SELECT submission.id,submission.tenant_id,submission.listing_id,submission.version_revision_id,submission.listing_revision_id,submission.evidence_set_id,submission.evidence_digest,submission.policy_version,submission.state,submission.submitted_by,submission.decisions,submission.revision,submission.submitted_at,submission.decided_at FROM review_submissions submission JOIN catalog_listings listing ON listing.tenant_id=submission.tenant_id AND listing.id=submission.listing_id JOIN resources resource ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id AND resource.deleted_at IS NULL WHERE submission.tenant_id=$1 AND submission.id=$2`
	if lock {
		query += ` FOR UPDATE OF submission`
	}
	return scanSubmission(tx.QueryRow(ctx, query, tenantID, idValue))
}

// scanSubmission decodes the retained ordered decision array together with
// submission identity, state and revision.
func scanSubmission(row scanner) (catalog.ReviewSubmission, error) {
	return scanSubmissionFields(row, false)
}

// scanSubmissionContext reads optional current-authorized queue context, never
// writing these display projections into retained submission evidence.
func scanSubmissionContext(row scanner) (catalog.ReviewSubmission, error) {
	return scanSubmissionFields(row, true)
}

func scanSubmissionFields(row scanner, withContext bool) (catalog.ReviewSubmission, error) {
	var item catalog.ReviewSubmission
	var decisions []byte
	fields := []any{&item.ID, &item.TenantID, &item.ListingID, &item.VersionRevisionID, &item.ListingRevisionID, &item.EvidenceSetID, &item.EvidenceDigest, &item.PolicyVersion, &item.State, &item.SubmittedBy, &decisions, &item.Revision, &item.SubmittedAt, &item.DecidedAt}
	if withContext {
		fields = append(fields, &item.CanonicalIdentifier, &item.SubmittedDisplayName)
	}
	if err := row.Scan(fields...); err != nil {
		return catalog.ReviewSubmission{}, translateError("read review submission", err)
	}
	if err := unmarshalJSON(decisions, &item.Decisions); err != nil {
		return catalog.ReviewSubmission{}, err
	}
	return item, nil
}

// DecideSubmission enforces current state, revision and separation of duties;
// approval rechecks validation and seals content only if not already sealed to
// that revision.
func (p *PostgresRepository) DecideSubmission(ctx context.Context, params catalog.DecideSubmissionParams) (catalog.ReviewSubmission, catalog.ResourceVersion, error) {
	bundle, err := withTenantTx(ctx, p.pool, params.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (decisionBundle, error) {
		submission, err := readSubmission(ctx, tx, params.TenantID, params.SubmissionID, true)
		if err != nil {
			return decisionBundle{}, err
		}
		if params.ExpectedRevision > 0 && submission.Revision != params.ExpectedRevision {
			return decisionBundle{}, catalog.E(catalog.CodePreconditionFailed, "review submission revision does not match")
		}
		if submission.State != catalog.SubmissionSubmitted {
			return decisionBundle{}, catalog.E(catalog.CodeInvalidTransition, "review submission is already terminal")
		}
		origin, err := readOriginatingPrincipal(ctx, tx, params.TenantID, "review_submission", submission.ID)
		if err != nil {
			return decisionBundle{}, err
		}
		if !identity.ValidTenantID(params.ActorMembershipID) || !identity.ValidTenantID(params.ActorRealmID) {
			return decisionBundle{}, catalog.E(catalog.CodeForbidden, "reviewer principal cannot be established")
		}
		if origin.SamePrincipal(identity.Actor{TenantID: params.TenantID, MembershipID: params.ActorMembershipID, RealmID: params.ActorRealmID, Subject: params.Actor}) {
			return decisionBundle{}, catalog.E(catalog.CodeSeparationOfDuties, "the submitter cannot decide this submission")
		}
		versionID, currentEvidenceID, err := lockVersionValidationState(ctx, tx, params.TenantID, submission.VersionRevisionID)
		if err != nil {
			return decisionBundle{}, err
		}
		if params.Action == "approve" && (params.ExpectedValidationEvidenceID == "" || currentEvidenceID != params.ExpectedValidationEvidenceID) {
			return decisionBundle{}, catalog.E(catalog.CodePreconditionFailed, "review submission validation evidence is no longer current")
		}
		version, err := scanVersion(tx.QueryRow(ctx, `SELECT id,tenant_id,resource_id,version,coalesce(candidate_revision_id::text,''),coalesce(sealed_revision_id::text,''),revision,created_at,updated_at FROM resource_versions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, params.TenantID, versionID))
		if err != nil {
			return decisionBundle{}, err
		}
		switch params.Action {
		case "approve":
			if version.SealedRevisionID != "" && version.SealedRevisionID != submission.VersionRevisionID {
				return decisionBundle{}, catalog.E(catalog.CodeConflict, "version is sealed to different content")
			}
			// Listing-only reviews reuse sealed content. PostgreSQL correctly
			// forbids even a metadata-only UPDATE of an already sealed version.
			if version.SealedRevisionID == "" {
				version.SealedRevisionID = submission.VersionRevisionID
				version.CandidateRevisionID = submission.VersionRevisionID
				version.Revision++
				version.UpdatedAt = params.At
				if _, err := tx.Exec(ctx, `UPDATE resource_versions SET sealed_revision_id=$3,candidate_revision_id=$3,revision=$4,updated_at=$5 WHERE tenant_id=$1 AND id=$2`, params.TenantID, version.ID, submission.VersionRevisionID, version.Revision, version.UpdatedAt); err != nil {
					return decisionBundle{}, translateError("seal resource version", err)
				}
			}
			submission.State = catalog.SubmissionApproved
		case "request_changes":
			submission.State = catalog.SubmissionChangesRequired
		case "reject":
			submission.State = catalog.SubmissionRejected
		default:
			return decisionBundle{}, catalog.E(catalog.CodeInvalidArgument, "review action is invalid")
		}
		submission.Decisions = append(submission.Decisions, catalog.ReviewDecision{Actor: params.Actor, Action: params.Action, Reason: params.Reason, At: params.At})
		submission.Revision++
		submission.DecidedAt = &params.At
		decisions, err := marshalJSON(submission.Decisions)
		if err != nil {
			return decisionBundle{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE review_submissions SET state=$3,decisions=$4,revision=$5,decided_at=$6 WHERE tenant_id=$1 AND id=$2`, params.TenantID, submission.ID, submission.State, decisions, submission.Revision, params.At); err != nil {
			return decisionBundle{}, translateError("decide review submission", err)
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.TenantID, "review_submission", params.SubmissionID, "review_submission."+params.Action)); err != nil {
			return decisionBundle{}, err
		}
		return decisionBundle{submission: submission, version: version}, nil
	})
	return bundle.submission, bundle.version, err
}

// Publish locks the listing and current validation pointer before atomically
// creating the release, retiring its predecessor, and appending evidence.
func (p *PostgresRepository) Publish(ctx context.Context, params catalog.PublishParams) (catalog.CatalogListing, catalog.ListingRelease, *catalog.ListingRelease, error) {
	bundle, err := withTenantTx(ctx, p.pool, params.TenantID, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) (publicationBundle, error) {
		listing, err := scanListing(tx.QueryRow(ctx, `SELECT id,tenant_id,namespace_id,resource_id,coalesce(candidate_revision_id::text,''),coalesce(current_release_id::text,''),revision,created_at,updated_at FROM catalog_listings WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, params.TenantID, params.ListingID))
		if err != nil {
			return publicationBundle{}, err
		}
		if params.ExpectedRevision > 0 && listing.Revision != params.ExpectedRevision {
			return publicationBundle{}, catalog.E(catalog.CodePreconditionFailed, "catalog listing revision does not match")
		}
		submission, err := readSubmission(ctx, tx, params.TenantID, params.SubmissionID, false)
		if err != nil {
			return publicationBundle{}, err
		}
		if submission.ListingID != listing.ID || submission.State != catalog.SubmissionApproved {
			return publicationBundle{}, catalog.E(catalog.CodePreconditionFailed, "approved submission does not belong to this listing")
		}
		_, currentEvidenceID, err := lockVersionValidationState(ctx, tx, params.TenantID, submission.VersionRevisionID)
		if err != nil {
			return publicationBundle{}, err
		}
		if params.ExpectedValidationEvidenceID == "" || currentEvidenceID != params.ExpectedValidationEvidenceID {
			return publicationBundle{}, catalog.E(catalog.CodePublicationNotReady, "validation evidence changed before publication")
		}
		if _, err := tx.Exec(ctx, `
      INSERT INTO listing_releases(id,tenant_id,listing_id,submission_id,version_revision_id,listing_revision_id,state,disposition_reason,activated_by,revision,activated_at,updated_at)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, params.Release.ID, params.Release.TenantID, params.Release.ListingID, params.Release.SubmissionID, params.Release.VersionRevisionID, params.Release.ListingRevisionID, params.Release.State, params.Release.DispositionReason, params.Release.ActivatedBy, params.Release.Revision, params.Release.ActivatedAt, params.Release.UpdatedAt); err != nil {
			return publicationBundle{}, translateError("create listing release", err)
		}
		var prior *catalog.ListingRelease
		if listing.CurrentReleaseID != "" {
			current, err := readRelease(ctx, tx, params.TenantID, listing.CurrentReleaseID, true)
			if err != nil {
				return publicationBundle{}, err
			}
			current.State = params.PriorDisposition
			current.DispositionReason = params.PriorReason
			current.UpdatedAt = params.Release.ActivatedAt
			current.Revision++
			if params.PriorDisposition == catalog.ReleaseSuperseded {
				current.ReplacementReleaseID = params.Release.ID
			}
			if _, err := tx.Exec(ctx, `UPDATE listing_releases SET state=$3,replacement_release_id=nullif($4,'')::uuid,disposition_reason=$5,revision=$6,updated_at=$7 WHERE tenant_id=$1 AND id=$2`, params.TenantID, current.ID, current.State, current.ReplacementReleaseID, current.DispositionReason, current.Revision, current.UpdatedAt); err != nil {
				return publicationBundle{}, translateError("retire prior release", err)
			}
			prior = &current
		}
		listing.CurrentReleaseID = params.Release.ID
		listing.Revision++
		listing.UpdatedAt = params.Release.ActivatedAt
		if _, err := tx.Exec(ctx, `UPDATE catalog_listings SET current_release_id=$3,revision=$4,updated_at=$5 WHERE tenant_id=$1 AND id=$2`, params.TenantID, listing.ID, listing.CurrentReleaseID, listing.Revision, listing.UpdatedAt); err != nil {
			return publicationBundle{}, translateError("activate listing release", err)
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.TenantID, "listing_release", params.Release.ID, "listing_release.published")); err != nil {
			return publicationBundle{}, err
		}
		return publicationBundle{listing: listing, release: params.Release, prior: prior}, nil
	})
	return bundle.listing, bundle.release, bundle.prior, err
}

// lockVersionValidationState serializes validation-pointer use and returns the
// revision's version identity and latest retained evidence identifier.
func lockVersionValidationState(ctx context.Context, tx pgx.Tx, tenantID, versionRevisionID string) (string, string, error) {
	if err := acquireCatalogStateLock(ctx, tx, "version-validation", tenantID+"\x00"+versionRevisionID); err != nil {
		return "", "", translateError("lock version validation state", err)
	}
	var versionID, evidenceID string
	err := tx.QueryRow(ctx, `
    SELECT revision.resource_version_id,
           coalesce((SELECT run.evidence_set_id FROM version_validation_runs run WHERE run.tenant_id=revision.tenant_id AND run.version_revision_id=revision.id ORDER BY run.run_number DESC LIMIT 1),revision.evidence_set_id)
    FROM resource_version_revisions revision
    WHERE revision.tenant_id=$1 AND revision.id=$2`, tenantID, versionRevisionID).Scan(&versionID, &evidenceID)
	if err != nil {
		return "", "", translateError("lock version validation state", err)
	}
	return versionID, evidenceID, nil
}

// GetRelease reads one retained tenant-owned release without implying it remains
// the listing's current published record.
func (p *PostgresRepository) GetRelease(ctx context.Context, tenantID, idValue string) (catalog.ListingRelease, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.ListingRelease, error) {
		return readRelease(ctx, tx, tenantID, idValue, false)
	})
}

// readRelease selects exact tenant/release identity and optionally obtains a row
// lock for lifecycle mutation.
func readRelease(ctx context.Context, tx pgx.Tx, tenantID, idValue string, lock bool) (catalog.ListingRelease, error) {
	query := `SELECT id,tenant_id,listing_id,submission_id,version_revision_id,listing_revision_id,state,coalesce(replacement_release_id::text,''),disposition_reason,sunset_at,activated_by,revision,activated_at,updated_at FROM listing_releases WHERE tenant_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanRelease(tx.QueryRow(ctx, query, tenantID, idValue))
}

// scanRelease decodes retained release disposition, replacement, sunset and
// revision metadata.
func scanRelease(row scanner) (catalog.ListingRelease, error) {
	var item catalog.ListingRelease
	if err := row.Scan(&item.ID, &item.TenantID, &item.ListingID, &item.SubmissionID, &item.VersionRevisionID, &item.ListingRevisionID, &item.State, &item.ReplacementReleaseID, &item.DispositionReason, &item.SunsetAt, &item.ActivatedBy, &item.Revision, &item.ActivatedAt, &item.UpdatedAt); err != nil {
		return catalog.ListingRelease{}, translateError("read listing release", err)
	}
	return item, nil
}

// TransitionRelease checks revision and legal disposition, updates evidence
// atomically and clears the listing's current pointer for dispositions other than
// deprecation.
func (p *PostgresRepository) TransitionRelease(ctx context.Context, params catalog.TransitionReleaseParams) (catalog.ListingRelease, error) {
	return withTenantTx(ctx, p.pool, params.TenantID, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) (catalog.ListingRelease, error) {
		release, err := readRelease(ctx, tx, params.TenantID, params.ReleaseID, true)
		if err != nil {
			return catalog.ListingRelease{}, err
		}
		if params.ExpectedRevision > 0 && release.Revision != params.ExpectedRevision {
			return catalog.ListingRelease{}, catalog.E(catalog.CodePreconditionFailed, "listing release revision does not match")
		}
		if !validTransition(release.State, params.TargetState) {
			return catalog.ListingRelease{}, catalog.E(catalog.CodeInvalidTransition, "release transition is not permitted")
		}
		release.State = params.TargetState
		release.DispositionReason = params.Reason
		release.ReplacementReleaseID = params.ReplacementID
		release.SunsetAt = params.SunsetAt
		release.Revision++
		release.UpdatedAt = params.At
		if _, err := tx.Exec(ctx, `UPDATE listing_releases SET state=$3,replacement_release_id=nullif($4,'')::uuid,disposition_reason=$5,sunset_at=$6,revision=$7,updated_at=$8 WHERE tenant_id=$1 AND id=$2`, params.TenantID, release.ID, release.State, release.ReplacementReleaseID, release.DispositionReason, nullTime(release.SunsetAt), release.Revision, release.UpdatedAt); err != nil {
			return catalog.ListingRelease{}, translateError("transition listing release", err)
		}
		if params.TargetState != catalog.ReleaseDeprecated {
			if _, err := tx.Exec(ctx, `UPDATE catalog_listings SET current_release_id=NULL,revision=revision+1,updated_at=$3 WHERE tenant_id=$1 AND id=$2 AND current_release_id=$4`, params.TenantID, release.ListingID, params.At, release.ID); err != nil {
				return catalog.ListingRelease{}, translateError("clear listing release", err)
			}
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.TenantID, "listing_release", params.ReleaseID, "listing_release."+string(params.TargetState))); err != nil {
			return catalog.ListingRelease{}, err
		}
		return release, nil
	})
}

// validTransition permits only forward release dispositions; revoked releases
// cannot transition again.
func validTransition(from, to catalog.ReleaseState) bool {
	switch from {
	case catalog.ReleasePublished:
		return to == catalog.ReleaseDeprecated || to == catalog.ReleaseRetired || to == catalog.ReleaseSuperseded || to == catalog.ReleaseRevoked
	case catalog.ReleaseDeprecated:
		return to == catalog.ReleaseRetired || to == catalog.ReleaseSuperseded || to == catalog.ReleaseRevoked
	case catalog.ReleaseRetired, catalog.ReleaseSuperseded:
		return to == catalog.ReleaseRevoked
	case catalog.ReleaseRevoked:
		return false
	default:
		return false
	}
}

// Listing extension values retain JSON numbers without a float64 round trip.
func unmarshalListingExtensions(raw []byte, target *map[string]any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "decode listing extensions", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return catalog.E(catalog.CodeInternal, "decode listing extensions")
	}
	return nil
}
