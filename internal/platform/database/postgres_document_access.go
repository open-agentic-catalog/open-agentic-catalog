package database

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func (p *PostgresRepository) DocumentAccessNamespace(ctx context.Context, tenant, document string) (string, error) {
	a, ok := identity.FromContext(ctx)
	if !ok || a.TenantID != tenant {
		return "", catalog.E(catalog.CodeForbidden, "current Catalog principal is required")
	}
	return withTenantRead(ctx, p.pool, tenant, func(tx pgx.Tx) (string, error) {
		var namespace string
		err := tx.QueryRow(ctx, `SELECT resource.namespace_id::text
 FROM representations rep
 JOIN resource_version_revisions revision ON revision.tenant_id=rep.tenant_id AND revision.id=rep.version_revision_id
 JOIN resource_versions version ON version.tenant_id=revision.tenant_id AND version.id=revision.resource_version_id
 JOIN resources resource ON resource.tenant_id=version.tenant_id AND resource.id=version.resource_id AND resource.deleted_at IS NULL
 WHERE rep.tenant_id=$1 AND rep.document_blob_id=$6 AND EXISTS (
 SELECT 1 FROM tenant_memberships m
 JOIN tenant_identity_realms realm ON realm.tenant_id=m.tenant_id AND realm.id=m.realm_id AND realm.state='active'
 JOIN role_assignments assignment ON assignment.tenant_id=m.tenant_id AND assignment.state='active' AND (
 assignment.membership_id=m.id OR EXISTS(SELECT 1 FROM access_group_members gm JOIN access_groups g ON g.tenant_id=gm.tenant_id AND g.id=gm.group_id AND g.realm_id=gm.realm_id
 WHERE gm.tenant_id=assignment.tenant_id AND gm.group_id=assignment.group_id AND gm.membership_id=m.id AND gm.realm_id=m.realm_id AND gm.state='active' AND g.state='active'))
 JOIN role_definitions d ON d.tenant_id=assignment.tenant_id AND d.id=assignment.role_definition_id AND d.state='active'
 JOIN role_permissions permission ON permission.tenant_id=d.tenant_id AND permission.role_definition_id=d.id AND permission.permission='resource.read_content'
 WHERE m.tenant_id=$1 AND m.id=$2 AND m.realm_id=$3 AND m.subject=$4 AND m.actor_type=$5 AND m.state='active'
 AND (assignment.namespace_id IS NULL OR assignment.namespace_id=resource.namespace_id))
 ORDER BY resource.namespace_id LIMIT 1`, tenant, a.MembershipID, a.RealmID, a.Subject, a.ActorType, document).Scan(&namespace)
		if err != nil {
			return "", translateError("read authorized document reference", err)
		}
		return namespace, nil
	})
}

var _ catalog.DocumentAccessReader = (*PostgresRepository)(nil)
