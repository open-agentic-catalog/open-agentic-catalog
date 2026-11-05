//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package tracing

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestHeadersReplacementRequiresProviderReplacement distinguishes projected
// file delivery from exporter activation, without claiming receiver revocation.
func TestHeadersReplacementRequiresProviderReplacement(t *testing.T) {
	var mu sync.Mutex
	var received []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received = append(received, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(server.Close)
	c := config.Defaults().Tracing
	c.Enabled, c.SampleRatio = true, 1
	c.Endpoint = server.URL + "/v1/traces"
	c.Installation = "fixture"
	c.AllowedAddressPrefixes = []string{"127.0.0.0/8"}
	c.CAFile = inputFile(t, "ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	c.HeadersFile = inputFile(t, "headers.json", []byte(`{"Authorization":"Bearer old-fixture"}`))
	newProvider := func() *Provider {
		t.Helper()
		p, err := New(t.Context(), c, config.RoleAPI, "fixture", "00000000-0000-7000-8000-000000000001", slog.New(slog.DiscardHandler), observability.New(time.Now))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.Shutdown(ctx); err != nil {
				t.Error("provider cleanup failed")
			}
		})
		return p
	}
	export := func(p *Provider) {
		t.Helper()
		_, end := p.Start(t.Context(), "credential activation", catalog.TraceServer, "")
		end("ok")
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := p.provider.ForceFlush(ctx); err != nil {
			t.Fatal("export flush failed")
		}
	}
	old := newProvider()
	export(old)
	if err := os.WriteFile(c.HeadersFile, []byte(`{"Authorization":"Bearer replacement-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	export(old)
	export(newProvider())
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 3 || received[0] != "Bearer old-fixture" || received[1] != "Bearer old-fixture" || received[2] != "Bearer replacement-fixture" {
		t.Fatal("projected headers were not isolated to provider startup")
	}
}

func TestExportCausalityResourcesAndRedaction(t *testing.T) {
	var mu sync.Mutex
	var requests []*collector.ExportTraceServiceRequest
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/v1/traces" || r.Header.Get("Authorization") != "Bearer fixture-secret" || r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Error("export contract")
		}
		if r.Header.Get("Traceparent") != "" || r.Header.Get("Baggage") != "" {
			t.Error("export propagation leak")
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Error(err)
		}
		value := new(collector.ExportTraceServiceRequest)
		if err := proto.Unmarshal(raw, value); err != nil {
			t.Error(err)
		}
		mu.Lock()
		requests = append(requests, value)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	c := config.Defaults().Tracing
	c.Enabled = true
	c.SampleRatio = 1
	c.Endpoint = server.URL + "/custom/v1/traces"
	c.Installation = "fixture"
	c.AllowedAddressPrefixes = []string{"127.0.0.0/8"}
	c.CAFile = inputFile(t, "ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	c.HeadersFile = inputFile(t, "headers.json", []byte(`{"Authorization":"Bearer fixture-secret"}`))
	var logs bytes.Buffer
	metrics := observability.New(time.Now)
	p, err := New(t.Context(), c, config.RoleAPI, "fixture", "00000000-0000-7000-8000-000000000001", slog.New(slog.NewJSONHandler(&logs, nil)), metrics)
	if err != nil {
		t.Fatal(err)
	}
	parent := catalog.TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx, end := p.Start(t.Context(), "HTTP GET /api/v1/resources", catalog.TraceServer, parent)
	cause := catalog.CausalTrace(ctx)
	_, childEnd := catalog.StartTrace(ctx, "SQL select", catalog.TraceClient, "")
	childEnd("ok")
	end("ok")
	worker, err := New(t.Context(), c, config.RoleWorker, "fixture", "00000000-0000-7000-8000-000000000002", slog.New(slog.NewJSONHandler(&logs, nil)), metrics)
	if err != nil {
		t.Fatal(err)
	}
	_, attemptEnd := worker.Start(t.Context(), "job process", catalog.TraceAttempt, cause)
	attemptEnd("dead_letter")
	_, dispatchEnd := worker.Start(t.Context(), "webhook dispatch", catalog.TraceAttempt, cause)
	dispatchEnd("error")
	func() {
		defer func() { _ = recover() }()
		_, finish := p.Start(t.Context(), "panic boundary", catalog.TraceServer, "")
		defer finish("error")
		panic("private-panic-secret")
	}()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Shutdown(shutdown); err != nil {
		t.Fatal(err)
	}
	if err := worker.Shutdown(shutdown); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	spans := map[string]*tracepb.Span{}
	instances := map[string]bool{}
	for _, request := range requests {
		for _, rs := range request.ResourceSpans {
			for _, a := range rs.Resource.Attributes {
				if a.Key == "service.instance.id" {
					instances[a.Value.GetStringValue()] = true
				}
			}
			for _, scope := range rs.ScopeSpans {
				for _, span := range scope.Spans {
					spans[span.Name] = span
				}
			}
			if stringsContainSecrets(request.String()) {
				t.Fatal("private input exported")
			}
		}
	}
	if len(instances) != 2 || !instances["00000000-0000-7000-8000-000000000001"] || !instances["00000000-0000-7000-8000-000000000002"] || len(spans) != 5 {
		t.Fatalf("instances=%d spans=%d", len(instances), len(spans))
	}
	if spans["job process"].Status.GetCode() != tracepb.Status_STATUS_CODE_ERROR || spans["webhook dispatch"].Status.GetCode() != tracepb.Status_STATUS_CODE_ERROR {
		t.Fatal("failed attempt span status")
	}
	root := spans["HTTP GET /api/v1/resources"]
	child := spans["SQL select"]
	attempt := spans["job process"]
	if !bytes.Equal(root.TraceId, child.TraceId) || !bytes.Equal(child.ParentSpanId, root.SpanId) {
		t.Fatal("child causality")
	}
	if len(attempt.Links) != 1 || !bytes.Equal(attempt.Links[0].TraceId, root.TraceId) || !bytes.Equal(attempt.Links[0].SpanId, root.SpanId) || bytes.Equal(attempt.TraceId, root.TraceId) {
		t.Fatal("attempt causality")
	}
	if len(spans["panic boundary"].Events) != 0 || stringsContainSecrets(logs.String()) || logs.Len() != 0 {
		t.Fatal("panic leak")
	}
	if !bytes.Contains(metrics.Render(t.Context()), []byte("open_agentic_catalog_trace_export_failures_total 0")) {
		t.Fatal("export metrics")
	}
}

func stringsContainSecrets(value string) bool {
	return strings.Contains(value, "fixture-secret") || strings.Contains(value, "private-panic-secret")
}

func TestRemoteSamplingTrustAndLocalChildren(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Type", "application/x-protobuf") }))
	defer server.Close()
	for _, trusted := range []bool{false, true} {
		for _, sampled := range []bool{false, true} {
			for _, ratio := range []float64{0, 1} {
				c := tlsConfig(t, server)
				c.TrustRemoteSampling, c.SampleRatio = trusted, ratio
				p, err := New(t.Context(), c, config.RoleAPI, "fixture", "00000000-0000-7000-8000-000000000001", nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				flags := "00"
				if sampled {
					flags = "01"
				}
				parent := catalog.TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-" + flags)
				ctx, end := p.Start(t.Context(), "request", catalog.TraceServer, parent)
				child, childEnd := catalog.StartTrace(ctx, "child", catalog.TraceInternal, "")
				want := ratio == 1
				if trusted {
					want = sampled
				}
				for _, value := range []catalog.TraceContext{catalog.CausalTrace(ctx), catalog.CausalTrace(child)} {
					if (value[54] == '1') != want {
						t.Fatal("sampling trust or local inheritance failed")
					}
				}
				childEnd("ok")
				end("ok")
				if err := p.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
