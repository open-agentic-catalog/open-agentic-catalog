// Notification projections expose aggregate outbox state while preserving the
// distinction between public events and internal worker coordination.

package catalog

import (
	"context"
	"time"
)

// PendingWebhookEventLimit bounds public unacknowledged events per tenant,
// including claimed and deferred events. It does not bound fan-out, attempts,
// authoritative data, audit history, retained outbox bytes, or request rate.
// Migration 000024 enforces the same bound at every PostgreSQL insert path.
const PendingWebhookEventLimit = 1000

// NotificationState is a coalesced tenant reconciliation indication.
// Counts are monotonic for this database history, not reset acknowledgements.
// CapacityLossCount counts retired public events; DeliveryLossCount counts
// terminal recipient failures, excludes synthetic tests, and survives delivery
// cleanup. Restoring an older database can move either checkpoint backward.
// Receivers reconcile current authorized state, not an ordered event history.
type NotificationState struct {
	PendingEvents      int64      `json:"pendingEvents"`
	PendingEventLimit  int64      `json:"pendingEventLimit"`
	CapacityLossCount  int64      `json:"capacityLossCount"`
	LastCapacityLossAt *time.Time `json:"lastCapacityLossAt,omitempty"`
	DeliveryLossCount  int64      `json:"deliveryLossCount"`
	LastDeliveryLossAt *time.Time `json:"lastDeliveryLossAt,omitempty"`
}

// NotificationState returns tenant reconciliation counters and the capped count
// of unacknowledged public events.
func (m *MemoryRepository) NotificationState(ctx context.Context, tenantID string) (NotificationState, error) {
	if err := ctx.Err(); err != nil {
		return NotificationState{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := clone(m.notification[tenantID])
	state.PendingEventLimit = PendingWebhookEventLimit
	for _, event := range m.outbox {
		if event.TenantID == tenantID && !event.Internal && event.PublishedAt == nil {
			state.PendingEvents++
			if state.PendingEvents == PendingWebhookEventLimit {
				break
			}
		}
	}
	return state, nil
}

// appendNotification runs under the repository evidence lock. Notification
// saturation must never omit the outbox/audit record or reject domain state.
func (m *MemoryRepository) appendNotification(event OutboxEvent) {
	event = cloneOutboxEvent(event)
	if !event.Internal && event.PublishedAt == nil {
		pending := 0
		for _, existing := range m.outbox {
			if existing.TenantID == event.TenantID && !existing.Internal && existing.PublishedAt == nil {
				pending++
				if pending == PendingWebhookEventLimit {
					break
				}
			}
		}
		if pending >= PendingWebhookEventLimit {
			state := m.notification[event.TenantID]
			state.CapacityLossCount++
			at := event.CreatedAt
			if state.LastCapacityLossAt == nil || at.After(*state.LastCapacityLossAt) {
				state.LastCapacityLossAt = &at
			}
			m.notification[event.TenantID] = state
			event.PublishedAt = &at
		}
	}
	m.outbox = append(m.outbox, event)
}

// Outbox operational fields deliberately have json:"-" and must not be
// cloned through the public JSON projection.
func cloneOutboxEvent(event OutboxEvent) OutboxEvent {
	event.Data = append([]byte(nil), event.Data...)
	if event.PublishedAt != nil {
		at := *event.PublishedAt
		event.PublishedAt = &at
	}
	if event.DispatchAfter != nil {
		at := *event.DispatchAfter
		event.DispatchAfter = &at
	}
	if event.LeaseExpiresAt != nil {
		at := *event.LeaseExpiresAt
		event.LeaseExpiresAt = &at
	}
	return event
}
