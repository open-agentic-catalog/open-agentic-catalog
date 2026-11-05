package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

func (s *Server) handleTrustRequest(w http.ResponseWriter, r *http.Request) {
	var input federation.TrustRequest
	if err := decodeJSON(w, r, s.maxBodyBytes, &input); err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.federation.PrepareTrustAssessment(r.Context(), r.PathValue("sourceId"), input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	raw, err := json.Marshal(p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	job, _, err := s.jobs.Enqueue(r.Context(), jobs.EnqueueInput{Kind: jobs.TrustKind, PayloadVersion: 1, Payload: raw, DeduplicationKey: jobs.TrustKind + ":" + audit.DigestBytes(raw), TrustReason: input.Reason})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	s.respondJSON(w, r, http.StatusAccepted, jobs.PublicView(job))
}
func (s *Server) handleTrustRead(w http.ResponseWriter, r *http.Request) {
	job, err := s.jobs.PrivateTrustJob(r.Context(), r.PathValue("jobId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := s.federation.ReadTrustAssessment(r.Context(), r.PathValue("sourceId"), job)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, result)
}
