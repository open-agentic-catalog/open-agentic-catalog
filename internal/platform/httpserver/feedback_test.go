// Unit tests exercise isolated validation and controlled dependencies.
package httpserver

import (
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestFeedbackScopeAdmission(t *testing.T) {
	for _, test := range []struct{ method, path, scope string }{{"GET", "/api/v1/feedback", "catalog.read"}, {"GET", "/api/v1/feedback/inbox", "tenant.admin"}, {"POST", "/api/v1/feedback", "feedback.write"}, {"PATCH", "/api/v1/feedback/id", "feedback.write"}, {"POST", "/api/v1/feedback/id:withdraw", "feedback.write"}, {"POST", "/api/v1/feedback/id:address", "tenant.admin"}, {"POST", "/api/v1/feedback/id:reopen", "tenant.admin"}, {"POST", "/api/v1/feedback/id:remove", "tenant.admin"}} {
		t.Run(fmt.Sprint(test.method, test.path), func(t *testing.T) {
			if got := requiredOAuthScope(test.method, test.path); got != test.scope {
				t.Fatalf("scope=%s want %s", got, test.scope)
			}
			for _, claim := range []string{"", "unrelated.scope", test.scope} {
				r := httptest.NewRequestWithContext(t.Context(), test.method, test.path, nil)
				r = r.WithContext(identityfixture.WithActor(r.Context(), identity.Actor{Claims: map[string]string{"scope:" + claim: "true"}}))
				w := httptest.NewRecorder()
				authorizationScopes(true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, r)
				want := http.StatusForbidden
				if claim == test.scope {
					want = http.StatusNoContent
				}
				if w.Code != want {
					t.Fatalf("claim=%q status=%d want=%d", claim, w.Code, want)
				}
			}
		})
	}
}
