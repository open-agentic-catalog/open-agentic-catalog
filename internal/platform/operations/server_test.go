package operations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
)

type webhookSnapshotProvider struct {
	failed bool
	err    error
	calls  int
}

func (p *webhookSnapshotProvider) OperationalSnapshot(context.Context, time.Time) (observability.OperationalSnapshot, error) {
	p.calls++
	result := observability.OperationalSnapshot{}
	if p.failed {
		result.Webhooks.LastAttemptFailedReceivers = 1
	}
	return result, p.err
}

func TestWebhookDegradationIsStatusOnly(t *testing.T) {
	registry := observability.New(nil)
	registry.SetLifecycle(observability.LifecycleRunning)
	registry.SetComponent(observability.ComponentWebhookDelivery, observability.ComponentRunning)
	provider := &webhookSnapshotProvider{failed: true}
	registry.SetSnapshotProvider(provider)
	server, err := New(Dependencies{Observability: registry, Readiness: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string) statusResponse {
		t.Helper()
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		var result statusResponse
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatal("invalid health response")
		}
		if strings.Contains(response.Body.String(), "secret") {
			t.Fatal("private snapshot error leaked")
		}
		return result
	}
	for _, fault := range []error{nil, errors.New("private secret snapshot failure")} {
		provider.err = fault
		before := provider.calls
		read("/livez")
		read("/readyz")
		if provider.calls != before {
			t.Fatal("receiver snapshot became health dependency")
		}
		status := read("/status")
		if !status.Degraded || status.Ready == nil || !*status.Ready || status.Components[observability.ComponentWebhookDelivery] != observability.ComponentDegraded {
			t.Fatal("receiver degradation not visible independently of readiness")
		}
	}
	provider.err, provider.failed = nil, false
	if read("/status").Degraded {
		t.Fatal("recovered receiver remained degraded")
	}
}

func TestWorkerOperationsHealthLifecycleAndIsolation(t *testing.T) {
	registry := observability.New(nil)
	registry.SetDependency("postgresql", true)
	readinessErr := error(nil)
	server, err := New(Dependencies{
		Observability:               registry,
		Readiness:                   func(context.Context) error { return readinessErr },
		Version:                     "1.2.3",
		Commit:                      "abc123",
		SourceState:                 "modified",
		HousekeepingInterval:        5 * time.Minute,
		TerminalJobRetention:        168 * time.Hour,
		AcknowledgedOutboxRetention: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), method, path, nil))
		for name, expected := range map[string]string{
			"Cache-Control":           "no-store",
			"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
			"X-Content-Type-Options":  "nosniff",
			"X-Frame-Options":         "DENY",
		} {
			if actual := response.Header().Get(name); actual != expected {
				t.Errorf("%s %s header %s = %q, want %q", method, path, name, actual, expected)
			}
		}
		return response
	}
	decode := func(response *httptest.ResponseRecorder) statusResponse {
		t.Helper()
		var status statusResponse
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		return status
	}

	startup := request(http.MethodGet, "/startupz")
	if status := decode(startup); startup.Code != http.StatusOK || status.Status != "started" || status.State != observability.LifecycleStarting || status.Ready != nil {
		t.Fatalf("startup response = %d %#v", startup.Code, status)
	}
	notReady := request(http.MethodGet, "/readyz")
	if status := decode(notReady); notReady.Code != http.StatusServiceUnavailable || status.Status != "not_ready" || status.Ready == nil || *status.Ready {
		t.Fatalf("starting readiness = %d %#v", notReady.Code, status)
	}

	registry.SetLifecycle(observability.LifecycleRunning)
	registry.SetComponent(observability.ComponentWorker, observability.ComponentRunning)
	registry.SetComponent(observability.ComponentOutbox, observability.ComponentDegraded)
	ready := request(http.MethodGet, "/readyz")
	if status := decode(ready); ready.Code != http.StatusOK || status.Status != "ready" || status.Ready == nil || !*status.Ready || !status.Degraded {
		t.Fatalf("ready response = %d %#v", ready.Code, status)
	}
	statusResult := request(http.MethodGet, "/status")
	status := decode(statusResult)
	if statusResult.Code != http.StatusOK || status.Status != "degraded" || status.Ready == nil || !*status.Ready || !status.Degraded || status.Version != "1.2.3" || status.Commit != "abc123" || status.Components[observability.ComponentOutbox] != observability.ComponentDegraded || !status.Dependencies["postgresql"] || status.Housekeeping == nil || status.Housekeeping.Interval != "5m0s" || status.Housekeeping.TerminalJobRetention != "168h0m0s" || status.Housekeeping.AcknowledgedOutboxRetention != "24h0m0s" || status.Housekeeping.BatchLimit != 100 {
		t.Fatalf("status response = %d %#v", statusResult.Code, status)
	}
	version := decode(request(http.MethodGet, "/version"))
	if version.Version != "1.2.3" || version.Commit != "abc123" || version.SourceState != "modified" || status.SourceState != "modified" || version.Role != "worker" {
		t.Fatalf("version response = %#v", version)
	}
	if live := request(http.MethodGet, "/livez"); live.Code != http.StatusOK || decode(live).Status != "live" || decode(live).Ready != nil {
		t.Fatalf("liveness response = %d %s", live.Code, live.Body.String())
	}

	readinessErr = errors.New("postgres://user:secret@database/catalog")
	unavailable := request(http.MethodGet, "/readyz")
	if unavailableStatus := decode(unavailable); unavailable.Code != http.StatusServiceUnavailable || unavailableStatus.Ready == nil || *unavailableStatus.Ready {
		t.Fatalf("dependency readiness = %d %s", unavailable.Code, unavailable.Body.String())
	}
	status = decode(request(http.MethodGet, "/status"))
	if status.Ready == nil || *status.Ready || !status.Degraded || status.Dependencies["postgresql"] {
		t.Fatalf("dependency status = %#v", status)
	}
	if strings.Contains(unavailable.Body.String(), "secret") || strings.Contains(unavailable.Body.String(), "postgres://") {
		t.Fatalf("readiness exposed dependency detail: %s", unavailable.Body.String())
	}

	readinessErr = nil
	if recovered := request(http.MethodGet, "/readyz"); recovered.Code != http.StatusOK {
		t.Fatalf("recovered readiness = %d %s", recovered.Code, recovered.Body.String())
	}
	registry.SetLifecycle(observability.LifecycleDraining)
	if draining := request(http.MethodGet, "/readyz"); draining.Code != http.StatusServiceUnavailable || decode(draining).State != observability.LifecycleDraining {
		t.Fatalf("draining readiness = %d %s", draining.Code, draining.Body.String())
	}
	if live := request(http.MethodGet, "/livez"); live.Code != http.StatusOK {
		t.Fatalf("draining liveness = %d", live.Code)
	}

	metrics := request(http.MethodGet, "/metrics")
	if metrics.Code != http.StatusOK || metrics.Header().Get("Content-Type") != "text/plain; version=0.0.4" || !strings.Contains(metrics.Body.String(), `open_agentic_catalog_process_state{state="draining"} 1`) || !strings.Contains(metrics.Body.String(), `open_agentic_catalog_http_responses_total{method="GET",route="/readyz",status_class="5xx"} 3`) || strings.Contains(metrics.Body.String(), "secret") {
		t.Fatalf("metrics response = %d headers=%v body=%s", metrics.Code, metrics.Header(), metrics.Body.String())
	}
	if product := request(http.MethodGet, "/api/v1/capabilities"); product.Code != http.StatusNotFound {
		t.Fatalf("operations listener served product route with %d", product.Code)
	}
	if wrongMethod := request(http.MethodPost, "/readyz"); wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("operations listener accepted POST readiness with %d", wrongMethod.Code)
	}
	if queried := request(http.MethodGet, "/readyz?detail=all"); queried.Code != http.StatusBadRequest || decode(queried).Status != "invalid_request" {
		t.Fatalf("operations listener accepted query with %d %s", queried.Code, queried.Body.String())
	}
	if emptyQuery := request(http.MethodGet, "/readyz?"); emptyQuery.Code != http.StatusBadRequest || decode(emptyQuery).Status != "invalid_request" {
		t.Fatalf("operations listener accepted an empty query marker with %d %s", emptyQuery.Code, emptyQuery.Body.String())
	}
	if escapedPath := request(http.MethodGet, "/read%79z"); escapedPath.Code != http.StatusBadRequest || decode(escapedPath).Status != "invalid_request" {
		t.Fatalf("operations listener accepted an encoded probe path with %d %s", escapedPath.Code, escapedPath.Body.String())
	}
}

