package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
)

func TestValidateIdentityProvisioningInput(t *testing.T) {
	valid := IdentityProvisioningInput{
		TenantID:          "00000000-0000-7000-8000-000000000001",
		TenantDisplayName: "Example tenant",
		Issuer:            "https://identity.example.com",
		TenancyMode:       "dedicated",
		Subject:           "initial-admin",
		ActorType:         identity.ActorHuman,
		Roles:             []identity.Role{identity.RoleTenantAdmin},
		Operator:          "release-operator",
		Reason:            "Initial tenant bootstrap",
	}
	if err := ValidateIdentityProvisioningInput(valid); err != nil {
		t.Fatalf("valid dedicated input returned %v", err)
	}
	shared := valid
	shared.TenancyMode = "shared"
	shared.ExternalOrganization = "organization-1"
	if err := ValidateIdentityProvisioningInput(shared); err != nil {
		t.Fatalf("valid shared input returned %v", err)
	}
	boundedUnicode := valid
	boundedUnicode.Reason = "\u2003" + strings.Repeat("\u00e9", 2000) + "\u3000"
	if err := ValidateIdentityProvisioningInput(boundedUnicode); err != nil {
		t.Fatalf("bounded Unicode reason returned %v", err)
	}
	boundedFields := valid
	boundedFields.TenantDisplayName = strings.Repeat("é", 80)
	boundedFields.Issuer = "https://identity.example.com/" + strings.Repeat("a", 2048-len("https://identity.example.com/"))
	boundedFields.Operator = strings.Repeat("é", 256)
	if err := ValidateIdentityProvisioningInput(boundedFields); err != nil {
		t.Fatalf("fields at their byte bounds returned %v", err)
	}

	for name, mutate := range map[string]func(*IdentityProvisioningInput){
		"tenant":                func(input *IdentityProvisioningInput) { input.TenantID = "not-a-uuid" },
		"tenant name":           func(input *IdentityProvisioningInput) { input.TenantDisplayName = "" },
		"long tenant name":      func(input *IdentityProvisioningInput) { input.TenantDisplayName = strings.Repeat("n", 161) },
		"long Unicode name":     func(input *IdentityProvisioningInput) { input.TenantDisplayName = strings.Repeat("é", 80) + "x" },
		"issuer empty":          func(input *IdentityProvisioningInput) { input.Issuer = "" },
		"issuer whitespace":     func(input *IdentityProvisioningInput) { input.Issuer = " https://identity.example.com" },
		"issuer too long":       func(input *IdentityProvisioningInput) { input.Issuer = boundedFields.Issuer + "x" },
		"subject":               func(input *IdentityProvisioningInput) { input.Subject = "" },
		"operator empty":        func(input *IdentityProvisioningInput) { input.Operator = "" },
		"operator whitespace":   func(input *IdentityProvisioningInput) { input.Operator = " operator" },
		"operator too long":     func(input *IdentityProvisioningInput) { input.Operator = boundedFields.Operator + "x" },
		"reason control":        func(input *IdentityProvisioningInput) { input.Reason = "line\nbreak" },
		"reason invalid utf-8":  func(input *IdentityProvisioningInput) { input.Reason = string([]byte{0xff}) },
		"reason too many runes": func(input *IdentityProvisioningInput) { input.Reason = strings.Repeat("\u00e9", 2001) },
		"actor type":            func(input *IdentityProvisioningInput) { input.ActorType = "robot" },
		"roles empty":           func(input *IdentityProvisioningInput) { input.Roles = nil },
		"role invalid":          func(input *IdentityProvisioningInput) { input.Roles = []identity.Role{"owner"} },
		"later role invalid": func(input *IdentityProvisioningInput) {
			input.Roles = []identity.Role{identity.RoleTenantAdmin, "owner"}
		},
		"dedicated organization": func(input *IdentityProvisioningInput) { input.ExternalOrganization = "organization-1" },
		"shared organization": func(input *IdentityProvisioningInput) {
			input.TenancyMode = "shared"
			input.ExternalOrganization = ""
		},
		"tenancy mode": func(input *IdentityProvisioningInput) { input.TenancyMode = "automatic" },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			err := ValidateIdentityProvisioningInput(input)
			if err == nil {
				t.Fatalf("invalid input was accepted: %#v", input)
			}
			// A zero-value pool cannot query. The public command must propagate
			// validation before inspecting database authority or writing evidence.
			got, provisionErr := ProvisionIdentity(t.Context(), &pgxpool.Pool{}, input)
			if provisionErr == nil || provisionErr.Error() != err.Error() || !reflect.DeepEqual(got, IdentityProvisioningResult{}) {
				t.Fatalf("provisioning preflight = %#v, %v; want empty result and %v", got, provisionErr, err)
			}
		})
	}

	if got, err := ProvisionIdentity(t.Context(), nil, valid); err == nil || !strings.Contains(err.Error(), "pool is required") || !reflect.DeepEqual(got, IdentityProvisioningResult{}) {
		t.Fatalf("nil provisioning pool returned %#v, %v", got, err)
	}
}

