// Shared-tenancy provisioning creates one tenant, its exact issuer/organization
// mapping, its first administrator and its namespaces with schema-owner
// authority. A repeated run that finds the complete state writes nothing.

package database

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// MaxSharedProvisioningNamespaces bounds one provisioning command.
const MaxSharedProvisioningNamespaces = 50

const (
	SharedProvisioningCreated   = "created"
	SharedProvisioningUnchanged = "unchanged"
)

type SharedTenantProvisioningInput struct {
	TenantID             string
	TenantDisplayName    string
	Issuer               string
	ExternalOrganization string
	Subject              string
	Namespaces           []string
	Operator             string
	Reason               string
	AcknowledgeOutbox    bool
}

type SharedTenantProvisioningResult struct {
	Status       string   `json:"status"`
	TenantID     string   `json:"tenantId"`
	RealmID      string   `json:"realmId"`
	MembershipID string   `json:"membershipId"`
	Subject      string   `json:"subject"`
	Namespaces   []string `json:"namespaces"`
}

// identityInput maps the shared request to the common provisioning input: one
// human tenant administrator in a shared realm.
func (input SharedTenantProvisioningInput) identityInput() IdentityProvisioningInput {
	return IdentityProvisioningInput{
		TenantID: input.TenantID, TenantDisplayName: input.TenantDisplayName, Issuer: input.Issuer,
		TenancyMode: "shared", ExternalOrganization: input.ExternalOrganization, Subject: input.Subject,
		ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin},
		Operator: input.Operator, Reason: input.Reason, AcknowledgeOutbox: input.AcknowledgeOutbox,
	}
}

// ValidateSharedTenantProvisioningInput checks every required value, the shared
// mapping and the bounded namespace list before any database access.
func ValidateSharedTenantProvisioningInput(input SharedTenantProvisioningInput) error {
	if input.ExternalOrganization == "" || len(input.Namespaces) == 0 || len(input.Namespaces) > MaxSharedProvisioningNamespaces {
		return errors.New("shared tenant provisioning requires an external organization and 1 to 50 namespaces")
	}
	for _, slug := range input.Namespaces {
		if !catalog.ValidNamespaceSlug(slug) {
			return errors.New("shared tenant provisioning namespace slug is invalid")
		}
	}
	return ValidateIdentityProvisioningInput(input.identityInput())
}

