//go:build functional

package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCatalogBridgePreservesDenialAndRefusesRedirect(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := make(chan struct{}, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- struct{}{}
				if r.URL.Path != "/api/v1/catalog/resources/fixture" || r.Header.Get("Authorization") != "Bearer fixture-reader" {
					t.Error("bridge changed path or authority")
				}
				w.Header().Set("Location", "/must-not-follow")
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"code":"FIXTURE_DENIAL"}`))
			}))
			defer server.Close()
			client := catalogClient()
			defer client.CloseIdleConnections()
			transport, ok := client.Transport.(*http.Transport)
			if !ok || transport.Proxy != nil || client.Timeout <= 0 {
				t.Fatal("fixture lost proxy isolation or deadline")
			}
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "127.0.0.1:8080" {
					t.Error("fixture attempted another origin")
				}
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			response, err := catalogExchange(t.Context(), client, catalogRequest{Method: "GET", Path: "/api/v1/catalog/resources/fixture", Token: "fixture-reader"})
			if err != nil || response.Status != status || string(response.Body) != `{"code":"FIXTURE_DENIAL"}` || len(requests) != 1 {
				t.Fatalf("unexpected response or followed redirect: %#v, %v, requests=%d", response, err, len(requests))
			}
		})
	}
}
