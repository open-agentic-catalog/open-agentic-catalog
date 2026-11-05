package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

// Only Claim is expected on these paths. An unexpected state transition through
// the embedded Store fails the test instead of silently accepting a write.
type claimTestStore struct {
	Store
	claim func(context.Context, string, time.Time, time.Duration) (Job, error)
}

func (s claimTestStore) Claim(ctx context.Context, owner string, now time.Time, lease time.Duration) (Job, error) {
	return s.claim(ctx, owner, now, lease)
}

func TestWorkerClaimResults(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "no ready work", err: catalog.E(catalog.CodeNotFound, "no job is ready")},
		{name: "dependency failure", err: catalog.E(catalog.CodeDependencyUnavailable, "claim unavailable")},
		{name: "unexpected failure", err: errors.New("claim failed")},
		{name: "cancellation", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := test.err
			if catalog.ErrorCodeOf(test.err) == catalog.CodeNotFound {
				want = nil
			}
			calls := 0
			ctx := context.Background()
			store := claimTestStore{claim: func(got context.Context, owner string, at time.Time, lease time.Duration) (Job, error) {
				calls++
				if got != ctx || owner != "worker" || !at.Equal(now) || lease != time.Minute {
					t.Error("claim did not receive the worker context, owner, clock, and lease")
				}
				return Job{}, test.err
			}}
			worker, err := NewWorker(store, clock.NewFake(now), "worker", time.Minute, time.Hour, time.Hour, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RunOnce(ctx); !errors.Is(err, want) || calls != 1 {
				t.Fatalf("RunOnce = %v, claims = %d; want %v and one claim", err, calls, want)
			}
		})
	}
}

func TestWorkerClaimFailureCancelsSiblingLoops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const concurrency = 3
	var calls atomic.Int32
	var cancelled atomic.Int32
	allClaiming := make(chan struct{})
	failure := errors.New("claim dependency failed")
	store := claimTestStore{claim: func(ctx context.Context, _ string, _ time.Time, _ time.Duration) (Job, error) {
		call := calls.Add(1)
		if call == concurrency {
			close(allClaiming)
		}
		if call == 1 {
			select {
			case <-allClaiming:
				return Job{}, failure
			case <-ctx.Done():
				return Job{}, ctx.Err()
			}
		}
		<-ctx.Done()
		if errors.Is(ctx.Err(), context.Canceled) {
			cancelled.Add(1)
		}
		return Job{}, ctx.Err()
	}}
	worker, err := NewWorker(store, clock.NewFake(time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)), "worker", time.Minute, time.Hour, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunConcurrent(ctx, concurrency); !errors.Is(err, failure) {
		t.Fatalf("RunConcurrent = %v, want original claim failure", err)
	}
	if calls.Load() != concurrency || cancelled.Load() != concurrency-1 || ctx.Err() != nil {
		t.Fatalf("claims = %d, cancelled siblings = %d, parent = %v", calls.Load(), cancelled.Load(), ctx.Err())
	}
}

func TestWorkerCancellationBetweenClaims(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	store := claimTestStore{claim: func(context.Context, string, time.Time, time.Duration) (Job, error) {
		calls.Add(1)
		cancel()
		return Job{}, catalog.E(catalog.CodeNotFound, "no job is ready")
	}}
	worker, err := NewWorker(store, clock.NewFake(time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)), "worker", time.Minute, time.Hour, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunConcurrent(ctx, 1); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("RunConcurrent = %v, claims = %d; want cancellation after one empty claim", err, calls.Load())
	}
}
