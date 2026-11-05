package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Failure injection leaves the real store's claimed row untouched so recovery
// can be checked against the normal lease and attempt rules.
type completionFailureStore struct {
	Store
	err                 error
	completes, failures int
}

// PostgreSQL retains microsecond timestamps; observations must compare the two
// retained values rather than mix one of them with the caller's nanoseconds.
type roundedFailureStore struct{ Store }

// Admit cancellation after claim but before the store decides the final state.
type cancellationBeforeFailureStore struct{ Store }

func (s cancellationBeforeFailureStore) Fail(ctx context.Context, tenant, job, owner string, now time.Time, class FailureClass, safe string, retryAt time.Time) (Job, error) {
	if _, err := s.Store.RequestCancellation(ctx, tenant, job, now); err != nil {
		return Job{}, err
	}
	return s.Store.Fail(ctx, tenant, job, owner, now, class, safe, retryAt)
}

func TestUnsupportedAttemptReportsPersistedCancellation(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	job := Job{ID: "cancel-unsupported", TenantID: "tenant", Kind: "unsupported.kind", State: StateQueued, AttemptLimit: 2, NotBefore: now, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	observer := &recordingJobObserver{}
	worker, err := NewWorker(cancellationBeforeFailureStore{store}, clock.NewFake(now), "worker", time.Minute, time.Second, time.Hour, nil, observer)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	retained, err := store.Get(t.Context(), job.TenantID, job.ID)
	if err != nil || retained.State != StateCancelled || !retained.CancellationRequested || retained.FailureClass != FailureInvalidInput || retained.LeaseOwner != "" {
		t.Fatalf("cancellation not retained: %+v, %v", retained, err)
	}
	if len(observer.results) != 1 || observer.results[0] != (AttemptResult{Outcome: "cancelled", FailureClass: FailureInvalidInput, Committed: true}) || len(observer.retries) != 0 {
		t.Fatalf("observation contradicts persisted cancellation: %+v", observer)
	}
}

func (s roundedFailureStore) Fail(ctx context.Context, tenant, job, owner string, now time.Time, class FailureClass, safe string, retryAt time.Time) (Job, error) {
	return s.Store.Fail(ctx, tenant, job, owner, now.Truncate(time.Microsecond), class, safe, retryAt.Truncate(time.Microsecond))
}

func TestWorkerRetryObservationUsesPersistedTimestamps(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 123, time.UTC)
	store := NewMemoryStore()
	job := Job{ID: "rounded-retry", TenantID: "tenant", Kind: "source.sync", State: StateQueued, AttemptLimit: 2, NotBefore: now, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	observer := &recordingJobObserver{}
	worker, err := NewWorker(roundedFailureStore{store}, clock.NewFake(now), "worker", time.Minute, time.Second, time.Hour, map[string]Handler{
		job.Kind: func(context.Context, Job, func(int) error) (any, error) {
			return nil, &HandlerError{Class: FailureRateLimited, RetryAfter: 24 * time.Hour}
		},
	}, observer)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	retained, err := store.Get(t.Context(), job.TenantID, job.ID)
	// Rounding cannot move UpdatedAt before the retained claim timestamp.
	wantDelay := 24*time.Hour - 123*time.Nanosecond
	if err != nil || retained.State != StateWaiting || retained.NotBefore.Sub(retained.UpdatedAt) != wantDelay || len(observer.retries) != 1 || observer.retries[0].delay != wantDelay {
		t.Fatalf("observation differs from stored timestamps: %+v, %v, %v", retained, err, observer.retries)
	}
}

func (s *completionFailureStore) Complete(context.Context, string, string, string, time.Time, json.RawMessage, ...audit.Record) (Job, error) {
	s.completes++
	return Job{}, s.err
}

func (s *completionFailureStore) Fail(context.Context, string, string, string, time.Time, FailureClass, string, time.Time) (Job, error) {
	s.failures++
	return Job{}, s.err
}

func TestWorkerFinalizationFailureLeavesAttemptRecoverable(t *testing.T) {
	for _, class := range []FailureClass{"", FailureInvalidInput, FailureTransientUpstream} {
		t.Run(string(class), func(t *testing.T) {
			success := class == ""
			now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
			store := NewMemoryStore()
			job := Job{ID: "job-finalization", TenantID: "tenant", Kind: "source.sync", State: StateQueued, Payload: json.RawMessage(`{}`), AttemptLimit: 3, NotBefore: now, CreatedAt: now, UpdatedAt: now}
			if _, _, err := store.Enqueue(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("attempt persistence unavailable")
			failingStore := &completionFailureStore{Store: store, err: failure}
			observer := &recordingJobObserver{}
			handlers := map[string]Handler{"source.sync": func(context.Context, Job, func(int) error) (any, error) {
				if success {
					return map[string]bool{"ok": true}, nil
				}
				return nil, &HandlerError{Class: class, Safe: "Safe source failure."}
			}}
			worker, err := NewWorker(failingStore, clock.NewFake(now), "original-worker", time.Minute, time.Second, time.Hour, handlers, observer)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RunOnce(t.Context()); !errors.Is(err, failure) {
				t.Fatalf("RunOnce = %v, want persistence failure", err)
			}
			wantCompletes := 0
			if success {
				wantCompletes = 1
			}
			if failingStore.completes != wantCompletes || failingStore.failures != 1-wantCompletes {
				t.Fatalf("unexpected finalization calls: complete=%d fail=%d", failingStore.completes, failingStore.failures)
			}
			if len(observer.started) != 1 || observer.started[0] != job.Kind || len(observer.finished) != 1 || observer.finished[0] != [2]string{job.Kind, "dependency_error"} || len(observer.retries) != 0 || observer.results[0].Committed {
				t.Fatalf("incorrect attempt observations: %#v", observer)
			}
			retained, err := store.Get(t.Context(), job.TenantID, job.ID)
			if err != nil || retained.State != StateRunning || retained.Attempt != 1 || retained.LeaseOwner != "original-worker" || retained.LeaseExpiresAt == nil || !retained.LeaseExpiresAt.Equal(now.Add(time.Minute)) || len(retained.Result) != 0 || retained.FailureClass != "" || retained.SafeError != "" {
				t.Fatalf("failed persistence changed attempt: %#v, %v", retained, err)
			}
			if _, err := store.Claim(t.Context(), "replacement-worker", now.Add(59*time.Second), time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("attempt reclaimed before lease expiry: %v", err)
			}
			recovered, err := store.Claim(t.Context(), "replacement-worker", now.Add(time.Minute), time.Minute)
			if err != nil || recovered.ID != job.ID || recovered.Attempt != 2 || recovered.LeaseOwner != "replacement-worker" {
				t.Fatalf("expired attempt not recoverable: %#v, %v", recovered, err)
			}
		})
	}
}

func TestWorkerHonorsRetryMinimumFromAttemptCompletion(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	store := NewMemoryStore()
	job := Job{ID: "job-retry-delay", TenantID: "tenant", Kind: "source.sync", State: StateQueued, Payload: json.RawMessage(`{}`), AttemptLimit: 3, NotBefore: now, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	calls := 0
	observer := &recordingJobObserver{}
	worker, err := NewWorker(store, fakeClock, "worker", time.Minute, time.Second, time.Hour, map[string]Handler{
		"source.sync": func(context.Context, Job, func(int) error) (any, error) {
			calls++
			if calls == 1 {
				fakeClock.Advance(2 * time.Second)
				return nil, &HandlerError{Class: FailureRateLimited, Safe: "Retry after the upstream limit resets.", RetryAfter: 3 * time.Minute, Cause: errors.New("private upstream response")}
			}
			return map[string]bool{"ok": true}, nil
		},
	}, observer)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.Get(t.Context(), job.TenantID, job.ID)
	if err != nil || waiting.State != StateWaiting || waiting.Attempt != 1 || waiting.NotBefore.Before(now.Add(3*time.Minute+2*time.Second)) || waiting.NotBefore.After(now.Add(3*time.Minute+7*time.Second)) || waiting.FailureClass != FailureRateLimited || waiting.SafeError != "Retry after the upstream limit resets." || waiting.LeaseOwner != "" || waiting.LeaseExpiresAt != nil {
		t.Fatalf("retry schedule or safe failure changed: %#v, %v", waiting, err)
	}
	if len(observer.retries) != 1 || observer.retries[0] != (jobRetryObservation{job.Kind, waiting.NotBefore.Sub(fakeClock.Now())}) {
		t.Fatalf("retry observation differs from persisted delay: %v", observer.retries)
	}
	fakeClock.Advance(waiting.NotBefore.Sub(fakeClock.Now()) - time.Nanosecond)
	if err := worker.RunOnce(t.Context()); err != nil || calls != 1 {
		t.Fatalf("Job ran before retry boundary: calls=%d err=%v", calls, err)
	}
	fakeClock.Advance(time.Nanosecond)
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	completed, err := store.Get(t.Context(), job.TenantID, job.ID)
	if err != nil || completed.State != StateSucceeded || len(observer.retries) != 1 || completed.Attempt != 2 || calls != 2 || completed.SafeError != "" || completed.FailureClass != "" || string(completed.Result) != `{"ok":true}` {
		t.Fatalf("retry did not complete cleanly: %#v, calls=%d err=%v", completed, calls, err)
	}
}

func TestWorkerRetryExhaustionAfterRepeatedLeaseExpiry(t *testing.T) {
	t.Parallel()
	for _, expiredClaims := range []int{19, 20} {
		t.Run(fmt.Sprintf("expired-claims-%d", expiredClaims), func(t *testing.T) {
			now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
			fakeClock := clock.NewFake(now)
			store := NewMemoryStore()
			job := Job{ID: "job-expired-retries", TenantID: "tenant", Kind: "source.sync", State: StateQueued, Payload: json.RawMessage(`{}`), AttemptLimit: 20, NotBefore: now, CreatedAt: now, UpdatedAt: now}
			if _, _, err := store.Enqueue(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			// Process loss leaves running work reclaimable at expiry, even
			// when previous claims have reached the retry limit.
			for attempt := 1; attempt <= expiredClaims; attempt++ {
				claimed, err := store.Claim(t.Context(), "lost-worker", fakeClock.Now(), time.Second)
				if err != nil || claimed.ID != job.ID || claimed.Attempt != attempt {
					t.Fatalf("claim %d = %#v, %v", attempt, claimed, err)
				}
				fakeClock.Advance(time.Second)
			}
			calls := 0
			worker, err := NewWorker(store, fakeClock, "replacement-worker", time.Minute, time.Second, time.Hour, map[string]Handler{
				"source.sync": func(_ context.Context, claimed Job, _ func(int) error) (any, error) {
					calls++
					if claimed.Attempt != expiredClaims+1 || claimed.LeaseOwner != "replacement-worker" {
						t.Errorf("replacement claim = %#v", claimed)
					}
					return nil, &HandlerError{Class: FailureTransientUpstream, Safe: "Upstream unavailable."}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			retained, err := store.Get(t.Context(), job.TenantID, job.ID)
			if err != nil || calls != 1 || retained.State != StateDeadLetter || retained.Attempt != expiredClaims+1 || retained.AttemptLimit != job.AttemptLimit || retained.FailureClass != FailureTransientUpstream || retained.SafeError != "Upstream unavailable." || retained.LeaseOwner != "" || retained.LeaseExpiresAt != nil || !retained.NotBefore.Equal(job.NotBefore) || !retained.UpdatedAt.Equal(fakeClock.Now()) {
				t.Fatalf("exhausted retry = %#v, calls=%d, err=%v", retained, calls, err)
			}
			if _, err := store.Claim(t.Context(), "later-worker", fakeClock.Now().Add(time.Hour), time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("terminal Job remained claimable: %v", err)
			}
		})
	}
}

// A hint cannot grant retry permission or bypass the durable scheduling bound.
func TestWorkerRetryHintsRespectFailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		name             string
		class            FailureClass
		hint             time.Duration
		limit            int
		state            State
		minimum, maximum time.Duration
	}{
		{"invalid input", FailureInvalidInput, time.Minute, 3, StateFailed, 0, 0},
		{"policy", FailurePolicyDenied, time.Minute, 3, StateFailed, 0, 0},
		{"authentication", FailureAuthentication, time.Minute, 3, StateFailed, 0, 0},
		{"permanent", FailurePermanentUpstream, time.Minute, 3, StateFailed, 0, 0},
		{"cancelled", FailureCancelled, time.Minute, 3, StateCancelled, 0, 0},
		{"initial only", FailureRateLimited, time.Minute, 1, StateDeadLetter, 0, 0},
		{"negative", FailureTransientUpstream, -time.Second, 3, StateWaiting, 0, 5 * time.Second},
		{"oversized", FailureTransientUpstream, 24*time.Hour + time.Nanosecond, 3, StateWaiting, 0, 5 * time.Second},
		{"maximum duration", FailureTransientUpstream, time.Duration(1<<63 - 1), 3, StateWaiting, 0, 5 * time.Second},
		{"zero", FailureRateLimited, 0, 3, StateWaiting, 0, 5 * time.Second},
		{"minimum", FailureRateLimited, time.Minute, 3, StateWaiting, time.Minute, time.Minute + 5*time.Second},
		{"near maximum", FailureTransientUpstream, 24*time.Hour - time.Nanosecond, 3, StateWaiting, 24*time.Hour - time.Nanosecond, 24 * time.Hour},
		{"maximum", FailureTransientUpstream, 24 * time.Hour, 3, StateWaiting, 24 * time.Hour, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
			fake := clock.NewFake(now)
			store := NewMemoryStore()
			job := Job{ID: "retry-policy", TenantID: "tenant", Kind: "source.sync", State: StateQueued, Payload: json.RawMessage(`{}`), AttemptLimit: tc.limit, NotBefore: now, CreatedAt: now, UpdatedAt: now}
			if _, _, err := store.Enqueue(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			observer := &recordingJobObserver{}
			worker, err := NewWorker(store, fake, "worker", time.Minute, time.Second, time.Hour, map[string]Handler{
				"source.sync": func(context.Context, Job, func(int) error) (any, error) {
					fake.Advance(2 * time.Second)
					return nil, &HandlerError{Class: tc.class, Safe: "Safe upstream failure.", RetryAfter: tc.hint, Cause: errors.New("private upstream detail")}
				},
			}, observer)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, err := store.Get(t.Context(), job.TenantID, job.ID)
			if err != nil || got.State != tc.state || got.Attempt != 1 || got.FailureClass != tc.class || got.SafeError != "Safe upstream failure." || got.LeaseOwner != "" || got.LeaseExpiresAt != nil || !got.UpdatedAt.Equal(fake.Now()) {
				t.Fatalf("failure disposition: %+v, %v", got, err)
			}
			if len(observer.results) != 1 || observer.results[0] != (AttemptResult{Outcome: string(tc.state), FailureClass: tc.class, Committed: true}) {
				t.Fatalf("attempt result: %+v", observer.results)
			}
			if tc.state == StateWaiting {
				if len(observer.retries) != 1 || observer.retries[0] != (jobRetryObservation{job.Kind, got.NotBefore.Sub(fake.Now())}) {
					t.Fatalf("persisted retry observation: %v", observer.retries)
				}
				delay := got.NotBefore.Sub(fake.Now())
				if delay < tc.minimum || delay > tc.maximum {
					t.Fatalf("retry delay %s outside [%s,%s]", delay, tc.minimum, tc.maximum)
				}
			} else if !got.NotBefore.Equal(now) || len(observer.retries) != 0 {
				t.Fatalf("terminal Job acquired a retry schedule: %+v", got)
			}
		})
	}
}

func TestWorkerCancellationRequestOverridesRetryHint(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	job := Job{ID: "cancelled-hint", TenantID: "tenant", Kind: "source.sync", State: StateQueued, AttemptLimit: 7, NotBefore: now, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	observer := &recordingJobObserver{}
	worker, err := NewWorker(store, clock.NewFake(now), "worker", time.Minute, time.Second, time.Hour, map[string]Handler{
		"source.sync": func(ctx context.Context, claimed Job, progress func(int) error) (any, error) {
			if _, err := store.RequestCancellation(ctx, claimed.TenantID, claimed.ID, now); err != nil {
				t.Fatal(err)
			}
			if err := progress(10); !errors.Is(err, context.Canceled) || !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("worker did not acknowledge requested cancellation: %v", err)
			}
			// An upstream failure can arrive after cancellation was acknowledged.
			return nil, fmt.Errorf("handler wrapper: %w", &HandlerError{
				Class: FailureTransientUpstream, Safe: "Temporary upstream failure.", RetryAfter: time.Hour,
				Cause: ctx.Err(),
			})
		},
	}, observer)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	retained, err := store.Get(t.Context(), job.TenantID, job.ID)
	if err != nil || retained.State != StateCancelled || len(observer.retries) != 0 || retained.Attempt != 1 || retained.FailureClass != FailureCancelled || retained.SafeError != "Job cancellation was acknowledged." || !retained.NotBefore.Equal(now) || retained.LeaseOwner != "" || retained.LeaseExpiresAt != nil {
		t.Fatalf("requested cancellation acquired a retry: %#v %v", retained, err)
	}
	if _, err := store.Claim(t.Context(), "replacement", now.Add(24*time.Hour), time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cancelled Job remained claimable: %v", err)
	}
}

// A cancelled database query can return a classified dependency error without
// wrapping context.Canceled. Stopping owned heartbeats must still allow fenced
// completion, while preserving fatal errors and the final lease check.
func TestWorkerCompletionJoinsCancelledHeartbeat(t *testing.T) {
	for _, mode := range []string{"context cancellation", "driver cancellation", "returned panic", "raised panic", "expired lease"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				serviceClock := clock.NewFake(now)
				memory := NewMemoryStore()
				job := Job{ID: "joined-heartbeat", TenantID: "tenant", Kind: "source.sync", State: StateQueued, AttemptLimit: 3, NotBefore: now, CreatedAt: now, UpdatedAt: now}
				if _, _, err := memory.Enqueue(t.Context(), job); err != nil {
					t.Fatal(err)
				}
				entered := make(chan struct{})
				joined := false
				store := &panicWorkerStore{Store: memory, heartbeat: func(ctx context.Context) error {
					close(entered)
					<-ctx.Done()
					joined = true
					switch mode {
					case "returned panic":
						return safeerr.ErrPanic
					case "raised panic":
						panic("private heartbeat shutdown panic")
					case "expired lease":
						serviceClock.Advance(4 * time.Second)
					case "context cancellation":
						return ctx.Err()
					}
					return catalog.E(catalog.CodeDependencyUnavailable, "cancelled heartbeat query")
				}}
				observer := &recordingJobObserver{}
				worker, err := NewWorker(store, serviceClock, "worker", 3*time.Second, time.Second, time.Hour, map[string]Handler{job.Kind: func(context.Context, Job, func(int) error) (any, error) {
					<-entered
					return map[string]bool{"ok": true}, nil
				}}, observer)
				if err != nil {
					t.Fatal(err)
				}
				err = worker.RunOnce(t.Context())
				if !joined {
					t.Fatal("completion did not join its active heartbeat")
				}
				retained, readErr := memory.Get(t.Context(), job.TenantID, job.ID)
				if readErr != nil || len(observer.results) != 1 || len(observer.retries) != 0 || store.failures.Load() != 0 {
					t.Fatalf("completion lost state or scheduled a retry: %v", readErr)
				}
				switch mode {
				case "returned panic", "raised panic":
					if !errors.Is(err, safeerr.ErrPanic) || retained.State != StateRunning || store.completes.Load() != 0 || observer.results[0].Committed {
						t.Fatal("heartbeat panic was hidden or finalized")
					}
				case "expired lease":
					if catalog.ErrorCodeOf(err) != catalog.CodeConflict || retained.State != StateRunning || len(retained.Result) != 0 || observer.results[0].Committed {
						t.Fatal("stopped heartbeat bypassed the final lease fence")
					}
				default:
					if err != nil || retained.State != StateSucceeded || string(retained.Result) != `{"ok":true}` || store.completes.Load() != 1 || observer.results[0] != (AttemptResult{Outcome: "succeeded", Committed: true}) {
						t.Fatalf("owned heartbeat cancellation prevented fenced completion: %v, %+v", err, retained)
					}
				}
			})
		})
	}
}
