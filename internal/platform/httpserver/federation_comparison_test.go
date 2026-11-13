// Direct handlers qualify exact selection/cursor admission without a listener
// or database; composed source history has separate functional coverage.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type comparisonProjectionStore struct {
	federation.Store
	compare func(context.Context, string, federation.SnapshotComparisonRequest) (federation.SnapshotComparison, error)
}

func (s comparisonProjectionStore) CompareSnapshots(ctx context.Context, tenant string, r federation.SnapshotComparisonRequest) (federation.SnapshotComparison, error) {
	return s.compare(ctx, tenant, r)
}

func TestSourceSnapshotComparisonCursorAndAuthority(t *testing.T) {
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleSourceAdmin))
	const beforeID = "00000000-0000-7000-8000-0000000000ab"
	const afterID = "00000000-0000-7000-8000-0000000000cd"
	calls := 0
	repo := comparisonProjectionStore{compare: func(c context.Context, tenant string, r federation.SnapshotComparisonRequest) (federation.SnapshotComparison, error) {
		calls++
		if tenant != projectionTenant || r.BeforeID != beforeID || r.AfterID != afterID || r.Limit != 2 || !sameProjectionContext(c, ctx) {
			t.Fatalf("selection=%+v tenant=%s", r, tenant)
		}
		page := federation.SnapshotComparison{SourceID: projectionItem, SourceRevision: 3, Before: federation.SnapshotComparisonSummary{ID: r.BeforeID}, After: federation.SnapshotComparisonSummary{ID: r.AfterID}, Items: []federation.SnapshotEntryChange{{Position: 1, SourceKey: "a", Change: "added"}, {Position: 2, SourceKey: "b", Change: "absent"}}}
		if calls == 2 {
			if r.AfterPosition != 1 || r.ExpectedSourceRevision != 3 {
				t.Fatal("cursor lost revision or position")
			}
			page.Items = page.Items[1:]
		}
		return page, nil
	}}
	s := federationProjectionServer(t, repo, nil)
	request := func(c context.Context, before, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(c, http.MethodGet, "/api/v1/federation/snapshots/"+before+"/comparison?"+query, nil)
		r.SetPathValue("snapshotId", before)
		w := httptest.NewRecorder()
		s.withQueryParameters(s.withUUIDPathParameters(s.handleSourceSnapshotComparison, "snapshotId"), "otherSnapshotId", "view", "limit", "cursor")(w, r)
		return w
	}
	query := "otherSnapshotId=" + afterID + "&limit=1"
	// UUID spelling must not change the selected records or signed cursor
	// binding. PostgreSQL and the development memory store use the same IDs.
	w := request(ctx, strings.ToUpper(beforeID), "otherSnapshotId="+strings.ToUpper(afterID)+"&limit=1")
	var first struct {
		Items      []federation.SnapshotEntryChange `json:"items"`
		NextCursor string                           `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil || w.Code != 200 || len(first.Items) != 1 || first.Items[0].SourceKey != "a" || first.NextCursor == "" {
		t.Fatalf("first=%d %s %v", w.Code, w.Body, err)
	}
	w = request(ctx, beforeID, query+"&cursor="+url.QueryEscape(first.NextCursor))
	var second struct {
		Items      []federation.SnapshotEntryChange `json:"items"`
		NextCursor string                           `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil || w.Code != 200 || len(second.Items) != 1 || second.Items[0].SourceKey != "b" || second.NextCursor != "" {
		t.Fatalf("second=%d %s %v", w.Code, w.Body, err)
	}
	for _, q := range []string{query + "&view=all", strings.ReplaceAll(query, afterID, projectionItem), strings.ReplaceAll(query, "limit=1", "limit=2")} {
		w = request(ctx, beforeID, q+"&cursor="+url.QueryEscape(first.NextCursor))
		if w.Code != 400 || calls != 2 {
			t.Fatalf("rebound cursor accepted: %d %s", w.Code, w.Body)
		}
	}
	reader := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
	w = request(reader, beforeID, query)
	if w.Code != 403 || calls != 2 {
		t.Fatalf("summary authority bypass: %d %s", w.Code, w.Body)
	}
	w = request(reader, beforeID, query+"&cursor="+url.QueryEscape(first.NextCursor))
	if w.Code != 403 || calls != 2 {
		t.Fatalf("revoked cursor accepted: %d %s", w.Code, w.Body)
	}
}

func TestSourceSnapshotComparisonRejectsMalformedSelectionsBeforeStore(t *testing.T) {
	s := federationProjectionServer(t, comparisonProjectionStore{compare: func(context.Context, string, federation.SnapshotComparisonRequest) (federation.SnapshotComparison, error) {
		t.Fatal("invalid selection reached storage")
		return federation.SnapshotComparison{}, nil
	}}, nil)
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleSourceAdmin))
	for _, query := range []string{"", "otherSnapshotId=invalid", "otherSnapshotId=" + projectionNext + "&view=", "otherSnapshotId=" + projectionNext + "&view=partial", "otherSnapshotId=" + projectionNext + "&limit=101", "otherSnapshotId=" + projectionNext + "&otherSnapshotId=" + projectionNext, "otherSnapshotId=" + projectionNext + "&extra=secret", "otherSnapshotId=" + projectionNext + "&cursor=invalid"} {
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/comparison?"+query, nil)
		r.SetPathValue("snapshotId", projectionResource)
		w := httptest.NewRecorder()
		s.withQueryParameters(s.handleSourceSnapshotComparison, "otherSnapshotId", "view", "limit", "cursor")(w, r)
		if w.Code != 400 || !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Fatalf("query=%s result=%d %s", query, w.Code, w.Body)
		}
	}
}
