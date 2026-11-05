package housekeeping

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type recordingStore struct {
	mu          sync.Mutex
	calls       []Class
	failAt      Class
	failures    map[Class]error
	cancel      context.CancelFunc
	cancelAt    Class
	cancelAfter int
}

func (s *recordingStore) run(class Class, limit int) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit != BatchLimit {
		return Result{}, errors.New("unexpected batch limit")
	}
	s.calls = append(s.calls, class)
	if class == s.cancelAt && s.cancel != nil && (s.cancelAfter == 0 || len(s.calls) == s.cancelAfter) {
		s.cancel()
	}
	result := Result{Reclaimed: int64(len(s.calls)), Marked: 1, Saturated: class == ClassTerminalJob, OldestEligibleAge: time.Minute}
	if class == s.failAt {
		return result, errors.New("dependency unavailable")
	}
	if err := s.failures[class]; err != nil {
		return result, err
	}
	return result, nil
}

func (s *recordingStore) recordedCalls() []Class {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Class(nil), s.calls...)
}

func (s *recordingStore) setFailure(class Class, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures == nil {
		s.failures = make(map[Class]error)
	}
	s.failures[class] = err
}

func (s *recordingStore) ReclaimExpiredIdempotency(_ context.Context, limit int) (Result, error) {
	return s.run(ClassIdempotency, limit)
}
func (s *recordingStore) ReclaimExpiredSourcePreviews(_ context.Context, limit int) (Result, error) {
	return s.run(ClassSourcePreview, limit)
}
func (s *recordingStore) ReclaimUnusedSourceInspections(_ context.Context, limit int) (Result, error) {
	return s.run(ClassSourceInspection, limit)
}
func (s *recordingStore) ReclaimObsoleteSourceCoordination(_ context.Context, limit int) (Result, error) {
	return s.run(ClassSourceState, limit)
}
func (s *recordingStore) ReclaimTerminalJobs(_ context.Context, retention time.Duration, limit int) (Result, error) {
	if retention != 7*24*time.Hour {
		return Result{}, errors.New("unexpected Job retention")
	}
	return s.run(ClassTerminalJob, limit)
}
func (s *recordingStore) ReclaimAcknowledgedOutbox(_ context.Context, retention time.Duration, limit int) (Result, error) {
	if retention != 24*time.Hour {
		return Result{}, errors.New("unexpected outbox retention")
	}
	return s.run(ClassOutbox, limit)
}

func (s *recordingStore) ReclaimWebhookDeliveries(_ context.Context, retention time.Duration, limit int) (Result, error) {
	if retention != 24*time.Hour {
		return Result{}, errors.New("unexpected delivery retention")
	}
	return s.run(ClassWebhookDelivery, limit)
}

type observedBatch struct {
	class   Class
	outcome string
	result  Result
}

type recordingObserver struct {
	batches      []observedBatch
	cycleOutcome string
}

func (o *recordingObserver) ObserveHousekeepingBatch(class Class, outcome string, result Result, _ time.Duration) {
	o.batches = append(o.batches, observedBatch{class: class, outcome: outcome, result: result})
}

func (o *recordingObserver) ObserveHousekeepingCycle(outcome string, _ time.Duration) {
	o.cycleOutcome = outcome
}

func TestRunOnceOffersEveryClassDespiteFailure(t *testing.T) {
	store := &recordingStore{failAt: ClassSourcePreview}
	observer := &recordingObserver{}
	runner, err := New(store, time.Minute, 7*24*time.Hour, 24*time.Hour, observer)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("failed class produced a successful cycle")
	}
	want := []Class{ClassIdempotency, ClassSourcePreview, ClassSourceInspection, ClassSourceState, ClassTerminalJob, ClassWebhookDelivery, ClassOutbox}
	if !reflect.DeepEqual(store.calls, want) {
		t.Fatalf("class order = %v, want %v", store.calls, want)
	}
	if observer.cycleOutcome != "failed" || len(observer.batches) != len(want) || observer.batches[1].outcome != "failed" || !observer.batches[4].result.Saturated {
		t.Fatalf("observations = %#v cycle=%q", observer.batches, observer.cycleOutcome)
	}
}

func TestRunnerValidationAndCancellation(t *testing.T) {
	store := &recordingStore{}
	for _, test := range []struct {
		name      string
		store     Store
		interval  time.Duration
		jobs      time.Duration
		outbox    time.Duration
		observers []Observer
	}{
		{name: "store", interval: time.Minute, jobs: time.Hour, outbox: time.Hour},
		{name: "interval", store: store, jobs: time.Hour, outbox: time.Hour},
		{name: "job retention", store: store, interval: time.Minute, outbox: time.Hour},
		{name: "outbox retention", store: store, interval: time.Minute, jobs: time.Hour},
		{name: "nil observer", store: store, interval: time.Minute, jobs: time.Hour, outbox: time.Hour, observers: []Observer{nil}},
		{name: "many observers", store: store, interval: time.Minute, jobs: time.Hour, outbox: time.Hour, observers: []Observer{&recordingObserver{}, &recordingObserver{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.store, test.interval, test.jobs, test.outbox, test.observers...); err == nil {
				t.Fatal("invalid runner configuration was accepted")
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (Noop{}).Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("no-op cancellation = %v", err)
	}
	runner, err := New(store, time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("runner cancellation = %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	cancelingStore := &recordingStore{cancel: cancel, cancelAt: ClassIdempotency}
	runner, err = New(cancelingStore, time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-cycle cancellation = %v", err)
	}
	if want := []Class{ClassIdempotency}; !reflect.DeepEqual(cancelingStore.calls, want) {
		t.Fatalf("classes after cancellation = %v, want %v", cancelingStore.calls, want)
	}
}

// Start each runner inside its test's synctest bubble. Cleanup joins even after
// an assertion fails; a missing tick or cancellation must not hang the suite.
func startTestRunner(t *testing.T, store *recordingStore, interval time.Duration) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store.cancel = cancel
	runner, err := New(store, interval, 7*24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			t.Logf("runner cleanup completed: %v", err)
		case <-time.After(2 * interval):
			t.Error("runner cleanup did not finish within two intervals")
		}
	})
	go func() {
		defer close(done)
		done <- runner.Run(ctx)
	}()
	return cancel, done
}

func completedRunner(t *testing.T, done <-chan error, stage string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Nanosecond):
		t.Fatalf("%s did not finish within one virtual nanosecond", stage)
		return nil
	}
}

