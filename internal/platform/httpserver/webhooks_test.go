// Unit tests exercise isolated validation and controlled dependencies.
package httpserver

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestCORSAndDependencyReadiness(t *testing.T) {
	responseServer := &Server{logger: slog.New(slog.DiscardHandler)}
	next := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		responseServer.respondJSON(writer, request, http.StatusOK, map[string]bool{"ok": true})
	})
	handler := cors([]string{"https://console.example.com"}, next)
	preflight := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "https://catalog.example.com/api/v1/catalog/resources", nil)
	preflight.Header.Set("Origin", "https://console.example.com")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	preflightResponse := httptest.NewRecorder()
	handler.ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusNoContent || preflightResponse.Header().Get("Access-Control-Allow-Origin") != "https://console.example.com" || !strings.Contains(preflightResponse.Header().Get("Access-Control-Allow-Headers"), "If-None-Match") || !strings.Contains(preflightResponse.Header().Get("Access-Control-Expose-Headers"), "Content-Disposition") || !strings.Contains(preflightResponse.Header().Get("Access-Control-Expose-Headers"), "Link") || !strings.Contains(preflightResponse.Header().Get("Access-Control-Expose-Headers"), "Retry-After") {
		t.Fatalf("unexpected CORS preflight: status=%d headers=%#v", preflightResponse.Code, preflightResponse.Header())
	}
	denied := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "https://catalog.example.com/api/v1/catalog/resources", nil)
	denied.Header.Set("Origin", "https://untrusted.example.com")
	deniedResponse := httptest.NewRecorder()
	handler.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusOK || deniedResponse.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("untrusted origin was allowed: status=%d headers=%#v", deniedResponse.Code, deniedResponse.Header())
	}
	allowed := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "https://catalog.example.com/api/v1/catalog/resources", nil)
	allowed.Header.Set("Origin", "https://console.example.com")
	allowedResponse := httptest.NewRecorder()
	handler.ServeHTTP(allowedResponse, allowed)
	if allowedResponse.Code != http.StatusOK || allowedResponse.Header().Get("Access-Control-Allow-Origin") != "https://console.example.com" {
		t.Fatalf("trusted simple request = status %d headers=%#v", allowedResponse.Code, allowedResponse.Header())
	}

	server := &Server{now: time.Now, logger: slog.New(slog.DiscardHandler), readiness: func(context.Context) error { return errors.New("database unavailable") }}
	server.ready.Store(true)
	readinessRequest := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "https://catalog.example.com/readyz", nil)
	readinessResponse := httptest.NewRecorder()
	server.handleReady(readinessResponse, readinessRequest)
	if readinessResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("dependency failure readiness = %d, body=%s", readinessResponse.Code, readinessResponse.Body.String())
	}
}

