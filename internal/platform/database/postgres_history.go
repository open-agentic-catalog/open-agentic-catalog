// History queries select tenant/parent-bound pages in SQL without hydrating
// complete documents for every listed revision.

package database

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// HasPublishedIdentity reports whether a nondeleted resource has a published or
// deprecated release for the exact canonical identifier and version in the tenant.
func (p *PostgresRepository) HasPublishedIdentity(ctx context.Context, tenantID, identifier, version string) (bool, error) {
	return withTenantCollectionRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (bool, error) {
		var found bool
		err := tx.QueryRow(ctx, collectionStatement(ctx, `SELECT EXISTS (
SELECT 1 FROM resources resource
JOIN catalog_listings listing ON listing.tenant_id=resource.tenant_id AND listing.resource_id=resource.id
JOIN listing_releases release ON release.tenant_id=listing.tenant_id AND release.id=listing.current_release_id AND release.state IN ('published','deprecated')
JOIN resource_version_revisions revision ON revision.tenant_id=release.tenant_id AND revision.id=release.version_revision_id
JOIN resource_versions version ON version.tenant_id=revision.tenant_id AND version.id=revision.resource_version_id
WHERE resource.tenant_id=$1 AND resource.canonical_identifier=$2 AND resource.deleted_at IS NULL AND version.version=$3 AND `+collectionPermissionSQL("resource.read", "listing.namespace_id")+`
)`), tenantID, identifier, version).Scan(&found)
		return found, translateError("check published identity", err)
	})
}

// nullableUUID converts an absent cursor or optional identifier to SQL NULL
// without changing supplied UUID text.
func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ListVersions returns an ID-ordered, SQL-limited version page for one
// tenant-bound, nondeleted resource without hydrating revision documents.
func (p *PostgresRepository) ListVersions(ctx context.Context, tenantID, parent string, request catalog.HistoryPageRequest) ([]catalog.ResourceVersion, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.ResourceVersion, error) {
		rows, err := tx.Query(ctx, `SELECT v.id,v.tenant_id,v.resource_id,v.version,coalesce(v.candidate_revision_id::text,''),coalesce(v.sealed_revision_id::text,''),v.revision,v.created_at,v.updated_at FROM resource_versions v JOIN resources r ON r.tenant_id=v.tenant_id AND r.id=v.resource_id AND r.deleted_at IS NULL
WHERE v.tenant_id=$1 AND v.resource_id=$2 AND ($3::uuid IS NULL OR v.id>$3) ORDER BY v.id LIMIT $4`, tenantID, parent, nullableUUID(request.AfterID), request.Limit)
		if err != nil {
			return nil, translateError("list history", err)
		}
		defer rows.Close()
		items := make([]catalog.ResourceVersion, 0)
		for rows.Next() {
			item, err := scanVersion(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, translateError("iterate history", rows.Err())
	})
}

// ListVersionRevisions returns an ID-ordered, SQL-limited metadata page for one
// tenant-bound resource version without loading representation documents.
func (p *PostgresRepository) ListVersionRevisions(ctx context.Context, tenantID, parent string, request catalog.HistoryPageRequest) ([]catalog.RevisionSummary, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.RevisionSummary, error) {
		rows, err := tx.Query(ctx, `SELECT v.id,v.revision_number,v.content_set_digest,v.created_by,v.created_at FROM resource_version_revisions v JOIN resource_versions p ON p.tenant_id=v.tenant_id AND p.id=v.resource_version_id JOIN resources r ON r.tenant_id=p.tenant_id AND r.id=p.resource_id AND r.deleted_at IS NULL
WHERE v.tenant_id=$1 AND v.resource_version_id=$2 AND ($3::uuid IS NULL OR v.id>$3) ORDER BY v.id LIMIT $4`, tenantID, parent, nullableUUID(request.AfterID), request.Limit)
		if err != nil {
			return nil, translateError("list history", err)
		}
		defer rows.Close()
		items := make([]catalog.RevisionSummary, 0)
		for rows.Next() {
			item, err := scanRevisionSummary(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, translateError("iterate history", rows.Err())
	})
}

// ListListings returns an ID-ordered, SQL-limited page of listings for one
// tenant-bound, nondeleted resource.
func (p *PostgresRepository) ListListings(ctx context.Context, tenantID, parent string, request catalog.HistoryPageRequest) ([]catalog.CatalogListing, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.CatalogListing, error) {
		rows, err := tx.Query(ctx, `SELECT v.id,v.tenant_id,v.namespace_id,v.resource_id,coalesce(v.candidate_revision_id::text,''),coalesce(v.current_release_id::text,''),v.revision,v.created_at,v.updated_at FROM catalog_listings v JOIN resources r ON r.tenant_id=v.tenant_id AND r.id=v.resource_id AND r.deleted_at IS NULL
WHERE v.tenant_id=$1 AND v.resource_id=$2 AND ($3::uuid IS NULL OR v.id>$3) ORDER BY v.id LIMIT $4`, tenantID, parent, nullableUUID(request.AfterID), request.Limit)
		if err != nil {
			return nil, translateError("list history", err)
		}
		defer rows.Close()
		items := make([]catalog.CatalogListing, 0)
		for rows.Next() {
			item, err := scanListing(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, translateError("iterate history", rows.Err())
	})
}

