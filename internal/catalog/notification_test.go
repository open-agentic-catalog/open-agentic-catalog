package catalog

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemoryNotificationAdmissionPreservesAtomicEvidence(t *testing.T) {
	repo := NewMemoryRepository()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	record := newAuditInvestigationRecord(t, "00000000-0000-7000-8000-000000000101", "worker", "resource.created", "resource", "00000000-0000-7000-8000-000000000201", "capacity-test", now)
	const extra = 25
	var group sync.WaitGroup
	for n := range PendingWebhookEventLimit + extra {
		group.Go(func() {
			event := OutboxEvent{ID: fmt.Sprint(n), TenantID: record.TenantID, Type: CanonicalEventType("resource.created"), CreatedAt: now}
			if err := repo.AppendEvidence(t.Context(), record, &event); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	state, err := repo.NotificationState(t.Context(), record.TenantID)
	if err != nil || state.PendingEvents != PendingWebhookEventLimit || state.CapacityLossCount != extra || state.LastCapacityLossAt == nil || !state.LastCapacityLossAt.Equal(now) || len(repo.audit) != PendingWebhookEventLimit+extra || len(repo.outbox) != len(repo.audit) {
		t.Fatalf("saturation omitted evidence or over-admitted: %#v %v", state, err)
	}
	*state.LastCapacityLossAt = time.Time{}
	state, _ = repo.NotificationState(t.Context(), record.TenantID)
	if state.LastCapacityLossAt.IsZero() {
		t.Fatal("state read exposed a mutable timestamp")
	}
	for _, event := range []OutboxEvent{
		{ID: "disabled", TenantID: record.TenantID, CreatedAt: now, PublishedAt: &now},
		{ID: "internal", TenantID: record.TenantID, CreatedAt: now, Internal: true},
		{ID: "other", TenantID: "other-tenant", CreatedAt: now},
	} {
		if err := repo.AppendEvidence(t.Context(), record, &event); err != nil {
			t.Fatal(err)
		}
	}
	state, _ = repo.NotificationState(t.Context(), record.TenantID)
	if state.CapacityLossCount != extra {
		t.Fatal("disabled/internal/other tenant work consumed capacity")
	}
	other, _ := repo.NotificationState(t.Context(), "other-tenant")
	if other.PendingEvents != 1 || other.CapacityLossCount != 0 {
		t.Fatal("tenant capacity was not independent")
	}
	invalid := record
	invalid.Action = "invalid"
	if err := repo.AppendEvidence(t.Context(), invalid, &OutboxEvent{TenantID: record.TenantID}); err == nil {
		t.Fatal("invalid audit committed")
	}
	state, _ = repo.NotificationState(t.Context(), record.TenantID)
	if state.CapacityLossCount != extra {
		t.Fatal("failed evidence changed reconciliation state")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := repo.NotificationState(canceled, record.TenantID); err == nil {
		t.Fatal("state read ignored cancellation")
	}
}
