package database

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// workOwnerSQL receives code-owned expressions only. No audit page is read or
// disclosed: indexed exact transition attribution is a selection predicate.
func workOwnerSQL(kind, target, revision, actions string) string {
	extra := ""
	if revision != "" {
		extra = " AND a.details->'facts'->>'revisionId'=" + revision + "::text"
	}
	return `EXISTS(SELECT 1 FROM audit_events a WHERE a.tenant_id=$1 AND a.actor=$2 AND a.details->>'realmId'=$3 AND a.format_version=1 AND a.details->>'principalType'='catalog_membership' AND a.details->>'actorType'='human' AND a.target_type='` + kind + `' AND a.target_id=` + target + `::text AND a.action IN (` + actions + `)` + extra + `)`
}
func (p *PostgresRepository) ListPersonalWork(ctx context.Context, tenant string, q catalog.PersonalWorkRequest) ([]catalog.PersonalWorkItem, error) {
	a, _ := identity.FromContext(ctx)
	return withTenantCollectionRead(ctx, p.pool, tenant, func(tx pgx.Tx) ([]catalog.PersonalWorkItem, error) {
		query := `WITH work AS (
 SELECT 'resource:'||r.id::text id,'resource' type,r.id resource_id,NULL::uuid version_id,NULL::uuid version_revision_id,NULL::uuid listing_id,NULL::uuid listing_revision_id,NULL::uuid submission_id,NULL::uuid current_candidate_revision_id,'empty_resource' state
 FROM resources r WHERE r.tenant_id=$1 AND $4='drafts' AND NOT EXISTS(SELECT 1 FROM resource_versions v WHERE v.tenant_id=$1 AND v.resource_id=r.id) AND ` + workOwnerSQL("resource", "r.id", "", "'resource.created'") + `
 UNION ALL
 SELECT 'version:'||v.id::text,'version',v.resource_id,v.id,v.candidate_revision_id,NULL,NULL,NULL,NULL,'unsealed_version'
 FROM resource_versions v WHERE v.tenant_id=$1 AND $4='drafts' AND v.candidate_revision_id IS NOT NULL AND v.sealed_revision_id IS NULL AND ` + workOwnerSQL("resource_version", "v.id", "v.candidate_revision_id", "'resource_version.created','resource_version.revised'") + `
 UNION ALL
 SELECT 'candidate:'||l.id::text,'candidate',l.resource_id,NULL,lr.version_revision_id,l.id,l.candidate_revision_id,NULL,l.candidate_revision_id,'unsubmitted_candidate'
 FROM catalog_listings l JOIN listing_revisions lr ON lr.tenant_id=l.tenant_id AND lr.id=l.candidate_revision_id
 WHERE l.tenant_id=$1 AND $4='drafts' AND NOT EXISTS(SELECT 1 FROM review_submissions s WHERE s.tenant_id=$1 AND s.listing_id=l.id AND s.listing_revision_id=l.candidate_revision_id) AND ` + workOwnerSQL("catalog_listing", "l.id", "l.candidate_revision_id", "'catalog_listing.revised'") + `
 UNION ALL
 SELECT 'submission:'||s.id::text,'submission',l.resource_id,NULL,s.version_revision_id,s.listing_id,s.listing_revision_id,s.id,l.candidate_revision_id,s.state
 FROM review_submissions s JOIN catalog_listings l ON l.tenant_id=s.tenant_id AND l.id=s.listing_id
 WHERE s.tenant_id=$1 AND s.state=$4 AND ` + workOwnerSQL("review_submission", "s.id", "", "'review_submission.submitted'") + `
 AND ($4<>'approved' OR ` + unreleasedSubmissionSQL("s") + `)
 AND ($4<>'changes_required' OR NOT EXISTS(SELECT 1 FROM review_submissions later WHERE later.tenant_id=$1 AND later.listing_id=s.listing_id AND (later.submitted_at,later.id)>(s.submitted_at,s.id)))
 )
 SELECT w.id,w.type,r.id,r.namespace_id,r.kind,
 CASE WHEN ` + collectionPermissionSQL("resource.read", "r.namespace_id") + ` THEN r.canonical_identifier ELSE '' END,
 CASE WHEN ` + collectionPermissionSQL("resource.read_content", "r.namespace_id") + ` THEN coalesce(lr.display_name,'') ELSE '' END,
 coalesce(w.version_id::text,''),coalesce(w.version_revision_id::text,''),coalesce(w.listing_id::text,''),coalesce(w.listing_revision_id::text,''),coalesce(w.submission_id::text,''),w.state,coalesce(w.current_candidate_revision_id::text,'')
 FROM work w JOIN resources r ON r.tenant_id=$1 AND r.id=w.resource_id AND r.deleted_at IS NULL
 LEFT JOIN listing_revisions lr ON lr.tenant_id=$1 AND lr.id=w.listing_revision_id AND lr.listing_id=w.listing_id
 WHERE w.id COLLATE "C">$5 AND ((w.type='resource' AND ` + collectionPermissionSQL("resource.read", "r.namespace_id") + `) OR (w.type<>'resource' AND ` + collectionPermissionSQL("resource.read_history", "r.namespace_id") + `))
 ORDER BY w.id COLLATE "C" LIMIT $6`
		rows, err := tx.Query(ctx, collectionStatement(ctx, query), tenant, a.MembershipID, a.RealmID, q.Queue, q.AfterID, q.Limit)
		if err != nil {
			return nil, translateError("list personal work", err)
		}
		defer rows.Close()
		items := []catalog.PersonalWorkItem{}
		for rows.Next() {
			var item catalog.PersonalWorkItem
			if err := rows.Scan(&item.ID, &item.Type, &item.ResourceID, &item.NamespaceID, &item.Kind, &item.CanonicalIdentifier, &item.DisplayName, &item.VersionID, &item.VersionRevisionID, &item.ListingID, &item.ListingRevisionID, &item.SubmissionID, &item.State, &item.CurrentCandidateRevisionID); err != nil {
				return nil, translateError("read personal work", err)
			}
			items = append(items, item)
		}
		return items, translateError("iterate personal work", rows.Err())
	})
}
