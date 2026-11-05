// Webhook persistence stores endpoint governance and encrypted signing material,
// then fences delivery against durable parent claims.

package database

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type PostgresWebhookStore struct{ repository *PostgresRepository }

// NewPostgresWebhookStore binds endpoint and delivery persistence to a nonnil
// admitted repository.
func NewPostgresWebhookStore(repository *PostgresRepository) (*PostgresWebhookStore, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL repository is required")
	}
	return &PostgresWebhookStore{repository: repository}, nil
}

// CreateEndpoint validates the creation audit binding before inserting endpoint
// metadata and encrypted signing material atomically.
func (p *PostgresWebhookStore) CreateEndpoint(ctx context.Context, endpoint webhook.Endpoint, records ...audit.Record) (webhook.Endpoint, error) {
	return withTenantTx(ctx, p.repository.pool, endpoint.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (webhook.Endpoint, error) {
		if err := webhook.ValidateEndpointEvidence(records, endpoint, audit.ActionWebhookCreated); err != nil {
			return webhook.Endpoint{}, err
		}
		_, err := tx.Exec(ctx, `
      INSERT INTO webhook_endpoints(id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, endpoint.ID, endpoint.TenantID, endpoint.Name, endpoint.URL, endpoint.EventTypes, endpoint.Status, endpoint.Revision, endpoint.SecretVersion, endpoint.SecretCiphertext, endpoint.CreatedBy, endpoint.CreatedAt, endpoint.UpdatedAt)
		if err != nil {
			return webhook.Endpoint{}, translateError("create webhook endpoint", err)
		}
		if err := appendWebhookAudit(ctx, tx, records); err != nil {
			return webhook.Endpoint{}, err
		}
		return endpoint, nil
	})
}

// GetEndpoint reads one tenant-owned endpoint including ciphertext for internal
// domain use; callers must apply the public disclosure projection.
func (p *PostgresWebhookStore) GetEndpoint(ctx context.Context, tenantID, endpointID string) (webhook.Endpoint, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (webhook.Endpoint, error) {
		return readWebhookEndpoint(tx.QueryRow(ctx, `SELECT `+webhookEndpointColumns+` FROM webhook_endpoints WHERE tenant_id=$1 AND id=$2`, tenantID, endpointID))
	})
}

// ListEndpointPage selects a SQL-bounded endpoint page ordered by exact name and
// ID.
func (p *PostgresWebhookStore) ListEndpointPage(ctx context.Context, tenantID string, request webhook.EndpointListRequest) ([]webhook.Endpoint, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]webhook.Endpoint, error) {
		rows, err := tx.Query(ctx, `SELECT `+webhookEndpointColumns+` FROM webhook_endpoints WHERE tenant_id=$1 AND ($2='' OR name COLLATE "C">$2 COLLATE "C" OR (name COLLATE "C"=$2 COLLATE "C" AND id>nullif($3,'')::uuid)) AND ($5='' OR name COLLATE "C"=$5 COLLATE "C") AND ($6='' OR status=$6) ORDER BY name COLLATE "C",id LIMIT $4`, tenantID, request.AfterName, request.AfterID, request.Limit, request.Name, request.Status)
		if err != nil {
			return nil, translateError("list webhook endpoint page", err)
		}
		defer rows.Close()
		items := make([]webhook.Endpoint, 0)
		for rows.Next() {
			item, scanErr := readWebhookEndpoint(rows)
			if scanErr != nil {
				return nil, scanErr
			}
			items = append(items, item)
		}
		return items, translateError("iterate webhook endpoint page", rows.Err())
	})
}

// ListPendingEndpoints selects at most nine matching active endpoints created
// before the event, excluding terminal deliveries and ordering pending work by
// next attempt.
func (p *PostgresWebhookStore) ListPendingEndpoints(ctx context.Context, event catalog.OutboxEvent, limit int) ([]webhook.Endpoint, error) {
	if limit < 1 || limit > 9 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "webhook dispatch selection limit is invalid")
	}
	return withTenantRead(ctx, p.repository.pool, event.TenantID, func(tx pgx.Tx) ([]webhook.Endpoint, error) {
		rows, err := tx.Query(ctx, `SELECT endpoint.`+strings.ReplaceAll(webhookEndpointColumns, ",", ",endpoint.")+`
FROM webhook_endpoints endpoint
LEFT JOIN webhook_deliveries delivery ON delivery.tenant_id=endpoint.tenant_id AND delivery.endpoint_id=endpoint.id AND delivery.event_id=$2
WHERE endpoint.tenant_id=$1 AND endpoint.status='active' AND endpoint.created_at<=$3
  AND (endpoint.event_types @> ARRAY['*']::text[] OR endpoint.event_types @> ARRAY[$4]::text[])
  AND (delivery.state IS NULL OR delivery.state NOT IN ('succeeded','dead_letter'))
ORDER BY coalesce(delivery.next_attempt_at,$3),endpoint.id LIMIT $5`, event.TenantID, event.ID, event.CreatedAt, event.Type, limit)
		if err != nil {
			return nil, translateError("select pending webhook endpoints", err)
		}
		defer rows.Close()
		items := make([]webhook.Endpoint, 0, limit)
		for rows.Next() {
			item, err := readWebhookEndpoint(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, translateError("iterate pending webhook endpoints", rows.Err())
	})
}

// UpdateEndpoint locks and revision-checks the endpoint, validates evidence for
// its actual transition and atomically updates metadata/ciphertext.
func (p *PostgresWebhookStore) UpdateEndpoint(ctx context.Context, endpoint webhook.Endpoint, expectedRevision int64, records ...audit.Record) (webhook.Endpoint, error) {
	return withTenantTx(ctx, p.repository.pool, endpoint.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (webhook.Endpoint, error) {
		current, err := readWebhookEndpoint(tx.QueryRow(ctx, `SELECT `+webhookEndpointColumns+` FROM webhook_endpoints WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, endpoint.TenantID, endpoint.ID))
		if err != nil {
			return webhook.Endpoint{}, err
		}
		if current.Revision != expectedRevision {
			return webhook.Endpoint{}, catalog.E(catalog.CodePreconditionFailed, "webhook revision does not match")
		}
		if err := webhook.ValidateEndpointEvidence(records, endpoint, webhook.EndpointChangeAction(current, endpoint)); err != nil {
			return webhook.Endpoint{}, err
		}
		item, err := readWebhookEndpoint(tx.QueryRow(ctx, `
      UPDATE webhook_endpoints SET name=$2,url=$3,event_types=$4,status=$5,revision=$6,secret_version=$7,encrypted_secret=$8,updated_at=$9
      WHERE tenant_id=$10 AND id=$1 RETURNING `+webhookEndpointColumns, endpoint.ID, endpoint.Name, endpoint.URL, endpoint.EventTypes, endpoint.Status, endpoint.Revision, endpoint.SecretVersion, endpoint.SecretCiphertext, endpoint.UpdatedAt, endpoint.TenantID))
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "P0001" && pgErr.Message == "webhook endpoint lifecycle transition is invalid" && endpoint.UpdatedAt.Before(current.UpdatedAt) {
				// Classify the actual attempted transition, not a later clock
				// sample. Preserve the SQL rejection and its private cause.
				err = safeerr.At("webhook.endpoint.time_regression", err)
			}
			return webhook.Endpoint{}, err
		}
		if err := appendWebhookAudit(ctx, tx, records); err != nil {
			return webhook.Endpoint{}, err
		}
		return item, nil
	})
}

