package database

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const (
	sharedNamespaceOne = "00000000-0000-7000-8000-000000000061"
)

func sharedProvisionTestInput() SharedTenantProvisioningInput {
	return SharedTenantProvisioningInput{
		TenantID: provisionTenant, TenantDisplayName: "Global",
		Issuer: "https://identity.example.com/realms/shared", ExternalOrganization: "organization-1",
		Subject: "initial-admin", Namespaces: []string{"alpha", "beta"},
		Operator: "release-operator", Reason: "Provision the shared tenant",
	}
}

func TestValidateSharedTenantProvisioningInput(t *testing.T) {
	t.Parallel()
	if err := ValidateSharedTenantProvisioningInput(sharedProvisionTestInput()); err != nil {
		t.Fatalf("valid shared input rejected: %v", err)
	}
	tooMany := make([]string, MaxSharedProvisioningNamespaces+1)
	for index := range tooMany {
		tooMany[index] = "ns" + strings.Repeat("a", index%5)
	}
	for name, mutate := range map[string]func(*SharedTenantProvisioningInput){
		"no organization":    func(i *SharedTenantProvisioningInput) { i.ExternalOrganization = "" },
		"no namespaces":      func(i *SharedTenantProvisioningInput) { i.Namespaces = nil },
		"too many":           func(i *SharedTenantProvisioningInput) { i.Namespaces = tooMany },
		"uppercase slug":     func(i *SharedTenantProvisioningInput) { i.Namespaces = []string{"Alpha"} },
		"leading hyphen":     func(i *SharedTenantProvisioningInput) { i.Namespaces = []string{"-alpha"} },
		"empty slug":         func(i *SharedTenantProvisioningInput) { i.Namespaces = []string{"alpha", ""} },
		"bad tenant":         func(i *SharedTenantProvisioningInput) { i.TenantID = "tenant" },
		"no tenant name":     func(i *SharedTenantProvisioningInput) { i.TenantDisplayName = "" },
		"no issuer":          func(i *SharedTenantProvisioningInput) { i.Issuer = "" },
		"no subject":         func(i *SharedTenantProvisioningInput) { i.Subject = "" },
		"no operator":        func(i *SharedTenantProvisioningInput) { i.Operator = "" },
		"no reason":          func(i *SharedTenantProvisioningInput) { i.Reason = " " },
		"padded operator":    func(i *SharedTenantProvisioningInput) { i.Operator = " operator" },
		"padded issuer":      func(i *SharedTenantProvisioningInput) { i.Issuer = "https://identity.example.com " },
		"overlong namespace": func(i *SharedTenantProvisioningInput) { i.Namespaces = []string{strings.Repeat("a", 65)} },
	} {
		t.Run(name, func(t *testing.T) {
			input := sharedProvisionTestInput()
			mutate(&input)
			if err := ValidateSharedTenantProvisioningInput(input); err == nil {
				t.Fatalf("input %#v was accepted", input)
			}
		})
	}
	if got := sharedProvisionTestInput().identityInput(); got.TenancyMode != "shared" || got.ActorType != identity.ActorHuman || !reflect.DeepEqual(got.Roles, []identity.Role{identity.RoleTenantAdmin}) || len(got.Namespaces) != 0 {
		t.Fatalf("identity input = %#v; want one shared human tenant administrator without namespace seeds", got)
	}
}

func TestProvisionSharedTenantTxReportsCompleteStateUnchangedWithoutWrites(t *testing.T) {
	t.Parallel()
	input := sharedProvisionTestInput()
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	tx.queryRows = []pgx.Row{
		scriptedValueRow([]any{provisionRealm, provisionMembership}),
		scriptedValueRow([]any{true}),
	}
	got, err := provisionSharedTenantTx(t.Context(), tx, input)
	want := SharedTenantProvisioningResult{Status: SharedProvisioningUnchanged, TenantID: provisionTenant, RealmID: provisionRealm, MembershipID: provisionMembership, Subject: input.Subject, Namespaces: input.Namespaces}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("complete state = %#v, %v; want %#v", got, err, want)
	}
	// The only statement is the advisory lock: no tenant, realm, evidence or namespace write.
	if len(tx.statements) != 1 || !strings.Contains(tx.statements[0], "pg_advisory_xact_lock") || len(tx.queryRows) != 0 {
		t.Fatalf("unchanged run made writes or skipped checks: %v", tx.statements)
	}
	if tx.commits != 0 || tx.rollbacks != 0 {
		t.Fatal("transaction helper took transaction ownership")
	}
}

