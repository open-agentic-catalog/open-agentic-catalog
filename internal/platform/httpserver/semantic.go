// Semantic administration uses the ordinary tenant authorization, audited Job
// enqueue and command-idempotency boundaries; it exposes no provider settings.
package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

// handleSemanticReindex rebuilds one current local or public snapshot scope.
func (s *Server) handleSemanticReindex(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Reason     string `json:"reason"`
		SnapshotID string `json:"snapshotId,omitempty"`
	}
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	target, err := s.catalog.SemanticReindexTarget(request.Context(), input.SnapshotID)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	payload, err := json.Marshal(target)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	job, _, err := s.jobs.Enqueue(request.Context(), jobs.EnqueueInput{Kind: "semantic.reindex", PayloadVersion: 1, Payload: payload, DeduplicationKey: target.Profile + ":" + target.Scope, SemanticReason: input.Reason})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	s.respondJSON(writer, request, http.StatusAccepted, jobs.PublicView(job))
}
