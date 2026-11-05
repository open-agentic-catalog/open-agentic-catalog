package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestRequestFailureLogsOnlyReviewedClockStages(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, stage := range []string{"webhook.endpoint.time_regression", "authorization.assignment.time_regression", "authorization.group.time_regression", "authorization.group_member.time_regression", "private-stage-password-sentinel", ""} {
			t.Run(format+"/"+stage, func(t *testing.T) {
				var logs bytes.Buffer
				s := &Server{logger: diagnostics.NewLogger(&logs, "info", format), now: time.Now, observability: observability.New(nil)}
				observed := &problemObservation{}
				request := httptest.NewRequestWithContext(context.WithValue(t.Context(), problemObservationKey{}, observed), http.MethodPost, "/api/v1/webhooks/private-id:rotate-secret", nil)
				err := catalog.Wrap(catalog.CodeDependencyUnavailable, "private-dependency-guidance", errors.New("private-secret-password-sentinel"))
				if stage != "" {
					err = safeerr.At(stage, err)
				}
				response := httptest.NewRecorder()
				writeProblem(response, request, err)
				s.logProblem(request, response.Code, observed)
				if response.Code != http.StatusServiceUnavailable || strings.Contains(logs.String(), "private-") || strings.Contains(response.Body.String(), "private-secret") || strings.Contains(response.Body.String(), "operation_stage") {
					t.Fatal("clock diagnostic changed public status or exposed private diagnostic data")
				}
				if strings.Contains(logs.String(), "operation_stage") != (stage == "webhook.endpoint.time_regression" || stage == "authorization.assignment.time_regression" || stage == "authorization.group.time_regression" || stage == "authorization.group_member.time_regression") {
					t.Fatal("diagnostic failed to allowlist the operation stage")
				}
				if format == "json" {
					var entry map[string]any
					if err := json.Unmarshal(logs.Bytes(), &entry); err != nil || entry["level"] != "WARN" || entry["event"] != "http.request_failed" {
						t.Fatal("clock diagnostic lost its severity or event")
					}
				}
			})
		}
	}
}

