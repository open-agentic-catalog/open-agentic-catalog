// Scoped provisioning units use scripted transactions, never PostgreSQL.
package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
)

func TestProvisionedScopeReferencesAndAuditUseTheSameBinding(t *testing.T) {
	input := identityProvisionTestInput()
	input.Issuer, input.Operator = authn.DevelopmentIssuer, "catalogctl-development-bootstrap"
	input.Namespaces = map[string][]identity.Role{originRealm: {identity.RoleReader}}
	if err := ValidateIdentityProvisioningInput(input); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1, 0).UTC()
	tx, _ := identityProvisionTestTx(input, false, at)
	suffix := append([]pgx.Row(nil), tx.queryRows[5:]...)
	tx.queryRows = append(tx.queryRows[:5], scriptedValueRow([]any{true}), scriptedValueRow([]any{originAuthorMember, input.TenantID, provisionMembership, identity.RoleReader, identity.AssignmentActive, int64(1), input.Operator, input.Reason, at}))
	tx.queryRows = append(tx.queryRows, suffix...)
	result, err := provisionIdentityTx(t.Context(), tx, input)
	if err != nil || len(result.Assignments) != 3 || result.Assignments[2].NamespaceID != originRealm {
		t.Fatal("owner result lost scoped reference", result, err)
	}
	var details audit.Details
	for i, statement := range tx.statements {
		if strings.Contains(statement, "INSERT INTO audit_events") {
			raw, ok := tx.arguments[i][11].([]byte)
			if !ok {
				t.Fatalf("audit details argument has type %T; want encoded bytes", tx.arguments[i][11])
			}
			if err := json.Unmarshal(raw, &details); err != nil {
				t.Fatal(err)
			}
		}
	}
	if details.Facts["namespaceGrantCount"] != float64(1) || details.Facts["namespaceGrantsDigest"] != audit.DigestBytes([]byte(`["`+originRealm+`:reader"]`)) {
		t.Fatal("audit does not bind returned scoped grants", details)
	}
}

func TestNamespaceProvisioningValidationAndFailureHandoffs(t *testing.T) {
	input := identityProvisionTestInput()
	input.Issuer, input.Operator, input.Roles = authn.DevelopmentIssuer, "catalogctl-development-bootstrap", nil
	input.Namespaces = map[string][]identity.Role{originRealm: {identity.RoleReader, identity.RoleReader}}
	if err := ValidateIdentityProvisioningInput(input); err != nil {
		t.Fatal("scoped-only preflight failed", err)
	}
	for _, mode := range []string{"issuer", "operator", "namespace", "empty roles", "unsupported role", "shared mapping"} {
		bad := input
		switch mode {
		case "issuer":
			bad.Issuer = "https://production.example"
		case "operator":
			bad.Operator = "ordinary-operator"
		case "namespace":
			bad.Namespaces = map[string][]identity.Role{"invalid": {identity.RoleReader}}
		case "empty roles":
			bad.Namespaces = map[string][]identity.Role{originRealm: {}}
		case "unsupported role":
			bad.Namespaces = map[string][]identity.Role{originRealm: {identity.RoleTenantAdmin}}
		case "shared mapping":
			bad.TenancyMode = "shared"
		}
		if got, err := ProvisionIdentity(t.Context(), nil, bad); err == nil || !reflect.DeepEqual(got, IdentityProvisioningResult{}) {
			t.Fatal("invalid scope reached owner boundary", mode, got, err)
		}
	}
	for _, stage := range []string{"valid", "namespace query", "missing namespace", "namespace membership", "assignment"} {
		t.Run(stage, func(t *testing.T) {
			sentinel := errors.New("owned scoped provision failure")
			row := scriptedValueRow([]any{true})
			if stage == "namespace query" {
				row = scannerFunc(func(...any) error { return sentinel })
			}
			if stage == "missing namespace" {
				row = scriptedValueRow([]any{false})
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{row}}
			at := time.Unix(1, 0).UTC()
			assignment := scriptedValueRow([]any{originAuthorMember, input.TenantID, provisionMembership, identity.RoleReader, identity.AssignmentActive, int64(1), input.Operator, input.Reason, at})
			if stage == "assignment" {
				assignment = scannerFunc(func(...any) error { return sentinel })
			}
			tx.queryRows = append(tx.queryRows, assignment)
			if stage == "namespace membership" {
				tx.execErrors = []error{sentinel}
			}
			got, err := provisionNamespaceAssignments(t.Context(), tx, input, provisionMembership)
			if stage != "valid" {
				if err == nil || got != nil || stage != "missing namespace" && !errors.Is(err, sentinel) {
					t.Fatal("failed scope returned partial success or lost cause", got, err)
				}
				return
			}
			if err != nil || len(got) != 1 || got[0].NamespaceID != originRealm || got[0].Role != identity.RoleReader || len(tx.statements) != 1 || len(tx.queryRowStatements) != 2 {
				t.Fatal("scope was duplicated, dropped or widened", got, err)
			}
			if !reflect.DeepEqual(tx.queryRowArguments[0], []any{input.TenantID, originRealm}) || !reflect.DeepEqual(tx.queryRowArguments[1], []any{input.TenantID, provisionMembership, originRealm, identity.RoleReader, input.Operator, input.Reason}) {
				t.Fatal("scoped SQL lost tenant/namespace/principal binding", tx.queryRowArguments)
			}
		})
	}
}

func TestNamespaceProvisioningLaterFailureReturnsNoPartialReferences(t *testing.T) {
	const later = "00000000-0000-7000-8000-000000000999"
	input := identityProvisionTestInput()
	input.Issuer, input.Operator, input.Roles = authn.DevelopmentIssuer, "catalogctl-development-bootstrap", nil
	input.Namespaces = map[string][]identity.Role{originRealm: {identity.RoleReader}, later: {identity.RoleReviewer}}
	at := time.Unix(1, 0).UTC()
	for _, stage := range []string{"valid", "namespace query", "missing namespace", "namespace membership", "assignment"} {
		t.Run(stage, func(t *testing.T) {
			sentinel := errors.New("later scoped provision failure")
			assignment := func(role identity.Role) pgx.Row {
				return scriptedValueRow([]any{originAuthorMember, input.TenantID, provisionMembership, role, identity.AssignmentActive, int64(1), input.Operator, input.Reason, at})
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{true}), assignment(identity.RoleReader), scriptedValueRow([]any{true}), assignment(identity.RoleReviewer)}}
			switch stage {
			case "namespace query":
				tx.queryRows[2] = scannerFunc(func(...any) error { return sentinel })
			case "missing namespace":
				tx.queryRows[2] = scriptedValueRow([]any{false})
			case "namespace membership":
				tx.execErrors = []error{nil, sentinel}
			case "assignment":
				tx.queryRows[3] = scannerFunc(func(...any) error { return sentinel })
			}
			got, err := provisionNamespaceAssignments(t.Context(), tx, input, provisionMembership)
			if stage == "valid" {
				if err != nil || len(got) != 2 || got[0].NamespaceID != originRealm || got[0].Role != identity.RoleReader || got[1].NamespaceID != later || got[1].Role != identity.RoleReviewer {
					t.Fatal("valid multi-namespace control failed", got, err)
				}
			} else if err == nil || got != nil || stage != "missing namespace" && !errors.Is(err, sentinel) {
				t.Fatal("later failure disclosed partial grants or lost its cause", got, err)
			}
		})
	}
}
