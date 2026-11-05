// Runtime admission requires direct login as the expected API or worker identity
// and rejects role-membership and privilege-boundary violations.

package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	RuntimeRoleAPI    = "api"
	RuntimeRoleWorker = "worker"
)

type RoleAttributes struct {
	Name        string `json:"name"`
	Login       bool   `json:"login"`
	Superuser   bool   `json:"superuser"`
	Inherit     bool   `json:"inherit"`
	CreateRole  bool   `json:"createRole"`
	CreateDB    bool   `json:"createDatabase"`
	Replication bool   `json:"replication"`
	BypassRLS   bool   `json:"bypassRls"`
}

// VerifyRuntimeRole checks direct login, role attributes, absence of memberships
// and the exact effective privilege manifest before runtime admission.
func VerifyRuntimeRole(ctx context.Context, pool *pgxpool.Pool, runtimeRole string) (RoleAttributes, error) {
	if pool == nil {
		return RoleAttributes{}, errors.New("PostgreSQL pool is required")
	}
	return verifyRuntimeRole(ctx, pool, runtimeRole)
}

// verifyRuntimeRole checks admission through read-only queries. The caller
// retains ownership of the pool and its sessions.
func verifyRuntimeRole(ctx context.Context, query interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, runtimeRole string) (RoleAttributes, error) {
	var sessionRole, currentRole string
	if err := query.QueryRow(ctx, `SELECT session_user,current_user`).Scan(&sessionRole, &currentRole); err != nil {
		return RoleAttributes{}, fmt.Errorf("inspect PostgreSQL session and current roles: %w", err)
	}
	if sessionRole != currentRole {
		return RoleAttributes{}, fmt.Errorf("PostgreSQL %s connection must authenticate directly as its runtime role; session role %q assumed current role %q", runtimeRole, sessionRole, currentRole)
	}
	attributes, err := readRoleAttributes(ctx, query, currentRole)
	if err != nil {
		return RoleAttributes{}, err
	}
	if err := validateRuntimeRoleAttributes(attributes, runtimeRole); err != nil {
		return RoleAttributes{}, err
	}
	if err := validateRuntimeRoleMembership(ctx, query, attributes.Name, runtimeRole); err != nil {
		return RoleAttributes{}, err
	}
	schema, _, err := currentCatalogSchema(ctx, query)
	if err != nil {
		return RoleAttributes{}, err
	}
	report, err := inspectRuntimePrivileges(ctx, query, schema, runtimeRole, attributes.Name)
	if err != nil {
		return RoleAttributes{}, err
	}
	if !report.Compliant {
		return RoleAttributes{}, fmt.Errorf("PostgreSQL %s role %q does not match runtime privilege manifest v%d: %s", runtimeRole, attributes.Name, runtimePrivilegeManifestVersion, strings.Join(report.Differences, "; "))
	}
	return attributes, nil
}

// readRoleAttributes reads the named database identity's attributes without
// assuming or switching roles.
func readRoleAttributes(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, roleName string) (RoleAttributes, error) {
	var attributes RoleAttributes
	err := query.QueryRow(ctx, `
    SELECT role.rolname, role.rolcanlogin, role.rolsuper, role.rolinherit, role.rolcreaterole,
           role.rolcreatedb, role.rolreplication, role.rolbypassrls
      FROM pg_catalog.pg_roles role
     WHERE role.rolname = $1`, roleName).Scan(
		&attributes.Name, &attributes.Login, &attributes.Superuser, &attributes.Inherit, &attributes.CreateRole,
		&attributes.CreateDB, &attributes.Replication, &attributes.BypassRLS,
	)
	if err != nil {
		return RoleAttributes{}, fmt.Errorf("inspect PostgreSQL role %q: %w", roleName, err)
	}
	return attributes, nil
}

// validateRuntimeRoleAttributes requires least-privilege LOGIN NOINHERIT
// attributes and the API/worker-specific BYPASSRLS setting.
func validateRuntimeRoleAttributes(attributes RoleAttributes, runtimeRole string) error {
	if runtimeRole != RuntimeRoleAPI && runtimeRole != RuntimeRoleWorker {
		return fmt.Errorf("unsupported PostgreSQL runtime role %q", runtimeRole)
	}
	if !attributes.Login || attributes.Superuser || attributes.CreateRole || attributes.CreateDB || attributes.Replication || attributes.Inherit {
		return fmt.Errorf("PostgreSQL %s role %q must be LOGIN NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOINHERIT", runtimeRole, attributes.Name)
	}
	if runtimeRole == RuntimeRoleAPI && attributes.BypassRLS {
		return fmt.Errorf("PostgreSQL API role %q must be NOBYPASSRLS", attributes.Name)
	}
	if runtimeRole == RuntimeRoleWorker && !attributes.BypassRLS {
		return fmt.Errorf("PostgreSQL worker role %q must have BYPASSRLS", attributes.Name)
	}
	return nil
}

// validateRuntimeRoleMembership rejects participation on either side of a database
// role grant.
func validateRuntimeRoleMembership(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, roleName, runtimeRole string) error {
	var participatesInRoleMembership bool
	if err := query.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1
    FROM pg_catalog.pg_auth_members membership
    JOIN pg_catalog.pg_roles member ON member.oid=membership.member
    JOIN pg_catalog.pg_roles granted_role ON granted_role.oid=membership.roleid
   WHERE member.rolname=$1 OR granted_role.rolname=$1
)`, roleName).Scan(&participatesInRoleMembership); err != nil {
		return fmt.Errorf("inspect PostgreSQL %s role memberships for %q: %w", runtimeRole, roleName, err)
	}
	if participatesInRoleMembership {
		return fmt.Errorf("PostgreSQL %s role %q must not participate in database role memberships", runtimeRole, roleName)
	}
	return nil
}
