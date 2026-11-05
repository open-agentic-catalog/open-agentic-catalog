package database

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const definitionProjection = `d.id::text,d.tenant_id::text,coalesce(d.key,''),d.builtin,coalesce(d.name,initcap(replace(d.key,'_',' '))),d.description,d.state,d.revision,d.created_at,d.updated_at,
 ARRAY(SELECT p.permission FROM role_permissions p WHERE p.tenant_id=d.tenant_id AND p.role_definition_id=d.id ORDER BY p.permission COLLATE "C")`
const groupProjection = `id::text,tenant_id::text,realm_id::text,name,description,state,revision,created_at,updated_at`
const accessAssignmentProjection = `a.id::text,a.tenant_id::text,a.realm_id::text,a.role_definition_id::text,coalesce(a.membership_id::text,''),coalesce(a.group_id::text,''),coalesce(a.namespace_id::text,''),a.state,a.revision,a.granted_by,a.grant_reason,a.granted_at,coalesce(a.revoked_by,''),a.revoke_reason,a.revoked_at`
const groupMemberProjection = `id::text,tenant_id::text,realm_id::text,group_id::text,membership_id::text,state,revision,added_by,add_reason,added_at,coalesce(revoked_by,''),revoke_reason,revoked_at`
const effectiveAssignmentPredicate = `a.state='active' AND d.state='active' AND (
 a.membership_id=$2 OR EXISTS(SELECT 1 FROM access_group_members gm JOIN access_groups g ON g.tenant_id=gm.tenant_id AND g.id=gm.group_id AND g.realm_id=gm.realm_id
 WHERE gm.tenant_id=a.tenant_id AND gm.group_id=a.group_id AND gm.membership_id=$2 AND gm.realm_id=$3 AND gm.state='active' AND g.state='active'))`

