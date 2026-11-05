// Source synchronization composition adapts durable Jobs to revision-bound
// federation work without changing domain failure or audit semantics.
package runtime

import (
	"context"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// sourceSyncJobHandler binds durable input and worker attribution to one source
// revision, preserving partial outcomes and the domain's retry classification.
func sourceSyncJobHandler(syncAtRevision func(context.Context, string, int64) (federation.Snapshot, error), metrics *observability.Registry) jobs.Handler {
	return func(ctx context.Context, job jobs.Job, progress func(int) error) (any, error) {
		var input struct {
			SourceID       string `json:"sourceId"`
			SourceRevision int64  `json:"sourceRevision"`
			Trigger        string `json:"trigger"`
		}
		payloadErr := jsonUnmarshal(job.Payload, &input)
		if payloadErr != nil || input.SourceID == "" || input.SourceRevision <= 0 || job.PayloadVersion != 2 || input.Trigger != "manual" && input.Trigger != "automatic" {
			return nil, &jobs.HandlerError{Class: jobs.FailureInvalidInput, Safe: "Source sync job payload is invalid.", Cause: payloadErr}
		}
		var authorityErr error
		ctx, authorityErr = workerActor(ctx, job, input.SourceID)
		if authorityErr != nil {
			return nil, authorityErr
		}
		if err := progress(10); err != nil {
			return nil, err
		}
		ctx = audit.WithJobTrigger(ctx, input.Trigger)
		snapshot, err := syncAtRevision(ctx, input.SourceID, input.SourceRevision)
		if err != nil {
			metrics.ObserveOperation(observability.OperationSourceSync, observability.OutcomeFailed)
			class, retryAfter := federation.ClassifySyncError(err)
			return nil, &jobs.HandlerError{Class: jobFailureClass(class), Safe: sourceSyncSafeError(class), RetryAfter: retryAfter, Cause: err}
		}
		syncOutcome := observability.OutcomeSucceeded
		if snapshot.State == federation.SnapshotIncomplete {
			syncOutcome = observability.OutcomePartial
		}
		metrics.ObserveOperation(observability.OperationSourceSync, syncOutcome)
		if err := progress(100); err != nil {
			return nil, err
		}
		return jobs.SnapshotReference{ID: snapshot.ID, SourceID: snapshot.SourceID, State: string(snapshot.State)}, nil
	}
}
