// Recent feedback HTTP units use recorders and an injected one-page store.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestRecentFeedbackCursorBindsOrderFiltersAndCaller(t *testing.T) {
	t.Parallel()
	actor := projectionActor(identity.RoleReader)
	ctx := identityfixture.WithActor(t.Context(), actor)
	newer, older := feedbackProjectionRecord(), feedbackProjectionRecord()
	newer.ID, newer.UpdatedAt = projectionNext, projectionTime.Add(time.Hour)
	reads := 0
	s := feedbackProjectionServer(t, feedbackProjectionStore{list: func(_ context.Context, tenant string, q feedback.ListInput) ([]feedback.Record, error) {
		reads++
		if tenant != actor.TenantID || q.MembershipID != actor.MembershipID || q.Order != feedback.OrderUpdatedDesc || q.State != "addressed" || q.Category != "problem" || q.Limit != 2 {
			t.Fatalf("lost own-feedback selection: %+v", q)
		}
		if q.AfterUpdatedAt == nil {
			if q.AfterID != "" {
				t.Fatal("partial first-page position")
			}
			return []feedback.Record{newer, older}, nil
		}
		if !q.AfterUpdatedAt.Equal(newer.UpdatedAt) || q.AfterID != newer.ID {
			t.Fatalf("lost tuple: %+v", q)
		}
		return []feedback.Record{older}, nil
	}}, ctx)
	query := url.Values{"order": {"updated_desc"}, "state": {"addressed"}, "category": {"problem"}, "limit": {"1"}}
	invoke := func(path string, values url.Values, other bool) *httptest.ResponseRecorder {
		t.Helper()
		requestContext := ctx
		if other {
			changed := actor
			changed.MembershipID = projectionResource
			requestContext = identityfixture.WithActor(ctx, changed)
		}
		w := httptest.NewRecorder()
		s.handleFeedbackList(w, httptest.NewRequestWithContext(requestContext, "GET", path+"?"+values.Encode(), nil))
		return w
	}
	w := invoke("/api/v1/feedback", query, false)
	var first cursorPage[feedback.Record]
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &first) != nil || len(first.Items) != 1 || first.Items[0].ID != newer.ID || first.NextCursor == "" {
		t.Fatalf("first page: %d %s", w.Code, w.Body)
	}
	query.Set("cursor", first.NextCursor)
	w = invoke("/api/v1/feedback", query, false)
	var last cursorPage[feedback.Record]
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &last) != nil || len(last.Items) != 1 || last.Items[0].ID != older.ID || last.NextCursor != "" {
		t.Fatalf("last page: %d %s", w.Code, w.Body)
	}
	for _, change := range []struct{ key, value string }{{"order", "id"}, {"state", "open"}, {"category", "other"}, {"resourceId", projectionResource}, {"limit", "2"}} {
		old := query.Get(change.key)
		query.Set(change.key, change.value)
		requireProjectionProblem(t, invoke("/api/v1/feedback", query, false), 400, catalog.CodeInvalidArgument)
		query.Set(change.key, old)
	}
	requireProjectionProblem(t, invoke("/api/v1/feedback", query, true), 400, catalog.CodeInvalidArgument)
	requireProjectionProblem(t, invoke("/api/v1/feedback/inbox", query, false), 400, catalog.CodeInvalidArgument)
	query.Del("cursor")
	for _, value := range []string{"", "random", "UPDATED_DESC"} {
		query.Set("order", value)
		requireProjectionProblem(t, invoke("/api/v1/feedback", query, false), 400, catalog.CodeInvalidArgument)
	}
	query.Set("order", "updated_desc")
	p, err := s.parseAdminPage(httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/feedback?"+query.Encode(), nil), "feedback-own", "", feedback.OrderUpdatedDesc, map[string]string{"order": "updated_desc", "category": "problem", "state": "addressed", "resourceId": ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, position := range [][]string{{newer.ID}, {"bad-time", newer.ID}, {newer.UpdatedAt.Format(time.RFC3339Nano), "bad-id"}} {
		cursor, err := s.encodeAdminCursor(p, position)
		if err != nil {
			t.Fatal(err)
		}
		query.Set("cursor", cursor)
		requireProjectionProblem(t, invoke("/api/v1/feedback", query, false), 400, catalog.CodeInvalidArgument)
	}
	if reads != 2 {
		t.Fatalf("invalid continuation reached the store: %d reads", reads)
	}
}