const (
	provisionTenant     = "00000000-0000-7000-8000-000000000001"
	provisionRealm      = "00000000-0000-7000-8000-000000000010"
	provisionMembership = "00000000-0000-7000-8000-000000000020"
	provisionAudit      = "00000000-0000-7000-8000-000000000030"
	provisionOutbox     = "00000000-0000-7000-8000-000000000040"
)

func identityProvisionTestInput() IdentityProvisioningInput {
	return IdentityProvisioningInput{
		TenantID: provisionTenant, TenantDisplayName: "Example tenant",
		Issuer: "https://identity.example.com", TenancyMode: "dedicated",
		Subject: "initial-admin", ActorType: identity.ActorHuman,
		Roles:    []identity.Role{identity.RoleTenantAdmin, identity.RoleReader, identity.RoleTenantAdmin},
		Operator: "release-operator", Reason: "Provision the initial administrator",
	}
}

// The rows supply new or retained identities; they do not emulate uniqueness,
// conflict resolution or SQL execution. Those contracts have integration tests.
func identityProvisionTestTx(input IdentityProvisioningInput, existing bool, at time.Time) (*identityScriptedTx, IdentityProvisioningResult) {
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	tx.queryRows = append(tx.queryRows, scriptedValueRow([]any{input.TenantDisplayName}))
	if existing {
		tx.queryRows = append(tx.queryRows, scannerFunc(func(...any) error { return pgx.ErrNoRows }))
	}
	tx.queryRows = append(tx.queryRows,
		scriptedValueRow([]any{provisionRealm}),
		scriptedValueRow([]any{provisionMembership, input.ActorType, identity.MembershipActive}),
	)
	want := IdentityProvisioningResult{
		TenantID: provisionTenant, RealmID: provisionRealm, MembershipID: provisionMembership,
		Subject: input.Subject, ActorType: input.ActorType,
	}
	for index, role := range []identity.Role{identity.RoleReader, identity.RoleTenantAdmin} {
		assignment := identity.RoleAssignment{
			ID:       fmt.Sprintf("00000000-0000-7000-8000-%012d", 50+index),
			TenantID: provisionTenant, MembershipID: provisionMembership,
			Subject: input.Subject, ActorType: input.ActorType, Role: role,
			State: identity.AssignmentActive, Revision: 1,
			GrantedBy: input.Operator, GrantReason: input.Reason, GrantedAt: at,
		}
		if existing {
			assignment.GrantedBy = "original-operator"
			assignment.GrantReason = "Original bootstrap"
			assignment.GrantedAt = at.Add(-24 * time.Hour)
		}
		want.Assignments = append(want.Assignments, assignment)
		tx.queryRows = append(tx.queryRows, scriptedValueRow([]any{
			assignment.ID, assignment.TenantID, assignment.MembershipID, assignment.Role,
			assignment.State, assignment.Revision, assignment.GrantedBy, assignment.GrantReason, assignment.GrantedAt,
		}))
	}
	tx.queryRows = append(tx.queryRows,
		scriptedValueRow([]any{at}),
		scriptedValueRow([]any{provisionAudit, "catalog_owner", "catalog_owner"}),
		scriptedValueRow([]any{provisionOutbox}),
	)
	return tx, want
}

