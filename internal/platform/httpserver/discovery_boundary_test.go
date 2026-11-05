// Discovery admission is tested without a publication store or network verifier.
package httpserver

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type discoveryAdmissionAuthenticator func(context.Context, string) (identity.Actor, error)

func (a discoveryAdmissionAuthenticator) Authenticate(ctx context.Context, token string) (identity.Actor, error) {
	return a(ctx, token)
}

func TestDiscoveryAdmissionBeforeSnapshotAccess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, host, target, authorization, header string
		mode                                      DiscoveryMode
		scope                                     bool
		status, calls                             int
		code                                      catalog.ErrorCode
		challenge                                 string
	}{
		{name: "anonymous ignores bearer", authorization: "Bearer invalid", mode: DiscoveryAnonymous},
		{name: "canonical absolute target", host: "CATALOG.EXAMPLE.COM:443", target: publicFixtureOrigin + "/.well-known/ard.json", mode: DiscoveryAnonymous},
		{name: "unknown host", host: "unknown.example", mode: DiscoveryAnonymous, status: 404, code: catalog.CodeNotFound},
		{name: "disabled", mode: DiscoveryDisabled, status: 404, code: catalog.CodeNotFound},
		{name: "invalid authority", host: "user@catalog.example.com", mode: DiscoveryAnonymous, status: 404, code: catalog.CodeNotFound},
		{name: "conflicting absolute target", target: "https://other.example/.well-known/ard.json", mode: DiscoveryAnonymous, status: 404, code: catalog.CodeNotFound},
		{name: "invalid absolute target authority", target: "https://catalog.example.com./.well-known/ard.json", mode: DiscoveryAnonymous, status: 404, code: catalog.CodeNotFound},
		{name: "forwarded", header: "Forwarded", mode: DiscoveryAnonymous, status: 404, code: catalog.CodeNotFound},
		{name: "forwarded host", header: "X-Forwarded-Host", mode: DiscoveryAnonymous, status: 404, code: catalog.CodeNotFound},
		{name: "original host", header: "X-Original-Host", mode: DiscoveryAnonymous, status: 404, code: catalog.CodeNotFound},
		{name: "missing token", mode: DiscoveryAuthenticated, status: 401, code: catalog.CodeUnauthenticated, challenge: `Bearer realm="open-agentic-catalog-discovery"`},
		{name: "empty token", authorization: "Bearer   ", mode: DiscoveryAuthenticated, status: 401, code: catalog.CodeUnauthenticated, challenge: `Bearer realm="open-agentic-catalog-discovery"`},
		{name: "wrong scheme", authorization: "Basic reader", mode: DiscoveryAuthenticated, status: 401, code: catalog.CodeUnauthenticated, challenge: `Bearer realm="open-agentic-catalog-discovery"`},
		{name: "invalid token", authorization: "Bearer invalid", mode: DiscoveryAuthenticated, status: 401, calls: 1, code: catalog.CodeUnauthenticated, challenge: `Bearer realm="open-agentic-catalog-discovery", error="invalid_token"`},
		{name: "other tenant", authorization: "Bearer other", mode: DiscoveryAuthenticated, status: 403, calls: 1, code: catalog.CodeForbidden},
		{name: "missing scope", authorization: "Bearer no-scope", mode: DiscoveryAuthenticated, scope: true, status: 403, calls: 1, code: catalog.CodeForbidden, challenge: `Bearer error="insufficient_scope", scope="catalog.read"`},
		{name: "scope not enforced", authorization: "Bearer no-scope", mode: DiscoveryAuthenticated, calls: 1},
		{name: "scoped token", authorization: "bEaReR  reader  ", mode: DiscoveryAuthenticated, scope: true, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens := discoveryAuthenticator{"reader": {TenantID: projectionTenant, Claims: map[string]string{"scope:catalog.read": "true"}}, "no-scope": {TenantID: projectionTenant}, "other": {TenantID: projectionNext, Claims: map[string]string{"scope:catalog.read": "true"}}}
			calls := 0
			a := discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
				calls++
				if ctx != t.Context() {
					t.Fatal("authentication context mismatch")
				}
				actor, err := tokens.Authenticate(ctx, token)
				if err != nil {
					return actor, err
				}
				actor.Subject = token
				actor.Roles = []identity.Role{identity.RoleReader}
				bound, _ := identity.FromContext(identityfixture.WithActor(ctx, actor))
				return bound, nil
			})
			binding := DiscoveryHost{Host: publicFixtureHost, TenantID: projectionTenant, PublisherDomain: publicFixtureHost, Mode: tc.mode}
			s := projectionServer(nil)
			s.authenticator = a
			s.discovery = map[string]DiscoveryHost{publicFixtureHost: binding}
			s.enforceOAuthScopes = tc.scope
			target := tc.target
			if target == "" {
				target = "/.well-known/ard.json"
			}
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
			r.Host = tc.host
			if r.Host == "" {
				r.Host = publicFixtureHost
			}
			r.Header.Set("Authorization", tc.authorization)
			if tc.header != "" {
				r.Header.Set(tc.header, "other.example")
			}
			w := httptest.NewRecorder()
			got, ok := s.authorizeDiscovery(w, r)
			if calls != tc.calls {
				t.Fatal("authentication call/context mismatch")
			}
			if w.Header().Get("WWW-Authenticate") != tc.challenge {
				t.Fatalf("challenge=%q", w.Header().Get("WWW-Authenticate"))
			}
			if tc.status != 0 {
				if ok || got != (DiscoveryHost{}) {
					t.Fatal("rejected discovery returned binding")
				}
				requireARDProjectionProblem(t, w, tc.status, tc.code)
				return
			}
			if !ok || got != binding || w.Body.Len() != 0 {
				t.Fatalf("authorized binding=%+v accepted=%t", got, ok)
			}
		})
	}
}
func TestPublicationControlHostBinding(t *testing.T) {
	t.Parallel()
	s := publicProjectionServer(t, DiscoveryAnonymous, nil)
	manager := s.discovery[publicFixtureHost].Publication
	disabled := s.discovery[publicFixtureHost]
	disabled.Mode = DiscoveryDisabled
	s.discovery["disabled.example"] = disabled
	unwired := s.discovery[publicFixtureHost]
	unwired.Publication = nil
	s.discovery["unwired.example"] = unwired
	for _, tc := range []struct {
		host, tenant string
		code         catalog.ErrorCode
	}{
		{publicFixtureHost, projectionTenant, ""}, {"CATALOG.EXAMPLE.COM", projectionTenant, catalog.CodeInvalidArgument}, {"catalog.example.com.", projectionTenant, catalog.CodeInvalidArgument}, {"catalog.example.com:443", projectionTenant, catalog.CodeInvalidArgument}, {"unknown.example", projectionTenant, catalog.CodeNotFound}, {"disabled.example", projectionTenant, catalog.CodeNotFound}, {"unwired.example", projectionTenant, catalog.CodeNotFound}, {publicFixtureHost, projectionNext, catalog.CodeNotFound},
	} {
		t.Run(tc.host+tc.tenant, func(t *testing.T) {
			got, err := s.publicationForControl(tc.tenant, tc.host)
			if tc.code != "" {
				if got != nil || catalog.ErrorCodeOf(err) != tc.code {
					t.Fatalf("control binding=%v %v", got, err)
				}
				return
			}
			if err != nil || got != manager {
				t.Fatal("exact binding lost")
			}
		})
	}
}
