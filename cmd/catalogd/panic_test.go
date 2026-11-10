package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// TestComponentResultPreservesCompletionOutcome binds the component boundary's
// safe stage and one-completion contract without starting service dependencies.
func TestComponentResultPreservesCompletionOutcome(t *testing.T) {
	const private = "PRIVATE_COMPONENT_FAILURE"
	cause := errors.New(private)
	staged := safeerr.At("startup", safeerr.Wrap(cause, "component startup refused"))
	for _, test := range []struct {
		name, stage, message string
		returned, want       error
		panics               bool
	}{
		{name: "successful completion"},
		{name: "ordinary failure", returned: cause, want: cause, stage: "component", message: "operation failed; check configuration and dependency availability"},
		{name: "specific existing stage", returned: staged, want: staged, stage: "startup", message: "component startup refused"},
		{name: "recovered panic", panics: true, want: safeerr.ErrPanic, stage: "panic", message: "application panic; process must stop"},
	} {
		t.Run(test.name, func(t *testing.T) {
			results := make(chan error, 2)
			calls := 0
			componentResult(results, func() error {
				calls++
				if test.panics {
					panic(private)
				}
				return test.returned
			})
			if calls != 1 || len(results) != 1 {
				t.Fatalf("callback calls=%d completions=%d, want exactly one of each", calls, len(results))
			}
			err := <-results
			if !errors.Is(err, test.want) || len(results) != 0 {
				t.Fatal("component completion lost its cause or left another completion")
			}
			if test.want == nil {
				return
			}
			message := safeerr.Message(err)
			if safeerr.Stage(err) != test.stage || message != test.message || strings.Contains(message, private) {
				t.Fatalf("component stage=%q message=%q", safeerr.Stage(err), message)
			}
			if !test.panics && !errors.Is(err, cause) {
				t.Fatal("component completion discarded its retained internal cause")
			}
		})
	}
}

func TestSupervisorDoesNotRestartPanickedDependency(t *testing.T) {
	calls := 0
	err := superviseDependency(t.Context(), slog.New(slog.DiscardHandler), "worker", config.Defaults().Worker, strings.NewReader(""), func(context.Context) error { calls++; panic("private-worker-panic") })
	if !errors.Is(err, safeerr.ErrPanic) || calls != 1 {
		t.Fatalf("panic restarted: %v calls=%d", err, calls)
	}
	// Even an erroneous dependency wrapper must not turn the panic into a retry.
	if retryableDependencyFailure(catalog.Wrap(catalog.CodeDependencyUnavailable, "dependency", err)) {
		t.Fatal("panic considered retryable")
	}
}

func TestPanickedComponentDrainsSiblings(t *testing.T) {
	for _, name := range []string{"cooperative sibling", "stalled sibling"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				logger := slog.New(slog.DiscardHandler)
				results := make(chan error, 2)
				operations := make(chan error, 1)
				started, release, siblingDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				drainDone, drainFinished := make(chan error, 1), make(chan struct{})
				stalled, stopped, drainStarted := name == "stalled sibling", false, false
				defer func() {
					cancel()
					close(release)
					<-siblingDone
					if drainStarted {
						<-drainFinished
					}
				}()
				go func() {
					defer close(siblingDone)
					componentResult(results, func() error {
						close(started)
						if stalled {
							<-release
						} else {
							<-ctx.Done()
						}
						stopped = true
						return nil
					})
				}()
				go componentResult(results, func() error { <-started; panic("private-listener-panic") })
				remaining, operationsExited, err := waitForComponentExit(ctx, cancel, logger, results, operations, 2)
				if remaining != 1 || operationsExited || !errors.Is(err, safeerr.ErrPanic) || ctx.Err() == nil {
					t.Fatalf("supervisor: %d %v %v", remaining, operationsExited, err)
				}
				// The fatal result is already consumed. The canceled process
				// context cannot replace its independent shutdown budget.
				shutdown, cancelShutdown := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancelShutdown()
				drainStarted = true
				go func() {
					defer close(drainFinished)
					drainDone <- drainComponents(shutdown, results, remaining, err)
				}()
				synctest.Wait()
				if stalled {
					select {
					case early := <-drainDone:
						t.Fatalf("drain returned before the stalled sibling completed or its shutdown budget expired: %v", early)
					default:
					}
					time.Sleep(5*time.Second - time.Nanosecond)
					synctest.Wait()
					select {
					case early := <-drainDone:
						t.Fatalf("drain replaced the remaining shutdown budget with an earlier deadline: %v", early)
					default:
					}
					time.Sleep(time.Nanosecond)
					synctest.Wait()
				}
				select {
				case err = <-drainDone:
				default:
					t.Fatal("drain did not finish after sibling completion or its shutdown deadline")
				}
				if !errors.Is(err, safeerr.ErrPanic) || stopped == stalled || errors.Is(err, context.DeadlineExceeded) != stalled {
					t.Fatalf("drain hid panic, missed sibling or lost deadline: stopped=%t error=%v", stopped, err)
				}
			})
		})
	}
}

func TestCancellationDoesNotHideReturnedPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := superviseDependency(ctx, slog.New(slog.DiscardHandler), "worker", config.Defaults().Worker, strings.NewReader(""), func(context.Context) error { cancel(); return errors.Join(context.Canceled, safeerr.ErrPanic) })
	if !errors.Is(err, safeerr.ErrPanic) {
		t.Fatalf("cancellation hid panic: %v", err)
	}
}