func TestProvisionIdentityTxBindingsAndEvidence(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                          string
		existing, shared, acknowledge bool
	}{
		{name: "new dedicated human"},
		{name: "existing dedicated human", existing: true, acknowledge: true},
		{name: "new shared service", shared: true, acknowledge: true},
		{name: "existing shared service", existing: true, shared: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := identityProvisionTestInput()
			input.AcknowledgeOutbox = tt.acknowledge
			if tt.shared {
				input.TenancyMode, input.ExternalOrganization = "shared", "organization-1"
				input.Subject, input.ActorType = "bootstrap-service", identity.ActorService
			}
			rolesBefore := slices.Clone(input.Roles)
			at := time.Date(2026, time.September, 20, 12, 0, 0, 123000, time.UTC)
			tx, want := identityProvisionTestTx(input, tt.existing, at)
			got, err := provisionIdentityTx(t.Context(), tx, input)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("provisioned identity = %#v, %v; want %#v", got, err, want)
			}
			if !slices.Equal(input.Roles, rolesBefore) {
				t.Fatal("provisioning changed caller-owned roles")
			}
			if len(tx.queryRows) != 0 || len(tx.statements) != 5 || len(tx.queryStatements) != 0 || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatal("provisioning skipped expected work, made extra calls or took transaction ownership")
			}
			if !strings.Contains(tx.statements[0], "INSERT INTO tenants") || !reflect.DeepEqual(tx.arguments[0], []any{provisionTenant, input.TenantDisplayName}) {
				t.Fatalf("tenant binding = %v", tx.arguments[0])
			}
			mapping := []any{provisionTenant, input.Issuer, input.TenancyMode, input.ExternalOrganization}
			wantReads := [][]any{{provisionTenant}, mapping}
			if tt.existing {
				wantReads = append(wantReads, mapping)
			}
			wantReads = append(wantReads, []any{provisionTenant, provisionRealm, input.Subject, input.ActorType})
			for _, role := range []identity.Role{identity.RoleReader, identity.RoleTenantAdmin} {
				wantReads = append(wantReads, []any{provisionTenant, provisionMembership, role, input.Operator, input.Reason})
			}
			wantReads = append(wantReads, nil, nil, nil) // Database time, attribution and outbox ID.
			if !reflect.DeepEqual(tx.queryRowArguments, wantReads) {
				t.Fatalf("identity query bindings = %#v; want %#v", tx.queryRowArguments, wantReads)
			}

			auditArgs := tx.arguments[3]
			if !strings.Contains(tx.statements[3], "INSERT INTO audit_events") || len(auditArgs) != 13 {
				t.Fatalf("audit write = %s, %#v", tx.statements[3], auditArgs)
			}
			var details audit.Details
			detailsJSON, ok := auditArgs[11].([]byte)
			if !ok {
				t.Fatalf("audit details have type %T; want encoded JSON bytes", auditArgs[11])
			}
			if err := json.Unmarshal(detailsJSON, &details); err != nil {
				t.Fatal(err)
			}
			wantDetails := audit.Details{
				PrincipalType: audit.PrincipalDatabaseLogin, CurrentUser: "catalog_owner", OperatorLabel: input.Operator,
				Facts: map[string]any{
					"realmId": provisionRealm, "tenancyMode": input.TenancyMode, "roleCount": float64(2),
					"rolesDigest": audit.DigestBytes([]byte(`["reader","tenant_admin"]`)),
				},
			}
			wantAudit := []any{new(audit.RecordFormatV1), provisionAudit, provisionTenant, "catalog_owner", "identity.membership_provisioned", "tenant_membership", provisionMembership, input.Reason, "", "", "", auditArgs[11], at}
			if !reflect.DeepEqual(details, wantDetails) || !reflect.DeepEqual(auditArgs, wantAudit) {
				t.Fatalf("provisioning audit = %#v, details=%#v", auditArgs, details)
			}

			outboxArgs := tx.arguments[4]
			if !strings.Contains(tx.statements[4], "INSERT INTO outbox_events") || len(outboxArgs) != 9 {
				t.Fatalf("outbox write = %s, %#v", tx.statements[4], outboxArgs)
			}
			var reference map[string]any
			referenceJSON, ok := outboxArgs[4].(json.RawMessage)
			if !ok {
				t.Fatalf("outbox reference has type %T; want json.RawMessage", outboxArgs[4])
			}
			if err := json.Unmarshal(referenceJSON, &reference); err != nil {
				t.Fatal(err)
			}
			wantReference := map[string]any{"action": "identity.membership.provisioned", "targetType": "tenant_membership", "targetId": provisionMembership}
			var publishedAt *time.Time
			if tt.acknowledge {
				publishedAt = &at
			}
			wantOutbox := []any{provisionOutbox, provisionTenant, "com.open.agentic.catalog.identity.membership.provisioned.v1", provisionMembership, outboxArgs[4], false, at, publishedAt, catalog.TraceContext("")}
			if !reflect.DeepEqual(reference, wantReference) || !reflect.DeepEqual(outboxArgs, wantOutbox) {
				t.Fatalf("provisioning outbox = %#v, reference=%#v", outboxArgs, reference)
			}
		})
	}
}

func TestProvisionIdentityTxStopsOnFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{
		"tenant insert", "tenant read", "tenant name conflict", "realm insert", "realm lookup", "realm mapping conflict",
		"membership write", "membership type conflict", "suspended membership", "offboarded membership",
		"first role", "later role", "audit clock", "audit attribution", "outbox identifier", "audit insert", "outbox insert",
	} {
		t.Run(mode, func(t *testing.T) {
			input := identityProvisionTestInput()
			rolesBefore := slices.Clone(input.Roles)
			existing := mode == "realm lookup" || mode == "realm mapping conflict"
			tx, _ := identityProvisionTestTx(input, existing, time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC))
			failure := errors.New("injected provisioning failure")
			failedRow := scannerFunc(func(...any) error { return failure })
			wantReads, wantWrites := 0, 1
			wantError := ""
			switch mode {
			case "tenant insert":
				tx.execErrors = []error{failure}
			case "tenant read":
				tx.queryRows[0], wantReads = failedRow, 1
			case "tenant name conflict":
				tx.queryRows[0], wantReads = scriptedValueRow([]any{"Another tenant name"}), 1
				wantError = "tenant already exists with another display name"
			case "realm insert":
				tx.queryRows[1], wantReads = failedRow, 2
			case "realm lookup":
				tx.queryRows[2], wantReads = failedRow, 3
			case "realm mapping conflict":
				tx.queryRows[2], wantReads = scannerFunc(func(...any) error { return pgx.ErrNoRows }), 3
				wantError = "identity realm mapping already belongs to another tenant or tenancy mode"
			case "membership write":
				tx.queryRows[2], wantReads = failedRow, 3
			case "membership type conflict", "suspended membership", "offboarded membership":
				actorType, state := input.ActorType, identity.MembershipActive
				switch mode {
				case "membership type conflict":
					actorType = identity.ActorService
				case "suspended membership":
					state = identity.MembershipSuspended
				case "offboarded membership":
					state = identity.MembershipOffboarded
				}
				tx.queryRows[2], wantReads = scriptedValueRow([]any{provisionMembership, actorType, state}), 3
				wantError = "membership already exists with another actor type or state"
			case "first role":
				tx.queryRows[3], wantReads = failedRow, 4
			case "later role":
				tx.queryRows[4], wantReads = failedRow, 5
			case "audit clock":
				tx.queryRows[5], wantReads = failedRow, 6
			case "audit attribution":
				tx.queryRows[6], wantReads = failedRow, 7
			case "outbox identifier":
				tx.queryRows[7], wantReads = failedRow, 8
			case "audit insert":
				tx.execErrors = []error{nil, nil, nil, failure}
				wantReads, wantWrites = 8, 4
			case "outbox insert":
				tx.execErrors = []error{nil, nil, nil, nil, failure}
				wantReads, wantWrites = 8, 5
			}
			if mode == "audit clock" || mode == "audit attribution" || mode == "outbox identifier" {
				wantWrites = 3
			}
			got, err := provisionIdentityTx(t.Context(), tx, input)
			if err == nil || !reflect.DeepEqual(got, IdentityProvisioningResult{}) {
				t.Fatalf("failed provisioning = %#v, %v; want no result and an error", got, err)
			}
			if wantError != "" && err.Error() != wantError || wantError == "" && !errors.Is(err, failure) {
				t.Fatalf("provisioning failure = %v; lost the conflict or injected cause", err)
			}
			if len(tx.queryRowStatements) != wantReads || len(tx.statements) != wantWrites || len(tx.queryStatements) != 0 {
				t.Fatalf("failure did not stop work: reads=%d, writes=%d; want %d, %d", len(tx.queryRowStatements), len(tx.statements), wantReads, wantWrites)
			}
			if !slices.Equal(input.Roles, rolesBefore) || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatal("failed provisioning changed caller roles or took transaction ownership")
			}
		})
	}
}

