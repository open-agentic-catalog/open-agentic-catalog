package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func (p *PostgresRepository) ReadActiveSummaries(ctx context.Context, tenant string, hosts []string) ([]publication.SnapshotSummary, error) {
	if len(hosts) > 64 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "publication host summary exceeds its bound")
	}
	return withTenantCollectionRead(ctx, p.pool, tenant, func(tx pgx.Tx) ([]publication.SnapshotSummary, error) {
		rows, err := tx.Query(ctx, collectionStatement(ctx, `SELECT id::text,tenant_id::text,host,coalesce(document->>'audience',''),generation,coalesce(document->>'ardProfile',''),coalesce(document->>'aiCatalogProfile',''),state,etag,created_at,activated_at FROM publication_snapshots WHERE tenant_id=$1 AND host=ANY($2::text[]) AND document_family=$3 AND state='active' AND `+collectionPermissionSQL("publication.read", "")+` ORDER BY host LIMIT 65`), tenant, hosts, publicationBundleFamily)
		if err != nil {
			return nil, translateError("select active publication metadata", err)
		}
		defer rows.Close()
		out := []publication.SnapshotSummary{}
		for rows.Next() {
			var item publication.SnapshotSummary
			if err = rows.Scan(&item.ID, &item.TenantID, &item.Host, &item.Audience, &item.Generation, &item.ARDProfile, &item.AICatalogProfile, &item.State, &item.ETag, &item.CreatedAt, &item.ActivatedAt); err != nil {
				return nil, translateError("read active publication metadata", err)
			}
			out = append(out, item)
		}
		if len(out) > 64 {
			return nil, catalog.E(catalog.CodeDependencyUnavailable, "active publication metadata exceeds its bound")
		}
		return out, translateError("iterate active publication metadata", rows.Err())
	})
}
