// Coordination tests isolate first-exit and operations-drain decisions from
// process signals, listeners and running service dependencies.
package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// A preceding shutdown phase can use the shared budget while other components
// have already buffered their results. Expiry must not discard those results.
func TestDrainQueuedAtDeadline(t *testing.T) {
	initial := errors.New("private initiating cause")
	later := errors.New("private component failure")
	extra := errors.New("unexpected completion")
	for _, tc := range []struct {
		name          string
		operations    bool
		expected      int
		queued        []error
		initial       error
		shutdownError error
		wantDeadline  bool
		wantSuccess   bool
	}{
		{name: "components finished", expected: 2, queued: []error{nil, nil}, wantSuccess: true},
		{name: "completed component failed", expected: 2, queued: []error{nil, later}},
		{name: "component failures and outstanding sibling", expected: 3, queued: []error{later, safeerr.ErrPanic}, initial: initial, wantDeadline: true},
		{name: "listener finished", operations: true, expected: 1, queued: []error{nil}, wantSuccess: true},
		{name: "listener panicked and shutdown timed out", operations: true, expected: 1, queued: []error{safeerr.ErrPanic}, initial: initial, shutdownError: context.DeadlineExceeded, wantDeadline: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// Repeat the case because select can choose either ready input.
				for range 32 {
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					results := make(chan error, len(tc.queued)+1)
					for _, err := range tc.queued {
						results <- err
					}
					allQueued := len(tc.queued) == tc.expected
					if allQueued {
						results <- extra
					}
					<-ctx.Done()
					var err error
					if tc.operations {
						err = drainOperations(ctx, func(context.Context) error { return tc.shutdownError }, results, false, tc.initial)
					} else {
						err = drainComponents(ctx, results, tc.expected, tc.initial)
					}
					cancel()
					if allQueued {
						if len(results) != 1 || !errors.Is(<-results, extra) {
							t.Fatal("drain did not consume exactly the expected completions")
						}
					} else if len(results) != 0 {
						t.Fatal("drain left an available completion unread")
					}
					if (err == nil) != tc.wantSuccess || errors.Is(err, context.DeadlineExceeded) != tc.wantDeadline {
						t.Fatalf("unexpected shutdown outcome: %v", err)
					}
					if tc.initial != nil && !errors.Is(err, tc.initial) {
						t.Fatalf("lost initiating failure: %v", err)
					}
					for _, want := range tc.queued {
						if want != nil && !errors.Is(err, want) {
							t.Fatalf("drain lost %v in %v", want, err)
						}
					}
					if tc.shutdownError != nil && (!strings.Contains(err.Error(), "shut down worker operations server:") || strings.Contains(err.Error(), "drain worker operations server:")) {
						t.Fatalf("unexpected deadline for completed listener: %v", err)
					}
					if !slices.Contains(tc.queued, safeerr.ErrPanic) {
						continue
					}
					var log bytes.Buffer
					diagnostics.Report(&log, "catalogd", "INTERNAL", err, true)
					if strings.Count(log.String(), "\n") != 1 || !strings.Contains(log.String(), "error_code=INTERNAL_PANIC") || strings.Contains(log.String(), "private") {
						t.Fatalf("unexpected terminal diagnostic: %s", log.String())
					}
				}
			})
		})
	}
}

