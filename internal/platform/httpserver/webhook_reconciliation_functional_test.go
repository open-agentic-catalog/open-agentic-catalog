//go:build functional

package httpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestHTTPWebhookReconciliationAuthorizationAndContract(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		authenticator, service := newTestIdentity(t, clock.NewFake(d.Now()), map[string]config.DevelopmentUser{
			"admin":           {Subject: "admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin}},
			"other-admin":     {Subject: "other-admin", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleTenantAdmin}},
			"contributor":     {Subject: "contributor", TenantID: tenantID, Roles: []identity.Role{identity.RoleContributor}},
			"webhook-admin":   {Subject: "webhook-admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleServiceClient}},
			"delegate":        {Subject: "delegate", TenantID: tenantID, Roles: []identity.Role{identity.RoleServiceClient}},
			"endpoint-reader": {Subject: "endpoint-reader", TenantID: tenantID, Roles: []identity.Role{identity.RoleServiceClient}},
		})
		d.Identity = service
		d.EnforceOAuthScopes = true
		d.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			claims := map[string]string{"scope:webhooks.manage": "true"}
			switch token {
			case "admin", "other-admin":
				claims["scope:tenant.admin"] = "true"
			case "webhook-admin-no-scope", "delegate-no-scope":
				token = strings.TrimSuffix(token, "-no-scope")
				claims = nil
			case "delegate-catalog-scope":
				token = "delegate"
				claims = map[string]string{"scope:catalog.read": "true"}
			}
			actor, err := authenticator.Authenticate(ctx, token)
			actor.Claims = claims
			return actor, err
		})
	})
	defer server.Close()
	const path = "/api/v1/webhooks/reconciliation"
	for _, token := range []string{"admin", "other-admin"} {
		result := requestJSON(t, server.URL, http.MethodGet, path, token, "", "", nil)
		requireStatus(t, result, http.StatusOK)
		requireControlSchema(t, "WebhookReconciliation", result.body)
		var state catalog.NotificationState
		decodeResult(t, result, &state)
		if state.PendingEventLimit != catalog.PendingWebhookEventLimit || state.CapacityLossCount != 0 || state.DeliveryLossCount != 0 {
			t.Fatal("unexpected empty tenant reconciliation state")
		}
	}
	// A broad token ceiling supplies no Catalog grant. The delegate starts
	// with an exact empty template and receives only the reviewed custom role.
	for _, token := range []string{"contributor", "delegate", "webhook-admin"} {
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, path, token, "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	}
	requireStatus(t, requestJSON(t, server.URL, http.MethodGet, path, "", "", "", nil), http.StatusUnauthorized)
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, path+"?reset=true", "admin", "", "", nil), http.StatusBadRequest, catalog.CodeInvalidArgument)

	definitions := requestJSON(t, server.URL, http.MethodGet, "/api/v1/access/roles?limit=100", "admin", "", "", nil)
	requireStatus(t, definitions, http.StatusOK)
	var roles identity.AuthorizationPage[identity.RoleDefinition]
	decodeResult(t, definitions, &roles)
	var webhookRoleID string
	for _, role := range roles.Items {
		if role.Key == "webhook_admin" {
			webhookRoleID = role.ID
		}
	}
	if webhookRoleID == "" {
		t.Fatal("Webhook Administrator template is unavailable")
	}
	created := requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/roles", "admin", "reconciliation-role", "", map[string]any{"name": "Reconciliation reader", "permissions": []string{"webhook.read", "webhook.read_evidence"}, "reason": "Delegate notification checkpoints"})
	requireStatus(t, created, http.StatusCreated)
	var role identity.RoleDefinition
	decodeResult(t, created, &role)
	endpointRoleResponse := requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/roles", "admin", "endpoint-reader-role", "", map[string]any{"name": "Endpoint summary reader", "permissions": []string{"webhook.read"}, "reason": "Read configuration counts only"})
	requireStatus(t, endpointRoleResponse, http.StatusCreated)
	var endpointRole identity.RoleDefinition
	decodeResult(t, endpointRoleResponse, &endpointRole)
	var assignment identity.AccessAssignment
	var assignmentETag string
	for _, grant := range []struct{ token, roleID string }{{"webhook-admin", webhookRoleID}, {"delegate", role.ID}, {"endpoint-reader", endpointRole.ID}} {
		self := requestJSON(t, server.URL, http.MethodGet, "/api/v1/me", grant.token, "", "", nil)
		requireStatus(t, self, http.StatusOK)
		var member struct {
			MembershipID string `json:"membershipId"`
		}
		decodeResult(t, self, &member)
		granted := requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/assignments", "admin", "reconciliation-grant-"+grant.token, "", map[string]any{"roleDefinitionId": grant.roleID, "membershipId": member.MembershipID, "reason": "Allow receiver reconciliation"})
		requireStatus(t, granted, http.StatusCreated)
		if grant.token == "delegate" {
			decodeResult(t, granted, &assignment)
			assignmentETag = granted.header.Get("ETag")
		}
	}

	requireStatus(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/webhook-endpoints", "endpoint-reader", "", "", nil), http.StatusOK)
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/webhook-deliveries", "endpoint-reader", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, path, "endpoint-reader", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	// One real Catalog mutation gives the first tenant nonempty counters.
	// Repeated delegated reads must not acknowledge its pending notification.
	requireStatus(t, requestJSON(t, server.URL, http.MethodPost, "/api/v1/namespaces", "admin", "reconciliation-event", "", map[string]any{"slug": "checkpoint", "displayName": "Checkpoint"}), http.StatusCreated)
	for _, summary := range []struct{ path, schema string }{{"webhook-endpoints", "WebhookEndpointOverview"}, {"webhook-deliveries", "WebhookDeliveryOverview"}} {
		response := requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/"+summary.path, "delegate", "", "", nil)
		requireStatus(t, response, http.StatusOK)
		requireControlSchema(t, summary.schema, response.body)
	}

	for _, test := range []struct {
		token   string
		pending int64
	}{
		{"delegate", 1}, {"delegate", 1}, {"webhook-admin", 1}, {"other-admin", 0},
	} {
		read := requestJSON(t, server.URL, http.MethodGet, path, test.token, "", "", nil)
		requireStatus(t, read, http.StatusOK)
		requireControlSchema(t, "WebhookReconciliation", read.body)
		var state catalog.NotificationState
		decodeResult(t, read, &state)
		if state.PendingEvents != test.pending || state.PendingEventLimit != catalog.PendingWebhookEventLimit || state.CapacityLossCount != 0 || state.DeliveryLossCount != 0 {
			t.Fatalf("%s reconciliation changed or crossed tenant: %+v", test.token, state)
		}
	}
	for _, token := range []string{"webhook-admin-no-scope", "delegate-no-scope", "delegate-catalog-scope"} {
		denied := requestJSON(t, server.URL, http.MethodGet, path, token, "", "", nil)
		requireControlProblem(t, denied, http.StatusForbidden, catalog.CodeForbidden)
		if denied.header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="webhooks.manage"` {
			t.Fatal("missing scope did not retain the correct OAuth challenge")
		}
	}
	revoked := requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/assignments/"+assignment.ID+":revoke", "admin", "reconciliation-revoke", assignmentETag, map[string]any{"reason": "Remove receiver access"})
	requireStatus(t, revoked, http.StatusOK)
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, path, "delegate", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	for _, name := range []string{"webhook-endpoints", "webhook-deliveries"} {
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/"+name, "delegate", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	}
}
