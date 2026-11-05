//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPerformanceSearchRequestShapes(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"", "local", "indexed", "mixed"} {
		t.Run(scenario, func(t *testing.T) {
			query := `skill "quoted" \ path`
			want := map[string]any{"limit": float64(20)}
			if scenario == "local" {
				want["visibility"] = "public"
			} else if scenario == "indexed" {
				want["text"] = query
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/catalog/search" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer local-reader" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("unexpected Search request envelope")
				}
				var body map[string]any
				decoder := json.NewDecoder(r.Body)
				if err := decoder.Decode(&body); err != nil || !reflect.DeepEqual(body, want) || decoder.Decode(new(any)) != io.EOF {
					t.Error("request did not preserve the exact selected body")
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			client := newClient()
			defer client.CloseIdleConnections()
			p := profile{Port: serverPort(t, server), Scenario: scenario}
			if scenario == "indexed" {
				p.Query = query
			}
			if _, err := workloadRequest(t.Context(), client, p); err != nil {
				t.Fatal(err)
			}
		})
	}
}