// TestWaitForComponentExit preserves the trigger and counts only consumed work.
func TestWaitForComponentExit(t *testing.T) {
	t.Parallel()
	failure := errors.New("component failed")
	extra := errors.New("later completion")
	for _, tc := range []struct {
		name, source   string
		count, remains int
		failure        error
	}{
		{name: "caller cancellation", source: "context", count: 5, remains: 5},
		{name: "API completes", source: "component", count: 1},
		{name: "worker fails", source: "component", count: 4, remains: 3, failure: failure},
		{name: "combined semantic role completes", source: "component", count: 6, remains: 5},
		{name: "operations completes", source: "operations", count: 4, remains: 4},
		{name: "operations fails", source: "operations", count: 6, remains: 6, failure: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var components, operations chan error
			queued := make(chan error, 2)
			switch tc.source {
			case "context":
				cancel()
			case "component":
				components = queued
			case "operations":
				operations = queued
			}
			queued <- tc.failure
			queued <- extra
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			stops := 0
			stop := func() { stops++; cancel() }
			remaining, operationsExited, err := waitForComponentExit(ctx, stop, logger, components, operations, tc.count)
			if remaining != tc.remains || operationsExited != (tc.source == "operations") {
				t.Fatalf("remaining=%d operationsExited=%t", remaining, operationsExited)
			}
			if !errors.Is(err, tc.failure) || errors.Is(err, extra) {
				t.Fatalf("exit error=%v, want %v", err, tc.failure)
			}
			if tc.source == "operations" && tc.failure != nil && err.Error() != "worker operations server: component failed" {
				t.Fatalf("operations error lost its context: %v", err)
			}
			if tc.source == "context" {
				if stops != 0 || len(queued) != 2 || !strings.Contains(logs.String(), "catalog service draining") {
					t.Fatal("caller cancellation stopped or consumed a component, or lost its drain diagnostic")
				}
			} else if stops != 1 || !errors.Is(ctx.Err(), context.Canceled) || len(queued) != 1 || !errors.Is(<-queued, extra) || logs.Len() != 0 {
				t.Fatal("component exit did not cancel exactly once and retain later completions")
			}
		})
	}
}

// TestWaitForComponentExitBlocks waits for a real completion without polling.
func TestWaitForComponentExitBlocks(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"operations completion", "caller cancellation"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				components, operations := make(chan error, 1), make(chan error, 1)
				type completion struct {
					remaining int
					exited    bool
					err       error
				}
				done, finished := make(chan completion, 1), make(chan struct{})
				var logs bytes.Buffer
				logger := slog.New(slog.NewTextHandler(&logs, nil))
				stops := 0
				stop := func() { stops++; cancel() }
				defer func() {
					cancel()
					synctest.Wait()
					select {
					case <-finished:
						return
					default:
					}
					// Only a failed completion assertion reaches this fallback.
					// Release a broken waiter without turning its failure into a pass.
					for _, results := range []chan error{components, operations} {
						select {
						case results <- nil:
						default:
						}
					}
					<-finished
				}()
				go func() {
					defer close(finished)
					remaining, exited, err := waitForComponentExit(ctx, stop, logger, components, operations, 4)
					done <- completion{remaining: remaining, exited: exited, err: err}
				}()
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("returned before cancellation or completion")
				default:
				}
				if name == "caller cancellation" {
					cancel() // The caller, rather than stop, supplies this trigger.
				} else {
					operations <- nil
				}
				synctest.Wait()
				var result completion
				select {
				case result = <-done:
				default:
					t.Fatal("blocked waiter did not wake for its cancellation or completion")
				}
				wantOperations := name == "operations completion"
				if result.remaining != 4 || result.exited != wantOperations || result.err != nil {
					t.Fatalf("completion = %d/%t/%v", result.remaining, result.exited, result.err)
				}
				if !errors.Is(ctx.Err(), context.Canceled) || len(components) != 0 || len(operations) != 0 {
					t.Fatal("process context was not canceled or a completion was left unconsumed")
				}
				if wantOperations {
					if stops != 1 || logs.Len() != 0 {
						t.Fatal("operations exit did not stop exactly once without a caller-drain event")
					}
				} else if stops != 0 || strings.Count(logs.String(), "event=process.draining") != 1 || !strings.Contains(logs.String(), "component=process") || bytes.Count(logs.Bytes(), []byte{'\n'}) != 1 {
					t.Fatal("active caller cancellation consumed a completion, called stop or lost its single drain event")
				}
			})
		})
	}
}

