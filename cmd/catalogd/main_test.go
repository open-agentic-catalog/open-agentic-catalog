// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
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
	if err := runArgs([]string{"unknown"}); err == nil {
		t.Fatal("unknown role succeeded")
	}
}

func TestTopLevelArgumentsFailBeforeStartingComponents(t *testing.T) {
	if err := runArgs(nil); err == nil {
		t.Fatal("missing role succeeded")
	}
	if err := runArgs([]string{"api", "--unknown"}); err == nil {
		t.Fatal("unknown API flag succeeded")
	}
	if err := runArgs([]string{"worker", "--config", filepath.Join(t.TempDir(), "missing.json")}); err == nil {
		t.Fatal("missing worker configuration succeeded")
	}

	originalArguments := os.Args
	os.Args = []string{"catalogd", "unknown"}
	t.Cleanup(func() { os.Args = originalArguments })
	if err := run(); err == nil {
		t.Fatal("run accepted an unknown role")
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
