// Package tracing implements the optional, process-owned OTLP trace boundary.
// Domain callers use catalog.Tracer; neither a global provider nor baggage is used.
package tracing

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

// Observer receives only process-local bounded export outcomes, never SDK errors.
type Observer interface{ ObserveTraceExport(bool, int) }

type Provider struct {
	client   *http.Client
	provider *sdk.TracerProvider
	tracer   trace.Tracer
}

var diagnosticsOnce sync.Once

// New starts an exporter only for enabled catalogd processes. Configuration
// inspection and operator commands must not call this constructor.
// The instance argument is the execution ID captured by processinfo at startup.
func New(ctx context.Context, c config.Tracing, role config.Role, version, instance string, logger *slog.Logger, observer Observer) (*Provider, error) {
	if !c.Enabled {
		return nil, nil //nolint:nilnil // Disabled tracing deliberately has no provider or lifecycle work.
	}
	if instance == "" {
		return nil, errors.New("tracing: process identity is required")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "OTEL_") && value != "" {
			return nil, errors.New("tracing: unsupported OTEL_* environment configuration; use CATALOG_TRACING settings")
		}
	}
	// The SDK's own diagnostics may contain remote response bodies or secrets.
	// Export results below supply a safe, rate-limited alternative.
	diagnosticsOnce.Do(func() {
		otel.SetLogger(logr.Discard())
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
			// Suppress raw SDK errors; they may contain private endpoint data.
		}))
	})
	headers, roots, certificates, err := loadInputs(c)
	if err != nil {
		return nil, err
	}
	prefixes := make([]netip.Prefix, 0, len(c.AllowedAddressPrefixes))
	for _, raw := range c.AllowedAddressPrefixes {
		p, _ := netip.ParsePrefix(raw)
		prefixes = append(prefixes, p)
	}
	u, _ := url.Parse(c.Endpoint)
	if u.Path == "" {
		u.Path = "/"
	}
	client, err := fetcher.NewTelemetryClient(fetcher.Policy{Timeout: c.ExportTimeout.Duration, MaxResponseBytes: 1 << 20, AllowedAddressPrefixes: prefixes, RootCAs: roots, Certificates: certificates, UserAgent: "open-agentic-catalog-tracing"}, u.String(), c.AllowHTTP)
	if err != nil {
		return nil, errors.New("tracing: invalid export transport")
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(u.String()), otlptracehttp.WithHTTPClient(client), otlptracehttp.WithHeaders(headers), otlptracehttp.WithEncoding(otlptracehttp.EncodingProtobuf), otlptracehttp.WithCompression(otlptracehttp.NoCompression), otlptracehttp.WithMaxRequestSize(1<<20), otlptracehttp.WithTimeout(c.ExportTimeout.Duration), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: true, InitialInterval: time.Second, MaxInterval: 2 * time.Second, MaxElapsedTime: c.ExportTimeout.Duration}))
	if err != nil {
		return nil, errors.New("tracing: exporter initialization failed")
	}
	rootSampler := sdk.TraceIDRatioBased(c.SampleRatio)
	remoteSampler := sdk.Sampler(localRandomSampler{ratio: c.SampleRatio, read: rand.Read})
	opts := []sdk.ParentBasedSamplerOption{sdk.WithRemoteParentSampled(remoteSampler), sdk.WithRemoteParentNotSampled(remoteSampler)}
	if c.TrustRemoteSampling {
		opts = nil
	}
	processor := sdk.NewBatchSpanProcessor(&observedExporter{SpanExporter: exporter, observer: observer, logger: logger}, sdk.WithMaxQueueSize(c.QueueSize), sdk.WithMaxExportBatchSize(c.BatchSize), sdk.WithBatchTimeout(time.Second), sdk.WithExportTimeout(c.ExportTimeout.Duration))
	p := sdk.NewTracerProvider(sdk.WithSampler(sdk.ParentBased(rootSampler, opts...)), sdk.WithResource(resource.NewSchemaless(attribute.String("service.name", "open-agentic-catalog-"+string(role)), attribute.String("service.namespace", c.Installation), attribute.String("service.version", bounded(version, 128)), attribute.String("service.instance.id", instance))), sdk.WithRawSpanLimits(sdk.SpanLimits{AttributeValueLengthLimit: 160, AttributeCountLimit: 16, EventCountLimit: 4, LinkCountLimit: 1, AttributePerEventCountLimit: 4, AttributePerLinkCountLimit: 0}), sdk.WithoutPanicRecording(), sdk.WithSpanProcessor(processor))
	return &Provider{provider: p, tracer: p.Tracer("open-agentic-catalog"), client: client}, nil
}