// ProvisionSharedTenant applies the request in one owner transaction. Missing
// state is created with audit and outbox evidence; a conflicting tenant name or
// realm mapping fails closed; complete matching state is reported unchanged.
func ProvisionSharedTenant(ctx context.Context, pool *pgxpool.Pool, input SharedTenantProvisioningInput) (SharedTenantProvisioningResult, error) {
	input.Namespaces = slices.Compact(slices.Sorted(slices.Values(input.Namespaces)))
	if err := ValidateSharedTenantProvisioningInput(input); err != nil {
		return SharedTenantProvisioningResult{}, err
	}
	normalizedReason, err := audit.NormalizeReason("identity.membership_provisioned", input.Reason)
	if err != nil {
		return SharedTenantProvisioningResult{}, fmt.Errorf("shared tenant provisioning reason is invalid: %w", err)
	}
	input.Reason = normalizedReason
	if err := verifyIdentityProvisioningRole(ctx, pool); err != nil {
		return SharedTenantProvisioningResult{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SharedTenantProvisioningResult{}, fmt.Errorf("begin shared tenant provisioning: %w", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	if err := setTenantContext(ctx, tx, input.TenantID); err != nil {
		return SharedTenantProvisioningResult{}, err
	}
	result, err := provisionSharedTenantTx(ctx, tx, input)
	if err != nil {
		return SharedTenantProvisioningResult{}, err
	}
	if result.Status == SharedProvisioningUnchanged {
		return result, nil // The deferred rollback ends the read-only transaction.
	}
	if err := tx.Commit(ctx); err != nil {
		return SharedTenantProvisioningResult{}, fmt.Errorf("commit shared tenant provisioning: %w", err)
	}
	return result, nil
}

// provisionSharedTenantTx runs in the caller's tenant-scoped owner transaction.
// The caller commits only a created result.
func provisionSharedTenantTx(ctx context.Context, tx pgx.Tx, input SharedTenantProvisioningInput) (SharedTenantProvisioningResult, error) {
	// Serialize provisioning of one tenant so the completeness check and the
	// writes below observe one state.
	if err := acquireCatalogStateLock(ctx, tx, "tenant-provisioning", input.TenantID); err != nil {
		return SharedTenantProvisioningResult{}, fmt.Errorf("lock shared tenant provisioning: %w", err)
	}
	var realmID, membershipID string
	err := tx.QueryRow(ctx, `
		SELECT r.id::text, m.id::text
		  FROM tenant_identity_realms r
		  JOIN tenant_memberships m ON m.tenant_id=r.tenant_id AND m.realm_id=r.id
		 WHERE r.tenant_id=$1 AND r.issuer=$2 AND r.tenancy_mode='shared' AND r.external_organization_key=$3
		   AND m.subject=$4 AND m.actor_type=$5 AND m.state='active'`,
		input.TenantID, input.Issuer, input.ExternalOrganization, input.Subject, identity.ActorHuman).Scan(&realmID, &membershipID)
	complete := false
	switch {
	case err == nil:
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1 AND display_name=$2)
			   AND EXISTS(SELECT 1 FROM role_assignments WHERE tenant_id=$1 AND membership_id=$3 AND namespace_id IS NULL AND role='tenant_admin' AND state='active')
			   AND (SELECT count(*) FROM namespaces WHERE tenant_id=$1 AND slug = ANY($4)) = $5`,
			input.TenantID, input.TenantDisplayName, membershipID, input.Namespaces, len(input.Namespaces)).Scan(&complete); err != nil {
			return SharedTenantProvisioningResult{}, fmt.Errorf("inspect shared tenant provisioning state: %w", err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return SharedTenantProvisioningResult{}, fmt.Errorf("inspect shared tenant provisioning state: %w", err)
	}
	if complete {
		return SharedTenantProvisioningResult{Status: SharedProvisioningUnchanged, TenantID: input.TenantID, RealmID: realmID, MembershipID: membershipID, Subject: input.Subject, Namespaces: input.Namespaces}, nil
	}
	provisioned, err := provisionIdentityTx(ctx, tx, input.identityInput())
	if err != nil {
		return SharedTenantProvisioningResult{}, err
	}
	for _, slug := range input.Namespaces {
		if err := createProvisionedNamespace(ctx, tx, input, slug); err != nil {
			return SharedTenantProvisioningResult{}, err
		}
	}
	return SharedTenantProvisioningResult{Status: SharedProvisioningCreated, TenantID: input.TenantID, RealmID: provisioned.RealmID, MembershipID: provisioned.MembershipID, Subject: input.Subject, Namespaces: input.Namespaces}, nil
}

// createProvisionedNamespace inserts one missing namespace with creation
// evidence. An existing slug is left untouched and records no evidence.
func createProvisionedNamespace(ctx context.Context, tx pgx.Tx, input SharedTenantProvisioningInput, slug string) error {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO namespaces(id,tenant_id,slug,display_name,description,revision,created_at,updated_at)
		VALUES (uuidv7(),$1,$2,$2,'',1,clock_timestamp(),clock_timestamp())
		ON CONFLICT (tenant_id,slug) DO NOTHING
		RETURNING id::text`, input.TenantID, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("provision namespace %q: %w", slug, err)
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return fmt.Errorf("choose namespace provisioning audit time: %w", err)
	}
	event, err := newDatabaseAuditRecord(ctx, tx, input.TenantID, "namespace.created", "namespace", id, input.Reason, input.Operator, nil, at)
	if err != nil {
		return err
	}
	outbox, err := newDatabasePublicOutbox(ctx, tx, event, input.AcknowledgeOutbox)
	if err != nil {
		return err
	}
	if err := insertEvidence(ctx, tx, event, outbox, expectEvidence(input.TenantID, "namespace", id, "namespace.created")); err != nil {
		return fmt.Errorf("record namespace provisioning evidence: %w", err)
	}
	return nil
}
