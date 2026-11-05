package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchRecorderPreservesBytesAndBoundsEvidence(t *testing.T) {
	input := []byte(`{"query":{"text":"order"},"pageToken":"native-token"}`)
	wire := []byte(`{"results":[],"staleSince":"2026-01-01T00:00:00Z"}`)
	forwarded := 0
	h := recordSearch(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(raw, input) {
			t.Fatal("observer changed request bytes")
		}
		forwarded++
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(wire)
	}))
	for range 513 {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/search", bytes.NewReader(input))
		r.Header.Set("Authorization", "Bearer private")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusAccepted || !bytes.Equal(w.Body.Bytes(), wire) {
			t.Fatal("observer changed response")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_qualification/requests", nil))
	var evidence struct {
		Requests []struct {
			Body          json.RawMessage `json:"body"`
			Authorization bool            `json:"authorizationPresent"`
		} `json:"requests"`
		Overflow bool `json:"overflow"`
	}
	if json.Unmarshal(w.Body.Bytes(), &evidence) != nil || !evidence.Overflow || len(evidence.Requests) != 512 || forwarded != 513 || !evidence.Requests[0].Authorization || !bytes.Equal(evidence.Requests[0].Body, input) || bytes.Contains(w.Body.Bytes(), []byte("private")) {
		t.Fatal("evidence bound, metadata or credential protection failed")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/search", bytes.NewReader(bytes.Repeat([]byte("x"), requestLimit+1))))
	if w.Code != http.StatusRequestEntityTooLarge || forwarded != 513 {
		t.Fatal("oversized request forwarded")
	}
}

func TestEvidenceObserverCountsRejectedPathsWithoutChangingResponses(t *testing.T) {
	h := recordSearch(http.NotFoundHandler())
	for _, path := range []string{"/healthz", "/.well-known/ai-catalog.json", "/checkpoint", "/v1/log/checkpoint/history", "/v1/agents/00000000-0000-7000-8000-000000000001/receipt", "/v1/agents/00000000-0000-7000-8000-000000000001/status-token"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatal("observer changed route admission")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_qualification/requests", nil))
	var report struct {
		Count    int  `json:"evidenceRequests"`
		Overflow bool `json:"overflow"`
	}
	if json.Unmarshal(w.Body.Bytes(), &report) != nil || report.Count != 4 || report.Overflow {
		t.Fatal(w.Body.String())
	}
}
