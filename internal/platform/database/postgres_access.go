// Identity persistence resolves verified principals through current active realms
// and tenant-scoped memberships and assignments.

package database

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// PostgresIdentityStore resolves verified principals and persists tenant-owned
// memberships and grants under the Catalog's RLS boundary.
type PostgresIdentityStore struct {
	repository *PostgresRepository
}

// NewPostgresIdentityStore binds identity persistence to an existing repository; a
// nil repository is rejected and pool ownership remains with the caller.
func NewPostgresIdentityStore(repository *PostgresRepository) (*PostgresIdentityStore, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL repository is required")
	}
	return &PostgresIdentityStore{repository: repository}, nil
}

// Resolve selects exactly one active issuer/tenant mapping, then derives roles
// only from active memberships, realms, and assignments in PostgreSQL.
func (p *PostgresIdentityStore) Resolve(ctx context.Context, principal identity.Principal) (identity.Actor, error) {
	return p.resolvePrincipal(ctx, principal, true)
}

// ResolvePrincipal resolves current membership without materializing role assignments.
func (p *PostgresIdentityStore) ResolvePrincipal(ctx context.Context, principal identity.Principal) (identity.Actor, error) {
	return p.resolvePrincipal(ctx, principal, false)
}
func (p *PostgresIdentityStore) resolvePrincipal(ctx context.Context, principal identity.Principal, hydrate bool) (identity.Actor, error) {
	if principal.Issuer == "" || !identity.ValidSubject(principal.Subject) || (principal.TenantID == "") == (principal.ExternalOrganization == "") || principal.TenantID != "" && !identity.ValidTenantID(principal.TenantID) {
		return identity.Actor{}, identity.E(identity.CodeUnauthenticated, "verified identity is incomplete")
	}
	tx, err := p.repository.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return identity.Actor{}, identity.Wrap(identity.CodeDependency, "begin identity resolution", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	for name, value := range map[string]string{
		"app.identity_issuer":       principal.Issuer,
		"app.identity_tenant_id":    principal.TenantID,
		"app.identity_organization": principal.ExternalOrganization,
	} {
		if _, err := tx.Exec(ctx, `SELECT set_config($1,$2,true)`, name, value); err != nil {
			return identity.Actor{}, identity.Wrap(identity.CodeDependency, "set verified identity context", err)
		}
	}
	var realmID, tenantID string
	err = tx.QueryRow(ctx, `
		SELECT id::text,tenant_id::text
		  FROM tenant_identity_realms
		 WHERE issuer=$1 AND state='active'
		   AND (($2<>'' AND tenancy_mode='dedicated' AND tenant_id=nullif($2,'')::uuid)
		     OR ($2='' AND tenancy_mode='shared' AND external_organization_key=$3))`,
		principal.Issuer, principal.TenantID, principal.ExternalOrganization).Scan(&realmID, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Actor{}, identity.E(identity.CodeUnauthenticated, "identity realm is not provisioned")
	}
	if err != nil {
		return identity.Actor{}, identity.Wrap(identity.CodeDependency, "resolve identity realm", err)
	}
	if err := setTenantContext(ctx, tx, tenantID); err != nil {
		return identity.Actor{}, identity.Wrap(identity.CodeDependency, "set resolved tenant context", err)
	}
	var membership identity.Membership
	err = tx.QueryRow(ctx, `
		SELECT id::text,tenant_id::text,realm_id::text,subject,actor_type,state,revision,created_at,updated_at
		  FROM tenant_memberships
		 WHERE tenant_id=$1 AND realm_id=$2 AND subject=$3 AND state='active'`, tenantID, realmID, principal.Subject).Scan(
		&membership.ID, &membership.TenantID, &membership.RealmID, &membership.Subject, &membership.ActorType,
		&membership.State, &membership.Revision, &membership.CreatedAt, &membership.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Actor{}, identity.E(identity.CodeUnauthenticated, "identity is not an active Catalog member")
	}
	if err != nil {
		return identity.Actor{}, identity.Wrap(identity.CodeDependency, "resolve Catalog membership", err)
	}
	actor := identity.BindPermissionStore(identity.Actor{Issuer: principal.Issuer, TenantID: tenantID, Subject: membership.Subject, RealmID: realmID, MembershipID: membership.ID, ActorType: membership.ActorType, Claims: copyStringMap(principal.Claims)}, p)
	if !hydrate {
		if err := tx.Commit(ctx); err != nil {
			return identity.Actor{}, identity.Wrap(identity.CodeDependency, "commit identity resolution", err)
		}
		return actor, nil
	}
	actor.Namespaces = make(map[string][]identity.Role)
	rows, err := tx.Query(ctx, `
		SELECT assignment.role,coalesce(assignment.namespace_id::text,'')
		  FROM role_assignments assignment
		  LEFT JOIN namespace_memberships namespace_membership
		    ON namespace_membership.tenant_id=assignment.tenant_id
		   AND namespace_membership.membership_id=assignment.membership_id
		   AND namespace_membership.namespace_id=assignment.namespace_id
		   AND namespace_membership.state='active'
		 WHERE assignment.tenant_id=$1 AND assignment.membership_id=$2 AND assignment.state='active'
		   AND assignment.role IN ('reader','contributor','reviewer','publisher','source_admin','security_reviewer','tenant_admin','service_client')
		   AND (assignment.namespace_id IS NULL OR namespace_membership.id IS NOT NULL)
		 ORDER BY coalesce(assignment.namespace_id::text,''),assignment.role`, tenantID, membership.ID)
	if err != nil {
		return identity.Actor{}, identity.Wrap(identity.CodeDependency, "resolve Catalog role assignments", err)
	}
	for rows.Next() {
		var role identity.Role
		var namespaceID string
		if err := rows.Scan(&role, &namespaceID); err != nil {
			rows.Close()
			return identity.Actor{}, identity.Wrap(identity.CodeDependency, "scan Catalog role assignment", err)
		}
		if namespaceID == "" {
			actor.Roles = append(actor.Roles, role)
		} else {
			actor.Namespaces[namespaceID] = append(actor.Namespaces[namespaceID], role)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return identity.Actor{}, identity.Wrap(identity.CodeDependency, "iterate Catalog role assignments", err)
	}
	rows.Close()
	actor.Roles = sortedIdentityRoles(actor.Roles)
	for namespaceID, roles := range actor.Namespaces {
		actor.Namespaces[namespaceID] = sortedIdentityRoles(roles)
	}
	if err := tx.Commit(ctx); err != nil {
		return identity.Actor{}, identity.Wrap(identity.CodeDependency, "commit identity resolution", err)
	}
	return actor, nil
}

// copyStringMap clones verified claim annotations so the resolved actor does not
// share mutable map storage with its principal.
func copyStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// sortedIdentityRoles sorts and deduplicates roles in place, returning a slice
// backed by the caller's storage.
func sortedIdentityRoles(values []identity.Role) []identity.Role {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

// ListMemberships returns a SQL-limited tenant page ordered by bytewise subject
// and ID, including inactive memberships for administration.
func (p *PostgresIdentityStore) ListMemberships(ctx context.Context, tenantID, afterSubject, afterID string, limit int, filters ...identity.MembershipFilter) ([]identity.Membership, error) {
	filter, err := identity.ValidateMembershipFilters(filters)
	if err != nil {
		return nil, err
	}
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]identity.Membership, error) {
		predicate := ""
		args := []any{tenantID, afterSubject, afterID, limit}
		if filter != (identity.MembershipFilter{}) {
			predicate = ` AND ($5='' OR subject=$5) AND ($6='' OR state=$6) AND ($7='' OR actor_type=$7) AND ($8='' OR realm_id=nullif($8,'')::uuid)`
			args = append(args, filter.Subject, filter.State, filter.ActorType, filter.RealmID)
		}
		rows, err := tx.Query(ctx, `
			SELECT id::text,tenant_id::text,realm_id::text,subject,actor_type,state,revision,created_at,updated_at
			  FROM tenant_memberships
			 WHERE tenant_id=$1
			   AND ($2='' OR subject COLLATE "C">$2 COLLATE "C" OR (subject=$2 AND id>nullif($3,'')::uuid))`+predicate+`
			 ORDER BY subject COLLATE "C",id
			 LIMIT $4`, args...)
		if err != nil {
			return nil, translateIdentityError("list memberships", err)
		}
		defer rows.Close()
		items := make([]identity.Membership, 0)
		for rows.Next() {
			membership, err := scanMembership(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, membership)
		}
		if err := rows.Err(); err != nil {
			return nil, translateIdentityError("iterate memberships", err)
		}
		return items, nil
	})
}

