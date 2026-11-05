// Package operations provides the worker-only HTTP health and metrics surface.
// It deliberately contains no product routes and is never attached to the API
// Service or discovery ingress.
package operations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/buildinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
)

// Dependencies contains the worker operations server's process-local state and
// required PostgreSQL readiness check.
type Dependencies struct {
	Process                     *processinfo.Process
	Observability               *observability.Registry
	Readiness                   func(context.Context) error
	Version                     string
	Commit                      string
	SourceState                 string
	HousekeepingInterval        time.Duration
	TerminalJobRetention        time.Duration
	AcknowledgedOutboxRetention time.Duration
}

// Server exposes the worker's operations-only health, status, version, and
// metrics handler. It owns no listener and contains no product API routes.
type Server struct {
	process       *processinfo.Process
	observability *observability.Registry
	readiness     func(context.Context) error
	version       string
	commit        string
	sourceState   string
	housekeeping  *housekeepingPolicy
	handler       http.Handler
}

type statusResponse struct {
	*processinfo.Snapshot
	Status       string                                                   `json:"status"`
	Role         string                                                   `json:"role"`
	State        observability.Lifecycle                                  `json:"state"`
	Ready        *bool                                                    `json:"ready,omitempty"`
	Degraded     bool                                                     `json:"degraded"`
	Components   map[observability.Component]observability.ComponentState `json:"components,omitempty"`
	Dependencies map[string]bool                                          `json:"dependencies,omitempty"`
	Version      string                                                   `json:"version,omitempty"`
	SourceState  string                                                   `json:"sourceState,omitempty"`
	Commit       string                                                   `json:"commit,omitempty"`
	Housekeeping *housekeepingPolicy                                      `json:"housekeeping,omitempty"`
}

type housekeepingPolicy struct {
	Interval                    string `json:"interval"`
	TerminalJobRetention        string `json:"terminalJobRetention"`
	AcknowledgedOutboxRetention string `json:"acknowledgedOutboxRetention"`
	BatchLimit                  int    `json:"batchLimit"`
}

// New constructs a worker operations server from explicit dependencies.
func New(dependencies Dependencies) (*Server, error) {
	if dependencies.Observability == nil || dependencies.Readiness == nil {
		return nil, errors.New("worker operations dependencies are required")
	}
	if dependencies.Process == nil {
		process, err := processinfo.New()
		if err != nil {
			return nil, err
		}
		dependencies.Process = process
	}
	server := &Server{process: dependencies.Process, observability: dependencies.Observability, readiness: dependencies.Readiness, version: dependencies.Version, commit: dependencies.Commit, sourceState: buildinfo.State(dependencies.SourceState)}
	if dependencies.HousekeepingInterval > 0 && dependencies.TerminalJobRetention > 0 && dependencies.AcknowledgedOutboxRetention > 0 {
		server.housekeeping = &housekeepingPolicy{Interval: dependencies.HousekeepingInterval.String(), TerminalJobRetention: dependencies.TerminalJobRetention.String(), AcknowledgedOutboxRetention: dependencies.AcknowledgedOutboxRetention.String(), BatchLimit: housekeeping.BatchLimit}
	}
	router := http.NewServeMux()
	router.HandleFunc("GET /startupz", server.handleStartup)
	router.HandleFunc("GET /livez", server.handleLive)
	router.HandleFunc("GET /readyz", server.handleReady)
	router.HandleFunc("GET /status", server.handleStatus)
	router.HandleFunc("GET /metrics", server.handleMetrics)
	router.HandleFunc("GET /version", server.handleVersion)
	server.handler = server.metricsMiddleware(securityHeaders(rejectQuery(router)))
	return server, nil
}

// Handler returns the complete operations-only HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// handleStartup confirms that the constructed operations handler is reachable;
// process composition completes before its listener starts accepting requests.
func (s *Server) handleStartup(writer http.ResponseWriter, request *http.Request) {
	state, degraded := s.observability.State()
	writeJSON(writer, http.StatusOK, statusResponse{Status: "started", Role: "worker", State: state, Degraded: degraded})
}

// handleLive reports process state without checking PostgreSQL or receivers.
func (s *Server) handleLive(writer http.ResponseWriter, _ *http.Request) {
	state, degraded := s.observability.State()
	writeJSON(writer, http.StatusOK, statusResponse{Status: "live", Role: "worker", State: state, Degraded: degraded})
}

