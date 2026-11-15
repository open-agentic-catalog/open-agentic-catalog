// Exact namespace filters bind the review queue's finite query and continuation.
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
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestReviewNamespaceContractAndCursorBinding(t *testing.T) {
	t.Parallel()
	const namespace = "aaaaaaaa-0000-7000-8000-000000000010"
	reads := 0
	repo := historyProjectionRepository{submissions: func(_ context.Context, tenant string, request catalog.SubmissionListRequest) ([]catalog.ReviewSubmission, error) {
		reads++
		if tenant != projectionActor(identity.RoleReader).TenantID || request.NamespaceID != namespace || request.State != catalog.SubmissionApproved || !request.Unreleased || request.Limit != 2 {
			t.Fatalf("request lost filter, tenant or sentinel: %+v", request)
		}
		if request.AfterID == "" {
			return []catalog.ReviewSubmission{{ID: projectionItem}, {ID: projectionNext}}, nil
		}
		if request.AfterID != projectionItem {
			t.Fatalf("position %s", request.AfterID)
		}
		return []catalog.ReviewSubmission{{ID: projectionNext}}, nil
	}}
	s := projectionServer(projectionCatalog(t, repo, nil))
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
	invoke := func(raw string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleSubmissionsList(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/review-submissions?"+raw, nil))
		return w
	}
	query := url.Values{"namespaceId": {strings.ToUpper(namespace)}, "state": {"approved"}, "unreleased": {"true"}, "limit": {"1"}}
	w := invoke(query.Encode())
	var first cursorPage[catalog.ReviewSubmission]
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || len(first.Items) != 1 || first.Items[0].ID != projectionItem || first.NextCursor == "" {
		t.Fatalf("first page: %d %s", w.Code, w.Body)
	}
	query.Set("namespaceId", namespace)
	query.Set("cursor", first.NextCursor)
	w = invoke(query.Encode())
	var last cursorPage[catalog.ReviewSubmission]
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &last) != nil || len(last.Items) != 1 || last.Items[0].ID != projectionNext || last.NextCursor != "" {
		t.Fatalf("last page: %d %s", w.Code, w.Body)
	}
	for _, changed := range []string{"", "aaaaaaaa-0000-7000-8000-000000000011"} {
		query.Set("namespaceId", changed)
		if changed == "" {
			query.Del("namespaceId")
		}
		requireProjectionProblem(t, invoke(query.Encode()), 400, catalog.CodeInvalidArgument)
	}
	for _, raw := range []string{"namespaceId=", "namespaceId=bad", "namespaceId=null", "namespaceId=" + namespace + "&namespaceId=" + namespace, "NamespaceId=" + namespace, "namespaceId=%zz"} {
		requireProjectionProblem(t, invoke(raw), 400, catalog.CodeInvalidArgument)
	}
	if reads != 2 {
		t.Fatalf("invalid query reached repository %d times", reads)
	}
}
