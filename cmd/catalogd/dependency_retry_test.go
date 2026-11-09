// Supervisor timing uses virtual time and private entropy inputs, never sleeps
// against the wall clock or replaces process-global randomness.
package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestDependencyRetrySampling(t *testing.T) {
	for _, tc := range []struct {
		name         string
		window, want time.Duration
		entropy      []byte
	}{
		{"lower", 10, 5, []byte{0}},
		{"interior", 10, 8, []byte{3}},
		{"upper", 10, 10, []byte{5}},
		{"odd upper", 11, 11, []byte{6}},
		{"odd lower", 11, 5, []byte{0}},
		{"failure midpoint", 10, 7, nil},
		{"odd failure midpoint", 11, 8, nil},
		{"fractional", 1250 * time.Millisecond, 625 * time.Millisecond, make([]byte, 8)},
		{"maximum", 5 * time.Minute, 150 * time.Second, make([]byte, 8)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dependencyRetryDelay(tc.window, bytes.NewReader(tc.entropy)); got != tc.want {
				t.Fatalf("delay=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestSupervisorGrowthAndElapsedRunReset(t *testing.T) {
	for _, tc := range []struct {
		name             string
		initial, ceiling time.Duration
		runs, waits      []time.Duration
	}{
		{"growth and cap", time.Second, 30 * time.Second, make([]time.Duration, 8), []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second}},
		{"fractional cap", 1250 * time.Millisecond, 2500 * time.Millisecond, make([]time.Duration, 3), []time.Duration{625 * time.Millisecond, 1250 * time.Millisecond, 1250 * time.Millisecond}},
		{"equal bounds", time.Second, time.Second, make([]time.Duration, 3), []time.Duration{500 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond}},
		{"run resets at minute", time.Second, 30 * time.Second, []time.Duration{0, 0, time.Minute - time.Nanosecond, time.Minute, 0}, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 500 * time.Millisecond, time.Second}},
		{"waiting does not reset", 2 * time.Minute, 5 * time.Minute, make([]time.Duration, 4), []time.Duration{time.Minute, 2 * time.Minute, 150 * time.Second, 150 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				policy := config.Defaults().Worker
				policy.DependencyRetryInitialDelay.Duration, policy.DependencyRetryCeiling.Duration = tc.initial, tc.ceiling
				// The same configuration starts fresh, with no retained failure counter.
				for range 2 {
					var calls []time.Time
					err := superviseDependency(t.Context(), slog.New(slog.DiscardHandler), "test", policy, bytes.NewReader(make([]byte, 128)), func(context.Context) error {
						calls = append(calls, time.Now())
						if len(calls) > len(tc.runs) {
							return nil
						}
						time.Sleep(tc.runs[len(calls)-1])
						return catalog.E(catalog.CodeDependencyUnavailable, "unavailable")
					})
					if err != nil || len(calls) != len(tc.runs)+1 {
						t.Fatalf("calls=%d err=%v", len(calls), err)
					}
					for i, want := range tc.waits {
						if got := calls[i+1].Sub(calls[i]) - tc.runs[i]; got != want {
							t.Fatalf("wait %d=%v want=%v", i, got, want)
						}
					}
				}
			})
		})
	}
}

func TestSupervisorWaitsForSampledDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		done := make(chan error, 1)
		go func() {
			// 500,000,000 selects the upper endpoint of the initial 1s window.
			done <- superviseDependency(t.Context(), slog.New(slog.DiscardHandler), "test", config.Defaults().Worker, bytes.NewReader([]byte{0x1d, 0xcd, 0x65, 0x00}), func(context.Context) error {
				if calls.Add(1) == 1 {
					return catalog.E(catalog.CodeDependencyUnavailable, "unavailable")
				}
				return nil
			})
		}()
		synctest.Wait()
		time.Sleep(time.Second - time.Nanosecond)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("component restarted before its sampled due time")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil || calls.Load() != 2 {
				t.Fatalf("completion=%v calls=%d", err, calls.Load())
			}
		default:
			t.Fatal("component did not complete at its due time")
		}
	})
}