func TestAuthorizationClockRefusalsPreserveConflictAndDebugPrivacy(t *testing.T) {
	for _, stage := range []string{"authorization.assignment.time_regression", "authorization.group.time_regression", "authorization.group_member.time_regression"} {
		for _, format := range []string{"text", "json"} {
			for _, level := range []string{"info", "debug"} {
				t.Run(stage+"/"+format+"/"+level, func(t *testing.T) {
					var logs bytes.Buffer
					s := &Server{logger: diagnostics.NewLogger(&logs, level, format), now: time.Now, observability: observability.New(nil)}
					observed := &problemObservation{}
					request := httptest.NewRequestWithContext(context.WithValue(t.Context(), problemObservationKey{}, observed), http.MethodPost, "/api/v1/access/assignments/00000000-0000-7000-8000-000000000001:revoke", nil)
					request.Header.Set("Authorization", "Bearer private-token-sentinel")
					err := safeerr.At(stage, identity.Wrap(identity.CodeConflict, "assignment cannot be revoked", errors.New("private-cause-sentinel")))
					response := httptest.NewRecorder()
					writeProblem(response, request, catalogIdentityError(err))
					s.logProblem(request, response.Code, observed)
					if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"CONFLICT"`) || strings.Contains(response.Body.String(), "operation_stage") || strings.Contains(response.Body.String(), "private-") || strings.Contains(logs.String(), "private-") {
						t.Fatal("assignment diagnostic changed the conflict or exposed private data")
					}
					if level == "info" {
						if logs.Len() != 0 {
							t.Fatal("expected conflict bypassed DEBUG filtering")
						}
					} else if !strings.Contains(logs.String(), stage) || !strings.Contains(logs.String(), "DEBUG") {
						t.Fatal("exact assignment refusal lost its DEBUG diagnostic")
					}
				})
			}
		}
	}
}

func TestReplayedRefusalsCountEachResponseWithoutExposingRetainedContent(t *testing.T) {
	for _, tc := range []struct {
		name, mediaType, body, reason string
		code                          catalog.ErrorCode
		status                        int
	}{
		{name: "forbidden", code: catalog.CodeForbidden, status: 403, reason: "forbidden"},
		{name: "unauthenticated", code: catalog.CodeUnauthenticated, status: 401, reason: "unauthenticated"},
		{name: "source policy", code: catalog.CodeSourcePolicyDenied, status: 403},
		{name: "plain status", status: 403},
		{name: "non problem", status: 403, mediaType: "application/json", body: `{"code":"FORBIDDEN","status":403}`},
		{name: "invalid JSON", status: 403, mediaType: "application/problem+json", body: `{"code":"FORBIDDEN","status":403`},
		{name: "different status", status: 403, mediaType: "application/problem+json", body: `{"code":"FORBIDDEN","status":401}`},
		{name: "different code", status: 403, mediaType: "application/problem+json", body: `{"code":"UNAUTHENTICATED","status":403}`},
		{name: "successful status", status: 200, mediaType: "application/problem+json", body: `{"code":"FORBIDDEN","status":200}`},
	} {
		for _, path := range []string{"atomic", "memory", "local waiter"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				var logs bytes.Buffer
				registry := observability.New(nil)
				s := &Server{logger: diagnostics.NewLogger(&logs, "debug", "json"), now: time.Now, observability: registry}
				store := &atomicReplayStore{}
				cache := newAtomicTestCache(store, s.logger)
				if path == "memory" {
					cache = newIdempotencyCache(idempotency.NewMemoryStore(), s.logger)
				}
				actor := identity.Actor{TenantID: "private-tenant", Subject: "private-subject"}
				calls := 0
				router := http.NewServeMux()
				router.HandleFunc("POST /api/v1/resources", func(w http.ResponseWriter, r *http.Request) {
					calls++
					if tc.code != "" {
						writeProblem(w, r, catalog.E(tc.code, "private-detail"))
						return
					}
					w.Header().Set("Content-Type", tc.mediaType)
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				})
				handler := requestIDMiddleware(s.metricsMiddleware(router, cache.middleware(1024, router)))
				serve := func(id string) *httptest.ResponseRecorder {
					request := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodPost, "/api/v1/resources", nil)
					request.Header.Set("Idempotency-Key", "private-key")
					request.Header.Set("X-Request-ID", id)
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					return response
				}
				first := serve("original-attempt")
				if path == "local waiter" {
					done := make(chan struct{})
					close(done)
					cache.entries["private-tenant\x00private-subject\x00POST /api/v1/resources\x00private-key"] = &storedResponse{
						digest: store.response.Digest, authorizationDigest: actor.AuthorizationDigest(),
						status: first.Code, header: http.Header(store.response.Headers).Clone(), body: append([]byte(nil), first.Body.Bytes()...), retained: true, done: done,
					}
				}
				logs.Reset()
				replay := serve("current-attempt")
				if calls != 1 || first.Code != tc.status || replay.Code != tc.status || !bytes.Equal(replay.Body.Bytes(), first.Body.Bytes()) || replay.Header().Get("Idempotency-Replayed") != "true" || replay.Header().Get("X-Request-ID") != "current-attempt" {
					t.Fatalf("replay changed: calls=%d first=%d replay=%d headers=%v", calls, first.Code, replay.Code, replay.Header())
				}
				for _, reason := range []string{"unauthenticated", "forbidden"} {
					want := 0
					if tc.reason == reason {
						want = 2
					}
					if !strings.Contains(string(registry.Render(t.Context())), fmt.Sprintf(`reason="%s"} %d`, reason, want)) {
						t.Fatalf("replayed %s refusal not counted correctly", reason)
					}
				}
				if tc.reason != "" {
					var entry map[string]any
					if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
						t.Fatalf("expected one replay diagnostic: %s (%v)", logs.String(), err)
					}
					if entry["request_id"] != "current-attempt" || entry["event"] != "http.request_failed" || entry["level"] != "DEBUG" || entry["error_type"] != nil {
						t.Fatalf("wrong replay diagnostic: %v", entry)
					}
				}
				if strings.Contains(logs.String(), "private-") || strings.Contains(logs.String(), "original-attempt") {
					t.Fatalf("retained content leaked: %s", logs.String())
				}
			})
		}
	}
}

func TestAtomicReservationDiagnosticsRespectRefusalsAndCleanupFailures(t *testing.T) {
	refusal := catalog.E(catalog.CodeForbidden, "authorization changed")
	for _, tc := range []struct {
		name     string
		err      error
		deadline bool
		status   int
		level    string
		refusals int
		executed bool
	}{
		{name: "success", status: 201, executed: true},
		{name: "reused key", err: catalog.E(catalog.CodeIdempotencyKeyReused, "key reused"), status: 409, level: "DEBUG"},
		{name: "wrapped reuse", err: fmt.Errorf("secret-sentinel: %w", catalog.E(catalog.CodeIdempotencyKeyReused, "key reused")), status: 409, level: "DEBUG"},
		{name: "authorization", err: refusal, status: 403, level: "DEBUG", refusals: 1},
		{name: "deadline overrides refusal", err: refusal, deadline: true, status: 503, level: "WARN"},
		{name: "refusal and failed cleanup", err: errors.Join(refusal, catalog.Wrap(catalog.CodeDependencyUnavailable, "cleanup failed", errors.New("secret-sentinel"))), status: 403, level: "ERROR", refusals: 1},
		{name: "refusal with unexpected cause", err: catalog.Wrap(catalog.CodeForbidden, "refused", errors.New("secret-sentinel")), status: 403, level: "ERROR", refusals: 1},
		{name: "database failure", err: catalog.Wrap(catalog.CodeDependencyUnavailable, "database unavailable", errors.New("secret-sentinel")), status: 503, level: "ERROR"},
		{name: "unclassified failure", err: errors.New("secret-sentinel"), status: 500, level: "ERROR"},
		{name: "failed completion", err: refusal, status: 403, level: "ERROR", refusals: 1, executed: true},
	} {
		for _, level := range []string{"debug", "error"} {
			t.Run(tc.name+"/"+level, func(t *testing.T) {
				var logs bytes.Buffer
				registry := observability.New(nil)
				s := &Server{logger: diagnostics.NewLogger(&logs, level, "json"), now: time.Now, observability: registry}
				store := &atomicReplayStore{err: tc.err, errAfterExecute: tc.executed}
				router := http.NewServeMux()
				calls := 0
				router.HandleFunc("POST /api/v1/resources", func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusCreated) })
				handler := requestIDMiddleware(s.metricsMiddleware(router, newAtomicTestCache(store, s.logger).middleware(1024, router)))
				ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant-correlation", Subject: "actor-correlation"})
				if tc.deadline {
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
				}
				request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/resources", nil)
				request.Header.Set("Idempotency-Key", "secret-sentinel")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != tc.status || (calls == 1) != tc.executed || store.executions.Load() != 1 || response.Header().Get("Idempotency-Replayed") != "" {
					t.Fatalf("status=%d calls=%d store=%d", response.Code, calls, store.executions.Load())
				}
				count := 0
				for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
					if line == "" {
						continue
					}
					var entry map[string]any
					if err := json.Unmarshal([]byte(line), &entry); err != nil {
						t.Fatal(err)
					}
					if entry["event"] == "security.refusal_summary" {
						continue
					}
					count++
					event := "http.request_failed"
					if tc.level == "ERROR" {
						event = "idempotency.completion_failed"
					}
					if entry["event"] != event || entry["level"] != tc.level || entry["request_id"] == "" {
						t.Fatalf("incorrect owner/severity: %s", logs.String())
					}
				}
				want := 0
				if tc.level != "" && (level == "debug" || tc.level == "ERROR") {
					want = 1
				}
				if count != want || strings.Contains(logs.String(), "secret-sentinel") || strings.Contains(logs.String(), "actor-correlation") || (tc.level != "ERROR" && strings.Contains(logs.String(), "tenant-correlation")) {
					t.Fatalf("missing/duplicate or unsafe diagnostic: %s", logs.String())
				}
				if !strings.Contains(string(registry.Render(t.Context())), fmt.Sprintf(`reason="forbidden"} %d`, tc.refusals)) {
					t.Fatal("final refusal count changed with logging")
				}
			})
		}
	}
}

func TestFinalHTTPDiagnosticsClassifyOnceAndRedact(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		code                 catalog.ErrorCode
		deadline, ard, panic bool
		status               int
		level, reason        string
	}{
		{name: "middleware authentication", code: catalog.CodeUnauthenticated, status: 401, level: "DEBUG", reason: "unauthenticated"},
		{name: "forbidden", code: catalog.CodeForbidden, status: 403, level: "DEBUG", reason: "forbidden"},
		{name: "protected discovery", code: catalog.CodeUnauthenticated, ard: true, status: 401, level: "DEBUG", reason: "unauthenticated"},
		{name: "discovery authorization", code: catalog.CodeForbidden, ard: true, status: 403, level: "DEBUG", reason: "forbidden"},
		{name: "source policy", code: catalog.CodeSourcePolicyDenied, status: 403, level: "DEBUG"},
		{name: "public snapshot pending", code: catalog.CodePublicationNotReady, ard: true, status: 503, level: "WARN"},
		{name: "control snapshot pending", code: catalog.CodePublicationNotReady, status: 422, level: "DEBUG"},
		{name: "internal", code: catalog.CodeInternal, status: 500, level: "ERROR"},
		{name: "dependency", code: catalog.CodeDependencyUnavailable, status: 503, level: "WARN"},
		{name: "test slot busy", code: catalog.CodeWebhookTestBusy, status: 503, level: "WARN"},
		{name: "deadline overrides internal", code: catalog.CodeInternal, deadline: true, status: 503, level: "WARN"},
		{name: "deadline overrides refusal", code: catalog.CodeForbidden, deadline: true, status: 503, level: "WARN"},
		{name: "panic", panic: true, status: 500, level: "ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			now := time.Unix(10, 0)
			registry := observability.New(nil)
			server := &Server{logger: diagnostics.NewLogger(&logs, "debug", "json"), now: func() time.Time { return now }, observability: registry}
			router := http.NewServeMux()
			route := "/api/v1/resources/private-resource"
			if tc.ard {
				route = "/ard/v1/agents"
			}
			router.HandleFunc("GET /api/v1/resources/{resourceId}", func(w http.ResponseWriter, r *http.Request) {
				if tc.panic {
					panic("private-panic")
				}
				server.fail(w, r, catalog.Wrap(tc.code, "fixed", errors.New("private-dsn")))
			})
			router.HandleFunc("GET /ard/v1/agents", func(w http.ResponseWriter, r *http.Request) { writeARDProblem(w, r, catalog.E(tc.code, "fixed")) })
			var next http.Handler = router
			if tc.name == "middleware authentication" {
				next = authentication(discoveryAuthenticator{}, next)
			}
			handler := requestIDMiddleware(server.metricsMiddleware(router, recoverPanics(server.logger, next)))
			for range 3 {
				ctx := t.Context()
				if tc.deadline {
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
				}
				request := httptest.NewRequestWithContext(ctx, http.MethodGet, route+"?query=private-query", nil)
				request.Header.Set("Cookie", "private-cookie")
				request.Header.Set("X-Request-ID", "safe-request")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != tc.status {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
			}
			records := make([]map[string]any, 0, 4)
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var entry map[string]any
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					t.Fatal(err)
				}
				records = append(records, entry)
			}
			count := 0
			for _, entry := range records {
				if entry["event"] == "security.refusal_summary" {
					continue
				}
				count++
				if entry["level"] != tc.level || entry["component"] != "api" || entry["request_id"] != "safe-request" {
					t.Fatalf("event: %v", entry)
				}
			}
			want := 3
			if tc.level == "WARN" {
				want = 1
			}
			if count != want {
				t.Fatalf("diagnostics=%d want=%d: %s", count, want, logs.String())
			}
			if strings.Contains(logs.String(), "private-") {
				t.Fatalf("private data: %s", logs.String())
			}
			metrics := string(registry.Render(t.Context()))
			for _, reason := range []string{"unauthenticated", "forbidden"} {
				want := 0
				if reason == tc.reason {
					want = 3
				}
				if !strings.Contains(metrics, fmt.Sprintf(`open_agentic_catalog_security_refusals_total{reason="%s"} %d`, reason, want)) {
					t.Fatalf("refusal count %s", reason)
				}
			}
			if tc.reason != "" && len(records) != 4 {
				t.Fatalf("summary duplicated/missing: %s", logs.String())
			}
		})
	}
}

func TestRefusalsStayCountedWithFilteringAndBoundedSummaries(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			var logs bytes.Buffer
			now := time.Unix(10, 0)
			registry := observability.New(nil)
			s := &Server{logger: diagnostics.NewLogger(&logs, level, "json"), now: func() time.Time { return now }, observability: registry}
			router := http.NewServeMux()
			router.HandleFunc("GET /api/v1/catalog", func(w http.ResponseWriter, r *http.Request) {
				writeProblem(w, r, catalog.E(catalog.CodeForbidden, "fixed"))
			})
			handler := s.metricsMiddleware(router, router)
			for i := range 5 {
				if i == 4 {
					now = now.Add(time.Minute)
				}
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/catalog", nil))
			}
			count := strings.Count(logs.String(), "security.refusal_summary")
			if level == "error" {
				if logs.Len() != 0 {
					t.Fatal("filter bypassed")
				}
			} else if count != 2 || !strings.Contains(logs.String(), `"suppressed":3`) {
				t.Fatalf("summaries: %s", logs.String())
			}
			if !strings.Contains(string(registry.Render(t.Context())), `reason="forbidden"} 5`) {
				t.Fatal("filtered counter lost")
			}
		})
	}
}

func TestUnclassifiedStatusAndDiscardedCaptureAreNotRefusals(t *testing.T) {
	registry := observability.New(nil)
	s := &Server{logger: slog.New(slog.DiscardHandler), now: time.Now, observability: registry}
	router := http.NewServeMux()
	for _, status := range []int{401, 403} {
		router.HandleFunc(fmt.Sprintf("GET /raw/%d", status), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	}
	router.HandleFunc("GET /discarded", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(httptest.NewRecorder(), r, catalog.E(catalog.CodeForbidden, "discarded"))
		w.WriteHeader(http.StatusOK)
	})
	handler := s.metricsMiddleware(router, router)
	for _, path := range []string{"/raw/401", "/raw/403", "/discarded"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	}
	metrics := string(registry.Render(t.Context()))
	if !strings.Contains(metrics, `reason="forbidden"} 0`) || !strings.Contains(metrics, `reason="unauthenticated"} 0`) {
		t.Fatal("counted arbitrary status or discarded response")
	}
}

func TestReadinessRecoveryRequiresSuccessfulProbe(t *testing.T) {
	var logs bytes.Buffer
	probeError := errors.New("private-dsn")
	now := time.Unix(10, 0)
	s := &Server{logger: diagnostics.NewLogger(&logs, "info", "json"), now: func() time.Time { return now }, observability: observability.New(nil), readiness: func(context.Context) error { return probeError }}
	s.SetReady(true)
	router := http.NewServeMux()
	router.HandleFunc("GET /readyz", s.handleReady)
	handler := s.metricsMiddleware(router, router)
	call := func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
	}
	call()
	call()
	if strings.Count(logs.String(), "dependency.unavailable") != 1 || strings.Contains(logs.String(), "dependency.recovered") {
		t.Fatalf("probe failures: %s", logs.String())
	}
	probeError = nil
	call()
	call()
	if strings.Count(logs.String(), "dependency.recovered") != 1 || strings.Count(logs.String(), `"level":"INFO"`) != 1 || strings.Contains(logs.String(), "private-") {
		t.Fatalf("probe recovery: %s", logs.String())
	}
}

func TestAbortedRequestReleasesActiveCountAndDoesNotReportWorkflowSuccess(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			registry := observability.New(nil)
			s := &Server{logger: slog.New(slog.DiscardHandler), now: time.Now, observability: registry}
			router := http.NewServeMux()
			router.HandleFunc("POST /api/v1/catalog/search", func(w http.ResponseWriter, _ *http.Request) {
				if committed {
					w.WriteHeader(http.StatusOK)
				}
				panic(http.ErrAbortHandler)
			})
			func() {
				defer func() {
					if recover() != http.ErrAbortHandler { //nolint:errorlint // net/http recognizes this exact panic value, not wrapped errors.
						t.Fatal("abort lost")
					}
				}()
				s.metricsMiddleware(router, router).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/catalog/search", nil))
			}()
			metrics := string(registry.Render(t.Context()))
			if !strings.Contains(metrics, "open_agentic_catalog_http_active_requests 0\n") || !strings.Contains(metrics, `operation="catalog_search",outcome="failed"} 1`) || strings.Contains(metrics, `operation="catalog_search",outcome="succeeded"`) {
				t.Fatal("aborted response changed accounting")
			}
		})
	}
}
