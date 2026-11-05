package jobs

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"testing"
	"testing/synctest"
	"time"
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
