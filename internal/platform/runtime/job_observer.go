package runtime

import (
	"context"
	"log/slog"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/tracing"
)

// jobObserver owns committed Job diagnostics independently of trace sampling.
// Uncommitted errors propagate to the process supervisor or terminal reporter.
type jobObserver struct {
	*observability.Registry
	logger *slog.Logger
}

func (o jobObserver) JobFinished(ctx context.Context, kind string, result jobs.AttemptResult) {
	o.Registry.JobFinished(ctx, kind, result)
	level, event := slog.LevelDebug, "job.attempt_completed"
	class := jobFailureLabel(result.FailureClass)
	switch {
	case !result.Committed:
		event = "job.attempt_interrupted"
	case class == "internal":
		level = slog.LevelError
	case result.Outcome == "failed" || result.Outcome == "dead_letter" || result.Outcome == "unsupported":
		level = slog.LevelWarn
	}
	attributes := make([]any, 0, 20)
	attributes = append(attributes, "event", event, "component", "job_worker", "kind", observability.JobKindLabel(kind), "outcome", observability.JobOutcomeLabel(result.Outcome), "failure_class", class, "committed", result.Committed)
	attributes = append(attributes, tracing.LogAttributes(ctx)...)
	o.logger.Log(ctx, level, "job attempt finished", attributes...)
}

func jobFailureLabel(class jobs.FailureClass) string {
	switch class {
	case "":
		return "none"
	case jobs.FailureInvalidInput, jobs.FailurePolicyDenied, jobs.FailureAuthentication, jobs.FailureRateLimited, jobs.FailureTransientUpstream, jobs.FailurePermanentUpstream, jobs.FailureDependency, jobs.FailureInternal, jobs.FailureCancelled:
		return string(class)
	default:
		return "internal"
	}
}
