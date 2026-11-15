package database

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// Both directory and Overview select the same current-revision completed
// attempt. The indexed lateral lookup never loads retained snapshot documents.
const sourceLatestCompletedJoinSQL = ` LEFT JOIN LATERAL (SELECT id,state,completed_at FROM source_snapshots ss WHERE ss.tenant_id=source.tenant_id AND ss.source_id=source.id AND ss.source_revision=source.revision AND ss.completed_at IS NOT NULL AND source.configuration->>'retrievalMode' IN ('indexed','hybrid') ORDER BY completed_at DESC,id DESC LIMIT 1) latest ON true`

// Read only scalar source/snapshot metadata. Never hydrate observation or native documents.
var sourceOverviewFromSQL = ` FROM federation_sources source` + sourceLatestCompletedJoinSQL + `
 LEFT JOIN source_snapshots complete ON complete.tenant_id=source.tenant_id AND complete.source_id=source.id AND complete.id=source.last_complete_snapshot_id AND complete.state='complete'
 WHERE source.tenant_id=$1 AND source.status<>'removed' AND ` + collectionPermissionSQL("source.read", "")

var sourceOverviewRowsSQL = `SELECT source.status,CASE WHEN source.configuration->>'retrievalMode' NOT IN ('indexed','hybrid') THEN 'not_applicable' ELSE coalesce(latest.state,'not_yet_synced') END,coalesce(complete.source_revision<>source.revision,false)` + sourceOverviewFromSQL + ` LIMIT $2`

var sourceOverviewProblemsSQL = `SELECT source.id::text,source.display_name,source.revision,latest.state,latest.completed_at,complete.source_revision,complete.completed_at` + sourceOverviewFromSQL + ` AND source.configuration->>'retrievalMode' IN ('indexed','hybrid') AND latest.state IN ('failed','incomplete') ORDER BY latest.completed_at DESC,source.id LIMIT 5`

func (p *PostgresFederationStore) ReadSourceOverview(ctx context.Context, tenant string) (federation.SourceOverviewRows, error) {
	return withTenantCollectionRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (federation.SourceOverviewRows, error) {
		out := federation.SourceOverviewRows{Problems: []federation.SourceSyncProblem{}}
		rows, err := tx.Query(ctx, collectionStatement(ctx, sourceOverviewRowsSQL), tenant, catalog.OverviewSelectionLimit+1)
		if err != nil {
			return out, translateError("select source summary", err)
		}
		for rows.Next() {
			var item federation.SourceOverviewRow
			if err = rows.Scan(&item.Status, &item.Sync, &item.HistoricalComplete); err != nil {
				rows.Close()
				return out, translateError("read source summary", err)
			}
			out.Items = append(out.Items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, translateError("iterate source summary", err)
		}
		rows, err = tx.Query(ctx, collectionStatement(ctx, sourceOverviewProblemsSQL), tenant)
		if err != nil {
			return out, translateError("select synchronization problems", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item federation.SourceSyncProblem
			var revision *int64
			var completed *time.Time
			if err = rows.Scan(&item.SourceID, &item.DisplayName, &item.Revision, &item.Latest.State, &item.Latest.CompletedAt, &revision, &completed); err != nil {
				return out, translateError("read synchronization problem", err)
			}
			item.Latest.Revision = item.Revision
			if revision != nil && completed != nil {
				item.LastComplete = &federation.SyncSummary{Revision: *revision, State: federation.SnapshotComplete, CompletedAt: *completed}
			}
			out.Problems = append(out.Problems, item)
		}
		return out, translateError("iterate synchronization problems", rows.Err())
	})
}
