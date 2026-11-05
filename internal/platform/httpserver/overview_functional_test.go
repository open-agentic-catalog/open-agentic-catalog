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

func TestOverviewHTTPContractAndPrivateAdmission(t *testing.T) {
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.Authenticator, d.Identity = newTestIdentity(t, clock.NewFake(d.Now()), map[string]config.DevelopmentUser{
			"admin":       {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleTenantAdmin}},
			"none":        {Subject: "none", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleServiceClient}},
			"other-admin": {Subject: "other-admin", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleTenantAdmin}},
		})
	})
	defer server.Close()
	for _, item := range []struct{ name, schema string }{{"resources", "ResourceOverview"}, {"releases", "ReleaseOverview"}, {"namespaces", "DirectoryOverview"}, {"publishers", "DirectoryOverview"}, {"indexed", "IndexedOverview"}, {"reviews", "ReviewOverview"}, {"publication", "PublicationOverview"}, {"sources", "SourceOverview"}, {"jobs", "JobOverview"}, {"webhook-endpoints", "WebhookEndpointOverview"}, {"webhook-deliveries", "WebhookDeliveryOverview"}, {"administration", "AdministrationOverview"}, {"feedback", "FeedbackOverview"}, {"ratings", "RatingOverview"}} {
		t.Run(item.name, func(t *testing.T) {
			path := "/api/v1/overview/" + item.name
			response := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
			requireStatus(t, response, 200)
			requireControlSchema(t, item.schema, response.body)
			if !strings.Contains(response.header.Get("Cache-Control"), "no-store") {
				t.Fatal("private summary can be cached", response.header)
			}
			requireStatus(t, requestJSON(t, server.URL, http.MethodGet, path, "", "", "", nil), 401)
			requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, path+"?limit=1", "admin", "", "", nil), 400, catalog.CodeInvalidArgument)
			requireStatus(t, requestJSON(t, server.URL, http.MethodPost, path, "admin", "unknown", "", map[string]any{}), 405)
		})
	}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/publishers", "none", "", "", nil), 403, catalog.CodeForbidden)
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/indexed", "none", "", "", nil), 403, catalog.CodeForbidden)
	other := requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/resources", "other-admin", "", "", nil)
	requireStatus(t, other, 200)
	var isolated catalog.ResourceOverview
	decodeResult(t, other, &isolated)
	if isolated.Total.Value != 0 {
		t.Fatal("other tenant received inventory", isolated)
	}
	other = requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/publication", "other-admin", "", "", nil)
	requireStatus(t, other, 200)
	var hosts struct {
		Hosts []any `json:"hosts"`
	}
	decodeResult(t, other, &hosts)
	if len(hosts.Hosts) != 0 {
		t.Fatal("another tenant received configured hosts", hosts)
	}
}

func TestOverviewHTTPEnforcesTokenScopesAndCurrentPermissions(t *testing.T) {
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.EnforceOAuthScopes = true
		authenticator, service := newTestIdentity(t, clock.NewFake(d.Now()), map[string]config.DevelopmentUser{
			"admin": {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleTenantAdmin}},
			"none":  {Subject: "none", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleServiceClient}},
		})
		d.Identity = service
		d.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			name, scope, _ := strings.Cut(token, "/")
			actor, err := authenticator.Authenticate(ctx, name)
			actor.Claims = map[string]string{"scope:" + scope: "true"}
			return actor, err
		})
	})
	defer server.Close()
	for _, name := range []string{"resources", "releases", "namespaces", "publishers", "indexed", "reviews", "publication", "sources", "jobs", "webhook-endpoints", "webhook-deliveries", "administration", "feedback", "ratings"} {
		t.Run(name, func(t *testing.T) {
			path := "/api/v1/overview/" + name
			scope, unrelated := "catalog.read", "publishers.read"
			if strings.HasPrefix(name, "webhook-") {
				scope, unrelated = "webhooks.manage", "catalog.read"
			}
			if name == "sources" {
				scope, unrelated = "sources.read", "catalog.read"
			}
			if name == "publishers" {
				scope, unrelated = unrelated, scope
			}
			if name == "administration" || name == "feedback" || name == "ratings" {
				scope, unrelated = "tenant.admin", "catalog.read"
			}
			refusals := []string{"", unrelated}
			if scope != "tenant.admin" {
				refusals = append(refusals, "tenant.admin")
			}
			for _, refused := range refusals {
				response := requestJSON(t, server.URL, http.MethodGet, path, "admin/"+refused, "", "", nil)
				requireControlProblem(t, response, http.StatusForbidden, catalog.CodeForbidden)
				if response.header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="`+scope+`"` {
					t.Fatal("scope refusal does not identify the required ceiling", response.header)
				}
			}
			requireStatus(t, requestJSON(t, server.URL, http.MethodGet, path, "admin/"+scope, "", "", nil), http.StatusOK)
			// A sufficient OAuth ceiling never supplies missing product grants.
			response := requestJSON(t, server.URL, http.MethodGet, path, "none/"+scope, "", "", nil)
			if name == "publishers" || name == "indexed" || name == "publication" || name == "sources" || name == "jobs" || name == "administration" || name == "feedback" || name == "ratings" || strings.HasPrefix(name, "webhook-") {
				requireControlProblem(t, response, http.StatusForbidden, catalog.CodeForbidden)
				return
			}
			requireStatus(t, response, http.StatusOK)
			switch name {
			case "resources":
				var value catalog.ResourceOverview
				decodeResult(t, response, &value)
				if value.Total != (catalog.BoundedCount{}) {
					t.Fatal("ungranted resources were counted", value)
				}
			case "releases":
				var value catalog.ReleaseOverview
				decodeResult(t, response, &value)
				if value.Published != (catalog.BoundedCount{}) || value.Deprecated != (catalog.BoundedCount{}) {
					t.Fatal("ungranted releases were counted", value)
				}
			case "namespaces":
				var value catalog.DirectoryOverview
				decodeResult(t, response, &value)
				if value.Count != (catalog.BoundedCount{}) {
					t.Fatal("ungranted namespaces were counted", value)
				}
			}
		})
	}
}

func TestReviewUnreleasedFilterHTTPValidation(t *testing.T) {
	server := newConfiguredHTTPServer(t, nil)
	defer server.Close()
	for _, query := range []string{"unreleased=", "unreleased=1", "unreleased=TRUE", "unreleased=null", "unreleased=true&unreleased=true&state=approved", "unreleased=true", "unreleased=true&state=submitted"} {
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/review-submissions?"+query, "admin", "", "", nil), 400, catalog.CodeInvalidArgument)
	}
	for _, query := range []string{"state=approved&unreleased=true", "state=approved&unreleased=false", "state=submitted&unreleased=false"} {
		requireStatus(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/review-submissions?"+query, "admin", "", "", nil), 200)
	}
}

func TestWebhookOverviewDisabledRoutes(t *testing.T) {
	server := newConfiguredHTTPServer(t, func(d *Dependencies) { d.Webhooks = nil })
	defer server.Close()
	for _, path := range []string{"webhook-endpoints", "webhook-deliveries"} {
		requireStatus(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/"+path, "admin", "", "", nil), http.StatusNotFound)
	}
	for _, path := range []string{"administration", "feedback", "ratings"} {
		requireStatus(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/"+path, "admin", "", "", nil), http.StatusOK)
	}
}
