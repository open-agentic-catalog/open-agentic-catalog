// Initial provisioning establishes a tenant, issuer realm and membership with
// schema-owner authority and database-attributed evidence.

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
)

type IdentityProvisioningInput struct {
	TenantID             string
	TenantDisplayName    string
	Issuer               string
	TenancyMode          string
	ExternalOrganization string
	Subject              string
	ActorType            identity.ActorType
	Roles                []identity.Role
	RoleTemplates        []string
	Namespaces           map[string][]identity.Role
	Operator             string
	Reason               string
	AcknowledgeOutbox    bool
}

type IdentityProvisioningResult struct {
	TenantID            string                      `json:"tenantId"`
	RealmID             string                      `json:"realmId"`
	MembershipID        string                      `json:"membershipId"`
	Subject             string                      `json:"subject"`
	ActorType           identity.ActorType          `json:"actorType"`
	Assignments         []identity.RoleAssignment   `json:"assignments"`
	TemplateAssignments []identity.AccessAssignment `json:"templateAssignments,omitempty"`
}

// ProvisionIdentity bootstraps the tenant, exact issuer mapping, first
// membership, role assignments, and audit/outbox evidence in one operator
// transaction. Repeated identical input is safe; conflicting identity is not.
func ProvisionIdentity(ctx context.Context, pool *pgxpool.Pool, input IdentityProvisioningInput) (IdentityProvisioningResult, error) {
	if err := ValidateIdentityProvisioningInput(input); err != nil {
		return IdentityProvisioningResult{}, err
	}
	normalizedReason, err := audit.NormalizeReason("identity.membership_provisioned", input.Reason)
	if err != nil {
		return IdentityProvisioningResult{}, fmt.Errorf("identity provisioning reason is invalid: %w", err)
	}
	input.Reason = normalizedReason
	if err := verifyIdentityProvisioningRole(ctx, pool); err != nil {
		return IdentityProvisioningResult{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IdentityProvisioningResult{}, fmt.Errorf("begin identity provisioning: %w", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	if err := setTenantContext(ctx, tx, input.TenantID); err != nil {
		return IdentityProvisioningResult{}, err
	}
	result, err := provisionIdentityTx(ctx, tx, input)
	if err != nil {
		return IdentityProvisioningResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IdentityProvisioningResult{}, fmt.Errorf("commit identity provisioning: %w", err)
	}
	return result, nil
}

// provisionIdentityTx applies validated, reason-normalized input in the caller's
// authorized, tenant-scoped transaction. Its result is provisional until the
// caller commits; transaction setup and cleanup remain with ProvisionIdentity.
func provisionIdentityTx(ctx context.Context, tx pgx.Tx, input IdentityProvisioningInput) (IdentityProvisioningResult, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,display_name,created_at) VALUES ($1,$2,clock_timestamp()) ON CONFLICT (id) DO NOTHING`, input.TenantID, input.TenantDisplayName); err != nil {
		return IdentityProvisioningResult{}, fmt.Errorf("provision tenant: %w", err)
	}
	if err := lockAuthorizationWriter(ctx, tx, input.TenantID); err != nil {
		return IdentityProvisioningResult{}, err
	}
	var tenantDisplayName string
	if err := tx.QueryRow(ctx, `SELECT display_name FROM tenants WHERE id=$1`, input.TenantID).Scan(&tenantDisplayName); err != nil {
		return IdentityProvisioningResult{}, fmt.Errorf("verify provisioned tenant: %w", err)
	}
	if tenantDisplayName != input.TenantDisplayName {
		return IdentityProvisioningResult{}, errors.New("tenant already exists with another display name")
	}
	var realmID string
	err := tx.QueryRow(ctx, `
		INSERT INTO tenant_identity_realms(id,tenant_id,issuer,tenancy_mode,external_organization_key,state,revision,created_at,updated_at)
		VALUES (uuidv7(),$1,$2,$3,$4,'active',1,clock_timestamp(),clock_timestamp())
		ON CONFLICT DO NOTHING
		RETURNING id::text`, input.TenantID, input.Issuer, input.TenancyMode, input.ExternalOrganization).Scan(&realmID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			SELECT id::text
			  FROM tenant_identity_realms
			 WHERE tenant_id=$1 AND issuer=$2 AND tenancy_mode=$3 AND external_organization_key=$4`,
			input.TenantID, input.Issuer, input.TenancyMode, input.ExternalOrganization).Scan(&realmID)
		if errors.Is(err, pgx.ErrNoRows) {
			return IdentityProvisioningResult{}, errors.New("identity realm mapping already belongs to another tenant or tenancy mode")
		}
	}
	if err != nil {
		return IdentityProvisioningResult{}, fmt.Errorf("provision identity realm: %w", err)
	}
	var membershipID string
	var membershipType identity.ActorType
	var membershipState identity.MembershipState
	err = tx.QueryRow(ctx, `
		INSERT INTO tenant_memberships(id,tenant_id,realm_id,subject,actor_type,state,revision,created_at,updated_at)
		VALUES (uuidv7(),$1,$2,$3,$4,'active',1,clock_timestamp(),clock_timestamp())
		ON CONFLICT (tenant_id,realm_id,subject) DO UPDATE SET updated_at=tenant_memberships.updated_at
		RETURNING id::text,actor_type,state`, input.TenantID, realmID, input.Subject, input.ActorType).Scan(&membershipID, &membershipType, &membershipState)
	if err != nil {
		return IdentityProvisioningResult{}, fmt.Errorf("provision tenant membership: %w", err)
	}
	if membershipType != input.ActorType || membershipState != identity.MembershipActive {
		return IdentityProvisioningResult{}, errors.New("membership already exists with another actor type or state")
	}
	roles := append([]identity.Role(nil), input.Roles...)
	sort.Slice(roles, func(i, j int) bool { return roles[i] < roles[j] })
	roles = slices.Compact(roles)
	roleNames := make([]string, 0, len(roles)+len(input.RoleTemplates))
	for _, role := range roles {
		roleNames = append(roleNames, string(role))
	}
	roleNames = append(roleNames, input.RoleTemplates...)
	sort.Strings(roleNames)
	assignments := make([]identity.RoleAssignment, 0, len(roles))
	for _, role := range roles {
		var assignment identity.RoleAssignment
		err := tx.QueryRow(ctx, `
			INSERT INTO role_assignments(id,tenant_id,membership_id,namespace_id,role,state,revision,granted_by,grant_reason,granted_at)
			VALUES (uuidv7(),$1,$2,NULL,$3,'active',1,$4,$5,clock_timestamp())
			ON CONFLICT (tenant_id,membership_id,(coalesce(namespace_id,'00000000-0000-0000-0000-000000000000'::uuid)),role) WHERE state='active'
			DO UPDATE SET granted_at=role_assignments.granted_at
			RETURNING id::text,tenant_id::text,membership_id::text,role,state,revision,granted_by,grant_reason,granted_at`, input.TenantID, membershipID, role, input.Operator, input.Reason).Scan(
			&assignment.ID, &assignment.TenantID, &assignment.MembershipID, &assignment.Role, &assignment.State, &assignment.Revision, &assignment.GrantedBy, &assignment.GrantReason, &assignment.GrantedAt,
		)
		if err != nil {
			return IdentityProvisioningResult{}, fmt.Errorf("provision role %q: %w", role, err)
		}
		assignment.Subject = input.Subject
		assignment.ActorType = input.ActorType
		assignments = append(assignments, assignment)
	}
	// Operator bootstrap explicitly expands former tenant-wide development
	// bundles into retained assignments. Runtime decisions never imply grants
	// from a role name; later administration changes individual assignments.
	for _, role := range roles {
		for _, key := range identity.BootstrapCompanions(role) {
			if _, err := tx.Exec(ctx, `INSERT INTO role_assignments
                (id,tenant_id,membership_id,realm_id,role_definition_id,role,namespace_id,state,revision,granted_by,grant_reason,granted_at)
                SELECT uuidv7(),$1,$2,$3,id,key,NULL,'active',1,$4,$5,clock_timestamp()
                FROM role_definitions WHERE tenant_id=$1 AND key=$6 AND builtin
                ON CONFLICT DO NOTHING`, input.TenantID, membershipID, realmID, input.Operator, input.Reason, key); err != nil {
				return IdentityProvisioningResult{}, fmt.Errorf("provision explicit companion assignment: %w", err)
			}
		}
	}
	// Only the explicit development provisioning path may select exact templates.
	// These assignments do not expand the historical bootstrap bundles.
	var templateAssignments []identity.AccessAssignment
	for _, key := range input.RoleTemplates {
		if _, err := tx.Exec(ctx, `INSERT INTO role_assignments
			(id,tenant_id,membership_id,realm_id,role_definition_id,role,namespace_id,state,revision,granted_by,grant_reason,granted_at)
			SELECT uuidv7(),$1,$2,$3,id,key,NULL,'active',1,$4,$5,clock_timestamp()
			FROM role_definitions WHERE tenant_id=$1 AND key=$6 AND builtin
			ON CONFLICT DO NOTHING`, input.TenantID, membershipID, realmID, input.Operator, input.Reason, key); err != nil {
			return IdentityProvisioningResult{}, fmt.Errorf("provision exact development template: %w", err)
		}
		assignment, err := scanAccessAssignment(tx.QueryRow(ctx, `SELECT `+accessAssignmentProjection+` FROM role_assignments a JOIN role_definitions d ON d.tenant_id=a.tenant_id AND d.id=a.role_definition_id WHERE a.tenant_id=$1 AND a.membership_id=$2 AND a.realm_id=$3 AND a.namespace_id IS NULL AND a.state='active' AND d.builtin AND d.key=$4`, input.TenantID, membershipID, realmID, key))
		if err != nil {
			return IdentityProvisioningResult{}, fmt.Errorf("verify exact development assignment: %w", err)
		}
		templateAssignments = append(templateAssignments, assignment)
	}
	scoped, err := provisionNamespaceAssignments(ctx, tx, input, membershipID)
	if err != nil {
		return IdentityProvisioningResult{}, err
	}
	assignments = append(assignments, scoped...)
	rolesJSON, err := json.Marshal(roleNames)
	if err != nil {
		return IdentityProvisioningResult{}, fmt.Errorf("encode provisioned role names: %w", err)
	}
	var auditAt time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&auditAt); err != nil {
		return IdentityProvisioningResult{}, fmt.Errorf("choose identity provisioning audit time: %w", err)
	}
	facts := map[string]any{"realmId": realmID, "tenancyMode": input.TenancyMode, "roleCount": len(roleNames), "rolesDigest": audit.DigestBytes(rolesJSON)}
	if len(scoped) > 0 {
		grants := make([]string, 0, len(scoped))
		for _, assignment := range scoped {
			grants = append(grants, assignment.NamespaceID+":"+string(assignment.Role))
		}
		encoded, err := json.Marshal(grants)
		if err != nil {
			return IdentityProvisioningResult{}, fmt.Errorf("encode scoped provisioning grants: %w", err)
		}
		facts["namespaceGrantCount"], facts["namespaceGrantsDigest"] = len(scoped), audit.DigestBytes(encoded)
	}
	auditEvent, err := newDatabaseAuditRecord(ctx, tx, input.TenantID, "identity.membership_provisioned", "tenant_membership", membershipID, input.Reason, input.Operator, facts, auditAt)
	if err != nil {
		return IdentityProvisioningResult{}, err
	}
	outbox, err := newDatabasePublicOutbox(ctx, tx, auditEvent, input.AcknowledgeOutbox)
	if err != nil {
		return IdentityProvisioningResult{}, err
	}
	if err := insertEvidence(ctx, tx, auditEvent, outbox, expectEvidence(input.TenantID, "tenant_membership", membershipID, "identity.membership_provisioned")); err != nil {
		return IdentityProvisioningResult{}, fmt.Errorf("record identity provisioning evidence: %w", err)
	}
	return IdentityProvisioningResult{TenantID: input.TenantID, RealmID: realmID, MembershipID: membershipID, Subject: input.Subject, ActorType: input.ActorType, Assignments: assignments, TemplateAssignments: templateAssignments}, nil
}

