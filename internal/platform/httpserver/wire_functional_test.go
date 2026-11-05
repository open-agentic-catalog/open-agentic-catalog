//go:build functional

package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// wireExchange preserves field lines and framing that net/http clients normalize.
func wireExchange(t *testing.T, address, request string) (int, http.Header, []byte) {
	t.Helper()
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, body
}

func TestControlWireRejectsAmbiguousHeadersBeforeMutation(t *testing.T) {
	for _, extra := range []string{"authorization: Bearer other-admin", "content-type: text/plain", "idempotency-key: other-key"} {
		t.Run(strings.Split(extra, ":")[0], func(t *testing.T) {
			server := newWebhookHTTPServer(t)
			defer server.Close()
			body := `{"slug":"wire","displayName":"Wire"}`
			request := fmt.Sprintf("POST /api/v1/namespaces HTTP/1.1\r\nHost: catalog.test\r\nAuthorization: Bearer admin\r\nContent-Type: application/json\r\nIdempotency-Key: wire-key\r\nIf-Match: \"rev-1\"\r\n%s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", extra, len(body), body)
			status, headers, response := wireExchange(t, server.Listener.Addr().String(), request)
			if status != 400 || headers.Get("X-Request-ID") == "" || !strings.Contains(string(response), `"code":"INVALID_ARGUMENT"`) {
				t.Fatalf("ambiguous request: %d %s", status, response)
			}
			listed := requestJSON(t, server.URL, http.MethodGet, "/api/v1/namespaces", "admin", "", "", nil)
			requireStatus(t, listed, 200)
			if !strings.Contains(string(listed.body), `"items":[]`) {
				t.Fatalf("rejection mutated namespaces: %s", listed.body)
			}
			// Reusing the first key succeeds: the ambiguous request reserved no command.
			clean := strings.Replace(request, extra+"\r\n", "Accept: application/json\r\naccept: application/problem+json\r\n", 1)
			status, _, response = wireExchange(t, server.Listener.Addr().String(), clean)
			if status != 201 {
				t.Fatalf("valid control: %d %s", status, response)
			}
			status, headers, replay := wireExchange(t, server.Listener.Addr().String(), clean)
			if status != 201 || headers.Get("Idempotency-Replayed") != "true" || string(replay) != string(response) {
				t.Fatal("valid command failed exact replay")
			}
		})
	}
}

func TestControlWireBodyRejectionAndRecovery(t *testing.T) {
	for _, mode := range []string{"chunked overflow", "truncated body", "slow body", "conflicting lengths"} {
		t.Run(mode, func(t *testing.T) {
			base := newWebhookHTTPServer(t)
			defer base.Close()
			server := httptest.NewUnstartedServer(base.Config.Handler)
			server.Config.ReadTimeout = 250 * time.Millisecond
			server.Start()
			defer server.Close()
			body := `{"slug":"recovered","displayName":"Recovered"}`
			headers := "POST /api/v1/namespaces HTTP/1.1\r\nHost: catalog.test\r\nAuthorization: Bearer admin\r\nContent-Type: application/json\r\nIdempotency-Key: recovery\r\nConnection: close\r\n"
			request := headers
			switch mode {
			case "chunked overflow":
				oversized := body + strings.Repeat(" ", (1<<20)+1-len(body))
				request += fmt.Sprintf("Transfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", len(oversized), oversized)
			case "conflicting lengths":
				request += fmt.Sprintf("Content-Length: %d\r\ncontent-length: %d\r\n\r\n%s", len(body), len(body)+1, body)
			default:
				request += fmt.Sprintf("Content-Length: %d\r\n\r\n{", len(body))
			}
			conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(conn, request); err != nil {
				t.Fatal(err)
			}
			if mode == "truncated body" {
				tcp, ok := conn.(*net.TCPConn)
				if !ok {
					t.Fatal("expected TCP connection")
				}
				if err := tcp.CloseWrite(); err != nil {
					t.Fatal(err)
				}
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != http.StatusBadRequest {
				t.Fatalf("rejection: %d %s %v", response.StatusCode, raw, err)
			}
			if mode != "conflicting lengths" && (!strings.Contains(string(raw), `"code":"INVALID_ARGUMENT"`) || response.Header.Get("X-Request-ID") == "") {
				t.Fatalf("missing application problem: %s", raw)
			}
			listed := requestJSON(t, server.URL, http.MethodGet, "/api/v1/namespaces", "admin", "", "", nil)
			requireStatus(t, listed, 200)
			if !strings.Contains(string(listed.body), `"items":[]`) {
				t.Fatalf("invalid input mutated state: %s", listed.body)
			}
			// Exact ceiling, chunked without Content-Length; same command key remains usable.
			valid := body + strings.Repeat(" ", (1<<20)-len(body))
			good := headers + fmt.Sprintf("Transfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", len(valid), valid)
			status, _, created := wireExchange(t, server.Listener.Addr().String(), good)
			if status != 201 {
				t.Fatalf("recovery: %d %s", status, created)
			}
			status, h, replayed := wireExchange(t, server.Listener.Addr().String(), good)
			if status != 201 || h.Get("Idempotency-Replayed") != "true" || string(created) != string(replayed) {
				t.Fatal("recovery replay changed")
			}
		})
	}
}

func TestControlWireLostCommittedResponseReplays(t *testing.T) {
	base := newWebhookHTTPServer(t)
	defer base.Close()
	completed, release := make(chan *httptest.ResponseRecorder, 1), make(chan struct{})
	var first atomic.Bool
	first.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && first.CompareAndSwap(true, false) {
			result := httptest.NewRecorder()
			base.Config.Handler.ServeHTTP(result, r)
			completed <- result
			select {
			case <-release:
			case <-r.Context().Done():
			}
			for k, v := range result.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(result.Code)
			_, _ = w.Write(result.Body.Bytes())
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer close(release)
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	body := `{"slug":"committed","displayName":"Committed"}`
	request := fmt.Sprintf("POST /api/v1/namespaces HTTP/1.1\r\nHost: catalog.test\r\nAuthorization: Bearer admin\r\nContent-Type: application/json\r\nIdempotency-Key: lost-response\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	var committed *httptest.ResponseRecorder
	select {
	case committed = <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("command did not complete")
	}
	if committed.Code != http.StatusCreated || committed.Header().Get("ETag") == "" {
		t.Fatalf("original command did not commit: %d %s", committed.Code, committed.Body.String())
	}
	_ = conn.Close()
	status, h, replayed := wireExchange(t, server.Listener.Addr().String(), request)
	if status != committed.Code || h.Get("Idempotency-Replayed") != "true" || h.Get("ETag") != committed.Header().Get("ETag") || string(replayed) != committed.Body.String() {
		t.Fatalf("lost response replay: %d %s", status, replayed)
	}
	listed := requestJSON(t, server.URL, http.MethodGet, "/api/v1/namespaces", "admin", "", "", nil)
	requireStatus(t, listed, http.StatusOK)
	var page cursorPage[catalog.Namespace]
	decodeResult(t, listed, &page)
	if len(page.Items) != 1 {
		t.Fatalf("committed command duplicated: %d", len(page.Items))
	}
	audit := requestJSON(t, server.URL, http.MethodGet, "/api/v1/audit-events?action=namespace.created", "admin", "", "", nil)
	requireStatus(t, audit, 200)
	var events cursorPage[json.RawMessage]
	decodeResult(t, audit, &events)
	if len(events.Items) != 1 {
		t.Fatalf("lost response duplicated audit: %d", len(events.Items))
	}
}

func TestProductWireDisconnectCancelsWorkAndRecovers(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan error, 1)
	router := http.NewServeMux()
	router.HandleFunc("GET /api/v1/blocked", func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		cancelled <- r.Context().Err()
	})
	router.HandleFunc("GET /api/v1/healthy", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := httptest.NewServer(productWorkDeadline(router, 3*time.Second, nil, router))
	defer server.Close()
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /api/v1/blocked HTTP/1.1\r\nHost: local\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler not reached")
	}
	_ = conn.Close()
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("disconnect became %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect retained work")
	}
	status, _, _ := wireExchange(t, server.Listener.Addr().String(), "GET /api/v1/healthy HTTP/1.1\r\nHost: local\r\nConnection: close\r\n\r\n")
	if status != 204 {
		t.Fatalf("recovery status: %d", status)
	}
}

