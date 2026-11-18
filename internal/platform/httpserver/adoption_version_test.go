package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// No listener or idempotency store: check the outer admission which must run
// even when an inner cache could otherwise disclose a completed response.
func TestVersionAdoptionOuterAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, method, path                  string
		enabled, enforce, sourceScope, pass bool
	}{
		{"default off append", "POST", "/api/v1/resources/00000000-0000-7000-8000-000000000001/versions:adopt", false, false, false, false},
		{"default off lookup", "GET", "/api/v1/observations/00000000-0000-7000-8000-000000000001/adoption-target", false, false, false, false},
		{"default off HEAD lookup", "HEAD", "/api/v1/observations/00000000-0000-7000-8000-000000000001/adoption-target", false, false, false, false},
		{"enabled development", "POST", "/api/v1/resources/00000000-0000-7000-8000-000000000001/versions:adopt", true, false, false, true},
		{"missing source ceiling", "POST", "/api/v1/resources/00000000-0000-7000-8000-000000000001/versions:adopt", true, true, false, false},
		{"missing HEAD source ceiling", "HEAD", "/api/v1/observations/00000000-0000-7000-8000-000000000001/adoption-target", true, true, false, false},
		{"HEAD source ceiling", "HEAD", "/api/v1/observations/00000000-0000-7000-8000-000000000001/adoption-target", true, true, true, true},
		{"source ceiling", "POST", "/api/v1/resources/00000000-0000-7000-8000-000000000001/versions:adopt", true, true, true, true},
		{"ordinary version unaffected", "POST", "/api/v1/resources/00000000-0000-7000-8000-000000000001/versions", false, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := new(federation.Manager)
			if err := federation.WithVersionAdoption(tc.enabled)(manager); err != nil {
				t.Fatal(err)
			}
			s := Server{federation: manager, enforceOAuthScopes: tc.enforce}
			actor := identity.Actor{Claims: map[string]string{}}
			if tc.sourceScope {
				actor.Claims["scope:sources.read"] = "true"
			}
			request := httptest.NewRequestWithContext(identity.WithActor(t.Context(), actor), tc.method, tc.path, nil)
			response := httptest.NewRecorder()
			called := false
			s.versionAdoptionAdmission(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(response, request)
			if called != tc.pass || (!tc.pass && response.Code != 403) {
				t.Fatal("outer admission", called, response.Code)
			}
		})
	}
}
