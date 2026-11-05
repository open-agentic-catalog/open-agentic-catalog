package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// Resolver outages must not invalidate a valid session or reveal private causes.
func TestAuthenticationFailureClassification(t *testing.T) {
	t.Parallel()
	private := errors.New("private DSN and token sentinel")
	for _, tc := range []struct {
		name    string
		failure error
		status  int
		code    catalog.ErrorCode
	}{
		{"wrapped resolver outage", fmt.Errorf("resolve principal: %w", identity.Wrap(identity.CodeDependency, "private resolver detail", private)), 503, catalog.CodeDependencyUnavailable},
		{"inactive membership", identity.Wrap(identity.CodeUnauthenticated, "private membership detail", private), 401, catalog.CodeUnauthenticated},
		{"refused identity", identity.Wrap(identity.CodeForbidden, "private tenant detail", private), 401, catalog.CodeUnauthenticated},
		{"unclassified token failure", private, 401, catalog.CodeUnauthenticated},
	} {
		for _, discovery := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/discovery=%t", tc.name, discovery), func(t *testing.T) {
				calls := 0
				auth := discoveryAdmissionAuthenticator(func(context.Context, string) (identity.Actor, error) {
					calls++
					return identity.Actor{}, tc.failure
				})
				tracer := new(ingressTrace)
				ctx := context.WithValue(catalog.WithTracer(catalog.WithTraceContext(t.Context(), "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"), tracer), requestIDKey{}, "authentication-failure-request")
				r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/me", nil)
				r.Header.Set("Authorization", "Bearer synthetic-token")
				w := httptest.NewRecorder()
				realm := "open-agentic-catalog"
				if discovery {
					realm += "-discovery"
					r.Host = publicFixtureHost
					s := projectionServer(nil)
					s.authenticator = auth
					s.discovery = map[string]DiscoveryHost{publicFixtureHost: {Host: publicFixtureHost, TenantID: projectionTenant, Mode: DiscoveryAuthenticated}}
					if binding, ok := s.authorizeDiscovery(w, r); ok || binding != (DiscoveryHost{}) {
						t.Fatal("failed authentication disclosed a discovery binding")
					}
					requireARDProjectionProblem(t, w, tc.status, tc.code)
				} else {
					authentication(auth, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("failed authentication reached handler") })).ServeHTTP(w, r)
					requireProjectionProblem(t, w, tc.status, tc.code)
					outcome := "4xx"
					if tc.status == 503 {
						outcome = "5xx"
					}
					if tracer.calls != 1 || tracer.ends != 1 || tracer.name != "authentication" || tracer.outcome != outcome {
						t.Fatal("authentication span misclassified the failure")
					}
				}
				expected := ""
				if tc.status == 401 {
					expected = `Bearer realm="` + realm + `", error="invalid_token"`
				}
				if calls != 1 || w.Header().Get("WWW-Authenticate") != expected {
					t.Fatal("authentication/challenge classification")
				}
				for _, secret := range []string{"private", "synthetic-token", "DSN"} {
					if strings.Contains(w.Body.String(), secret) {
						t.Fatal("authentication failure disclosed private input")
					}
				}
			})
		}
	}
}
