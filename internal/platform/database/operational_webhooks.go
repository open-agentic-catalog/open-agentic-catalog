// Webhook diagnostics derive dispatch pressure and delivery degradation from
// durable outbox and endpoint relationships.

package database

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// webhookOperationalSnapshot collects fixed global dispatch, retry,
// receiver-failure and loss aggregates without revealing tenant or endpoint
// identities.
func webhookOperationalSnapshot(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, now time.Time) (observability.WebhookSnapshot, error) {
	var result observability.WebhookSnapshot
	err := query.QueryRow(ctx, `
SELECT count(*),coalesce(greatest(0,extract(epoch FROM $1::timestamptz-min(event.created_at)))::bigint,0)
 FROM outbox_events event
 WHERE event.published_at IS NULL AND NOT event.is_internal
   AND (event.lease_expires_at IS NULL OR event.lease_expires_at<=$1)
   AND coalesce(event.dispatch_after,event.created_at)<=$1
   AND EXISTS (
     SELECT 1 FROM webhook_endpoints endpoint
       LEFT JOIN webhook_deliveries delivery ON delivery.tenant_id=event.tenant_id AND delivery.endpoint_id=endpoint.id AND delivery.event_id=event.id
      WHERE endpoint.tenant_id=event.tenant_id AND endpoint.status='active' AND endpoint.created_at<=event.created_at
        AND (endpoint.event_types @> ARRAY['*']::text[] OR endpoint.event_types @> ARRAY[event.event_type])
        AND (delivery.id IS NULL OR delivery.state IN ('pending','waiting') AND (delivery.next_attempt_at IS NULL OR delivery.next_attempt_at<=$1))
   )`, now).Scan(&result.DispatchableEvents, &result.OldestDispatchableAgeSec)
	if err != nil {
		return result, translateError("collect dispatchable webhook aggregates", err)
	}
	err = query.QueryRow(ctx, `
SELECT count(*) FROM webhook_deliveries delivery
 JOIN outbox_events event ON event.tenant_id=delivery.tenant_id AND event.id=delivery.event_id AND event.event_type=delivery.event_type
 JOIN webhook_endpoints endpoint ON endpoint.tenant_id=delivery.tenant_id AND endpoint.id=delivery.endpoint_id
 WHERE delivery.state='waiting' AND event.published_at IS NULL AND NOT event.is_internal
   AND endpoint.status='active' AND endpoint.created_at<=event.created_at
   AND (endpoint.event_types @> ARRAY['*']::text[] OR endpoint.event_types @> ARRAY[event.event_type])`).Scan(&result.WaitingRetries)
	if err != nil {
		return result, translateError("collect webhook retry aggregates", err)
	}
	err = query.QueryRow(ctx, `
SELECT count(*) FROM webhook_endpoints endpoint
 JOIN LATERAL (
   SELECT delivery.state FROM webhook_deliveries delivery
    WHERE delivery.tenant_id=endpoint.tenant_id AND delivery.endpoint_id=endpoint.id
      AND delivery.event_type<>'com.open.agentic.catalog.webhook.test.v1' AND delivery.updated_at>=endpoint.updated_at
    ORDER BY delivery.updated_at DESC,delivery.id DESC LIMIT 1
 ) latest ON true
 WHERE endpoint.status='active' AND latest.state IN ('waiting','dead_letter')`).Scan(&result.LastAttemptFailedReceivers)
	if err != nil {
		return result, translateError("collect webhook receiver outcome aggregates", err)
	}
	err = query.QueryRow(ctx, `
SELECT count(*) FILTER(WHERE capacity_loss_count>0 OR delivery_loss_count>0),
       coalesce(sum(capacity_loss_count),0)::bigint,coalesce(sum(delivery_loss_count),0)::bigint
 FROM webhook_notification_state`).Scan(&result.ReconciliationTenants, &result.CapacityLostEvents, &result.TerminalLostDeliveries)
	return result, translateError("collect webhook reconciliation aggregates", err)
}