func TestProvisionSharedTenantTxTreatsPartialStateAsWork(t *testing.T) {
	t.Parallel()
	input := sharedProvisionTestInput()
	input.Namespaces = []string{"alpha"}
	at := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	noRows := func(...any) error { return pgx.ErrNoRows }
	// Realm and membership exist but the completeness check is false, so the run repairs the gap.
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	tx.queryRows = []pgx.Row{
		scriptedValueRow([]any{provisionRealm, provisionMembership}),
		scriptedValueRow([]any{false}),
		scriptedValueRow([]any{input.TenantDisplayName}),
		scannerFunc(noRows), // Realm insert conflicts, then the exact lookup finds it.
		scriptedValueRow([]any{provisionRealm}),
		scriptedValueRow([]any{provisionMembership, identity.ActorHuman, identity.MembershipActive}),
		scriptedValueRow([]any{"00000000-0000-7000-8000-000000000050", provisionTenant, provisionMembership, identity.RoleTenantAdmin, identity.AssignmentActive, int64(1), "original-operator", "Original bootstrap", at.Add(-24 * time.Hour)}),
		scriptedValueRow([]any{at}),
		scriptedValueRow([]any{provisionAudit, "catalog_owner", "catalog_owner"}),
		scriptedValueRow([]any{provisionOutbox}),
		scannerFunc(noRows), // Namespace alpha already exists: no namespace evidence.
	}
	got, err := provisionSharedTenantTx(t.Context(), tx, input)
	if err != nil || got.Status != SharedProvisioningCreated || len(tx.queryRows) != 0 {
		t.Fatalf("partial state = %#v, %v (unread rows %d); want created", got, err, len(tx.queryRows))
	}
	// Lock, tenant, identity audit and identity outbox only; the existing namespace adds nothing.
	if len(tx.statements) != 4 {
		t.Fatalf("writes = %d (%v); want 4", len(tx.statements), tx.statements)
	}
}

func TestProvisionSharedTenantTxCreatesStateAndNamespaceEvidence(t *testing.T) {
	t.Parallel()
	input := sharedProvisionTestInput()
	input.Namespaces = []string{"alpha", "beta"}
	at := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	noRows := func(...any) error { return pgx.ErrNoRows }
	tx.queryRows = []pgx.Row{
		scannerFunc(noRows), // Nothing exists yet.
		scriptedValueRow([]any{input.TenantDisplayName}),
		scriptedValueRow([]any{provisionRealm}),
		scriptedValueRow([]any{provisionMembership, identity.ActorHuman, identity.MembershipActive}),
		scriptedValueRow([]any{"00000000-0000-7000-8000-000000000050", provisionTenant, provisionMembership, identity.RoleTenantAdmin, identity.AssignmentActive, int64(1), input.Operator, input.Reason, at}),
		scriptedValueRow([]any{at}),
		scriptedValueRow([]any{provisionAudit, "catalog_owner", "catalog_owner"}),
		scriptedValueRow([]any{provisionOutbox}),
		// Namespace alpha is new; namespace beta already exists.
		scriptedValueRow([]any{sharedNamespaceOne}),
		scriptedValueRow([]any{at}),
		scriptedValueRow([]any{"00000000-0000-7000-8000-000000000031", "catalog_owner", "catalog_owner"}),
		scriptedValueRow([]any{"00000000-0000-7000-8000-000000000041"}),
		scannerFunc(noRows),
	}
	got, err := provisionSharedTenantTx(t.Context(), tx, input)
	if err != nil {
		t.Fatal(err)
	}
	want := SharedTenantProvisioningResult{Status: SharedProvisioningCreated, TenantID: provisionTenant, RealmID: provisionRealm, MembershipID: provisionMembership, Subject: input.Subject, Namespaces: input.Namespaces}
	if !reflect.DeepEqual(got, want) || len(tx.queryRows) != 0 {
		t.Fatalf("created result = %#v; want %#v (unread rows %d)", got, want, len(tx.queryRows))
	}
	// Lock, tenant, identity audit, identity outbox, namespace audit and namespace outbox.
	if len(tx.statements) != 6 {
		t.Fatalf("writes = %d (%v); want 6", len(tx.statements), tx.statements)
	}
	if !strings.Contains(tx.statements[4], "INSERT INTO audit_events") || tx.arguments[4][4] != "namespace.created" || tx.arguments[4][6] != sharedNamespaceOne {
		t.Fatalf("namespace audit = %v", tx.arguments[4])
	}
	if !strings.Contains(tx.statements[5], "INSERT INTO outbox_events") {
		t.Fatalf("namespace outbox = %s", tx.statements[5])
	}
}

