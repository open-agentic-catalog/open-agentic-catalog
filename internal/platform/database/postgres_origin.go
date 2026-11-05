package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// GetOriginatingPrincipal resolves only the immutable attribution needed by a
// creator or independent-review check, not a general audit read.
func (p *PostgresRepository) GetOriginatingPrincipal(ctx context.Context, tenant, kind, target string) (catalog.OriginatingPrincipal, error) {
	return withTenantRead(ctx, p.pool, tenant, func(tx pgx.Tx) (catalog.OriginatingPrincipal, error) {
		return readOriginatingPrincipal(ctx, tx, tenant, kind, target)
	})
}

func readOriginatingPrincipal(ctx context.Context, tx pgx.Tx, tenant, kind, target string) (catalog.OriginatingPrincipal, error) {
	actions := catalog.OriginatingActions(kind)
	if len(actions) == 0 {
		return catalog.OriginatingPrincipal{}, catalog.E(catalog.CodeForbidden, "originating principal cannot be established")
	}
	rows, err := tx.Query(ctx, `SELECT event.actor,coalesce(member.realm_id::text,''),coalesce(member.subject,'')
FROM audit_events event LEFT JOIN tenant_memberships member
 ON member.tenant_id=event.tenant_id AND member.id=CASE WHEN pg_input_is_valid(event.actor,'uuid') THEN event.actor::uuid END
 AND member.realm_id::text=event.details->>'realmId'
 AND member.actor_type=event.details->>'actorType'
 AND event.format_version=1 AND event.details->>'principalType'='catalog_membership'
WHERE event.tenant_id=$1 AND event.target_type=$2 AND event.target_id=$3 AND event.action=ANY($4::text[])
 AND event.action IN ('resource.created','observation.adopted','review_submission.submitted') LIMIT 2`, tenant, kind, target, actions)
	if err != nil {
		return catalog.OriginatingPrincipal{}, translateError("read originating principal", err)
	}
	defer rows.Close()
	var origin catalog.OriginatingPrincipal
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&origin.MembershipID, &origin.RealmID, &origin.Subject); err != nil {
			return catalog.OriginatingPrincipal{}, translateError("read originating principal", err)
		}
	}
	if err := rows.Err(); err != nil {
		return catalog.OriginatingPrincipal{}, translateError("read originating principal", err)
	}
	if count != 1 || !identity.ValidTenantID(origin.MembershipID) || !identity.ValidTenantID(origin.RealmID) || origin.Subject == "" {
		return catalog.OriginatingPrincipal{}, catalog.E(catalog.CodeForbidden, "originating principal cannot be established")
	}
	origin.TenantID = tenant
	return origin, nil
}
