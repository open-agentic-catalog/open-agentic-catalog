// Lifecycle tests exercise completion accounting without starting service components.
package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// TestRoleConfigurationRejection keeps every runnable role behind strict startup validation.
func TestRoleConfigurationRejection(t *testing.T) {
	for _, role := range []string{"api", "worker", "all"} {
		for _, raw := range []string{`{"unknown":"private-config-value"}`, `{"profile":"a","profile":"b"}`, `{} {}`} {
			t.Run(role+"/"+raw, func(t *testing.T) {
				filename := filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(filename, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				err := runArgs([]string{role, "--config", filename})
				if err == nil || !strings.HasPrefix(err.Error(), "initialize service:") || strings.Contains(err.Error(), "private-config-value") {
					t.Fatalf("startup rejection=%v", err)
				}
			})
		}
	}
}

func TestListenerConstructorsKeepBoundsAndSafeFallbackLogs(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	settings := config.HTTP{Address: "127.0.0.1:0", ReadTimeout: config.Duration{Duration: 2 * time.Second}, WriteTimeout: config.Duration{Duration: 3 * time.Second}, IdleTimeout: config.Duration{Duration: 7 * time.Second}}
	for _, tc := range []struct {
		name              string
		server            *http.Server
		read, write, idle time.Duration
		headers           int
	}{
		{"api", apiHTTPServer(settings, handler, logger), 2 * time.Second, 3 * time.Second, 7 * time.Second, 32 << 10},
		{"operations", operationsHTTPServer(settings.Address, handler, logger), 5 * time.Second, 5 * time.Second, 30 * time.Second, 8 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.server
			if s.Addr != settings.Address || s.ReadTimeout != tc.read || s.ReadHeaderTimeout != tc.read || s.WriteTimeout != tc.write || s.IdleTimeout != tc.idle || s.MaxHeaderBytes != tc.headers {
				t.Fatalf("listener bounds changed: %+v", s)
			}
			response := httptest.NewRecorder()
			s.Handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			if response.Code != http.StatusNoContent {
				t.Fatalf("handler lost: status %d", response.Code)
			}
			logs.Reset()
			s.ErrorLog.Print("private-listener-cause")
			if strings.Contains(logs.String(), "private-listener-cause") || !strings.Contains(logs.String(), "http.listener_error") {
				t.Fatalf("unsafe fallback log: %s", logs.String())
			}
		})
	}
	if listenerLogger([]*slog.Logger{logger}) != logger || listenerLogger(nil) == nil || listenerLogger([]*slog.Logger{nil}) == nil {
		t.Fatal("listener logger ownership/default changed")
	}
}

// TestServeHTTPAlreadyClosed exercises coordinated closure without binding a socket.
func TestServeHTTPAlreadyClosed(t *testing.T) {
	server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := serveHTTP(server); err != nil {
		t.Fatalf("coordinated close=%v", err)
	}
}

// TestDrainComponentsCompletion preserves all failures and consumes only the expected count.
func TestDrainComponentsCompletion(t *testing.T) {
	initial := errors.New("initial component failure")
	later := errors.New("later component failure")
	last := errors.New("last component failure")
	extra := errors.New("unrelated completion")
	for _, tc := range []struct {
		name, want string
		initial    error
		results    []error
	}{
		{name: "no components"},
		{name: "initial failure only", initial: initial, want: "initial component failure"},
		{name: "successful completions", results: []error{nil, nil}},
		{name: "later failures", results: []error{later, nil, last}, want: "later component failure\nlast component failure"},
		{name: "initial and later failures", initial: initial, results: []error{later, nil, last}, want: "initial component failure\nlater component failure\nlast component failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := make(chan error, len(tc.results)+1)
			for _, err := range tc.results {
				results <- err
			}
			results <- extra
			err := drainComponents(t.Context(), results, len(tc.results), tc.initial)
			for _, result := range tc.results {
				if result != nil && !errors.Is(err, result) {
					t.Fatalf("lost cause %v in %v", result, err)
				}
			}
			if tc.initial != nil && !errors.Is(err, tc.initial) {
				t.Fatalf("lost initial failure: %v", err)
			}
			if (tc.want == "" && err != nil) || (tc.want != "" && (err == nil || err.Error() != tc.want)) {
				t.Fatalf("result=%v want=%s", err, tc.want)
			}
			if len(results) != 1 || !errors.Is(<-results, extra) || errors.Is(err, extra) {
				t.Fatal("drain consumed an unrelated completion")
			}
		})
	}
}

// TestDrainComponentsCancellation retains the triggering failure when the shared budget ends.
func TestDrainComponentsCancellation(t *testing.T) {
	initial := errors.New("initial component failure")
	for _, expired := range []bool{false, true} {
		var ctx context.Context
		var cancel context.CancelFunc
		want := context.Canceled
		if expired {
			ctx, cancel = context.WithDeadline(t.Context(), time.Unix(0, 0))
			want = context.DeadlineExceeded
		} else {
			ctx, cancel = context.WithCancel(t.Context())
		}
		cancel()
		err := drainComponents(ctx, nil, 2, initial)
		if !errors.Is(err, initial) || !errors.Is(err, want) || !strings.Contains(err.Error(), "drain service components:") {
			t.Fatalf("drain result=%v, want initial cause and %v", err, want)
		}
		if err := drainComponents(ctx, nil, 0, initial); !errors.Is(err, initial) {
			t.Fatalf("completed drain changed its result: %v", err)
		}
	}
}

// TestDrainComponentsWaitsForCompletions proves outstanding work is joined before returning.
func TestDrainComponentsWaitsForCompletions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		results := make(chan error)
		done := make(chan error, 1)
		later := errors.New("last completion failed")
		go func() { done <- drainComponents(t.Context(), results, 2, nil) }()
		results <- nil
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("returned before last completion: %v", err)
		default:
		}
		results <- later
		if err := <-done; !errors.Is(err, later) {
			t.Fatalf("last failure lost: %v", err)
		}
	})
}

// TestDrainComponentsSharedDeadline uses virtual time to retain completed failures when another component stalls.
func TestDrainComponentsSharedDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		results := make(chan error)
		done := make(chan error, 1)
		initial, later := errors.New("initial failure"), errors.New("completed failure")
		go func() { done <- drainComponents(ctx, results, 2, initial) }()
		results <- later
		// The next receive advances virtual time only once the drain is blocked.
		err := <-done
		if !errors.Is(err, initial) || !errors.Is(err, later) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline lost failures: %v", err)
		}
	})
}
