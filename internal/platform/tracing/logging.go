package tracing

import (
	"context"
	"go.opentelemetry.io/otel/trace"
)

// LogAttributes projects only an active local span, including unsampled spans.
// Retained or incoming causality alone does not establish local sampling.
func LogAttributes(ctx context.Context) []any {
	span := trace.SpanContextFromContext(ctx)
	if !span.IsValid() || span.IsRemote() {
		return nil
	}
	return []any{"trace_id", span.TraceID().String(), "span_id", span.SpanID().String(), "trace_sampled", span.IsSampled()}
}