func TestExactDevelopmentProvisioningAdmission(t *testing.T) {
	valid := identityProvisionTestInput()
	valid.Roles = nil
	valid.RoleTemplates = []string{"webhook_admin"}
	valid.Issuer = authn.DevelopmentIssuer
	valid.Operator = "catalogctl-development-bootstrap"
	if err := ValidateIdentityProvisioningInput(valid); err != nil {
		t.Fatal(err)
	}
	t.Run("complete roster", func(t *testing.T) {
		input := valid
		input.ActorType = identity.ActorService
		input.RoleTemplates = []string{"reader", "contributor", "reviewer", "publisher", "discovery_reader", "publisher_creator", "publication_admin", "source_admin", "security_reviewer", "webhook_admin", "feedback_member", "library_member", "tenant_admin", "service_client"}
		if err := ValidateIdentityProvisioningInput(input); err != nil {
			t.Fatal("complete current roster rejected", err)
		}
	})
	for name, change := range map[string]func(*IdentityProvisioningInput){
		"production issuer": func(i *IdentityProvisioningInput) { i.Issuer = "https://production.example" },
		"ordinary operator": func(i *IdentityProvisioningInput) { i.Operator = "release-operator" },
		"mixed bundles":     func(i *IdentityProvisioningInput) { i.Roles = []identity.Role{identity.RoleReader} },
		"unknown":           func(i *IdentityProvisioningInput) { i.RoleTemplates = []string{"unknown"} },
		"duplicate":         func(i *IdentityProvisioningInput) { i.RoleTemplates = []string{"reader", "reader"} },
		"overflow":          func(i *IdentityProvisioningInput) { i.RoleTemplates = make([]string, 15) },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			change(&input)
			if err := ValidateIdentityProvisioningInput(input); err == nil {
				t.Fatal("invalid exact provisioning accepted")
			}
		})
	}
}

