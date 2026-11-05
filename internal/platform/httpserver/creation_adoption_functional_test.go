//go:build functional

package httpserver

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// Use the existing source and retained occurrence; qualify only the client's authority.
func qualifyHTTPWriteOnlyCreationAndAdoption(t *testing.T, base string, dependencies Dependencies, namespaceID, observationID string, body map[string]any) (httpResult, httpResult) {
	t.Helper()
	self := requestJSON(t, base, http.MethodGet, "/api/v1/me", "adopter", "", "", nil)
	requireStatus(t, self, http.StatusOK)
	var member struct {
		MembershipID string `json:"membershipId"`
	}
	decodeResult(t, self, &member)
	for index, permissions := range [][]string{{"source.read", "source.read_evidence"}, {"namespace.read", "resource.create"}} {
		name := []string{"Source evidence consumer", "Exact namespace creator"}[index]
		created := requestJSON(t, base, http.MethodPost, "/api/v1/access/roles", "source-admin", "write-only-role-"+name, "", map[string]any{"name": name, "permissions": permissions, "reason": "Qualify minimal creation and adoption"})
		requireStatus(t, created, http.StatusCreated)
		var role identity.RoleDefinition
		decodeResult(t, created, &role)
		grant := map[string]any{"membershipId": member.MembershipID, "roleDefinitionId": role.ID, "reason": "Grant only required authority"}
		if index == 1 {
			grant["namespaceId"] = namespaceID
		}
		requireStatus(t, requestJSON(t, base, http.MethodPost, "/api/v1/access/assignments", "source-admin", "write-only-grant-"+name, "", grant), http.StatusCreated)
	}
	otherResult := requestJSON(t, base, http.MethodPost, "/api/v1/namespaces", "source-admin", "write-only-other-namespace", "", map[string]any{"slug": "unassigned-adoption", "displayName": "Unassigned adoption destination"})
	requireStatus(t, otherResult, http.StatusCreated)
	var other catalog.Namespace
	decodeResult(t, otherResult, &other)
	authenticator := dependencies.Authenticator
	dependencies.EnforceOAuthScopes = true
	dependencies.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
		// Keep the same principal and grants; change only its OAuth ceiling.
		missingWrite := token == "adopter-read-scope-only" // #nosec G101 -- synthetic local fixture token, never a production credential.
		if missingWrite {
			token = "adopter"
		}
		actor, err := authenticator.Authenticate(ctx, token)
		actor.Claims = map[string]string{"scope:sources.read": "true"}
		if !missingWrite {
			actor.Claims["scope:catalog.write"] = "true"
		}
		return actor, err
	})
	server, err := New(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	qualified := httptest.NewServer(server.Handler())
	defer qualified.Close()
	for _, path := range []string{"/api/v1/namespaces", "/api/v1/resources"} {
		requireControlProblem(t, requestJSON(t, qualified.URL, http.MethodGet, path, "adopter", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	}
	createBody := map[string]any{"namespaceId": namespaceID, "canonicalIdentifier": "urn:air:remote.example:default:creation-only", "kind": "plugin"}
	wrongCreate := maps.Clone(createBody)
	wrongCreate["namespaceId"] = other.ID
	path := "/api/v1/observations/" + observationID + ":adopt"
	wrongAdopt := maps.Clone(body)
	wrongAdopt["namespaceId"] = other.ID
	for _, denied := range []struct {
		name, token, path, key string
		body                   map[string]any
	}{
		{"create outside assigned namespace", "adopter", "/api/v1/resources", "write-only-wrong-create", wrongCreate},
		{"adopt outside assigned namespace", "adopter", path, "write-only-wrong-adopt", wrongAdopt},
		{"create without write scope", "adopter-read-scope-only", "/api/v1/resources", "write-only-native-create", createBody},
		{"adopt without write scope", "adopter-read-scope-only", path, "write-only-adopt", body},
	} {
		t.Run(denied.name, func(t *testing.T) {
			requireControlProblem(t, requestJSON(t, qualified.URL, http.MethodPost, denied.path, denied.token, denied.key, "", denied.body), http.StatusForbidden, catalog.CodeForbidden)
		})
	}
	// Denied requests must not consume these valid command keys or create either identity.
	created := requestJSON(t, qualified.URL, http.MethodPost, "/api/v1/resources", "adopter", "write-only-native-create", "", createBody)
	requireStatus(t, created, http.StatusCreated)
	requireControlSchema(t, "Resource", created.body)
	var resource catalog.Resource
	decodeResult(t, created, &resource)
	requireControlProblem(t, requestJSON(t, qualified.URL, http.MethodGet, "/api/v1/resources/"+resource.ID, "adopter", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	adopted := requestJSON(t, qualified.URL, http.MethodPost, path, "adopter", "write-only-adopt", "", body)
	requireStatus(t, adopted, http.StatusCreated)
	replay := requestJSON(t, qualified.URL, http.MethodPost, path, "adopter", "write-only-adopt", "", body)
	return adopted, replay
}
