// Tracing metrics retain bounded success/failure counters and batch sizes without
// recording exporter errors or destinations.

package observability

import (
	"bytes"
	"fmt"
)

// ObserveTraceExport counts submitted batches and uncertain/failed exports.
// SDK queue drops are not exposed as an invented exact loss counter.
func (r *Registry) ObserveTraceExport(ok bool, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.traceBatches++
	if count > 0 {
		r.traceSpans += uint64(count)
	}
	if !ok {
		r.traceFailures++
	}
}

// writeTraceMetrics snapshots exporter counters under the registry lock and emits
// fixed metric names without collector details.
func (r *Registry) writeTraceMetrics(output *bytes.Buffer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range []struct {
		name, help string
		value      uint64
	}{
		{"batches", "SDK batches submitted to the trace exporter.", r.traceBatches},
		{"spans", "Spans submitted to the exporter, not confirmed backend retention.", r.traceSpans},
		{"failures", "Failed or partially accepted trace export batches.", r.traceFailures},
	} {
		name := "open_agentic_catalog_trace_export_" + m.name + "_total"
		writeMetricHeader(output, name, m.help, "counter")
		fmt.Fprintf(output, "%s %d\n", name, m.value)
	}
}
