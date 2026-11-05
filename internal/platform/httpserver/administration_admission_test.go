// Admission units reject invalid requests before any domain or store access.
package httpserver

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestAdministrationPaginationAdmission(t *testing.T) {
	t.Parallel()
	s := projectionServer(nil)
	actor := projectionActor(identity.RoleTenantAdmin)
	for _, tc := range []struct {
		purpose, path, parent, order string
		filters                      map[string]string
		handler                      http.HandlerFunc
		badPositions                 [][]string
	}{
		{"feedback-own", "/api/v1/feedback", "", "id", map[string]string{"resourceId": "", "category": "", "state": ""}, s.handleFeedbackList, [][]string{{projectionItem, projectionNext}}},
		{"role-assignments", "/api/v1/role-assignments", "", "subject,namespaceId,role,id", map[string]string{}, s.handleRoleAssignmentsList, [][]string{{"member", projectionItem}}},
		{"jobs", "/api/v1/jobs", "", "createdAt desc,id desc", map[string]string{}, s.handleJobsList, [][]string{{projectionItem}, {"not-a-time", projectionItem}}},
		{"webhooks", "/api/v1/webhooks", "", "name,id", map[string]string{}, s.handleWebhooksList, [][]string{{projectionItem}}},
		{"webhook-deliveries", "/api/v1/webhooks/" + projectionItem + "/deliveries", projectionItem, "updatedAt desc,id desc", map[string]string{}, s.handleWebhookDeliveries, [][]string{{projectionItem}, {"not-a-time", projectionItem}}},
	} {
		t.Run(tc.purpose, func(t *testing.T) {
			queries := make([]string, 0, 2+len(tc.badPositions))
			queries = append(queries, "?limit=0", "?limit=1&cursor=invalid")
			for _, position := range tc.badPositions {
				binding := adminCursorBinding{Purpose: tc.purpose, Parent: tc.parent, Order: tc.order, Filters: tc.filters, AuthorizationDigest: fixtureDecisionDigest(t, t.Context(), actor), Limit: 1}
				token, err := s.encodeAdminCursor(adminPageRequest{Limit: 1, binding: binding}, position)
				if err != nil {
					t.Fatal(err)
				}
				query := "?limit=1&cursor=" + token
				request := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodGet, tc.path+query, nil)
				page, err := s.parseAdminPage(request, tc.purpose, tc.parent, tc.order, tc.filters)
				if err != nil || !slices.Equal(page.Position, position) {
					t.Fatalf("position rejected before handler: %+v %v", page, err)
				}
				queries = append(queries, query)
			}
			for _, query := range queries {
				r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodGet, tc.path+query, nil)
				r.SetPathValue("webhookId", tc.parent)
				w := httptest.NewRecorder()
				tc.handler(w, r)
				requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
			}
		})
	}
	for _, query := range []string{"?limit=0", "?unknown=value", "?cursor=", "?cursor=a&cursor=b", "?cursor=" + strings.Repeat("a", 2049)} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/memberships"+query, nil)
		w := httptest.NewRecorder()
		s.handleMembershipsList(w, r)
		requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	}
}

func TestAdministrationCommandsRequireRevision(t *testing.T) {
	t.Parallel()
	s := projectionServer(nil)
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"feedback edit", s.handleFeedbackEdit}, {"feedback command", s.handleFeedbackCommand},
		{"membership transition", func(w http.ResponseWriter, r *http.Request) {
			s.handleMembershipTransition(w, r, identity.MembershipSuspend)
		}},
		{"role revoke", s.handleRoleAssignmentDelete},
		{"webhook patch", s.handleWebhookPatch}, {"webhook disable", s.handleWebhookDelete}, {"webhook rotate", s.handleWebhookRotateSecret}, {"webhook test", s.handleWebhookTest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/command", nil)
			r.SetPathValue("feedbackCommand", projectionItem+":address")
			w := httptest.NewRecorder()
			tc.handler(w, r)
			requireProjectionProblem(t, w, 412, catalog.CodePreconditionFailed)
		})
	}
}
func TestAdministrationDispatchAdmission(t *testing.T) {
	t.Parallel()
	s := projectionServer(nil)
	for _, tc := range []struct {
		name, key, value string
		handler          http.HandlerFunc
		status           int
		code             catalog.ErrorCode
	}{
		{"membership invalid ID", "membershipCommand", "invalid:suspend", s.dispatchMembershipCommand, 400, catalog.CodeInvalidArgument},
		{"membership missing ID", "membershipCommand", ":suspend", s.dispatchMembershipCommand, 404, catalog.CodeNotFound},
		{"membership unknown command", "membershipCommand", projectionItem + ":unknown", s.dispatchMembershipCommand, 404, catalog.CodeNotFound},
		{"feedback invalid ID", "feedbackCommand", "invalid:address", s.handleFeedbackCommand, 400, catalog.CodeInvalidArgument},
		{"feedback unknown command", "feedbackCommand", projectionItem + ":unknown", s.handleFeedbackCommand, 404, catalog.CodeNotFound},
		{"feedback missing command", "feedbackCommand", projectionItem, s.handleFeedbackCommand, 404, catalog.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/command", nil)
			r.SetPathValue(tc.key, tc.value)
			w := httptest.NewRecorder()
			tc.handler(w, r)
			requireProjectionProblem(t, w, tc.status, tc.code)
		})
	}
	// An internal caller cannot select an unsupported lifecycle transition.
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/command", strings.NewReader(`{"reason":"Access maintenance"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", `"rev-4"`)
	w := httptest.NewRecorder()
	s.handleMembershipTransition(w, r, identity.MembershipTransition("unknown"))
	requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	mux := http.NewServeMux()
	s.feedbackRoutes(mux)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/feedback", nil))
	if w.Code != 404 {
		t.Fatal("disabled feedback registered routes")
	}
}