// GetMembership reads one tenant-bound membership, including inactive states;
// missing or other-tenant IDs return an identity not-found error.
func (p *PostgresIdentityStore) GetMembership(ctx context.Context, tenantID, membershipID string) (identity.Membership, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (identity.Membership, error) {
		return scanMembership(tx.QueryRow(ctx, `
			SELECT id::text,tenant_id::text,realm_id::text,subject,actor_type,state,revision,created_at,updated_at
			  FROM tenant_memberships WHERE tenant_id=$1 AND id=$2`, tenantID, membershipID))
	})
}

// ChangeMembership serializes tenant identity administration so last-admin
// protection and offboarding revocations are decided from one locked state.
func (p *PostgresIdentityStore) ChangeMembership(ctx context.Context, params identity.ChangeMembershipParams) (identity.MembershipLifecycleResult, error) {
	return withTenantTx(ctx, p.repository.pool, params.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (identity.MembershipLifecycleResult, error) {
		if err := lockIdentityAdministration(ctx, tx, params.TenantID); err != nil {
			return identity.MembershipLifecycleResult{}, err
		}
		membership, err := scanMembership(tx.QueryRow(ctx, `
			SELECT id::text,tenant_id::text,realm_id::text,subject,actor_type,state,revision,created_at,updated_at
			  FROM tenant_memberships WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, params.TenantID, params.MembershipID))
		if err != nil {
			return identity.MembershipLifecycleResult{}, err
		}
		if membership.Revision != params.ExpectedRevision {
			return identity.MembershipLifecycleResult{}, identity.E(identity.CodePreconditionFailed, "membership revision does not match")
		}
		var targetState identity.MembershipState
		switch params.Transition {
		case identity.MembershipSuspend:
			if membership.State != identity.MembershipActive {
				return identity.MembershipLifecycleResult{}, identity.E(identity.CodeConflict, "only an active membership can be suspended")
			}
			targetState = identity.MembershipSuspended
		case identity.MembershipReactivate:
			if membership.State != identity.MembershipSuspended {
				return identity.MembershipLifecycleResult{}, identity.E(identity.CodeConflict, "only a suspended membership can be reactivated")
			}
			var activeRealm bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_identity_realms WHERE tenant_id=$1 AND id=$2 AND state='active')`, params.TenantID, membership.RealmID).Scan(&activeRealm); err != nil {
				return identity.MembershipLifecycleResult{}, translateIdentityError("read membership identity realm", err)
			}
			if !activeRealm {
				return identity.MembershipLifecycleResult{}, identity.E(identity.CodeConflict, "membership cannot be reactivated while its identity realm is suspended")
			}
			targetState = identity.MembershipActive
		case identity.MembershipOffboard:
			if membership.State == identity.MembershipOffboarded {
				return identity.MembershipLifecycleResult{}, identity.E(identity.CodeConflict, "membership is already offboarded")
			}
			targetState = identity.MembershipOffboarded
		default:
			return identity.MembershipLifecycleResult{}, identity.E(identity.CodeInvalidArgument, "membership transition is invalid")
		}
		if targetState != identity.MembershipActive && membership.State == identity.MembershipActive {
			var targetIsAdministrator bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(
				  SELECT 1 FROM tenant_identity_realms realm
				  JOIN role_assignments assignment ON assignment.tenant_id=realm.tenant_id
				   AND assignment.membership_id=$2 AND assignment.role='tenant_admin'
				   AND assignment.namespace_id IS NULL AND assignment.state='active'
				 WHERE realm.tenant_id=$1 AND realm.id=$3 AND realm.state='active')`, params.TenantID, membership.ID, membership.RealmID).Scan(&targetIsAdministrator); err != nil {
				return identity.MembershipLifecycleResult{}, translateIdentityError("inspect tenant administrator membership", err)
			}
			if targetIsAdministrator {
				var administrators int
				if err := tx.QueryRow(ctx, `
					SELECT count(DISTINCT membership.id)
					  FROM tenant_memberships membership
					  JOIN tenant_identity_realms realm ON realm.tenant_id=membership.tenant_id AND realm.id=membership.realm_id AND realm.state='active'
					  JOIN role_assignments assignment ON assignment.tenant_id=membership.tenant_id AND assignment.membership_id=membership.id
					   AND assignment.role='tenant_admin' AND assignment.namespace_id IS NULL AND assignment.state='active'
					 WHERE membership.tenant_id=$1 AND membership.state='active'`, params.TenantID).Scan(&administrators); err != nil {
					return identity.MembershipLifecycleResult{}, translateIdentityError("count tenant administrator memberships", err)
				}
				if administrators <= 1 {
					return identity.MembershipLifecycleResult{}, identity.E(identity.CodeSeparationOfDuties, "the final active tenant administrator membership cannot be disabled")
				}
			}
		}
		result := identity.MembershipLifecycleResult{}
		if params.Transition == identity.MembershipOffboard {
			command, err := tx.Exec(ctx, `
				UPDATE role_assignments
				   SET state='revoked',revision=revision+1,revoked_by=$3,revoke_reason=$4,revoked_at=$5
				 WHERE tenant_id=$1 AND membership_id=$2 AND state='active'`, params.TenantID, membership.ID, params.Actor, params.Reason, params.At)
			if err != nil {
				return identity.MembershipLifecycleResult{}, translateIdentityError("revoke offboarded membership roles", err)
			}
			result.RevokedRoleAssignments = int(command.RowsAffected())
			command, err = tx.Exec(ctx, `
				UPDATE namespace_memberships
				   SET state='revoked',revision=revision+1,revoked_by=$3,revoke_reason=$4,revoked_at=$5
				 WHERE tenant_id=$1 AND membership_id=$2 AND state='active'`, params.TenantID, membership.ID, params.Actor, params.Reason, params.At)
			if err != nil {
				return identity.MembershipLifecycleResult{}, translateIdentityError("revoke offboarded namespace memberships", err)
			}
			result.RevokedNamespaceMemberships = int(command.RowsAffected())
			command, err = tx.Exec(ctx, `UPDATE access_group_members SET state='revoked',revision=revision+1,revoked_by=$3,revoke_reason=$4,revoked_at=GREATEST($5,added_at) WHERE tenant_id=$1 AND membership_id=$2 AND state='active'`, params.TenantID, membership.ID, params.Actor, params.Reason, params.At)
			if err != nil {
				return identity.MembershipLifecycleResult{}, translateIdentityError("revoke offboarded group memberships", err)
			}
			result.RevokedGroupMemberships = int(command.RowsAffected())
		}
		membership.State = targetState
		membership.Revision++
		membership.UpdatedAt = params.At
		if _, err := tx.Exec(ctx, `UPDATE tenant_memberships SET state=$3,revision=$4,updated_at=$5 WHERE tenant_id=$1 AND id=$2`, params.TenantID, membership.ID, membership.State, membership.Revision, membership.UpdatedAt); err != nil {
			return identity.MembershipLifecycleResult{}, translateIdentityError("update membership", err)
		}
		facts := map[string]any{"transition": params.Transition, "revokedRoleAssignments": result.RevokedRoleAssignments, "revokedNamespaceMemberships": result.RevokedNamespaceMemberships, "revokedGroupMemberships": result.RevokedGroupMemberships}

		if params.Evidence.Details == nil {
			params.Evidence.Details = make(map[string]any)
		}
		params.Evidence.Details["facts"] = facts

		expectedAction := "identity.membership.offboarded"
		if params.Transition == identity.MembershipSuspend {
			expectedAction = "identity.membership.suspended"
		} else if params.Transition == identity.MembershipReactivate {
			expectedAction = "identity.membership.reactivated"
		}
		if err := insertIdentityEvidence(ctx, tx, params.TenantID, params.Evidence, expectEvidence(params.TenantID, "tenant_membership", params.MembershipID, expectedAction)); err != nil {
			return identity.MembershipLifecycleResult{}, err
		}
		result.Membership = membership
		return result, nil
	})
}

// scanMembership decodes the shared membership projection and translates missing
// rows into identity-domain errors.
func scanMembership(row scanner) (identity.Membership, error) {
	var membership identity.Membership
	if err := row.Scan(&membership.ID, &membership.TenantID, &membership.RealmID, &membership.Subject, &membership.ActorType, &membership.State, &membership.Revision, &membership.CreatedAt, &membership.UpdatedAt); err != nil {
		return identity.Membership{}, translateIdentityError("read membership", err)
	}
	return membership, nil
}

// ListRoleAssignments returns a SQL-limited tenant page, optionally filtered by
// membership, ordered by subject, namespace, role and ID.
func (p *PostgresIdentityStore) ListRoleAssignments(ctx context.Context, tenantID string, request identity.RoleAssignmentListRequest) ([]identity.RoleAssignment, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]identity.RoleAssignment, error) {
		rows, err := tx.Query(ctx, `
			SELECT assignment.id::text,assignment.tenant_id::text,assignment.membership_id::text,membership.subject,membership.actor_type,
			       assignment.role,coalesce(assignment.namespace_id::text,''),assignment.state,assignment.revision,
			       assignment.granted_by,assignment.grant_reason,assignment.granted_at,coalesce(assignment.revoked_by,''),assignment.revoke_reason,assignment.revoked_at
			  FROM role_assignments assignment
			  JOIN tenant_memberships membership ON membership.tenant_id=assignment.tenant_id AND membership.id=assignment.membership_id
			  JOIN role_definitions definition ON definition.tenant_id=assignment.tenant_id AND definition.id=assignment.role_definition_id AND definition.builtin AND definition.key=assignment.role
			 WHERE assignment.tenant_id=$1
			   AND assignment.group_id IS NULL AND assignment.membership_id IS NOT NULL
			   AND assignment.role IN ('reader','contributor','reviewer','publisher','source_admin','security_reviewer','tenant_admin','service_client')
			   AND ($7='' OR assignment.membership_id=nullif($7,'')::uuid)
			   AND ($2='' OR (membership.subject COLLATE "C",coalesce(assignment.namespace_id::text,'') COLLATE "C",assignment.role::text COLLATE "C",assignment.id) > ($2::text COLLATE "C",$3::text COLLATE "C",$4::text COLLATE "C",nullif($5,'')::uuid))
			 ORDER BY membership.subject COLLATE "C",coalesce(assignment.namespace_id::text,'') COLLATE "C",assignment.role COLLATE "C",assignment.id
			 LIMIT $6`, tenantID, request.AfterSubject, request.AfterNamespaceID, request.AfterRole, request.AfterID, request.Limit, request.MembershipID)
		if err != nil {
			return nil, translateIdentityError("list role assignments", err)
		}
		defer rows.Close()
		result := make([]identity.RoleAssignment, 0)
		for rows.Next() {
			assignment, err := scanRoleAssignment(rows)
			if err != nil {
				return nil, err
			}
			result = append(result, assignment)
		}
		if err := rows.Err(); err != nil {
			return nil, translateIdentityError("iterate role assignments", err)
		}
		return result, nil
	})
}

// GrantRole atomically creates an active assignment and its evidence, creating a
// membership when absent and restoring namespace membership as needed. Tenant
// identity locking serializes it with revocation and offboarding.
func (p *PostgresIdentityStore) GrantRole(ctx context.Context, params identity.GrantRoleParams) (identity.RoleAssignment, error) {
	return withTenantTx(ctx, p.repository.pool, params.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (identity.RoleAssignment, error) {
		if err := lockIdentityAdministration(ctx, tx, params.TenantID); err != nil {
			return identity.RoleAssignment{}, err
		}
		var realmTenant string
		if err := tx.QueryRow(ctx, `SELECT tenant_id::text FROM tenant_identity_realms WHERE tenant_id=$1 AND id=$2 AND state='active'`, params.TenantID, params.RealmID).Scan(&realmTenant); err != nil {
			return identity.RoleAssignment{}, translateIdentityError("read identity realm", err)
		}
		if realmTenant != params.TenantID {
			return identity.RoleAssignment{}, identity.E(identity.CodeNotFound, "identity realm was not found")
		}
		var membership identity.Membership
		err := tx.QueryRow(ctx, `
			SELECT id::text,tenant_id::text,realm_id::text,subject,actor_type,state,revision,created_at,updated_at
			  FROM tenant_memberships WHERE tenant_id=$1 AND realm_id=$2 AND subject=$3 FOR UPDATE`, params.TenantID, params.RealmID, params.Subject).Scan(
			&membership.ID, &membership.TenantID, &membership.RealmID, &membership.Subject, &membership.ActorType,
			&membership.State, &membership.Revision, &membership.CreatedAt, &membership.UpdatedAt,
		)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			membership = identity.Membership{ID: params.MembershipID, TenantID: params.TenantID, RealmID: params.RealmID, Subject: params.Subject, ActorType: params.ActorType, State: identity.MembershipActive, Revision: 1, CreatedAt: params.At, UpdatedAt: params.At}
			if _, err := tx.Exec(ctx, `INSERT INTO tenant_memberships(id,tenant_id,realm_id,subject,actor_type,state,revision,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, membership.ID, membership.TenantID, membership.RealmID, membership.Subject, membership.ActorType, membership.State, membership.Revision, membership.CreatedAt, membership.UpdatedAt); err != nil {
				return identity.RoleAssignment{}, translateIdentityError("create tenant membership", err)
			}
		case err != nil:
			return identity.RoleAssignment{}, translateIdentityError("read tenant membership", err)
		case membership.ActorType != params.ActorType || membership.State != identity.MembershipActive:
			return identity.RoleAssignment{}, identity.E(identity.CodeConflict, "membership exists with another type or state")
		}
		if params.NamespaceID != "" {
			// A clock correction must not backdate the retained membership when
			// restoring access. Keep the database lifecycle guard authoritative.
			if _, err := tx.Exec(ctx, `
				INSERT INTO namespace_memberships(id,tenant_id,membership_id,namespace_id,state,revision,created_by,created_at)
				VALUES ($1,$2,$3,$4,'active',1,$5,$6)
				ON CONFLICT (tenant_id,membership_id,namespace_id) DO UPDATE
				SET state='active',revision=namespace_memberships.revision+1,created_by=excluded.created_by,
				    created_at=GREATEST(namespace_memberships.created_at,namespace_memberships.revoked_at,excluded.created_at),
				    revoked_by=NULL,revoke_reason='',revoked_at=NULL
				WHERE namespace_memberships.state='revoked'`, params.NamespaceMembershipID, params.TenantID, membership.ID, params.NamespaceID, params.Actor, params.At); err != nil {
				return identity.RoleAssignment{}, translateIdentityError("ensure namespace membership", err)
			}
		}
		assignment := identity.RoleAssignment{ID: params.AssignmentID, TenantID: params.TenantID, MembershipID: membership.ID, Subject: membership.Subject, ActorType: membership.ActorType, Role: params.Role, NamespaceID: params.NamespaceID, State: identity.AssignmentActive, Revision: 1, GrantedBy: params.Actor, GrantReason: params.Reason, GrantedAt: params.At}
		if _, err := tx.Exec(ctx, `
			INSERT INTO role_assignments(id,tenant_id,membership_id,namespace_id,role,state,revision,granted_by,grant_reason,granted_at)
			VALUES ($1,$2,$3,nullif($4,'')::uuid,$5,$6,$7,$8,$9,$10)`, assignment.ID, assignment.TenantID, assignment.MembershipID, assignment.NamespaceID, assignment.Role, assignment.State, assignment.Revision, assignment.GrantedBy, assignment.GrantReason, assignment.GrantedAt); err != nil {
			return identity.RoleAssignment{}, translateIdentityError("create role assignment", err)
		}
		if err := insertIdentityEvidence(ctx, tx, params.TenantID, params.Evidence, expectEvidence(params.TenantID, "role_assignment", params.AssignmentID, "identity.role_assignment.granted")); err != nil {
			return identity.RoleAssignment{}, err
		}
		return assignment, nil
	})
}

