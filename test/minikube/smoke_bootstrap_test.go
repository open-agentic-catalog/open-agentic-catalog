//go:build minikube

package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestSmokeBootstrapPolicy(t *testing.T) {
	for _, fault := range []string{"", "cursor", "missing", "foreign-definition", "custom-definition", "inactive-definition", "duplicate-definition", "grant-member", "grant-subject", "companion-member", "companion-scope", "companion-group", "companion-role", "companion-etag", "companion-state"} {
		t.Run(fault, func(t *testing.T) { testSmokeBootstrapPolicy(t, fault) })
	}
}

func testSmokeBootstrapPolicy(t *testing.T, fault string) {
	t.Helper()
	grants := map[string][]string{}
	definitions := []identity.RoleDefinition{}
	for _, key := range []string{"discovery_reader", "publisher_creator", "publication_admin"} {
		definitions = append(definitions, identity.RoleDefinition{ID: key, TenantID: primaryTenantID, Key: key, Builtin: true, State: identity.DefinitionActive})
	}
	switch fault {
	case "missing":
		definitions = definitions[:2]
	case "foreign-definition":
		definitions[0].TenantID = otherTenantID
	case "custom-definition":
		definitions[0].Builtin = false
	case "inactive-definition":
		definitions[0].State = identity.DefinitionArchived
	case "duplicate-definition":
		definitions = append(definitions, definitions[0])
	}
	respond := func(value any) httpResult {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return httpResult{Status: 201, Header: http.Header{"Etag": {`"rev-1"`}}, Body: body}
	}
	err := bootstrapSmokePolicy(func(_ int, method, path, key, match string, body any) (httpResult, error) {
		if method == "GET" && path == "/api/v1/access/roles?limit=100" {
			page := map[string]any{"items": definitions}
			if fault == "cursor" {
				page["nextCursor"] = "unread"
			}
			result := respond(page)
			result.Status = 200
			return result, nil
		}
		if method == "POST" && path == "/api/v1/role-assignments" {
			value, ok := body.(map[string]any)
			if !ok {
				t.Fatal("workflow assignment body is not an object")
			}
			subject, subjectOK := value["subject"].(string)
			role, roleOK := value["role"].(string)
			if !subjectOK || !roleOK {
				t.Fatal("workflow assignment lacks string identity or role")
			}
			grants[subject] = append(grants[subject], role)
			assignment := identity.RoleAssignment{ID: key, TenantID: primaryTenantID, MembershipID: subject, Subject: subject, Role: identity.Role(role), State: identity.AssignmentActive, Revision: 1}
			if fault == "grant-member" {
				assignment.MembershipID = ""
			}
			if fault == "grant-subject" {
				assignment.Subject = "different"
			}
			return respond(assignment), nil
		}
		if method == "POST" && path == "/api/v1/access/assignments" {
			value, ok := body.(map[string]string)
			if !ok {
				t.Fatal("companion assignment body is not a string object")
			}
			subject, role := value["membershipId"], value["roleDefinitionId"]
			grants[subject] = append(grants[subject], role)
			assignment := identity.AccessAssignment{ID: key, TenantID: primaryTenantID, MembershipID: subject, RoleDefinitionID: role, State: identity.AssignmentActive, Revision: 1}
			switch fault {
			case "companion-member":
				assignment.MembershipID = "different"
			case "companion-scope":
				assignment.NamespaceID = "narrow"
			case "companion-group":
				assignment.GroupID = "group"
			case "companion-role":
				assignment.RoleDefinitionID = "different"
			case "companion-state":
				assignment.State = identity.AssignmentRevoked
			}
			result := respond(assignment)
			if fault == "companion-etag" {
				result.Header.Set("ETag", `"rev-2"`)
			}
			return result, nil
		}
		t.Fatalf("unexpected bootstrap request %s %s", method, path)
		return httpResult{}, nil
	})
	if fault != "" {
		if err == nil {
			t.Fatal("invalid bootstrap control was accepted")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	// Independent required workflows: discovery, publisher creation and host
	// publication need explicit tenant companions, never role-name inference.
	for subject, expected := range map[string][]string{
		"oidc-contributor": {"contributor", "reader", "discovery_reader", "publisher_creator"},
		"oidc-reviewer":    {"reviewer", "reader", "discovery_reader"},
		"oidc-publisher":   {"publisher", "reader", "discovery_reader", "publication_admin"},
		"oidc-reader":      {"reader", "discovery_reader"},
	} {
		actual := grants[subject]
		slices.Sort(actual)
		slices.Sort(expected)
		if !slices.Equal(actual, expected) {
			t.Errorf("%s grants = %v, want %v", subject, actual, expected)
		}
	}
}
