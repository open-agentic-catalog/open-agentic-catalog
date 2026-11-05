package main

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The peer has no execution routes. Static answers include all supported kinds
// and an unavailable second source for independent partial-result controls.
func matrixHandler(archives http.Handler, outageFile string) http.Handler {
	fallback := stdioHandler(archives)
	var mu sync.Mutex
	counts := map[string]int{"live": 0, "partial": 0, "unexpected": 0, "credentials": 0}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/matrix-observations" && r.URL.Path != "/live/search" && r.URL.Path != "/partial/search" {
			fallback.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			counts["credentials"]++
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/matrix-observations" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(counts)
			return
		}
		if r.Method == http.MethodGet && r.URL.RawQuery == "" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method != http.MethodPost || r.URL.RawQuery != "" {
			counts["unexpected"]++
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var query struct {
			PageSize  int    `json:"pageSize"`
			PageToken string `json:"pageToken"`
			Query     struct {
				Text   string `json:"text"`
				Filter struct {
					Types     []string `json:"type"`
					Publisher string   `json:"publisher"`
					Tags      []string `json:"tags"`
					License   string   `json:"metadata.license"`
				} `json:"filter"`
			} `json:"query"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&query) != nil {
			counts["unexpected"]++
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/partial/search" {
			counts["partial"]++
			if _, err := os.Stat(outageFile); err != nil {
				_, _ = w.Write([]byte(`{"results":[]}`))
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"Owned unavailable control"}}`))
			return
		}
		counts["live"]++
		rows := []any{}
		matches := !strings.Contains(query.Query.Text, "no-match-control") &&
			(query.Query.Filter.Publisher == "" || query.Query.Filter.Publisher == "catalog.example.com") &&
			(query.Query.Filter.License == "" || query.Query.Filter.License == "Apache-2.0")
		for _, tag := range query.Query.Filter.Tags {
			matches = matches && tag == "matrix-live"
		}
		if matches {
			for i, media := range []string{"application/a2a-agent-card+json", "application/mcp-server-card+json", `text/markdown; profile="urn:air:agent-skills"`, "application/agent-plugins+zip"} {
				if len(query.Query.Filter.Types) > 0 && !slices.Contains(query.Query.Filter.Types, media) {
					continue
				}
				kind := []string{"agent", "mcp", "skill", "plugin"}[i]
				rows = append(rows, map[string]any{"identifier": "urn:air:catalog.example.com:matrix:" + kind, "displayName": "Owned live " + kind, "type": media, "url": stdioOrigin + "/never-invoke", "description": "Inert live metadata", "tags": []string{"matrix-live"}, "metadata": map[string]any{"license": "Apache-2.0"}, "http://purl.org/dc/terms/subject": []string{"development"}, "score": 90 - i, "source": "https://" + r.Host + "/live/search"})
			}
		}
		offset := 0
		if query.PageToken != "" {
			var err error
			offset, err = strconv.Atoi(query.PageToken)
			if err != nil || offset < 0 || offset > len(rows) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		end := len(rows)
		if query.PageSize > 0 && query.PageSize < end-offset {
			end = offset + query.PageSize
		}
		result := map[string]any{"results": rows[offset:end]}
		if end < len(rows) {
			result["pageToken"] = strconv.Itoa(end)
		}
		_ = json.NewEncoder(w).Encode(result)
	})
}
