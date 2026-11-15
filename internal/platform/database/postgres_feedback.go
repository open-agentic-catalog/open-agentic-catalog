// Feedback persistence commits private state, minimal audit and command replay together.
package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// PostgresFeedbackStore shares the admitted API repository and transaction coordinator.
type PostgresFeedbackStore struct{ repository *PostgresRepository }

// NewPostgresFeedbackStore never creates another pool or database authority.
func NewPostgresFeedbackStore(repository *PostgresRepository) (*PostgresFeedbackStore, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL repository is required")
	}
	return &PostgresFeedbackStore{repository}, nil
}

const feedbackColumns = `id,tenant_id,membership_id,resource_id,coalesce(release_id::text,''),coalesce(version_revision_id::text,''),category,comment,response,state,revision,created_at,updated_at`

// scanFeedback decodes bounded columns without retaining SQL errors in public output.
func scanFeedback(row pgx.Row) (feedback.Record, error) {
	var r feedback.Record
	err := row.Scan(&r.ID, &r.TenantID, &r.MembershipID, &r.ResourceID, &r.ReleaseID, &r.VersionRevisionID, &r.Category, &r.Comment, &r.Response, &r.State, &r.Revision, &r.CreatedAt, &r.UpdatedAt)
	return r, translateError("read feedback", err)
}

// Get resolves one retained record inside the caller's tenant transaction.
func (p *PostgresFeedbackStore) Get(ctx context.Context, tenant, id string) (feedback.Record, error) {
	return withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (feedback.Record, error) {
		return scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackColumns+` FROM item_feedback WHERE tenant_id=$1 AND id=$2`, tenant, id))
	})
}

// feedbackListStatement owns both ordered SQL pages; plan qualification uses the same statement.
func feedbackListStatement(tenant string, q feedback.ListInput) (string, []any) {
	selection := `SELECT ` + feedbackColumns + ` FROM item_feedback WHERE tenant_id=$1 AND EXISTS(SELECT 1 FROM resources WHERE resources.tenant_id=item_feedback.tenant_id AND resources.id=item_feedback.resource_id AND resources.deleted_at IS NULL)`
	query := selection + ` AND ($2='' OR membership_id=nullif($2,'')::uuid) AND ($3='' OR resource_id=nullif($3,'')::uuid) AND ($4='' OR category=$4) AND ($5='' OR state=$5) AND ($6='' OR id>nullif($6,'')::uuid) ORDER BY id LIMIT $7`
	args := []any{tenant, q.MembershipID, q.ResourceID, q.Category, q.State, q.AfterID, q.Limit}
	if q.Order == feedback.OrderUpdatedDesc {
		state := ` AND ($5='' OR state=$5)`
		if q.State != "" {
			state = ` AND state=$5`
		}
		query = selection + ` AND membership_id=$2::uuid AND ($3='' OR resource_id=nullif($3,'')::uuid) AND ($4='' OR category=$4)` + state + ` AND ($6::timestamptz IS NULL OR (updated_at,id)<($6,$7::uuid)) ORDER BY updated_at DESC,id DESC LIMIT $8`
		args = []any{tenant, q.MembershipID, q.ResourceID, q.Category, q.State, q.AfterUpdatedAt, nullableUUID(q.AfterID), q.Limit}
	}
	return query, args
}

// List applies ownership and filters in SQL before selecting the bounded page.
func (p *PostgresFeedbackStore) List(ctx context.Context, tenant string, q feedback.ListInput) ([]feedback.Record, error) {
	recent := q.Order == feedback.OrderUpdatedDesc
	if q.Limit < 1 || q.Limit > 101 || q.Order != "" && q.Order != feedback.OrderID && !recent || recent && !identity.ValidTenantID(q.MembershipID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "feedback page is invalid")
	}
	return withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) ([]feedback.Record, error) {
		query, args := feedbackListStatement(tenant, q)
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return nil, translateError("list feedback", err)
		}
		defer rows.Close()
		items := []feedback.Record{}
		for rows.Next() {
			r, err := scanFeedback(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, r)
		}
		return items, translateError("iterate feedback", rows.Err())
	})
}

// Save revalidates the locked predecessor and atomically appends audit evidence.
func (p *PostgresFeedbackStore) Save(ctx context.Context, r feedback.Record, expected int64, e audit.Record) (feedback.Record, error) {
	return withTenantTx(ctx, p.repository.pool, r.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (feedback.Record, error) {
		var old feedback.Record
		if expected > 0 {
			var err error
			old, err = scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackColumns+` FROM item_feedback WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, r.TenantID, r.ID))
			if err != nil {
				return feedback.Record{}, err
			}
		}
		if err := feedback.ValidateSave(old, r, expected, e); err != nil {
			return feedback.Record{}, err
		}
		var err error
		if expected == 0 {
			_, err = tx.Exec(ctx, `INSERT INTO item_feedback(id,tenant_id,membership_id,resource_id,release_id,version_revision_id,category,comment,response,state,revision,created_at,updated_at) VALUES($1,$2,$3,$4,nullif($5,'')::uuid,nullif($6,'')::uuid,$7,$8,$9,$10,$11,$12,$13)`, r.ID, r.TenantID, r.MembershipID, r.ResourceID, r.ReleaseID, r.VersionRevisionID, r.Category, r.Comment, r.Response, r.State, r.Revision, r.CreatedAt, r.UpdatedAt)
		} else {
			_, err = tx.Exec(ctx, `UPDATE item_feedback SET category=$3,comment=$4,response=$5,state=$6,revision=$7,updated_at=$8 WHERE tenant_id=$1 AND id=$2`, r.TenantID, r.ID, r.Category, r.Comment, r.Response, r.State, r.Revision, r.UpdatedAt)
		}
		if err != nil {
			return feedback.Record{}, translateError("save feedback", err)
		}
		if err := insertAuditEvent(ctx, tx, e); err != nil {
			return feedback.Record{}, err
		}
		return r, nil
	})
}