func TestExactDevelopmentProvisioningRequiresRetainedAssignment(t *testing.T) {
	for _, tt := range []struct {
		name          string
		lookupError   error
		insertFailure bool
	}{
		{name: "retained exact assignment"}, {name: "missing template", lookupError: pgx.ErrNoRows}, {name: "lookup failure", lookupError: errors.New("lookup failed")}, {name: "insert failure", insertFailure: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := identityProvisionTestInput()
			input.Roles = nil
			input.RoleTemplates = []string{"webhook_admin"}
			input.Issuer = authn.DevelopmentIssuer
			input.Operator = "catalogctl-development-bootstrap"
			at := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
			tx, _ := identityProvisionTestTx(input, false, at)
			retained := identity.AccessAssignment{ID: provisionOutbox, TenantID: input.TenantID, RealmID: provisionRealm, RoleDefinitionID: provisionAudit, MembershipID: provisionMembership, State: identity.AssignmentActive, Revision: 1, GrantedBy: input.Operator, GrantReason: input.Reason, GrantedAt: at}
			var row pgx.Row = scriptedValueRow([]any{retained.ID, retained.TenantID, retained.RealmID, retained.RoleDefinitionID, retained.MembershipID, "", "", retained.State, retained.Revision, retained.GrantedBy, retained.GrantReason, at, "", "", (*time.Time)(nil)})
			if tt.lookupError != nil {
				row = scannerFunc(func(...any) error { return tt.lookupError })
			}
			tx.queryRows = append(append(tx.queryRows[:3:3], row), tx.queryRows[5:]...)
			if tt.insertFailure {
				tx.execErrors = []error{nil, errors.New("exact assignment insertion failed")}
			}
			got, err := provisionIdentityTx(t.Context(), tx, input)
			if tt.insertFailure || tt.lookupError != nil {
				if err == nil || !reflect.DeepEqual(got, IdentityProvisioningResult{}) {
					t.Fatalf("failed exact provisioning %#v %v", got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got.TemplateAssignments, []identity.AccessAssignment{retained}) || len(got.Assignments) != 0 {
				t.Fatalf("exact result %#v %v", got, err)
			}
		})
	}
}

func TestBootstrapCompanionsAreExplicitAndStopBeforeEvidenceOnFailure(t *testing.T) {
	for _, test := range []struct {
		role identity.Role
		want []string
	}{
		{identity.RoleContributor, []string{"discovery_reader", "feedback_member", "publisher_creator"}},
		{identity.RolePublisher, []string{"discovery_reader", "feedback_member", "publication_admin"}},
	} {
		for _, fail := range []bool{false, true} {
			t.Run(string(test.role)+map[bool]string{false: "/complete", true: "/companion failure"}[fail], func(t *testing.T) {
				input := identityProvisionTestInput()
				input.Roles = []identity.Role{test.role}
				at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
				tx, fixture := identityProvisionTestTx(input, false, at)
				assignment := fixture.Assignments[0]
				assignment.Role = test.role
				row := scriptedValueRow([]any{assignment.ID, assignment.TenantID, assignment.MembershipID, assignment.Role, assignment.State, assignment.Revision, assignment.GrantedBy, assignment.GrantReason, assignment.GrantedAt})
				tx.queryRows = append(append(tx.queryRows[:3:3], row), tx.queryRows[5:]...)
				failure := errors.New("companion grant failed")
				if fail {
					tx.execErrors = []error{nil, failure}
				}
				got, err := provisionIdentityTx(t.Context(), tx, input)
				if fail {
					if !errors.Is(err, failure) || !reflect.DeepEqual(got, IdentityProvisioningResult{}) || len(tx.statements) != 2 || len(tx.queryRowStatements) != 4 {
						t.Fatal("failed companion grant produced successful output or audit", err)
					}
					return
				}
				if err != nil || !reflect.DeepEqual(got.Assignments, []identity.RoleAssignment{assignment}) {
					t.Fatalf("bootstrap assignments=%+v error=%v", got.Assignments, err)
				}
				if len(tx.statements) != len(test.want)+3 {
					t.Fatal("unexpected companion or evidence writes")
				}
				for i, key := range test.want {
					want := []any{input.TenantID, provisionMembership, provisionRealm, input.Operator, input.Reason, key}
					if !strings.Contains(tx.statements[i+1], "INSERT INTO role_assignments") || !reflect.DeepEqual(tx.arguments[i+1], want) {
						t.Fatalf("companion %d binding=%v; want %v", i, tx.arguments[i+1], want)
					}
				}
				if tx.commits != 0 || tx.rollbacks != 0 {
					t.Fatal("bootstrap helper took transaction ownership")
				}
			})
		}
	}
}
