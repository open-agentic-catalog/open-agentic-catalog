package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"go.opentelemetry.io/otel/trace"
)

func TestRequestLogAttributesUseActiveTraceAndBoundedRoute(t *testing.T) {
	t.Parallel()
	const incomingTrace = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const activeTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	const activeSpan = "00f067aa0ba902b7"
	for _, test := range []struct {
		name   string
		cause  catalog.TraceContext
		legacy string
		want   []any
	}{
		{"request only", "", "", []any{"request_id", "request-one"}},
		{"incoming trace", "", incomingTrace, []any{"request_id", "request-one", "trace_id", incomingTrace}},
		{"active span takes precedence", catalog.TraceContext("00-" + activeTrace + "-" + activeSpan + "-01"), incomingTrace, []any{"request_id", "request-one", "trace_id", activeTrace, "span_id", activeSpan, "trace_sampled", true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.WithValue(t.Context(), requestIDKey{}, "request-one")
			ctx = context.WithValue(ctx, traceIDKey{}, test.legacy)
			ctx = catalog.WithTraceContext(ctx, "00-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-cccccccccccccccc-01")
			if test.cause != "" {
				traceID, _ := trace.TraceIDFromHex(activeTrace)
				spanID, _ := trace.SpanIDFromHex(activeSpan)
				ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled}))
			}
			request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/resources/private-id?query=private", nil)
			request.Header.Set("Authorization", "Bearer private-token")
			request.Pattern = "GET /api/v1/resources/{resourceId}"
			got := requestLogAttributes(request, "status", http.StatusOK)
			want := slices.Clone(test.want)
			want = append(want, "method", http.MethodGet, "route", "/api/v1/resources/{resourceId}", "status", http.StatusOK)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("log attributes = %#v, want %#v", got, want)
			}
		})
	}
}

func TestRequestLogAttributesBoundUnknownMethodsAndPatterns(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodPut, http.MethodConnect, "PRIVATE-METHOD"} {
		for _, pattern := range []string{"", "relative-path", "/invalid\npattern", "/" + strings.Repeat("a", 180)} {
			request := httptest.NewRequestWithContext(t.Context(), method, "/private-path?token=private", nil)
			request.Pattern = pattern
			want := []any{"request_id", "", "method", "OTHER", "route", "unmatched"}
			if got := requestLogAttributes(request); !reflect.DeepEqual(got, want) {
				t.Fatalf("method=%q pattern=%q produced %#v, want %#v", method, pattern, got, want)
			}
		}
	}
}