// RevokeRole checks the expected revision under the tenant identity lock, protects
// the final active administrator, and atomically revokes the assignment and any
// now-empty namespace membership with evidence.
func (p *PostgresIdentityStore) RevokeRole(ctx context.Context, params identity.RevokeRoleParams) (identity.RoleAssignment, error) {
	return withTenantTx(ctx, p.repository.pool, params.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (identity.RoleAssignment, error) {
		if err := lockIdentityAdministration(ctx, tx, params.TenantID); err != nil {
			return identity.RoleAssignment{}, err
		}
		assignment, err := scanRoleAssignment(tx.QueryRow(ctx, `
			SELECT assignment.id::text,assignment.tenant_id::text,assignment.membership_id::text,membership.subject,membership.actor_type,
			       assignment.role,coalesce(assignment.namespace_id::text,''),assignment.state,assignment.revision,
			       assignment.granted_by,assignment.grant_reason,assignment.granted_at,coalesce(assignment.revoked_by,''),assignment.revoke_reason,assignment.revoked_at
			  FROM role_assignments assignment
			  JOIN tenant_memberships membership ON membership.tenant_id=assignment.tenant_id AND membership.id=assignment.membership_id
			  JOIN role_definitions definition ON definition.tenant_id=assignment.tenant_id AND definition.id=assignment.role_definition_id AND definition.builtin AND definition.key=assignment.role
			 WHERE assignment.tenant_id=$1 AND assignment.id=$2 AND membership.realm_id=$3
			   AND assignment.group_id IS NULL AND assignment.membership_id IS NOT NULL
			   AND assignment.role IN ('reader','contributor','reviewer','publisher','source_admin','security_reviewer','tenant_admin','service_client')
			 FOR UPDATE OF assignment`, params.TenantID, params.AssignmentID, params.RealmID))
		if err != nil {
			return identity.RoleAssignment{}, err
		}
		if assignment.Revision != params.ExpectedRevision {
			return identity.RoleAssignment{}, identity.E(identity.CodePreconditionFailed, "role-assignment revision does not match")
		}
		if assignment.State != identity.AssignmentActive {
			return identity.RoleAssignment{}, identity.E(identity.CodeConflict, "role assignment is already revoked")
		}
		if assignment.Role == identity.RoleTenantAdmin && assignment.NamespaceID == "" {
			var administrators int
			if err := tx.QueryRow(ctx, `
				SELECT count(*)
				  FROM role_assignments candidate
				  JOIN tenant_memberships membership
				    ON membership.tenant_id=candidate.tenant_id AND membership.id=candidate.membership_id AND membership.state='active'
				  JOIN tenant_identity_realms realm
				    ON realm.tenant_id=membership.tenant_id AND realm.id=membership.realm_id AND realm.state='active'
				 WHERE candidate.tenant_id=$1 AND candidate.role='tenant_admin' AND candidate.namespace_id IS NULL AND candidate.state='active'`, params.TenantID).Scan(&administrators); err != nil {
				return identity.RoleAssignment{}, translateIdentityError("count tenant administrators", err)
			}
			if administrators <= 1 {
				return identity.RoleAssignment{}, identity.E(identity.CodeSeparationOfDuties, "the final tenant administrator role cannot be revoked")
			}
		}
		assignment.State = identity.AssignmentRevoked
		assignment.Revision++
		assignment.RevokedBy = params.Actor
		assignment.RevokeReason = params.Reason
		assignment.RevokedAt = &params.At
		if _, err := tx.Exec(ctx, `
			UPDATE role_assignments
			   SET state='revoked',revision=$2,revoked_by=$3,revoke_reason=$4,revoked_at=$5
			 WHERE id=$1 AND tenant_id=$6`, assignment.ID, assignment.Revision, assignment.RevokedBy, assignment.RevokeReason, assignment.RevokedAt, params.TenantID); err != nil {
			return identity.RoleAssignment{}, translateIdentityError("revoke role assignment", err)
		}
		if assignment.NamespaceID != "" {
			if _, err := tx.Exec(ctx, `
				UPDATE namespace_memberships membership
				   SET state='revoked',revision=revision+1,revoked_by=$3,revoke_reason=$4,revoked_at=$5
				 WHERE tenant_id=$1 AND membership_id=$2 AND namespace_id=$6 AND state='active'
				   AND NOT EXISTS (
				     SELECT 1 FROM role_assignments assignment
				      WHERE assignment.tenant_id=$1 AND assignment.membership_id=$2 AND assignment.namespace_id=$6 AND assignment.state='active'
				   )`, params.TenantID, assignment.MembershipID, params.Actor, params.Reason, params.At, assignment.NamespaceID); err != nil {
				return identity.RoleAssignment{}, translateIdentityError("revoke empty namespace membership", err)
			}
		}
		if err := insertIdentityEvidence(ctx, tx, params.TenantID, params.Evidence, expectEvidence(params.TenantID, "role_assignment", params.AssignmentID, "identity.role_assignment.revoked")); err != nil {
			return identity.RoleAssignment{}, err
		}
		return assignment, nil
	})
}