func scanDefinition(row scanner) (identity.RoleDefinition, error) {
	var d identity.RoleDefinition
	err := row.Scan(&d.ID, &d.TenantID, &d.Key, &d.Builtin, &d.Name, &d.Description, &d.State, &d.Revision, &d.CreatedAt, &d.UpdatedAt, &d.Permissions)
	if err != nil {
		return identity.RoleDefinition{}, translateIdentityError("read role definition", err)
	}
	d.NamespaceCapable = identity.NamespaceCapable(d.Permissions)
	return d, nil
}
func scanAccessGroup(row scanner) (identity.AccessGroup, error) {
	var g identity.AccessGroup
	err := row.Scan(&g.ID, &g.TenantID, &g.RealmID, &g.Name, &g.Description, &g.State, &g.Revision, &g.CreatedAt, &g.UpdatedAt)
	return g, translateIdentityError("read access group", err)
}
func scanAccessAssignment(row scanner) (identity.AccessAssignment, error) {
	var a identity.AccessAssignment
	err := row.Scan(&a.ID, &a.TenantID, &a.RealmID, &a.RoleDefinitionID, &a.MembershipID, &a.GroupID, &a.NamespaceID, &a.State, &a.Revision, &a.GrantedBy, &a.GrantReason, &a.GrantedAt, &a.RevokedBy, &a.RevokeReason, &a.RevokedAt)
	return a, translateIdentityError("read authorization assignment", err)
}
func scanGroupMember(row scanner) (identity.GroupMember, error) {
	var m identity.GroupMember
	err := row.Scan(&m.ID, &m.TenantID, &m.RealmID, &m.GroupID, &m.MembershipID, &m.State, &m.Revision, &m.AddedBy, &m.AddReason, &m.AddedAt, &m.RevokedBy, &m.RevokeReason, &m.RevokedAt)
	return m, translateIdentityError("read group member", err)
}
func collectAuthorizationRows[T any](rows pgx.Rows, decode func(scanner) (T, error)) ([]T, error) {
	defer rows.Close()
	items := []T{}
	for rows.Next() {
		item, err := decode(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, translateIdentityError("read authorization rows", err)
	}
	return items, nil
}
func postgresAuthorizationRead[T any](ctx context.Context, p *PostgresIdentityStore, a identity.Actor, request *identity.AuthorizationPageRequest, own bool, read func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	var err error
	ctx, err = identity.BindPermissionActor(ctx, a)
	if err != nil {
		return zero, err
	}
	if request != nil {
		if err := identity.ValidateAuthorizationPage(*request); err != nil {
			return zero, err
		}
	}
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if command := commandTransactionFrom(ctx); command != nil {
		command.mu.Lock()
		active := command.tx != nil
		fenced := command.authorizationShared || command.authorizationExclusive
		command.mu.Unlock()
		if active {
			if !fenced {
				return zero, identity.E(identity.CodeConflict, "authorization reads require transaction admission")
			}
			options.IsoLevel = ""
		}
	}
	return withTenantTx(ctx, p.repository.pool, a.TenantID, options, func(tx pgx.Tx) (T, error) {
		decision, err := checkCurrentPermissions(ctx, tx, a, []identity.PermissionCheck{{Permission: "access.manage"}})
		if err != nil {
			return zero, err
		}
		if !own && !decision.Allowed[0] {
			return zero, identity.E(identity.CodeForbidden, "current access administration permission is required")
		}
		if own {
			var active bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_memberships m JOIN tenant_identity_realms r ON r.tenant_id=m.tenant_id AND r.id=m.realm_id WHERE m.tenant_id=$1 AND m.id=$2 AND m.realm_id=$3 AND m.subject=$4 AND m.actor_type=$5 AND m.state='active' AND r.state='active')`, a.TenantID, a.MembershipID, a.RealmID, a.Subject, a.ActorType).Scan(&active); err != nil {
				return zero, translateIdentityError("read current principal", err)
			}
			if !active {
				return zero, identity.E(identity.CodeUnauthenticated, "current Catalog principal is inactive")
			}
		}
		if request != nil && decision.Revision != request.AuthorizationRevision {
			return zero, identity.E(identity.CodeConflict, "authorization changed; restart this page")
		}
		return read(tx)
	})
}
func postgresAuthorizationWrite[T any](ctx context.Context, p *PostgresIdentityStore, a identity.Actor, write func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	var err error
	ctx, err = identity.BindPermissionActor(ctx, a)
	if err != nil {
		return zero, err
	}
	return withTenantTx(ctx, p.repository.pool, a.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (T, error) {
		if err := lockIdentityAdministration(ctx, tx, a.TenantID); err != nil {
			return zero, err
		}
		return write(tx)
	})
}
func insertAuthorizationAudit(ctx context.Context, tx pgx.Tx, change identity.AdministrationChange, action, target string, facts map[string]any) error {
	record, err := identity.AdministrationRecord(change, action, target, facts)
	if err != nil {
		return err
	}
	// The exclusive writer fence selected the policy revision admitting this mutation.
	record.Details["authorizationDigest"] = identity.DecisionDigest(ctx)
	if err := insertAuditEvent(ctx, tx, record); err != nil {
		return identity.Wrap(identity.CodeDependency, "persist authorization audit", err)
	}
	return nil
}
func loadDefinition(ctx context.Context, tx pgx.Tx, tenant, id string) (identity.RoleDefinition, error) {
	return scanDefinition(tx.QueryRow(ctx, `SELECT `+definitionProjection+` FROM role_definitions d WHERE d.tenant_id=$1 AND d.id=$2`, tenant, id))
}
func loadAccessGroup(ctx context.Context, tx pgx.Tx, a identity.Actor, id string) (identity.AccessGroup, error) {
	return scanAccessGroup(tx.QueryRow(ctx, `SELECT `+groupProjection+` FROM access_groups WHERE tenant_id=$1 AND realm_id=$2 AND id=$3`, a.TenantID, a.RealmID, id))
}

func (p *PostgresIdentityStore) ListDefinitions(ctx context.Context, a identity.Actor, r identity.AuthorizationPageRequest) ([]identity.RoleDefinition, error) {
	return postgresAuthorizationRead(ctx, p, a, &r, false, func(tx pgx.Tx) ([]identity.RoleDefinition, error) {
		predicate := ""
		args := []any{a.TenantID, r.AfterID, r.Limit}
		if r.Name != "" || r.State != "" || r.BuiltinKey != "" {
			predicate = ` AND ($4='' OR coalesce(d.name,initcap(replace(d.key,'_',' ')))=$4) AND ($5='' OR d.state=$5) AND ($6='' OR d.builtin AND d.key=$6)`
			args = append(args, r.Name, r.State, r.BuiltinKey)
		}
		rows, err := tx.Query(ctx, `SELECT `+definitionProjection+` FROM role_definitions d WHERE d.tenant_id=$1 AND ($2='' OR d.id>nullif($2,'')::uuid)`+predicate+` ORDER BY d.id LIMIT $3`, args...)
		if err != nil {
			return nil, translateIdentityError("list role definitions", err)
		}
		return collectAuthorizationRows(rows, scanDefinition)
	})
}
func (p *PostgresIdentityStore) GetDefinition(ctx context.Context, a identity.Actor, id string) (identity.RoleDefinition, error) {
	return postgresAuthorizationRead(ctx, p, a, nil, false, func(tx pgx.Tx) (identity.RoleDefinition, error) { return loadDefinition(ctx, tx, a.TenantID, id) })
}
func (p *PostgresIdentityStore) SaveDefinition(ctx context.Context, params identity.SaveDefinitionParams) (identity.RoleDefinition, error) {
	return postgresAuthorizationWrite(ctx, p, params.Actor, func(tx pgx.Tx) (identity.RoleDefinition, error) {
		var before identity.RoleDefinition
		var err error
		if params.ExpectedRevision > 0 {
			before, err = loadDefinition(ctx, tx, params.Actor.TenantID, params.ID)
			if err != nil {
				return identity.RoleDefinition{}, err
			}
			if before.Builtin {
				return identity.RoleDefinition{}, identity.E(identity.CodeConflict, "built-in roles are immutable")
			}
			if before.Revision != params.ExpectedRevision {
				return identity.RoleDefinition{}, identity.E(identity.CodePreconditionFailed, "role revision does not match")
			}
			if before.State != identity.DefinitionActive || params.At.Before(before.UpdatedAt) {
				return identity.RoleDefinition{}, identity.E(identity.CodeConflict, "role cannot be changed in this state or time")
			}
		}
		if params.Archive && params.ExpectedRevision == 0 {
			return identity.RoleDefinition{}, identity.E(identity.CodeInvalidArgument, "role archival requires a current revision")
		}
		action := "authorization.role.created"
		if params.Archive || !identity.NamespaceCapable(params.Input.Permissions) {
			var incompatible bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM role_assignments WHERE tenant_id=$1 AND role_definition_id=$2 AND state='active' AND ($3 OR namespace_id IS NOT NULL))`, params.Actor.TenantID, params.ID, params.Archive).Scan(&incompatible); err != nil {
				return identity.RoleDefinition{}, translateIdentityError("check role assignment impact", err)
			}
			if incompatible {
				return identity.RoleDefinition{}, identity.E(identity.CodeConflict, "active assignments conflict with this role change")
			}
		}
		switch {
		case params.Archive:
			action = "authorization.role.archived"
			_, err = tx.Exec(ctx, `UPDATE role_definitions SET state='archived',revision=revision+1,updated_at=$3 WHERE tenant_id=$1 AND id=$2`, params.Actor.TenantID, params.ID, params.At)
		case params.ExpectedRevision == 0:
			_, err = tx.Exec(ctx, `INSERT INTO role_definitions(id,tenant_id,name,description,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5)`, params.ID, params.Actor.TenantID, params.Input.Name, params.Input.Description, params.At)
		default:
			action = "authorization.role.updated"
			_, err = tx.Exec(ctx, `UPDATE role_definitions SET name=$3,description=$4,revision=revision+1,updated_at=$5 WHERE tenant_id=$1 AND id=$2`, params.Actor.TenantID, params.ID, params.Input.Name, params.Input.Description, params.At)
		}
		if err != nil {
			return identity.RoleDefinition{}, translateIdentityError("write role definition", err)
		}
		if !params.Archive && !slices.Equal(before.Permissions, params.Input.Permissions) {
			if _, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_definition_id=$2`, params.Actor.TenantID, params.ID); err != nil {
				return identity.RoleDefinition{}, translateIdentityError("replace role permissions", err)
			}
			for _, key := range params.Input.Permissions {
				if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_definition_id,permission) VALUES($1,$2,$3)`, params.Actor.TenantID, params.ID, key); err != nil {
					return identity.RoleDefinition{}, translateIdentityError("insert role permission", err)
				}
			}
		}
		after, err := loadDefinition(ctx, tx, params.Actor.TenantID, params.ID)
		if err != nil {
			return identity.RoleDefinition{}, err
		}
		if err := insertAuthorizationAudit(ctx, tx, params.AdministrationChange, action, "role_definition", map[string]any{"before": before, "after": after}); err != nil {
			return identity.RoleDefinition{}, err
		}
		return after, nil
	})
}
func (p *PostgresIdentityStore) ListGroups(ctx context.Context, a identity.Actor, r identity.AuthorizationPageRequest) ([]identity.AccessGroup, error) {
	return postgresAuthorizationRead(ctx, p, a, &r, false, func(tx pgx.Tx) ([]identity.AccessGroup, error) {
		predicate := ""
		args := []any{a.TenantID, a.RealmID, r.AfterID, r.Limit}
		if r.Name != "" || r.State != "" {
			predicate = ` AND ($5='' OR name=$5) AND ($6='' OR state=$6)`
			args = append(args, r.Name, r.State)
		}
		rows, err := tx.Query(ctx, `SELECT `+groupProjection+` FROM access_groups WHERE tenant_id=$1 AND realm_id=$2 AND ($3='' OR id>nullif($3,'')::uuid)`+predicate+` ORDER BY id LIMIT $4`, args...)
		if err != nil {
			return nil, translateIdentityError("list access groups", err)
		}
		return collectAuthorizationRows(rows, scanAccessGroup)
	})
}
func (p *PostgresIdentityStore) GetGroup(ctx context.Context, a identity.Actor, id string) (identity.AccessGroup, error) {
	return postgresAuthorizationRead(ctx, p, a, nil, false, func(tx pgx.Tx) (identity.AccessGroup, error) { return loadAccessGroup(ctx, tx, a, id) })
}
func (p *PostgresIdentityStore) SaveGroup(ctx context.Context, params identity.SaveGroupParams) (identity.AccessGroup, error) {
	return postgresAuthorizationWrite(ctx, p, params.Actor, func(tx pgx.Tx) (identity.AccessGroup, error) {
		var before identity.AccessGroup
		var err error
		action := "authorization.group.created"
		if params.ExpectedRevision > 0 {
			before, err = loadAccessGroup(ctx, tx, params.Actor, params.ID)
			if err != nil {
				return identity.AccessGroup{}, err
			}
			if before.Revision != params.ExpectedRevision {
				return identity.AccessGroup{}, identity.E(identity.CodePreconditionFailed, "group revision does not match")
			}
			if before.State == identity.GroupArchived {
				return identity.AccessGroup{}, identity.E(identity.CodeConflict, "group cannot be changed in this state or time")
			}
			if params.At.Before(before.UpdatedAt) {
				return identity.AccessGroup{}, safeerr.At("authorization.group.time_regression", identity.E(identity.CodeConflict, "group cannot be changed in this state or time"))
			}
		}
		switch {
		case params.Transition != "":
			if params.ExpectedRevision == 0 || params.Transition == identity.GroupActive && before.State != identity.GroupSuspended || params.Transition == identity.GroupSuspended && before.State != identity.GroupActive || !slices.Contains([]identity.GroupState{identity.GroupActive, identity.GroupSuspended, identity.GroupArchived}, params.Transition) {
				return identity.AccessGroup{}, identity.E(identity.CodeConflict, "group transition is invalid")
			}
			if params.Transition == identity.GroupArchived {
				var active bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM role_assignments WHERE tenant_id=$1 AND group_id=$2 AND state='active') OR EXISTS(SELECT 1 FROM access_group_members WHERE tenant_id=$1 AND group_id=$2 AND state='active')`, params.Actor.TenantID, params.ID).Scan(&active); err != nil {
					return identity.AccessGroup{}, translateIdentityError("check group assignment impact", err)
				}
				if active {
					return identity.AccessGroup{}, identity.E(identity.CodeConflict, "revoke active assignments and members before group archival")
				}
			}
			switch params.Transition {
			case identity.GroupActive:
				action = "authorization.group.reactivated"
			case identity.GroupSuspended:
				action = "authorization.group.suspended"
			case identity.GroupArchived:
				action = "authorization.group.archived"
			}
			_, err = tx.Exec(ctx, `UPDATE access_groups SET state=$4,revision=revision+1,updated_at=$5 WHERE tenant_id=$1 AND realm_id=$2 AND id=$3`, params.Actor.TenantID, params.Actor.RealmID, params.ID, params.Transition, params.At)
		case params.ExpectedRevision == 0:
			_, err = tx.Exec(ctx, `INSERT INTO access_groups(id,tenant_id,realm_id,name,description,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6)`, params.ID, params.Actor.TenantID, params.Actor.RealmID, params.Input.Name, params.Input.Description, params.At)
		default:
			action = "authorization.group.updated"
			_, err = tx.Exec(ctx, `UPDATE access_groups SET name=$4,description=$5,revision=revision+1,updated_at=$6 WHERE tenant_id=$1 AND realm_id=$2 AND id=$3`, params.Actor.TenantID, params.Actor.RealmID, params.ID, params.Input.Name, params.Input.Description, params.At)
		}
		if err != nil {
			return identity.AccessGroup{}, translateIdentityError("write access group", err)
		}
		after, err := loadAccessGroup(ctx, tx, params.Actor, params.ID)
		if err != nil {
			return identity.AccessGroup{}, err
		}
		if err := insertAuthorizationAudit(ctx, tx, params.AdministrationChange, action, "access_group", map[string]any{"before": before, "after": after}); err != nil {
			return identity.AccessGroup{}, err
		}
		return after, nil
	})
}
func (p *PostgresIdentityStore) ListGroupMembers(ctx context.Context, a identity.Actor, r identity.AuthorizationPageRequest) ([]identity.GroupMember, error) {
	return postgresAuthorizationRead(ctx, p, a, &r, false, func(tx pgx.Tx) ([]identity.GroupMember, error) {
		if _, err := loadAccessGroup(ctx, tx, a, r.GroupID); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT `+groupMemberProjection+` FROM access_group_members WHERE tenant_id=$1 AND realm_id=$2 AND group_id=$3 AND ($4='' OR id>nullif($4,'')::uuid) ORDER BY id LIMIT $5`, a.TenantID, a.RealmID, r.GroupID, r.AfterID, r.Limit)
		if err != nil {
			return nil, translateIdentityError("list group members", err)
		}
		return collectAuthorizationRows(rows, scanGroupMember)
	})
}
func (p *PostgresIdentityStore) ChangeGroupMember(ctx context.Context, params identity.ChangeGroupMemberParams) (identity.GroupMember, error) {
	return postgresAuthorizationWrite(ctx, p, params.Actor, func(tx pgx.Tx) (identity.GroupMember, error) {
		group, err := loadAccessGroup(ctx, tx, params.Actor, params.GroupID)
		if err != nil {
			return identity.GroupMember{}, err
		}
		if group.State == identity.GroupArchived {
			return identity.GroupMember{}, identity.E(identity.CodeConflict, "group is archived")
		}
		var before identity.GroupMember
		action := "authorization.group_member.added"
		if params.Revoke {
			before, err = scanGroupMember(tx.QueryRow(ctx, `SELECT `+groupMemberProjection+` FROM access_group_members WHERE tenant_id=$1 AND realm_id=$2 AND group_id=$3 AND id=$4`, params.Actor.TenantID, params.Actor.RealmID, group.ID, params.ID))
			if err != nil {
				return identity.GroupMember{}, err
			}
			if before.Revision != params.ExpectedRevision {
				return identity.GroupMember{}, identity.E(identity.CodePreconditionFailed, "group member revision does not match")
			}
			if before.State != identity.AssignmentActive || params.At.Before(before.AddedAt) {
				return identity.GroupMember{}, identity.E(identity.CodeConflict, "group member cannot be revoked")
			}
			action = "authorization.group_member.revoked"
			_, err = tx.Exec(ctx, `UPDATE access_group_members SET state='revoked',revision=revision+1,revoked_by=$5,revoke_reason=$6,revoked_at=$7 WHERE tenant_id=$1 AND realm_id=$2 AND group_id=$3 AND id=$4`, params.Actor.TenantID, params.Actor.RealmID, group.ID, params.ID, params.Actor.Subject, params.Evidence.Reason, params.At)
		} else {
			member, memberErr := scanMembership(tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,realm_id::text,subject,actor_type,state,revision,created_at,updated_at FROM tenant_memberships WHERE tenant_id=$1 AND realm_id=$2 AND id=$3`, params.Actor.TenantID, params.Actor.RealmID, params.MembershipID))
			if memberErr != nil {
				return identity.GroupMember{}, memberErr
			}
			if member.State != identity.MembershipActive || group.State != identity.GroupActive {
				return identity.GroupMember{}, identity.E(identity.CodeConflict, "group and membership must be active")
			}
			_, err = tx.Exec(ctx, `INSERT INTO access_group_members(id,tenant_id,realm_id,group_id,membership_id,added_by,add_reason,added_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, params.ID, params.Actor.TenantID, params.Actor.RealmID, group.ID, params.MembershipID, params.Actor.Subject, params.Evidence.Reason, params.At)
		}
		if err != nil {
			return identity.GroupMember{}, translateIdentityError("write group member", err)
		}
		after, err := scanGroupMember(tx.QueryRow(ctx, `SELECT `+groupMemberProjection+` FROM access_group_members WHERE tenant_id=$1 AND realm_id=$2 AND group_id=$3 AND id=$4`, params.Actor.TenantID, params.Actor.RealmID, group.ID, params.ID))
		if err != nil {
			return identity.GroupMember{}, err
		}
		if err := insertAuthorizationAudit(ctx, tx, params.AdministrationChange, action, "group_member", map[string]any{"before": before, "after": after}); err != nil {
			return identity.GroupMember{}, err
		}
		return after, nil
	})
}
func (p *PostgresIdentityStore) ListAccessAssignments(ctx context.Context, a identity.Actor, r identity.AuthorizationPageRequest) ([]identity.AccessAssignment, error) {
	return postgresAuthorizationRead(ctx, p, a, &r, false, func(tx pgx.Tx) ([]identity.AccessAssignment, error) {
		if r.RoleDefinitionID != "" {
			if _, err := loadDefinition(ctx, tx, a.TenantID, r.RoleDefinitionID); err != nil {
				return nil, err
			}
		}
		if r.GroupID != "" {
			if _, err := loadAccessGroup(ctx, tx, a, r.GroupID); err != nil {
				return nil, err
			}
		}
		rows, err := tx.Query(ctx, `SELECT `+accessAssignmentProjection+` FROM role_assignments a WHERE a.tenant_id=$1 AND ($2='' OR a.id>nullif($2,'')::uuid) AND ($3='' OR a.role_definition_id=nullif($3,'')::uuid) AND ($4='' OR a.membership_id=nullif($4,'')::uuid) AND ($5='' OR a.group_id=nullif($5,'')::uuid) ORDER BY a.id LIMIT $6`, a.TenantID, r.AfterID, r.RoleDefinitionID, r.MembershipID, r.GroupID, r.Limit)
		if err != nil {
			return nil, translateIdentityError("list authorization assignments", err)
		}
		return collectAuthorizationRows(rows, scanAccessAssignment)
	})
}
func (p *PostgresIdentityStore) GrantAccessAssignment(ctx context.Context, params identity.GrantAccessAssignmentParams) (identity.AccessAssignment, error) {
	return postgresAuthorizationWrite(ctx, p, params.Actor, func(tx pgx.Tx) (identity.AccessAssignment, error) {
		input := params.Input
		role, err := loadDefinition(ctx, tx, params.Actor.TenantID, input.RoleDefinitionID)
		if err != nil {
			return identity.AccessAssignment{}, err
		}
		if role.State != identity.DefinitionActive {
			return identity.AccessAssignment{}, identity.E(identity.CodeConflict, "role is archived")
		}
		if (input.MembershipID == "") == (input.GroupID == "") {
			return identity.AccessAssignment{}, identity.E(identity.CodeInvalidArgument, "exactly one assignment target is required")
		}
		if input.NamespaceID != "" {
			if !role.NamespaceCapable {
				return identity.AccessAssignment{}, identity.E(identity.CodeInvalidArgument, "role cannot be assigned at namespace scope")
			}
			var visible bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM namespaces WHERE tenant_id=$1 AND id=$2)`, params.Actor.TenantID, input.NamespaceID).Scan(&visible); err != nil {
				return identity.AccessAssignment{}, translateIdentityError("read assignment namespace", err)
			}
			if !visible {
				return identity.AccessAssignment{}, identity.E(identity.CodeNotFound, "namespace was not found")
			}
		}
		if input.GroupID != "" {
			group, err := loadAccessGroup(ctx, tx, params.Actor, input.GroupID)
			if err != nil {
				return identity.AccessAssignment{}, err
			}
			if group.State != identity.GroupActive || role.Key == "tenant_admin" {
				return identity.AccessAssignment{}, identity.E(identity.CodeConflict, "group assignment is inactive or protected")
			}
		} else {
			member, err := scanMembership(tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,realm_id::text,subject,actor_type,state,revision,created_at,updated_at FROM tenant_memberships WHERE tenant_id=$1 AND realm_id=$2 AND id=$3`, params.Actor.TenantID, params.Actor.RealmID, input.MembershipID))
			if err != nil {
				return identity.AccessAssignment{}, err
			}
			if member.State != identity.MembershipActive {
				return identity.AccessAssignment{}, identity.E(identity.CodeConflict, "assignment membership is inactive")
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO role_assignments(id,tenant_id,realm_id,role_definition_id,membership_id,group_id,namespace_id,role,state,revision,granted_by,grant_reason,granted_at) VALUES($1,$2,$3,$4,nullif($5,'')::uuid,nullif($6,'')::uuid,nullif($7,'')::uuid,'','active',1,$8,$9,$10)`, params.ID, params.Actor.TenantID, params.Actor.RealmID, input.RoleDefinitionID, input.MembershipID, input.GroupID, input.NamespaceID, params.Actor.Subject, params.Evidence.Reason, params.At)
		if err != nil {
			return identity.AccessAssignment{}, translateIdentityError("grant authorization assignment", err)
		}
		after, err := scanAccessAssignment(tx.QueryRow(ctx, `SELECT `+accessAssignmentProjection+` FROM role_assignments a WHERE a.tenant_id=$1 AND a.id=$2`, params.Actor.TenantID, params.ID))
		if err != nil {
			return identity.AccessAssignment{}, err
		}
		if err := insertAuthorizationAudit(ctx, tx, params.AdministrationChange, "authorization.assignment.granted", "role_assignment", map[string]any{"after": after}); err != nil {
			return identity.AccessAssignment{}, err
		}
		return after, nil
	})
}
func (p *PostgresIdentityStore) RevokeAccessAssignment(ctx context.Context, params identity.AdministrationChange) (identity.AccessAssignment, error) {
	return postgresAuthorizationWrite(ctx, p, params.Actor, func(tx pgx.Tx) (identity.AccessAssignment, error) {
		before, err := scanAccessAssignment(tx.QueryRow(ctx, `SELECT `+accessAssignmentProjection+` FROM role_assignments a WHERE a.tenant_id=$1 AND a.realm_id=$2 AND a.id=$3`, params.Actor.TenantID, params.Actor.RealmID, params.ID))
		if err != nil {
			return identity.AccessAssignment{}, err
		}
		if before.Revision != params.ExpectedRevision {
			return identity.AccessAssignment{}, identity.E(identity.CodePreconditionFailed, "assignment revision does not match")
		}
		if before.State != identity.AssignmentActive {
			return identity.AccessAssignment{}, identity.E(identity.CodeConflict, "assignment cannot be revoked")
		}
		if params.At.Before(before.GrantedAt) {
			// Classify the actual attempted transition against the locked row.
			// Retain the conflict without exposing either time or the assignment.
			return identity.AccessAssignment{}, safeerr.At("authorization.assignment.time_regression", identity.E(identity.CodeConflict, "assignment cannot be revoked"))
		}
		role, err := loadDefinition(ctx, tx, params.Actor.TenantID, before.RoleDefinitionID)
		if err != nil {
			return identity.AccessAssignment{}, err
		}
		if role.Key == "tenant_admin" {
			count, err := activeTenantAdministratorCount(ctx, tx, params.Actor.TenantID)
			if err != nil {
				return identity.AccessAssignment{}, err
			}
			if count <= 1 {
				return identity.AccessAssignment{}, identity.E(identity.CodeSeparationOfDuties, "the final active administrator cannot be revoked")
			}
		}
		_, err = tx.Exec(ctx, `UPDATE role_assignments SET state='revoked',revision=revision+1,revoked_by=$3,revoke_reason=$4,revoked_at=$5 WHERE tenant_id=$1 AND id=$2`, params.Actor.TenantID, params.ID, params.Actor.Subject, params.Evidence.Reason, params.At)
		if err != nil {
			return identity.AccessAssignment{}, translateIdentityError("revoke authorization assignment", err)
		}
		after, err := scanAccessAssignment(tx.QueryRow(ctx, `SELECT `+accessAssignmentProjection+` FROM role_assignments a WHERE a.tenant_id=$1 AND a.id=$2`, params.Actor.TenantID, params.ID))
		if err != nil {
			return identity.AccessAssignment{}, err
		}
		if err := insertAuthorizationAudit(ctx, tx, params, "authorization.assignment.revoked", "role_assignment", map[string]any{"before": before, "after": after}); err != nil {
			return identity.AccessAssignment{}, err
		}
		return after, nil
	})
}
func (p *PostgresIdentityStore) ListEffectiveAssignments(ctx context.Context, a identity.Actor, r identity.AuthorizationPageRequest) ([]identity.EffectiveAssignment, error) {
	return postgresAuthorizationRead(ctx, p, a, &r, true, func(tx pgx.Tx) ([]identity.EffectiveAssignment, error) {
		rows, err := tx.Query(ctx, `SELECT `+accessAssignmentProjection+` FROM role_assignments a JOIN role_definitions d ON d.tenant_id=a.tenant_id AND d.id=a.role_definition_id WHERE a.tenant_id=$1 AND `+effectiveAssignmentPredicate+` AND ($4='' OR a.id>nullif($4,'')::uuid) ORDER BY a.id LIMIT $5`, a.TenantID, a.MembershipID, a.RealmID, r.AfterID, r.Limit)
		if err != nil {
			return nil, translateIdentityError("list effective assignments", err)
		}
		assignments, err := collectAuthorizationRows(rows, scanAccessAssignment)
		if err != nil {
			return nil, err
		}
		// Both stages are bounded by the page, within one coherent snapshot.
		ids := make([]string, 0, len(assignments))
		for _, assignment := range assignments {
			ids = append(ids, assignment.RoleDefinitionID)
		}
		items := []identity.EffectiveAssignment{}
		if len(ids) == 0 {
			return items, nil
		}
		rows, err = tx.Query(ctx, `SELECT `+definitionProjection+` FROM role_definitions d WHERE d.tenant_id=$1 AND d.id=ANY($2::uuid[]) LIMIT $3`, a.TenantID, ids, r.Limit)
		if err != nil {
			return nil, translateIdentityError("read effective role definitions", err)
		}
		definitions, err := collectAuthorizationRows(rows, scanDefinition)
		if err != nil {
			return nil, err
		}
		roles := make(map[string]identity.RoleDefinition, len(definitions))
		for _, definition := range definitions {
			roles[definition.ID] = definition
		}
		groupIDs := []string{}
		for _, assignment := range assignments {
			if assignment.GroupID != "" && !slices.Contains(groupIDs, assignment.GroupID) {
				groupIDs = append(groupIDs, assignment.GroupID)
			}
		}
		groupNames := map[string]string{}
		if len(groupIDs) != 0 {
			rows, err = tx.Query(ctx, `SELECT id::text,name FROM access_groups WHERE tenant_id=$1 AND realm_id=$2 AND id=ANY($3::uuid[]) LIMIT $4`, a.TenantID, a.RealmID, groupIDs, r.Limit)
			if err != nil {
				return nil, translateIdentityError("read effective group captions", err)
			}
			type caption struct{ id, name string }
			captions, err := collectAuthorizationRows(rows, func(row scanner) (caption, error) {
				var value caption
				err := row.Scan(&value.id, &value.name)
				return value, translateIdentityError("read effective group caption", err)
			})
			if err != nil {
				return nil, err
			}
			for _, value := range captions {
				groupNames[value.id] = value.name
			}
		}
		for _, assignment := range assignments {
			role, ok := roles[assignment.RoleDefinitionID]
			if !ok {
				return nil, identity.E(identity.CodeInternal, "effective role definition is missing")
			}
			item := identity.EffectiveAssignment{Assignment: assignment, Role: role}
			if assignment.GroupID != "" {
				var ok bool
				item.GroupName, ok = groupNames[assignment.GroupID]
				if !ok {
					return nil, identity.E(identity.CodeInternal, "effective group caption is missing")
				}
			}
			items = append(items, item)
		}
		return items, nil
	})
}

var _ identity.AdministrationStore = (*PostgresIdentityStore)(nil)

func activeTenantAdministratorCount(ctx context.Context, tx pgx.Tx, tenant string) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `SELECT count(DISTINCT m.id) FROM tenant_memberships m JOIN tenant_identity_realms r ON r.tenant_id=m.tenant_id AND r.id=m.realm_id JOIN role_assignments a ON a.tenant_id=m.tenant_id AND a.membership_id=m.id WHERE m.tenant_id=$1 AND m.state='active' AND r.state='active' AND a.state='active' AND a.namespace_id IS NULL AND a.role='tenant_admin' AND a.group_id IS NULL`, tenant).Scan(&count)
	return count, translateIdentityError("count current direct administrators", err)
}
