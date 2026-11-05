// Restore checks inspect feedback integrity without decoding private comments.
package database

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// scanRestoreFeedback verifies terminal erasure and same-resource version context.
func scanRestoreFeedback(ctx context.Context, tx pgx.Tx, tenant, after string, limit int) ([]restoreRecord, bool, error) {
	rows, err := tx.Query(ctx, `SELECT f.id::text,
 ((f.state IN ('withdrawn','removed') AND (f.comment<>'' OR f.response<>'')) OR
  (f.state='open' AND f.response<>'') OR char_length(f.comment)>4000 OR char_length(f.response)>4000 OR
  (f.version_revision_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM resource_version_revisions r JOIN resource_versions v ON v.tenant_id=r.tenant_id AND v.id=r.resource_version_id WHERE r.tenant_id=f.tenant_id AND r.id=f.version_revision_id AND v.resource_id=f.resource_id)) OR
  (f.release_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM listing_releases l WHERE l.tenant_id=f.tenant_id AND l.id=f.release_id AND l.version_revision_id=f.version_revision_id)))
 FROM item_feedback f WHERE f.tenant_id=$1 AND ($2='' OR f.id>nullif($2,'')::uuid) ORDER BY f.id LIMIT $3`, tenant, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var id string
		var invalid bool
		if err := rows.Scan(&id, &invalid); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		r := restoreRecord{key: id}
		if invalid {
			r.finding = integrityFinding("RESTORE_FEEDBACK_INVALID", "feedback", id, "keep traffic disabled and investigate feedback context or terminal text; do not expose private content")
		}
		records = append(records, r)
	}
	return records, false, rows.Err()
}
