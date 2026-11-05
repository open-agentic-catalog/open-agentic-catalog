package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// alias is a code-owned SQL alias, never a request value.
func unreleasedSubmissionSQL(alias string) string {
	return `NOT EXISTS(SELECT 1 FROM listing_releases release WHERE release.tenant_id=$1 AND release.submission_id=` + alias + `.id)`
}

var reviewOverviewFromSQL = ` FROM review_submissions s JOIN catalog_listings p ON p.tenant_id=s.tenant_id AND p.id=s.listing_id JOIN resources r ON r.tenant_id=p.tenant_id AND r.id=p.resource_id WHERE s.tenant_id=$1 AND r.deleted_at IS NULL AND ` + collectionPermissionSQL("resource.read_history", "p.namespace_id")

var reviewPendingTimesSQL = `SELECT s.submitted_at` + reviewOverviewFromSQL + ` AND s.state='submitted' LIMIT $2`

var reviewOldestPendingSQL = `SELECT s.submitted_at` + reviewOverviewFromSQL + ` AND s.state='submitted' ORDER BY s.submitted_at,s.id LIMIT 1`

var reviewDecisionTimesSQL = `SELECT s.submitted_at,s.decided_at,s.state` + reviewOverviewFromSQL + ` AND s.decided_at >= $2 AND s.decided_at < $3 LIMIT $4`

func (p *PostgresRepository) ReadReviewTimes(ctx context.Context, tenant string, start, end time.Time) (catalog.ReviewTimes, error) {
	return withTenantCollectionRead(ctx, p.pool, tenant, func(tx pgx.Tx) (catalog.ReviewTimes, error) {
		out := catalog.ReviewTimes{}
		// Every population is bounded before timestamps reach the service.
		rows, err := tx.Query(ctx, collectionStatement(ctx, reviewPendingTimesSQL), tenant, catalog.OverviewSelectionLimit+1)
		if err != nil {
			return out, translateError("select pending review times", err)
		}
		for rows.Next() {
			var stamp time.Time
			if err = rows.Scan(&stamp); err != nil {
				rows.Close()
				return out, translateError("read pending review time", err)
			}
			out.Pending = append(out.Pending, stamp)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, translateError("iterate pending review times", err)
		}
		var oldest time.Time
		err = tx.QueryRow(ctx, collectionStatement(ctx, reviewOldestPendingSQL), tenant).Scan(&oldest)
		if err == nil {
			out.OldestPendingAt = &oldest
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return out, translateError("read oldest pending review", err)
		}
		err = tx.QueryRow(ctx, collectionStatement(ctx, `SELECT count(*) FROM (SELECT s.id`+reviewOverviewFromSQL+` AND s.state='approved' AND `+unreleasedSubmissionSQL("s")+` LIMIT $2) selected`), tenant, catalog.OverviewSelectionLimit+1).Scan(&out.ApprovedUnreleased)
		if err != nil {
			return out, translateError("count approved unreleased reviews", err)
		}
		rows, err = tx.Query(ctx, collectionStatement(ctx, `SELECT s.submitted_at`+reviewOverviewFromSQL+` AND s.submitted_at >= $2 AND s.submitted_at < $3 LIMIT $4`), tenant, start, end, catalog.OverviewSelectionLimit+1)
		if err != nil {
			return out, translateError("select submitted review times", err)
		}
		for rows.Next() {
			var stamp time.Time
			if err = rows.Scan(&stamp); err != nil {
				rows.Close()
				return out, translateError("read submitted review time", err)
			}
			out.Submitted = append(out.Submitted, stamp)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, translateError("iterate submitted review times", err)
		}
		rows, err = tx.Query(ctx, collectionStatement(ctx, reviewDecisionTimesSQL), tenant, start, end, catalog.OverviewSelectionLimit+1)
		if err != nil {
			return out, translateError("select review decision times", err)
		}
		defer rows.Close()
		for rows.Next() {
			var decision catalog.ReviewDecisionTime
			if err = rows.Scan(&decision.SubmittedAt, &decision.DecidedAt, &decision.State); err != nil {
				return out, translateError("read review decision time", err)
			}
			out.Decisions = append(out.Decisions, decision)
		}
		return out, translateError("iterate review decision times", rows.Err())
	})
}
