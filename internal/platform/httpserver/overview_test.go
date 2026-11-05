package httpserver

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOverviewOAuthCeilings(t *testing.T) {
	for _, name := range []string{"resources", "releases", "namespaces", "publishers", "indexed", "reviews", "publication", "sources", "jobs", "webhook-endpoints", "webhook-deliveries", "administration", "feedback", "ratings"} {
		path := "/api/v1/overview/" + name
		scope := "catalog.read"
		if name == "publishers" {
			scope = "publishers.read"
		}
		if strings.HasPrefix(name, "webhook-") {
			scope = "webhooks.manage"
		}
		if name == "sources" {
			scope = "sources.read"
		}
		if name == "administration" || name == "feedback" || name == "ratings" {
			scope = "tenant.admin"
		}
		if got := requiredOAuthScope(http.MethodGet, path); got != scope {
			t.Fatalf("%s scope=%s", name, got)
		}
		for _, claim := range []string{"", "tenant.admin", scope} {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			r = r.WithContext(identityfixture.WithActor(r.Context(), identity.Actor{Claims: map[string]string{"scope:" + claim: "true"}}))
			w := httptest.NewRecorder()
			authorizationScopes(true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, r)
			want := 403
			if claim == scope {
				want = 204
			}
			if w.Code != want {
				t.Fatalf("%s %s: %d", name, claim, w.Code)
			}
		}
	}
}

func TestReviewUnreleasedFilterBindsContinuation(t *testing.T) {
	server := &Server{cursorKey: []byte("0123456789abcdef0123456789abcdef"), now: func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "review-reader", Roles: []identity.Role{identity.RoleReader}}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/review-submissions?state=approved&unreleased=true&limit=1", nil)
	request = request.WithContext(identityfixture.WithActor(request.Context(), actor))
	filters := map[string]string{"listingId": "", "state": "approved", "unreleased": "true"}
	page, err := server.parseAdminPage(request, "review-submissions", "", "id", filters)
	if err != nil {
		t.Fatal(err)
	}
	token, err := server.encodeAdminCursor(page, []string{"00000000-0000-7000-8000-000000000010"})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"true", "false", ""} {
		candidate := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/review-submissions?state=approved&unreleased="+value+"&limit=1&cursor="+token, nil)
		candidate = candidate.WithContext(identityfixture.WithActor(candidate.Context(), actor))
		_, err = server.parseAdminPage(candidate, "review-submissions", "", "id", map[string]string{"listingId": "", "state": "approved", "unreleased": value})
		if value == "true" && err != nil || value != "true" && catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal(value, err)
		}
	}
}