func TestProvisionSharedTenantTxFailsClosedOnConflict(t *testing.T) {
	t.Parallel()
	input := sharedProvisionTestInput()
	noRows := func(...any) error { return pgx.ErrNoRows }
	for name, tc := range map[string]struct {
		rows []pgx.Row
		want string
	}{
		"tenant name conflict": {
			rows: []pgx.Row{scannerFunc(noRows), scriptedValueRow([]any{"Another tenant name"})},
			want: "tenant already exists with another display name",
		},
		"realm mapping belongs elsewhere": {
			rows: []pgx.Row{scannerFunc(noRows), scriptedValueRow([]any{input.TenantDisplayName}), scannerFunc(noRows), scannerFunc(noRows)},
			want: "identity realm mapping already belongs to another tenant or tenancy mode",
		},
		"subject has another actor type": {
			rows: []pgx.Row{scannerFunc(noRows), scriptedValueRow([]any{input.TenantDisplayName}), scriptedValueRow([]any{provisionRealm}), scriptedValueRow([]any{provisionMembership, identity.ActorService, identity.MembershipActive})},
			want: "membership already exists with another actor type or state",
		},
		"subject is suspended": {
			rows: []pgx.Row{scannerFunc(noRows), scriptedValueRow([]any{input.TenantDisplayName}), scriptedValueRow([]any{provisionRealm}), scriptedValueRow([]any{provisionMembership, identity.ActorHuman, identity.MembershipSuspended})},
			want: "membership already exists with another actor type or state",
		},
	} {
		t.Run(name, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: tc.rows}
			got, err := provisionSharedTenantTx(t.Context(), tx, input)
			if err == nil || err.Error() != tc.want || !reflect.DeepEqual(got, SharedTenantProvisioningResult{}) {
				t.Fatalf("conflict = %#v, %v; want error %q and no result", got, err, tc.want)
			}
			for _, statement := range tx.statements {
				if strings.Contains(statement, "audit_events") || strings.Contains(statement, "outbox_events") {
					t.Fatalf("conflict wrote evidence: %s", statement)
				}
			}
		})
	}
}

func TestProvisionSharedTenantRejectsInvalidInputBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	input := sharedProvisionTestInput()
	input.Namespaces = nil
	if got, err := ProvisionSharedTenant(t.Context(), nil, input); err == nil || got.Status != "" {
		t.Fatalf("invalid input = %#v, %v; want a validation error", got, err)
	}
	if got, err := ProvisionSharedTenant(t.Context(), nil, sharedProvisionTestInput()); err == nil || got.Status != "" {
		t.Fatalf("nil pool = %#v, %v; want an error", got, err)
	}
}
