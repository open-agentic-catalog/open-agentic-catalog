// Supervisor observations use local log handlers, channels and virtual time.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

type supervisorWarning struct {
	Level        string                  `json:"level"`
	Event        string                  `json:"event"`
	Component    observability.Component `json:"component"`
	ErrorCode    catalog.ErrorCode       `json:"error_code"`
	ErrorType    string                  `json:"error_type"`
	RetryDelayMS int64                   `json:"retry_delay_ms"`
	Suppressed   *uint64                 `json:"suppressed"`
}

// supervisorLog snapshots remain independent of writes after a later virtual
// timer advance; quiescence alone does not protect those future observations.
type supervisorLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (l *supervisorLog) Write(raw []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.Write(raw)
}

func (l *supervisorLog) snapshot() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.buffer.Bytes())
}

// supervisorWarnings groups each component's records without imposing a
// scheduling order on independent goroutines sharing one log handler.
func supervisorWarnings(t *testing.T, raw []byte) map[observability.Component][]supervisorWarning {
	t.Helper()
	grouped := make(map[observability.Component][]supervisorWarning)
	if len(raw) == 0 {
		return grouped
	}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		var record supervisorWarning
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal("supervisor warning is not a JSON record", err)
		}
		grouped[record.Component] = append(grouped[record.Component], record)
	}
	return grouped
}

// TestSupervisorWarningsAreComponentOwned binds warning-window ownership and
// safe sampled-delay metadata to the actual retry call site.
func TestSupervisorWarningsAreComponentOwned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs supervisorLog
		logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
		ctx, cancel := context.WithCancel(t.Context())
		components := []observability.Component{observability.ComponentWorker, observability.ComponentOutbox}
		calls := make([]atomic.Int32, len(components))
		results := make([]chan error, len(components))
		finished := make([]chan struct{}, len(components))
		policy := config.Defaults().Worker
		policy.DependencyRetryInitialDelay.Duration, policy.DependencyRetryCeiling.Duration = 2*time.Second, 2*time.Second
		for i := range components {
			results[i], finished[i] = make(chan error, 1), make(chan struct{})
		}
		defer func() {
			cancel()
			for _, done := range finished {
				<-done
			}
		}()
		for i, component := range components {
			go func() {
				defer close(finished[i])
				fault := catalog.Wrap(catalog.CodeDependencyUnavailable, "database unavailable", errors.New("postgres://fixture:private-supervisor-password@database/catalog"))
				// Zero entropy selects 1s from the fixed valid 2s window;
				// the private reader has enough bytes for every expected attempt.
				results[i] <- superviseDependency(ctx, logger, string(component), policy, bytes.NewReader(make([]byte, 1024)), func(context.Context) error {
					if calls[i].Add(1) > 61 {
						return nil // Bound an unintended extra attempt instead of looping forever.
					}
					return fault
				})
			}()
		}
		assertWarnings := func(suppressed []uint64) {
			t.Helper()
			raw := logs.snapshot()
			if bytes.Contains(raw, []byte("private-supervisor-password")) || bytes.Contains(raw, []byte("postgres://")) {
				t.Fatal("supervisor warning disclosed the private dependency cause")
			}
			grouped := supervisorWarnings(t, raw)
			if len(grouped) != len(components) {
				t.Fatalf("warning owners=%d, want %d", len(grouped), len(components))
			}
			for _, component := range components {
				records := grouped[component]
				if len(records) != len(suppressed) {
					t.Fatalf("component %s warning count=%d, want %d", component, len(records), len(suppressed))
				}
				for i, record := range records {
					if record.Level != "WARN" || record.Event != "dependency.unavailable" || record.ErrorCode != catalog.CodeDependencyUnavailable || record.ErrorType != "*catalog.DomainError" || record.RetryDelayMS != 1000 || record.Suppressed == nil || *record.Suppressed != suppressed[i] {
						t.Fatalf("component %s warning %d lost its classified, sampled or suppression fields: %+v", component, i, record)
					}
				}
			}
		}
		synctest.Wait()
		if calls[0].Load() != 1 || calls[1].Load() != 1 {
			t.Fatalf("first supervisor attempts=%d/%d", calls[0].Load(), calls[1].Load())
		}
		assertWarnings([]uint64{0})
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls[0].Load() != 61 || calls[1].Load() != 61 || ctx.Err() != nil {
			t.Fatalf("independent supervisor attempts=%d/%d, context=%v", calls[0].Load(), calls[1].Load(), ctx.Err())
		}
		assertWarnings([]uint64{0, 59})
		cancel()
		for _, result := range results {
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("supervisor cancellation result=%v", err)
			}
		}
	})
}

