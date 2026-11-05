//go:build minikube && functional

// Functional tests retain real local HTTP and TLS exchanges.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMonitoringHTTPBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantError bool
	}{
		{"success", 200, `{"ok":true}`, false},
		{"private failure", 500, "sensitive-response", true},
		{"redirect", 302, "", true},
		{"invalid JSON", 200, "not-json", true},
		{"oversize", 200, strings.Repeat("x", (1<<20)+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "fixture" || password != "test-only-secret" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("monitoring request lost authentication or content type")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			h := &harness{ctx: context.Background()}
			var result map[string]any
			err := h.monitoringJSON(monitoringClient{client: server.Client(), base: server.URL, user: "fixture", password: "test-only-secret"}, http.MethodPost, "/test", map[string]bool{"test": true}, &result)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "sensitive-response") || strings.Contains(err.Error(), "test-only-secret")) {
				t.Fatal("private monitoring response leaked")
			}
		})
	}
}

func TestMonitoringQueryRequiresFinitePositiveEvidence(t *testing.T) {
	for _, value := range []string{"42", "0", "-1", "NaN", "+Inf", "invalid"} {
		t.Run(value, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("time") != "123" || r.URL.Query().Get("query") != "metric{role=\"api\"}" {
					t.Error("query identity or historical timestamp changed")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{map[string]any{"value": []any{123, value}}}}})
			}))
			defer server.Close()
			h := &harness{ctx: context.Background()}
			client := monitoringClient{client: server.Client(), base: server.URL}
			if err := h.monitoringQuery(client, `metric{role="api"}`, "123", 1); (err == nil) != (value == "42") {
				t.Fatalf("observation %s: %v", value, err)
			}
			if err := h.monitoringQuery(client, `metric{role="api"}`, "123", 2); err == nil {
				t.Fatal("wrong historical sample count accepted")
			}
		})
	}
}

func TestMonitoringMissingSamplesAreNotZeroObservations(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		absent     bool
	}{
		{"no samples", `{"status":"success","data":{"result":[]}}`, true},
		{"zero observation", `{"status":"success","data":{"result":[{"value":[123,"0"]}]}}`, false},
		{"query failure", `{"status":"error","data":{"result":[]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			h := &harness{ctx: context.Background()}
			client := monitoringClient{client: server.Client(), base: server.URL}
			if err := h.monitoringQuery(client, "heap", "", 0); (err == nil) != tc.absent {
				t.Fatalf("missing sample check: %v", err)
			}
			if err := h.monitoringQuery(client, "heap", "", 1); err == nil {
				t.Fatal("missing or zero samples establish positive collection")
			}
		})
	}
}
