package observability

import (
	"bytes"
	"context"
	"math"
	"runtime/metrics"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
)

func TestRuntimeMetricContractAndAvailability(t *testing.T) {
	reading := runtimeReading{pauses: &metrics.Float64Histogram{Buckets: []float64{math.Inf(-1), 0, 0.5, math.Inf(1)}, Counts: []uint64{0, 2, 3}}}
	for i := range reading.values {
		reading.values[i] = uint64(i + 1)
		reading.available[i] = true
	}
	var output bytes.Buffer
	writeRuntime(&output, reading)
	for _, want := range []string{
		"# TYPE open_agentic_catalog_go_heap_bytes gauge\nopen_agentic_catalog_go_heap_bytes 1\n",
		"# TYPE open_agentic_catalog_go_allocated_bytes_total counter\nopen_agentic_catalog_go_allocated_bytes_total 4\n",
		"open_agentic_catalog_go_gc_pause_seconds_bucket{le=\"0.49999999999999994\"} 2\n",
		"open_agentic_catalog_go_gc_pause_seconds_bucket{le=\"+Inf\"} 5\n",
		"open_agentic_catalog_go_gc_pause_seconds_count 5\n",
		"open_agentic_catalog_go_metric_available{metric=\"open_agentic_catalog_go_gc_pause_seconds\"} 1\n",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(output.String(), "_sum") || strings.Contains(output.String(), "le=\"-Inf") {
		t.Fatal("invented sum or negative duration boundary")
	}
	if output.Len() > 64<<10 {
		t.Fatal("runtime output exceeded its budget")
	}
	output.Reset()
	writeRuntime(&output, runtimeReading{})
	for _, metric := range runtimeScalars {
		if strings.Contains(output.String(), "\n"+metric.name+" ") || !strings.Contains(output.String(), "metric="+quote(metric.name)+"} 0") {
			t.Errorf("unavailable metric %s became a value", metric.name)
		}
	}
	if strings.Contains(output.String(), runtimePauseName+"_count") {
		t.Fatal("unavailable pause count became zero")
	}
}

func TestRuntimeHistogramRejectsInvalidShapeAndOverflow(t *testing.T) {
	for name, h := range map[string]*metrics.Float64Histogram{
		"missing":              nil,
		"empty":                {},
		"wrong-count":          {Buckets: []float64{0, 1, math.Inf(1)}, Counts: []uint64{1}},
		"missing-zero":         {Buckets: []float64{1, math.Inf(1)}, Counts: []uint64{1}},
		"missing-infinity":     {Buckets: []float64{0, 1}, Counts: []uint64{1}},
		"nan":                  {Buckets: []float64{math.NaN(), math.Inf(1)}, Counts: []uint64{1}},
		"decreasing":           {Buckets: []float64{0, 2, 1, math.Inf(1)}, Counts: []uint64{1, 2, 3}},
		"negative-observation": {Buckets: []float64{math.Inf(-1), 0, math.Inf(1)}, Counts: []uint64{1, 0}},
		"straddles-zero":       {Buckets: []float64{-1, 1, math.Inf(1)}, Counts: []uint64{0, 0}},
		"overflow":             {Buckets: []float64{0, 1, math.Inf(1)}, Counts: []uint64{math.MaxUint64, 1}},
		"too-many":             {Buckets: make([]float64, maxRuntimeBuckets+2), Counts: make([]uint64, maxRuntimeBuckets+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if validRuntimeHistogram(h) {
				t.Fatal("invalid histogram accepted")
			}
			var output bytes.Buffer
			writeRuntime(&output, runtimeReading{pauses: h})
			if strings.Contains(output.String(), runtimePauseName+"_bucket") {
				t.Fatal("invalid histogram rendered")
			}
		})
	}
}

func TestPinnedRuntimeMetricsAndConcurrentScrapes(t *testing.T) {
	reading := readRuntime()
	for i, available := range reading.available {
		if !available {
			t.Errorf("pinned toolchain no longer supplies %s", runtimeScalars[i].source)
		}
	}
	if !validRuntimeHistogram(reading.pauses) {
		t.Fatal("pinned runtime pause histogram unsupported")
	}
	r := New(nil)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 20 {
				r.SetLifecycle(LifecycleRunning)
				r.SetComponent(ComponentWorker, ComponentRunning)
				if state, degraded := r.State(); state != LifecycleRunning || degraded {
					t.Error("concurrent state lost the running component")
				}
				r.RecordRequestDeadline()
				r.ObserveTraceExport(false, 2)
				r.BeginHTTP()("GET", "/metrics", 200, 1, 0)
				body := r.Render(context.Background())
				if !bytes.Contains(body, []byte("open_agentic_catalog_go_goroutines ")) || len(body) > MaxMetricsBytes {
					t.Error("runtime metric missing or oversized during concurrent scrape")
				}
			}
		})
	}
	group.Wait()
	body := string(r.Render(t.Context()))
	assertCounterSample(t, body, "open_agentic_catalog_http_request_deadlines_total", 160)
	assertCounterSample(t, body, "open_agentic_catalog_trace_export_batches_total", 160)
	assertCounterSample(t, body, "open_agentic_catalog_trace_export_spans_total", 320)
	assertCounterSample(t, body, "open_agentic_catalog_trace_export_failures_total", 160)
}

