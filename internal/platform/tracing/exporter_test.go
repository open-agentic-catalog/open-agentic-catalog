// Unit tests exercise exporter accounting and sanitized diagnostics with local
// span snapshots. The fake exporter performs no HTTP or background work.
package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	sdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type exportFunc func(context.Context, []sdk.ReadOnlySpan) error

func (f exportFunc) ExportSpans(ctx context.Context, spans []sdk.ReadOnlySpan) error {
	return f(ctx, spans)
}
func (exportFunc) Shutdown(context.Context) error { return nil }

type exportObserverFunc func(bool, int)

func (f exportObserverFunc) ObserveTraceExport(success bool, spans int) { f(success, spans) }

func TestObservedExporterOutcomesAndSafeDiagnostics(t *testing.T) {
	for _, policy := range []string{"without diagnostics", "visible warnings", "filtered warnings"} {
		t.Run(policy, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				batch := tracetest.SpanStubs{{Name: "catalog search"}, {Name: "publication validate"}}.Snapshots()
				ctx := t.Context()
				var logs bytes.Buffer
				var exportErr error
				privateErr := errors.New("private-collector-response credential=fixture-secret")
				calls := 0
				exporter := &observedExporter{SpanExporter: exportFunc(func(got context.Context, spans []sdk.ReadOnlySpan) error {
					calls++
					if got != ctx || !reflect.DeepEqual(spans, batch) {
						t.Error("export wrapper changed context or batch")
					}
					return exportErr
				})}
				type observation struct {
					success bool
					spans   int
				}
				var observations []observation
				if policy != "without diagnostics" {
					level := slog.LevelInfo
					if policy == "filtered warnings" {
						level = slog.LevelError
					}
					exporter.logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level}))
					exporter.observer = exportObserverFunc(func(success bool, spans int) { observations = append(observations, observation{success, spans}) })
				}
				for _, phase := range []string{"success", "failure", "suppressed failure", "eligible failure", "recovery"} {
					exportErr = privateErr
					switch phase {
					case "success", "recovery":
						exportErr = nil
					case "eligible failure":
						time.Sleep(time.Minute)
					}
					err := exporter.ExportSpans(ctx, batch)
					if exportErr == nil {
						if err != nil {
							t.Fatalf("%s: %v", phase, err)
						}
					} else if err == nil || err.Error() != "trace export incomplete" || errors.Is(err, privateErr) {
						t.Fatalf("%s returned unsanitized error: %v", phase, err)
					}
				}
				if calls != 5 {
					t.Fatalf("export calls=%d", calls)
				}
				if policy == "without diagnostics" {
					if len(observations) != 0 || logs.Len() != 0 {
						t.Fatal("absent diagnostics produced output")
					}
					return
				}
				want := []observation{{true, 2}, {false, 2}, {false, 2}, {false, 2}, {true, 2}}
				if !reflect.DeepEqual(observations, want) {
					t.Fatalf("export observations=%v", observations)
				}
				if policy == "filtered warnings" {
					if logs.Len() != 0 {
						t.Fatal("trace export warnings bypassed the configured threshold")
					}
					return
				}
				lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
				if len(lines) != 2 {
					t.Fatalf("expected two eligible warnings, got %d", len(lines))
				}
				for i, line := range lines {
					var record map[string]any
					if err := json.Unmarshal([]byte(line), &record); err != nil {
						t.Fatal(err)
					}
					if len(record) != 6 || record["event"] != "trace.export_failed" || record["component"] != "tracing" || record["suppressed"] != float64(i) || record["level"] != "WARN" || record["msg"] != "trace export incomplete; telemetry may be lost" || record["time"] == nil || strings.Contains(line, "fixture-secret") || strings.Contains(line, "private-collector") {
						t.Fatalf("unsafe export diagnostic: %s", line)
					}
				}
			})
		})
	}
}

func TestObservedExporterConcurrentFailuresAreAccountedAndThrottled(t *testing.T) {
	var logs bytes.Buffer
	var exports, observations, spanCount atomic.Int64
	batch := tracetest.SpanStubs{{Name: "worker attempt"}}.Snapshots()
	exporter := &observedExporter{
		SpanExporter: exportFunc(func(context.Context, []sdk.ReadOnlySpan) error { exports.Add(1); return errors.New("fixture-secret") }),
		observer: exportObserverFunc(func(success bool, n int) {
			if success {
				t.Error("failed export reported success")
			}
			observations.Add(1)
			spanCount.Add(int64(n))
		}),
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	const workers = 16
	var work sync.WaitGroup
	for range workers {
		work.Go(func() {
			if err := exporter.ExportSpans(t.Context(), batch); err == nil || err.Error() != "trace export incomplete" {
				t.Errorf("concurrent export error=%v", err)
			}
		})
	}
	work.Wait()
	if exports.Load() != workers || observations.Load() != workers || spanCount.Load() != workers {
		t.Fatalf("exports=%d observations=%d spans=%d", exports.Load(), observations.Load(), spanCount.Load())
	}
	if strings.Count(logs.String(), "\n") != 1 || strings.Contains(logs.String(), "fixture-secret") {
		t.Fatalf("concurrent failure logging was not bounded and safe: %s", logs.String())
	}
}
