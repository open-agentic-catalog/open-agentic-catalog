// Scoped development provisioning joins the existing owner transaction and audit.
package database

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// provisionNamespaceAssignments admits only existing same-tenant namespaces.
// It restores configured grants explicitly, retaining revoked assignment history;
// any failure must roll back the caller's entire principal transaction.
func provisionNamespaceAssignments(ctx context.Context, tx pgx.Tx, input IdentityProvisioningInput, member string) ([]identity.RoleAssignment, error) {
	namespaces := make([]string, 0, len(input.Namespaces))
	for namespace := range input.Namespaces {
		namespaces = append(namespaces, namespace)
	}
	slices.Sort(namespaces)
	var result []identity.RoleAssignment
	for _, namespace := range namespaces {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM namespaces WHERE tenant_id=$1 AND id=$2)`, input.TenantID, namespace).Scan(&exists); err != nil {
			return nil, fmt.Errorf("verify development namespace: %w", err)
		}
		if !exists {
			return nil, errors.New("development namespace does not exist in the selected tenant")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO namespace_memberships(id,tenant_id,membership_id,namespace_id,state,revision,created_by,created_at)
VALUES (uuidv7(),$1,$2,$3,'active',1,$4,clock_timestamp())
ON CONFLICT (tenant_id,membership_id,namespace_id) DO UPDATE
SET state='active',revision=namespace_memberships.revision+1,created_by=excluded.created_by,
 created_at=GREATEST(namespace_memberships.created_at,namespace_memberships.revoked_at,excluded.created_at),
 revoked_by=NULL,revoke_reason='',revoked_at=NULL WHERE namespace_memberships.state='revoked'`, input.TenantID, member, namespace, input.Operator); err != nil {
			return nil, fmt.Errorf("provision namespace membership: %w", err)
		}
		roles := slices.Clone(input.Namespaces[namespace])
		slices.Sort(roles)
		for _, role := range slices.Compact(roles) {
			a := identity.RoleAssignment{Subject: input.Subject, ActorType: input.ActorType, NamespaceID: namespace}
			if err := tx.QueryRow(ctx, `INSERT INTO role_assignments(id,tenant_id,membership_id,namespace_id,role,state,revision,granted_by,grant_reason,granted_at)
VALUES (uuidv7(),$1,$2,$3,$4,'active',1,$5,$6,clock_timestamp())
ON CONFLICT (tenant_id,membership_id,(coalesce(namespace_id,'00000000-0000-0000-0000-000000000000'::uuid)),role) WHERE state='active'
DO UPDATE SET granted_at=role_assignments.granted_at
RETURNING id::text,tenant_id::text,membership_id::text,role,state,revision,granted_by,grant_reason,granted_at`, input.TenantID, member, namespace, role, input.Operator, input.Reason).Scan(&a.ID, &a.TenantID, &a.MembershipID, &a.Role, &a.State, &a.Revision, &a.GrantedBy, &a.GrantReason, &a.GrantedAt); err != nil {
				return nil, fmt.Errorf("provision namespace role: %w", err)
			}
			result = append(result, a)
		}
	}
	return result, nil
}
