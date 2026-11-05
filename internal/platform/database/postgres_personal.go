package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
)

type PostgresPersonalStore struct{ repository *PostgresRepository }

func NewPostgresPersonalStore(r *PostgresRepository) (*PostgresPersonalStore, error) {
	if r == nil {
		return nil, errors.New("personal library repository is required")
	}
	return &PostgresPersonalStore{r}, nil
}

const personalColumns = `id,input,revision,created_at,updated_at`

func scanPersonal(row scanner) (personal.Item, error) {
	var item personal.Item
	var raw []byte
	if err := row.Scan(&item.ID, &raw, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return item, translateError("read saved item", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&item.Input); err != nil {
		return personal.Item{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "decode saved item", err)
	}
	if err := personal.Validate(item.Input); err != nil {
		return personal.Item{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "validate saved item", err)
	}
	return item, nil
}
func (p *PostgresPersonalStore) Get(ctx context.Context, a identity.Actor, id string) (personal.Item, error) {
	if err := personal.BoundOwner(ctx, a); err != nil {
		return personal.Item{}, err
	}
	return withTenantRead(ctx, p.repository.pool, a.TenantID, func(tx pgx.Tx) (personal.Item, error) {
		return scanPersonal(tx.QueryRow(ctx, `SELECT `+personalColumns+` FROM personal_items WHERE tenant_id=$1 AND membership_id=$2 AND realm_id=$3 AND id=$4`, a.TenantID, a.MembershipID, a.RealmID, id))
	})
}
func (p *PostgresPersonalStore) List(ctx context.Context, a identity.Actor, q personal.ListRequest) ([]personal.Item, error) {
	if err := personal.BoundOwner(ctx, a); err != nil {
		return nil, err
	}
	if q.Limit < 1 || q.Limit > 101 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "saved page is invalid")
	}
	if q.Label != nil {
		if err := personal.ValidateLabel(*q.Label); err != nil {
			return nil, err
		}
	}
	return withTenantRead(ctx, p.repository.pool, a.TenantID, func(tx pgx.Tx) ([]personal.Item, error) {
		rows, err := tx.Query(ctx, `SELECT `+personalColumns+` FROM personal_items WHERE tenant_id=$1 AND membership_id=$2 AND realm_id=$3 AND ($4='' OR input->>'type'=$4) AND ($5='' OR id>nullif($5,'')::uuid) AND ($7::text IS NULL OR (input->>'label') COLLATE "C" = $7 COLLATE "C") ORDER BY id LIMIT $6`, a.TenantID, a.MembershipID, a.RealmID, q.Type, q.AfterID, q.Limit, q.Label)
		if err != nil {
			return nil, translateError("list saved items", err)
		}
		defer rows.Close()
		items := []personal.Item{}
		for rows.Next() {
			i, err := scanPersonal(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, i)
		}
		return items, translateError("iterate saved items", rows.Err())
	})
}
func (p *PostgresPersonalStore) Save(ctx context.Context, a identity.Actor, item personal.Item, expected int64, remove bool, e audit.Record) (personal.Item, error) {
	if err := personal.BoundOwner(ctx, a); err != nil {
		return personal.Item{}, err
	}
	return withTenantTx(ctx, p.repository.pool, a.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (personal.Item, error) {
		// A retained owner row serializes quota decisions across replicas and calls.
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id FROM tenant_memberships WHERE tenant_id=$1 AND id=$2 AND realm_id=$3 AND state='active' AND actor_type='human' FOR UPDATE`, a.TenantID, a.MembershipID, a.RealmID).Scan(&locked); err != nil {
			return personal.Item{}, translateError("lock saved library owner", err)
		}
		var old personal.Item
		if expected > 0 {
			var err error
			old, err = scanPersonal(tx.QueryRow(ctx, `SELECT `+personalColumns+` FROM personal_items WHERE tenant_id=$1 AND membership_id=$2 AND realm_id=$3 AND id=$4 FOR UPDATE`, a.TenantID, a.MembershipID, a.RealmID, item.ID))
			if err != nil {
				return personal.Item{}, err
			}
		}
		if err := personal.ValidateSave(old, item, expected, remove, e, a); err != nil {
			return personal.Item{}, err
		}
		if expected == 0 {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM personal_items WHERE tenant_id=$1 AND membership_id=$2 AND realm_id=$3 LIMIT 200) bounded`, a.TenantID, a.MembershipID, a.RealmID).Scan(&count); err != nil {
				return personal.Item{}, translateError("count saved library", err)
			}
			if count >= personal.MaxItems {
				return personal.Item{}, catalog.E(catalog.CodeConflict, "saved library contains 200 items; remove one first")
			}
		}
		raw, err := json.Marshal(item.Input)
		if err != nil {
			return personal.Item{}, catalog.Wrap(catalog.CodeInternal, "encode saved item", err)
		}
		switch {
		case remove:
			_, err = tx.Exec(ctx, `DELETE FROM personal_items WHERE tenant_id=$1 AND membership_id=$2 AND realm_id=$3 AND id=$4`, a.TenantID, a.MembershipID, a.RealmID, item.ID)
		case expected == 0:
			_, err = tx.Exec(ctx, `INSERT INTO personal_items(tenant_id,membership_id,realm_id,id,input,revision,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, a.TenantID, a.MembershipID, a.RealmID, item.ID, raw, item.Revision, item.CreatedAt, item.UpdatedAt)
		default:
			_, err = tx.Exec(ctx, `UPDATE personal_items SET input=$5,revision=$6,updated_at=$7 WHERE tenant_id=$1 AND membership_id=$2 AND realm_id=$3 AND id=$4`, a.TenantID, a.MembershipID, a.RealmID, item.ID, raw, item.Revision, item.UpdatedAt)
		}
		if err != nil {
			var violation *pgconn.PgError
			if errors.As(err, &violation) && violation.Code == "23514" && violation.ConstraintName == "personal_items_input_check" {
				return personal.Item{}, catalog.E(catalog.CodeInvalidArgument, "saved item exceeds retained input bounds")
			}
			return personal.Item{}, translateError("save personal item", err)
		}
		if err := insertAuditEvent(ctx, tx, e); err != nil {
			return personal.Item{}, err
		}
		return item, nil
	})
}
