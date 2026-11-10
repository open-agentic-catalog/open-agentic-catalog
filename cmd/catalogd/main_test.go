// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestTopLevelHelpVersionAndUnknownRole(t *testing.T) {
	previousVersion, previousCommit, previousState := version, commit, sourceState
	version, commit, sourceState = "1.2.3-unit", "unit-commit", "modified"
	t.Cleanup(func() { version, commit, sourceState = previousVersion, previousCommit, previousState })
	const usage = "usage: catalogd <api|worker|all|version> [--config <path>]\n"
	const build = "open-agentic-catalog 1.2.3-unit (unit-commit) [modified]\n"
	for _, test := range []struct{ argument, want string }{
		{argument: "help", want: usage},
		{argument: "-h", want: usage},
		{argument: "--help", want: usage},
		{argument: "version", want: build},
		{argument: "--version", want: build},
	} {
		t.Run(test.argument, func(t *testing.T) {
			output, err := os.CreateTemp(t.TempDir(), "stdout")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := output.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
					t.Error(err)
				}
			})
			// Keep process-global stdout replacement within each sequential call.
			runWithOutput := func() error {
				previous := os.Stdout
				os.Stdout = output
				defer func() { os.Stdout = previous }()
				return runArgs([]string{test.argument})
			}
			if err := runWithOutput(); err != nil {
				t.Fatalf("runArgs(%q) returned %v", test.argument, err)
			}
			contents, err := os.ReadFile(output.Name())
			if err != nil || string(contents) != test.want {
				t.Fatalf("output = %q, %v; want %q", contents, err, test.want)
			}
			if err := output.Close(); err != nil {
				t.Fatal(err)
			}
			if err := runWithOutput(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("closed stdout returned %v; want the write error", err)
			}
		})
	}
	output, diagnostic, err := runCatalogdPreflightUnit(t, []string{"UNKNOWN_PRIVATE_ROLE"})
	assertCatalogdPreflightRefusal(t, output, diagnostic, err, "unknown role; use catalogd --help", "command", nil)
}

func TestTopLevelArgumentsFailBeforeStartingComponents(t *testing.T) {
	// Isolate supported runtime inputs without discarding launcher/tool settings.
	for _, name := range config.EnvironmentVariables() {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, message string
		arguments     []string
		invalidDriver bool
	}{
		{name: "missing role", message: "usage: catalogd <api|worker|all|version> [--config <path>]"},
		{name: "unknown API flag", arguments: []string{"api", "--UNKNOWN_PRIVATE_FLAG"}, message: "invalid flags; use catalogd api --help", invalidDriver: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.invalidDriver {
				// A parser regression still reaches a preflight refusal, not
				// successful runtime composition. Exact stage/message checks
				// ensure the driver cannot mask the intended flag rejection.
				t.Setenv("CATALOG_DATABASE_DRIVER", "PRIVATE_FLAG_GUARD_DRIVER")
			}
			output, diagnostic, err := runCatalogdPreflightUnit(t, test.arguments)
			assertCatalogdPreflightRefusal(t, output, diagnostic, err, test.message, "command", nil)
		})
	}
	for _, role := range []string{"api", "worker", "all"} {
		t.Run(role, func(t *testing.T) {
			// A missing explicit file would mask driver validation. Calibrate
			// the supported override without --config, then test file bypass
			// independently with otherwise benign configuration inputs.
			t.Run("invalid driver", func(t *testing.T) {
				t.Setenv("CATALOG_DATABASE_DRIVER", "PRIVATE_UNSUPPORTED_DRIVER")
				arguments := []string{role}
				output, diagnostic, err := runCatalogdPreflightUnit(t, arguments)
				assertCatalogdPreflightRefusal(t, output, diagnostic, err, "config: database.driver must be memory or postgres", "startup", nil)
				assertCatalogdRoleHelp(t, role, arguments)
			})
			t.Run("missing private configuration", func(t *testing.T) {
				missing := filepath.Join(t.TempDir(), "PRIVATE_CONFIGURATION_PATH", "missing.json")
				arguments := []string{role, "--config", missing}
				output, diagnostic, err := runCatalogdPreflightUnit(t, arguments)
				assertCatalogdPreflightRefusal(t, output, diagnostic, err, "cannot read configuration file", "startup", fs.ErrNotExist)
				assertCatalogdRoleHelp(t, role, arguments)
			})
		})
	}

	originalArguments := os.Args
	os.Args = []string{"catalogd", "UNKNOWN_PRIVATE_ROLE"}
	t.Cleanup(func() { os.Args = originalArguments })
	output, diagnostic, err := captureCatalogdUnitStreams(t, run)
	assertCatalogdPreflightRefusal(t, output, diagnostic, err, "unknown role; use catalogd --help", "command", nil)
}

