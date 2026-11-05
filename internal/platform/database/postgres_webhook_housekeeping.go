// Webhook retention reclaims eligible terminal deliveries and acknowledged parents
// while preserving active dispatch relationships.

package database

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
)

// Parent acknowledgement is terminal and delivery references prevent parent
// cleanup. Locking the bounded delivery page is sufficient: a dispatcher can
// commit only under a live unpublished parent claim, never an eligible parent.
// The trigger repeats this predicate for direct-SQL writes and deletes.
const disposableWebhookDelivery = `(
  EXISTS (SELECT 1 FROM outbox_events event
           WHERE event.tenant_id=delivery.tenant_id AND event.id=delivery.event_id
             AND event.event_type=delivery.event_type AND event.published_at IS NOT NULL
             AND event.lease_owner IS NULL AND event.lease_expires_at IS NULL)
  OR (delivery.event_type='com.open.agentic.catalog.webhook.test.v1'
      AND delivery.state IN ('succeeded','dead_letter')
      AND NOT EXISTS (SELECT 1 FROM outbox_events event
                      WHERE event.tenant_id=delivery.tenant_id AND event.id=delivery.event_id))
)`

// ReclaimWebhookDeliveries marks and reclaims bounded eligible delivery pages only
// after parent acknowledgement or terminal synthetic completion, rechecking
// retention predicates.
func (p *PostgresHousekeepingStore) ReclaimWebhookDeliveries(ctx context.Context, retention time.Duration, limit int) (housekeeping.Result, error) {
	if err := validHousekeepingTiming(retention, limit); err != nil {
		return housekeeping.Result{}, err
	}
	return withHousekeepingTx(ctx, p.repository.pool, func(tx pgx.Tx) (housekeeping.Result, error) {
		return reclaimWebhookDeliveriesTx(ctx, tx, retention, limit)
	})
}

// reclaimWebhookDeliveriesTx applies validated limits in the caller's bounded transaction.
// Progress is provisional until that transaction commits.
func reclaimWebhookDeliveriesTx(ctx context.Context, tx pgx.Tx, retention time.Duration, limit int) (housekeeping.Result, error) {
	var marked int64
	var markedAge float64
	if err := tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT delivery.tenant_id,delivery.id FROM webhook_deliveries delivery
   WHERE delivery.cleanup_after IS NULL AND `+disposableWebhookDelivery+`
   ORDER BY delivery.updated_at,delivery.tenant_id,delivery.id
   FOR UPDATE OF delivery SKIP LOCKED LIMIT $1
), marked AS (
  UPDATE webhook_deliveries delivery SET cleanup_after=clock_timestamp()+$2::interval
    FROM candidate WHERE delivery.tenant_id=candidate.tenant_id AND delivery.id=candidate.id
     AND delivery.cleanup_after IS NULL AND `+disposableWebhookDelivery+`
   RETURNING delivery.updated_at
)
SELECT count(*),coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(updated_at))),0)
  FROM marked`, limit, retention.String()).Scan(&marked, &markedAge); err != nil {
		return housekeeping.Result{}, translateError("mark webhook deliveries for cleanup", err)
	}
	result, err := scanHousekeepingResult(tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT delivery.tenant_id,delivery.id FROM webhook_deliveries delivery
   WHERE delivery.cleanup_after<=clock_timestamp() AND `+disposableWebhookDelivery+`
   ORDER BY delivery.cleanup_after,delivery.tenant_id,delivery.id
   FOR UPDATE OF delivery SKIP LOCKED LIMIT $1
), deleted AS (
  DELETE FROM webhook_deliveries delivery USING candidate
   WHERE delivery.tenant_id=candidate.tenant_id AND delivery.id=candidate.id
     AND delivery.cleanup_after<=clock_timestamp() AND `+disposableWebhookDelivery+`
   RETURNING delivery.cleanup_after
)
SELECT count(*),0::bigint,count(*)=$1,
       coalesce(greatest(0,extract(epoch FROM clock_timestamp()-min(cleanup_after))),0)
  FROM deleted`, limit), "reclaim webhook deliveries")
	if err != nil {
		return housekeeping.Result{}, err
	}
	return combineHousekeepingProgress(result, marked, markedAge, limit), nil
}
