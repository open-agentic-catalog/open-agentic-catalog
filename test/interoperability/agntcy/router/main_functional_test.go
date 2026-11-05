//go:build functional

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTLSGatewayKeepsNativeResponse verifies the actual two-hop HTTP/TLS exchange.
func TestTLSGatewayKeepsNativeResponse(t *testing.T) {
	for _, tc := range []struct {
		method, path, request, response string
		status                          int
	}{
		{http.MethodGet, "/v1/agents?pageSize=1", "", "{\"agents\":[]}\n", http.StatusOK},
		{http.MethodPost, "/v1/explore", "{}", "{\"native\":\"unsupported explore\"}\n", http.StatusNotFound},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RequestURI() != tc.path || r.Method != tc.method {
					t.Error(r.Method, r.URL)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != tc.request {
					t.Errorf("upstream request = %q, %v", body, err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", "native")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer upstream.Close()
			selected, err := handler(upstream.URL, false, upstream.Client())
			if err != nil {
				t.Fatal(err)
			}
			gateway := httptest.NewTLSServer(selected)
			defer gateway.Close()
			request, err := http.NewRequestWithContext(t.Context(), tc.method, gateway.URL+tc.path, strings.NewReader(tc.request))
			if err != nil {
				t.Fatal(err)
			}
			response, err := gateway.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != tc.status || string(body) != tc.response || response.Header.Get("ETag") != "native" || response.Header.Get("Content-Type") != "application/json" {
				t.Fatal(response, err)
			}
		})
	}
}
