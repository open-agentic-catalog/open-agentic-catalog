package database

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/overview"
)

func (p *PostgresIdentityStore) ReadAdministrationOverview(ctx context.Context, actor identity.Actor) (identity.AdministrationOverviewRows, error) {
	return postgresAuthorizationRead(ctx, p, actor, nil, false, func(tx pgx.Tx) (identity.AdministrationOverviewRows, error) {
		out := identity.AdministrationOverviewRows{}
		for _, selection := range []struct {
			kind identity.ActorType
			rows *[]identity.MembershipState
		}{{identity.ActorHuman, &out.Human}, {identity.ActorService, &out.Service}} {
			rows, err := tx.Query(ctx, `SELECT state FROM tenant_memberships WHERE tenant_id=$1 AND actor_type=$2 LIMIT $3`, actor.TenantID, selection.kind, overview.SelectionLimit+1)
			if err != nil {
				return out, translateIdentityError("select membership summary", err)
			}
			values, err := collectAuthorizationRows(rows, func(row scanner) (identity.MembershipState, error) {
				var state identity.MembershipState
				err := row.Scan(&state)
				return state, translateIdentityError("read membership summary", err)
			})
			if err != nil {
				return out, err
			}
			*selection.rows = values
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM access_groups WHERE tenant_id=$1 AND realm_id=$2 AND state='active' LIMIT $3) bounded`, actor.TenantID, actor.RealmID, overview.SelectionLimit+1).Scan(&out.Groups); err != nil {
			return out, translateIdentityError("count active groups", err)
		}
		rows, err := tx.Query(ctx, `SELECT builtin FROM role_definitions WHERE tenant_id=$1 AND state='active' LIMIT $2`, actor.TenantID, overview.SelectionLimit+1)
		if err != nil {
			return out, translateIdentityError("select active role summary", err)
		}
		out.BuiltinRoles, err = collectAuthorizationRows(rows, func(row scanner) (bool, error) {
			var builtin bool
			err := row.Scan(&builtin)
			return builtin, translateIdentityError("read active role summary", err)
		})
		if err != nil {
			return out, err
		}
		err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM role_assignments WHERE tenant_id=$1 AND state='active' LIMIT $2) bounded`, actor.TenantID, overview.SelectionLimit+1).Scan(&out.Assignments)
		return out, translateIdentityError("count retained active assignments", err)
	})
}
