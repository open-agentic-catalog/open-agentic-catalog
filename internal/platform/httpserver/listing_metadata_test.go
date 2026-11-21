package httpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type listingMetadataRepo struct {
	catalog.Repository
	calls []catalog.ListingMetadataQuery
}

func (r *listingMetadataRepo) ListListingMetadataGaps(_ context.Context, _ string, q catalog.ListingMetadataQuery) ([]catalog.ListingMetadataGap, error) {
	r.calls = append(r.calls, q)
	if q.AfterID == "" {
		return []catalog.ListingMetadataGap{{ListingID: projectionItem}, {ListingID: projectionNext}}, nil
	}
	return []catalog.ListingMetadataGap{{ListingID: projectionNext}}, nil
}

func TestListingMetadataSelectionAndContinuation(t *testing.T) {
	s, request := personalHandlerFixture(t, personalHandlerStore{})
	repo := &listingMetadataRepo{}
	s.catalog = projectionCatalog(t, repo, nil)
	get := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleListingMetadataGaps(w, request("GET", "/api/v1/listing-metadata-gaps?"+query, ""))
		return w
	}
	w := get("checks=support,license&limit=1")
	var first cursorPage[catalog.ListingMetadataGap]
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil || w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || len(first.Items) != 1 || first.Items[0].ListingID != projectionItem || first.NextCursor == "" {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	cursor := "&cursor=" + url.QueryEscape(first.NextCursor)
	w = get("checks=license,support&limit=1" + cursor)
	var last cursorPage[catalog.ListingMetadataGap]
	if err := json.Unmarshal(w.Body.Bytes(), &last); err != nil || w.Code != 200 || len(last.Items) != 1 || last.Items[0].ListingID != projectionNext || last.NextCursor != "" || repo.calls[1].AfterID != projectionItem {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	for _, query := range []string{"checks=support&limit=1" + cursor, "checks=support,license&limit=2" + cursor, "checks=support,license&limit=1&kind=agent" + cursor, "checks=support,license&limit=1&namespaceId=" + projectionItem + cursor, "checks=support&checks=license", "checks=license,license", "checks=", "checks=tags", "kind=", "kind=unknown", "namespaceId=", "namespaceId=wrong", "limit=0", "limit=101", "cursor=wrong", "url=unexpected"} {
		requireProjectionProblem(t, get(query), 400, catalog.CodeInvalidArgument)
	}
	if len(repo.calls) != 2 {
		t.Fatal("invalid criteria reached storage", repo.calls)
	}
	w = get("")
	if w.Code != 200 || repo.calls[2].Limit != 21 || repo.calls[2].Checks != (catalog.ListingMetadataChecks{Documentation: true, Support: true, License: true}) {
		t.Fatal(w.Code, repo.calls)
	}
}
