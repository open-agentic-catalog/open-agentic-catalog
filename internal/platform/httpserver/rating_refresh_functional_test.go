//go:build functional

package httpserver

import (
	"encoding/json"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

// Reuse the owned federation lifecycle fixture and advance only its injected clock.
func qualifyHTTPLiveRatingProofRefresh(t *testing.T, base string, clk *clock.Fake, responses routedFetcher, endpoint, original string) {
	t.Helper()
	key := http.MethodPost + " " + endpoint
	prior := responses[key]
	defer func() { responses[key] = prior }()
	var wire struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(original), &wire); err != nil {
		t.Fatal(err)
	}
	other := maps.Clone(wire.Results[0])
	other["identifier"] = "urn:air:remote.example:default:two"
	other["displayName"] = "Remote second agent"
	other["score"] = 90
	wire.Results = append(wire.Results, other)
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	responses[key] = federationHTTPResult(endpoint, "application/json", string(raw))
	query := map[string]any{"text": "remote agent", "origins": []string{"live"}, "kinds": []string{"agent"}, "tags": []string{"selected"}, "limit": 1}
	search := func(body map[string]any) catalog.CatalogSearchResponse {
		t.Helper()
		reply := requestJSON(t, base, http.MethodPost, "/api/v1/catalog/search", "reader", "", "", body)
		requireStatus(t, reply, http.StatusOK)
		requireControlSchema(t, "CatalogSearchResponse", reply.body)
		var page catalog.CatalogSearchResponse
		decodeResult(t, reply, &page)
		if len(page.Items) != 1 || page.Items[0].Ratings == nil || page.Items[0].Ratings.Receipt == "" {
			t.Fatal("missing live rating proof")
		}
		return page
	}
	first := search(query)
	if first.NextCursor == "" {
		t.Fatal("second live page was not exercised")
	}
	laterQuery := maps.Clone(query)
	laterQuery["cursor"] = first.NextCursor
	later := search(laterQuery)
	if later.Items[0].Identifier == first.Items[0].Identifier {
		t.Fatal("continuation repeated the first target")
	}
	ratingRead := func(item catalog.CatalogSearchResult) ratingRequest {
		return ratingRequest{Target: ratingItemAdmission(item).Target, Receipt: item.Ratings.Receipt}
	}
	for _, page := range []catalog.CatalogSearchResponse{first, later} {
		requireStatus(t, requestJSON(t, base, http.MethodPost, "/api/v1/ratings/read", "reader", "", "", ratingRead(page.Items[0])), http.StatusOK)
	}
	clk.Advance(6 * time.Minute)
	for _, saved := range []struct {
		query map[string]any
		page  catalog.CatalogSearchResponse
	}{{query, first}, {laterQuery, later}} {
		old := saved.page.Items[0]
		requireControlProblem(t, requestJSON(t, base, http.MethodPost, "/api/v1/ratings/read", "reader", "", "", ratingRead(old)), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
		fresh := search(saved.query).Items[0]
		if ratingItemAdmission(fresh).Target != ratingItemAdmission(old).Target || fresh.Ratings.Receipt == old.Ratings.Receipt {
			t.Fatal("proof renewal changed the target or retained expired proof")
		}
		requireStatus(t, requestJSON(t, base, http.MethodPost, "/api/v1/ratings/read", "reader", "", "", ratingRead(fresh)), http.StatusOK)
	}
	clk.Advance(10 * time.Minute)
	requireControlProblem(t, requestJSON(t, base, http.MethodPost, "/api/v1/catalog/search", "reader", "", "", laterQuery), http.StatusBadRequest, catalog.CodeInvalidArgument)
	search(query) // A deliberate first-page restart still succeeds.
}