// ValidateIdentityProvisioningInput checks tenant/principal identity, tenancy-mode
// consistency, supported roles and bounded operator/reason fields before mutation.
func ValidateIdentityProvisioningInput(input IdentityProvisioningInput) error {
	if !identity.ValidTenantID(input.TenantID) || strings.TrimSpace(input.TenantDisplayName) == "" || len(input.TenantDisplayName) > 160 || strings.TrimSpace(input.Issuer) == "" || strings.TrimSpace(input.Issuer) != input.Issuer || len(input.Issuer) > 2048 || !identity.ValidSubject(input.Subject) || strings.TrimSpace(input.Operator) == "" || strings.TrimSpace(input.Operator) != input.Operator || len(input.Operator) > 512 || !identity.ValidActorType(input.ActorType) || len(input.Roles) == 0 && len(input.RoleTemplates) == 0 && len(input.Namespaces) == 0 {
		return errors.New("identity provisioning input is incomplete or invalid")
	}
	if len(input.Namespaces) > 0 {
		if input.Issuer != authn.DevelopmentIssuer || input.Operator != "catalogctl-development-bootstrap" || input.TenancyMode != "dedicated" {
			return errors.New("namespace seeds require explicit development provisioning")
		}
		for namespace, roles := range input.Namespaces {
			if !identity.ValidTenantID(namespace) || len(roles) == 0 {
				return errors.New("development namespace assignment is invalid")
			}
			for _, role := range roles {
				if role != identity.RoleReader && role != identity.RoleContributor && role != identity.RoleReviewer && role != identity.RolePublisher {
					return errors.New("development namespace assignment role is invalid")
				}
			}
		}
	}
	if len(input.RoleTemplates) > 0 {
		if input.Issuer != authn.DevelopmentIssuer || input.Operator != "catalogctl-development-bootstrap" || len(input.Roles) > 0 || len(input.RoleTemplates) > identity.MaxDevelopmentRoleTemplates {
			return errors.New("exact templates require explicit development provisioning")
		}
		seen := map[string]bool{}
		for _, key := range input.RoleTemplates {
			if _, ok := identity.BuiltinPermissions(key); !ok || seen[key] {
				return errors.New("development template is invalid or repeated")
			}
			seen[key] = true
		}
	}
	if _, err := audit.NormalizeReason("identity.membership_provisioned", input.Reason); err != nil {
		return fmt.Errorf("identity provisioning reason is invalid: %w", err)
	}
	if input.TenancyMode == "dedicated" && input.ExternalOrganization != "" || input.TenancyMode == "shared" && !identity.ValidSubject(input.ExternalOrganization) {
		return errors.New("identity provisioning tenancy mode and external organization do not agree")
	}
	if input.TenancyMode != "dedicated" && input.TenancyMode != "shared" {
		return errors.New("identity provisioning tenancy mode must be dedicated or shared")
	}
	for _, role := range input.Roles {
		if !identity.ValidRole(role) {
			return fmt.Errorf("identity provisioning role %q is invalid", role)
		}
	}
	return nil
}

// verifyIdentityProvisioningRole requires current-user ownership of the identity
// realm table for operator commands.
func verifyIdentityProvisioningRole(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("PostgreSQL pool is required")
	}
	return verifyIdentityProvisioningAuthority(ctx, pool)
}

// verifyIdentityProvisioningAuthority requires exact current-user ownership of
// the identity table without taking ownership of the caller's connection.
func verifyIdentityProvisioningAuthority(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	var currentUser, tableOwner string
	if err := query.QueryRow(ctx, `SELECT current_user,pg_get_userbyid(class.relowner) FROM pg_catalog.pg_class class WHERE class.oid=to_regclass('tenant_identity_realms')`).Scan(&currentUser, &tableOwner); err != nil {
		return fmt.Errorf("inspect identity provisioning database authority: %w", err)
	}
	if currentUser != tableOwner {
		return fmt.Errorf("identity provisioning requires the operator-controlled schema owner; current role %q does not own the identity tables", currentUser)
	}
	return nil
}