// captureCatalogdUnitStreams scopes process-global streams to a sequential call.
func captureCatalogdUnitStreams(t *testing.T, call func() error) ([]byte, []byte, error) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	}()
	diagnostic, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := diagnostic.Close(); err != nil {
			t.Error(err)
		}
	}()
	callErr := func() error {
		previousOutput, previousDiagnostic := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = output, diagnostic
		defer func() { os.Stdout, os.Stderr = previousOutput, previousDiagnostic }()
		return call()
	}()
	stdout, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.ReadFile(diagnostic.Name())
	if err != nil {
		t.Fatal(err)
	}
	return stdout, stderr, callErr
}

// runCatalogdPreflightUnit exercises only rejection/help before composition.
func runCatalogdPreflightUnit(t *testing.T, arguments []string) ([]byte, []byte, error) {
	t.Helper()
	var served atomic.Int32
	output, diagnostic, err := captureCatalogdUnitStreams(t, func() error {
		return runArgsServing(arguments, func(*http.Server) error {
			served.Add(1)
			return errors.New("unit preflight unexpectedly reached a listener")
		})
	})
	if served.Load() != 0 {
		t.Fatal("preflight reached a serving callback")
	}
	return output, diagnostic, err
}

// assertCatalogdPreflightRefusal checks reviewed output, not private Error text.
func assertCatalogdPreflightRefusal(t *testing.T, output, diagnostic []byte, err error, message, stage string, cause error) {
	t.Helper()
	if err == nil || errors.Is(err, flag.ErrHelp) || safeerr.Message(err) != message || safeerr.Stage(err) != stage || len(output) != 0 || len(diagnostic) != 0 {
		t.Fatalf("preflight refusal: message=%q stage=%q stdout=%q stderr=%q", safeerr.Message(err), safeerr.Stage(err), output, diagnostic)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("preflight lost expected cause %v", cause)
	}
}

// assertCatalogdRoleHelp uses the successful neighbor of each refusal control.
func assertCatalogdRoleHelp(t *testing.T, role string, arguments []string) {
	t.Helper()
	want := "Usage of catalogd " + role + ":\n  -config\n    optional path to a service JSON configuration baseline\n"
	for _, alias := range []string{"-h", "--help"} {
		t.Run(alias, func(t *testing.T) {
			args := append(append([]string{}, arguments...), alias)
			output, diagnostic, err := runCatalogdPreflightUnit(t, args)
			if !errors.Is(err, flag.ErrHelp) || string(output) != want || len(diagnostic) != 0 || safeerr.Message(err) != "invalid flags; use catalogd "+role+" --help" || safeerr.Stage(err) != "command" {
				t.Fatalf("role help lost safe dispatch: message=%q stage=%q stdout=%q stderr=%q", safeerr.Message(err), safeerr.Stage(err), output, diagnostic)
			}
		})
	}
}

func TestDependencySupervisorRetriesOnlyDependencyFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logger := slog.New(slog.DiscardHandler)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		var degraded []bool
		started := time.Now()
		err := superviseDependency(ctx, logger, "test component", config.Defaults().Worker, bytes.NewReader(nil), func(context.Context) error {
			calls++
			if calls < 3 {
				return catalog.E(catalog.CodeDependencyUnavailable, "database unavailable")
			}
			cancel()
			return context.Canceled
		}, func(value bool) { degraded = append(degraded, value) })
		if !errors.Is(err, context.Canceled) || calls != 3 || time.Since(started) != 2250*time.Millisecond {
			t.Fatalf("supervisor returned %v after %d calls in %v", err, calls, time.Since(started))
		}
		if len(degraded) != 5 || degraded[0] || !degraded[1] || degraded[2] || !degraded[3] || degraded[4] {
			t.Fatalf("dependency degradation transitions = %#v", degraded)
		}
		permanent := errors.New("permanent failure")
		if err := superviseDependency(t.Context(), logger, "test component", config.Defaults().Worker, bytes.NewReader(nil), func(context.Context) error { return permanent }); !errors.Is(err, permanent) {
			t.Fatalf("permanent failure returned %v", err)
		}
		dependency := catalog.E(catalog.CodeDependencyUnavailable, "database unavailable")
		if retryableDependencyFailure(nil) {
			t.Fatal("nil error was retryable")
		}
		if retryableDependencyFailure(emptyJoinedError{}) {
			t.Fatal("empty joined error was retryable")
		}
		if !retryableDependencyFailure(errors.Join(dependency, dependency)) {
			t.Fatal("joined dependency failures were not retryable")
		}
		if retryableDependencyFailure(errors.Join(dependency, permanent)) {
			t.Fatal("joined permanent failure was hidden by a dependency outage")
		}
		if err := superviseDependency(t.Context(), logger, "test component", config.Defaults().Worker, bytes.NewReader(nil), func(context.Context) error { return nil }); err != nil {
			t.Fatalf("successful component returned %v", err)
		}
	})
}

