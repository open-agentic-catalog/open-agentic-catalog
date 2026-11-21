package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// ListListingMetadataGaps reads only released scalar metadata. Outer joins preserve a
// broken current pointer as an error even when none of the optional fields is missing.
func (p *PostgresRepository) ListListingMetadataGaps(ctx context.Context, tenant string, q catalog.ListingMetadataQuery) ([]catalog.ListingMetadataGap, error) {
	if q.Limit < 1 || q.Limit > catalog.MaxCatalogPageSize+1 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "invalid listing metadata page limit")
	}
	return withTenantCollectionRead(ctx, p.pool, tenant, func(tx pgx.Tx) ([]catalog.ListingMetadataGap, error) {
		query := `WITH current_metadata AS (
 SELECT listing.id, listing.namespace_id, listing.resource_id, listing.current_release_id, lr.id AS revision_id,
 r.kind, lr.display_name, v.version, rel.state,
 r.id IS NULL OR rel.id IS NULL OR lr.id IS NULL OR v.id IS NULL AS broken,
 $5::boolean AND coalesce(lr.documentation_url,'')='' AS documentation,
 $6::boolean AND coalesce(lr.support_url,'')='' AS support,
 $7::boolean AND lr.license='' AS license
 FROM catalog_listings listing
 LEFT JOIN resources r ON r.tenant_id=listing.tenant_id AND r.id=listing.resource_id AND r.namespace_id=listing.namespace_id
 LEFT JOIN listing_releases rel ON rel.tenant_id=listing.tenant_id AND rel.id=listing.current_release_id AND rel.listing_id=listing.id
 LEFT JOIN listing_revisions lr ON lr.tenant_id=listing.tenant_id AND lr.id=rel.listing_revision_id AND lr.listing_id=listing.id AND lr.version_revision_id=rel.version_revision_id
 LEFT JOIN resource_version_revisions vr ON vr.tenant_id=listing.tenant_id AND vr.id=rel.version_revision_id
 LEFT JOIN resource_versions v ON v.tenant_id=listing.tenant_id AND v.id=vr.resource_version_id AND v.resource_id=listing.resource_id
 WHERE listing.tenant_id=$1 AND listing.current_release_id IS NOT NULL AND r.deleted_at IS NULL
 AND (` + collectionPermissionSQL("resource.read", "listing.namespace_id") + `)
 AND ($2::uuid IS NULL OR listing.id>$2) AND ($3::uuid IS NULL OR listing.namespace_id=$3)
 AND ($4::text='' OR r.id IS NULL OR r.kind=$4) AND (rel.id IS NULL OR rel.state IN ('published','deprecated'))
 )
 SELECT id,namespace_id,resource_id,current_release_id,revision_id,kind,display_name,version,state,documentation,support,license,broken
 FROM current_metadata WHERE broken OR documentation OR support OR license ORDER BY id LIMIT $8`
		rows, err := tx.Query(ctx, collectionStatement(ctx, query), tenant, nullableUUID(q.AfterID), nullableUUID(q.NamespaceID), q.Kind, q.Checks.Documentation, q.Checks.Support, q.Checks.License, q.Limit)
		if err != nil {
			return nil, translateError("select released Listing metadata", err)
		}
		defer rows.Close()
		items := []catalog.ListingMetadataGap{}
		for rows.Next() {
			var row catalog.ListingMetadataGap
			var missing catalog.ListingMetadataChecks
			var broken bool
			err := rows.Scan(&row.ListingID, &row.NamespaceID, &row.ResourceID, &row.ReleaseID, &row.ListingRevisionID, &row.Kind, &row.DisplayName, &row.Version, &row.State, &missing.Documentation, &missing.Support, &missing.License, &broken)
			if err != nil || broken {
				return nil, catalog.E(catalog.CodeDependencyUnavailable, "invalid released Listing metadata")
			}
			row.Missing = missing.Fields()
			items = append(items, row)
		}
		if err := rows.Err(); err != nil {
			return nil, translateError("read released Listing metadata", err)
		}
		return items, nil
	})
}
