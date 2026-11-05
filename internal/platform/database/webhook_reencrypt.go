// Key maintenance re-encrypts bounded tenant pages atomically, preserving
// signing-secret identity and returning safe progress evidence.

package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

// WebhookReencryptResult is safe progress evidence, never key or endpoint data.
type WebhookReencryptResult struct {
	TenantID     string `json:"tenantId"`
	Verified     int    `json:"verified"`
	Changed      int    `json:"changed"`
	AfterID      string `json:"afterId,omitempty"`
	HasMore      bool   `json:"hasMore"`
	AuditEventID string `json:"auditEventId,omitempty"`
}

// ValidateWebhookReencryptInput checks tenant/cursor UUIDs, the 100-endpoint batch
// ceiling and required audit reason without reading keys.
func ValidateWebhookReencryptInput(tenantID, afterID, reason string, limit int) error {
	if !uuidText(tenantID) || (afterID != "" && !uuidText(afterID)) || limit < 1 || limit > 100 {
		return errors.New("webhook re-encryption requires a UUID tenant, an optional UUID cursor, and a limit between 1 and 100")
	}
	_, err := audit.NormalizeReason(audit.ActionWebhookSecretsReencrypted, reason)
	return err
}

// ReencryptWebhookSecrets performs one atomic, bounded maintenance batch. The
// operator must stop ALL API and worker roles before the first batch and keep
// them stopped until every tenant has been verified with the replacement key.
// A cursor is emitted only after commit; repeating a batch is safe.
func ReencryptWebhookSecrets(ctx context.Context, pool *pgxpool.Pool, tenantID, afterID, reason string, limit int, oldKey, newKey []byte) (WebhookReencryptResult, error) {
	result := WebhookReencryptResult{TenantID: tenantID}
	if pool == nil {
		return result, errors.New("PostgreSQL pool is required")
	}
	if err := ValidateWebhookReencryptInput(tenantID, afterID, reason, limit); err != nil {
		return result, err
	}
	if len(oldKey) != 32 || len(newKey) != 32 || bytes.Equal(oldKey, newKey) {
		return result, errors.New("webhook re-encryption requires two distinct 32-byte keys")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return result, fmt.Errorf("begin webhook re-encryption: %w", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	var authorized bool
	if err := tx.QueryRow(ctx, `SELECT session_user=current_user AND current_user=pg_get_userbyid(relowner) FROM pg_class WHERE oid=to_regclass('webhook_endpoints')`).Scan(&authorized); err != nil || !authorized {
		return result, errors.New("webhook re-encryption requires the directly authenticated Catalog table owner")
	}
	if err := setTenantContext(ctx, tx, tenantID); err != nil {
		return result, err
	}
	var tenantExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1)`, tenantID).Scan(&tenantExists); err != nil || !tenantExists {
		return result, errors.New("webhook re-encryption tenant was not found")
	}
	// Serialize batches and refuse concurrent endpoint writes without waiting.
	// This does not replace stopping runtime processes: worker reads can send.
	if _, err := tx.Exec(ctx, `LOCK TABLE webhook_endpoints IN SHARE ROW EXCLUSIVE MODE NOWAIT`); err != nil {
		return result, errors.New("webhook re-encryption requires quiescent endpoint writers")
	}
	result, err = reencryptWebhookSecretBatch(ctx, tx, tenantID, afterID, reason, limit, oldKey, newKey)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WebhookReencryptResult{}, fmt.Errorf("commit webhook re-encryption: %w", err)
	}
	return result, nil
}

// reencryptWebhookSecretBatch processes validated input under the caller's
// authorized, tenant-scoped transaction and endpoint-write lock. Its progress
// is provisional until the caller commits; it does not own transaction cleanup.
func reencryptWebhookSecretBatch(ctx context.Context, tx pgx.Tx, tenantID, afterID, reason string, limit int, oldKey, newKey []byte) (WebhookReencryptResult, error) {
	result := WebhookReencryptResult{TenantID: tenantID}
	rows, err := tx.Query(ctx, `SELECT id::text,encrypted_secret FROM webhook_endpoints WHERE tenant_id=$1 AND ($2='' OR id>nullif($2,'')::uuid) ORDER BY id LIMIT $3 FOR UPDATE`, tenantID, afterID, limit+1)
	if err != nil {
		return result, fmt.Errorf("select webhook re-encryption batch: %w", err)
	}
	type selectedSecret struct {
		id         string
		ciphertext []byte
	}
	selected := make([]selectedSecret, 0, limit+1)
	for rows.Next() {
		var item selectedSecret
		if err := rows.Scan(&item.id, &item.ciphertext); err != nil {
			rows.Close()
			return result, fmt.Errorf("read webhook re-encryption batch: %w", err)
		}
		selected = append(selected, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	result.HasMore = len(selected) > limit
	selected = selected[:min(len(selected), limit)]
	for _, item := range selected {
		replacement, changed, err := webhook.ReencryptSecret(item.ciphertext, oldKey, newKey)
		if err != nil {
			return WebhookReencryptResult{}, err
		}
		if changed {
			if _, err := tx.Exec(ctx, `UPDATE webhook_endpoints SET encrypted_secret=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, item.id, replacement); err != nil {
				return WebhookReencryptResult{}, fmt.Errorf("replace webhook ciphertext: %w", err)
			}
			result.Changed++
		}
		result.Verified++
		result.AfterID = item.id
	}
	if len(selected) > 0 {
		var at time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
			return WebhookReencryptResult{}, err
		}
		record, err := newDatabaseAuditRecord(ctx, tx, tenantID, audit.ActionWebhookSecretsReencrypted, "webhook_secret_batch", selected[0].id, reason, "", map[string]any{"changed": result.Changed, "verified": result.Verified, "firstId": selected[0].id, "lastId": result.AfterID}, at)
		if err != nil {
			return WebhookReencryptResult{}, err
		}
		if err := insertAuditEvent(ctx, tx, record); err != nil {
			return WebhookReencryptResult{}, err
		}
		result.AuditEventID = record.ID
	}
	return result, nil
}
