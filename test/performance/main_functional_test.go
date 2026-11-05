//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serverPort(t *testing.T, server *httptest.Server) int {
	t.Helper()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPerformanceClientReusesBoundedConnections(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/catalog/search" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer local-reader" {
			t.Error("unexpected request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	client := newClient()
	defer client.CloseIdleConnections()
	for range 3 {
		if _, err := search(t.Context(), client, serverPort(t, server)); err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 1 {
		t.Fatalf("connections=%d; keep-alive not preserved", connections.Load())
	}
}

func TestPerformanceClientRejectsUnsafeResponses(t *testing.T) {
	for _, mode := range []string{"redirect", "overflow", "status", "media", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch mode {
				case "redirect":
					w.Header().Set("Location", "http://unowned.invalid/")
					w.WriteHeader(http.StatusFound)
				case "overflow":
					_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
				case "status":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "media":
					w.Header().Set("Content-Type", "text/html")
				case "cancel":
					select {
					case <-r.Context().Done():
					case <-time.After(250 * time.Millisecond):
					}
				}
			}))
			defer server.Close()
			client := newClient()
			defer client.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if _, err := search(ctx, client, serverPort(t, server)); err == nil {
				t.Fatal("unsafe response accepted")
			}
		})
	}
}

func TestPerformanceRunReconcilesFiniteWork(t *testing.T) {
	raw := validPage(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	output, err := run(t.Context(), profile{Port: serverPort(t, server), Seconds: 1, Workers: 2, MaxRequests: 1000})
	if err != nil || output.Measured.Count == 0 || output.Measured.Succeeded != output.Measured.Count || strconv.FormatUint(output.Measured.Buckets[11], 10) != strconv.Itoa(output.Measured.Count) {
		t.Fatalf("incomplete report: %+v %v", output, err)
	}
	if _, err := run(t.Context(), profile{Port: serverPort(t, server), Seconds: 1, Workers: 1, MaxRequests: 1}); err == nil {
		t.Fatal("early exhausted request budget passed")
	}
	if _, err := run(t.Context(), profile{}); err == nil {
		t.Fatal("invalid run accepted")
	}
}

// search issues the baseline search against the owned listener used by functional fixtures.
func search(ctx context.Context, client *http.Client, port int) ([]byte, error) {
	return workloadRequest(ctx, client, profile{Port: port})
}
