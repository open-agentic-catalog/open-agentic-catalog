package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// ListMemberContributions selects limit+1 exact assignment references before
// hydrating their bounded metadata in the same policy-bound read transaction.
func (p *PostgresIdentityStore) ListMemberContributions(ctx context.Context, actor identity.Actor, request identity.AuthorizationPageRequest) (identity.MemberContributionSelection, error) {
	return postgresAuthorizationRead(ctx, p, actor, &request, false, func(tx pgx.Tx) (identity.MemberContributionSelection, error) {
		member, err := scanMembership(tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,realm_id::text,subject,actor_type,state,revision,created_at,updated_at FROM tenant_memberships WHERE tenant_id=$1 AND id=$2`, actor.TenantID, request.MembershipID))
		if err != nil {
			return identity.MemberContributionSelection{}, err
		}
		var realmActive bool
		if err := tx.QueryRow(ctx, `SELECT state='active' FROM tenant_identity_realms WHERE tenant_id=$1 AND id=$2`, actor.TenantID, member.RealmID).Scan(&realmActive); err != nil {
			return identity.MemberContributionSelection{}, translateIdentityError("read member realm eligibility", err)
		}
		rows, err := tx.Query(ctx, `SELECT a.id::text,coalesce(gm.id::text,'') FROM role_assignments a
LEFT JOIN LATERAL (SELECT rel.id FROM access_group_members rel JOIN access_groups g ON g.tenant_id=rel.tenant_id AND g.id=rel.group_id AND g.realm_id=rel.realm_id
 WHERE rel.tenant_id=a.tenant_id AND rel.group_id=a.group_id AND rel.membership_id=$2 AND rel.realm_id=$3 AND $4=$3
 ORDER BY (rel.state='active') DESC,rel.added_at DESC,rel.id DESC LIMIT 1) gm ON true
WHERE a.tenant_id=$1 AND ($5='' OR a.id>nullif($5,'')::uuid) AND (a.membership_id=$2 OR gm.id IS NOT NULL)
ORDER BY a.id LIMIT $6`, actor.TenantID, member.ID, actor.RealmID, member.RealmID, request.AfterID, request.Limit)
		if err != nil {
			return identity.MemberContributionSelection{}, translateIdentityError("select member contribution page", err)
		}
		type reference struct{ assignment, relation string }
		refs, err := collectAuthorizationRows(rows, func(row scanner) (reference, error) {
			var ref reference
			err := row.Scan(&ref.assignment, &ref.relation)
			return ref, translateIdentityError("read contribution reference", err)
		})
		if err != nil {
			return identity.MemberContributionSelection{}, err
		}
		items := make([]identity.MemberContribution, 0, len(refs))
		for _, ref := range refs {
			item, err := loadMemberContribution(ctx, tx, actor, ref.assignment, ref.relation)
			if err != nil {
				return identity.MemberContributionSelection{}, err
			}
			item.Effective = member.State == identity.MembershipActive && realmActive && item.Assignment.State == identity.AssignmentActive && item.Role.State == identity.DefinitionActive && (item.Group == nil || item.Group.State == identity.GroupActive && item.Relation.State == identity.AssignmentActive)
			items = append(items, item)
		}
		return identity.MemberContributionSelection{Membership: member, Items: items}, nil
	})
}

func loadMemberContribution(ctx context.Context, tx pgx.Tx, actor identity.Actor, assignmentID, relationID string) (identity.MemberContribution, error) {
	item := identity.MemberContribution{}
	var err error
	item.Assignment, err = scanAccessAssignment(tx.QueryRow(ctx, `SELECT `+accessAssignmentProjection+` FROM role_assignments a WHERE a.tenant_id=$1 AND a.id=$2`, actor.TenantID, assignmentID))
	if err != nil {
		return item, err
	}
	item.Role, err = loadDefinition(ctx, tx, actor.TenantID, item.Assignment.RoleDefinitionID)
	if err != nil {
		return item, err
	}
	if relationID != "" {
		group, err := loadAccessGroup(ctx, tx, actor, item.Assignment.GroupID)
		if err != nil {
			return item, err
		}
		relation, err := scanGroupMember(tx.QueryRow(ctx, `SELECT `+groupMemberProjection+` FROM access_group_members WHERE tenant_id=$1 AND realm_id=$2 AND id=$3`, actor.TenantID, actor.RealmID, relationID))
		if err != nil {
			return item, err
		}
		item.Group, item.Relation = &group, &relation
	}
	return item, nil
}