// TestSupervisorObservationAtRetryAdmission holds the second callback open to
// distinguish entering work from completion, independently of warning filtering.
func TestSupervisorObservationAtRetryAdmission(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelWarn, slog.LevelError} {
		t.Run(level.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs supervisorLog
				logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level}))
				registry := observability.New(nil)
				registry.SetComponent(observability.ComponentAPI, observability.ComponentStopped)
				registry.SetComponent(observability.ComponentWorker, observability.ComponentRunning)
				registry.SetComponent(observability.ComponentOutbox, observability.ComponentDegraded)
				ctx, cancel := context.WithCancel(t.Context())
				release, entered, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				result := make(chan error, 1)
				defer func() { cancel(); unblock(); <-finished }()
				var calls atomic.Int32
				var states []bool
				observe := componentStateObserver(registry, observability.ComponentWorker)
				policy := config.Defaults().Worker
				policy.DependencyRetryInitialDelay.Duration, policy.DependencyRetryCeiling.Duration = 2*time.Second, 2*time.Second
				go func() {
					defer close(finished)
					result <- superviseDependency(ctx, logger, string(observability.ComponentWorker), policy, bytes.NewReader(make([]byte, 16)), func(context.Context) error {
						attempt := calls.Add(1)
						if attempt == 1 {
							return catalog.E(catalog.CodeDependencyUnavailable, "database unavailable")
						}
						if attempt == 2 {
							close(entered)
							<-release
						}
						return nil
					}, func(degraded bool) { states = append(states, degraded); observe(degraded) })
				}()
				want := map[observability.Component]observability.ComponentState{
					observability.ComponentAPI: observability.ComponentStopped, observability.ComponentWorker: observability.ComponentDegraded, observability.ComponentOutbox: observability.ComponentDegraded,
				}
				synctest.Wait()
				_, degraded, components, _ := registry.HealthState()
				if calls.Load() != 1 || !degraded || !maps.Equal(components, want) {
					t.Fatalf("backoff calls=%d degraded=%t components=%v", calls.Load(), degraded, components)
				}
				time.Sleep(time.Second)
				synctest.Wait()
				select {
				case <-entered:
				default:
					t.Fatal("retry callback did not enter at its sampled deadline")
				}
				want[observability.ComponentWorker] = observability.ComponentRunning
				_, degraded, components, _ = registry.HealthState()
				if calls.Load() != 2 || ctx.Err() != nil || !degraded || !maps.Equal(components, want) {
					t.Fatalf("held retry calls=%d degraded=%t components=%v context=%v", calls.Load(), degraded, components, ctx.Err())
				}
				if level == slog.LevelError {
					if len(logs.snapshot()) != 0 {
						t.Fatal("filtered warning reached the ERROR-level handler")
					}
				} else if records := supervisorWarnings(t, logs.snapshot()); len(records) != 1 || len(records[observability.ComponentWorker]) != 1 {
					t.Fatal("retry observation lost its one component-owned warning")
				}
				unblock()
				synctest.Wait()
				select {
				case err := <-result:
					if err != nil || calls.Load() != 2 || !slices.Equal(states, []bool{false, true, false}) {
						t.Fatalf("released retry result=%v calls=%d states=%v", err, calls.Load(), states)
					}
				default:
					t.Fatal("released retry callback did not complete")
				}
			})
		})
	}
}
