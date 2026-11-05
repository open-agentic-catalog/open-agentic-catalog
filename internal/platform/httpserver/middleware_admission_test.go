package httpserver

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestAuthenticationAdmissionAndActorHandoff(t *testing.T) {
	t.Parallel()
	for _, header := range []string{"", "Basic token", "Bearer", "Bearer   ", "bEaReR  token  "} {
		t.Run(header, func(t *testing.T) {
			actor := projectionActor(identity.RoleReader)
			calls, nextCalls := 0, 0
			ctx := context.WithValue(t.Context(), requestIDKey{}, "auth-request")
			auth := discoveryAdmissionAuthenticator(func(c context.Context, token string) (identity.Actor, error) {
				calls++
				if token != "token" || requestID(c) != "auth-request" {
					t.Fatal("authentication lost token or context")
				}
				return actor, nil
			})
			handler := authentication(auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalls++
				got, ok := identity.FromContext(r.Context())
				if !ok || !reflect.DeepEqual(got, actor) || requestID(r.Context()) != "auth-request" {
					t.Fatal("actor handoff")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/resources", nil)
			r.Header.Set("Authorization", header)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if header == "bEaReR  token  " {
				if calls != 1 || nextCalls != 1 || w.Code != 204 || w.Header().Get("WWW-Authenticate") != "" {
					t.Fatal("valid envelope rejected")
				}
			} else {
				requireProjectionProblem(t, w, 401, catalog.CodeUnauthenticated)
				if calls != 0 || nextCalls != 0 || w.Header().Get("WWW-Authenticate") != `Bearer realm="open-agentic-catalog"` {
					t.Fatal("invalid envelope crossed authentication boundary")
				}
			}
			if _, ok := identity.FromContext(r.Context()); ok {
				t.Fatal("authentication mutated caller context")
			}
		})
	}
	// Public/discovery authentication has its own admission boundary.
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.well-known/ard.json", nil)
	r.Header.Set("Authorization", "Bearer token")
	called := false
	w := httptest.NewRecorder()
	authentication(nil, http.HandlerFunc(func(w http.ResponseWriter, got *http.Request) {
		called = true
		if got != r {
			t.Fatal("public request replaced")
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(w, r)
	if !called || w.Code != 204 {
		t.Fatal("control authentication intercepted public discovery")
	}
}

func TestAuditExecutionUsesResolvedAuthorization(t *testing.T) {
	t.Parallel()
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "anonymous", true: "resolved actor"}[authenticated], func(t *testing.T) {
			actor := projectionActor(identity.RoleReader)
			ctx := context.WithValue(t.Context(), requestIDKey{}, "audit-request")
			if authenticated {
				ctx = identityfixture.WithActor(ctx, actor)
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/resources", nil)
			r.Header.Set("X-Authorization-Digest", "untrusted")
			r.Header.Set("X-Request-ID", "untrusted")
			called := false
			auditExecution(http.HandlerFunc(func(w http.ResponseWriter, got *http.Request) {
				called = true
				expected := audit.Execution{}
				if authenticated {
					expected.RequestID = "audit-request"
					expected.AuthorizationDigest = actor.AuthorizationDigest()
				}
				if execution := audit.ExecutionFrom(got.Context()); execution != expected {
					t.Fatalf("execution=%+v want=%+v", execution, expected)
				}
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(httptest.NewRecorder(), r)
			if !called || audit.ExecutionFrom(r.Context()) != (audit.Execution{}) {
				t.Fatal("audit middleware did not preserve caller context")
			}
		})
	}
}

func TestSecurityHeadersPreservePublicCachePolicy(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/v1/resources", "/.well-known/ard.json", "/livez"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			w.Header().Set("Cache-Control", "public, max-age=30")
			called := false
			securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
			for name, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'", "Permissions-Policy": "camera=(), microphone=(), geolocation=()"} {
				if w.Header().Get(name) != want {
					t.Fatalf("%s=%q", name, w.Header().Get(name))
				}
			}
			cache := "public, max-age=30"
			if path == "/api/v1/resources" {
				cache = "no-store"
			}
			if !called || w.Code != 204 || w.Header().Get("Cache-Control") != cache {
				t.Fatal("security middleware changed response/cache contract")
			}
		})
	}
}
