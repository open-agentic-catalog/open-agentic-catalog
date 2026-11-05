package database

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// scanRestoreTenantSettings bounds the singleton scan and never reports policy values.
func scanRestoreTenantSettings(ctx context.Context, tx pgx.Tx, tenant, after string, limit int) ([]restoreRecord, bool, error) {
	rows, err := tx.Query(ctx, `SELECT tenant_id::text,revision<=0 OR updated_at IS NULL OR coalesce(new_job_max_attempts NOT BETWEEN 1 AND 20,false) OR coalesce(automatic_source_sync_interval_ns NOT BETWEEN 60000000000 AND 86400000000000,false)
FROM tenant_settings WHERE tenant_id=$1 AND ($2='' OR tenant_id>nullif($2,'')::uuid) ORDER BY tenant_id LIMIT $3`, tenant, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, 1)
	for rows.Next() {
		var id string
		var invalid bool
		if err := rows.Scan(&id, &invalid); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: id}
		if invalid {
			record.finding = integrityFinding("RESTORE_TENANT_SETTINGS_INVALID", "tenant_settings", id, "keep traffic disabled and investigate retained tenant policy")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}
