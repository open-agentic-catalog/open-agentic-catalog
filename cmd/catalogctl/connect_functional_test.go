//go:build functional

// Connector checks exercise public discovery alongside the composed remote CLI workflow.
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"
)

// checkConnectorHTTP reuses the pinned connector request without interpreting assistant instructions.
func checkConnectorHTTP(t *testing.T, origin, identifier string, expectedData any) {
	t.Helper()
	expectedJSON, err := json.Marshal(expectedData)
	if err != nil {
		t.Fatal(err)
	}
	var expected any
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../internal/platform/httpserver/testdata/ard-reference-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, fixtureName := range []string{"github-current-nested-query", "hugging-face-nested-search"} {
		t.Run(fixtureName, func(t *testing.T) {
			var body map[string]any
			for _, entry := range corpus.Cases {
				if entry.Name == fixtureName {
					if body != nil {
						t.Fatal("duplicate connector request")
					}
					if err := json.Unmarshal(entry.Value, &body); err != nil {
						t.Fatal(err)
					}
				}
			}
			if body == nil {
				t.Fatal("connector request fixture missing")
			}
			query, ok := body["query"].(map[string]any)
			if !ok {
				t.Fatal("connector fixture requires a nested query object")
			}
			query["text"] = identifier
			client := &http.Client{Timeout: 5 * time.Second}
			for _, mode := range []string{"none", "referrals"} {
				t.Run(mode, func(t *testing.T) {
					body["federation"] = mode
					payload, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin+"/ard/v1/search", bytes.NewReader(payload))
					if err != nil {
						t.Fatal(err)
					}
					request.Header.Set("Content-Type", "application/json")
					response, err := client.Do(request)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = response.Body.Close() }()
					var result struct {
						Results   []map[string]json.RawMessage `json:"results"`
						Referrals []json.RawMessage            `json:"referrals"`
					}
					if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
						t.Fatal(err)
					}
					if response.StatusCode != http.StatusOK || len(result.Results) != 1 || len(result.Referrals) != 0 {
						t.Fatalf("discovery status=%d results=%d referrals=%d", response.StatusCode, len(result.Results), len(result.Referrals))
					}
					entry := result.Results[0]
					if string(entry["type"]) != `"application/a2a-agent-card+json"` || entry["mediaType"] != nil {
						t.Fatalf("noncanonical media type: %s / %s", entry["type"], entry["mediaType"])
					}
					// Check the full-entry subset expected by HF without cloning its decoder.
					for _, field := range []string{"displayName", "source"} {
						var value string
						if err := json.Unmarshal(entry[field], &value); err != nil || value == "" {
							t.Fatalf("missing full-result %s: %s", field, entry[field])
						}
					}
					var score *int
					if err := json.Unmarshal(entry["score"], &score); err != nil || score == nil || *score < 0 || *score > 100 {
						t.Fatalf("invalid score: %s", entry["score"])
					}
					var got string
					if err := json.Unmarshal(entry["identifier"], &got); err != nil || got != identifier {
						t.Fatalf("identifier=%q: %v", got, err)
					}
					var actual any
					if err := json.Unmarshal(entry["data"], &actual); err != nil {
						t.Fatalf("inline representation: %v", err)
					}
					if !reflect.DeepEqual(actual, expected) || entry["url"] != nil {
						t.Fatal("inline metadata changed or was relabeled as a delivery URL")
					}
				})
			}
		})
	}
}
