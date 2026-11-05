package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// This relation is shared by the finite capability union and the bounded
// namespace page. It never hydrates a principal's full assignment collection.
const selfEffectivePermissionsSQL = `WITH effective AS NOT MATERIALIZED (
 SELECT a.namespace_id,p.permission FROM role_assignments a
 JOIN role_definitions d ON d.tenant_id=a.tenant_id AND d.id=a.role_definition_id
 JOIN role_permissions p ON p.tenant_id=d.tenant_id AND p.role_definition_id=d.id
 WHERE a.tenant_id=$1 AND ` + effectiveAssignmentPredicate + `
) `

func (p *PostgresIdentityStore) ReadSelfAccess(ctx context.Context, actor identity.Actor, request identity.SelfAccessRequest) (identity.SelfAccessProjection, error) {
	if err := identity.ValidateSelfAccessRequest(request); err != nil {
		return identity.SelfAccessProjection{}, err
	}
	page := identity.AuthorizationPageRequest{Limit: request.Limit, AuthorizationRevision: request.AuthorizationRevision}
	return postgresAuthorizationRead(ctx, p, actor, &page, true, func(tx pgx.Tx) (identity.SelfAccessProjection, error) {
		result := identity.SelfAccessProjection{AuthorizationRevision: request.AuthorizationRevision, NamespaceGrants: []identity.NamespacePermissionAccess{}}
		if err := tx.QueryRow(ctx, selfEffectivePermissionsSQL+`SELECT
 ARRAY(SELECT DISTINCT permission COLLATE "C" FROM effective WHERE namespace_id IS NULL ORDER BY 1),
 ARRAY(SELECT DISTINCT permission COLLATE "C" FROM effective ORDER BY 1)`, actor.TenantID, actor.MembershipID, actor.RealmID).Scan(&result.Permissions, &result.AvailablePermissions); err != nil {
			return identity.SelfAccessProjection{}, translateIdentityError("read effective capability hints", err)
		}
		rows, err := tx.Query(ctx, selfEffectivePermissionsSQL+`, scopes AS (
 SELECT namespace_id FROM effective WHERE namespace_id IS NOT NULL
 AND ($4='' OR namespace_id=nullif($4,'')::uuid) AND ($5='' OR namespace_id>nullif($5,'')::uuid)
 GROUP BY namespace_id ORDER BY namespace_id LIMIT $6
) SELECT scopes.namespace_id::text,
 ARRAY(SELECT DISTINCT permission COLLATE "C" FROM effective WHERE namespace_id IS NULL OR namespace_id=scopes.namespace_id ORDER BY 1)
 FROM scopes ORDER BY scopes.namespace_id`, actor.TenantID, actor.MembershipID, actor.RealmID, request.NamespaceID, request.AfterNamespaceID, request.Limit)
		if err != nil {
			return identity.SelfAccessProjection{}, translateIdentityError("select self-access scopes", err)
		}
		defer rows.Close()
		for rows.Next() {
			var namespace identity.NamespacePermissionAccess
			if err := rows.Scan(&namespace.NamespaceID, &namespace.Permissions); err != nil {
				return identity.SelfAccessProjection{}, translateIdentityError("read self-access scope", err)
			}
			result.NamespaceGrants = append(result.NamespaceGrants, namespace)
		}
		if err := rows.Err(); err != nil {
			return identity.SelfAccessProjection{}, translateIdentityError("read self-access scopes", err)
		}
		return result, nil
	})
}

var _ identity.SelfAccessStore = (*PostgresIdentityStore)(nil)
