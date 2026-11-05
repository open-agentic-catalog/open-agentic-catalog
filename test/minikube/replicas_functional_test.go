//go:build minikube && functional

// Functional tests retain real local HTTP and TLS exchanges.
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReplicaIsolationChecksEveryAPI(t *testing.T) {
	for _, broken := range []bool{false, true} {
		var paths []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer other-tenant" {
				t.Error("isolation check lost tenant identity")
			}
			if broken && strings.HasPrefix(r.URL.Path, "/2/") {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		client := replicaHTTPClient()
		h := &harness{ctx: context.Background(), tokens: map[string]string{"other": "other-tenant"}}
		err := h.verifyReplicaIsolation([]string{server.URL + "/0", server.URL + "/1", server.URL + "/2"}, []httpResult{{Body: []byte(`{"id":"namespace-id"}`)}}, client)
		client.CloseIdleConnections()
		server.Close()
		if (err != nil) != broken || len(paths) != 3 {
			t.Fatalf("broken=%v paths=%v err=%v", broken, paths, err)
		}
	}
}

func TestUICommandHeadersAndBoundedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:4180" || r.Header.Get("Origin") != "http://127.0.0.1:4180" || r.Header.Get("X-Catalog-Request") != "1" || r.Header.Get("Cookie") != "session=test" || r.Header.Get("Idempotency-Key") != "command-key" || r.Header.Get("If-Match") != `"rev-1"` {
			t.Error("UI forwarding inputs were lost")
		}
		if r.URL.Path == "/large" {
			_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
			return
		}
		w.Header().Set("ETag", `"rev-2"`)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	h := &harness{ctx: context.Background()}
	client := replicaHTTPClient()
	defer client.CloseIdleConnections()
	result, err := h.uiCommand(client, server.URL, "PATCH", "/small", `{}`, "session=test", "command-key", `"rev-1"`)
	if err != nil || result.Status != 200 || string(result.Body) != `{"ok":true}` || result.Header.Get("ETag") != `"rev-2"` {
		t.Fatalf("UI response was lost: %#v, %v", result, err)
	}
	if _, err := h.uiCommand(client, server.URL, "PATCH", "/large", `{}`, "session=test", "command-key", `"rev-1"`); err == nil {
		t.Fatal("oversized UI response accepted")
	}
}