// GetDelivery resolves the exact tenant, endpoint and event relation rather than a
// globally selected delivery.
func (p *PostgresWebhookStore) GetDelivery(ctx context.Context, tenantID, endpointID, eventID string) (webhook.Delivery, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (webhook.Delivery, error) {
		return readWebhookDelivery(tx.QueryRow(ctx, `SELECT `+webhookDeliveryColumns+` FROM webhook_deliveries WHERE tenant_id=$1 AND endpoint_id=$2 AND event_id=$3`, tenantID, endpointID, eventID))
	})
}

// SaveDelivery persists an unfenced synthetic test result with validated evidence;
// worker outcomes must use SaveClaimedDelivery.
func (p *PostgresWebhookStore) SaveDelivery(ctx context.Context, delivery webhook.Delivery, records ...audit.Record) (webhook.Delivery, error) {
	return withTenantTx(ctx, p.repository.pool, delivery.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (webhook.Delivery, error) {
		return saveWebhookDelivery(ctx, tx, delivery, records)
	})
}

// SaveClaimedDelivery fences only durable worker outcomes. Synthetic HTTP test
// sends have no outbox row and deliberately use SaveDelivery instead.
func (p *PostgresWebhookStore) SaveClaimedDelivery(ctx context.Context, delivery webhook.Delivery, owner string, clock catalog.Clock, record audit.Record) (webhook.Delivery, error) {
	if owner == "" || clock == nil {
		return webhook.Delivery{}, catalog.E(catalog.CodeConflict, "outbox claim evidence is invalid")
	}
	return withTenantTx(ctx, p.repository.pool, delivery.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (webhook.Delivery, error) {
		var eventID string
		err := tx.QueryRow(ctx, `SELECT id FROM outbox_events WHERE tenant_id=$1 AND id=$2 AND lease_owner=$3 AND published_at IS NULL FOR UPDATE`, delivery.TenantID, delivery.EventID, owner).Scan(&eventID)
		if errors.Is(err, pgx.ErrNoRows) {
			return webhook.Delivery{}, catalog.E(catalog.CodeConflict, "outbox event lease is not owned or has expired")
		}
		if err != nil {
			return webhook.Delivery{}, translateError("lock webhook outbox claim", err)
		}
		// Evaluate the database clock in a separate statement AFTER acquiring
		// the row lock, and again after writes that may wait on other locks.
		check := func() error {
			var live bool
			if err := tx.QueryRow(ctx, `SELECT lease_expires_at>greatest($3,clock_timestamp()) FROM outbox_events WHERE tenant_id=$1 AND id=$2`, delivery.TenantID, delivery.EventID, clock.Now()).Scan(&live); err != nil {
				return translateError("check webhook outbox claim", err)
			}
			if !live {
				return catalog.E(catalog.CodeConflict, "outbox event lease is not owned or has expired")
			}
			return nil
		}
		if err := check(); err != nil {
			return webhook.Delivery{}, err
		}
		result, err := saveWebhookDelivery(ctx, tx, delivery, []audit.Record{record})
		if err != nil {
			return webhook.Delivery{}, err
		}
		if err := check(); err != nil {
			return webhook.Delivery{}, err
		}
		return result, nil
	})
}

