package tracing

import (
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestLogCorrelationRequiresLocalSDKContext(t *testing.T) {
	cause := catalog.TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx := catalog.WithTraceContext(t.Context(), cause)
	if LogAttributes(ctx) != nil || LogAttributes(trace.ContextWithRemoteSpanContext(ctx, decode(cause))) != nil {
		t.Fatal("retained or remote metadata became a local span")
	}
	for _, sampled := range []bool{false, true} {
		sampler := sdk.NeverSample()
		if sampled {
			sampler = sdk.AlwaysSample()
		}
		provider := sdk.NewTracerProvider(sdk.WithSampler(sampler))
		local, span := provider.Tracer("fixture").Start(ctx, "fixed")
		sc := span.SpanContext()
		if got, want := LogAttributes(local), ([]any{"trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String(), "trace_sampled", sampled}); !reflect.DeepEqual(got, want) {
			t.Fatalf("correlation: %v", got)
		}
		span.End()
		if err := provider.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
