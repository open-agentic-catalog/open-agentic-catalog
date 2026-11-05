//go:build functional

package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

// Reuse the authenticated authoring fixture.
func testHTTPJobMetadata(t *testing.T, baseURL, tenantID string, store *jobs.MemoryStore, at time.Time) {
	t.Helper()
	const snapshotID = "00000000-0000-7000-8000-000000000097"
	const sourceID = "00000000-0000-7000-8000-000000000096"
	retained := strings.Repeat("private-retained-document", 100000)
	fixtures := []jobs.Job{
		{ID: "00000000-0000-7000-8000-000000000099", Kind: "publication.generate", Result: json.RawMessage(`{"id":"` + snapshotID + `","host":"catalog.example.com","generation":3,"state":"candidate","ard":"` + retained + `"}`)},
		{ID: "00000000-0000-7000-8000-000000000098", Kind: "source.sync", Result: json.RawMessage(`{"id":"` + snapshotID + `","sourceId":"` + sourceID + `","state":"incomplete","findings":[{"message":"` + retained + `"}]}`)},
		{ID: "00000000-0000-7000-8000-000000000097", Kind: "source.sync", Result: json.RawMessage(`{"id":"` + snapshotID + `","sourceId":"` + sourceID + `","state":"complete","host":{"private":"` + retained + `"}}`)},
		{ID: "00000000-0000-7000-8000-000000000095", Kind: "semantic.reindex", Result: json.RawMessage(`{"generation":"00000000-0000-7000-8000-000000000095","members":0,"private":"` + retained + `"}`)},
		{ID: "00000000-0000-7000-8000-000000000094", Kind: "semantic.reindex", Result: json.RawMessage(`{"generation":"` + snapshotID + `","members":1,"private":"` + retained + `"}`)},
	}
	zero := int64(0)
	expectedResults := []*jobs.ResultSummary{
		{Type: "publication_snapshot", SnapshotID: snapshotID, Host: "catalog.example.com", Generation: 3, State: "candidate"},
		{Type: "source_snapshot", SnapshotID: snapshotID, SourceID: sourceID, State: "incomplete"},
		nil, // A wrongly typed retained field invalidates only the compact summary.
		{Type: "semantic_index", GenerationID: "00000000-0000-7000-8000-000000000095", Members: &zero},
		nil, // A generation from another Job must not be presented as this completion.
	}
	for index := range fixtures {
		job := &fixtures[index]
		job.TenantID, job.State, job.AttemptLimit = tenantID, jobs.StateSucceeded, 5
		job.CreatedAt, job.UpdatedAt, job.NotBefore = at, at, at
		job.DeduplicationKey = job.ID
		job.Payload = json.RawMessage(`{"private":"execution-input"}`)
		if _, existing, err := store.Enqueue(t.Context(), *job); err != nil || existing {
			t.Fatalf("seed retained Job: existing=%v err=%v", existing, err)
		}
	}
	cursor := ""
	for index, job := range fixtures {
		pageResponse := requestJSON(t, baseURL, http.MethodGet, "/api/v1/jobs?limit=1"+cursor, "admin", "", "", nil)
		requireStatus(t, pageResponse, http.StatusOK)
		requireControlSchema(t, "JobPage", pageResponse.body)
		var page struct {
			Items      []jobs.View `json:"items"`
			NextCursor string      `json:"nextCursor"`
		}
		decodeResult(t, pageResponse, &page)
		expected := jobs.View{ID: job.ID, TenantID: tenantID, Kind: job.Kind, State: jobs.StateSucceeded, AttemptLimit: 5, NotBefore: at, CreatedAt: at, UpdatedAt: at, Result: expectedResults[index]}
		if len(page.Items) != 1 || (page.NextCursor != "") != (index < len(fixtures)-1) || !reflect.DeepEqual(page.Items[0], expected) {
			t.Fatalf("Job page lost summary or continuation: %+v", page)
		}
		cursor = "&cursor=" + url.QueryEscape(page.NextCursor)
		detail := requestJSON(t, baseURL, http.MethodGet, "/api/v1/jobs/"+job.ID, "admin", "", "", nil)
		requireStatus(t, detail, http.StatusOK)
		requireControlSchema(t, "Job", detail.body)
		var view jobs.View
		decodeResult(t, detail, &view)
		if !reflect.DeepEqual(view, page.Items[0]) {
			t.Fatal("Job detail differs from collection summary")
		}
		for _, response := range [][]byte{pageResponse.body, detail.body} {
			if len(response) > 2048 || bytes.Contains(response, []byte("private-retained")) || bytes.Contains(response, []byte("execution-input")) {
				t.Fatal("Job metadata exposed private documents or exceeded response bound")
			}
		}
		requireStatus(t, requestJSON(t, baseURL, http.MethodGet, "/api/v1/jobs/"+job.ID, "other", "", "", nil), http.StatusNotFound)
		stored, err := store.Get(t.Context(), tenantID, job.ID)
		if err != nil || !bytes.Equal(stored.Result, job.Result) || !bytes.Equal(stored.Payload, job.Payload) {
			t.Fatal("metadata reads changed retained execution data")
		}
		t.Logf("%s retained=%d bytes, page=%d bytes, detail=%d bytes", job.Kind, len(job.Result), len(pageResponse.body), len(detail.body))
	}
	for _, tc := range []struct {
		query string
		ids   []string
	}{
		{"kind=source.sync&state=succeeded", []string{fixtures[1].ID, fixtures[2].ID}},
		{"kind=semantic.reindex&state=succeeded", []string{fixtures[3].ID, fixtures[4].ID}},
		{"kind=Source.sync", nil},
		{"kind=unrecognized.job", nil},
		{"parentJobId=" + snapshotID, nil},
		{"state=queued", nil},
	} {
		r := requestJSON(t, baseURL, http.MethodGet, "/api/v1/jobs?limit=10&"+tc.query, "admin", "", "", nil)
		requireStatus(t, r, http.StatusOK)
		requireControlSchema(t, "JobPage", r.body)
		var page struct {
			Items []jobs.View `json:"items"`
		}
		decodeResult(t, r, &page)
		if len(page.Items) != len(tc.ids) {
			t.Fatalf("filtered HTTP %s: %+v", tc.query, page.Items)
		}
		for index, id := range tc.ids {
			if page.Items[index].ID != id {
				t.Fatalf("filtered HTTP %s row %d: got %s, want %s", tc.query, index, page.Items[index].ID, id)
			}
		}
	}
	// A matching row must still be found after a filtered continuation. The
	// publication row sorts ahead of both matches in the unfiltered collection.
	filteredCursor := ""
	for index, id := range []string{fixtures[1].ID, fixtures[2].ID} {
		r := requestJSON(t, baseURL, http.MethodGet, "/api/v1/jobs?limit=1&kind=source.sync&state=succeeded"+filteredCursor, "admin", "", "", nil)
		requireStatus(t, r, http.StatusOK)
		requireControlSchema(t, "JobPage", r.body)
		var page struct {
			Items      []jobs.View `json:"items"`
			NextCursor string      `json:"nextCursor"`
		}
		decodeResult(t, r, &page)
		if len(page.Items) != 1 || page.Items[0].ID != id || (page.NextCursor != "") != (index == 0) {
			t.Fatalf("filtered continuation lost, duplicated or reordered a match: %+v", page)
		}
		filteredCursor = "&cursor=" + url.QueryEscape(page.NextCursor)
	}
	for _, query := range []string{"Kind=source.sync", "parentjobid=" + snapshotID, "state=unknown", "kind=", "state=queued&state=failed", "unknown=value"} {
		requireStatus(t, requestJSON(t, baseURL, http.MethodGet, "/api/v1/jobs?"+query, "admin", "", "", nil), http.StatusBadRequest)
	}
}
