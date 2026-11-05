package database

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

var overviewJobTargetSQL = strings.NewReplacer("$5", "$2", "$6", "$3", "$7", "$4", "$8", "$5").Replace(jobTargetCollectionSQL)

const overviewJobDueSQL = `CASE WHEN state IN ('queued','waiting') THEN not_before WHEN state='running' THEN lease_expires_at END`

func (p *PostgresJobStore) ReadJobOverview(ctx context.Context, tenant string, request jobs.ListRequest, at time.Time) (jobs.OverviewRows, error) {
	return withTenantCollectionRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (jobs.OverviewRows, error) {
		base := ` FROM jobs WHERE tenant_id=$1 AND ` + jobCollectionSQL + ` AND ` + overviewJobTargetSQL
		args := []any{tenant, request.Targets.Enforced, request.Targets.PublicationHosts, request.Targets.DefaultPublicationHost, request.Targets.SemanticProfile}
		out := jobs.OverviewRows{}
		rows, err := tx.Query(ctx, collectionStatement(ctx, `SELECT kind,state`+base+` LIMIT $6`), append(args, catalog.OverviewSelectionLimit+1)...)
		if err != nil {
			return out, translateError("select Job summary", err)
		}
		for rows.Next() {
			var item jobs.OverviewItem
			if err = rows.Scan(&item.Kind, &item.State); err != nil {
				rows.Close()
				return out, translateError("read Job summary", err)
			}
			out.Items = append(out.Items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, translateError("iterate Job summary", err)
		}
		due := base + ` AND ` + overviewJobDueSQL + ` <= $6`
		err = tx.QueryRow(ctx, collectionStatement(ctx, `SELECT count(*) FROM (SELECT id`+due+` LIMIT $7) selected`), append(args, at, catalog.OverviewSelectionLimit+1)...).Scan(&out.Due)
		if err != nil {
			return out, translateError("count due Jobs", err)
		}
		var oldest time.Time
		err = tx.QueryRow(ctx, collectionStatement(ctx, `SELECT `+overviewJobDueSQL+due+` ORDER BY 1,id LIMIT 1`), append(args, at)...).Scan(&oldest)
		if err == nil {
			out.OldestDueAt = &oldest
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return out, translateError("read oldest due Job", err)
		}
		return out, nil
	})
}
