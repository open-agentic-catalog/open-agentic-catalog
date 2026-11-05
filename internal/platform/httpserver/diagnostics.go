// HTTP diagnostics observe the final classified response, including middleware
// refusals, without capturing new response bodies or depending on tracing.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

type problemObservation struct {
	code      catalog.ErrorCode
	status    int
	errorType string
	stage     string
	diagnosed bool
}
type problemObservationKey struct{}

// A rejected reservation never ran the command. Only its ordinary binding
// refusals use common HTTP diagnostics; joined cleanup failures and unexpected
// causes must retain the dedicated operational diagnostic.
func atomicReservationRefusal(err error) bool {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		return false
	}
	var refusal *catalog.DomainError
	return errors.As(err, &refusal) && refusal.Cause == nil && (refusal.Code == catalog.CodeForbidden || refusal.Code == catalog.CodeIdempotencyKeyReused)
}

func observeProblem(request *http.Request, code catalog.ErrorCode, status int, err error) {
	if observation, ok := request.Context().Value(problemObservationKey{}).(*problemObservation); ok {
		observation.code, observation.status, observation.errorType = code, status, fmt.Sprintf("%T", err)
		// Only these reviewed fixed stages cross into request logs. Never
		// project arbitrary stage text or a dependency's Error method.
		observation.stage = ""
		switch safeerr.Stage(err) {
		case "webhook.endpoint.time_regression":
			observation.stage = "webhook.endpoint.time_regression"
		case "authorization.assignment.time_regression":
			observation.stage = "authorization.assignment.time_regression"
		case "authorization.group.time_regression":
			observation.stage = "authorization.group.time_regression"
		case "authorization.group_member.time_regression":
			observation.stage = "authorization.group_member.time_regression"
		}
	}
}

// Replay bypasses the problem writer. Read only the fixed refusal projection
// from the already retained control response; never log its private content or
// invent an error type for an operation that did not run on this attempt.
func observeReplayedRefusal(request *http.Request, status int, header http.Header, body []byte) {
	if (status != http.StatusUnauthorized && status != http.StatusForbidden) || header.Get("Content-Type") != "application/problem+json" {
		return
	}
	var problem struct {
		Code   catalog.ErrorCode `json:"code"`
		Status int               `json:"status"`
	}
	if json.Unmarshal(body, &problem) != nil || problem.Status != status {
		return
	}
	if (status == http.StatusUnauthorized && problem.Code == catalog.CodeUnauthenticated) || (status == http.StatusForbidden && problem.Code == catalog.CodeForbidden) {
		if observation, ok := request.Context().Value(problemObservationKey{}).(*problemObservation); ok {
			observation.code, observation.status = problem.Code, status
		}
	}
}

func diagnosticOwned(ctx context.Context) {
	if observation, ok := ctx.Value(problemObservationKey{}).(*problemObservation); ok {
		observation.diagnosed = true
	}
}

// logProblem runs after response selection, so discarded captures and deadline
// translation cannot count or log the original error as the final refusal.
func (s *Server) logProblem(request *http.Request, status int, observed *problemObservation) {
	if observed.status != status || observed.code == "" {
		return
	}
	level := slog.LevelDebug
	switch {
	case observed.code == catalog.CodeUnauthenticated || observed.code == catalog.CodeForbidden:
		reason, index := "unauthenticated", 0
		if observed.code == catalog.CodeForbidden {
			reason, index = "forbidden", 1
		}
		s.observability.ObserveSecurityRefusal(reason)
		s.securityWarnings[index].Warn(s.now(), s.logger, "security refusals observed", "event", "security.refusal_summary", "component", "api", "reason", reason)
	case observed.code == catalog.CodeWebhookTestBusy || observed.code == catalog.CodeDependencyUnavailable || observed.code == catalog.CodeRequestDeadlineExceeded || (observed.code == catalog.CodePublicationNotReady && status == http.StatusServiceUnavailable):
		level = slog.LevelWarn
	default:
		if status >= 500 {
			level = slog.LevelError
		}
	}
	if observed.diagnosed {
		return
	}
	attributes := requestLogAttributes(request, "event", "http.request_failed", "component", "api", "error_code", observed.code, "status", status)
	if observed.errorType != "" {
		attributes = append(attributes, "error_type", observed.errorType)
	}
	if observed.stage != "" {
		attributes = append(attributes, "operation_stage", observed.stage)
	}
	if level == slog.LevelWarn {
		s.dependencyWarning.Warn(s.now(), s.logger, "request dependency unavailable", attributes...)
		return
	}
	s.logger.Log(request.Context(), level, "request failed", attributes...)
}