// handleReady requires the running lifecycle and a bounded database readiness
// check. Degraded optional delivery does not independently make the worker unready.
func (s *Server) handleReady(writer http.ResponseWriter, request *http.Request) {
	state, degraded := s.observability.State()
	if state != observability.LifecycleRunning {
		s.observability.ObserveOperation(observability.OperationReadiness, observability.OutcomeFailed)
		writeJSON(writer, http.StatusServiceUnavailable, statusResponse{Status: "not_ready", Role: "worker", State: state, Ready: boolPointer(false), Degraded: degraded})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := s.readiness(ctx); err != nil {
		s.observability.UpdateDependency("postgresql", false)
		s.observability.ObserveOperation(observability.OperationReadiness, observability.OutcomeFailed)
		writeJSON(writer, http.StatusServiceUnavailable, statusResponse{Status: "not_ready", Role: "worker", State: state, Ready: boolPointer(false), Degraded: true})
		return
	}
	s.observability.UpdateDependency("postgresql", true)
	s.observability.ObserveOperation(observability.OperationReadiness, observability.OutcomeSucceeded)
	writeJSON(writer, http.StatusOK, statusResponse{Status: "ready", Role: "worker", State: state, Ready: boolPointer(true), Degraded: degraded})
}

// handleStatus combines required dependency readiness with bounded component
// degradation. Its HTTP success indicates a readable status report, not readiness.
func (s *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	state, degraded, components, dependencies := s.observability.HealthState()
	// Receiver outcomes are durable evidence, not supervisor errors. Consult
	// their bounded aggregate only on status/metrics, never on liveness or the
	// required PostgreSQL readiness path.
	if _, enabled := components[observability.ComponentWebhookDelivery]; enabled {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		snapshot, _ := s.observability.Snapshot(ctx)
		cancel()
		components = snapshot.Components
		degraded = degraded || components[observability.ComponentWebhookDelivery] == observability.ComponentDegraded
	}
	ready := state == observability.LifecycleRunning
	if ready {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if err := s.readiness(ctx); err != nil {
			ready = false
			degraded = true
			s.observability.UpdateDependency("postgresql", false)
			if _, monitored := dependencies["postgresql"]; monitored {
				dependencies["postgresql"] = false
			}
		} else {
			s.observability.UpdateDependency("postgresql", true)
			if _, monitored := dependencies["postgresql"]; monitored {
				dependencies["postgresql"] = true
			}
		}
	}
	status := "ready"
	if !ready {
		status = "not_ready"
	} else if degraded {
		status = "degraded"
	}
	snapshot := s.process.Read()
	writeJSON(writer, http.StatusOK, statusResponse{Snapshot: &snapshot, Status: status, Role: "worker", State: state, Ready: boolPointer(ready), Degraded: degraded, Components: components, Dependencies: dependencies, Version: s.version, Commit: s.commit, SourceState: s.sourceState, Housekeeping: s.housekeeping})
}

// handleMetrics bounds collection time and emits the registry's fixed-label text.
func (s *Server) handleMetrics(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = writer.Write(s.observability.Render(ctx))
}

// handleVersion exposes build and in-process lifecycle metadata without I/O.
func (s *Server) handleVersion(writer http.ResponseWriter, _ *http.Request) {
	state, degraded := s.observability.State()
	writeJSON(writer, http.StatusOK, statusResponse{Status: "version", Role: "worker", State: state, Degraded: degraded, Version: s.version, Commit: s.commit, SourceState: s.sourceState})
}

// rejectQuery rejects query strings and alternate escaped paths because the
// operations contract has no parameters or encoded-path aliases.
func rejectQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.RawPath != "" {
			writeJSON(writer, http.StatusBadRequest, statusResponse{Status: "invalid_request", Role: "worker"})
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// securityHeaders prevents caching and browser embedding of operational responses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(writer, request)
	})
}

// writeJSON commits one typed status response; a write failure cannot replace
// the already sent status with a second response.
func writeJSON(writer http.ResponseWriter, status int, value statusResponse) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		return
	}
}

type metricResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

// WriteHeader records only the first status, matching net/http response semantics.
func (writer *metricResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

// Write accounts for successfully written bytes and an implicit success status.
func (writer *metricResponseWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	written, err := writer.ResponseWriter.Write(value)
	writer.bytes += int64(written)
	return written, err
}

// metricsMiddleware measures each request using the matched route pattern so
// caller-controlled paths cannot expand the registry's label vocabulary.
func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		finish := s.observability.BeginHTTP()
		startedAt := time.Now()
		capture := &metricResponseWriter{ResponseWriter: writer}
		next.ServeHTTP(capture, request)
		status := capture.status
		if status == 0 {
			status = http.StatusOK
		}
		finish(request.Method, request.Pattern, status, capture.bytes, time.Since(startedAt))
	})
}

// boolPointer preserves an explicit false readiness value in optional JSON fields.
func boolPointer(value bool) *bool { return &value }
