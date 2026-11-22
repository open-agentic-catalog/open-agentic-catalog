package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

func TestClaimedAuthorityEndsWithHandlerAndBindsEntireAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewMemoryStore()
		now := time.Now()
		job := Job{ID: "00000000-0000-7000-8000-000000000010", TenantID: "00000000-0000-7000-8000-000000000001", Kind: "publication.generate", PayloadVersion: 1, Payload: json.RawMessage(`{}`), State: StateQueued, AttemptLimit: 1, NotBefore: now, CreatedAt: now, UpdatedAt: now}
		if _, _, err := store.Enqueue(t.Context(), job); err != nil {
			t.Fatal(err)
		}
		var retained context.Context
		var claim Job
		worker, err := NewWorker(store, clock.Real{}, "owned-unit", time.Minute, time.Second, time.Minute, map[string]Handler{"publication.generate": func(ctx context.Context, current Job, _ func(int) error) (any, error) {
			retained, claim = ctx, current //nolint:fatcontext // Deliberately retain handler input to prove claim authority expires after return.
			if err := CheckClaimedAttempt(ctx, current); err != nil {
				t.Fatal("live exact claim refused", err)
			}
			for _, mutate := range []func(*Job){
				func(j *Job) { j.ID = "other" }, func(j *Job) { j.TenantID = "other" }, func(j *Job) { j.Kind = "source.sync" }, func(j *Job) { j.Attempt++ }, func(j *Job) { j.LeaseOwner = "other" }, func(j *Job) { j.Payload = json.RawMessage(`{"host":"other"}`) },
			} {
				other := current
				mutate(&other)
				if err := CheckClaimedAttempt(ctx, other); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
					t.Fatal("claim binding escaped", other, err)
				}
			}
			return map[string]any{}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := worker.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		if retained == nil {
			t.Fatal("handler was not executed")
		}
		if err := CheckClaimedAttempt(retained, claim); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
			t.Fatal("completed handler authority survived", err)
		}
		if err := CheckClaimedAttempt(t.Context(), claim); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
			t.Fatal("Job-shaped input fabricated authority", err)
		}
	})
}

func TestClaimedAuthorityRechecksStoredLeaseAndCancellation(t *testing.T) {
	for _, action := range []string{"reclaim", "cancel"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const lease = time.Minute
				clk := clock.NewFake(time.Now())
				store := NewMemoryStore()
				job := Job{ID: "job", TenantID: "tenant", Kind: "publication.generate", Payload: json.RawMessage(`{}`), State: StateQueued, AttemptLimit: 2, NotBefore: clk.Now()}
				if _, _, err := store.Enqueue(t.Context(), job); err != nil {
					t.Fatal(err)
				}
				handler := func(ctx context.Context, claim Job, _ func(int) error) (any, error) {
					// Advance only the service clock so no periodic heartbeat renews the lease.
					clk.Advance(lease - time.Nanosecond)
					if err := CheckClaimedAttempt(ctx, claim); err != nil {
						t.Fatalf("live claim refused before expiry: %v", err)
					}
					if action == "cancel" {
						if _, err := store.RequestCancellation(ctx, claim.TenantID, claim.ID, clk.Now()); err != nil {
							t.Fatal(err)
						}
						err := CheckClaimedAttempt(ctx, claim)
						if !errors.Is(err, context.Canceled) || ctx.Err() != nil {
							t.Fatalf("stored cancellation: claim check %v, handler context %v", err, ctx.Err())
						}
						return nil, err
					}
					clk.Advance(time.Nanosecond)
					if err := CheckClaimedAttempt(ctx, claim); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
						t.Fatalf("claim at exact expiry: %v", err)
					}
					if _, err := store.Claim(ctx, "replacement", clk.Now(), lease); err != nil {
						t.Fatal(err)
					}
					err := CheckClaimedAttempt(ctx, claim)
					if catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
						t.Fatalf("original claim after reassignment: %v", err)
					}
					return nil, err
				}
				worker, err := NewWorker(store, clk, "original", lease, time.Second, lease, map[string]Handler{job.Kind: handler})
				if err != nil {
					t.Fatal(err)
				}
				runErr := worker.RunOnce(t.Context())
				stored, err := store.Get(t.Context(), job.TenantID, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				if action == "cancel" {
					if runErr != nil || stored.State != StateCancelled || len(stored.Result) != 0 {
						t.Fatalf("cancelled attempt: state=%s result=%s err=%v", stored.State, stored.Result, runErr)
					}
				} else if catalog.ErrorCodeOf(runErr) != catalog.CodeConflict || stored.State != StateRunning || stored.LeaseOwner != "replacement" || stored.Attempt != 2 || len(stored.Result) != 0 {
					t.Fatalf("replacement attempt: job=%+v err=%v", stored, runErr)
				}
			})
		})
	}
}
