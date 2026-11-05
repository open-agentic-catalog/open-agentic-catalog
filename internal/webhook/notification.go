// Authorized notification reads combine Catalog outbox counts with delivery
// state without disclosing signing material or receiver response bodies.

package webhook

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// NotificationState requires current tenant-wide webhook.read_evidence
// permission before returning aggregate notification state.
func (s *Service) NotificationState(ctx context.Context) (catalog.NotificationState, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "webhook.read_evidence")
	if err != nil {
		return catalog.NotificationState{}, err
	}
	return s.store.NotificationState(ctx, actor.TenantID)
}

type notificationReader interface {
	NotificationState(context.Context, string) (catalog.NotificationState, error)
}

// NotificationState combines the tenant's outbox projection with in-memory
// delivery counts through the configured evidence reader.
func (m *MemoryStore) NotificationState(ctx context.Context, tenantID string) (catalog.NotificationState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	reader, ok := m.evidence.(notificationReader)
	if !ok {
		return catalog.NotificationState{}, catalog.E(catalog.CodeInternal, "notification state is unavailable")
	}
	state, err := reader.NotificationState(ctx, tenantID)
	if err != nil {
		return catalog.NotificationState{}, err
	}
	loss := m.notification[tenantID]
	state.DeliveryLossCount = loss.DeliveryLossCount
	if loss.LastDeliveryLossAt != nil {
		at := *loss.LastDeliveryLossAt
		state.LastDeliveryLossAt = &at
	}
	return state, nil
}

// recordNotificationLoss advances retained recipient-loss counters for terminal
// public deliveries without counting synthetic tests.
func (m *MemoryStore) recordNotificationLoss(delivery Delivery) {
	if delivery.State != DeliveryDeadLetter || delivery.EventType == catalog.CanonicalEventType("webhook.test") {
		return
	}
	state := m.notification[delivery.TenantID]
	state.DeliveryLossCount++
	at := delivery.UpdatedAt
	if state.LastDeliveryLossAt == nil || at.After(*state.LastDeliveryLossAt) {
		state.LastDeliveryLossAt = &at
	}
	m.notification[delivery.TenantID] = state
}
