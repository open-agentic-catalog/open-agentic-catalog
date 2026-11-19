// Validation maintenance selects current, authorized subjects before hydration.
package database

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// maintenanceConditionsSQL mirrors the owning validator's finite metadata
// predicates. Missing/unsupported evidence is selected for fail-closed hydration.
const maintenanceConditionsSQL = `
 e.id IS NULL OR validation.evidence_set_id IS NULL OR e.subject_digest<>revision.content_set_digest OR
 CASE $3
 WHEN 'attention' THEN e.state='blocked' OR e.validation_profile<>$6 OR e.policy_version<>$7
   OR EXISTS(SELECT 1 FROM jsonb_array_elements(e.external_retrievals) retrieval WHERE retrieval->>'policyDigest' IS DISTINCT FROM $8)
   OR validation.expires_at <= $4::timestamptz + $5::integer * interval '1 hour'
 WHEN 'blocked' THEN e.state='blocked'
 WHEN 'expired' THEN validation.expires_at <= $4::timestamptz
 WHEN 'expiring' THEN validation.expires_at > $4::timestamptz AND validation.expires_at <= $4::timestamptz + $5::integer * interval '1 hour'
 WHEN 'stale' THEN e.validation_profile<>$6 OR e.policy_version<>$7
   OR EXISTS(SELECT 1 FROM jsonb_array_elements(e.external_retrievals) retrieval WHERE retrieval->>'policyDigest' IS DISTINCT FROM $8)
 ELSE false END`

// ListValidationMaintenance uses a coherent repeatable-read policy snapshot.
// Its bounded second phase reads evidence and delivery bindings, never native
// documents, artifact bytes or package inventories, and makes no remote calls.
func (p *PostgresRepository) ListValidationMaintenance(ctx context.Context, tenant string, q catalog.ValidationMaintenanceRequest) ([]catalog.ValidationMaintenanceItem, error) {
	if q.Limit < 1 || q.Limit > 101 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "validation maintenance page is invalid")
	}
	return withTenantCollectionRead(ctx, p.pool, tenant, func(tx pgx.Tx) ([]catalog.ValidationMaintenanceItem, error) {
		query := `SELECT revision.id,r.id,r.namespace_id,r.kind,r.canonical_identifier,version.id,version.version,e.id IS NULL OR validation.evidence_set_id IS NULL
 FROM resource_version_revisions revision
 JOIN resource_versions version ON version.tenant_id=revision.tenant_id AND version.id=revision.resource_version_id
 JOIN resources r ON r.tenant_id=version.tenant_id AND r.id=version.resource_id AND r.deleted_at IS NULL
 LEFT JOIN LATERAL(SELECT run.evidence_set_id,run.expires_at FROM version_validation_runs run WHERE run.tenant_id=revision.tenant_id AND run.version_revision_id=revision.id ORDER BY run.run_number DESC LIMIT 1) validation ON true
 LEFT JOIN evidence_sets e ON e.tenant_id=revision.tenant_id AND e.id=validation.evidence_set_id
 WHERE revision.tenant_id=$1 AND ($2::uuid IS NULL OR revision.id>$2)
 AND (` + collectionPermissionSQL("resource.read", "r.namespace_id") + ` AND ` + collectionPermissionSQL("resource.read_content", "r.namespace_id") + ` AND ` + collectionPermissionSQL("resource.validate", "r.namespace_id") + `)
 AND (revision.id=version.candidate_revision_id OR revision.id=version.sealed_revision_id
 OR EXISTS(SELECT 1 FROM catalog_listings l JOIN listing_revisions lr ON lr.tenant_id=l.tenant_id AND lr.id=l.candidate_revision_id WHERE l.tenant_id=$1 AND l.resource_id=r.id AND lr.version_revision_id=revision.id)
 OR EXISTS(SELECT 1 FROM catalog_listings l JOIN listing_releases release ON release.tenant_id=l.tenant_id AND release.id=l.current_release_id AND release.state IN ('published','deprecated') WHERE l.tenant_id=$1 AND l.resource_id=r.id AND release.version_revision_id=revision.id))
 AND (` + maintenanceConditionsSQL + `) ORDER BY revision.id LIMIT $9`
		rows, err := tx.Query(ctx, collectionStatement(ctx, query), tenant, nullableUUID(q.AfterID), q.State, q.ObservedAt.UTC().Truncate(time.Microsecond), q.HorizonHours, q.Policy.Profile, q.Policy.Version, q.Policy.ExternalDigest, q.Limit)
		if err != nil {
			return nil, translateError("select validation maintenance", err)
		}
		defer rows.Close()
		type selected struct {
			id      string
			r       catalog.Resource
			v       catalog.ResourceVersion
			missing bool
		}
		selectedRows := []selected{}
		for rows.Next() {
			var row selected
			if err := rows.Scan(&row.id, &row.r.ID, &row.r.NamespaceID, &row.r.Kind, &row.r.CanonicalIdentifier, &row.v.ID, &row.v.Version, &row.missing); err != nil {
				return nil, translateError("scan validation maintenance", err)
			}
			row.r.TenantID = tenant
			selectedRows = append(selectedRows, row)
		}
		if err := rows.Err(); err != nil {
			return nil, translateError("iterate validation maintenance", err)
		}
		rows.Close()
		items := []catalog.ValidationMaintenanceItem{}
		for _, row := range selectedRows {
			if row.missing {
				return nil, catalog.E(catalog.CodeDependencyUnavailable, "current validation evidence is missing")
			}
			revision, evidence, err := scanVersionRevision(tx.QueryRow(ctx, versionRevisionReadSQL, tenant, []string{row.id}, false))
			if err != nil {
				return nil, err
			}
			bindings, err := readMaintenanceBindings(ctx, tx, tenant, row.id)
			if err != nil {
				return nil, err
			}
			if err := catalog.ValidateExternalRetrievalBindings(bindings, evidence); err != nil {
				return nil, catalog.Wrap(catalog.CodeDependencyUnavailable, "stored validation bindings are invalid", err)
			}
			item, err := catalog.ProjectValidationMaintenance(row.r, row.v, revision, evidence, q)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	})
}

const validationMaintenanceBindingsSQL = catalogSummaryBindingsSQL + ` LIMIT 17`

// readMaintenanceBindings bounds transfer at the 16-representation ceiling plus
// one before refusing missing or oversized retained representation sets.
func readMaintenanceBindings(ctx context.Context, tx pgx.Tx, tenant, id string) ([]catalog.ExternalRepresentationBinding, error) {
	rows, err := tx.Query(ctx, validationMaintenanceBindingsSQL, tenant, []string{id})
	if err != nil {
		return nil, translateError("read validation bindings", err)
	}
	defer rows.Close()
	bindings := []catalog.ExternalRepresentationBinding{}
	for rows.Next() {
		var revisionID string
		var binding catalog.ExternalRepresentationBinding
		if err := rows.Scan(&revisionID, &binding.ID, &binding.URL, &binding.MediaType, &binding.ContentDigest, &binding.Size, &binding.Retained); err != nil {
			return nil, translateError("scan validation binding", err)
		}
		if revisionID != id || len(bindings) >= 16 {
			return nil, catalog.E(catalog.CodeDependencyUnavailable, "stored validation representations are invalid")
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, translateError("iterate validation bindings", err)
	}
	if len(bindings) == 0 {
		return nil, catalog.E(catalog.CodeDependencyUnavailable, "stored validation representations are missing")
	}
	return bindings, nil
}
