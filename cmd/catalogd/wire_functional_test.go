//go:build functional

package main

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestConfiguredListenersBoundWireHeadersAndRecover(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for name, server := range map[string]*http.Server{
		"api":    apiHTTPServer(config.HTTP{ReadTimeout: config.Duration{Duration: 250 * time.Millisecond}, WriteTimeout: config.Duration{Duration: time.Second}}, handler),
		"worker": operationsHTTPServer("", handler),
	} {
		t.Run(name, func(t *testing.T) {
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() {
				_ = server.Close()
				if err := <-done; !errors.Is(err, http.ErrServerClosed) {
					t.Errorf("serve: %v", err)
				}
			})
			exchange := func(raw string, want int) {
				t.Helper()
				conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conn.Close() }()
				if err := conn.SetDeadline(time.Now().Add(server.ReadHeaderTimeout + 3*time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(conn, raw); err != nil {
					t.Fatal(err)
				}
				response, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if want == 0 {
					if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatalf("incomplete header was not closed by server: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != want {
					t.Fatalf("status=%d want=%d", response.StatusCode, want)
				}
			}
			healthy := "GET /livez HTTP/1.1\r\nHost: local\r\nConnection: close\r\n\r\n"
			exchange(healthy, 204)
			// Go permits parser overhead beyond MaxHeaderBytes; probe well outside it.
			exchange("GET /livez HTTP/1.1\r\nHost: local\r\nX-Fill: "+strings.Repeat("x", server.MaxHeaderBytes+8192)+"\r\n\r\n", 431)
			exchange("GET /livez HTTP/1.1\r\nHost: local\r\nX-Incomplete: ", 0)
			exchange(healthy, 204)
		})
	}
}

// channelLog hands each complete diagnostic to the assertion goroutine.
type channelLog chan string

func (c channelLog) Write(raw []byte) (int, error) { c <- string(raw); return len(raw), nil }

func TestConfiguredListenersContainWirePanics(t *testing.T) {
	for _, name := range []string{"api", "worker"} {
		for _, mode := range []string{"early", "late", "abort", "fallback"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				logs := make(channelLog, 4)
				logger := slog.New(slog.NewTextHandler(logs, nil))
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/healthy" {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					if mode == "late" {
						_, _ = w.Write([]byte("accepted-prefix"))
						if err := http.NewResponseController(w).Flush(); err != nil {
							t.Error(err)
						}
					}
					if mode == "abort" {
						panic(http.ErrAbortHandler)
					}
					panic("SYNTHETIC_PRIVATE_PANIC")
				})
				server := apiHTTPServer(config.Defaults().HTTP, handler, logger)
				if name == "worker" {
					server = operationsHTTPServer("", handler, logger)
				}
				if mode == "fallback" {
					server.Handler = handler
				}
				if server.ErrorLog == nil {
					t.Fatal("raw HTTP fallback remains enabled")
				}
				listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- server.Serve(listener) }()
				t.Cleanup(func() {
					_ = server.Close()
					if err := <-done; !errors.Is(err, http.ErrServerClosed) {
						t.Error(err)
					}
				})
				client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
				t.Cleanup(client.CloseIdleConnections)
				request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+"/panic", nil)
				response, err := client.Do(request)
				if mode == "late" {
					if err != nil {
						t.Fatal(err)
					}
					raw, readErr := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if !errors.Is(readErr, io.ErrUnexpectedEOF) || string(raw) != "accepted-prefix" {
						t.Fatalf("late panic changed/finished response: %q %v", raw, readErr)
					}
				} else if err == nil {
					_ = response.Body.Close()
					t.Fatal("panic did not abort connection")
				}
				request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+"/healthy", nil)
				response, err = client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusNoContent {
					t.Fatal("listener did not recover")
				}
				if mode != "abort" {
					select {
					case entry := <-logs:
						if !strings.Contains(entry, "level=ERROR") || strings.Contains(entry, "SYNTHETIC") || strings.Contains(entry, "goroutine") || strings.Contains(entry, "127.0.0.1") {
							t.Fatalf("unsafe diagnostic: %q", entry)
						}
					case <-time.After(time.Second):
						t.Fatal("missing panic diagnostic")
					}
				}
				select {
				case entry := <-logs:
					t.Fatalf("duplicate/abort diagnostic: %q", entry)
				default:
				}
			})
		}
	}
}