// saveWebhookDelivery validates evidence before and after upsert so retained
// delivery identity, state and attempt agree with the appended audit.
func saveWebhookDelivery(ctx context.Context, tx pgx.Tx, delivery webhook.Delivery, records []audit.Record) (webhook.Delivery, error) {
	if err := webhook.ValidateDeliveryEvidence(records, delivery); err != nil {
		return webhook.Delivery{}, err
	}
	result, err := readWebhookDelivery(tx.QueryRow(ctx, `
      INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,event_id,event_type,state,attempt,response_status,safe_error,next_attempt_at,created_at,updated_at)
      VALUES ($1,$2,$3,$4,$5,$6,$7,nullif($8,0),$9,$10,$11,$12)
      ON CONFLICT (tenant_id,endpoint_id,event_id) DO UPDATE SET
        state=excluded.state,attempt=excluded.attempt,response_status=excluded.response_status,safe_error=excluded.safe_error,
        next_attempt_at=excluded.next_attempt_at,updated_at=excluded.updated_at
      RETURNING `+webhookDeliveryColumns, delivery.ID, delivery.TenantID, delivery.EndpointID, delivery.EventID, delivery.EventType, delivery.State, delivery.Attempt, delivery.ResponseStatus, delivery.SafeError, delivery.NextAttemptAt, delivery.CreatedAt, delivery.UpdatedAt))
	if err != nil {
		return webhook.Delivery{}, err
	}
	if err := webhook.ValidateDeliveryEvidence(records, result); err != nil {
		return webhook.Delivery{}, err
	}
	if err := appendWebhookAudit(ctx, tx, records); err != nil {
		return webhook.Delivery{}, err
	}
	return result, nil
}

