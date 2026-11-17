package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/collections"
)

// Unreadable/ineligible targets are valid retained references, not repair work.
// Findings expose collection IDs only, never authored text or target identities.
func scanRestoreCollections(ctx context.Context, tx pgx.Tx, tenant, after string, limit int) ([]restoreRecord, bool, error) {
	rows, err := tx.Query(ctx, `SELECT id,namespace_id,
 CASE WHEN octet_length(name)<=640 THEN name ELSE '' END,
 CASE WHEN octet_length(description)<=8000 THEN description ELSE '' END,
 octet_length(name)<=640 AND octet_length(description)<=8000,
 revision,created_at,updated_at,
 EXISTS(SELECT 1 FROM namespaces n WHERE n.tenant_id=c.tenant_id AND n.id=c.namespace_id),
 (SELECT count(*) FROM (SELECT id FROM shared_collections x WHERE x.tenant_id=c.tenant_id LIMIT 1001) bounded)<=1000,
 (SELECT count(*) FROM (SELECT id FROM shared_collections x WHERE x.tenant_id=c.tenant_id AND x.namespace_id=c.namespace_id LIMIT 101) bounded)<=100,
 (SELECT count(*) FROM (SELECT listing_id FROM shared_collection_members m WHERE m.tenant_id=c.tenant_id AND m.collection_id=c.id LIMIT 201) bounded)<=200,
 NOT EXISTS(SELECT 1 FROM shared_collection_members m LEFT JOIN catalog_listings l ON l.tenant_id=m.tenant_id AND l.id=m.listing_id WHERE m.tenant_id=c.tenant_id AND m.collection_id=c.id AND l.id IS NULL)
 FROM shared_collections c WHERE tenant_id=$1 AND ($2='' OR id>nullif($2,'')::uuid) ORDER BY id LIMIT $3`, tenant, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := []restoreRecord{}
	for rows.Next() {
		var c collections.Collection
		var textBound, namespace, total, scoped, members, targets bool
		if err := rows.Scan(&c.ID, &c.NamespaceID, &c.Name, &c.Description, &textBound, &c.Revision, &c.CreatedAt, &c.UpdatedAt, &namespace, &total, &scoped, &members, &targets); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		name, description, err := collections.Normalize(c.Name, c.Description)
		r := restoreRecord{key: c.ID, decodedBytes: int64(len(c.Name) + len(c.Description))}
		if err != nil || !textBound || name != c.Name || description != c.Description || c.Revision < 1 || c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) || !namespace || !total || !scoped || !members || !targets {
			r.finding = integrityFinding("RESTORE_SHARED_COLLECTION_INVALID", "shared_collections", c.ID, "keep traffic disabled and investigate owner, text, reference or quota integrity without exposing collection contents")
		}
		records = append(records, r)
	}
	return records, false, rows.Err()
}
