// Domain tracing carries private causal identifiers through narrow context
// ports while keeping telemetry separate from identity and audit authority.

package catalog

import (
	"context"
	"encoding/hex"
	"strings"
)

// TraceContext is private causal metadata, never an actor, public payload or
// audit fact. Only the fixed W3C version-00 identifiers and sampled bit survive.
type TraceContext string

// Valid reports whether the value is the accepted lowercase W3C version-00
// traceparent shape with nonzero identifiers.
func (t TraceContext) Valid() bool {
	s := string(t)
	if len(s) != 55 || s[:3] != "00-" || s[35] != '-' || s[52] != '-' || (s[53:] != "00" && s[53:] != "01") {
		return false
	}
	for _, part := range []string{s[3:35], s[36:52]} {
		if part == strings.Repeat("0", len(part)) || strings.ToLower(part) != part {
			return false
		}
		if _, err := hex.DecodeString(part); err != nil {
			return false
		}
	}
	return true
}

type TraceKind uint8

const (
	TraceInternal TraceKind = iota
	TraceServer
	TraceClient
	TraceAttempt
	TraceProducer
)

// Tracer is the narrow domain tracing port. Names and outcomes are supplied by
// code, not request data. Attempt spans start a new trace linked to cause.
type Tracer interface {
	Start(context.Context, string, TraceKind, TraceContext) (context.Context, func(string))
}

type tracerKey struct{}
type causalTraceKey struct{}

// WithoutTracing suppresses background maintenance spans without dropping the
// caller's cancellation, deadline, identity or execution attribution.
func WithoutTracing(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, tracerKey{}, false)
	return context.WithValue(ctx, causalTraceKey{}, TraceContext(""))
}

// WithTracer attaches the process tracing port; a nil tracer leaves ctx
// unchanged.
func WithTracer(ctx context.Context, tracer Tracer) context.Context {
	if tracer == nil {
		return ctx
	}
	return context.WithValue(ctx, tracerKey{}, tracer)
}

// WithTraceContext attaches valid private causal metadata and ignores an
// invalid value.
func WithTraceContext(ctx context.Context, value TraceContext) context.Context {
	if !value.Valid() {
		return ctx
	}
	return context.WithValue(ctx, causalTraceKey{}, value)
}

// CausalTrace returns the private causal metadata attached to ctx, or an empty
// value.
func CausalTrace(ctx context.Context) TraceContext {
	value, _ := ctx.Value(causalTraceKey{}).(TraceContext)
	return value
}

// StartTrace is allocation-free when no process tracer is attached. Attaching
// tracing never changes cancellation, deadlines or identity context.
func StartTrace(ctx context.Context, name string, kind TraceKind, cause TraceContext) (context.Context, func(string)) {
	if (kind == TraceInternal || kind == TraceClient) && !CausalTrace(ctx).Valid() {
		return ctx, ignoreTraceOutcome
	}
	if tracer, ok := ctx.Value(tracerKey{}).(Tracer); ok {
		return tracer.Start(ctx, name, kind, cause)
	}
	return ctx, ignoreTraceOutcome
}

// ignoreTraceOutcome makes the disabled tracing completion callback safe to
// call unconditionally.
func ignoreTraceOutcome(string) {
	// No span was started, so completion has nothing to record.
}