// Start uses only reviewed code-owned names and safe outcomes; errors are never
// recorded as exception events or status descriptions.
func (p *Provider) Start(ctx context.Context, name string, kind catalog.TraceKind, cause catalog.TraceContext) (context.Context, func(string)) {
	if p == nil {
		return ctx, func(string) {
			// Disabled tracing creates no span to complete.
		}
	}
	opts := []trace.SpanStartOption{}
	switch kind {
	case catalog.TraceInternal:
		// The SDK's default is an internal span.
	case catalog.TraceServer:
		opts = append(opts, trace.WithSpanKind(trace.SpanKindServer))
		if parent := decode(cause); parent.IsValid() {
			ctx = trace.ContextWithRemoteSpanContext(ctx, parent)
		}
	case catalog.TraceClient:
		opts = append(opts, trace.WithSpanKind(trace.SpanKindClient))
	case catalog.TraceProducer:
		opts = append(opts, trace.WithSpanKind(trace.SpanKindProducer))
	case catalog.TraceAttempt:
		opts = append(opts, trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindConsumer))
		if parent := decode(cause); parent.IsValid() {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: parent}))
		}
	}
	ctx, span := p.tracer.Start(ctx, bounded(name, 160), opts...)
	ctx = catalog.WithTracer(ctx, p)
	sc := span.SpanContext()
	flags := "00"
	if sc.IsSampled() {
		flags = "01"
	}
	ctx = catalog.WithTraceContext(ctx, catalog.TraceContext("00-"+sc.TraceID().String()+"-"+sc.SpanID().String()+"-"+flags))
	return ctx, func(outcome string) {
		switch outcome {
		case "ok", "partial", "succeeded", "4xx", "cancelled", "unsupported", "waiting", "error", "failed", "dead_letter", "5xx", "dependency_error":
		default:
			outcome = "other"
		}
		span.SetAttributes(attribute.String("catalog.outcome", outcome))
		if outcome == "error" || outcome == "failed" || outcome == "dead_letter" || outcome == "unsupported" || outcome == "5xx" || outcome == "dependency_error" {
			span.SetStatus(codes.Error, "")
		}
		span.End()
	}
}

// Shutdown drains the provider within ctx and always closes idle collector
// connections. A nil provider is harmless; failures return a fixed safe error.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	defer p.client.CloseIdleConnections()
	if err := p.provider.Shutdown(ctx); err != nil {
		return errors.New("tracing shutdown incomplete")
	}
	return nil
}

// decode converts only validated private trace context into a remote SDK context,
// preserving the sampled flag and dropping invalid input.
func decode(value catalog.TraceContext) trace.SpanContext {
	if !value.Valid() {
		return trace.SpanContext{}
	}
	s := string(value)
	tid, _ := trace.TraceIDFromHex(s[3:35])
	sid, _ := trace.SpanIDFromHex(s[36:52])
	flags := trace.TraceFlags(0)
	if s[54] == '1' {
		flags = trace.FlagsSampled
	}
	return trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: flags, Remote: true})
}

// bounded truncates code-owned span or operation names to the configured byte
// ceiling before recording.
func bounded(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type localRandomSampler struct {
	ratio float64
	read  func([]byte) (int, error)
}

// Description returns a fixed sampler name without exposing sampling inputs.
func (s localRandomSampler) Description() string { return "CatalogRemoteIngress" }

// ShouldSample draws independent local entropy instead of deriving acceptance from
// caller-controlled trace identifiers; entropy failure drops the span.
func (s localRandomSampler) ShouldSample(sdk.SamplingParameters) sdk.SamplingResult {
	decision := sdk.Drop
	var raw [8]byte
	if s.ratio >= 1 {
		decision = sdk.RecordAndSample
	} else if s.ratio > 0 {
		if n, err := s.read(raw[:]); err == nil && n == len(raw) && float64(binary.BigEndian.Uint64(raw[:])>>11)/float64(uint64(1)<<53) < s.ratio {
			decision = sdk.RecordAndSample
		}
	}
	return sdk.SamplingResult{Decision: decision}
}

type observedExporter struct {
	sdk.SpanExporter
	observer Observer
	logger   *slog.Logger
	warning  diagnostics.Warning
}

// ExportSpans records only bounded batch outcomes and rate-limits a fixed failure
// message, replacing the exporter error before returning it.
func (e *observedExporter) ExportSpans(ctx context.Context, spans []sdk.ReadOnlySpan) error {
	err := e.SpanExporter.ExportSpans(ctx, spans)
	if e.observer != nil {
		e.observer.ObserveTraceExport(err == nil, len(spans))
	}
	if err != nil {
		if e.logger != nil {
			e.warning.Warn(time.Now(), e.logger, "trace export incomplete; telemetry may be lost", "event", "trace.export_failed", "component", "tracing")
		}
		return errors.New("trace export incomplete")
	}
	return nil
}
