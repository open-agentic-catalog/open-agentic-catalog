package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWebhookMetricsAreBoundedAndMissingIsNotZero(t *testing.T) {
	r := New(nil)
	r.ObserveWebhookAttempt("tenant-secret", "accepted", time.Second)
	r.ObserveWebhookAttempt("worker", "https://private.example", time.Second)
	if len(r.webhookAttempts) != 0 {
		t.Fatal("unbounded labels were retained")
	}
	for _, mode := range []string{"test", "worker"} {
		for _, outcome := range []string{"accepted", "retryable_response", "terminal_response", "transport_error", "canceled"} {
			r.ObserveWebhookAttempt(mode, outcome, time.Second)
		}
	}
	r.SetComponent(ComponentWebhookDelivery, ComponentRunning)
	r.SetLifecycle(LifecycleRunning)
	r.SetSnapshotProvider(snapshotProviderFunc(func(context.Context, time.Time) (OperationalSnapshot, error) {
		return OperationalSnapshot{Webhooks: WebhookSnapshot{DispatchableEvents: 2, OldestDispatchableAgeSec: 3, WaitingRetries: 4, LastAttemptFailedReceivers: 1, CapacityLostEvents: 5, TerminalLostDeliveries: 6, ReconciliationTenants: 7}}, nil
	}))
	rendered := string(r.Render(t.Context()))
	for _, expected := range []string{
		`open_agentic_catalog_webhook_attempt_duration_seconds_count{mode="worker",outcome="accepted"} 1`,
		`open_agentic_catalog_webhook_capacity_lost_events 5`,
		`open_agentic_catalog_webhook_terminal_lost_deliveries 6`,
		`open_agentic_catalog_webhook_reconciliation_tenants 7`,
		`open_agentic_catalog_component_state{component="webhook_delivery",state="degraded"} 1`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("missing metric %s", expected)
		}
	}
	if strings.Contains(rendered, "tenant-secret") || strings.Contains(rendered, "private.example") {
		t.Fatal("private metric data exposed")
	}
	r.SetSnapshotProvider(snapshotProviderFunc(func(context.Context, time.Time) (OperationalSnapshot, error) {
		return OperationalSnapshot{}, errors.New("private database failure")
	}))
	rendered = string(r.Render(t.Context()))
	if strings.Contains(rendered, "open_agentic_catalog_webhook_dispatchable_events") || strings.Contains(rendered, "private database failure") || !strings.Contains(rendered, "open_agentic_catalog_operational_snapshot_available 0") {
		t.Fatal("failed snapshot became zero backlog or leaked a cause")
	}
	r.SetSnapshotProvider(snapshotProviderFunc(func(context.Context, time.Time) (OperationalSnapshot, error) { return OperationalSnapshot{}, nil }))
	report, err := r.Snapshot(t.Context())
	if err != nil || report.Components[ComponentWebhookDelivery] != ComponentRunning {
		t.Fatal("receiver observation did not recover")
	}
	r.SetLifecycle(LifecycleStopped)
	report, err = r.Snapshot(t.Context())
	if err != nil || report.Components[ComponentWebhookDelivery] != ComponentStopped {
		t.Fatal("stopped lifecycle was hidden")
	}
	failure := errors.New("private stopped database failure")
	r.SetSnapshotProvider(snapshotProviderFunc(func(context.Context, time.Time) (OperationalSnapshot, error) {
		return OperationalSnapshot{}, failure
	}))
	report, err = r.Snapshot(t.Context())
	if !errors.Is(err, failure) || report.Lifecycle != LifecycleStopped || report.Components[ComponentWebhookDelivery] != ComponentStopped || !report.OperationalConfigured || report.OperationalAvailable || report.Operational != nil {
		t.Fatalf("stopped failed snapshot = %#v, %v", report, err)
	}
	rendered = string(r.Render(t.Context()))
	if !strings.Contains(rendered, `open_agentic_catalog_component_state{component="webhook_delivery",state="stopped"} 1`) || !strings.Contains(rendered, "open_agentic_catalog_operational_snapshot_available 0") || strings.Contains(rendered, "open_agentic_catalog_webhook_dispatchable_events") || strings.Contains(rendered, failure.Error()) {
		t.Fatal("stopped snapshot failure hid lifecycle, invented backlog or exposed its cause")
	}
}
