package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

func TestActivityClassificationUsesMatchedPatternsAndAcceptedMethods(t *testing.T) {
	cases := []struct {
		method, pattern string
		want            observability.HTTPFamily
	}{
		{"POST", "POST /api/v1/catalog/search", observability.FamilyControlSearch},
		{"POST", "POST /api/v1/catalog/explore", observability.FamilyControlExplore},
		{"POST", "POST /api/v1/observations:search", observability.FamilyObservationSearch},
		{"POST", "POST /api/v1/federation/sources:live-search", observability.FamilyAdministrativeLiveSearch},
		{"POST", "POST /ard/v1/search", observability.FamilyARDSearch},
		{"POST", "POST /ard/v1/explore", observability.FamilyARDExplore},
		{"HEAD", "GET /ard/v1/agents", observability.FamilyARDList},
		{"HEAD", "GET /public/v1/artifacts/{artifactId}", observability.FamilyDiscoveryDocuments},
		{"POST", "POST /api/v1/access/capabilities", observability.FamilyControlRead},
		{"POST", "POST /api/v1/artwork/read", observability.FamilyControlRead},
		{"POST", "POST /api/v1/ratings/read", observability.FamilyControlRead},
		{"POST", "POST /api/v1/personal/items/{itemId}/resolve", observability.FamilyControlRead},
		{"POST", "POST /api/v1/federation/sources/{sourceId}/change-previews", observability.FamilyControlCommand},
		{"PATCH", "PATCH /api/v1/resources/{resourceId}", observability.FamilyControlCommand},
		{"HEAD", "GET /api/v1/resources", observability.FamilyControlRead},
		{"HEAD", "GET /api/v1/service-information", observability.FamilyPlatform},
		{"GET", "GET /metrics", observability.FamilyPlatform},
		{"GET", "GET /api/v1/service-activity", observability.FamilyObserver},
		{"GET", "", observability.FamilyAuxiliary},
		{"POST", "GET /api/v1/resources", observability.FamilyAuxiliary},
		{"GET", "/api/v1/resources/private-identifier", observability.FamilyAuxiliary},
		{"OPTIONS", "", observability.FamilyAuxiliary},
		{"GET", "/mcp", observability.FamilyAuxiliary},
		{"POST", "/mcp", observability.FamilyHTTPMCP},
		{"OPTIONS", "/mcp", observability.FamilyHTTPMCP},
		{"HEAD", mcpMetadataPath, observability.FamilyHTTPMCP},
		{"OPTIONS", mcpMetadataPath, observability.FamilyHTTPMCP},
		{"POST", mcpMetadataPath, observability.FamilyAuxiliary},
	}
	for _, tt := range cases {
		t.Run(tt.method+" "+tt.pattern, func(t *testing.T) {
			if got := activityFamily(tt.method, tt.pattern); got != tt.want {
				t.Fatalf("family %d, want %d", got, tt.want)
			}
		})
	}
	// The router determines whether a pattern is real. A plausible raw prefix
	// cannot make a missing route or wrong method become Catalog work.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/resources/{resourceId}", func(http.ResponseWriter, *http.Request) {})
	for _, target := range []struct {
		method, url string
		want        observability.HTTPFamily
	}{
		{"GET", "/api/v1/resources/owned", observability.FamilyControlRead},
		{"HEAD", "/api/v1/resources/owned", observability.FamilyControlRead},
		{"POST", "/api/v1/resources/owned", observability.FamilyAuxiliary},
		{"GET", "/api/v1/does-not-exist", observability.FamilyAuxiliary},
	} {
		_, pattern := mux.Handler(httptest.NewRequestWithContext(t.Context(), target.method, target.url, nil))
		if got := activityFamily(target.method, pattern); got != target.want {
			t.Fatalf("router target %s %s classified %d", target.method, target.url, got)
		}
	}
}

func TestActivityMiddlewareBalancesHeldRefusedCancelledAndPanickingWork(t *testing.T) {
	for _, mode := range []string{"refusal", "cancellation", "panic"} {
		t.Run(mode, func(t *testing.T) {
			registry := observability.New(nil)
			server := &Server{logger: slog.New(slog.DiscardHandler), observability: registry, now: time.Now}
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/v1/resources/{resourceId}", func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
				if mode == "panic" {
					panic("private-exception-sentinel")
				}
				if r.Context().Err() != nil {
					w.WriteHeader(http.StatusRequestTimeout)
					return
				}
				w.WriteHeader(http.StatusForbidden)
			})
			handler := server.metricsMiddleware(mux, recoverPanics(server.logger, mux))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := httptest.NewRequestWithContext(ctx, "GET", "/api/v1/resources/private-resource?text=private-query", nil)
			response := httptest.NewRecorder()
			go func() { defer close(done); handler.ServeHTTP(response, request) }()
			<-entered
			active, err := registry.Activity(false)
			if err != nil || active.HTTP.ActiveCatalog != 1 || active.HTTP.Completed != 0 {
				t.Error("held work not visible", err)
			}
			if mode == "cancellation" {
				cancel()
			}
			releaseOnce.Do(func() { close(release) })
			<-done
			sample, err := registry.Activity(false)
			want := 403
			if mode == "panic" {
				want = 500
			} else if mode == "cancellation" {
				want = 408
			}
			if err != nil || response.Code != want || sample.HTTP.ActiveCatalog != 0 || sample.HTTP.Completed != 1 || sample.HTTP.Families[observability.FamilyControlRead].Completed != 1 {
				t.Fatalf("unbalanced outcome %d %+v %v", response.Code, sample.HTTP, err)
			}
			raw, err := json.Marshal(sample)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"private-resource", "private-query", "private-exception-sentinel", "/api/v1/"} {
				if bytes.Contains(raw, []byte(private)) {
					t.Fatalf("sample leaked %s", private)
				}
			}
		})
	}
}
