// Unit tests exercise isolated validation and controlled dependencies.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

type failingJSONValue struct{}

func (failingJSONValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("sensitive encoding failure")
}

type failingHTTPResponseWriter struct {
	header   http.Header
	status   int
	writeErr error
	short    bool
}

func TestHTTPMetricsUseRouteTemplatesAndBoundedWorkflowOutcomes(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	registry := observability.New(func() time.Time { return now })
	server := &Server{logger: slog.New(slog.DiscardHandler), observability: registry, now: func() time.Time { return now }}
	router := http.NewServeMux()
	router.HandleFunc("POST /api/v1/catalog/search", func(writer http.ResponseWriter, request *http.Request) {
		observeRequestOperation(request, observability.OperationCatalogSearch, observability.OutcomePartial)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"partial":true}`))
	})
	router.HandleFunc("GET /api/v1/resources/{resourceId}", func(writer http.ResponseWriter, request *http.Request) {
		writeProblem(writer, request, catalog.E(catalog.CodeUnauthenticated, "token required"))
	})
	handler := requestIDMiddleware(server.metricsMiddleware(router, router))

	search := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/catalog/search?query=private-search", strings.NewReader(`{}`))
	searchResponse := httptest.NewRecorder()
	handler.ServeHTTP(searchResponse, search)
	privateID := "00000000-0000-7000-8000-000000000099"
	missing := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/resources/"+privateID, nil)
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missing)

	rendered := string(registry.Render(context.Background()))
	for _, expected := range []string{
		`open_agentic_catalog_http_responses_total{method="POST",route="/api/v1/catalog/search",status_class="2xx"} 1`,
		`open_agentic_catalog_http_response_bytes_total{method="POST",route="/api/v1/catalog/search",status_class="2xx"} 16`,
		`open_agentic_catalog_http_responses_total{method="GET",route="/api/v1/resources/{resourceId}",status_class="4xx"} 1`,
		`open_agentic_catalog_operations_total{operation="catalog_search",outcome="partial"} 1`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("metrics omit %q\n%s", expected, rendered)
		}
	}
	for _, private := range []string{privateID, "private-search"} {
		if strings.Contains(rendered, private) {
			t.Errorf("metrics exposed %q\n%s", private, rendered)
		}
	}

	validation := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/version-revisions/00000000-0000-7000-8000-000000000001:validate", nil)
	validation.SetPathValue("revisionCommand", "00000000-0000-7000-8000-000000000001:validate")
	if operation, ok := observedHTTPOperation(validation, "POST /api/v1/version-revisions/{revisionCommand}"); !ok || operation != observability.OperationValidation {
		t.Fatalf("validation operation = %q, %v", operation, ok)
	}
	publicationEnqueue := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/publication/snapshots", nil)
	if operation, ok := observedHTTPOperation(publicationEnqueue, "POST /api/v1/publication/snapshots"); ok {
		t.Fatalf("publication enqueue was mislabeled as completed construction: %q", operation)
	}
}

func (w *failingHTTPResponseWriter) Header() http.Header { return w.header }

func (w *failingHTTPResponseWriter) WriteHeader(status int) { w.status = status }

func (w *failingHTTPResponseWriter) Write(value []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if w.short && len(value) > 0 {
		return len(value) - 1, nil
	}
	return len(value), nil
}

func TestHTTPHealthStateAndResponseWriterBoundaries(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	server := &Server{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	server.handleReady(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status = %d", response.Code)
	}
	server.SetReady(true)
	response = httptest.NewRecorder()
	server.handleReady(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ready status = %d", response.Code)
	}

	capture := newResponseCapture()
	status := &statusWriter{ResponseWriter: capture, status: http.StatusConflict}
	if status.Unwrap() != capture {
		t.Fatal("status writer did not expose its wrapped response writer")
	}
	if !status.resetResponse() || status.status != 0 {
		t.Fatalf("status writer retained status %d after reset", status.status)
	}
	plain := &statusWriter{ResponseWriter: httptest.NewRecorder(), status: http.StatusConflict}
	if plain.resetResponse() || plain.status != http.StatusConflict {
		t.Fatal("status writer reset a response writer without reset support")
	}

	request = httptest.NewRequestWithContext(context.WithValue(context.Background(), requestIDKey{}, "request-encoding"), http.MethodGet, "/api/v1/encoding", nil)
	marshalFailure := httptest.NewRecorder()
	marshalFailure.Header().Set("X-Request-ID", "request-encoding")
	for _, name := range []string{"Content-Digest", "Content-Disposition", "ETag", "Last-Modified", "Link", "Location", "Server-Timing"} {
		marshalFailure.Header().Set(name, "stale-success-value")
	}
	server.respondJSON(marshalFailure, request, http.StatusCreated, failingJSONValue{})
	var problem Problem
	if err := json.Unmarshal(marshalFailure.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if marshalFailure.Code != http.StatusInternalServerError || problem.Code != catalog.CodeInternal || problem.RequestID != "request-encoding" || problem.Detail != safeDetail(nil, catalog.CodeInternal) {
		t.Fatalf("JSON marshal failure response = status %d, problem %#v", marshalFailure.Code, problem)
	}
	if marshalFailure.Header().Get("Content-Type") != "application/problem+json" || marshalFailure.Header().Get("Cache-Control") != "no-store" || marshalFailure.Header().Get("Content-Length") != strconv.Itoa(marshalFailure.Body.Len()) {
		t.Fatalf("JSON marshal failure headers = %#v", marshalFailure.Header())
	}
	for _, name := range []string{"Content-Digest", "Content-Disposition", "ETag", "Last-Modified", "Link", "Location", "Server-Timing"} {
		if value := marshalFailure.Header().Get(name); value != "" {
			t.Fatalf("JSON marshal failure retained %s = %q", name, value)
		}
	}
	if strings.Contains(marshalFailure.Body.String(), "sensitive") || strings.Contains(logs.String(), "sensitive encoding failure") {
		t.Fatalf("encoding failure leaked internal detail: body=%s logs=%s", marshalFailure.Body.String(), logs.String())
	}

	ardFailure := httptest.NewRecorder()
	ardFailure.Header().Set("ETag", `"stale"`)
	server.respondARDJSON(ardFailure, request, "application/ai-catalog+json", failingJSONValue{})
	var publicProblem ardProblem
	if err := json.Unmarshal(ardFailure.Body.Bytes(), &publicProblem); err != nil {
		t.Fatal(err)
	}
	if ardFailure.Code != http.StatusInternalServerError || publicProblem.ErrorCode != catalog.CodeInternal || publicProblem.Message != safeDetail(nil, catalog.CodeInternal) || ardFailure.Header().Get("ETag") != "" || ardFailure.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("public JSON marshal failure = status %d, headers %#v, problem %#v", ardFailure.Code, ardFailure.Header(), publicProblem)
	}

	logSize := logs.Len()
	noContent := httptest.NewRecorder()
	server.respondJSON(noContent, request, http.StatusNoContent, failingJSONValue{})
	if noContent.Code != http.StatusNoContent || noContent.Body.Len() != 0 || noContent.Header().Get("Content-Type") != "" || noContent.Header().Get("Content-Length") != "" || logs.Len() != logSize {
		t.Fatalf("no-content response = status %d, headers %#v, body %q", noContent.Code, noContent.Header(), noContent.Body.String())
	}
	for name, responseWriter := range map[string]*failingHTTPResponseWriter{
		"error": {header: make(http.Header), writeErr: errors.New("write unavailable")},
		"short": {header: make(http.Header), short: true},
	} {
		t.Run("response write "+name, func(t *testing.T) {
			var writeLogs bytes.Buffer
			writeServer := &Server{logger: slog.New(slog.NewTextHandler(&writeLogs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
			writeServer.respondJSON(responseWriter, request, http.StatusOK, map[string]bool{"ok": true})
			if responseWriter.status != http.StatusOK || !strings.Contains(writeLogs.String(), "HTTP response write failed") {
				t.Fatalf("response write failure = status %d, logs %s", responseWriter.status, writeLogs.String())
			}
		})
	}

	sensitiveFailure := httptest.NewRecorder()
	writeProblem(sensitiveFailure, request, catalog.Wrap(catalog.CodeInternal, "database DSN must stay private", errors.New("secret cause")))
	if strings.Contains(sensitiveFailure.Body.String(), "database DSN") || strings.Contains(sensitiveFailure.Body.String(), "secret cause") {
		t.Fatalf("internal problem leaked diagnostics: %s", sensitiveFailure.Body.String())
	}
	if detail := safeDetail(nil, catalog.CodeInvalidArgument); detail != "The request could not be completed." {
		t.Fatalf("nil problem detail = %q", detail)
	}
	if detail := safeDetail(errors.New("plain failure"), catalog.CodeInvalidArgument); detail != "plain failure" {
		t.Fatalf("plain problem detail = %q", detail)
	}

	createdAt := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	if got := snapshotModifiedAt(publication.Snapshot{CreatedAt: createdAt}); !got.Equal(createdAt) {
		t.Fatalf("candidate snapshot modification time = %v", got)
	}
	if err := publicSnapshotError(errors.New("storage unavailable"), "ARD"); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("unexpected public snapshot failure = %v", err)
	}
}

func TestHTTPMetricsClassifyOperationsWithoutSuccessfulOverrideOnErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, pattern, command string
		status                       int
		override, operation          observability.Operation
		outcome                      observability.Outcome
	}{
		{"implicit success", "/api/v1/observations:search", "POST /api/v1/observations:search", "", 0, "", observability.OperationObservationSearch, observability.OutcomeSucceeded},
		{"failure ignores override", "/api/v1/federation/sources:live-search", "POST /api/v1/federation/sources:live-search", "", 503, observability.OperationLiveSearch, observability.OperationLiveSearch, observability.OutcomeFailed},
		{"rejection ignores override", "/api/v1/publication:unpublish", "POST /api/v1/publication:unpublish", "", 403, observability.OperationPublicationChange, observability.OperationPublicationChange, observability.OutcomeRejected},
		{"different operation override", "/api/v1/publication/snapshots/" + projectionItem + ":activate", "POST /api/v1/publication/snapshots/{snapshotCommand}", "", 204, observability.OperationCatalogSearch, observability.OperationPublicationChange, observability.OutcomeSucceeded},
		{"validation", "/api/v1/version-revisions/" + projectionItem + ":validate", "POST /api/v1/version-revisions/{revisionCommand}", projectionItem + ":validate", 200, "", observability.OperationValidation, observability.OutcomeSucceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := observability.New(func() time.Time { return projectionTime })
			s := &Server{observability: registry, now: func() time.Time { return projectionTime }}
			mux := http.NewServeMux()
			mux.HandleFunc(tc.pattern, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected router execution") })
			called := false
			handler := s.metricsMiddleware(mux, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.Pattern != tc.pattern || !strings.Contains(string(registry.Render(r.Context())), "open_agentic_catalog_http_active_requests 1") {
					t.Fatal("request was not observed while active")
				}
				// A nested adapter may replace Pattern; preserve the initial matched route.
				r.Pattern = ""
				if tc.override != "" {
					observeRequestOperation(r, tc.override, observability.OutcomePartial)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
			}))
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tc.path+"?query=private-fixture", nil)
			r.SetPathValue("revisionCommand", tc.command)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			got := string(registry.Render(t.Context()))
			if !called || !strings.Contains(got, `open_agentic_catalog_operations_total{operation="`+string(tc.operation)+`",outcome="`+string(tc.outcome)+`"} 1`) || !strings.Contains(got, "open_agentic_catalog_http_active_requests 0") || strings.Contains(got, "private-fixture") || strings.Contains(got, projectionItem) {
				t.Fatalf("operation metrics: %s", got)
			}
			if strings.Contains(got, `outcome="partial"`) {
				t.Fatal("unrelated or failed operation accepted success override")
			}
		})
	}
}

func TestStatusWriterCountsOnlyAcceptedBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		short bool
		err   error
		want  int
	}{{"complete", false, nil, 4}, {"short", true, nil, 3}, {"failed", false, errors.New("write unavailable"), 0}} {
		t.Run(tc.name, func(t *testing.T) {
			underlying := &failingHTTPResponseWriter{header: make(http.Header), short: tc.short, writeErr: tc.err}
			writer := &statusWriter{ResponseWriter: underlying}
			n, err := writer.Write([]byte("body"))
			if n != tc.want || !errors.Is(err, tc.err) || writer.bytes != int64(tc.want) || writer.status != 200 || !writer.responseCommitted() {
				t.Fatalf("write=%d %v status=%d bytes=%d", n, err, writer.status, writer.bytes)
			}
		})
	}
}
