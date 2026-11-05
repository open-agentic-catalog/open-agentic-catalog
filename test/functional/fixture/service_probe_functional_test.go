//go:build functional

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestServiceProbeTransportHasNoProxyRedirectOrConnectionReuse(t *testing.T) {
	var redirected, connections atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, target.URL, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	// The explicit transport ignores environment proxy settings.
	t.Setenv("HTTP_PROXY", target.URL)
	address := strings.TrimPrefix(server.URL, "http://")
	client, transport := serviceProbeClient(address)
	defer transport.CloseIdleConnections()
	for _, path := range []string{"/", "/", "/redirect"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if path == "/redirect" && response.StatusCode != http.StatusFound {
			t.Fatal("redirect followed")
		}
	}
	if connections.Load() != 3 || redirected.Load() != 0 {
		t.Fatalf("connections=%d unexpected destination calls=%d", connections.Load(), redirected.Load())
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("unexpected destination accepted")
	}
}
