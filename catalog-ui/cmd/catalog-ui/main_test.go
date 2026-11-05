// Command tests isolate local startup and listener decisions from sockets and signals.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/catalog-ui/internal/web"
)

type listenerFixture struct {
	listen   func() error
	shutdown func(context.Context) error
	close    func() error
}

func (f listenerFixture) ListenAndServe() error              { return f.listen() }
func (f listenerFixture) Shutdown(ctx context.Context) error { return f.shutdown(ctx) }
func (f listenerFixture) Close() error                       { return f.close() }

// commandConfig writes only synthetic local startup inputs; no configured address is dialed.
func commandConfig(t *testing.T) (string, web.Config) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	write("index.html", `<!doctype html><script src="./assets/app.js"></script>`)
	write("assets/app.js", "export const ready = true;")
	c := web.Config{LogLevel: "info", LogFormat: "text", Listen: "127.0.0.1:0", Environment: "development", Origin: "http://127.0.0.1:4173", Upstream: "http://127.0.0.1:8080", Assets: dir, CookieName: "catalog-ui-unit", SessionSeconds: 3600, TimeoutSeconds: 7, RequestBytes: 4096, ResponseBytes: 8192, AuthMode: "development",
		KeysFile:             write("keys.json", `[{"hash":"`+strings.Repeat("ab", 64)+`","block":"`+strings.Repeat("cd", 32)+`"}]`),
		DevelopmentUsersFile: write("users.json", `[{"alias":"reader","label":"Reader","token":"synthetic-token"}]`)}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return write("config.json", string(raw)), c
}

