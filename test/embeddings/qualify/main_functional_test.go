//go:build functional

// Qualifier controls use real loopback HTTP to distinguish local admission
// from a provider response. They do not start a model or application process.
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// TestRunProviderRejectionQualification requires observed successful controls
// and a dispatched long query; a local refusal cannot qualify the provider.
func TestRunProviderRejectionQualification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		maxBytes int
		reject   bool
		wantErr  string
		calls    int
	}{
		{"provider rejects", 8192, true, "", 7},
		{"provider accepts", 8192, false, "embedding provider did not reject overlength input", 7},
		{"local rejection", 128, true, "overlength probe exceeds configured input byte limit", 0},
		{"exact prefixed bytes", 3002, true, "", 7},
		{"prefix overflows by one", 3001, true, "overlength probe exceeds configured input byte limit", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			texts := []string{"Search public documentation for a software library.", "Translate a short sentence into French.", "Inspect a synthetic weather forecast."}
			longQuery := "é" + strings.Repeat("word ", 600)
			var mu sync.Mutex
			var requests [][]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model    string   `json:"model"`
					Input    []string `json:"input"`
					Encoding string   `json:"encoding_format"`
				}
				decoder := json.NewDecoder(r.Body)
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&request); err != nil || r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || request.Model != "fixture" || request.Encoding != "float" || r.Header.Get("Authorization") != "" {
					t.Error("invalid fixture request")
					http.Error(w, "invalid fixture request", http.StatusBadRequest)
					return
				}
				mu.Lock()
				requests = append(requests, request.Input)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if tc.reject && len(request.Input) == 1 && request.Input[0] == longQuery {
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = w.Write([]byte(`{"error":"synthetic model context limit"}`))
					return
				}
				var vectors []map[string]any
				for i, input := range request.Input {
					vector := []int{1, 0, 0}
					for position, text := range texts {
						if input == "doc:"+text || input == "é"+text {
							vector = make([]int, 3)
							vector[position] = 1
						}
					}
					vectors = append(vectors, map[string]any{"index": i, "embedding": vector})
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "data": vectors}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			settings := config.SemanticDefaults()
			settings.Enabled, settings.AllowHTTP = true, true
			settings.Endpoint = server.URL + "/v1/embeddings"
			settings.AllowedAddressPrefixes = []string{"127.0.0.1/32"}
			settings.Model, settings.ProfileRevision, settings.Dimensions = "fixture", "one", 3
			settings.QueryPrefix, settings.DocumentPrefix, settings.MaxInputBytes = "é", "doc:", tc.maxBytes
			if err := settings.Validate(); err != nil {
				t.Fatalf("control configuration must be valid: %v", err)
			}
			raw, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = run(bytes.NewReader(raw), &output)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr || output.Len() != 0 {
					t.Fatalf("unqualified provider: error=%v output=%s", err, &output)
				}
			} else {
				var report map[string]any
				if err != nil || json.Unmarshal(output.Bytes(), &report) != nil || len(report) != 5 || report["dimensions"] != float64(3) || report["batchOrdering"] != true || report["repeatStable"] != true || report["overlengthRejected"] != true {
					t.Fatalf("qualified provider: error=%v output=%s", err, &output)
				}
				if elapsed, ok := report["elapsedMillis"].(float64); !ok || elapsed < 0 {
					t.Fatal("missing or invalid elapsed time")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != tc.calls {
				t.Fatalf("provider calls=%d, want %d", len(requests), tc.calls)
			}
			if tc.calls != 0 {
				want := [][]string{{"doc:" + texts[0], "doc:" + texts[1], "doc:" + texts[2]}, {"doc:" + texts[0]}, {"doc:" + texts[1]}, {"doc:" + texts[2]}, {"é" + texts[0]}, {"é" + texts[0]}, {longQuery}}
				if !reflect.DeepEqual(requests, want) {
					t.Fatal("qualification did not dispatch the exact short controls and long query")
				}
			}
		})
	}
}
