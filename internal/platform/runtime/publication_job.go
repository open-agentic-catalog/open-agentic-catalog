// Publication job composition binds candidate generation to the configured host
// and translates domain failures into safe durable Job outcomes.
package runtime

import (
	"context"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// publicationJobHandler selects one configured host before reporting progress
// or building an immutable candidate under the claimed Job's tenant.
func publicationJobHandler(publicationManagers map[string]*publication.Manager, metrics *observability.Registry) jobs.Handler {
	return func(ctx context.Context, job jobs.Job, progress func(int) error) (any, error) {
		var input struct {
			Host string `json:"host"`
		}
		if len(job.Payload) > 0 && string(job.Payload) != "{}" {
			if err := jsonUnmarshal(job.Payload, &input); err != nil {
				return nil, &jobs.HandlerError{Class: jobs.FailureInvalidInput, Safe: "Publication generation job payload is invalid.", Cause: err}
			}
		}
		manager := publicationManagers[input.Host]
		if manager == nil && input.Host == "" && len(publicationManagers) == 1 {
			manager = firstPublicationManager(publicationManagers)
		}
		if manager == nil {
			return nil, &jobs.HandlerError{Class: jobs.FailureInvalidInput, Safe: "Publication generation requires one exact configured discovery host."}
		}
		var authorityErr error
		ctx, authorityErr = workerActor(ctx, job, manager.Host())
		if authorityErr != nil {
			return nil, authorityErr
		}
		ctx = audit.WithJobTrigger(ctx, "manual")
		if err := progress(20); err != nil {
			return nil, err
		}
		snapshot, err := manager.BuildCandidate(ctx, job.TenantID)
		if err != nil {
			metrics.ObserveOperation(observability.OperationPublicationBuild, observability.OutcomeFailed)
			return nil, &jobs.HandlerError{Class: jobs.FailureInternal, Safe: "Publication candidate generation failed.", Cause: err}
		}
		metrics.ObserveOperation(observability.OperationPublicationBuild, observability.OutcomeSucceeded)
		if err := progress(100); err != nil {
			return nil, err
		}
		return jobs.SnapshotReference{ID: snapshot.ID, Host: snapshot.Host, Generation: snapshot.Generation, State: string(snapshot.State)}, nil
	}
}
