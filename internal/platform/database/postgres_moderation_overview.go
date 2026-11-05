package database

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"time"
)

const feedbackOverviewPredicate = `tenant_id=$1 AND state='open' AND EXISTS(SELECT 1 FROM resources r WHERE r.tenant_id=item_feedback.tenant_id AND r.id=item_feedback.resource_id AND r.deleted_at IS NULL)`

func (p *PostgresFeedbackStore) ReadOverview(ctx context.Context, tenant string) (feedback.OverviewRows, error) {
	return withTenantCollectionRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (feedback.OverviewRows, error) {
		out := feedback.OverviewRows{Categories: []string{}}
		predicate := feedbackOverviewPredicate + ` AND ` + collectionPermissionSQL("feedback.moderate", "")
		rows, err := tx.Query(ctx, collectionStatement(ctx, `SELECT category FROM item_feedback WHERE `+predicate+` LIMIT $2`), tenant, catalog.OverviewSelectionLimit+1)
		if err != nil {
			return out, translateError("select open feedback summary", err)
		}
		for rows.Next() {
			var category string
			if err = rows.Scan(&category); err != nil {
				rows.Close()
				return out, translateError("read feedback summary", err)
			}
			out.Categories = append(out.Categories, category)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, translateError("iterate feedback summary", err)
		}
		rows, err = tx.Query(ctx, collectionStatement(ctx, `SELECT created_at FROM item_feedback WHERE `+predicate+` ORDER BY created_at,id LIMIT 1`), tenant)
		if err != nil {
			return out, translateError("select oldest open feedback", err)
		}
		defer rows.Close()
		if rows.Next() {
			var at time.Time
			if err = rows.Scan(&at); err != nil {
				return out, translateError("read oldest open feedback", err)
			}
			out.OldestOpenAt = &at
		}
		return out, translateError("iterate oldest open feedback", rows.Err())
	})
}

func (p *PostgresRatingsStore) ReadOverview(ctx context.Context, tenant string) ([]string, error) {
	return withTenantCollectionRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) ([]string, error) {
		rows, err := tx.Query(ctx, collectionStatement(ctx, `SELECT state FROM item_ratings WHERE tenant_id=$1 AND `+collectionPermissionSQL("ratings.moderate", "")+` LIMIT $2`), tenant, catalog.OverviewSelectionLimit+1)
		if err != nil {
			return nil, translateError("select rating moderation summary", err)
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var state string
			if err = rows.Scan(&state); err != nil {
				return nil, translateError("read rating moderation summary", err)
			}
			out = append(out, state)
		}
		return out, translateError("iterate rating moderation summary", rows.Err())
	})
}
