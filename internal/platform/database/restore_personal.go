package database

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
)

// Restore reports IDs and fixed findings, never a person's label or query. A
// stale/unreadable reference remains a valid preference, not a broken FK.
func scanRestorePersonal(ctx context.Context, tx pgx.Tx, tenant, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 16)
	rows, err := tx.Query(ctx, `SELECT i.id::text,CASE WHEN octet_length(i.input::text)<=16384 THEN i.input ELSE NULL END,i.revision,i.created_at,i.updated_at,
 EXISTS(SELECT 1 FROM tenant_memberships m WHERE m.tenant_id=i.tenant_id AND m.id=i.membership_id AND m.realm_id=i.realm_id AND m.actor_type='human'),
 (SELECT count(*) FROM (SELECT id FROM personal_items p WHERE p.tenant_id=i.tenant_id AND p.membership_id=i.membership_id AND p.realm_id=i.realm_id LIMIT 201) bounded)<=200
 FROM personal_items i WHERE i.tenant_id=$1 AND ($2='' OR i.id>nullif($2,'')::uuid) ORDER BY i.id LIMIT $3`, tenant, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := []restoreRecord{}
	for rows.Next() {
		var id string
		var raw []byte
		var revision int64
		var created, updated time.Time
		var human, quota bool
		if err := rows.Scan(&id, &raw, &revision, &created, &updated, &human, &quota); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		var in personal.Input
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&in)
		r := restoreRecord{key: id, decodedBytes: int64(len(raw))}
		if decodeErr != nil || personal.Validate(in) != nil || revision < 1 || created.IsZero() || updated.Before(created) || !human || !quota {
			r.finding = integrityFinding("RESTORE_PERSONAL_ITEM_INVALID", "personal_library", id, "keep traffic disabled and investigate private item shape, ownership or quota without exposing labels or criteria")
		}
		records = append(records, r)
	}
	return records, false, rows.Err()
}