func TestSupervisorSkipsAlreadyCanceledEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls, observations := 0, 0
	err := superviseDependency(ctx, slog.New(slog.DiscardHandler), "test", config.Defaults().Worker, bytes.NewReader(nil), func(context.Context) error { calls++; return nil }, func(bool) { observations++ })
	if !errors.Is(err, context.Canceled) || calls != 0 || observations != 0 {
		t.Fatalf("canceled entry: %v calls=%d observations=%d", err, calls, observations)
	}
}

func TestSupervisorTerminalResultAfterDependencyRetry(t *testing.T) {
	dependency := catalog.E(catalog.CodeDependencyUnavailable, "unavailable")
	terminal := errors.New("terminal failure")
	for _, test := range []struct {
		name   string
		result error
		panics bool
	}{
		{name: "success"},
		{name: "terminal failure", result: terminal},
		{name: "mixed dependency and terminal", result: errors.Join(dependency, terminal)},
		{name: "returned classified panic", result: catalog.Wrap(catalog.CodeDependencyUnavailable, "dependency", safeerr.ErrPanic)},
		{name: "recovered panic", panics: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				var states []bool
				started := time.Now()
				err := superviseDependency(t.Context(), slog.New(slog.DiscardHandler), "test", config.Defaults().Worker, bytes.NewReader(nil), func(context.Context) error {
					calls++
					if calls == 1 {
						return dependency
					}
					if calls > 2 {
						t.Error("terminal result restarted a component")
						return nil // Bound an erroneous extra attempt without canceling its caller.
					}
					if test.panics {
						panic("private-supervisor-retry-panic")
					}
					return test.result
				}, func(degraded bool) { states = append(states, degraded) })
				want := test.result
				if test.panics {
					want = safeerr.ErrPanic
				}
				if !errors.Is(err, want) || calls != 2 || time.Since(started) != 750*time.Millisecond || !slices.Equal(states, []bool{false, true, false}) {
					t.Fatalf("terminal result: %v calls=%d elapsed=%v states=%v", err, calls, time.Since(started), states)
				}
			})
		})
	}
}

func TestSupervisorPropagatesDeadlineToRunningComponent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		calls := 0
		var states []bool
		started := time.Now()
		err := superviseDependency(ctx, slog.New(slog.DiscardHandler), "test", config.Defaults().Worker, bytes.NewReader(nil), func(runContext context.Context) error {
			calls++
			if calls != 1 || runContext != ctx {
				t.Fatal("supervisor restarted work or replaced its caller context")
			}
			<-runContext.Done()
			return catalog.E(catalog.CodeDependencyUnavailable, "dependency failed during drain")
		}, func(degraded bool) { states = append(states, degraded) })
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || time.Since(started) != time.Second || !slices.Equal(states, []bool{false}) {
			t.Fatalf("deadline result: %v calls=%d elapsed=%v states=%v", err, calls, time.Since(started), states)
		}
	})
}

func TestSupervisorUsesLoadedPolicyIndependently(t *testing.T) {
	for _, name := range config.EnvironmentVariables() {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "worker.json")
	if err := os.WriteFile(path, []byte(`{"worker":{"dependencyRetryInitialDelay":"1.25s","dependencyRetryCeiling":"2.5s"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.LoadFor(path, config.RoleWorker)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		type result struct {
			waits   []time.Duration
			err     error
			longRun bool
		}
		done := make(chan result, 2)
		for i := range 2 {
			go func() {
				var waits []time.Duration
				previous := time.Now()
				calls := 0
				err := superviseDependency(t.Context(), slog.New(slog.DiscardHandler), "independent", configuration.Worker, bytes.NewReader(make([]byte, 16)), func(context.Context) error {
					calls++
					if calls > 1 {
						waits = append(waits, time.Since(previous))
					}
					if i == 1 && calls == 2 {
						time.Sleep(time.Minute)
					}
					previous = time.Now()
					if calls == 4 {
						return nil
					}
					return catalog.E(catalog.CodeDependencyUnavailable, "unavailable")
				})
				done <- result{waits: waits, err: err, longRun: i == 1}
			}()
		}
		for range 2 {
			got := <-done
			want := []time.Duration{625 * time.Millisecond, 1250 * time.Millisecond, 1250 * time.Millisecond}
			if got.longRun {
				want[1] = 625 * time.Millisecond
			}
			if got.err != nil || !slices.Equal(got.waits, want) {
				t.Fatalf("independent policy: %v %v", got.waits, got.err)
			}
		}
	})
}
