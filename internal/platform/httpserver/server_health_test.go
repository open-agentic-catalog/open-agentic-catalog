package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
)

func TestReadinessProbeDeadlineAndMetrics(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ready", "failed", "parent deadline", "memory"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			registry := observability.New(func() time.Time { return projectionTime })
			if mode != "memory" {
				registry.SetDependency("postgresql", false)
			}
			ctx := context.WithValue(t.Context(), requestIDKey{}, "readiness-request")
			deadline := time.Now().Add(-time.Second)
			if mode == "parent deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, deadline)
				defer cancel()
			}
			var probeError func() error
			calls := 0
			start := time.Now()
			s := &Server{now: time.Now, logger: slog.New(slog.NewJSONHandler(&logs, nil)), observability: registry, readiness: func(c context.Context) error {
				probeError = c.Err
				calls++
				limit, ok := c.Deadline()
				if !ok || limit.After(time.Now().Add(2*time.Second)) || c.Value(requestIDKey{}) != "readiness-request" {
					t.Fatal("probe lost deadline or context")
				}
				if mode == "parent deadline" {
					if !limit.Equal(deadline) || !errors.Is(c.Err(), context.DeadlineExceeded) {
						t.Fatal("probe did not retain parent deadline")
					}
					return c.Err()
				}
				if limit.Before(start.Add(2 * time.Second)) {
					t.Fatal("probe deadline shortened unexpectedly")
				}
				if mode == "failed" {
					return errors.New("private-fixture-dsn")
				}
				return nil
			}}
			s.SetReady(true)
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", nil)
			w := httptest.NewRecorder()
			s.handleReady(w, r)
			if calls != 1 || probeError == nil || probeError() == nil {
				t.Fatal("probe must run once and release its context")
			}
			outcome := "succeeded"
			ready := "1"
			if mode == "failed" || mode == "parent deadline" {
				code := catalog.CodeDependencyUnavailable
				if mode == "parent deadline" {
					code = catalog.CodeRequestDeadlineExceeded
				}
				requireProjectionProblem(t, w, 503, code)
				outcome = "failed"
				ready = "0"
				if !strings.Contains(logs.String(), "readiness dependency check failed") {
					t.Fatal("missing safe failure log")
				}
			} else {
				requireControlJSON(t, w, 200, serviceStatusResponse{Status: "ready"})
			}
			metrics := string(registry.Render(t.Context()))
			if !strings.Contains(metrics, `open_agentic_catalog_operations_total{operation="readiness",outcome="`+outcome+`"} 1`) {
				t.Fatalf("readiness observation missing: %s", metrics)
			}
			if mode == "memory" {
				if strings.Contains(metrics, `dependency="postgresql"`) {
					t.Fatal("memory readiness invented a database")
				}
			} else if !strings.Contains(metrics, `open_agentic_catalog_dependency_ready{dependency="postgresql"} `+ready) {
				t.Fatalf("dependency state: %s", metrics)
			}
			if strings.Contains(logs.String(), "private-fixture") || strings.Contains(w.Body.String(), "private-fixture") {
				t.Fatal("probe leaked cause")
			}
			r = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
			s.SetReady(false)
			w = httptest.NewRecorder()
			s.handleReady(w, r)
			requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
			if calls != 1 {
				t.Fatal("draining readiness contacted a dependency")
			}
			w = httptest.NewRecorder()
			s.handleLive(w, r)
			requireControlJSON(t, w, 200, serviceStatusResponse{Status: "live"})
			if calls != 1 {
				t.Fatal("liveness contacted a dependency")
			}
		})
	}
}

func TestMetricsHandlerProjection(t *testing.T) {
	t.Parallel()
	registry := observability.New(func() time.Time { return projectionTime })
	registry.SetDependency("postgresql", true)
	s := &Server{observability: registry}
	w := httptest.NewRecorder()
	s.handleMetrics(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "text/plain; version=0.0.4" || !strings.Contains(w.Body.String(), `open_agentic_catalog_dependency_ready{dependency="postgresql"} 1`) || !strings.Contains(w.Body.String(), "open_agentic_catalog_http_active_requests 0") {
		t.Fatalf("metrics response %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

// Compact information must work even when every dependency is unavailable.
func TestServiceInformationIsCoherentAndLocal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		process, err := processinfo.New()
		if err != nil {
			t.Fatal(err)
		}
		s := &Server{process: process, role: "all", serviceVersion: "1.2.3", commit: "build-sha", sourceState: "clean", ardProfile: "ard", aiCatalogProfile: "ai-catalog", readiness: func(context.Context) error { t.Fatal("compact information probed readiness"); return nil }}
		// No services or registry are initialized. Access to either would panic.
		time.Sleep(4500 * time.Millisecond)
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/service-information", nil)
		r.Header.Set("Authorization", "Bearer reader")
		w := httptest.NewRecorder()
		// A controlled authenticated reader has no OAuth scopes. The information
		// read must remain available when the scope middleware is enabled.
		tokens := discoveryAuthenticator{"reader": {TenantID: projectionTenant, Roles: []identity.Role{identity.RoleReader}}}
		handler := authentication(tokens, authorizationScopes(true, http.HandlerFunc(s.handleServiceInformation)))
		handler.ServeHTTP(w, r)
		var got serviceInformationResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || got.InstanceID != process.ID() || got.UptimeSeconds != 4 || !got.StartedAt.Equal(process.Read().StartedAt) || !got.ObservedAt.Equal(process.Read().ObservedAt) || got.Role != "all" || got.Version != "1.2.3" || got.Commit != "build-sha" || got.SourceState != "clean" || got.Service != "open-agentic-catalog" || got.ARDProfile != "ard" || got.AICatalogProfile != "ai-catalog" {
			t.Fatalf("incoherent information: %d %s", w.Code, w.Body.String())
		}
		old := httptest.NewRecorder()
		s.handleVersion(old, r)
		var fields map[string]any
		if err := json.Unmarshal(old.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 6 || fields["instanceId"] != nil {
			t.Fatal("static version response changed")
		}
		// A protected read remains refused; the successful information read does
		// not result from disabling scope enforcement for every route.
		guard := authentication(tokens, authorizationScopes(true, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("unscoped protected read reached its handler")
		})))
		protected := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/catalog/resources", nil)
		protected.Header.Set("Authorization", "Bearer reader")
		denied := httptest.NewRecorder()
		guard.ServeHTTP(denied, protected)
		if denied.Code != http.StatusForbidden || denied.Header().Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="catalog.read"` {
			t.Fatalf("scope enforcement lost: %d %s", denied.Code, denied.Body.String())
		}
	})
}
