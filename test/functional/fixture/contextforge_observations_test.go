package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextForgeObservationsPreserveBytesAndExcludePrivateInputs(t *testing.T) {
	directory := contextForgeInputs(t)
	raw, err := os.ReadFile(filepath.Join(directory, "secrets.json")) // #nosec G304 -- test-owned input.
	if err != nil {
		t.Fatal(err)
	}
	var secrets struct {
		Tokens map[string]string `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &secrets); err != nil {
		t.Fatal(err)
	}
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"catalog_control_search","arguments":{"text":"private-query-sentinel"}}}`
	handler, err := contextForgeHandler(filepath.Join(directory, "frontend"), peerRoundTrip(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil || string(body) != input {
			t.Fatal("observation changed request bytes", err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"result":{}}`))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(input))
	r.Header.Set("Authorization", "Bearer "+secrets.Tokens["a-reader"])
	r.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != `{"result":{}}` {
		t.Fatal("observation changed response bytes")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/observations", nil))
	if strings.Contains(w.Body.String(), "private-query-sentinel") || strings.Contains(w.Body.String(), secrets.Tokens["a-reader"]) {
		t.Fatal("observations disclosed private input")
	}
	var observed struct {
		Events []contextForgeEvent `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &observed); err != nil || len(observed.Events) != 1 {
		t.Fatal("missing observation", err)
	}
	event := observed.Events[0]
	if event.Credential != "a-reader" || event.Tool != "catalog_control_search" || event.Protocol != "2026-07-28" || event.Status != http.StatusOK {
		t.Fatal("lost bounded request facts")
	}
}

func TestContextForgeObservationBoundIsExplicit(t *testing.T) {
	observations := &contextForgeObservations{labels: map[string]string{}}
	for range 1025 {
		observations.add(contextForgeEvent{Path: "catalog", Method: "ping", Credential: "none", Status: http.StatusOK})
	}
	w := httptest.NewRecorder()
	observations.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/observations", nil))
	var result struct {
		Events   []contextForgeEvent `json:"events"`
		Overflow int                 `json:"overflow"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Events) != 1024 || result.Overflow != 1 {
		t.Fatal("observation loss was not explicit", err)
	}
}

func TestContextForgeObservationKeepsOnlyKnownProtocolFields(t *testing.T) {
	for _, protocol := range []string{"2025-11-25", "2026-07-28", "private-protocol-sentinel"} {
		t.Run(protocol, func(t *testing.T) {
			observations := &contextForgeObservations{labels: map[string]string{}}
			handler := observations.observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"result":{"protocolVersion":"`+protocol+`"}}`)
			}), "catalog")
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(`{"method":"initialize","params":{"protocolVersion":"`+protocol+`","name":"private-tool-sentinel"}}`))
			request.Header.Set("Authorization", "Bearer private-token-sentinel")
			handler.ServeHTTP(httptest.NewRecorder(), request)
			event := observations.events[0]
			if event.Method != "initialize" || event.Tool != "" || event.Credential != "other" {
				t.Fatal("unexpected diagnostic fields")
			}
			if protocol == "private-protocol-sentinel" {
				if event.Protocol != "" || event.Negotiated != "" {
					t.Fatal("retained unknown protocol text")
				}
			} else if event.Protocol != protocol || event.Negotiated != protocol {
				t.Fatal("lost protocol negotiation")
			}
		})
	}
}

func TestContextForgeObservationRejectsOversizedBodyBeforeForwarding(t *testing.T) {
	directory := contextForgeInputs(t)
	forwarded := false
	handler, err := contextForgeHandler(filepath.Join(directory, "frontend"), peerRoundTrip(func(_ *http.Request) (*http.Response, error) {
		forwarded = true
		return nil, errors.New("unexpected upstream request")
	}))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("a", (1<<20)+1))))
	if w.Code != http.StatusRequestEntityTooLarge || forwarded {
		t.Fatal("oversized body reached the upstream")
	}
}
