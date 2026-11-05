// Notification reconciliation reads bounded pending work and durable loss
// checkpoints without changing or acknowledging them.

package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// NotificationState reads one tenant's bounded reconciliation checkpoint in a
// single statement. It never acknowledges losses or writes state; migrations
// 000024/000025 own counter updates at the outbox/delivery transition boundary.
// PendingEvents is capped, so equality means at least the fixed ceiling
// if an older database already retained excess work before admission existed.
func (p *PostgresWebhookStore) NotificationState(ctx context.Context, tenantID string) (catalog.NotificationState, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (catalog.NotificationState, error) {
		state := catalog.NotificationState{PendingEventLimit: catalog.PendingWebhookEventLimit}
		err := tx.QueryRow(ctx, `
SELECT (SELECT count(*) FROM (SELECT 1 FROM outbox_events
         WHERE tenant_id=$1 AND NOT is_internal AND published_at IS NULL LIMIT $2) bounded),
       coalesce(state.capacity_loss_count,0),state.last_capacity_loss_at,
       coalesce(state.delivery_loss_count,0),state.last_delivery_loss_at
  FROM (SELECT $1::uuid AS tenant_id) tenant
  LEFT JOIN webhook_notification_state state ON state.tenant_id=tenant.tenant_id`, tenantID, catalog.PendingWebhookEventLimit).Scan(
			&state.PendingEvents, &state.CapacityLossCount, &state.LastCapacityLossAt,
			&state.DeliveryLossCount, &state.LastDeliveryLossAt)
		return state, translateError("read webhook reconciliation state", err)
	})
}
