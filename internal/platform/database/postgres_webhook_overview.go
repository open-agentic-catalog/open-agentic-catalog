package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

var webhookOverviewFailuresSQL = `SELECT e.id::text,e.name,latest.updated_at,latest.id::text,latest.state FROM webhook_endpoints e
 JOIN LATERAL (SELECT id,state,updated_at FROM webhook_deliveries d
 WHERE d.tenant_id=e.tenant_id AND d.endpoint_id=e.id AND d.event_type<>$2 AND d.attempt>0 AND d.updated_at>=e.updated_at
 ORDER BY d.updated_at DESC,d.id DESC LIMIT 1) latest ON true
 WHERE e.tenant_id=$1 AND e.status='active' AND ` + collectionPermissionSQL("webhook.read_evidence", "") + ` AND latest.state IN ('waiting','dead_letter')
 ORDER BY latest.updated_at DESC,e.id LIMIT 5`

func (p *PostgresWebhookStore) ReadEndpointOverview(ctx context.Context, tenant string) ([]string, error) {
	return withTenantCollectionRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) ([]string, error) {
		rows, err := tx.Query(ctx, collectionStatement(ctx, `SELECT status FROM webhook_endpoints WHERE tenant_id=$1 AND `+collectionPermissionSQL("webhook.read", "")+` LIMIT $2`), tenant, catalog.OverviewSelectionLimit+1)
		if err != nil {
			return nil, translateError("select webhook endpoint summary", err)
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var state string
			if err = rows.Scan(&state); err != nil {
				return nil, translateError("read webhook endpoint summary", err)
			}
			out = append(out, state)
		}
		return out, translateError("iterate webhook endpoint summary", rows.Err())
	})
}

func (p *PostgresWebhookStore) ReadDeliveryOverview(ctx context.Context, tenant string) (webhook.DeliveryOverviewRows, error) {
	return withTenantCollectionRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (webhook.DeliveryOverviewRows, error) {
		out := webhook.DeliveryOverviewRows{States: []string{}, FailedEndpoints: []webhook.FailedEndpoint{}}
		rows, err := tx.Query(ctx, collectionStatement(ctx, `SELECT state FROM webhook_deliveries WHERE tenant_id=$1 AND event_type<>$2 AND `+collectionPermissionSQL("webhook.read_evidence", "")+` LIMIT $3`), tenant, catalog.CanonicalEventType("webhook.test"), catalog.OverviewSelectionLimit+1)
		if err != nil {
			return out, translateError("select webhook delivery summary", err)
		}
		for rows.Next() {
			var state string
			if err = rows.Scan(&state); err != nil {
				rows.Close()
				return out, translateError("read webhook delivery summary", err)
			}
			out.States = append(out.States, state)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, translateError("iterate webhook delivery summary", err)
		}
		rows, err = tx.Query(ctx, collectionStatement(ctx, webhookOverviewFailuresSQL), tenant, catalog.CanonicalEventType("webhook.test"))
		if err != nil {
			return out, translateError("select webhook attempt failures", err)
		}
		defer rows.Close()
		for rows.Next() {
			var row webhook.FailedEndpoint
			if err = rows.Scan(&row.EndpointID, &row.Name, &row.AttemptedAt, &row.DeliveryID, &row.State); err != nil {
				return out, translateError("read webhook attempt failure", err)
			}
			out.FailedEndpoints = append(out.FailedEndpoints, row)
		}
		return out, translateError("iterate webhook attempt failures", rows.Err())
	})
}
