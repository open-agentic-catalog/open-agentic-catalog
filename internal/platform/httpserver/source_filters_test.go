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

func TestSourceFiltersAndCursorBinding(t *testing.T) {
	t.Parallel()
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleSourceAdmin))
	item := federationFixtureSource()
	item.ID = projectionItem
	item.DisplayName = "Exact Name"
	item.Status = federation.SourcePaused
	item.LatestCompletedSync = &federation.SourceSyncAttempt{SnapshotID: projectionItem, Revision: item.Revision, State: federation.SnapshotFailed, CompletedAt: projectionTime}
	next := item
	next.ID = projectionNext
	next.LatestCompletedSync = &federation.SourceSyncAttempt{SnapshotID: projectionNext, Revision: next.Revision, State: federation.SnapshotFailed, CompletedAt: projectionTime}
	calls := 0
	server := federationProjectionServer(t, federationProjectionStore{sources: func(_ context.Context, tenant string, q federation.SourceListRequest) ([]federation.Source, error) {
		calls++
		if tenant != projectionTenant || q.DisplayName != "Exact Name" || q.Status != federation.SourcePaused || q.SyncState != federation.SnapshotFailed || q.Limit != 2 {
			t.Fatalf("binding: %s %+v", tenant, q)
		}
		if calls == 1 {
			return []federation.Source{item, next}, nil
		}
		if q.AfterID != item.ID || q.AfterDisplayName != item.DisplayName {
			t.Fatalf("position=%+v", q)
		}
		return nil, nil
	}}, nil)
	query := url.Values{"limit": {"1"}, "displayName": {"Exact Name"}, "status": {"paused"}, "syncState": {"failed"}}
	request := func(q url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		server.handleSourcesList(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/federation/sources?"+q.Encode(), nil))
		return w
	}
	w := request(query)
	var page struct {
		Items      []federation.Source `json:"items"`
		NextCursor string              `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page %d %s %v", w.Code, w.Body, err)
	}
	if page.Items[0].LatestCompletedSync == nil || *page.Items[0].LatestCompletedSync != *item.LatestCompletedSync {
		t.Fatal("matching attempt projection lost", page.Items[0])
	}
	query.Set("cursor", page.NextCursor)
	if w = request(query); w.Code != 200 {
		t.Fatalf("continuation %s", w.Body)
	}
	for key, value := range map[string]string{"displayName": "Exact", "status": "active", "syncState": "incomplete", "limit": "2"} {
		changed, err := url.ParseQuery(query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		changed.Set(key, value)
		requireProjectionProblem(t, request(changed), 400, catalog.CodeInvalidArgument)
	}
	// A valid filtered continuation is also fenced to its current tenant and
	// principal. Fresh reads still require Source-read authority.
	other := projectionActor(identity.RoleSourceAdmin)
	other.TenantID = projectionResource
	w = httptest.NewRecorder()
	server.handleSourcesList(w, httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), other), http.MethodGet, "/api/v1/federation/sources?"+query.Encode(), nil))
	requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	// Same principal/tenant after authority loss cannot reuse the filtered page.
	revoked := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
	w = httptest.NewRecorder()
	server.handleSourcesList(w, httptest.NewRequestWithContext(revoked, http.MethodGet, "/api/v1/federation/sources?"+query.Encode(), nil))
	requireProjectionProblem(t, w, 403, catalog.CodeForbidden)
	w = httptest.NewRecorder()
	server.handleSourcesList(w, httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader)), http.MethodGet, "/api/v1/federation/sources?syncState=failed", nil))
	requireProjectionProblem(t, w, 403, catalog.CodeForbidden)
	if calls != 2 {
		t.Fatalf("mismatched cursor reached store %d", calls)
	}
}

func TestSourceFilterValidationBeforeRepository(t *testing.T) {
	t.Parallel()
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleSourceAdmin))
	server := federationProjectionServer(t, federationProjectionStore{sources: func(context.Context, string, federation.SourceListRequest) ([]federation.Source, error) {
		t.Fatal("invalid query reached store")
		return nil, nil
	}}, nil)
	for _, query := range []string{"displayName=", "status=", "displayName=a&displayName=b", "status=active&status=paused", "status=removed", "status=Active", "syncState=", "syncState=complete", "syncState=running", "syncState=Failed", "syncState=failed&syncState=incomplete", "displayName=" + url.QueryEscape(strings.Repeat("界", 161)), "displayName=%FF", "displayName=a%00b"} {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			server.handleSourcesList(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/federation/sources?"+query, nil))
			requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		})
	}
}
