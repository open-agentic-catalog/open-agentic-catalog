//go:build functional

package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUIConcurrentAdmissionDisconnectAndRecovery(t *testing.T) {
	entered, cancelled := make(chan struct{}, 2), make(chan struct{}, 2)
	var blocked atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" {
			_, _ = io.WriteString(w, testAccess)
			return
		}
		if blocked.Load() {
			entered <- struct{}{}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, strings.Repeat(" ", 4095))
			_ = http.NewResponseController(w).Flush()
			<-r.Context().Done()
			cancelled <- struct{}{}
			return
		}
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	defer api.Close()
	configuration := fixtureConfig(t, api.URL)
	configuration.ResponseBytes = 4096
	s := newFixture(t, configuration)
	// Exercise the existing admission implementation with a small deterministic capacity.
	s.admission = make(chan struct{}, 2)
	cookie := loginFixture(t, s)
	finished := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.ServeHTTP(w, r)
		if r.Context().Err() != nil {
			finished <- struct{}{}
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	invoke := func(ctx context.Context, path string) (int, string, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		if err != nil {
			return 0, "", err
		}
		r.Host = s.origin.Host
		r.Header.Set("X-Catalog-Request", "1")
		r.AddCookie(cookie)
		response, err := client.Do(r)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = response.Body.Close() }()
		raw, err := io.ReadAll(response.Body)
		return response.StatusCode, string(raw), err
	}
	path := "/_catalog/api/v1/resources"
	if status, _, err := invoke(t.Context(), path); err != nil || status != 200 {
		t.Fatalf("control: %d %v", status, err)
	}
	blocked.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	results := make(chan error, 2)
	for range 2 {
		go func() { _, _, err := invoke(ctx, path); results <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("upstream not reached")
		}
	}
	if status, body, err := invoke(t.Context(), "/auth/session"); err != nil || status != 503 || !strings.Contains(body, "ui_busy") {
		t.Fatalf("admission: %d %s %v", status, body, err)
	}
	if status, _, err := invoke(t.Context(), "/livez"); err != nil || status != 200 {
		t.Fatalf("busy liveness: %d %v", status, err)
	}
	cancel()
	for range 2 {
		select {
		case err := <-results:
			if err == nil {
				t.Fatal("canceled request succeeded")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("browser request retained")
		}
		select {
		case <-cancelled:
		case <-time.After(5 * time.Second):
			t.Fatal("upstream body read retained after disconnect")
		}
	}
	blocked.Store(false)
	for range 2 {
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("UI handler retained admission after disconnect")
		}
	}
	if len(s.admission) != 0 {
		t.Fatal("admission slots leaked")
	}

	if status, body, err := invoke(t.Context(), path); err != nil || status != 200 || body != `{"items":[]}` {
		t.Fatalf("recovery: %d %s %v", status, body, err)
	}
}

func TestFixedUpstreamPartialResponseDeadlineAndRecovery(t *testing.T) {
	const limit = 1024
	valid := `{"ok":true}` + strings.Repeat(" ", limit-len(`{"ok":true}`))
	cancelled := make(chan struct{}, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stall":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"partial":`)
			_ = http.NewResponseController(w).Flush()
			<-r.Context().Done()
			cancelled <- struct{}{}
		case "/disconnect":
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 1024\r\n\r\n{}")
			_ = conn.Close()
		default:
			// Flush forces chunked framing, so no Content-Length can pre-admit it.
			w.WriteHeader(http.StatusOK)
			_ = http.NewResponseController(w).Flush()
			_, _ = io.WriteString(w, valid)
			if r.URL.Path == "/overflow" {
				_, _ = io.WriteString(w, " ")
			}
		}
	}))
	defer api.Close()
	origin, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newClient(origin, "", limit, 250*time.Millisecond, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	for _, path := range []string{"/stall", "/disconnect", "/overflow"} {
		t.Run(path, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil {
				t.Fatal("incomplete or oversized response succeeded")
			}
			if path == "/stall" {
				select {
				case <-cancelled:
				case <-time.After(5 * time.Second):
					t.Fatal("deadline retained upstream body")
				}
			}
			request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL+"/healthy", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err = client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK || string(body) != valid {
				t.Fatalf("exact-limit recovery: bytes=%d error=%v", len(body), err)
			}
		})
	}
}
