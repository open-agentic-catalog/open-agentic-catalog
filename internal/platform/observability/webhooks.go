// Webhook metrics restrict attempt labels to fixed modes and outcomes and render
// aggregate durable delivery state.

package observability

import (
	"bytes"
	"fmt"
	"maps"
	"time"
)

// WebhookSnapshot is database-global, not a per-worker queue. Never sum these
// gauges across replicas. A missing operational snapshot is not zero backlog.
type WebhookSnapshot struct {
	DispatchableEvents         int64 `json:"dispatchableEvents"`
	OldestDispatchableAgeSec   int64 `json:"oldestDispatchableAgeSeconds"`
	WaitingRetries             int64 `json:"waitingRetries"`
	LastAttemptFailedReceivers int64 `json:"lastAttemptFailedReceivers"`
	ReconciliationTenants      int64 `json:"reconciliationTenants"`
	CapacityLostEvents         int64 `json:"capacityLostEvents"`
	TerminalLostDeliveries     int64 `json:"terminalLostDeliveries"`
}

type webhookAttemptKey struct{ mode, outcome string }

// ObserveWebhookAttempt records sender-call duration only for known modes and
// outcomes; it does not imply subsequent database commit succeeded.
func (r *Registry) ObserveWebhookAttempt(mode, outcome string, elapsed time.Duration) {
	if mode != "test" && mode != "worker" {
		return
	}
	switch outcome {
	case "accepted", "retryable_response", "terminal_response", "transport_error", "canceled":
	default:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.webhookAttempts == nil {
		r.webhookAttempts = make(map[webhookAttemptKey]durationMetric)
	}
	key := webhookAttemptKey{mode, outcome}
	r.webhookAttempts[key] = observeDuration(r.webhookAttempts[key], elapsed)
}

// cloneWebhookAttempts copies value-based histograms so rendering can proceed
// independently of registry updates.
func cloneWebhookAttempts(values map[webhookAttemptKey]durationMetric) map[webhookAttemptKey]durationMetric {
	return maps.Clone(values)
}

// writeWebhookAttemptMetrics renders the closed mode/outcome cross-product once
// any sender attempt has been observed.
func writeWebhookAttemptMetrics(output *bytes.Buffer, values map[webhookAttemptKey]durationMetric) {
	if len(values) == 0 {
		return
	}
	writeMetricHeader(output, "open_agentic_catalog_webhook_attempt_duration_seconds", "Process-local sender-call duration and count, independent of subsequent database commit.", "histogram")
	for _, mode := range []string{"test", "worker"} {
		for _, outcome := range []string{"accepted", "retryable_response", "terminal_response", "transport_error", "canceled"} {
			writeDurationHistogram(output, "open_agentic_catalog_webhook_attempt_duration_seconds", fmt.Sprintf("mode=%s,outcome=%s", quote(mode), quote(outcome)), values[webhookAttemptKey{mode, outcome}])
		}
	}
}

// writeWebhookOperationalMetrics emits database-global backlog and loss gauges
// whose values must not be summed across worker replicas.
func writeWebhookOperationalMetrics(output *bytes.Buffer, values WebhookSnapshot) {
	for _, metric := range []struct {
		name, help string
		value      int64
	}{
		{"dispatchable_events", "Database-global public events with at least one due recipient and no live claim.", values.DispatchableEvents},
		{"oldest_dispatchable_age_seconds", "Age of the oldest currently dispatchable public event.", values.OldestDispatchableAgeSec},
		{"waiting_retries", "Database-global retained waiting recipients still eligible under an unfinished parent, including future retries.", values.WaitingRetries},
		{"last_attempt_failed_receivers", "Active endpoints whose latest retained non-test attempt under current endpoint configuration failed; not an availability probe.", values.LastAttemptFailedReceivers},
		{"reconciliation_tenants", "Tenants with retained capacity or terminal-recipient loss history; this is not an unacknowledged-work queue.", values.ReconciliationTenants},
		{"capacity_lost_events", "Database-global cumulative public events retired at notification capacity; do not sum replicas.", values.CapacityLostEvents},
		{"terminal_lost_deliveries", "Database-global cumulative terminal recipient failures excluding synthetic tests; do not sum replicas.", values.TerminalLostDeliveries},
	} {
		name := "open_agentic_catalog_webhook_" + metric.name
		writeMetricHeader(output, name, metric.help, "gauge")
		fmt.Fprintf(output, "%s %d\n", name, metric.value)
	}
}
