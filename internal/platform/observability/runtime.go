// Runtime metric decoding selects fixed Go counters and histogram buckets without
// exposing arbitrary runtime sample names.

package observability

import (
	"bytes"
	"fmt"
	"math"
	"runtime/metrics"
	"strconv"
)

// These names are a reviewed allowlist, not an export of metrics.All(). Values
// are sampled on scrape, never on a product request or by forcing collection.
var runtimeScalars = [...]struct {
	source, name, help, kind string
}{
	{"/memory/classes/heap/objects:bytes", "open_agentic_catalog_go_heap_bytes", "Heap bytes occupied by live or not yet freed objects; not RSS.", "gauge"},
	{"/gc/heap/objects:objects", "open_agentic_catalog_go_heap_objects", "Live or unswept heap objects.", "gauge"},
	{"/gc/heap/live:bytes", "open_agentic_catalog_go_last_gc_live_heap_bytes", "Live heap bytes marked by the previous completed GC.", "gauge"},
	{"/gc/heap/allocs:bytes", "open_agentic_catalog_go_allocated_bytes_total", "Cumulative heap bytes allocated since process start.", "counter"},
	{"/sched/goroutines:goroutines", "open_agentic_catalog_go_goroutines", "Live goroutines in this process.", "gauge"},
	{"/gc/cycles/total:gc-cycles", "open_agentic_catalog_go_gc_cycles_total", "Completed GC cycles since process start.", "counter"},
}

const runtimePauseSource = "/sched/pauses/total/gc:seconds"
const runtimePauseName = "open_agentic_catalog_go_gc_pause_seconds"
const maxRuntimeBuckets = 512

type runtimeReading struct {
	values    [len(runtimeScalars)]uint64
	available [len(runtimeScalars)]bool
	pauses    *metrics.Float64Histogram
}

// readRuntime samples the reviewed Go metric allowlist into scrape-owned storage
// without sharing mutable histogram buffers.
func readRuntime() runtimeReading {
	var samples [len(runtimeScalars) + 1]metrics.Sample
	for i, metric := range runtimeScalars {
		samples[i].Name = metric.source
	}
	samples[len(runtimeScalars)].Name = runtimePauseSource
	// Each read owns its sample and histogram storage. No mutable slice is shared
	// between simultaneous scrapes, and no registry request-update lock is held.
	metrics.Read(samples[:])
	return decodeRuntime(samples)
}

// decodeRuntime accepts only expected metric names and types, retaining absence
// when a runtime version supplies a different shape.
func decodeRuntime(samples [len(runtimeScalars) + 1]metrics.Sample) runtimeReading {
	var reading runtimeReading
	for i, metric := range runtimeScalars {
		if samples[i].Name == metric.source && samples[i].Value.Kind() == metrics.KindUint64 {
			reading.available[i] = true
			reading.values[i] = samples[i].Value.Uint64()
		}
	}
	pause := samples[len(runtimeScalars)]
	if pause.Name == runtimePauseSource && pause.Value.Kind() == metrics.KindFloat64Histogram {
		reading.pauses = pause.Value.Float64Histogram()
	}
	return reading
}

// writeRuntime renders available scalars and checked GC buckets, adjusting
// exclusive runtime boundaries to inclusive Prometheus bounds without inventing a
// sum.
func writeRuntime(output *bytes.Buffer, reading runtimeReading) {
	writeMetricHeader(output, "open_agentic_catalog_go_metric_available", "Whether the named fixed runtime metric was collected with its expected shape.", "gauge")
	for i, metric := range runtimeScalars {
		available := 0
		if reading.available[i] {
			available = 1
			writeMetricHeader(output, metric.name, metric.help, metric.kind)
			fmt.Fprintf(output, "%s %d\n", metric.name, reading.values[i])
		}
		fmt.Fprintf(output, "open_agentic_catalog_go_metric_available{metric=%s} %d\n", quote(metric.name), available)
	}
	available := 0
	if validRuntimeHistogram(reading.pauses) {
		available = 1
		writeMetricHeader(output, runtimePauseName, "GC stop-the-world pause distribution; exact sum unavailable from runtime buckets.", "histogram")
		var cumulative uint64
		for i, count := range reading.pauses.Counts {
			cumulative += count
			upper := reading.pauses.Buckets[i+1]
			if upper <= 0 {
				continue // The empty negative-duration bucket is not a duration signal.
			}
			if !math.IsInf(upper, 1) {
				// Runtime buckets have exclusive upper bounds; Prometheus le is
				// inclusive. Preserve membership instead of moving boundary samples.
				upper = math.Nextafter(upper, math.Inf(-1))
			}
			fmt.Fprintf(output, "%s_bucket{le=%s} %d\n", runtimePauseName, quote(strconv.FormatFloat(upper, 'g', -1, 64)), cumulative)
		}
		fmt.Fprintf(output, "%s_count %d\n", runtimePauseName, cumulative)
		// runtime/metrics provides no observation sum. Do not invent one from
		// bucket midpoints or publish a misleading zero-valued _sum.
	}
	fmt.Fprintf(output, "open_agentic_catalog_go_metric_available{metric=%s} %d\n", quote(runtimePauseName), available)
}

// validRuntimeHistogram rejects excessive or inconsistent buckets, invalid
// floating bounds, negative observations and count overflow.
func validRuntimeHistogram(h *metrics.Float64Histogram) bool {
	if h == nil || len(h.Counts) == 0 || len(h.Counts) > maxRuntimeBuckets || len(h.Buckets) != len(h.Counts)+1 {
		return false
	}
	if h.Buckets[0] > 0 || !math.IsInf(h.Buckets[len(h.Buckets)-1], 1) {
		return false
	}
	var total uint64
	for i, count := range h.Counts {
		lower, upper := h.Buckets[i], h.Buckets[i+1]
		if math.IsNaN(lower) || math.IsNaN(upper) || lower >= upper || lower < 0 && (upper > 0 || count != 0) || math.MaxUint64-total < count {
			return false
		}
		total += count
	}
	return true
}