func TestOAuthScopesAreRequiredInAdditionToRoles(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		called = true
		writer.WriteHeader(http.StatusNoContent)
	})
	handler := authorizationScopes(true, next)
	capabilitiesRequest := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "https://catalog.example.com/api/v1/capabilities", nil)
	capabilitiesResponse := httptest.NewRecorder()
	handler.ServeHTTP(capabilitiesResponse, capabilitiesRequest)
	if capabilitiesResponse.Code != http.StatusNoContent || !called {
		t.Fatalf("capabilities scope bypass = %d called=%v", capabilitiesResponse.Code, called)
	}
	called = false
	actor := identity.Actor{TenantID: "tenant-one", Subject: "publisher", Roles: []identity.Role{identity.RolePublisher}, Claims: map[string]string{}}
	requestWithoutScope := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "https://catalog.example.com/api/v1/publication/snapshots", nil)
	requestWithoutScope = requestWithoutScope.WithContext(identityfixture.WithActor(requestWithoutScope.Context(), actor))
	responseWithoutScope := httptest.NewRecorder()
	handler.ServeHTTP(responseWithoutScope, requestWithoutScope)
	if responseWithoutScope.Code != http.StatusForbidden || called || responseWithoutScope.Header().Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="catalog.publish"` {
		t.Fatalf("missing-scope response = %d called=%v headers=%#v", responseWithoutScope.Code, called, responseWithoutScope.Header())
	}
	actor.Claims["scope:catalog.publish"] = "true"
	requestWithScope := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "https://catalog.example.com/api/v1/publication/snapshots", nil)
	requestWithScope = requestWithScope.WithContext(identityfixture.WithActor(requestWithScope.Context(), actor))
	responseWithScope := httptest.NewRecorder()
	handler.ServeHTTP(responseWithScope, requestWithScope)
	if responseWithScope.Code != http.StatusNoContent || !called {
		t.Fatalf("authorized scope response = %d called=%v", responseWithScope.Code, called)
	}

	cases := map[string]string{
		http.MethodGet + " /api/v1/catalog/resources":               "catalog.read",
		http.MethodPost + " /api/v1/catalog/search":                 "catalog.read",
		http.MethodPost + " /api/v1/catalog/explore":                "catalog.read",
		http.MethodPost + " /api/v1/resources":                      "catalog.write",
		http.MethodPost + " /api/v1/version-revisions/id:validate":  "catalog.write",
		http.MethodPost + " /api/v1/review-submissions/id:approve":  "catalog.review",
		http.MethodPost + " /api/v1/catalog-listings/id:publish":    "catalog.publish",
		http.MethodGet + " /api/v1/federation/sources":              "sources.read",
		http.MethodPost + " /api/v1/observations:search":            "sources.read",
		http.MethodPost + " /api/v1/federation/sources:live-search": "sources.read",
		http.MethodPost + " /api/v1/federation/source-inspections":  "sources.manage",
		http.MethodPost + " /api/v1/observations/id:adopt":          "catalog.write",
		http.MethodGet + " /api/v1/publishers":                      "publishers.read",
		http.MethodPost + " /api/v1/publishers":                     "publishers.manage",
		http.MethodPost + " /api/v1/role-assignments":               "tenant.admin",
		http.MethodGet + " /api/v1/publication/hosts":               "catalog.read",
		http.MethodGet + " /api/v1/publication/snapshots":           "catalog.read",
		http.MethodGet + " /api/v1/review-submissions/id":           "catalog.read",
		http.MethodGet + " /api/v1/audit-events":                    "audit.read",
		http.MethodPost + " /api/v1/audit-exports":                  "audit.export",
		http.MethodPost + " /api/v1/webhooks/id:rotate-secret":      "webhooks.manage",
		http.MethodGet + " /api/v1/webhooks/reconciliation":         "webhooks.manage",
		http.MethodPost + " /api/v1/namespaces":                     "tenant.admin",
		http.MethodGet + " /api/v1/capabilities":                    "",
	}
	for input, want := range cases {
		method, path, _ := strings.Cut(input, " ")
		if got := requiredOAuthScope(method, path); got != want {
			t.Errorf("requiredOAuthScope(%q, %q) = %q, want %q", method, path, got, want)
		}
	}
}

func TestWebhookReconciliationOAuthScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		enforce bool
		scope   string
		allowed bool
	}{
		{"not enforced", false, "", true},
		{"webhooks scope", true, "webhooks.manage", true},
		{"missing scope", true, "", false},
		{"tenant admin scope", true, "tenant.admin", false},
		{"catalog read scope", true, "catalog.read", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "receiver", Roles: []identity.Role{identity.RoleTenantAdmin}, Claims: map[string]string{}}
			if test.scope != "" {
				actor.Claims["scope:"+test.scope] = "true"
			}
			request := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodGet, "/api/v1/webhooks/reconciliation", nil)
			response := httptest.NewRecorder()
			calls := 0
			authorizationScopes(test.enforce, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls++
				writer.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(response, request)
			if test.allowed {
				if response.Code != http.StatusNoContent || calls != 1 {
					t.Fatalf("scope admission: status=%d calls=%d", response.Code, calls)
				}
			} else if response.Code != http.StatusForbidden || calls != 0 || response.Header().Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="webhooks.manage"` {
				t.Fatalf("scope refusal: status=%d calls=%d headers=%v", response.Code, calls, response.Header())
			}
		})
	}
}
