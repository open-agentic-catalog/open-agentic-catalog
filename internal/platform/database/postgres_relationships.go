package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Release selection uses the sealed revisions. Candidate edits never enter either endpoint.
const relationshipReleaseFrom = `
 FROM listing_releases release
 JOIN catalog_listings listing ON listing.tenant_id=release.tenant_id AND listing.id=release.listing_id
 JOIN resources r ON r.tenant_id=listing.tenant_id AND r.id=listing.resource_id AND r.deleted_at IS NULL
 JOIN resource_version_revisions vr ON vr.tenant_id=release.tenant_id AND vr.id=release.version_revision_id
 JOIN resource_versions v ON v.tenant_id=vr.tenant_id AND v.id=vr.resource_version_id
 JOIN listing_revisions rev ON rev.tenant_id=release.tenant_id AND rev.id=release.listing_revision_id
 WHERE release.tenant_id=$1 AND `

const eligibleRelationshipRelease = `listing.current_release_id=release.id AND release.state IN ('published','deprecated')`

var readableRelationshipRelease = relationshipReleaseFrom + collectionPermissionSQL("resource.read", "r.namespace_id")
var currentRelationshipRows = `WITH readable AS (
 SELECT release.id, rev.id AS revision_id, rev.display_name, r.canonical_identifier, v.version, rev.relationships` +
	readableRelationshipRelease + " AND " + eligibleRelationshipRelease + ") "

var incomingRelationshipSQL = currentRelationshipRows + `
 SELECT source.id::text, edge.ord, edge.value->>'type', edge.value->>'targetIdentifier', coalesce(edge.value->>'targetVersion',''),
        source.id::text, source.revision_id::text, source.display_name, source.version
 FROM readable source
 CROSS JOIN LATERAL jsonb_array_elements(nullif(source.relationships,'null'::jsonb)) WITH ORDINALITY edge(value,ord)
 WHERE source.relationships @> jsonb_build_array(jsonb_build_object('targetIdentifier',$2::text))
 AND edge.value->>'targetIdentifier'=$2
 AND (NOT $3::boolean OR (edge.value->>'type'='depends_on' AND (edge.value->>'targetVersion' IS NULL OR edge.value->>'targetVersion'=$4)))
 AND (source.id,edge.ord)>(coalesce(nullif($5,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid),$6::bigint)
 ORDER BY source.id,edge.ord LIMIT $7`

var outgoingRelationshipSQL = currentRelationshipRows + `
 SELECT source.id::text, edge.ord, edge.value->>'type', edge.value->>'targetIdentifier', coalesce(edge.value->>'targetVersion',''),
        coalesce(dest.id::text,''), coalesce(dest.revision_id::text,''), coalesce(dest.display_name,''), coalesce(dest.version,'')
 FROM listing_revisions source_rev
 JOIN listing_releases source ON source.tenant_id=source_rev.tenant_id AND source.listing_revision_id=source_rev.id
 CROSS JOIN LATERAL jsonb_array_elements(nullif(source_rev.relationships,'null'::jsonb)) WITH ORDINALITY edge(value,ord)
 LEFT JOIN readable dest ON dest.canonical_identifier=edge.value->>'targetIdentifier'
 AND (edge.value->>'targetVersion' IS NULL OR dest.version=edge.value->>'targetVersion')
 WHERE source.tenant_id=$1 AND source.id=$2 AND edge.ord>$3 ORDER BY edge.ord LIMIT $4`

func (p *PostgresRepository) ListReleaseRelationships(ctx context.Context, tenant, releaseID string, request catalog.RelationshipPageRequest) (catalog.RelationshipSelection, error) {
	return withTenantCollectionRead(ctx, p.pool, tenant, func(tx pgx.Tx) (catalog.RelationshipSelection, error) {
		// A withdrawal may concern a historical release. Its exact version is private
		// release history. The ordinary reader views require a current release.
		eligibility := eligibleRelationshipRelease
		if request.View == catalog.ReleaseDependencies {
			eligibility = collectionPermissionSQL("resource.read_history", "r.namespace_id")
		}
		var identifier, version, generation string
		subject := `SELECT r.canonical_identifier, v.version, coalesce((SELECT generation::text FROM catalog_search_generations WHERE tenant_id=$1),'0')` +
			readableRelationshipRelease + " AND release.id=$2 AND " + eligibility
		if err := tx.QueryRow(ctx, collectionStatement(ctx, subject), tenant, releaseID).Scan(&identifier, &version, &generation); err != nil {
			return catalog.RelationshipSelection{}, translateError("read relationship subject", err)
		}
		page := catalog.RelationshipSelection{Items: []catalog.ReleasedRelationship{}, Generation: catalog.RelationshipGeneration(ctx, generation)}
		if request.Generation != "" && request.Generation != page.Generation {
			return catalog.RelationshipSelection{}, catalog.E(catalog.CodeInvalidArgument, "release relationships changed; restart the read")
		}
		statement := incomingRelationshipSQL
		args := []any{tenant, identifier, request.View == catalog.ReleaseDependencies, version, request.AfterReleaseID, request.AfterOrdinal, request.Limit + 1}
		if request.View == catalog.OutgoingRelationships {
			statement, args = outgoingRelationshipSQL, []any{tenant, releaseID, request.AfterOrdinal, request.Limit + 1}
		}
		rows, err := tx.Query(ctx, collectionStatement(ctx, statement), args...)
		if err != nil {
			return catalog.RelationshipSelection{}, translateError("select release relationships", err)
		}
		defer rows.Close()
		for rows.Next() {
			var edge catalog.ReleasedRelationship
			var related catalog.RelatedRelease
			err := rows.Scan(&edge.SourceReleaseID, &edge.Ordinal, &edge.Declaration.Type, &edge.Declaration.TargetIdentifier, &edge.Declaration.TargetVersion, &related.ReleaseID, &related.ListingRevisionID, &related.DisplayName, &related.Version)
			if err != nil {
				return catalog.RelationshipSelection{}, translateError("read relationship", err)
			}
			if related.ReleaseID != "" {
				edge.Related = &related
			}
			page.Items = append(page.Items, edge)
		}
		return page, rows.Err()
	})
}
