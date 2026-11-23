//go:build integration

// Real owner provisioning tests for shared tenancy: idempotency, conflicts and
// tenant isolation run against PostgreSQL with the exact API runtime role.
package database

import (
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const (
	sharedCustomerA = "00000000-0000-7000-8000-000000000901"
	sharedCustomerB = "00000000-0000-7000-8000-000000000902"
	sharedIssuer    = "https://identity.shared.example/realms/oac"
)

func sharedIntegrationInput(tenantID, name, organization string, namespaces ...string) SharedTenantProvisioningInput {
	return SharedTenantProvisioningInput{
		TenantID: tenantID, TenantDisplayName: name, Issuer: sharedIssuer, ExternalOrganization: organization,
		Subject: "admin-" + organization, Namespaces: namespaces, Operator: "qualification-operator",
		Reason: "Qualify shared tenant provisioning", AcknowledgeOutbox: true,
	}
}

func TestPostgresSharedTenantProvisioningIsIdempotentConflictSafeAndIsolated(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := t.Context()
	evidenceCounts := func() [4]int64 {
		t.Helper()
		var counts [4]int64
		if err := db.operator.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM audit_events WHERE tenant_id IN ($1,$2)),
			(SELECT count(*) FROM outbox_events WHERE tenant_id IN ($1,$2)),
			(SELECT count(*) FROM tenants WHERE id IN ($1,$2)),
			(SELECT count(*) FROM tenant_identity_realms WHERE tenant_id IN ($1,$2))`,
			sharedCustomerA, sharedCustomerB).Scan(&counts[0], &counts[1], &counts[2], &counts[3]); err != nil {
			t.Fatal(err)
		}
		return counts
	}
	inputA := sharedIntegrationInput(sharedCustomerA, "Customer A", "org-a", "alpha", "shared")
	inputB := sharedIntegrationInput(sharedCustomerB, "Customer B", "org-b", "beta", "shared")

	first, err := ProvisionSharedTenant(ctx, db.operator, inputA)
	if err != nil || first.Status != SharedProvisioningCreated || first.TenantID != sharedCustomerA || first.RealmID == "" || first.MembershipID == "" {
		t.Fatalf("first provisioning = %#v, %v; want created", first, err)
	}
	afterFirst := evidenceCounts()
	if afterFirst[2] != 1 || afterFirst[3] != 1 || afterFirst[0] < 1 || afterFirst[1] != afterFirst[0] {
		t.Fatalf("first run state tenants=%d realms=%d audit=%d outbox=%d", afterFirst[2], afterFirst[3], afterFirst[0], afterFirst[1])
	}

	// A repeat run changes nothing: no row, audit event or outbox event is added.
	repeated, err := ProvisionSharedTenant(ctx, db.operator, inputA)
	if err != nil || repeated.Status != SharedProvisioningUnchanged || repeated.RealmID != first.RealmID || repeated.MembershipID != first.MembershipID {
		t.Fatalf("repeated provisioning = %#v, %v; want unchanged with identical references", repeated, err)
	}
	if got := evidenceCounts(); got != afterFirst {
		t.Fatalf("repeated run changed state: %v -> %v", afterFirst, got)
	}

	// Conflicting input fails closed and writes nothing.
	conflicts := map[string]SharedTenantProvisioningInput{
		"organization owned by another tenant": sharedIntegrationInput(sharedCustomerB, "Customer B", "org-a", "beta"),
		"tenant with another display name":     sharedIntegrationInput(sharedCustomerA, "Renamed customer", "org-a", "alpha", "shared"),
	}
	for name, conflict := range conflicts {
		before := evidenceCounts()
		if result, err := ProvisionSharedTenant(ctx, db.operator, conflict); err == nil || result.Status != "" {
			t.Fatalf("%s = %#v, %v; want a failure", name, result, err)
		}
		if got := evidenceCounts(); got != before {
			t.Fatalf("%s changed state: %v -> %v", name, before, got)
		}
	}

	// A missing namespace is added on a later run; existing ones are untouched.
	extended := inputA
	extended.Namespaces = []string{"alpha", "gamma", "shared"}
	grown, err := ProvisionSharedTenant(ctx, db.operator, extended)
	if err != nil || grown.Status != SharedProvisioningCreated || grown.RealmID != first.RealmID {
		t.Fatalf("namespace extension = %#v, %v; want created in the existing realm", grown, err)
	}
	var namespaceCount int
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM namespaces WHERE tenant_id=$1`, sharedCustomerA).Scan(&namespaceCount); err != nil || namespaceCount != 3 {
		t.Fatalf("customer A namespaces = %d, %v; want 3", namespaceCount, err)
	}

	second, err := ProvisionSharedTenant(ctx, db.operator, inputB)
	if err != nil || second.Status != SharedProvisioningCreated || second.TenantID != sharedCustomerB || second.RealmID == first.RealmID {
		t.Fatalf("customer B provisioning = %#v, %v", second, err)
	}

	// Isolation: the API role resolves each organization to its own tenant and
	// cannot read the other customer's namespaces.
	repository, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		organization, subject, tenant string
	}{
		{"org-a", "admin-org-a", sharedCustomerA},
		{"org-b", "admin-org-b", sharedCustomerB},
	} {
		actor, err := store.ResolvePrincipal(ctx, identity.Principal{Issuer: sharedIssuer, ExternalOrganization: tc.organization, Subject: tc.subject})
		if err != nil || actor.TenantID != tc.tenant {
			t.Fatalf("organization %s resolved to %q, %v; want %s", tc.organization, actor.TenantID, err, tc.tenant)
		}
	}
	if _, err := store.ResolvePrincipal(ctx, identity.Principal{Issuer: sharedIssuer, ExternalOrganization: "org-a", Subject: "admin-org-b"}); err == nil {
		t.Fatal("customer B administrator authenticated inside customer A's organization")
	}
	if _, err := store.ResolvePrincipal(ctx, identity.Principal{Issuer: sharedIssuer, ExternalOrganization: "org-unknown", Subject: "admin-org-a"}); err == nil {
		t.Fatal("an unknown organization was admitted")
	}
	pageA, err := repository.ListNamespaces(ctx, sharedCustomerA, catalog.NamespaceListRequest{Limit: 50})
	if err != nil || len(pageA) != 3 {
		t.Fatalf("customer A namespaces = %d, %v; want 3", len(pageA), err)
	}
	pageB, err := repository.ListNamespaces(ctx, sharedCustomerB, catalog.NamespaceListRequest{Limit: 50})
	if err != nil || len(pageB) != 2 {
		t.Fatalf("customer B namespaces = %d, %v; want 2", len(pageB), err)
	}
	for _, namespace := range pageB {
		if namespace.TenantID != sharedCustomerB {
			t.Fatalf("customer B list returned tenant %s", namespace.TenantID)
		}
		if _, err := repository.GetNamespace(ctx, sharedCustomerA, namespace.ID); err == nil {
			t.Fatalf("customer A read customer B namespace %s", namespace.ID)
		}
	}
	for _, namespace := range pageA {
		if _, err := repository.GetNamespace(ctx, sharedCustomerB, namespace.ID); err == nil {
			t.Fatalf("customer B read customer A namespace %s", namespace.ID)
		}
	}
}

func TestPostgresSharedTenantProvisioningRequiresSchemaOwner(t *testing.T) {
	db := newHousekeepingDatabase(t)
	before := int64(0)
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM tenants`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// The API runtime role does not own the identity tables, so it must be refused.
	if result, err := ProvisionSharedTenant(t.Context(), db.api, sharedIntegrationInput(sharedCustomerA, "Customer A", "org-a", "alpha")); err == nil || result.Status != "" {
		t.Fatalf("API role provisioning = %#v, %v; want a refusal", result, err)
	}
	var after int64
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM tenants`).Scan(&after); err != nil || after != before {
		t.Fatalf("refused provisioning changed tenants: %d -> %d, %v", before, after, err)
	}
}
