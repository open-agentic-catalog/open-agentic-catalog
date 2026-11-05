// Unit tests exercise isolated validation and controlled dependencies.
package tracing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func inputFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProjectedSecretInput(t *testing.T) {
	target := inputFile(t, "headers.json", []byte(`{"Authorization":"Bearer synthetic"}`))
	link := filepath.Join(t.TempDir(), "headers.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	c := config.Defaults().Tracing
	c.HeadersFile = link
	headers, _, _, err := loadInputs(c)
	if err != nil || headers["Authorization"] != "Bearer synthetic" {
		t.Fatalf("projected input: %v", err)
	}
}

func TestDisabledAndAmbientEnvironment(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", "/must-not-be-read-secret")
	c := config.Defaults().Tracing
	c.HeadersFile = "/must-not-be-read-secret"
	if p, err := New(t.Context(), c, config.RoleAPI, "test", "00000000-0000-7000-8000-000000000001", nil, nil); err != nil || p != nil {
		t.Fatal("disabled startup")
	}
	c.Enabled = true
	c.Endpoint = "https://collector.example/v1/traces"
	c.Installation = "test"
	if _, err := New(t.Context(), c, config.RoleAPI, "test", "00000000-0000-7000-8000-000000000001", nil, nil); err == nil || strings.Contains(err.Error(), "must-not") {
		t.Fatal("environment startup isolation")
	}
}

func TestEnabledProviderZeroSamplingHasNoExportWork(t *testing.T) {
	// New rejects nonempty OTEL_* inputs. Clear inherited values locally, also
	// keeping the pinned SDK's experimental instrumentation disabled.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "OTEL_") {
			t.Setenv(name, "")
		}
	}
	synctest.Test(t, func(t *testing.T) {
		c := config.Defaults().Tracing
		c.Enabled, c.Installation, c.Endpoint = true, "fixture", "https://collector.example/v1/traces"
		c.SampleRatio, c.TrustRemoteSampling = 0, false
		var exports atomic.Int64
		// The pinned OTLP/HTTP client's Start initializes local state and Stop
		// closes a channel; neither issues requests. The SDK batch processor's
		// timer and shutdown export only a nonempty batch of sampled spans.
		p, err := New(t.Context(), c, config.RoleAPI, "fixture", "00000000-0000-7000-8000-000000000001", nil, exportObserverFunc(func(bool, int) { exports.Add(1) }))
		if err != nil || p == nil {
			t.Fatalf("enabled provider construction: provider=%v error=%v", p, err)
		}
		defer func() {
			if err := p.Shutdown(context.Background()); err != nil {
				t.Errorf("provider shutdown: %v", err)
			}
		}()
		incoming := catalog.TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		parent, end := p.Start(t.Context(), "request", catalog.TraceServer, incoming)
		parentSpan := trace.SpanFromContext(parent)
		// Check before ending: even a broken sampler must not enqueue a span
		// for real export during deferred provider cleanup.
		if parentSpan.IsRecording() || parentSpan.SpanContext().IsSampled() {
			t.Fatal("zero sampling trusted the caller's sampled flag")
		}
		parentCause := catalog.CausalTrace(parent)
		if !parentCause.Valid() || parentCause[3:35] != incoming[3:35] || parentCause[36:52] == incoming[36:52] || parentCause[53:] != "00" {
			t.Fatalf("unsampled server context=%q", parentCause)
		}
		child, childEnd := catalog.StartTrace(parent, "child", catalog.TraceInternal, "")
		childSpan := trace.SpanFromContext(child)
		if childSpan.IsRecording() || childSpan.SpanContext().IsSampled() {
			t.Fatal("local child did not inherit the unsampled decision")
		}
		childCause := catalog.CausalTrace(child)
		if !childCause.Valid() || childCause[3:35] != parentCause[3:35] || childCause[36:52] == parentCause[36:52] || childCause[53:] != "00" {
			t.Fatalf("unsampled child context=%q", childCause)
		}
		childEnd("ok")
		end("ok")
		synctest.Wait()
		time.Sleep(2 * time.Second) // Virtual time exercises the idle batch timer.
		synctest.Wait()
		if exports.Load() != 0 {
			t.Fatal("unsampled spans caused background export work")
		}
		if err := p.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if exports.Load() != 0 {
			t.Fatal("shutdown exported unsampled spans")
		}
	})
}

func TestRemoteSamplingDoesNotUseCallerID(t *testing.T) {
	for _, test := range []struct {
		ratio float64
		fill  byte
		want  sdk.SamplingDecision
	}{{0, 0, sdk.Drop}, {1, 255, sdk.RecordAndSample}, {0.5, 0, sdk.RecordAndSample}, {0.5, 255, sdk.Drop}} {
		s := localRandomSampler{ratio: test.ratio, read: func(b []byte) (int, error) {
			for i := range b {
				b[i] = test.fill
			}
			return len(b), nil
		}}
		if got := s.ShouldSample(sdk.SamplingParameters{}); got.Decision != test.want || got.Tracestate.String() != "" || s.Description() != "CatalogRemoteIngress" {
			t.Fatal("local sampling policy")
		}
	}
}

func TestTracingRequiresOwnedInstanceOnlyWhenEnabled(t *testing.T) {
	c := config.Defaults().Tracing
	if p, err := New(t.Context(), c, config.RoleAPI, "fixture", "", nil, nil); err != nil || p != nil {
		t.Fatal("disabled tracing acquired identity")
	}
	c.Enabled = true
	if p, err := New(t.Context(), c, config.RoleAPI, "fixture", "", nil, nil); p != nil || err == nil || err.Error() != "tracing: process identity is required" {
		t.Fatalf("missing process identity: %v", err)
	}
}
