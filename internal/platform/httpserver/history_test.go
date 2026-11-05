package httpserver

import (
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestHistoryHandlersRejectMalformedPaginationBeforeServiceAccess(t *testing.T) {
	t.Parallel()
	const parent = "00000000-0000-7000-8000-000000000010"
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "reader", Roles: []identity.Role{identity.RoleReader}}
	// Intentionally no Catalog service: invalid pagination must be rejected
	// entirely by the handler before any repository or domain operation.
	server := &Server{cursorKey: []byte("0123456789abcdef0123456789abcdef"), now: func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }, logger: slog.New(slog.DiscardHandler)}
	for _, test := range []struct {
		purpose string
		pathKey string
		handler http.HandlerFunc
	}{
		{"history-Versions", "resourceId", server.handleVersionsList},
		{"history-VersionRevisions", "versionId", server.handleVersionRevisionsList},
		{"history-Listings", "resourceId", server.handleListingsList},
		{"history-ListingRevisions", "listingId", server.handleListingRevisionsList},
		{"history-Releases", "listingId", server.handleReleasesList},
		{"review-submissions", "", server.handleSubmissionsList},
	} {
		t.Run(test.purpose, func(t *testing.T) {
			binding := adminCursorBinding{Purpose: test.purpose, Parent: parent, AuthorizationDigest: fixtureDecisionDigest(t, t.Context(), actor), Order: "id", Limit: 1}
			if test.pathKey == "" {
				binding.Parent = ""
				binding.Filters = map[string]string{"listingId": "", "state": ""}
			}
			queries := make([]string, 0, 4)
			queries = append(queries, "?limit=0", "?limit=1&cursor=invalid")
			for _, position := range [][]string{{"invalid-uuid"}, {parent, parent}} {
				token, err := server.encodeAdminCursor(adminPageRequest{Limit: 1, binding: binding}, position)
				if err != nil {
					t.Fatal(err)
				}
				// Prove the signed envelope is accepted, so the handler must reject
				// the history position rather than an unrelated cursor-binding error.
				request := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodGet, "/history?limit=1&cursor="+token, nil)
				page, err := server.parseAdminPage(request, binding.Purpose, binding.Parent, binding.Order, binding.Filters)
				if err != nil || !slices.Equal(page.Position, position) {
					t.Fatalf("signed position did not reach history validation: %#v, %v", page.Position, err)
				}
				queries = append(queries, "?limit=1&cursor="+token)
			}
			for _, query := range queries {
				request := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodGet, "/history"+query, nil)
				if test.pathKey != "" {
					request.SetPathValue(test.pathKey, parent)
				}
				response := httptest.NewRecorder()
				test.handler(response, request)
				var problem struct {
					Code catalog.ErrorCode `json:"code"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || response.Code != http.StatusBadRequest || problem.Code != catalog.CodeInvalidArgument {
					t.Fatalf("invalid pagination returned %d %s (%v)", response.Code, response.Body, err)
				}
			}
		})
	}
}

func TestHistoryPositionPreservesValidatedContinuation(t *testing.T) {
	t.Parallel()
	const after = "00000000-0000-7000-8000-000000000010"
	got, err := historyPosition(adminPageRequest{Limit: 3, Position: []string{after}})
	if err != nil || got.Limit != 3 || got.AfterID != after {
		t.Fatalf("history continuation = %#v, %v", got, err)
	}
}
