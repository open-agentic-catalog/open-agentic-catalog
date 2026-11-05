//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPerformanceScaleRejectsInvalidSourceIDsBeforeMeasurement(t *testing.T) {
	const first = "00000000-0000-7000-8000-000000000010"
	const second = "00000000-0000-7000-8000-000000000011"
	for _, tc := range []struct {
		name    string
		sources []string
		valid   bool
	}{
		{"distinct", []string{first, second}, true},
		{"duplicate", []string{first, first}, false},
		{"empty second", []string{first, ""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := catalog.CatalogSearchResponse{
				DurableTotal: 1, Generation: "current", Profile: catalog.CoreSearchProfile, Tier: "core",
				Items: []catalog.CatalogSearchResult{{
					ID: "indexed-result", Identifier: "urn:air:scale.example:scale:indexed-000000",
					DisplayName: "Scale Indexed 000000", Kind: catalog.KindAgent, Type: catalog.SearchResultIndexed,
					Indexed: &catalog.IndexedSearchEvidence{SourceCount: 2, SourceIDs: tc.sources},
				}},
			}
			body, err := json.Marshal(page)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var query catalog.SearchQuery
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/catalog/search" || r.Header.Get("Authorization") != "Bearer local-reader" || json.NewDecoder(r.Body).Decode(&query) != nil || query.Text != page.Items[0].Identifier {
					t.Error("unexpected scale request")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer server.Close()
			output, err := run(t.Context(), profile{Port: serverPort(t, server), Scenario: "scale", Scale: &scaleProfile{Items: 1000, Case: "exact-indexed", Publisher: first}, Seconds: 1, Workers: 1, MaxRequests: 1000})
			if tc.valid {
				if err != nil || output.Verdict != "passed" || output.BaselineResults != 1 || output.Measured.Count == 0 || output.Measured.Failed != 0 || !output.Measured.Complete {
					t.Fatalf("valid control failed: %+v, %v", output, err)
				}
			} else if err == nil || calls.Load() != 1 || output.BaselineResults != 0 || output.Warmup.Count != 0 || output.Measured.Count != 0 || output.Verdict != "incomplete" {
				t.Fatalf("invalid baseline reached measurement: calls=%d, %+v, %v", calls.Load(), output, err)
			}
		})
	}
}

func TestPerformancePublicReadIsAnonymous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/.well-known/ard.json" || r.Header.Get("Authorization") != "" {
			t.Error("public read gained authentication")
		}
		w.Header().Set("Content-Type", "application/ard+json")
		_, _ = w.Write([]byte(`{"entries":[{},{},{}]}`))
	}))
	defer server.Close()
	client := newClient()
	defer client.CloseIdleConnections()
	if _, err := workloadRequest(t.Context(), client, profile{Port: serverPort(t, server), Scenario: "public"}); err != nil {
		t.Fatal(err)
	}
}
