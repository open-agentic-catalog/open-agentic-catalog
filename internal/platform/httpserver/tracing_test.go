package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type ingressTrace struct {
	name, outcome string
	cause         catalog.TraceContext
	calls, ends   int
}

func (s *ingressTrace) Start(ctx context.Context, name string, kind catalog.TraceKind, cause catalog.TraceContext) (context.Context, func(string)) {
	s.name, s.cause = name, cause
	s.calls++
	return ctx, func(outcome string) { s.outcome = outcome; s.ends++ }
}

func TestIngressTraceFinalStatusAndPanicOutcome(t *testing.T) {
	for _, tc := range []struct {
		status  int
		panics  bool
		outcome string
	}{
		{0, false, "ok"}, {204, false, "ok"}, {399, false, "ok"},
		{400, false, "4xx"}, {499, false, "4xx"},
		{500, false, "5xx"}, {599, false, "5xx"},
		{0, true, "error"}, {200, true, "error"}, {403, true, "error"},
	} {
		t.Run(fmt.Sprintf("status=%d/panic=%t", tc.status, tc.panics), func(t *testing.T) {
			tracer := new(ingressTrace)
			router := http.NewServeMux()
			router.HandleFunc("GET /api/v1/resources/{id}", func(w http.ResponseWriter, _ *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				if tc.panics {
					panic("private-fault")
				}
			})
			response := httptest.NewRecorder()
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				traceHTTP(tracer, router, router).ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/resources/private-id?query=private-value", nil))
			}()
			if tc.panics && recovered != "private-fault" || !tc.panics && recovered != nil {
				t.Fatal("tracing changed the panic recovery owner's input")
			}
			if tracer.calls != 1 || tracer.ends != 1 || tracer.outcome != tc.outcome || tracer.name != "HTTP GET /api/v1/resources/{id}" {
				t.Fatalf("trace classification: %#v", tracer)
			}
			status := tc.status
			if status == 0 {
				status = 200
			}
			if response.Code != status || response.Body.Len() != 0 {
				t.Fatal("trace observation modified the response")
			}
		})
	}
}

func TestIngressTraceUsesOnlyRouteTemplatesAndBoundedParent(t *testing.T) {
	parent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	for _, tc := range []struct {
		headers []string
		want    catalog.TraceContext
	}{
		{[]string{parent}, catalog.TraceContext(parent)},
		{[]string{parent[:53] + "fe"}, catalog.TraceContext(parent[:53] + "00")},
		{[]string{parent[:53] + "ff"}, catalog.TraceContext(parent)},
		{[]string{parent, parent}, ""},
		{[]string{parent + "," + parent}, ""},
		{[]string{strings.Repeat("a", 4096)}, ""},
		{[]string{"00-00000000000000000000000000000000-00f067aa0ba902b7-01"}, ""},
		{[]string{"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"}, ""},
		{[]string{"invalid"}, ""}, {nil, ""},
	} {
		tracer := new(ingressTrace)
		router := http.NewServeMux()
		router.HandleFunc("GET /api/v1/resources/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
		router.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		var admitted string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { admitted = traceID(r.Context()); router.ServeHTTP(w, r) })
		handler := requestIDMiddleware(traceHTTP(tracer, router, next))
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/resources/private-id?query=private-value", nil)
		request.Header["Traceparent"] = tc.headers
		request.Header.Set("Tracestate", "private=secret")
		request.Header.Set("Baggage", "private=secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 403 || tracer.name != "HTTP GET /api/v1/resources/{id}" || tracer.outcome != "4xx" {
			t.Fatalf("incorrect ingress trace: %#v", tracer)
		}
		wantID := ""
		if tc.want != "" {
			wantID = string(tc.want)[3:35]
		}
		if tracer.cause != tc.want || admitted != wantID {
			t.Fatal("invalid parent selection")
		}
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
		if tracer.calls != 1 {
			t.Fatal("health traffic traced")
		}
	}
}

func TestTraceparentBrowserPreflightPreservesOriginBoundary(t *testing.T) {
	for _, origin := range []string{"https://allowed.example", "https://denied.example"} {
		handler := cors([]string{"https://allowed.example"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/resources", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "GET")
		req.Header.Set("Access-Control-Request-Headers", "traceparent")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if origin == "https://allowed.example" {
			if res.Code != 204 || !strings.Contains(res.Header().Get("Access-Control-Allow-Headers"), "Traceparent") {
				t.Fatal("approved preflight denied")
			}
		} else if res.Code != 404 || res.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("denied origin allowed")
		}
	}
}
