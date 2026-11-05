//go:build functional

package httpserver

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"net/http"
	"strings"
	"testing"
)

func TestAdministrationOverviewModeratorWithoutResourceReadsAndRevocation(t *testing.T) {
	var access *identity.Service
	var adminActor identity.Actor
	var assignment identity.AccessAssignment
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		authenticator, service := newTestIdentity(t, clock.NewFake(d.Now()), map[string]config.DevelopmentUser{
			"admin":     {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleTenantAdmin}},
			"moderator": {Subject: "moderator", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleServiceClient}},
			"reader":    {Subject: "reader", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleReader}},
		})
		access = service
		admin, err := authenticator.Authenticate(t.Context(), "admin")
		if err != nil {
			t.Fatal(err)
		}
		adminActor = admin
		adminContext := identity.WithActor(t.Context(), admin)
		role, err := service.SaveRoleDefinition(adminContext, "", 0, identity.RoleDefinitionInput{Name: "Inbox summaries", Permissions: []identity.Permission{"feedback.moderate", "ratings.moderate"}, Reason: "Qualify moderator summaries"}, false, "summary-role")
		if err != nil {
			t.Fatal(err)
		}
		members, err := service.ListMemberships(adminContext, 25, "", identity.MembershipFilter{Subject: "moderator"})
		if err != nil || len(members.Items) != 1 {
			t.Fatal(members, err)
		}
		assignment, err = service.GrantAuthorizationAssignment(adminContext, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: members.Items[0].ID, Reason: "Qualify exact moderator authority"}, "summary-grant")
		if err != nil {
			t.Fatal(err)
		}
		namespace, err := d.Catalog.CreateNamespace(adminContext, catalog.CreateNamespaceInput{Slug: "summary", DisplayName: "Summary"})
		if err != nil {
			t.Fatal(err)
		}
		resource, err := d.Catalog.CreateResource(adminContext, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:summary:private", Kind: catalog.KindAgent})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = d.Feedback.Create(adminContext, feedback.CreateInput{ResourceID: resource.ID, Category: "problem", Comment: "summary-private-body"}); err != nil {
			t.Fatal(err)
		}
		moderator, err := authenticator.Authenticate(t.Context(), "moderator")
		if err != nil {
			t.Fatal(err)
		}
		decisions, err := service.CurrentPermissionDecisions(identity.WithActor(t.Context(), moderator), []identity.PermissionCheck{{Permission: "resource.read"}, {Permission: "feedback.moderate"}, {Permission: "ratings.moderate"}})
		if err != nil || decisions.Allowed[0] || !decisions.Allowed[1] || !decisions.Allowed[2] {
			t.Fatal(decisions, err)
		}
		d.Identity = service
		d.EnforceOAuthScopes = true
		d.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			name, scope, _ := strings.Cut(token, "/")
			actor, err := authenticator.Authenticate(ctx, name)
			actor.Claims = map[string]string{"scope:" + scope: "true"}
			return actor, err
		})
	})
	defer server.Close()
	for _, name := range []string{"feedback", "ratings"} {
		path := "/api/v1/overview/" + name
		response := requestJSON(t, server.URL, http.MethodGet, path, "moderator/tenant.admin", "", "", nil)
		requireStatus(t, response, http.StatusOK)
		if strings.Contains(string(response.body), "summary-private-body") || strings.Contains(string(response.body), "resourceId") || strings.Contains(string(response.body), "membershipId") {
			t.Fatal("summary contains private submission context", string(response.body))
		}
		if name == "feedback" {
			var value feedback.Overview
			decodeResult(t, response, &value)
			if value.Open.Value != 1 || value.Categories[0].Count != 1 || value.OldestOpenAt == nil {
				t.Fatal(value)
			}
		}
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, path, "moderator/catalog.read", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, path, "reader/tenant.admin", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/administration", "moderator/tenant.admin", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	if _, err := access.RevokeAuthorizationAssignment(identity.WithActor(t.Context(), adminActor), assignment.ID, assignment.Revision, "Revoke summary qualification authority", "summary-revoke"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"feedback", "ratings"} {
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/"+name, "moderator/tenant.admin", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	}
}
