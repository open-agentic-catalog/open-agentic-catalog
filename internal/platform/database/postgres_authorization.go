// Current permissions are read from PostgreSQL, never from a hydrated Actor.
package database

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// CheckPermissions returns current decisions and their coherent authorization revision.
// Standalone/preparatory reads acquire a repeatable-read snapshot. Reads inside
// an admitted command join its fenced transaction; no local authority is cached.
func (p *PostgresIdentityStore) CheckPermissions(ctx context.Context, actor identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	if err := identity.ValidatePermissionChecks(checks); err != nil {
		return identity.PermissionDecision{}, err
	}
	if err := identity.ValidatePermissionActor(actor); err != nil {
		return identity.PermissionDecision{}, err
	}
	if command := commandTransactionFrom(ctx); command != nil {
		if err := command.validate(p.repository.pool, actor.TenantID); err != nil {
			return identity.PermissionDecision{}, err
		}
		command.mu.Lock()
		defer command.mu.Unlock()
		if command.failed != nil {
			return identity.PermissionDecision{}, command.failed
		}
		if command.closed {
			return identity.PermissionDecision{}, identity.E(identity.CodeConflict, "command is already complete")
		}
		if command.tx != nil {
			if !command.authorizationShared && !command.authorizationExclusive {
				return identity.PermissionDecision{}, identity.E(identity.CodeConflict, "permission reads require transaction admission before domain writes")
			}
			return checkCurrentPermissions(ctx, command.tx, actor, checks)
		}
		var result identity.PermissionDecision
		err := command.runShort(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			var err error
			result, err = checkCurrentPermissions(ctx, tx, actor, checks)
			return err
		})
		if err != nil {
			return identity.PermissionDecision{}, err
		}
		return result, nil
	}
	return withStandaloneTenantTx(ctx, p.repository.pool, actor.TenantID, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (identity.PermissionDecision, error) {
		return checkCurrentPermissions(ctx, tx, actor, checks)
	})
}

func checkCurrentPermissions(ctx context.Context, tx pgx.Tx, actor identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	if err := identity.ValidatePermissionActor(actor); err != nil {
		return identity.PermissionDecision{}, err
	}
	var result identity.PermissionDecision
	keys := make([]string, len(checks))
	scopes := make([]string, len(checks))
	for i, c := range checks {
		keys[i] = string(c.Permission)
		scopes[i] = c.NamespaceID
	}
	// Use the same ordered decision array as collection admission. One tuple
	// binds the revision and all decisions without a second network round trip.
	query := `SELECT state.revision,ARRAY(` + currentPermissionChecksSQL + `) FROM tenant_authorization_state state WHERE state.tenant_id=$1`
	if err := tx.QueryRow(ctx, query, actor.TenantID, actor.MembershipID, actor.RealmID, actor.Subject, actor.ActorType, keys, scopes).Scan(&result.Revision, &result.Allowed); err != nil {
		return identity.PermissionDecision{}, identity.Wrap(identity.CodeDependency, "check current permissions", err)
	}
	if len(result.Allowed) != len(checks) {
		return identity.PermissionDecision{}, identity.E(identity.CodeDependency, "permission decision count is invalid")
	}
	return result, nil
}

