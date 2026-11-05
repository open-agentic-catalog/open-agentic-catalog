package tracing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"go.opentelemetry.io/otel/codes"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type failingShutdownProcessor struct {
	*tracetest.SpanRecorder
}

func (failingShutdownProcessor) Shutdown(context.Context) error {
	return errors.New("private exporter failure")
}

type idleConnectionRecorder struct {
	http.RoundTripper
	closed bool
}

func (r *idleConnectionRecorder) CloseIdleConnections() { r.closed = true }

func TestDisabledProviderAndFailedShutdown(t *testing.T) {
	t.Parallel()
	var disabled *Provider
	ctx := t.Context()
	got, finish := disabled.Start(ctx, "disabled", catalog.TraceInternal, "")
	finish("ok")
	if got != ctx || disabled.Shutdown(ctx) != nil {
		t.Fatal("disabled tracing changed context or required shutdown work")
	}
	transport := &idleConnectionRecorder{}
	sdkProvider := sdk.NewTracerProvider(sdk.WithSpanProcessor(failingShutdownProcessor{tracetest.NewSpanRecorder()}))
	p := &Provider{provider: sdkProvider, client: &http.Client{Transport: transport}}
	if err := p.Shutdown(ctx); err == nil || err.Error() != "tracing shutdown incomplete" || !transport.closed {
		t.Fatalf("failed shutdown did not redact the cause and close idle connections: %v, closed=%v", err, transport.closed)
	}
	t.Run("successful shutdown", func(t *testing.T) {
		recorder := tracetest.NewSpanRecorder()
		sdkProvider := sdk.NewTracerProvider(sdk.WithSpanProcessor(recorder))
		transport := &idleConnectionRecorder{}
		p := &Provider{provider: sdkProvider, client: &http.Client{Transport: transport}, tracer: sdkProvider.Tracer("fixture")}
		ctx, end := p.Start(t.Context(), "work", catalog.TraceInternal, "")
		end("ok")
		if err := p.Shutdown(ctx); err != nil || !transport.closed || len(recorder.Ended()) != 1 {
			t.Fatalf("successful shutdown: error=%v, connections closed=%v", err, transport.closed)
		}
		_, after := p.Start(ctx, "after shutdown", catalog.TraceInternal, "")
		after("ok")
		if len(recorder.Ended()) != 1 {
			t.Fatal("shutdown provider continued recording")
		}
	})
}

func TestSpanKindsBoundsAndUnlinkedAttempts(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdk.NewTracerProvider(sdk.WithSpanProcessor(recorder), sdk.WithSampler(sdk.AlwaysSample()))
	defer func() { _ = provider.Shutdown(t.Context()) }()
	p := &Provider{provider: provider, tracer: provider.Tracer("fixture")}
	ctx, end := p.Start(t.Context(), "request", catalog.TraceServer, "")
	_, finish := p.Start(ctx, strings.Repeat("x", 161), catalog.TraceProducer, "")
	finish("private-unrecognized-outcome")
	_, attempt := p.Start(ctx, "unlinked attempt", catalog.TraceAttempt, "")
	attempt("dependency_error")
	end("ok")
	spans := recorder.Ended()
	if len(spans) != 3 {
		t.Fatalf("expected three spans, got %d", len(spans))
	}
	producer, worker, root := spans[0], spans[1], spans[2]
	if producer.Name() != strings.Repeat("x", 160) || producer.SpanKind() != trace.SpanKindProducer || !producer.Parent().Equal(root.SpanContext()) {
		t.Fatal("producer lost bounded name, kind or request parentage")
	}
	if attrs := producer.Attributes(); len(attrs) != 1 || string(attrs[0].Key) != "catalog.outcome" || attrs[0].Value.AsString() != "other" || producer.Status().Code != codes.Unset {
		t.Fatal("unknown outcome was not safely classified")
	}
	if worker.SpanKind() != trace.SpanKindConsumer || worker.Parent().IsValid() || len(worker.Links()) != 0 || worker.SpanContext().TraceID() == root.SpanContext().TraceID() {
		t.Fatal("attempt without a cause inherited ambient request or invented a link")
	}
	if worker.Status().Code != codes.Error || worker.Status().Description != "" || len(worker.Events()) != 0 {
		t.Fatal("dependency error lost classification or recorded private details")
	}
}

func TestRemoteSamplingEntropyFailureDrops(t *testing.T) {
	for _, test := range []struct {
		name string
		n    int
		err  error
	}{
		{"read error", 0, io.ErrUnexpectedEOF},
		{"short read", 7, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := localRandomSampler{ratio: 0.5, read: func([]byte) (int, error) { return test.n, test.err }}
			result := s.ShouldSample(sdk.SamplingParameters{})
			if result.Decision != sdk.Drop || len(result.Attributes) != 0 || result.Tracestate.String() != "" {
				t.Fatal("failed entropy produced a sampled trace or metadata")
			}
		})
	}
}

func TestPartialOutcomeIsNotAnError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdk.NewTracerProvider(sdk.WithSpanProcessor(recorder), sdk.WithSampler(sdk.AlwaysSample()))
	defer func() { _ = provider.Shutdown(t.Context()) }()
	p := &Provider{provider: provider, tracer: provider.Tracer("fixture")}
	_, end := p.Start(t.Context(), "catalog search", catalog.TraceServer, "")
	end("partial")
	span := recorder.Ended()[0]
	if span.Status().Code != codes.Unset || len(span.Attributes()) != 1 || span.Attributes()[0].Value.AsString() != "partial" {
		t.Fatal("partial result lost its distinct non-error outcome")
	}
}

func TestConcurrentContextsRemainIndependent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdk.NewTracerProvider(sdk.WithSpanProcessor(recorder), sdk.WithSampler(sdk.AlwaysSample()))
	defer func() { _ = provider.Shutdown(t.Context()) }()
	p := &Provider{provider: provider, tracer: provider.Tracer("fixture")}
	var wg sync.WaitGroup
	for i := 1; i <= 32; i++ {
		wg.Go(func() {
			cause := catalog.TraceContext(fmt.Sprintf("00-%032x-%016x-01", i, i))
			ctx, end := p.Start(t.Context(), "ingress", catalog.TraceServer, cause)
			_, child := catalog.StartTrace(ctx, "work", catalog.TraceInternal, "")
			child("ok")
			end("ok")
		})
	}
	wg.Wait()
	roots := map[string]string{}
	for _, span := range recorder.Ended() {
		if span.Name() == "ingress" {
			roots[span.SpanContext().TraceID().String()] = span.SpanContext().SpanID().String()
		}
	}
	if len(roots) != 32 || len(recorder.Ended()) != 64 {
		t.Fatal("concurrent trace loss or mixing")
	}
	for _, span := range recorder.Ended() {
		if span.Name() == "work" && roots[span.SpanContext().TraceID().String()] != span.Parent().SpanID().String() {
			t.Fatal("child attached to another request")
		}
	}
}
