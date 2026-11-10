// Retry classification tests preserve explicit domain classifications while
// preventing ordinary wrappers from hiding terminal joined failures.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestDependencyClassificationThroughWrappers(t *testing.T) {
	dependency := catalog.E(catalog.CodeDependencyUnavailable, "dependency unavailable")
	terminal := errors.New("terminal invariant failure")
	leaseConflict := catalog.E(catalog.CodeConflict, "outbox lease is not owned or has expired")
	wrap := func(err error) error { return fmt.Errorf("cycle: %w", err) }
	for _, test := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"wrapped dependency", wrap(dependency), true},
		{"wrapped terminal", wrap(terminal), false},
		{"lease conflict", leaseConflict, false},
		{"wrapped lease conflict", wrap(leaseConflict), false},
		{"wrapped mixed dependency first", wrap(errors.Join(dependency, terminal)), false},
		{"wrapped mixed terminal first", wrap(errors.Join(terminal, dependency)), false},
		{"nested mixed join", wrap(errors.Join(wrap(dependency), wrap(errors.Join(dependency, terminal)))), false},
		{"nested dependencies", wrap(errors.Join(wrap(dependency), wrap(errors.Join(dependency, dependency)))), true},
		{"multiple wrapped dependencies", fmt.Errorf("first: %w; second: %w", dependency, dependency), true},
		{"multiple wrapped mixed", fmt.Errorf("first: %w; second: %w", dependency, terminal), false},
		{"explicit internal classification", wrap(catalog.Wrap(catalog.CodeInternal, "internal failure", dependency)), false},
		{"explicit dependency classification", wrap(catalog.Wrap(catalog.CodeDependencyUnavailable, "dependency failure", terminal)), true},
		{"wrapped empty join", wrap(emptyJoinedError{}), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := retryableDependencyFailure(test.err); got != test.retry {
				t.Fatalf("retryable = %t, want %t", got, test.retry)
			}
		})
	}
}

func TestSupervisorHonorsWrappedJoinedFailures(t *testing.T) {
	dependency := catalog.E(catalog.CodeDependencyUnavailable, "dependency unavailable")
	terminal := errors.New("terminal invariant failure")
	leaseConflict := catalog.E(catalog.CodeConflict, "outbox lease is not owned or has expired")
	for _, test := range []struct {
		name  string
		cause error
		calls int
	}{
		{"dependency first", errors.Join(dependency, terminal), 1},
		{"terminal first", errors.Join(terminal, dependency), 1},
		{"dependencies only", errors.Join(dependency, dependency), 2},
		{"lease conflict", leaseConflict, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cause := fmt.Errorf("cycle: %w", test.cause)
				calls := 0
				var states []bool
				started := time.Now()
				err := superviseDependency(t.Context(), slog.New(slog.DiscardHandler), "test component", config.Defaults().Worker, bytes.NewReader(nil), func(ctx context.Context) error {
					if ctx != t.Context() {
						t.Fatal("supervisor replaced the caller context")
					}
					calls++
					if calls == 1 {
						return cause
					}
					return nil
				}, func(degraded bool) { states = append(states, degraded) })
				if calls != test.calls {
					t.Fatalf("attempts = %d, want %d", calls, test.calls)
				}
				if test.calls == 1 && !errors.Is(err, cause) {
					t.Fatalf("terminal cycle returned %v", err)
				}
				if test.calls == 2 && err != nil {
					t.Fatalf("recovered dependency returned %v", err)
				}
				wantStates := []bool{false}
				if test.calls == 2 {
					wantStates = append(wantStates, true, false)
				}
				if !slices.Equal(states, wantStates) || time.Since(started) != time.Duration(test.calls-1)*750*time.Millisecond {
					t.Fatalf("retry states=%v elapsed=%v", states, time.Since(started))
				}
			})
		})
	}
}

// TestSupervisorCancellationOverridesResult prevents a completed or failed
// callback from restarting work after its caller cancels the process context.
func TestSupervisorCancellationOverridesResult(t *testing.T) {
	for _, result := range []error{nil, errors.New("terminal failure"), catalog.E(catalog.CodeDependencyUnavailable, "dependency unavailable")} {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		var states []bool
		err := superviseDependency(ctx, slog.New(slog.DiscardHandler), "test component", config.Defaults().Worker, bytes.NewReader(nil), func(got context.Context) error {
			calls++
			if got != ctx {
				t.Fatal("supervisor replaced the caller context")
			}
			cancel()
			return result
		}, func(degraded bool) { states = append(states, degraded) })
		cancel()
		if !errors.Is(err, context.Canceled) || calls != 1 || !slices.Equal(states, []bool{false}) {
			t.Fatalf("callback result=%v: error=%v calls=%d states=%v", result, err, calls, states)
		}
	}
}