func requireWaitingRunner(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("runner returned before its scheduled cycle: %v", err)
	default:
	}
}

func TestRunnerWaitsForCadenceAfterImmediateCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = time.Second
		store := &recordingStore{cancelAt: ClassOutbox, cancelAfter: 14}
		_, done := startTestRunner(t, store, interval)
		synctest.Wait()
		oneCycle := []Class{ClassIdempotency, ClassSourcePreview, ClassSourceInspection, ClassSourceState, ClassTerminalJob, ClassWebhookDelivery, ClassOutbox}
		if !reflect.DeepEqual(store.recordedCalls(), oneCycle) {
			t.Fatalf("immediate cycle = %v, want %v", store.recordedCalls(), oneCycle)
		}
		requireWaitingRunner(t, done)
		time.Sleep(interval - time.Nanosecond)
		synctest.Wait()
		if !reflect.DeepEqual(store.recordedCalls(), oneCycle) {
			t.Fatalf("cleanup ran before cadence: %v", store.recordedCalls())
		}
		requireWaitingRunner(t, done)
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if err := completedRunner(t, done, "scheduled cycle"); !errors.Is(err, context.Canceled) {
			t.Fatalf("runner cadence cancellation = %v", err)
		}
		want := append(append([]Class(nil), oneCycle...), oneCycle...)
		if !reflect.DeepEqual(store.recordedCalls(), want) {
			t.Fatalf("scheduled class order = %v, want %v", store.recordedCalls(), want)
		}
	})
}

func TestRunnerReturnsScheduledCycleFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = time.Second
		store := &recordingStore{}
		_, done := startTestRunner(t, store, interval)
		synctest.Wait()
		oneCycle := []Class{ClassIdempotency, ClassSourcePreview, ClassSourceInspection, ClassSourceState, ClassTerminalJob, ClassWebhookDelivery, ClassOutbox}
		if !reflect.DeepEqual(store.recordedCalls(), oneCycle) {
			t.Fatalf("immediate cycle = %v, want %v", store.recordedCalls(), oneCycle)
		}
		requireWaitingRunner(t, done)
		failure := errors.New("dependency unavailable")
		store.setFailure(ClassSourcePreview, failure)
		time.Sleep(interval - time.Nanosecond)
		synctest.Wait()
		if !reflect.DeepEqual(store.recordedCalls(), oneCycle) {
			t.Fatalf("failed cycle ran before cadence: %v", store.recordedCalls())
		}
		requireWaitingRunner(t, done)
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if err := completedRunner(t, done, "scheduled failure"); !errors.Is(err, failure) || err.Error() != failure.Error() {
			t.Fatalf("scheduled cycle lost error identity: %v", err)
		}
		want := append(append([]Class(nil), oneCycle...), oneCycle...)
		if !reflect.DeepEqual(store.recordedCalls(), want) {
			t.Fatalf("scheduled failure skipped or reordered classes: %v, want %v", store.recordedCalls(), want)
		}
	})
}

func TestRunnerCancelsWhileIdleWithoutMoreWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = time.Second
		store := &recordingStore{}
		cancel, done := startTestRunner(t, store, interval)
		synctest.Wait()
		oneCycle := []Class{ClassIdempotency, ClassSourcePreview, ClassSourceInspection, ClassSourceState, ClassTerminalJob, ClassWebhookDelivery, ClassOutbox}
		if !reflect.DeepEqual(store.recordedCalls(), oneCycle) {
			t.Fatalf("immediate cycle = %v, want %v", store.recordedCalls(), oneCycle)
		}
		time.Sleep(interval / 2)
		synctest.Wait()
		requireWaitingRunner(t, done)
		cancel()
		synctest.Wait()
		if err := completedRunner(t, done, "idle cancellation"); !errors.Is(err, context.Canceled) {
			t.Fatalf("idle cancellation = %v", err)
		}
		time.Sleep(2 * interval)
		synctest.Wait()
		if !reflect.DeepEqual(store.recordedCalls(), oneCycle) {
			t.Fatalf("cleanup continued after idle cancellation: %v", store.recordedCalls())
		}
	})
}

func TestRunOnceDoesNotHidePermanentFailureBehindDependencyOutage(t *testing.T) {
	store := &recordingStore{failures: map[Class]error{
		ClassIdempotency:   catalog.E(catalog.CodeDependencyUnavailable, "database unavailable"),
		ClassSourcePreview: errors.New("invalid cleanup invariant"),
	}}
	runner, err := New(store, time.Minute, 7*24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	err = runner.RunOnce(t.Context())
	if !strings.Contains(err.Error(), "database unavailable") || !strings.Contains(err.Error(), "invalid cleanup invariant") {
		t.Fatalf("mixed cycle failure = %v", err)
	}
}