func TestDecodeRuntimeRejectsMissingNamesAndKinds(t *testing.T) {
	var samples [len(runtimeScalars) + 1]metrics.Sample
	for i, metric := range runtimeScalars {
		samples[i].Name = metric.source
	}
	samples[len(runtimeScalars)].Name = runtimePauseSource
	reading := decodeRuntime(samples)
	for _, available := range reading.available {
		if available {
			t.Fatal("missing typed value accepted")
		}
	}
	if reading.pauses != nil {
		t.Fatal("missing histogram accepted")
	}
	metrics.Read(samples[:])
	for i := range samples {
		samples[i].Name = "unknown"
	}
	reading = decodeRuntime(samples)
	for _, available := range reading.available {
		if available {
			t.Fatal("unreviewed metric name accepted")
		}
	}
	if reading.pauses != nil {
		t.Fatal("unreviewed histogram accepted")
	}
}

func TestRuntimeHistogramAcceptsZeroAndExactBounds(t *testing.T) {
	for name, h := range map[string]*metrics.Float64Histogram{
		"zero-observations": {Buckets: []float64{0, math.Inf(1)}, Counts: []uint64{0}},
		"maximum-count":     {Buckets: []float64{0, 1, math.Inf(1)}, Counts: []uint64{math.MaxUint64 - 1, 1}},
		"smallest-bound":    {Buckets: []float64{0, math.SmallestNonzeroFloat64, math.Inf(1)}, Counts: []uint64{1, 0}},
	} {
		t.Run(name, func(t *testing.T) {
			if !validRuntimeHistogram(h) {
				t.Fatal("valid histogram boundary rejected")
			}
			var output bytes.Buffer
			writeRuntime(&output, runtimeReading{pauses: h})
			if !strings.Contains(output.String(), "metric="+quote(runtimePauseName)+"} 1\n") {
				t.Fatal("valid histogram reported unavailable")
			}
			if name == "smallest-bound" && !strings.Contains(output.String(), runtimePauseName+"_bucket{le=\"0\"} 1\n") {
				t.Fatal("exclusive smallest boundary did not become inclusive zero")
			}
			if name == "maximum-count" && !strings.Contains(output.String(), runtimePauseName+"_count 18446744073709551615\n") {
				t.Fatal("maximum count lost integer precision")
			}
		})
	}
	h := &metrics.Float64Histogram{Buckets: make([]float64, maxRuntimeBuckets+1), Counts: make([]uint64, maxRuntimeBuckets)}
	for i := range maxRuntimeBuckets {
		h.Buckets[i] = float64(i)
		h.Counts[i] = 1
	}
	h.Buckets[maxRuntimeBuckets] = math.Inf(1)
	var output bytes.Buffer
	writeRuntime(&output, runtimeReading{pauses: h})
	if strings.Count(output.String(), runtimePauseName+"_bucket{") != maxRuntimeBuckets || output.Len() > 64<<10 {
		t.Fatal("maximum supported histogram is missing or exceeds output budget")
	}
}

func TestRegistryRuntimeAvailabilityCanRecoverWithoutStaleValues(t *testing.T) {
	r := New(nil)
	reading := runtimeReading{}
	r.runtimeRead = func() runtimeReading { return reading }
	for _, available := range []bool{true, false, true} {
		reading.available[0] = available
		reading.values[0] = 42
		body := string(r.Render(t.Context()))
		if strings.Contains(body, "\nopen_agentic_catalog_go_heap_bytes 42\n") != available {
			t.Fatal("registry retained stale runtime data or failed to recover")
		}
	}
}

func TestSuccessfulEmptyHousekeepingClearsAgeWithoutUndoingCounters(t *testing.T) {
	r := New(nil)
	r.ObserveHousekeepingBatch(housekeeping.ClassTerminalJob, "succeeded", housekeeping.Result{Reclaimed: 2, Marked: 3, OldestEligibleAge: time.Minute}, time.Second)
	r.ObserveHousekeepingBatch(housekeeping.ClassTerminalJob, "succeeded", housekeeping.Result{Reclaimed: -1, Marked: -1, OldestEligibleAge: -time.Second}, -time.Second)
	if r.housekeeping.oldest[housekeeping.ClassTerminalJob] != 0 || r.housekeeping.reclaimed[housekeeping.ClassTerminalJob] != 2 || r.housekeeping.marked[housekeeping.ClassTerminalJob] != 3 {
		t.Fatal("empty housekeeping left stale age or invalid counts undid progress")
	}
}

func BenchmarkRuntimeCollection(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = readRuntime()
	}
}

func BenchmarkRuntimeExposition(b *testing.B) {
	reading := readRuntime()
	var output bytes.Buffer
	b.ReportAllocs()
	for b.Loop() {
		output.Reset()
		writeRuntime(&output, reading)
	}
}
