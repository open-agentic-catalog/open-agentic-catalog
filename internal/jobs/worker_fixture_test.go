package jobs

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// Unsupported/internal worker inputs are fixtures of the Store/Worker contract,
// not commands a user may enqueue. Keep the production permission guard intact.
func enqueueWorkerFixture(service *Service, ctx context.Context, input EnqueueInput) (Job, error) {
	actor, _ := identity.FromContext(ctx)
	now := service.clock.Now()
	id, err := service.ids.New(now)
	if err != nil {
		return Job{}, err
	}
	limit := input.AttemptLimit
	if limit == 0 {
		limit = service.maxAttempts
	}
	job, _, err := service.store.Enqueue(ctx, Job{ID: id, TenantID: actor.TenantID, Kind: input.Kind, Payload: input.Payload,
		State: StateQueued, DeduplicationKey: input.DeduplicationKey, AttemptLimit: limit, NotBefore: now, CreatedAt: now, UpdatedAt: now})
	return job, err
}
