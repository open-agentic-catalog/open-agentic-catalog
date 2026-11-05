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
	next := item
	next.ID = projectionNext
	calls := 0
	server := federationProjectionServer(t, federationProjectionStore{sources: func(_ context.Context, tenant string, q federation.SourceListRequest) ([]federation.Source, error) {
		calls++
		if tenant != projectionTenant || q.DisplayName != "Exact Name" || q.Status != federation.SourcePaused || q.Limit != 2 {
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
	query := url.Values{"limit": {"1"}, "displayName": {"Exact Name"}, "status": {"paused"}}
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
	query.Set("cursor", page.NextCursor)
	if w = request(query); w.Code != 200 {
		t.Fatalf("continuation %s", w.Body)
	}
	for key, value := range map[string]string{"displayName": "Exact", "status": "active", "limit": "2"} {
		changed, err := url.ParseQuery(query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		changed.Set(key, value)
		requireProjectionProblem(t, request(changed), 400, catalog.CodeInvalidArgument)
	}
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
	for _, query := range []string{"displayName=", "status=", "displayName=a&displayName=b", "status=active&status=paused", "status=removed", "status=Active", "displayName=" + url.QueryEscape(strings.Repeat("界", 161)), "displayName=%FF", "displayName=a%00b"} {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			server.handleSourcesList(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/federation/sources?"+query, nil))
			requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		})
	}
}