// ListListingRevisions returns an ID-ordered, SQL-limited metadata page for one
// tenant-bound listing, excluding deleted resources.
func (p *PostgresRepository) ListListingRevisions(ctx context.Context, tenantID, parent string, request catalog.HistoryPageRequest) ([]catalog.RevisionSummary, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.RevisionSummary, error) {
		rows, err := tx.Query(ctx, `SELECT v.id,v.revision_number,v.digest,v.created_by,v.created_at FROM listing_revisions v JOIN catalog_listings p ON p.tenant_id=v.tenant_id AND p.id=v.listing_id JOIN resources r ON r.tenant_id=p.tenant_id AND r.id=p.resource_id AND r.deleted_at IS NULL
WHERE v.tenant_id=$1 AND v.listing_id=$2 AND ($3::uuid IS NULL OR v.id>$3) ORDER BY v.id LIMIT $4`, tenantID, parent, nullableUUID(request.AfterID), request.Limit)
		if err != nil {
			return nil, translateError("list history", err)
		}
		defer rows.Close()
		items := make([]catalog.RevisionSummary, 0)
		for rows.Next() {
			item, err := scanRevisionSummary(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, translateError("iterate history", rows.Err())
	})
}

// ListReleases returns an ID-ordered, SQL-limited release history for one
// tenant-bound listing, including noncurrent release states.
func (p *PostgresRepository) ListReleases(ctx context.Context, tenantID, parent string, request catalog.HistoryPageRequest) ([]catalog.ListingRelease, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.ListingRelease, error) {
		rows, err := tx.Query(ctx, `SELECT v.id,v.tenant_id,v.listing_id,v.submission_id,v.version_revision_id,v.listing_revision_id,v.state,coalesce(v.replacement_release_id::text,''),v.disposition_reason,v.sunset_at,v.activated_by,v.revision,v.activated_at,v.updated_at FROM listing_releases v JOIN catalog_listings p ON p.tenant_id=v.tenant_id AND p.id=v.listing_id JOIN resources r ON r.tenant_id=p.tenant_id AND r.id=p.resource_id AND r.deleted_at IS NULL
WHERE v.tenant_id=$1 AND v.listing_id=$2 AND ($3::uuid IS NULL OR v.id>$3) ORDER BY v.id LIMIT $4`, tenantID, parent, nullableUUID(request.AfterID), request.Limit)
		if err != nil {
			return nil, translateError("list history", err)
		}
		defer rows.Close()
		items := make([]catalog.ListingRelease, 0)
		for rows.Next() {
			item, err := scanRelease(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, translateError("iterate history", rows.Err())
	})
}

// scanRevisionSummary decodes immutable revision metadata without hydrating the
// retained document.
func scanRevisionSummary(row scanner) (catalog.RevisionSummary, error) {
	var item catalog.RevisionSummary
	err := row.Scan(&item.ID, &item.RevisionNumber, &item.Digest, &item.CreatedBy, &item.CreatedAt)
	return item, translateError("read revision summary", err)
}

// ListSubmissions returns a SQL-limited tenant page of review submissions with
// optional listing and state filters, excluding deleted resources.
func (p *PostgresRepository) ListSubmissions(ctx context.Context, tenantID string, request catalog.SubmissionListRequest) ([]catalog.ReviewSubmission, error) {
	return withTenantCollectionRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.ReviewSubmission, error) {
		rows, err := tx.Query(ctx, collectionStatement(ctx, `SELECT v.id,v.tenant_id,v.listing_id,v.version_revision_id,v.listing_revision_id,v.evidence_set_id,v.evidence_digest,v.policy_version,v.state,v.submitted_by,v.decisions,v.revision,v.submitted_at,v.decided_at,
CASE WHEN `+collectionPermissionSQL("resource.read", "p.namespace_id")+` THEN r.canonical_identifier ELSE '' END,
CASE WHEN `+collectionPermissionSQL("resource.read_content", "p.namespace_id")+` THEN coalesce(submitted.display_name,'') ELSE '' END
FROM review_submissions v JOIN catalog_listings p ON p.tenant_id=v.tenant_id AND p.id=v.listing_id JOIN resources r ON r.tenant_id=p.tenant_id AND r.id=p.resource_id AND r.deleted_at IS NULL
LEFT JOIN listing_revisions submitted ON submitted.tenant_id=v.tenant_id AND submitted.listing_id=v.listing_id AND submitted.id=v.listing_revision_id
WHERE v.tenant_id=$1 AND `+collectionPermissionSQL("resource.read_history", "p.namespace_id")+` AND ($2::uuid IS NULL OR v.listing_id=$2) AND ($3='' OR v.state=$3) AND ($4::uuid IS NULL OR v.id>$4)
AND (NOT $6::boolean OR `+unreleasedSubmissionSQL("v")+`)
ORDER BY v.id LIMIT $5`), tenantID, nullableUUID(request.ListingID), request.State, nullableUUID(request.AfterID), request.Limit, request.Unreleased)
		if err != nil {
			return nil, translateError("list submissions", err)
		}
		defer rows.Close()
		items := make([]catalog.ReviewSubmission, 0)
		for rows.Next() {
			item, err := scanSubmissionContext(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, translateError("iterate submissions", rows.Err())
	})
}