func TestWorkerOperationsDoesNotInventPostgreSQLForMemoryReadiness(t *testing.T) {
	registry := observability.New(nil)
	registry.SetLifecycle(observability.LifecycleRunning)
	server, err := New(Dependencies{Observability: registry, Readiness: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/status", nil))
	status := statusResponse{}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || status.Ready == nil || !*status.Ready || len(status.Dependencies) != 0 {
		t.Fatalf("memory-backed status = %d %#v", response.Code, status)
	}
	if rendered := string(registry.Render(context.Background())); strings.Contains(rendered, `dependency="postgresql"`) {
		t.Fatalf("memory-backed metrics invented PostgreSQL readiness: %s", rendered)
	}
}

func TestWorkerOperationsRequiresDependencies(t *testing.T) {
	registry := observability.New(nil)
	for _, dependencies := range []Dependencies{
		{},
		{Observability: registry},
		{Readiness: func(context.Context) error { return nil }},
	} {
		if _, err := New(dependencies); err == nil {
			t.Fatalf("incomplete dependencies %#v were accepted", dependencies)
		}
	}
}

func TestWorkerStatusSharesProcessIdentityWithoutChangingVersion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		process, err := processinfo.New()
		if err != nil {
			t.Fatal(err)
		}
		registry := observability.New(time.Now, process)
		registry.SetLifecycle(observability.LifecycleRunning)
		calls := 0
		server, err := New(Dependencies{Process: process, Observability: registry, Readiness: func(context.Context) error { calls++; return errors.New("private database cause") }, Version: "worker-build", Commit: "worker-sha", SourceState: "clean"})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		read := func(path string) statusResponse {
			t.Helper()
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", path, nil))
			var got statusResponse
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private database cause") {
				t.Fatalf("status: %d %s", w.Code, w.Body.String())
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			return got
		}
		got := read("/status")
		if got.Snapshot == nil || got.InstanceID != process.ID() || got.UptimeSeconds != 3 || got.Role != "worker" || got.Version != "worker-build" || got.Commit != "worker-sha" || got.Ready == nil || *got.Ready || !got.Degraded || calls != 1 {
			t.Fatalf("worker status: %#v calls=%d", got, calls)
		}
		if got := read("/version"); got.Snapshot != nil || calls != 1 {
			t.Fatal("static version changed or probed database")
		}
	})
}