// TestDrainOperationsRetainsFailures checks all combinations of initial,
// shutdown and listener failures, including shutdown-before-consumption ordering.
func TestDrainOperationsRetainsFailures(t *testing.T) {
	t.Parallel()
	initial := errors.New("initial failure")
	shutdownFailure := errors.New("shutdown failed")
	listenerFailure := errors.New("listener failed")
	extra := errors.New("unrelated completion")
	for mask := range 8 {
		var causes [3]error
		var messages []string
		if mask&1 != 0 {
			causes[0] = initial
			messages = append(messages, initial.Error())
		}
		if mask&2 != 0 {
			causes[1] = shutdownFailure
			messages = append(messages, "shut down worker operations server: "+shutdownFailure.Error())
		}
		if mask&4 != 0 {
			causes[2] = listenerFailure
			messages = append(messages, listenerFailure.Error())
		}
		name := strings.Join(messages, "; ")
		if name == "" {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			results := make(chan error, 2)
			results <- causes[2]
			results <- extra
			calls := 0
			err := drainOperations(ctx, func(got context.Context) error {
				calls++
				if got != ctx || len(results) != 2 {
					t.Fatal("shutdown lost the shared context or ran after consuming a completion")
				}
				return causes[1]
			}, results, false, causes[0])
			if calls != 1 || len(results) != 1 || !errors.Is(<-results, extra) || errors.Is(err, extra) {
				t.Fatal("shutdown or completion count changed")
			}
			for _, cause := range []error{initial, shutdownFailure, listenerFailure} {
				want := errors.Is(causes[0], cause) || errors.Is(causes[1], cause) || errors.Is(causes[2], cause)
				if errors.Is(err, cause) != want {
					t.Fatalf("result=%v, want cause %v present=%t", err, cause, want)
				}
			}
			want := strings.Join(messages, "\n")
			if (want == "" && err != nil) || (want != "" && (err == nil || err.Error() != want)) {
				t.Fatalf("result=%v, want %q", err, want)
			}
		})
	}
}

func TestDrainOperationsAfterListenerExit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	listenerFailure := errors.New("accept failed")
	shutdownFailure := errors.New("shutdown failed")
	calls := 0
	err := drainOperations(ctx, func(got context.Context) error {
		calls++
		if got != ctx {
			t.Fatal("shutdown replaced the shared context")
		}
		return shutdownFailure
	}, nil, true, listenerFailure)
	if calls != 1 || !errors.Is(err, shutdownFailure) || !errors.Is(err, listenerFailure) || errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown calls=%d, result=%v", calls, err)
	}
}

// TestDrainOperationsWaitsForListener retains shutdown failure while awaiting
// the listener's own completion within the original deadline.
func TestDrainOperationsWaitsForListener(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		results := make(chan error)
		done := make(chan error, 1)
		shutdownFailure := errors.New("shutdown failed")
		listenerFailure := errors.New("listener failed")
		go func() {
			done <- drainOperations(ctx, func(context.Context) error { return shutdownFailure }, results, false, nil)
		}()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("returned before listener completion: %v", err)
		default:
		}
		results <- listenerFailure
		if err := <-done; !errors.Is(err, shutdownFailure) || !errors.Is(err, listenerFailure) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("listener completion lost failures or its deadline: %v", err)
		}
	})
}

// TestDrainOperationsUsesRemainingBudget proves API/component drain time is not
// replaced with a fresh deadline for the operations listener.
func TestDrainOperationsUsesRemainingBudget(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		initial := errors.New("earlier component failed")
		shutdownFailure := errors.New("operations shutdown failed")
		started := time.Now()
		time.Sleep(3 * time.Second) // Virtual time consumed by earlier drain phases.
		calls := 0
		err := drainOperations(ctx, func(got context.Context) error {
			calls++
			if got != ctx {
				t.Fatal("operations shutdown replaced the shared context")
			}
			return shutdownFailure
		}, nil, false, initial)
		if calls != 1 || time.Since(started) != 5*time.Second || !errors.Is(err, initial) || !errors.Is(err, shutdownFailure) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "drain worker operations server:") {
			t.Fatalf("drain elapsed=%v calls=%d error=%v", time.Since(started), calls, err)
		}
	})
}

// TestDrainOperationsCancelled still attempts shutdown and retains prior errors.
func TestDrainOperationsCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	initial := errors.New("initial failure")
	calls := 0
	err := drainOperations(ctx, func(got context.Context) error {
		calls++
		return got.Err()
	}, nil, false, initial)
	if calls != 1 || !errors.Is(err, initial) || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "shut down worker operations server:") || !strings.Contains(err.Error(), "drain worker operations server:") {
		t.Fatalf("cancelled operations drain calls=%d error=%v", calls, err)
	}
}
