//go:build functional

package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProxyPreservesMCPBytesHeadersAndStreamingCancellation(t *testing.T) {
	const key = "01234567890123456789012345678901"
	const payload = `{ "jsonrpc":"2.0", "id":9007199254740993, "method":"tools/call", "params":{"name":"echo","arguments":{"message":"exact"}} }`
	received := make(chan string, 1)
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Mcp-Test-Key") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credential forwarded")
		}
		if r.URL.Path != "/mcp" || r.Header.Get("Mcp-Protocol-Version") != "2025-11-25" || r.Header.Get("Mcp-Session-Id") != "session-test" {
			t.Error("MCP request headers lost")
		}
		if r.Method == http.MethodPost {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			received <- string(data)
			w.Header().Set("Mcp-Session-Id", "session-test")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":9007199254740993,"result":{"content":[{"type":"text","text":"Echo: exact"}]}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: first\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		<-r.Context().Done()
		close(cancelled)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(key, target)
	defer f.transport.CloseIdleConnections()
	proxy := httptest.NewTLSServer(f)
	defer proxy.Close()
	client := proxy.Client()
	client.Timeout = 3 * time.Second
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, proxy.URL+"/mcp", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Mcp-Test-Key", key)
	request.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	request.Header.Set("Mcp-Session-Id", "session-test")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if <-received != payload || !strings.Contains(string(body), `"id":9007199254740993`) || response.Header.Get("Mcp-Session-Id") != "session-test" {
		t.Fatal("proxy changed MCP bytes/session")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, err := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream.Header = request.Header.Clone()
	response, err = client.Do(stream)
	if err != nil {
		t.Fatal(err)
	}
	// A buffering proxy would block here until the deadline instead of delivering the first event.
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "event: message\n" {
		t.Fatalf("SSE first event %q: %v", line, err)
	}
	cancel()
	_ = response.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request not cancelled")
	}
}

func TestUnavailableBackendHasBoundedGenericFailure(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	upstream.Close()
	f := newFixture("test-key", target)
	defer f.transport.CloseIdleConnections()
	proxy := httptest.NewServer(f)
	defer proxy.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, proxy.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Mcp-Test-Key", "test-key")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadGateway || string(body) != "fixture upstream unavailable\n" {
		t.Fatalf("failure %d: %s", response.StatusCode, body)
	}
}
