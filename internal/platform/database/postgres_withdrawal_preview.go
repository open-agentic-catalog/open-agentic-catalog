package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// publicationPreviewRow keeps the existing immutable row/document decoder while
// reading the SQL-computed encoded size as one additional scalar.
type publicationPreviewRow struct {
	scanner
	bytes int
}

func (r *publicationPreviewRow) Scan(destinations ...any) error {
	return r.scanner.Scan(append(destinations, &r.bytes)...)
}

// ActiveForWithdrawalPreview caps document transfer in the same tenant/host
// selection that observes the active row. An oversized row returns only scalars.
func (p *PostgresPublicationStore) ActiveForWithdrawalPreview(ctx context.Context, tenant string, maxBytes int) (publication.Snapshot, int, error) {
	type result struct {
		snapshot publication.Snapshot
		bytes    int
		missing  bool
	}
	maxBytes = min(maxBytes, publication.MaxPublicationSnapshotBytes)
	if maxBytes < 1 {
		return publication.Snapshot{}, 0, catalog.E(catalog.CodeWorkLimitExceeded, "withdrawal preview exhausted its retained-byte budget")
	}
	value, err := withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (result, error) {
		row := &publicationPreviewRow{scanner: tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,host,profile_id,generation,etag,
CASE WHEN octet_length(document::text)<=$5 THEN document ELSE NULL END,state,created_at,activated_at,octet_length(document::text)
FROM publication_snapshots WHERE tenant_id=$1 AND host=$2 AND document_family=$3 AND state=$4`, tenant, p.host, publicationBundleFamily, publication.SnapshotActive, maxBytes)}
		snapshot, err := scanPublicationSnapshot(row)
		if row.bytes > maxBytes {
			return result{}, catalog.E(catalog.CodeWorkLimitExceeded, "withdrawal preview exceeds its remaining retained-byte budget")
		}
		if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
			return result{missing: true}, nil
		}
		return result{snapshot: snapshot, bytes: row.bytes}, err
	})
	if err != nil {
		return publication.Snapshot{}, 0, err
	}
	if value.missing {
		return publication.Snapshot{}, 0, catalog.E(catalog.CodeNotFound, "active publication snapshot was not found")
	}
	return value.snapshot, value.bytes, nil
}
