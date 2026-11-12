package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestControlProxyPreservesNativeWire binds observed facts to unchanged HTTP bytes.
func TestControlProxyPreservesNativeWire(t *testing.T) {
	f := newControlProxy()
	defer f.transport.CloseIdleConnections()
	input := `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{}}}`
	output := ` {"jsonrpc":"2.0","id":7,"result":{"protocolVersion":"2025-11-25"}} `
	body := &trackedBody{Reader: strings.NewReader(output)}
	f.proxy.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || string(raw) != input || r.URL.String() != "http://127.0.0.1:8080/mcp" || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Fatal("transparent request changed")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body, Request: r}, nil
	})
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://127.0.0.1:9555/mcp", strings.NewReader(input))
	r.Header.Set("Authorization", "Bearer synthetic")
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != output || !body.closed {
		t.Fatal("transparent result or response closure changed")
	}
	if f.traffic.Sampling || f.traffic.Methods["initialize"] != 1 || f.traffic.Negotiated[0] != "2025-11-25" || f.traffic.Offered[0] != "2026-07-28" || f.traffic.Statuses["200"] != 1 {
		t.Fatal("native negotiation observations changed")
	}
}

// TestControlProxyRefusesResourceTraffic verifies the guard before transport.
func TestControlProxyRefusesResourceTraffic(t *testing.T) {
	f := newControlProxy()
	defer f.transport.CloseIdleConnections()
	f.proxy.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("resource destination reached the API")
		return nil, errors.New("unexpected transport")
	})
	for _, target := range []string{"/never-invoke", "/mcp?private=value", "/api/v1/resources"} {
		w := httptest.NewRecorder()
		f.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))
		if w.Code != http.StatusForbidden {
			t.Fatal("resource destination accepted")
		}
	}
	w := httptest.NewRecorder()
	f.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", maxBody+1))))
	if w.Code != http.StatusRequestEntityTooLarge || f.traffic.Unexpected != 3 {
		t.Fatal("body or route bounds changed")
	}
}

// TestControlObservationsBoundUntrustedLabels avoids retaining private payload text.
func TestControlObservationsBoundUntrustedLabels(t *testing.T) {
	f := newControlProxy()
	defer f.transport.CloseIdleConnections()
	f.proxy.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})
	for range 40 {
		raw := `{"method":"initialize","params":{"protocolVersion":"PRIVATE_SENTINEL","capabilities":{"sampling":{}}}}`
		f.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(raw)))
	}
	f.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"PRIVATE_SENTINEL"}}`)))
	w := httptest.NewRecorder()
	f.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/adk-observations", nil))
	var observed controlTraffic
	if err := json.Unmarshal(w.Body.Bytes(), &observed); err != nil {
		t.Fatal(err)
	}
	if !observed.Sampling || len(observed.Offered) != 32 || observed.Offered[0] != "other" || observed.Tools["other"] != 1 || observed.Statuses["401"] != 41 || strings.Contains(w.Body.String(), "PRIVATE_SENTINEL") {
		t.Fatal("unbounded or private observation")
	}
}

// TestControlProxyReportsTransportAndInitializationFailures closes rejected bodies.
func TestControlProxyReportsTransportAndInitializationFailures(t *testing.T) {
	f := newControlProxy()
	defer f.transport.CloseIdleConnections()
	f.proxy.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("PRIVATE_SENTINEL") })
	w := httptest.NewRecorder()
	f.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(`{}`)))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "PRIVATE_SENTINEL") {
		t.Fatal("private transport failure escaped")
	}
	body := &trackedBody{Reader: strings.NewReader(strings.Repeat("x", maxBody+1))}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil).WithContext(context.WithValue(t.Context(), controlInitialization{}, true))
	if f.observeNegotiation(&http.Response{StatusCode: http.StatusOK, Request: r, Body: body}) == nil || !body.closed {
		t.Fatal("initialization byte bound or closure changed")
	}
}