// WithPermissionAdmission retains the shared tenant fence until the domain write
// and (when present) encoded command response commit. The callback must be local,
// bounded database work; outbound calls do not belong in this transaction.
func (p *PostgresIdentityStore) WithPermissionAdmission(ctx context.Context, actor identity.Actor, checks []identity.PermissionCheck, operation func(context.Context) error) (err error) {
	if err := identity.ValidatePermissionChecks(checks); err != nil {
		return err
	}
	if operation == nil {
		return identity.E(identity.CodeInvalidArgument, "admitted operation is required")
	}
	var bindErr error
	ctx, bindErr = identity.BindPermissionActor(ctx, actor)
	if bindErr != nil {
		return bindErr
	}
	command := commandTransactionFrom(ctx)
	owned := command == nil
	if owned {
		conn, e := p.repository.pool.Acquire(ctx)
		if e != nil {
			return identity.Wrap(identity.CodeDependency, "acquire authorization connection", e)
		}
		defer conn.Release()
		command = &commandTransaction{pool: p.repository.pool, conn: conn, tenantID: actor.TenantID}
		ctx = withCommandTransaction(ctx, command)
	}
	if err := command.validate(p.repository.pool, actor.TenantID); err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			command.abort(ctx, errors.New("admitted operation panicked"))
			panic(recovered)
		}
		if err != nil {
			command.abort(ctx, err)
		}
	}()
	err = command.run(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var revision int64
		// RC deliberately uses another statement after this possibly waiting lock.
		if err := tx.QueryRow(ctx, `SELECT revision FROM tenant_authorization_state WHERE tenant_id=$1 FOR SHARE`, actor.TenantID).Scan(&revision); err != nil {
			return identity.Wrap(identity.CodeDependency, "admit authorization transaction", err)
		}
		command.authorizationShared = true
		decision, err := checkCurrentPermissions(ctx, tx, actor, checks)
		if err != nil {
			return err
		}
		identity.SetPermissionRevision(ctx, decision.Revision)
		for _, allowed := range decision.Allowed {
			if !allowed {
				return identity.E(identity.CodeForbidden, "current permission denied the operation")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = operation(ctx); err != nil {
		return err
	}
	if owned {
		return command.complete(ctx, 200, func(context.Context, pgx.Tx) error { return nil })
	}
	return nil
}

// Writers take exclusive authority before any tenant advisory or object lock.
// The fresh check is a separate RC statement, so a waited revocation is visible.
func lockAuthorizationWriter(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if command := commandTransactionFrom(ctx); command != nil && command.authorizationShared {
		return identity.E(identity.CodeConflict, "authorization administration cannot upgrade shared admission")
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM tenant_authorization_state WHERE tenant_id=$1 FOR NO KEY UPDATE`, tenantID).Scan(&revision); err != nil {
		return identity.Wrap(identity.CodeDependency, "lock authorization administration", err)
	}
	if command := commandTransactionFrom(ctx); command != nil {
		command.authorizationExclusive = true
	}
	if actor, ok := identity.FromContext(ctx); ok {
		if actor.TenantID != tenantID {
			return identity.E(identity.CodeForbidden, "authorization administration crossed tenant boundary")
		}
		decision, err := checkCurrentPermissions(ctx, tx, actor, []identity.PermissionCheck{{Permission: "access.manage"}})
		if err != nil {
			return err
		}
		identity.SetPermissionRevision(ctx, decision.Revision)
		if !decision.Allowed[0] {
			return identity.E(identity.CodeForbidden, "current access administration permission is required")
		}
	}
	return nil
}

var _ identity.PermissionStore = (*PostgresIdentityStore)(nil)

// admitRecordedPermissions rechecks preparatory decisions in the transaction
// that owns the domain data. Ordinary writes acquire policy before domain locks;
// administration owns its separate exclusive path.
func admitRecordedPermissions(ctx context.Context, tx pgx.Tx, readOnly bool, command *commandTransaction) error {
	if lease, ok := identity.InternalLeaseFrom(ctx); ok {
		var current int
		query := `SELECT 1 FROM jobs WHERE tenant_id=$1 AND id=$2 AND lease_owner=$3 AND attempt=$4 AND kind=$5 AND state='running' AND lease_expires_at>clock_timestamp()`
		if !readOnly {
			query += " FOR SHARE"
		}
		if err := tx.QueryRow(ctx, query, lease.TenantID, lease.JobID, lease.Owner, lease.Attempt, lease.Kind).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return catalog.E(catalog.CodeForbidden, "claimed Job lease is no longer current")
			}
			return catalog.Wrap(catalog.CodeDependencyUnavailable, "admit claimed Job lease", err)
		}
	}
	checks := identity.RecordedPermissionChecks(ctx)
	if len(checks) == 0 {
		return nil
	}
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return identity.E(identity.CodeForbidden, "current Catalog principal is required")
	}
	if !readOnly && !identity.IsAdministration(ctx) && (command == nil || !command.authorizationShared && !command.authorizationExclusive) {
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM tenant_authorization_state WHERE tenant_id=$1 FOR SHARE`, actor.TenantID).Scan(&revision); err != nil {
			return identity.Wrap(identity.CodeDependency, "admit domain permissions", err)
		}
		if command != nil {
			command.authorizationShared = true
		}
	}
	decision, err := checkCurrentPermissions(ctx, tx, actor, checks)
	if err != nil {
		return err
	}
	identity.SetPermissionRevision(ctx, decision.Revision)
	for _, allowed := range decision.Allowed {
		if !allowed {
			return catalog.E(catalog.CodeForbidden, "current permission denied the operation")
		}
	}
	return nil
}

// currentPermissionChecksSQL is shared by exact decisions and collection snapshots.
const currentPermissionChecksSQL = `SELECT EXISTS (
 SELECT 1 FROM tenant_memberships m JOIN tenant_identity_realms r ON r.tenant_id=m.tenant_id AND r.id=m.realm_id AND r.state='active'
 JOIN role_assignments a ON a.tenant_id=m.tenant_id AND a.state='active' AND (a.membership_id=m.id OR EXISTS(
 SELECT 1 FROM access_group_members gm JOIN access_groups g ON g.tenant_id=gm.tenant_id AND g.id=gm.group_id AND g.realm_id=gm.realm_id
 WHERE gm.tenant_id=a.tenant_id AND gm.group_id=a.group_id AND gm.membership_id=m.id AND gm.realm_id=m.realm_id AND gm.state='active' AND g.state='active'))
 JOIN role_definitions d ON d.tenant_id=a.tenant_id AND d.id=a.role_definition_id AND d.state='active'
 JOIN role_permissions p ON p.tenant_id=d.tenant_id AND p.role_definition_id=d.id AND p.permission=c.permission
 WHERE m.tenant_id=$1 AND m.id=$2 AND m.realm_id=$3 AND m.subject=$4 AND m.actor_type=$5 AND m.state='active'
 AND (a.namespace_id IS NULL OR (c.namespace<>'' AND a.namespace_id=nullif(c.namespace,'')::uuid)))
 FROM unnest($6::text[],$7::text[]) WITH ORDINALITY AS c(permission,namespace,position) ORDER BY c.position`
