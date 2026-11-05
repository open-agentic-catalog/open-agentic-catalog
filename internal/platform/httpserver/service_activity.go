package httpserver

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

const maxServiceActivityBytes = 64 << 10

type serviceActivityResponse struct {
	Schema         string    `json:"schema"`
	Population     string    `json:"population"`
	Scope          string    `json:"scope"`
	Source         string    `json:"source"`
	InstanceID     string    `json:"instanceId"`
	Role           string    `json:"role"`
	StartedAt      time.Time `json:"startedAt"`
	ObservedAt     time.Time `json:"observedAt"`
	ElapsedSeconds float64   `json:"elapsedSeconds"`
	observability.ActivitySample
}

// handleServiceActivity discloses only fixed process counters after current
// administrator admission. Registration requires the deployment disclosure gate.
func (s *Server) handleServiceActivity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, err := identity.RequirePermission(r.Context(), "access.manage", ""); err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "this read does not accept a request body"))
		return
	}
	if s.process == nil || (s.role != "api" && s.role != "all") {
		s.fail(w, r, catalog.E(catalog.CodeDependencyUnavailable, "service activity is unavailable"))
		return
	}
	sample, err := s.observability.Activity(s.mcp != nil)
	if err != nil {
		s.fail(w, r, catalog.E(catalog.CodeDependencyUnavailable, "service activity is unavailable"))
		return
	}
	process, elapsed := s.process.ReadWithElapsed()
	if elapsed < 0 {
		s.fail(w, r, catalog.E(catalog.CodeDependencyUnavailable, "service activity is unavailable"))
		return
	}
	result := serviceActivityResponse{Schema: "service-activity-v1", Population: "catalog-http-v1", Scope: "responding_api_process", Source: "process_memory", InstanceID: process.InstanceID, Role: s.role, StartedAt: process.StartedAt, ObservedAt: process.ObservedAt, ElapsedSeconds: elapsed.Seconds(), ActivitySample: sample}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > maxServiceActivityBytes {
		s.fail(w, r, catalog.E(catalog.CodeDependencyUnavailable, "service activity is unavailable"))
		return
	}
	// Keep the standard deadline, encoding and write-error handling. The bounded
	// RawMessage prevents another projection or provider call during response work.
	s.respondJSON(w, r, http.StatusOK, json.RawMessage(raw))
}
