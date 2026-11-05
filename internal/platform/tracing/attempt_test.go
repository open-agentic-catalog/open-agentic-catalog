// Attempt units verify recorded span relationships and private outcome projection
// without constructing an OTLP exporter or contacting a collector.
package tracing

import (
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"go.opentelemetry.io/otel/codes"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestLinkedAttemptClientParentageAndSafeOutcome(t *testing.T) {
	for _, tc := range []struct {
		outcome, want string
		status        codes.Code
	}{
		{"failed", "failed", codes.Error},
		{"dead_letter", "dead_letter", codes.Error},
		{"unsupported", "unsupported", codes.Error},
		{"waiting", "waiting", codes.Unset},
		{"succeeded", "succeeded", codes.Unset},
		{"cancelled", "cancelled", codes.Unset},
		{"private-outcome-secret", "other", codes.Unset},
	} {
		t.Run(tc.want, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdk.NewTracerProvider(sdk.WithSpanProcessor(recorder), sdk.WithSampler(sdk.AlwaysSample()))
			defer func() { _ = provider.Shutdown(t.Context()) }()
			p := &Provider{provider: provider, tracer: provider.Tracer("fixture")}
			ambient, endAmbient := p.Start(t.Context(), "request", catalog.TraceServer, "")
			const cause = catalog.TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00")
			name := strings.Repeat("j", 161)
			attemptCtx, endAttempt := p.Start(ambient, name, catalog.TraceAttempt, cause)
			childCtx, endChild := catalog.StartTrace(attemptCtx, "HTTP outbound", catalog.TraceClient, "")
			if !catalog.CausalTrace(attemptCtx).Valid() || !catalog.CausalTrace(childCtx).Valid() {
				t.Fatal("local spans lost domain causal context")
			}
			endChild("ok")
			endAttempt(tc.outcome)
			endAmbient("ok")
			spans := recorder.Ended()
			if len(spans) != 3 {
				t.Fatalf("ended spans=%d", len(spans))
			}
			child, attempt, root := spans[0], spans[1], spans[2]
			if child.SpanKind() != trace.SpanKindClient || !child.Parent().Equal(attempt.SpanContext()) {
				t.Fatal("client child did not attach to attempt")
			}
			if attempt.SpanKind() != trace.SpanKindConsumer || attempt.Parent().IsValid() || attempt.SpanContext().TraceID() == root.SpanContext().TraceID() || attempt.Name() != strings.Repeat("j", 160) {
				t.Fatal("attempt inherited ambient parentage or lost its bounded name")
			}
			links := attempt.Links()
			if len(links) != 1 {
				t.Fatalf("attempt links=%d", len(links))
			}
			linked := links[0].SpanContext
			if linked.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" || linked.SpanID().String() != "00f067aa0ba902b7" || !linked.IsRemote() || linked.IsSampled() || len(links[0].Attributes) != 0 {
				t.Fatal("attempt did not preserve its unsampled remote cause as a link")
			}
			attrs := attempt.Attributes()
			if len(attrs) != 1 || string(attrs[0].Key) != "catalog.outcome" || attrs[0].Value.AsString() != tc.want || attempt.Status().Code != tc.status || attempt.Status().Description != "" || len(attempt.Events()) != 0 {
				t.Fatal("attempt outcome retained private details or lost classification")
			}
		})
	}
}
