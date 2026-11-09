// Coordination tests isolate first-exit and operations-drain decisions from
// process signals, listeners and running service dependencies.
package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

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
			}, results, causes[0])
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
			done <- drainOperations(ctx, func(context.Context) error { return shutdownFailure }, results, nil)
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
		}, nil, initial)
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
	}, nil, initial)
	if calls != 1 || !errors.Is(err, initial) || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "shut down worker operations server:") || !strings.Contains(err.Error(), "drain worker operations server:") {
		t.Fatalf("cancelled operations drain calls=%d error=%v", calls, err)
	}
}
