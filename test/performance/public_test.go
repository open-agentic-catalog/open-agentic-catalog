package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func publicFixture(t *testing.T) ([]byte, *publicProfile) {
	t.Helper()
	entries := make([]map[string]string, 1, 1001)
	entries[0] = map[string]string{"identifier": "registry", "type": "application/ai-registry+json"}
	for i := range 1000 {
		entries = append(entries, map[string]string{"identifier": fmt.Sprint(i), "type": "application/mcp+json"})
	}
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	return body, &publicProfile{Entries: 1001, Bytes: len(body), ETag: `"retained"`, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body))}
}

func TestPublicDeliveryProfileAndPopulationStaySeparate(t *testing.T) {
	body, public := publicFixture(t)
	p := profile{Port: 8080, Scenario: "public", Public: public, Seconds: 1, Workers: 2, MaxRequests: 1000}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	if count, err := validateScenario(body, p); err != nil || count != 1001 {
		t.Fatalf("valid public population: %d %v", count, err)
	}
	if _, err := validateScenario(body, profile{Scenario: "public"}); err == nil {
		t.Fatal("ordinary four-entry contract widened")
	}
	for _, change := range []func(*profile){
		func(p *profile) { p.Scenario = "local" },
		func(p *profile) { p.Public.Entries = 4 },
		func(p *profile) { p.Public.Bytes = 0 },
		func(p *profile) { p.Public.Bytes = 16<<20 + 1 },
		func(p *profile) { p.Public.ETag = "\"bad\nheader\"" },
		func(p *profile) { p.Public.ETag = `W/"weak"` },
		func(p *profile) { p.Public.Digest = "sha256:" + strings.Repeat("z", 64) },
	} {
		changed, selected := p, *public
		changed.Public = &selected
		change(&changed)
		if changed.validate() == nil {
			t.Fatal("invalid public profile admitted")
		}
	}
}

func TestPublicConditionalRequiresExactStatusETagAndEmptyBody(t *testing.T) {
	_, selected := publicFixture(t)
	selected.Conditional = true
	p := profile{Port: 8080, Scenario: "public", Public: selected}
	for _, tc := range []struct {
		status int
		etag   string
		body   string
		valid  bool
	}{{304, selected.ETag, "", true}, {200, selected.ETag, "", false}, {304, `"other"`, "", false}, {304, selected.ETag, "unexpected", false}, {401, "", "{}", false}} {
		client := &http.Client{Transport: performanceRoundTrip(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.Host != "127.0.0.1:8080" || r.Header.Get("If-None-Match") != selected.ETag || r.Header.Get("Authorization") != "" {
				t.Fatal("unexpected conditional request")
			}
			return &http.Response{StatusCode: tc.status, Header: http.Header{"Etag": {tc.etag}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})}
		if body, err := workloadRequest(t.Context(), client, p); (err == nil) != tc.valid || tc.valid && len(body) != 0 {
			t.Fatalf("status=%d etag=%q body=%q: %v", tc.status, tc.etag, tc.body, err)
		}
	}
}
