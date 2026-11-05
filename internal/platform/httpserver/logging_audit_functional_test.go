//go:build functional

package httpserver

import (
	"bytes"
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// Failure is injected at the repository port before an atomic write. Real
// PostgreSQL rollback, mandatory append and replay have integration fixtures.
type telemetryAuditRepository struct {
	*catalog.MemoryRepository
	fail bool
}

// Capture attempted records even when the external log sink cannot accept them.
// This deliberately does not panic or block: synchronous logging can still block
// on a stalled collector, and the service promises no durable diagnostic delivery.
type unavailableLogWriter struct {
	attempted bytes.Buffer
}

func (w *unavailableLogWriter) Write(raw []byte) (int, error) {
	_, _ = w.attempted.Write(raw)
	return 0, errors.New("private-log-sink")
}

func (r *telemetryAuditRepository) CreateNamespace(ctx context.Context, n catalog.Namespace, a catalog.AuditEvent, event catalog.OutboxEvent) (catalog.Namespace, error) {
	if r.fail {
		return catalog.Namespace{}, catalog.Wrap(catalog.CodeInternal, "required audit unavailable", errors.New("private-persistence"))
	}
	return r.MemoryRepository.CreateNamespace(ctx, n, a, event)
}

func (r *telemetryAuditRepository) AppendAuditEvent(ctx context.Context, a catalog.AuditEvent) error {
	if r.fail {
		return errors.New("private-persistence")
	}
	return r.MemoryRepository.AppendAuditEvent(ctx, a)
}

func TestLoggingAndTraceSelectionPreserveAuditReplayAndNotifications(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, level := range []string{"debug", "info", "warn", "error"} {
			for _, traceEnabled := range []bool{false, true} {
				for _, sink := range []string{"available", "unavailable"} {
					traceName := "trace-off"
					if traceEnabled {
						traceName = "trace-observer"
					}
					t.Run(format+"/"+level+"/"+traceName+"/"+sink, func(t *testing.T) {
						var logs bytes.Buffer
						var unavailable unavailableLogWriter
						var output io.Writer = &logs
						if sink == "unavailable" {
							output = &unavailable
						}
						logger := diagnostics.NewLogger(output, level, format)
						repo := &telemetryAuditRepository{MemoryRepository: catalog.NewMemoryRepository()}
						serviceClock := clock.NewFake(projectionTime)
						service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: serviceClock, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte(strings.Repeat("k", 32))})
						if err != nil {
							t.Fatal(err)
						}
						s := &Server{catalog: service, logger: logger, maxBodyBytes: 4096, cursorKey: []byte(strings.Repeat("k", 32)), now: serviceClock.Now, observability: observability.New(serviceClock.Now)}
						router := http.NewServeMux()
						router.HandleFunc("POST /api/v1/namespaces", s.handleNamespacesCreate)
						router.HandleFunc("GET /api/v1/audit-events", s.handleAuditEvents)
						handler := s.metricsMiddleware(router, auditExecution(newIdempotencyCache(idempotency.NewMemoryStore(), logger).middleware(4096, router)))
						tracer := &ingressTrace{}
						if traceEnabled {
							handler = traceHTTP(tracer, router, handler)
						}
						handler = requestIDMiddleware(handler)
						actor := projectionActor(identity.RoleTenantAdmin)
						serve := func(method, path, key, body string) *httptest.ResponseRecorder {
							r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), method, path, strings.NewReader(body))
							r.Header.Set("Content-Type", "application/json")
							r.Header.Set("Idempotency-Key", key)
							r.Header.Set("X-Request-ID", "audit-request")
							w := httptest.NewRecorder()
							handler.ServeHTTP(w, r)
							return w
						}
						body := `{"slug":"safe","displayName":"private-document"}`
						first := serve("POST", "/api/v1/namespaces", "private-command", body)
						replay := serve("POST", "/api/v1/namespaces", "private-command", body)
						if first.Code != 201 || replay.Code != 201 || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) || replay.Header().Get("Idempotency-Replayed") != "true" {
							t.Fatalf("command/replay changed: %d %d", first.Code, replay.Code)
						}
						read := serve("GET", "/api/v1/audit-events?limit=10", "", "")
						if read.Code != 200 || !strings.Contains(read.Body.String(), "namespace.created") {
							t.Fatalf("audited read withheld: %d %s", read.Code, read.Body.String())
						}
						repo.fail = true
						denied := serve("GET", "/api/v1/audit-events?limit=10", "", "")
						failed := serve("POST", "/api/v1/namespaces", "failed-command", `{"slug":"failed","displayName":"private-document"}`)
						if denied.Code != 500 || failed.Code != 500 || strings.Contains(denied.Body.String(), "namespace.created") || strings.Contains(denied.Body.String(), "private-") {
							t.Fatal("failed mandatory audit released data or success")
						}
						namespaces, err := repo.ListNamespaces(t.Context(), actor.TenantID, catalog.NamespaceListRequest{Limit: 10})
						if err != nil || len(namespaces) != 1 {
							t.Fatalf("failed write/replay changed state: %v %v", namespaces, err)
						}
						records, err := repo.AuditEvents(t.Context(), actor.TenantID, catalog.AuditEventListRequest{Limit: 10})
						if err != nil || len(records) != 2 {
							t.Fatalf("audit inventory: %d %v", len(records), err)
						}
						actions := map[string]int{}
						for _, record := range records {
							if err := audit.ValidateNewRecord(record); err != nil {
								t.Fatal(err)
							}
							if record.Actor != actor.MembershipID || record.RequestID != "audit-request" {
								t.Fatal("audit attribution changed")
							}
							actions[record.Action]++
						}
						if actions["namespace.created"] != 1 || actions[audit.ActionAuditEventsRead] != 1 {
							t.Fatalf("classified evidence: %v", actions)
						}
						events, err := repo.ClaimOutbox(t.Context(), "worker", serviceClock.Now(), time.Minute, 10)
						if err != nil || len(events) != 1 || events[0].Type != catalog.CanonicalEventType("namespace.created") {
							t.Fatalf("audit-only read manufactured a notification: %v %v", events, err)
						}
						diagnostic := logs.String()
						if sink == "unavailable" {
							diagnostic = unavailable.attempted.String()
						}
						if strings.Contains(diagnostic, "private-") || strings.Contains(diagnostic, actor.Subject) || strings.Count(diagnostic, "http.request_failed") != 2 {
							t.Fatalf("missing or unsafe diagnostic: %s", diagnostic)
						}
						if traceEnabled && (tracer.calls != 5 || tracer.outcome != "5xx") {
							t.Fatalf("trace observation changed: %+v", tracer)
						}
					})
				}
			}
		}
	}
}
