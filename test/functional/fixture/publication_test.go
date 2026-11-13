package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicationExactBytesAndTransportSelection(t *testing.T) {
	raw := []byte("{\n  \"specVersion\": \"1.0\", \"entries\": []\n}\n")
	handler := publicationHandler(raw)
	for path, media := range map[string]string{"/ai-catalog.json": "application/ai-catalog+json", "/generic/ai-catalog.json": "application/json"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Body.String() != string(raw) || response.Header().Get("Content-Type") != media || response.Header().Get("ETag") == "" {
			t.Fatalf("publication bytes or transport changed: %s", path)
		}
	}
}

func TestPublicationOutageRecoveryAndClosedControls(t *testing.T) {
	handler := publicationHandler([]byte(`{"entries":[]}`))
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body)))
		return r
	}
	for _, body := range []string{`{}`, `{"unavailable":null}`, `{"unavailable":true,"extra":1}`, `{"unavailable":true} {}`} {
		if request(http.MethodPost, "/control", body).Code != http.StatusBadRequest {
			t.Fatalf("invalid control accepted: %s", body)
		}
	}
	if request(http.MethodPost, "/control", `{"unavailable":true}`).Code != http.StatusOK || request(http.MethodGet, "/ai-catalog.json", "").Code != http.StatusServiceUnavailable {
		t.Fatal("publication outage was not observable")
	}
	if request(http.MethodGet, "/healthz", "").Code != http.StatusOK {
		t.Fatal("outage changed fixture health")
	}
	request(http.MethodPost, "/control", `{"unavailable":false}`)
	if got := request(http.MethodGet, "/ai-catalog.json", ""); got.Code != http.StatusOK || got.Body.String() != `{"entries":[]}` {
		t.Fatal("recovery altered the publication")
	}
	request(http.MethodGet, "/runtime", "")
	request(http.MethodGet, "/.well-known/ard.json", "")
	request(http.MethodGet, "/.well-known/ai-catalog.json", "")
	request(http.MethodGet, "/ai-catalog.json?follow=true", "")
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ai-catalog.json", nil)
	r.Header.Set("Authorization", "synthetic")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	var state struct {
		Requests             int            `json:"requests"`
		UnexpectedRequests   int            `json:"unexpectedRequests"`
		CredentialedRequests int            `json:"credentialedRequests"`
		DiscoveryProbes      int            `json:"discoveryProbes"`
		Statuses             map[string]int `json:"statuses"`
	}
	if err := json.Unmarshal(request(http.MethodGet, "/state", "").Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Requests != 3 || state.UnexpectedRequests != 2 || state.CredentialedRequests != 1 || state.DiscoveryProbes != 2 || state.Statuses["503"] != 1 || state.Statuses["200"] != 2 {
		t.Fatalf("incomplete traffic observations: %+v", state)
	}
}

func TestPublicationCountsCredentialsOnEveryRoute(t *testing.T) {
	handler := publicationHandler([]byte(`{"entries":[]}`))
	state := func() int {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/state", nil))
		var value struct {
			CredentialedRequests int `json:"credentialedRequests"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value.CredentialedRequests
	}
	if state() != 0 {
		t.Fatal("anonymous request counted as credentialed")
	}
	paths := []string{"/ai-catalog.json", "/generic/ai-catalog.json", "/.well-known/ard.json", "/.well-known/ai-catalog.json", "/unexpected", "/healthz", "/state", "/control"}
	for _, path := range paths {
		for _, header := range []string{"Authorization", "Cookie"} {
			method, body := http.MethodGet, ""
			if path == "/control" {
				method, body = http.MethodPost, `{"unavailable":false}`
			}
			request := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
			request.Header.Set(header, "synthetic-credential")
			handler.ServeHTTP(httptest.NewRecorder(), request)
		}
	}
	if got, want := state(), 2*len(paths); got != want {
		t.Fatalf("credentialed requests = %d, want %d across all routes", got, want)
	}
}