type emptyJoinedError struct{}

func (emptyJoinedError) Error() string { return "empty joined error" }

func (emptyJoinedError) Unwrap() []error { return nil }

func TestComponentStateObserverKeepsComponentsIndependent(t *testing.T) {
	t.Parallel()
	components := []observability.Component{
		observability.ComponentWorker, observability.ComponentOutbox,
		observability.ComponentSourceMaintenance, observability.ComponentHousekeeping,
		observability.ComponentSemanticMaintenance,
	}
	for _, component := range components {
		t.Run(string(component), func(t *testing.T) {
			registry := observability.New(nil)
			want := map[observability.Component]observability.ComponentState{observability.ComponentAPI: observability.ComponentStopped}
			for _, other := range components {
				want[other] = observability.ComponentRunning
			}
			for other, state := range want {
				registry.SetComponent(other, state)
			}
			observe := componentStateObserver(registry, component)
			for _, degraded := range []bool{false, true, false} {
				observe(degraded)
				want[component] = observability.ComponentRunning
				if degraded {
					want[component] = observability.ComponentDegraded
				}
				_, gotDegraded, states, _ := registry.HealthState()
				if gotDegraded != degraded || !maps.Equal(states, want) {
					t.Fatalf("degraded=%t components=%v; want degraded=%t components=%v", gotDegraded, states, degraded, want)
				}
			}
		})
	}
}

func TestDependencyDiagnosticsAreRedactedAndUpdateComponentState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		registry := observability.New(nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		observe := componentStateObserver(registry, observability.ComponentWorker)
		done := make(chan error, 1)
		calls := 0
		go func() {
			done <- superviseDependency(ctx, logger, "catalog test worker", config.Defaults().Worker, bytes.NewReader(nil), func(context.Context) error {
				calls++
				return catalog.Wrap(catalog.CodeDependencyUnavailable, "database unavailable", errors.New("postgres://user:private-password@database/catalog"))
			}, observe)
		}()
		synctest.Wait()
		_, degraded, components, _ := registry.HealthState()
		if !degraded || components[observability.ComponentWorker] != observability.ComponentDegraded {
			t.Fatalf("component state = degraded %v components %v", degraded, components)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if calls < 2 || strings.Count(logs.String(), "dependency.unavailable") != 1 || strings.Contains(logs.String(), "recovered") {
			t.Fatalf("callback reentry or noisy retries: calls=%d logs=%s", calls, logs.String())
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("dependency supervisor returned %v", err)
		}
		if strings.Contains(logs.String(), "private-password") || strings.Contains(logs.String(), "postgres://") || !strings.Contains(logs.String(), "error_code=DEPENDENCY_UNAVAILABLE") || !strings.Contains(logs.String(), "retry_delay_ms=750") {
			t.Fatalf("dependency diagnostics = %s", logs.String())
		}
	})
}

func TestDependencySupervisorStopsPromptlyDuringRetryBackoff(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint("deadline=", deadline), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				want := context.Canceled
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
					want = context.DeadlineExceeded
				}
				defer cancel()
				calls := 0
				done := make(chan error, 1)
				started := time.Now()
				go func() {
					done <- superviseDependency(ctx, slog.New(slog.DiscardHandler), "test component", config.Defaults().Worker, bytes.NewReader(nil), func(context.Context) error {
						calls++
						return catalog.E(catalog.CodeDependencyUnavailable, "database unavailable")
					})
				}()
				synctest.Wait()
				if calls != 1 {
					t.Fatal("dependency supervisor did not call the component")
				}
				time.Sleep(250 * time.Millisecond)
				if !deadline {
					cancel()
				}
				if err := <-done; !errors.Is(err, want) || calls != 1 || time.Since(started) != 250*time.Millisecond {
					t.Fatalf("retry backoff did not stop promptly: %v calls=%d", err, calls)
				}
			})
		})
	}
}
