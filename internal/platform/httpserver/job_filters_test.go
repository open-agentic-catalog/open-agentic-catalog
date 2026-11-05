// These handler units use an injected repository and no listener or database.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestJobFiltersAndCursorBinding(t *testing.T) {
	t.Parallel()
	actor := projectionActor(identity.RoleTenantAdmin)
	ctx := identityfixture.WithActor(t.Context(), actor)
	item := projectionJob()
	item.ParentJobID = projectionNext
	next := item
	next.ID = "00000000-0000-7000-8000-000000000009"
	next.CreatedAt = item.CreatedAt.Add(-time.Second)
	calls := 0
	s := jobProjectionServer(t, jobProjectionStore{list: func(_ context.Context, tenant string, p jobs.ListRequest) ([]jobs.Job, error) {
		calls++
		if tenant != projectionTenant || p.Kind != "publication.generate" || p.State != jobs.StateQueued || p.ParentJobID != projectionNext || p.Limit != 2 {
			t.Fatalf("filter binding: tenant=%s, %+v", tenant, p)
		}
		if calls == 1 {
			if p.AfterID != "" {
				t.Fatal("initial position")
			}
			return []jobs.Job{item, next}, nil
		}
		if p.AfterID != item.ID || p.AfterCreatedAt == nil || !p.AfterCreatedAt.Equal(item.CreatedAt) {
			t.Fatal("lost continuation")
		}
		return nil, nil
	}})
	query := url.Values{"limit": {"1"}, "kind": {"publication.generate"}, "state": {"queued"}, "parentJobId": {projectionNext}}
	request := func(q url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleJobsList(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/jobs?"+q.Encode(), nil))
		return w
	}
	w := request(query)
	var page struct {
		Items      []jobs.View `json:"items"`
		NextCursor string      `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("first page: %d %s %v", w.Code, w.Body, err)
	}
	query.Set("cursor", page.NextCursor)
	w = request(query)
	if w.Code != 200 {
		t.Fatalf("same binding rejected: %s", w.Body)
	}
	for key, value := range map[string]string{"kind": "source.sync", "state": "failed", "parentJobId": projectionItem, "limit": "2"} {
		changed, err := url.ParseQuery(query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		changed.Set(key, value)
		requireProjectionProblem(t, request(changed), http.StatusBadRequest, catalog.CodeInvalidArgument)
	}
	if calls != 2 {
		t.Fatalf("changed cursor reached store: %d", calls)
	}
}

func TestJobFilterValidationBeforeRepository(t *testing.T) {
	t.Parallel()
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
	s := jobProjectionServer(t, jobProjectionStore{list: func(context.Context, string, jobs.ListRequest) ([]jobs.Job, error) {
		t.Fatal("invalid query reached repository")
		return nil, nil
	}})
	for _, query := range []string{"kind=", "state=", "parentJobId=", "kind=a&kind=b", "state=queued&state=failed", "parentJobId=x&parentJobId=y", "kind=" + url.QueryEscape(strings.Repeat("界", 101)), "kind=a%00b", "kind=%FF", "state=Queued", "state=cancellation_requested", "parentJobId=no-uuid"} {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.handleJobsList(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/jobs?"+query, nil))
			requireProjectionProblem(t, w, http.StatusBadRequest, catalog.CodeInvalidArgument)
		})
	}
}
