package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

func TestMatrixPeerPreservesValidARDFieldsAndControlledFailures(t *testing.T) {
	outage := filepath.Join(t.TempDir(), "outage")
	h := matrixHandler(http.NotFoundHandler(), outage)
	request := func(method, path, body, token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), method, "https://172.31.0.10:9443"+path, strings.NewReader(body))
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("status: %d, want %d", w.Code, status)
		}
		return w
	}
	request("GET", "/live/search", "", "", 405)
	w := request("POST", "/live/search", `{"query":{"text":"Owned"},"pageSize":50,"federation":"none"}`, "", 200)
	var result struct {
		Results []json.RawMessage `json:"results"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Results) != 4 {
		t.Fatal("four-kind fixture")
	}
	seen := map[string]bool{}
	for _, raw := range result.Results {
		if err := standards.ValidateARDSearchEntry(raw); err != nil {
			t.Fatalf("invalid fixture entry: %v", err)
		}
		var entry struct {
			Type   string `json:"type"`
			Score  int    `json:"score"`
			Source string `json:"source"`
		}
		if json.Unmarshal(raw, &entry) != nil || entry.Score < 0 || entry.Score > 100 || entry.Source != "https://172.31.0.10:9443/live/search" {
			t.Fatal("fixture attribution")
		}
		seen[entry.Type] = true
	}
	if len(seen) != 4 {
		t.Fatal("fixture collapsed kinds")
	}
	w = request("POST", "/live/search", `{"query":{"text":"no-match-control"}}`, "", 200)
	if w.Body.String() != "{\"results\":[]}\n" {
		t.Fatal("empty control")
	}
	request("POST", "/live/search", `{"query":"invalid-shape"}`, "", 400)
	request("POST", "/live/search", `{}`, "Bearer forbidden", 403)
	request("POST", "/partial/search", `{}`, "", 200)
	if err := os.WriteFile(outage, nil, 0600); err != nil {
		t.Fatal(err)
	}
	request("POST", "/partial/search", `{}`, "", 503)
	request("GET", "/never-invoke", "", "", 403)
	w = request("GET", "/matrix-observations", "", "", 200)
	var counts map[string]int
	if json.Unmarshal(w.Body.Bytes(), &counts) != nil || counts["live"] != 2 || counts["partial"] != 2 || counts["unexpected"] != 1 || counts["credentials"] != 1 {
		t.Fatal("unreliable counters", counts)
	}
}

func TestMatrixPeerHonorsExactMediaTypesAndPageSize(t *testing.T) {
	h := matrixHandler(http.NotFoundHandler(), filepath.Join(t.TempDir(), "outage"))
	call := func(body string) (int, []struct {
		Type string `json:"type"`
	}, string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "POST", "https://172.31.0.10:9443/live/search", strings.NewReader(body)))
		var page struct {
			Results []struct {
				Type string `json:"type"`
			} `json:"results"`
			Next string `json:"pageToken"`
		}
		if w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatal("invalid peer page")
		}
		return w.Code, page.Results, page.Next
	}
	for _, media := range []string{`text/markdown; profile="urn:air:agent-skills"`, "application/agent-plugins+zip"} {
		body, err := json.Marshal(map[string]any{"query": map[string]any{"text": "Owned", "filter": map[string]any{"type": []string{media}}}})
		if err != nil {
			t.Fatal(err)
		}
		status, rows, next := call(string(body))
		if status != 200 || len(rows) != 1 || rows[0].Type != media || next != "" {
			t.Fatal("exact type filter", status, rows, next)
		}
	}
	status, rows, next := call(`{"query":{"text":"Owned"},"pageSize":2}`)
	if status != 200 || len(rows) != 2 || next != "2" {
		t.Fatal("first page", status, rows, next)
	}
	status, rows, next = call(`{"query":{"text":"Owned"},"pageSize":2,"pageToken":"2"}`)
	if status != 200 || len(rows) != 2 || next != "" || rows[0].Type != `text/markdown; profile="urn:air:agent-skills"` {
		t.Fatal("second page", status, rows, next)
	}
	status, _, _ = call(`{"query":{"text":"Owned"},"pageToken":"invalid"}`)
	if status != 400 {
		t.Fatal("invalid token accepted", status)
	}
}
