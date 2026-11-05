//go:build functional

package httpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

func TestActivityRealHTTPDisconnectReleasesOnlyCatalogWork(t *testing.T) {
	registry := observability.New(nil)
	server := &Server{observability: registry, logger: slog.New(slog.DiscardHandler), now: time.Now}
	entered, finished := make(chan struct{}), make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/resources/{resourceId}", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	})
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	wrapped := server.metricsMiddleware(mux, mux)
	actual := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/resources/held" {
			defer close(finished)
		}
		wrapped.ServeHTTP(w, r)
	}))
	defer actual.Close()
	client := actual.Client()
	client.Timeout = 5 * time.Second
	for _, target := range []string{"/livez", "/missing"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		req.RequestURI = ""
		req.URL.Scheme = "http"
		req.URL.Host = actual.Listener.Addr().String()
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, actual.URL+"/api/v1/resources/held", nil)
	if err != nil {
		t.Fatal(err)
	}
	outcome := make(chan error, 1)
	go func() {
		response, err := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		outcome <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not enter handler")
	}
	before, err := registry.Activity(false)
	if err != nil || before.HTTP.ActiveCatalog != 1 || before.HTTP.Completed != 2 || before.HTTP.Families[observability.FamilyPlatform].Completed != 1 || before.HTTP.Families[observability.FamilyAuxiliary].Completed != 1 {
		t.Fatalf("wrong held sample: %+v %v", before.HTTP, err)
	}
	cancel()
	select {
	case err := <-outcome:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("client cancellation not observed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not cancel")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not unwind")
	}
	after, err := registry.Activity(false)
	row := after.HTTP.Families[observability.FamilyControlRead]
	if err != nil || after.HTTP.ActiveCatalog != 0 || after.HTTP.Completed != 3 || row.Completed != 1 || row.StatusClasses[3].Completed != 1 {
		t.Fatalf("disconnect lost or duplicated completion: %+v %v", after.HTTP, err)
	}
}

func TestActivityRealHTTPBodyCanFinishBeforeMiddleware(t *testing.T) {
	registry := observability.New(nil)
	server := &Server{observability: registry, logger: slog.New(slog.DiscardHandler), now: time.Now}
	release := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/resources/{resourceId}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "8192")
		_, _ = io.WriteString(w, strings.Repeat("x", 8192))
		<-release
	})
	actual := httptest.NewServer(server.metricsMiddleware(mux, mux))
	defer actual.Close()
	defer once.Do(func() { close(release) })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, actual.URL+"/api/v1/resources/held", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := actual.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(raw) != strings.Repeat("x", 8192) {
		t.Fatal("body did not complete before release", string(raw), err)
	}
	held, err := registry.Activity(false)
	if err != nil || held.HTTP.Completed != 0 || held.HTTP.ActiveCatalog != 1 {
		t.Fatal("body completion was confused with middleware completion", held.HTTP, err)
	}
	once.Do(func() { close(release) })
	finished := activityAfterCompletion(t, registry, 1)
	if finished.HTTP.ActiveCatalog != 0 || finished.HTTP.Families[observability.FamilyControlRead].Completed != 1 {
		t.Fatal("middleware completion was not observed", finished.HTTP)
	}
}