func TestControlWireAmbiguousPreconditionPreservesRevision(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	created := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "create", "", map[string]string{"slug": "revision", "displayName": "Original"})
	requireStatus(t, created, 201)
	var namespace catalog.Namespace
	decodeResult(t, created, &namespace)
	path := "/api/v1/namespaces/" + namespace.ID
	body := `{"displayName":"Changed","reason":"Wire precondition test"}`
	request := fmt.Sprintf("PATCH %s HTTP/1.1\r\nHost: local\r\nAuthorization: Bearer admin\r\nContent-Type: application/json\r\nIdempotency-Key: update\r\nIf-Match: %s\r\nif-match: \"rev-99\"\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", path, created.header.Get("ETag"), len(body), body)
	status, _, raw := wireExchange(t, server.Listener.Addr().String(), request)
	if status != 400 {
		t.Fatalf("ambiguous precondition: %d %s", status, raw)
	}
	got := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
	requireStatus(t, got, 200)
	var retained catalog.Namespace
	decodeResult(t, got, &retained)
	if retained.Revision != namespace.Revision || retained.DisplayName != namespace.DisplayName {
		t.Fatal("ambiguous precondition changed revision")
	}
	clean := strings.Replace(request, "if-match: \"rev-99\"\r\n", "", 1)
	status, _, raw = wireExchange(t, server.Listener.Addr().String(), clean)
	if status != 200 {
		t.Fatalf("valid precondition recovery: %d %s", status, raw)
	}
}

func TestProductWireWorkDeadlineReturnsSafeProblem(t *testing.T) {
	router := http.NewServeMux()
	completed := make(chan error, 1)
	router.HandleFunc("GET /api/v1/blocked", func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done(); completed <- r.Context().Err() })
	router.HandleFunc("GET /api/v1/healthy", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := httptest.NewServer(requestIDMiddleware(productWorkDeadline(router, 250*time.Millisecond, nil, router)))
	defer server.Close()
	status, headers, body := wireExchange(t, server.Listener.Addr().String(), "GET /api/v1/blocked HTTP/1.1\r\nHost: local\r\nConnection: close\r\n\r\n")
	if status != http.StatusServiceUnavailable || headers.Get("X-Request-ID") == "" || headers.Get("Content-Type") != "application/problem+json" || !strings.Contains(string(body), `"code":"REQUEST_DEADLINE_EXCEEDED"`) {
		t.Fatalf("deadline: %d %s", status, body)
	}
	select {
	case err := <-completed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handler cancellation: %v", err)
		}
	default:
		t.Fatal("response preceded work cancellation")
	}
	status, _, _ = wireExchange(t, server.Listener.Addr().String(), "GET /api/v1/healthy HTTP/1.1\r\nHost: local\r\nConnection: close\r\n\r\n")
	if status != http.StatusNoContent {
		t.Fatalf("recovery status: %d", status)
	}
}