// ListDeliveries selects a bounded endpoint-scoped page and records any requested
// read evidence in the same transaction.
func (p *PostgresWebhookStore) ListDeliveries(ctx context.Context, tenantID string, request webhook.DeliveryListRequest, evidence ...webhook.DeliveryReadEvidence) ([]webhook.Delivery, error) {
	return withTenantTx(ctx, p.repository.pool, tenantID, pgx.TxOptions{}, func(tx pgx.Tx) ([]webhook.Delivery, error) {
		rows, err := tx.Query(ctx, `
      SELECT `+webhookDeliveryColumns+` FROM webhook_deliveries
      WHERE tenant_id=$1 AND endpoint_id=$2
        AND ($3::timestamptz IS NULL OR updated_at<$3 OR (updated_at=$3 AND id<nullif($4,'')::uuid))
        AND ($6='' OR id=nullif($6,'')::uuid)
        AND ($7='' OR event_id=nullif($7,'')::uuid)
        AND ($8='' OR state=$8)
      ORDER BY updated_at DESC,id DESC LIMIT $5`, tenantID, request.EndpointID, request.AfterUpdatedAt, request.AfterID, request.Limit, request.DeliveryID, request.EventID, request.State)
		if err != nil {
			return nil, translateError("list webhook deliveries", err)
		}
		defer rows.Close()
		items := make([]webhook.Delivery, 0)
		for rows.Next() {
			item, scanErr := readWebhookDelivery(rows)
			if scanErr != nil {
				return nil, scanErr
			}
			items = append(items, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, translateError("iterate webhook deliveries", err)
		}
		records, err := webhook.PrepareDeliveryReadEvidence(tenantID, request.EndpointID, items, request.Limit, evidence)
		if err != nil {
			return nil, err
		}
		if err := appendWebhookAudit(ctx, tx, records); err != nil {
			return nil, err
		}
		return items, nil
	})
}

// appendWebhookAudit inserts the optional record after callers have validated
// evidence cardinality and binding.
func appendWebhookAudit(ctx context.Context, tx pgx.Tx, records []audit.Record) error {
	if len(records) == 0 {
		return nil
	}
	return insertAuditEvent(ctx, tx, records[0])
}

const webhookEndpointColumns = `id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at`
const webhookDeliveryColumns = `id,tenant_id,endpoint_id,event_id,event_type,state,attempt,coalesce(response_status,0),safe_error,next_attempt_at,created_at,updated_at`

// readWebhookEndpoint decodes the internal endpoint projection, including
// encrypted secret bytes that must not reach public responses.
func readWebhookEndpoint(row scanner) (webhook.Endpoint, error) {
	var endpoint webhook.Endpoint
	if err := row.Scan(&endpoint.ID, &endpoint.TenantID, &endpoint.Name, &endpoint.URL, &endpoint.EventTypes, &endpoint.Status, &endpoint.Revision, &endpoint.SecretVersion, &endpoint.SecretCiphertext, &endpoint.CreatedBy, &endpoint.CreatedAt, &endpoint.UpdatedAt); err != nil {
		return webhook.Endpoint{}, translateError("read webhook endpoint", err)
	}
	return endpoint, nil
}

// readWebhookDelivery decodes durable attempt status and scheduling metadata,
// translating scan failures into domain errors.
func readWebhookDelivery(row scanner) (webhook.Delivery, error) {
	var delivery webhook.Delivery
	if err := row.Scan(&delivery.ID, &delivery.TenantID, &delivery.EndpointID, &delivery.EventID, &delivery.EventType, &delivery.State, &delivery.Attempt, &delivery.ResponseStatus, &delivery.SafeError, &delivery.NextAttemptAt, &delivery.CreatedAt, &delivery.UpdatedAt); err != nil {
		return webhook.Delivery{}, translateError("read webhook delivery", err)
	}
	return delivery, nil
}