// TestRunHostStartup rejects invalid local inputs before handing off a listener.
func TestRunHostStartup(t *testing.T) {
	for _, mode := range []string{"missing config", "malformed config", "missing assets", "invalid keys"} {
		t.Run(mode, func(t *testing.T) {
			filename, c := commandConfig(t)
			switch mode {
			case "missing config":
				filename = filepath.Join(t.TempDir(), "missing.json")
			case "malformed config":
				if err := os.WriteFile(filename, []byte(`{"unknown":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing assets":
				if err := os.Remove(filepath.Join(c.Assets, "index.html")); err != nil {
					t.Fatal(err)
				}
			case "invalid keys":
				if err := os.WriteFile(c.KeysFile, []byte(`[]`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			err := runHost(filename, func(*http.Server, func()) error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("startup error=%v, listener called=%v", err, called)
			}
		})
	}
}

// TestRunHostListenerHandoff preserves HTTP bounds, readiness/drain binding and listener results.
func TestRunHostListenerHandoff(t *testing.T) {
	for _, cause := range []error{nil, errors.New("listener result")} {
		t.Run(fmt.Sprint(cause), func(t *testing.T) {
			filename, c := commandConfig(t)
			output, err := os.CreateTemp(t.TempDir(), "host-output-*")
			if err != nil {
				t.Fatal(err)
			}
			original := os.Stdout
			os.Stdout = output
			defer func() { os.Stdout = original; _ = output.Close() }()
			calls := 0
			err = runHost(filename, func(server *http.Server, drain func()) error {
				calls++
				if server.ErrorLog == nil {
					t.Fatal("unsafe standard HTTP fallback")
				}
				if server.Addr != c.Listen || server.ReadHeaderTimeout != 10*time.Second || server.ReadTimeout != 7*time.Second || server.WriteTimeout != 12*time.Second || server.IdleTimeout != 60*time.Second || server.MaxHeaderBytes != 32<<10 {
					t.Fatalf("listener bounds = %+v", server)
				}
				for _, phase := range []struct {
					drain  bool
					path   string
					status int
				}{{false, "/readyz", 200}, {true, "/readyz", 503}, {false, "/livez", 200}} {
					if phase.drain {
						drain()
						drain()
					}
					w := httptest.NewRecorder()
					server.Handler.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, c.Origin+phase.path, nil))
					if w.Code != phase.status {
						t.Fatalf("%s status=%d, want %d", phase.path, w.Code, phase.status)
					}
				}
				return cause
			})
			if !errors.Is(err, cause) || calls != 1 {
				t.Fatalf("result=%v calls=%d", err, calls)
			}
			raw, readErr := os.ReadFile(output.Name())
			stopped := 0
			if cause == nil {
				stopped = 1
			}
			if readErr != nil || strings.Count(string(raw), "event=process.logging_configured") != 1 || strings.Count(string(raw), "event=process.draining") != 1 || strings.Count(string(raw), "event=process.stopped") != stopped || strings.Contains(string(raw), "listener result") {
				t.Fatalf("lifecycle output: %s, %v", raw, readErr)
			}
		})
	}
}

// TestServeListenerCompletion preserves clean close and sanitizes unexpected listener failures.
func TestServeListenerCompletion(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "closed", err: http.ErrServerClosed},
		{name: "wrapped closed", err: fmt.Errorf("listener: %w", http.ErrServerClosed)},
		{name: "failure", err: errors.New("private-listener-diagnostic"), want: "UI listener failed"},
		{name: "unexpected nil", want: "UI listener failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			listener := listenerFixture{
				listen:   func() error { return tc.err },
				shutdown: func(context.Context) error { calls++; return nil },
				close:    func() error { calls++; return nil },
			}
			err := serve(t.Context(), listener, func() { calls++ })
			if (tc.want == "" && err != nil) || (tc.want != "" && (err == nil || err.Error() != tc.want)) || calls != 0 {
				t.Fatalf("result=%v, unexpected shutdown/drain/close calls=%d", err, calls)
			}
		})
	}
}

// TestServeCancellation uses controlled completion to assert drain/shutdown/forced-close ordering.
func TestServeCancellation(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		expired               bool
		shutdownErr, closeErr error
	}{
		{name: "graceful"},
		{name: "expired parent", expired: true},
		{name: "deadline", shutdownErr: context.DeadlineExceeded},
		{name: "shutdown failure", shutdownErr: errors.New("private-shutdown-diagnostic")},
		{name: "close failure", shutdownErr: context.DeadlineExceeded, closeErr: errors.New("private-close-diagnostic")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.Context()
			if tc.expired {
				var stop context.CancelFunc
				parent, stop = context.WithDeadline(parent, time.Unix(0, 0))
				defer stop()
			}
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			released, finished := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			t.Cleanup(func() {
				release()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("listener fixture did not finish")
				}
			})
			var events []string
			var shutdownError func() error
			listener := listenerFixture{
				listen: func() error { defer close(finished); cancel(); <-released; return http.ErrServerClosed },
				shutdown: func(shutdown context.Context) error {
					events = append(events, "shutdown")
					shutdownError = shutdown.Err
					deadline, ok := shutdown.Deadline()
					if ctx.Err() == nil || shutdown.Err() != nil || !ok || !deadline.After(time.Now()) || time.Until(deadline) > 15*time.Second || time.Until(deadline) < 14*time.Second {
						t.Error("shutdown did not receive a fresh bounded context after cancellation")
					}
					if tc.shutdownErr == nil {
						release()
					}
					return tc.shutdownErr
				},
				close: func() error { events = append(events, "close"); release(); return tc.closeErr },
			}
			err := serve(ctx, listener, func() { events = append(events, "drain") })
			want := []string{"drain", "shutdown"}
			if tc.shutdownErr != nil {
				want = append(want, "close")
				if err == nil || err.Error() != "UI shutdown timed out" {
					t.Fatalf("shutdown error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(events, want) || shutdownError == nil || !errors.Is(shutdownError(), context.Canceled) {
				t.Fatalf("shutdown events=%v, want %v; context must be released", events, want)
			}
		})
	}
}

func TestHostVersionCommandNeedsNoConfiguration(t *testing.T) {
	previousVersion, previousCommit, previousState := version, commit, sourceState
	version, commit, sourceState = "ui-test", "own-commit", "modified"
	t.Cleanup(func() { version, commit, sourceState = previousVersion, previousCommit, previousState })
	for _, command := range []string{"version", "--version"} {
		output, err := os.CreateTemp(t.TempDir(), "stdout")
		if err != nil {
			t.Fatal(err)
		}
		previous := os.Stdout
		os.Stdout = output
		err = runArgs([]string{command})
		os.Stdout = previous
		if closeErr := output.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		raw, readErr := os.ReadFile(output.Name())
		var got map[string]string
		if err != nil || readErr != nil || json.Unmarshal(raw, &got) != nil || len(got) != 3 || got["version"] != "ui-test" || got["commit"] != "own-commit" || got["sourceState"] != "modified" {
			t.Fatalf("version command: %s %v %v", raw, err, readErr)
		}
	}
}

func TestHostArgumentsFailBeforeStarting(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"--private-setting"}, "flag provided but not defined"},
		{"invalid probe flag", []string{"--probe=not-a-boolean"}, "invalid boolean value"},
		{"positional argument", []string{"unexpected"}, "unexpected arguments; use catalog-ui --help"},
		{"missing config", []string{"--config", filepath.Join(t.TempDir(), "absent.json")}, "configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runArgs(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("argument admission: %v; want %q", err, tc.want)
			}
		})
	}
	if err := runArgs([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help lost its classification: %v", err)
	}
}

func TestServeShutdownStillBoundsListenerCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		release, finished := make(chan struct{}), make(chan struct{})
		releaseListener := sync.OnceFunc(func() { close(release) })
		defer releaseListener()
		var events []string
		listener := listenerFixture{
			listen: func() error {
				defer close(finished)
				cancel()
				<-release
				return http.ErrServerClosed
			},
			shutdown: func(context.Context) error {
				events = append(events, "shutdown")
				return nil // A successful shutdown does not prove the listener has returned.
			},
			close: func() error {
				events = append(events, "close")
				releaseListener()
				return nil
			},
		}
		started := time.Now()
		err := serve(ctx, listener, func() { events = append(events, "drain") })
		if err == nil || err.Error() != "UI shutdown timed out" || time.Since(started) != 15*time.Second {
			t.Fatalf("listener completion budget: %v, elapsed %v", err, time.Since(started))
		}
		<-finished
		if !reflect.DeepEqual(events, []string{"drain", "shutdown", "close"}) {
			t.Fatalf("shutdown sequence = %v", events)
		}
	})
}
