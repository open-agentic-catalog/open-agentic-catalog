package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

type panicWorkerStore struct {
	Store
	heartbeat           func(context.Context) error
	complete            bool
	completes, failures atomic.Int32
}

func (s *panicWorkerStore) Heartbeat(ctx context.Context, tenant, id, owner string, now time.Time, lease time.Duration, progress int) (Job, error) {
	if s.heartbeat != nil {
		if err := s.heartbeat(ctx); err != nil {
			return Job{}, err
		}
	}
	return s.Store.Heartbeat(ctx, tenant, id, owner, now, lease, progress)
}
func (s *panicWorkerStore) Complete(ctx context.Context, tenant, id, owner string, now time.Time, raw json.RawMessage, evidence ...audit.Record) (Job, error) {
	s.completes.Add(1)
	if s.complete {
		panic("private-persistence-panic")
	}
	return s.Store.Complete(ctx, tenant, id, owner, now, raw, evidence...)
}
func (s *panicWorkerStore) Fail(ctx context.Context, tenant, id, owner string, now time.Time, class FailureClass, message string, retry time.Time) (Job, error) {
	s.failures.Add(1)
	return s.Store.Fail(ctx, tenant, id, owner, now, class, message, retry)
}

type panicJobTrace struct{ outcomes *[]string }

func (p panicJobTrace) Start(ctx context.Context, _ string, _ catalog.TraceKind, _ catalog.TraceContext) (context.Context, func(string)) {
	return ctx, func(outcome string) { *p.outcomes = append(*p.outcomes, outcome) }
}

func TestJobPanicStopsHeartbeatWithoutFinalizingLease(t *testing.T) {
	for _, mode := range []string{"handler with active heartbeat", "heartbeat", "progress", "completion", "returned panic"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				memory := NewMemoryStore()
				job := Job{ID: "panic-job", TenantID: "tenant", Kind: "source.sync", State: StateQueued, AttemptLimit: 3, NotBefore: now, CreatedAt: now, UpdatedAt: now}
				if _, _, err := memory.Enqueue(t.Context(), job); err != nil {
					t.Fatal(err)
				}
				store := &panicWorkerStore{Store: memory, complete: mode == "completion"}
				entered := make(chan struct{})
				var heartbeatStopped atomic.Bool
				if mode == "handler with active heartbeat" {
					store.heartbeat = func(ctx context.Context) error {
						close(entered)
						<-ctx.Done()
						heartbeatStopped.Store(true)
						return ctx.Err()
					}
				} else if mode == "heartbeat" || mode == "progress" {
					store.heartbeat = func(context.Context) error { panic("private-heartbeat-panic") }
				}
				observer := &recordingJobObserver{}
				var handlerContextError func() error
				worker, err := NewWorker(store, clock.NewFake(now), "worker", 3*time.Second, time.Second, time.Hour, map[string]Handler{job.Kind: func(ctx context.Context, _ Job, progress func(int) error) (any, error) {
					handlerContextError = ctx.Err
					switch mode {
					case "handler with active heartbeat":
						<-entered
						panic("private-handler-panic")
					case "heartbeat":
						<-ctx.Done()
						return nil, ctx.Err()
					case "progress":
						return nil, progress(10)
					case "returned panic":
						return nil, safeerr.ErrPanic
					default:
						return map[string]bool{"ok": true}, nil
					}
				}}, observer)
				if err != nil {
					t.Fatal(err)
				}
				var outcomes []string
				err = worker.RunOnce(catalog.WithTracer(t.Context(), panicJobTrace{&outcomes}))
				if !errors.Is(err, safeerr.ErrPanic) || !errors.Is(handlerContextError(), context.Canceled) {
					t.Fatalf("panic/cancellation: %v, %v", err, handlerContextError())
				}
				if mode == "handler with active heartbeat" && !heartbeatStopped.Load() {
					t.Fatal("heartbeat not joined")
				}
				expectedCompletes := int32(0)
				if mode == "completion" {
					expectedCompletes = 1
				}
				if store.failures.Load() != 0 || store.completes.Load() != expectedCompletes || len(observer.retries) != 0 {
					t.Fatal("panic wrote false final state or scheduled retry")
				}
				retained, err := memory.Get(t.Context(), job.TenantID, job.ID)
				if err != nil || retained.State != StateRunning || retained.Attempt != 1 {
					t.Fatalf("lease lost: %+v %v", retained, err)
				}
				if len(observer.finished) != 1 || observer.results[0].Committed || len(outcomes) != 1 || outcomes[0] == "succeeded" || outcomes[0] == "cancelled" || outcomes[0] == "waiting" {
					t.Fatalf("false attempt outcome: %v %v", outcomes, observer.finished)
				}
				reclaimed, err := memory.Claim(t.Context(), "replacement", now.Add(4*time.Second), 3*time.Second)
				if err != nil || reclaimed.ID != job.ID || reclaimed.Attempt != 2 {
					t.Fatalf("lease unrecoverable: %+v %v", reclaimed, err)
				}
			})
		})
	}
}

func TestClaimPanicCancelsAndJoinsSiblingLoops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls, stopped atomic.Int32
		all := make(chan struct{})
		store := claimTestStore{claim: func(ctx context.Context, _ string, _ time.Time, _ time.Duration) (Job, error) {
			n := calls.Add(1)
			if n == 3 {
				close(all)
			}
			if n == 1 {
				<-all
				panic("private-claim-panic")
			}
			<-ctx.Done()
			stopped.Add(1)
			return Job{}, ctx.Err()
		}}
		worker, err := NewWorker(store, clock.NewFake(time.Now()), "worker", time.Minute, time.Second, time.Hour, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = worker.RunConcurrent(t.Context(), 3); !errors.Is(err, safeerr.ErrPanic) || stopped.Load() != 2 || calls.Load() != 3 {
			t.Fatalf("sibling cleanup: %v, stopped=%d calls=%d", err, stopped.Load(), calls.Load())
		}
	})
}

func TestConcurrentPanicDominatesEarlierDependencyFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		both := make(chan struct{})
		store := claimTestStore{claim: func(ctx context.Context, _ string, _ time.Time, _ time.Duration) (Job, error) {
			if calls.Add(1) == 1 {
				<-both
				return Job{}, catalog.E(catalog.CodeDependencyUnavailable, "claim failed")
			}
			close(both)
			<-ctx.Done()
			panic("private-cancellation-panic")
		}}
		worker, err := NewWorker(store, clock.NewFake(time.Now()), "worker", time.Minute, time.Second, time.Hour, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = worker.RunConcurrent(t.Context(), 2)
		if !errors.Is(err, safeerr.ErrPanic) || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
			t.Fatalf("first failure hid sibling panic: %v", err)
		}
	})
}
