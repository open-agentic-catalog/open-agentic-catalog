package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

// Use the production Worker to establish a real in-memory lease. A Job-shaped
// value or audit attribution by itself must never authorize a handler.
func claimedHandler(t *testing.T, kind string, handler jobs.Handler) jobs.Handler {
	t.Helper()
	return func(ctx context.Context, job jobs.Job, progress func(int) error) (any, error) {
		store := jobs.NewMemoryStore()
		clock := publicationJobTimeIDs{}
		job.ID = "00000000-0000-7000-8000-000000000011"
		job.Kind, job.State, job.AttemptLimit = kind, jobs.StateQueued, 5
		job.NotBefore, job.CreatedAt, job.UpdatedAt = clock.Now(), clock.Now(), clock.Now()
		if execution, ok := audit.JobExecutionFrom(ctx); ok {
			job.Actor = execution.Initiator
		}
		if _, _, err := store.Enqueue(ctx, job); err != nil {
			t.Fatal(err)
		}
		var result any
		var resultErr error
		worker, err := jobs.NewWorker(store, clock, "handler-fixture", time.Minute, time.Second, time.Minute, map[string]jobs.Handler{
			kind: func(bound context.Context, claimed jobs.Job, _ func(int) error) (any, error) {
				result, resultErr = handler(bound, claimed, progress)
				return result, resultErr
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := worker.RunOnce(ctx); err != nil && resultErr == nil {
			return nil, err
		}
		return result, resultErr
	}
}