// lockIdentityAdministration serializes identity changes for one tenant using the
// Catalog state lock and verifies that the tenant exists in the transaction.
func lockIdentityAdministration(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if err := lockAuthorizationWriter(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := acquireCatalogStateLock(ctx, tx, "tenant", tenantID); err != nil {
		return translateIdentityError("lock tenant identity administration", err)
	}
	var lockedTenant string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1`, tenantID).Scan(&lockedTenant); err != nil {
		return translateIdentityError("lock tenant identity administration", err)
	}
	return nil
}

// scanRoleAssignment decodes assignment history with its membership identity and
// translates database errors to identity-domain errors.
func scanRoleAssignment(row scanner) (identity.RoleAssignment, error) {
	var assignment identity.RoleAssignment
	if err := row.Scan(&assignment.ID, &assignment.TenantID, &assignment.MembershipID, &assignment.Subject, &assignment.ActorType,
		&assignment.Role, &assignment.NamespaceID, &assignment.State, &assignment.Revision,
		&assignment.GrantedBy, &assignment.GrantReason, &assignment.GrantedAt, &assignment.RevokedBy, &assignment.RevokeReason, &assignment.RevokedAt); err != nil {
		return identity.RoleAssignment{}, translateIdentityError("read role assignment", err)
	}
	return assignment, nil
}

// insertIdentityEvidence projects identity change evidence into the shared
// audit/outbox contract within the caller's transaction.
func insertIdentityEvidence(ctx context.Context, tx pgx.Tx, tenantID string, evidence identity.ChangeEvidence, expected ...evidenceExpectation) error {
	audit := catalog.AuditEvent{FormatVersion: evidence.FormatVersion, ID: evidence.AuditID, TenantID: tenantID, Actor: evidence.Actor, Action: evidence.Action, TargetType: evidence.TargetType, TargetID: evidence.TargetID, Reason: evidence.Reason, RequestID: evidence.RequestID, Details: evidence.Details, At: evidence.At}
	outbox := catalog.OutboxEvent{ID: evidence.OutboxID, TenantID: tenantID, Type: evidence.OutboxType, Subject: evidence.OutboxSubject, Data: json.RawMessage(evidence.OutboxData), CreatedAt: evidence.At, PublishedAt: evidence.OutboxPublished}
	if err := insertEvidence(ctx, tx, audit, outbox, expected...); err != nil {
		return identity.Wrap(identity.CodeDependency, "persist identity audit and outbox evidence", err)
	}
	return nil
}

// translateIdentityError preserves existing identity errors and maps PostgreSQL
// constraints, conflicts and policy denials to safe domain codes while retaining
// causes.
func translateIdentityError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var identityError *identity.Error
	if errors.As(err, &identityError) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.E(identity.CodeNotFound, operation+": object was not found")
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return identity.Wrap(identity.CodeConflict, operation+": active assignment already exists", err)
		case "23503", "23514", "22P02":
			return identity.Wrap(identity.CodeInvalidArgument, operation+": relational constraint failed", err)
		case "40001", "40P01":
			return identity.Wrap(identity.CodeConflict, operation+": concurrent transaction conflict", err)
		case "42501":
			return identity.Wrap(identity.CodeForbidden, operation+": tenant policy denied access", err)
		}
	}
	return identity.Wrap(identity.CodeDependency, operation, err)
}

var _ identity.Store = (*PostgresIdentityStore)(nil)
