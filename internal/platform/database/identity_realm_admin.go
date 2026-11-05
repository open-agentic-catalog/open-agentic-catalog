// Operator realm administration applies revision-checked access transitions with
// tenant suspension and publication-disclosure consequences.

package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type IdentityRealmPage struct {
	Items     []identity.Realm `json:"items"`
	NextAfter string           `json:"nextAfter,omitempty"`
	Total     int              `json:"total"`
}

type IdentityRealmTransitionInput struct {
	TenantID                string
	RealmID                 string
	ExpectedRevision        int64
	TargetState             identity.RealmState
	Operator                string
	Reason                  string
	TenantDiscoveryDisabled bool
	AcknowledgeOutbox       bool
}

type IdentityRealmTransitionResult struct {
	Realm                 identity.Realm `json:"realm"`
	TenantAccessSuspended bool           `json:"tenantAccessSuspended"`
}

// ListIdentityRealms performs an operator-authorized read-only tenant page with an
// exact total and UUID continuation, accepting at most 100 items.
func ListIdentityRealms(ctx context.Context, pool *pgxpool.Pool, tenantID, after string, limit int) (IdentityRealmPage, error) {
	if pool == nil || !identity.ValidTenantID(tenantID) || after != "" && !identity.ValidTenantID(after) || limit < 1 || limit > 100 {
		return IdentityRealmPage{}, errors.New("identity realm list requires a tenant UUID, optional after UUID, and limit from 1 through 100")
	}
	if err := verifyIdentityProvisioningRole(ctx, pool); err != nil {
		return IdentityRealmPage{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return IdentityRealmPage{}, fmt.Errorf("begin identity realm list: %w", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	if err := setTenantContext(ctx, tx, tenantID); err != nil {
		return IdentityRealmPage{}, err
	}
	page, err := listIdentityRealmsTx(ctx, tx, tenantID, after, limit)
	if err != nil {
		return IdentityRealmPage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IdentityRealmPage{}, fmt.Errorf("commit identity realm list: %w", err)
	}
	return page, nil
}

// listIdentityRealmsTx selects limit plus one rows to derive continuation without
// hydrating every tenant realm.
func listIdentityRealmsTx(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) (IdentityRealmPage, error) {
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tenant_identity_realms WHERE tenant_id=$1`, tenantID).Scan(&total); err != nil {
		return IdentityRealmPage{}, fmt.Errorf("count identity realms: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text,tenant_id::text,issuer,tenancy_mode,external_organization_key,state,revision,created_at,updated_at
		  FROM tenant_identity_realms
		 WHERE tenant_id=$1 AND ($2='' OR id>nullif($2,'')::uuid)
		 ORDER BY id LIMIT $3`, tenantID, after, limit+1)
	if err != nil {
		return IdentityRealmPage{}, fmt.Errorf("list identity realms: %w", err)
	}
	defer rows.Close()
	items := make([]identity.Realm, 0)
	for rows.Next() {
		realm, err := scanIdentityRealm(rows)
		if err != nil {
			return IdentityRealmPage{}, err
		}
		items = append(items, realm)
	}
	if err := rows.Err(); err != nil {
		return IdentityRealmPage{}, fmt.Errorf("iterate identity realms: %w", err)
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	return IdentityRealmPage{Items: items, NextAfter: next, Total: total}, nil
}

// TransitionIdentityRealm is an operator-only change that serializes tenant
// identity administration and verifies discovery is quiescent before the final
// active realm is suspended.
func TransitionIdentityRealm(ctx context.Context, pool *pgxpool.Pool, input IdentityRealmTransitionInput) (IdentityRealmTransitionResult, error) {
	input.Operator = strings.TrimSpace(input.Operator)
	if pool == nil || !identity.ValidTenantID(input.TenantID) || !identity.ValidTenantID(input.RealmID) || input.ExpectedRevision <= 0 || input.Operator == "" || len(input.Operator) > 512 || input.TargetState != identity.RealmActive && input.TargetState != identity.RealmSuspended {
		return IdentityRealmTransitionResult{}, errors.New("identity realm transition requires tenant and realm UUIDs, current revision, target state, operator, and bounded reason")
	}
	action := "identity.realm.reactivated"
	if input.TargetState == identity.RealmSuspended {
		action = "identity.realm.suspended"
	}
	var err error
	input.Reason, err = audit.NormalizeReason(action, input.Reason)
	if err != nil {
		return IdentityRealmTransitionResult{}, fmt.Errorf("identity realm transition reason is invalid: %w", err)
	}
	if err := verifyIdentityProvisioningRole(ctx, pool); err != nil {
		return IdentityRealmTransitionResult{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IdentityRealmTransitionResult{}, fmt.Errorf("begin identity realm transition: %w", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	if err := setTenantContext(ctx, tx, input.TenantID); err != nil {
		return IdentityRealmTransitionResult{}, err
	}
	result, err := transitionIdentityRealmTx(ctx, tx, input)
	if err != nil {
		return IdentityRealmTransitionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IdentityRealmTransitionResult{}, fmt.Errorf("commit identity realm transition: %w", err)
	}
	return result, nil
}

// transitionIdentityRealmTx serializes tenant administration, checks the realm
// revision and requires discovery to be disabled and unpublished before suspending
// the final active realm. State and evidence commit in the caller's transaction.
func transitionIdentityRealmTx(ctx context.Context, tx pgx.Tx, input IdentityRealmTransitionInput) (IdentityRealmTransitionResult, error) {
	if err := lockAuthorizationWriter(ctx, tx, input.TenantID); err != nil {
		return IdentityRealmTransitionResult{}, err
	}
	if err := acquireCatalogStateLock(ctx, tx, "tenant", input.TenantID); err != nil {
		return IdentityRealmTransitionResult{}, fmt.Errorf("lock tenant identity administration: %w", err)
	}
	var lockedTenantID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1`, input.TenantID).Scan(&lockedTenantID); err != nil {
		return IdentityRealmTransitionResult{}, fmt.Errorf("lock tenant identity administration: %w", err)
	}
	realm, err := scanIdentityRealm(tx.QueryRow(ctx, `
		SELECT id::text,tenant_id::text,issuer,tenancy_mode,external_organization_key,state,revision,created_at,updated_at
		  FROM tenant_identity_realms WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, input.TenantID, input.RealmID))
	if err != nil {
		return IdentityRealmTransitionResult{}, err
	}
	if realm.Revision != input.ExpectedRevision {
		return IdentityRealmTransitionResult{}, errors.New("identity realm revision does not match")
	}
	if realm.State == input.TargetState {
		return IdentityRealmTransitionResult{}, fmt.Errorf("identity realm is already %s", input.TargetState)
	}
	result := IdentityRealmTransitionResult{}
	if input.TargetState == identity.RealmSuspended {
		var otherActive int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tenant_identity_realms WHERE tenant_id=$1 AND id<>$2 AND state='active'`, input.TenantID, input.RealmID).Scan(&otherActive); err != nil {
			return IdentityRealmTransitionResult{}, fmt.Errorf("count active identity realms: %w", err)
		}
		if otherActive == 0 {
			if !input.TenantDiscoveryDisabled {
				return IdentityRealmTransitionResult{}, errors.New("suspending the final active identity realm requires every configured discovery host for the tenant to be disabled")
			}
			var activeSnapshots int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM publication_snapshots WHERE tenant_id=$1 AND state='active'`, input.TenantID).Scan(&activeSnapshots); err != nil {
				return IdentityRealmTransitionResult{}, fmt.Errorf("count active publication snapshots: %w", err)
			}
			if activeSnapshots != 0 {
				return IdentityRealmTransitionResult{}, errors.New("suspending the final active identity realm requires active discovery snapshots to be unpublished")
			}
			result.TenantAccessSuspended = true
		}
	}
	realm.State = input.TargetState
	realm.Revision++
	if err := tx.QueryRow(ctx, `UPDATE tenant_identity_realms SET state=$3,revision=$4,updated_at=greatest(clock_timestamp(),updated_at) WHERE tenant_id=$1 AND id=$2 RETURNING updated_at`, input.TenantID, input.RealmID, realm.State, realm.Revision).Scan(&realm.UpdatedAt); err != nil {
		return IdentityRealmTransitionResult{}, fmt.Errorf("update identity realm: %w", err)
	}
	action := "identity.realm.reactivated"
	if input.TargetState == identity.RealmSuspended {
		action = "identity.realm.suspended"
	}
	auditEvent, err := newDatabaseAuditRecord(ctx, tx, input.TenantID, action, "tenant_identity_realm", input.RealmID, input.Reason, input.Operator, map[string]any{"state": input.TargetState, "tenantAccessSuspended": result.TenantAccessSuspended}, realm.UpdatedAt)
	if err != nil {
		return IdentityRealmTransitionResult{}, err
	}
	outbox, err := newDatabasePublicOutbox(ctx, tx, auditEvent, input.AcknowledgeOutbox)
	if err != nil {
		return IdentityRealmTransitionResult{}, err
	}
	if err := insertEvidence(ctx, tx, auditEvent, outbox, expectEvidence(input.TenantID, "tenant_identity_realm", input.RealmID, action)); err != nil {
		return IdentityRealmTransitionResult{}, fmt.Errorf("record identity realm evidence: %w", err)
	}
	result.Realm = realm
	return result, nil
}

// scanIdentityRealm decodes the operator realm projection, retaining database read
// errors for the caller's diagnostic context.
func scanIdentityRealm(row scanner) (identity.Realm, error) {
	var realm identity.Realm
	if err := row.Scan(&realm.ID, &realm.TenantID, &realm.Issuer, &realm.TenancyMode, &realm.ExternalOrganization, &realm.State, &realm.Revision, &realm.CreatedAt, &realm.UpdatedAt); err != nil {
		return identity.Realm{}, fmt.Errorf("read identity realm: %w", err)
	}
	return realm, nil
}
