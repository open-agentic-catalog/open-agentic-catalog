package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
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

type upstreamPageStore struct {
	federation.Store
	selectPage func(string, federation.UpstreamChangesQuery) ([]federation.UpstreamChange, error)
}

func (s upstreamPageStore) ListUpstreamChanges(_ context.Context, tenant string, query federation.UpstreamChangesQuery) ([]federation.UpstreamChange, error) {
	return s.selectPage(tenant, query)
}

func TestUpstreamChangesHandlerBindsExactFiltersAndPosition(t *testing.T) {
	const source = "00000000-0000-7000-8000-0000000000af"
	const path = "/api/v1/upstream-changes"
	query := federation.UpstreamChangesQuery{Check: "entry_comparison", SourceID: source, NamespaceID: projectionResource, Limit: 1}
	var calls int
	store := upstreamPageStore{selectPage: func(tenant string, q federation.UpstreamChangesQuery) ([]federation.UpstreamChange, error) {
		calls++
		if tenant != projectionTenant || q != query {
			t.Fatalf("selection = %s %+v, want %s %+v", tenant, q, projectionTenant, query)
		}
		if q.AfterID != "" {
			return []federation.UpstreamChange{}, nil
		}
		return []federation.UpstreamChange{
			{Occurrence: federation.ObservationOccurrence{ObservationID: projectionItem}},
			{Occurrence: federation.ObservationOccurrence{ObservationID: projectionNext}},
		}, nil
	}}
	s := federationProjectionServer(t, store, nil)
	if err := federation.WithObservationAdopter(projectionCatalog(t, nil, nil))(s.federation); err != nil {
		t.Fatal(err)
	}
	if err := federation.WithVersionAdoption(true)(s.federation); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
	get := func(queryString string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleUpstreamChanges(w, httptest.NewRequestWithContext(ctx, "GET", path+"?"+queryString, nil))
		return w
	}
	base := "limit=1&check=entry_comparison&namespaceId=" + projectionResource + "&sourceId="
	w := get(base + strings.ToUpper(source))
	var first cursorPage[federation.UpstreamChange]
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil || w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || len(first.Items) != 1 || first.Items[0].Occurrence.ObservationID != projectionItem || first.NextCursor == "" {
		t.Fatal("first page", w.Code, first, err)
	}
	query.AfterID = projectionItem
	w = get(base + source + "&cursor=" + url.QueryEscape(first.NextCursor))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"items":[]}` {
		t.Fatal("last page", w.Code, w.Body.String())
	}
	p, err := s.parseAdminPage(httptest.NewRequestWithContext(ctx, "GET", path, nil), "upstream-changes", "", "observation-id", map[string]string{"check": "all", "sourceId": "", "namespaceId": "", "observationId": ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, position := range [][]string{{"not-a-uuid"}, {projectionItem, projectionNext}} {
		token, err := s.encodeAdminCursor(p, position)
		if err != nil {
			t.Fatal(err)
		}
		requireProjectionProblem(t, get("cursor="+url.QueryEscape(token)), 400, catalog.CodeInvalidArgument)
	}
	for _, invalid := range []string{"check=", "sourceId=bad", "namespaceId=", "observationId=bad", "cursor=bad"} {
		requireProjectionProblem(t, get(invalid), 400, catalog.CodeInvalidArgument)
	}
	s.enforceOAuthScopes = true
	requireProjectionProblem(t, get(""), 403, catalog.CodeForbidden)
	if err := federation.WithVersionAdoption(false)(s.federation); err != nil {
		t.Fatal(err)
	}
	s.enforceOAuthScopes = false
	requireProjectionProblem(t, get(""), 403, catalog.CodeForbidden)
	if calls != 2 {
		t.Fatalf("rejected requests reached inventory: %d selections", calls)
	}
}
