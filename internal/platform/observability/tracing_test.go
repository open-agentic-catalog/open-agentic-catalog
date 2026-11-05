// Trace counter tests distinguish attempted exports from confirmed retention.
package observability

import "testing"

// TestTraceExportCounters records every batch outcome while preventing invalid
// negative counts from wrapping the submitted-span counter.
func TestTraceExportCounters(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name            string
		success         bool
		count           int
		spans, failures uint64
	}{
		{"successful batch", true, 3, 3, 0},
		{"failed batch still submitted spans", false, 2, 2, 1},
		{"empty successful batch", true, 0, 0, 0},
		{"empty failed batch", false, 0, 0, 1},
		{"negative successful count", true, -1, 0, 0},
		{"negative failed count", false, -1, 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := New(nil)
			assertCounts := func(batches, spans, failures uint64) {
				t.Helper()
				body := string(r.Render(t.Context()))
				assertCounterSample(t, body, "open_agentic_catalog_trace_export_batches_total", batches)
				assertCounterSample(t, body, "open_agentic_catalog_trace_export_spans_total", spans)
				assertCounterSample(t, body, "open_agentic_catalog_trace_export_failures_total", failures)
			}
			assertCounts(0, 0, 0)
			r.ObserveTraceExport(test.success, test.count)
			assertCounts(1, test.spans, test.failures)
			r.ObserveTraceExport(true, 4)
			assertCounts(2, test.spans+4, test.failures)
			r.ObserveTraceExport(false, 2)
			assertCounts(3, test.spans+6, test.failures+1)
			assertCounts(3, test.spans+6, test.failures+1)
		})
	}
}
