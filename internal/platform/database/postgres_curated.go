package database

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/collections"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type PostgresCollectionStore struct{ repository *PostgresRepository }

func NewPostgresCollectionStore(r *PostgresRepository) (*PostgresCollectionStore, error) {
	if r == nil {
		return nil, errors.New("collection repository is required")
	}
	return &PostgresCollectionStore{r}, nil
}

const curatedColumns = `id,namespace_id,name,description,revision,created_at,updated_at`

func scanCollection(row scanner) (collections.Collection, error) {
	var c collections.Collection
	err := row.Scan(&c.ID, &c.NamespaceID, &c.Name, &c.Description, &c.Revision, &c.CreatedAt, &c.UpdatedAt)
	return c, translateError("read shared collection", err)
}
func collectionActor(ctx context.Context, a identity.Actor) error {
	current, err := collections.Actor(ctx)
	if err != nil {
		return err
	}
	if current.TenantID != a.TenantID || current.CommandPrincipal() != a.CommandPrincipal() {
		return catalog.E(catalog.CodeForbidden, "collection principal does not match")
	}
	return nil
}
func readSharedCollection(ctx context.Context, tx pgx.Tx, tenant, id string) (collections.Collection, error) {
	return scanCollection(tx.QueryRow(ctx, `SELECT `+curatedColumns+` FROM shared_collections WHERE tenant_id=$1 AND id=$2 AND `+collectionPermissionSQL("collection.read", "namespace_id"), tenant, id))
}
func (p *PostgresCollectionStore) Get(ctx context.Context, a identity.Actor, id string) (collections.Collection, error) {
	if err := collectionActor(ctx, a); err != nil {
		return collections.Collection{}, err
	}
	return withTenantCollectionRead(ctx, p.repository.pool, a.TenantID, func(tx pgx.Tx) (collections.Collection, error) { return readSharedCollection(ctx, tx, a.TenantID, id) })
}
func (p *PostgresCollectionStore) List(ctx context.Context, a identity.Actor, q collections.ListRequest) ([]collections.Collection, error) {
	if err := collectionActor(ctx, a); err != nil {
		return nil, err
	}
	if q.Limit < 1 || q.Limit > 101 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "collection page is invalid")
	}
	return withTenantCollectionRead(ctx, p.repository.pool, a.TenantID, func(tx pgx.Tx) ([]collections.Collection, error) {
		rows, err := tx.Query(ctx, `SELECT `+curatedColumns+` FROM shared_collections WHERE tenant_id=$1 AND ($2='' OR namespace_id=nullif($2,'')::uuid) AND ($3='' OR id>nullif($3,'')::uuid) AND `+collectionPermissionSQL("collection.read", "namespace_id")+` ORDER BY id LIMIT $4`, a.TenantID, q.NamespaceID, q.AfterID, q.Limit)
		if err != nil {
			return nil, translateError("list shared collections", err)
		}
		defer rows.Close()
		out := []collections.Collection{}
		for rows.Next() {
			c, err := scanCollection(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, translateError("iterate shared collections", rows.Err())
	})
}

const curatedTargetSQL = `SELECT listing.namespace_id FROM catalog_listings listing JOIN listing_releases release ON release.tenant_id=listing.tenant_id AND release.id=listing.current_release_id AND release.state IN ('published','deprecated') JOIN resources resource ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id AND resource.deleted_at IS NULL WHERE listing.tenant_id=$1 AND listing.id=$2`

func readCollectionTarget(ctx context.Context, tx pgx.Tx, tenant, id string) (string, error) {
	var namespace string
	err := tx.QueryRow(ctx, curatedTargetSQL, tenant, id).Scan(&namespace)
	return namespace, translateError("read eligible local Listing", err)
}
func (p *PostgresCollectionStore) Target(ctx context.Context, a identity.Actor, id string) (string, error) {
	if err := collectionActor(ctx, a); err != nil {
		return "", err
	}
	return withTenantRead(ctx, p.repository.pool, a.TenantID, func(tx pgx.Tx) (string, error) { return readCollectionTarget(ctx, tx, a.TenantID, id) })
}
func (p *PostgresCollectionStore) Members(ctx context.Context, a identity.Actor, id string, q collections.MemberRequest) (collections.MemberPage, error) {
	if err := collectionActor(ctx, a); err != nil {
		return collections.MemberPage{}, err
	}
	if q.Limit < 0 || q.Limit > 101 {
		return collections.MemberPage{}, catalog.E(catalog.CodeInvalidArgument, "collection member page is invalid")
	}
	return withTenantCollectionRead(ctx, p.repository.pool, a.TenantID, func(tx pgx.Tx) (collections.MemberPage, error) {
		item, err := readSharedCollection(ctx, tx, a.TenantID, id)
		if err != nil {
			return collections.MemberPage{}, err
		}
		var generation int64
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT generation FROM catalog_search_generations WHERE tenant_id=$1),0)`, a.TenantID).Scan(&generation); err != nil {
			return collections.MemberPage{}, translateError("read collection release generation", err)
		}
		out := collections.MemberPage{Collection: item, Generation: strconv.FormatInt(generation, 10), Items: []catalog.CatalogSummary{}}
		if err := collections.CheckSnapshot(item, out.Generation, q); err != nil {
			return collections.MemberPage{}, err
		}
		if q.Limit == 0 {
			return out, nil
		}
		rows, err := tx.Query(ctx, `SELECT release.id FROM shared_collection_members member JOIN catalog_listings listing ON listing.tenant_id=member.tenant_id AND listing.id=member.listing_id JOIN listing_releases release ON release.tenant_id=listing.tenant_id AND release.id=listing.current_release_id AND release.state IN ('published','deprecated') JOIN resources resource ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id AND resource.deleted_at IS NULL WHERE member.tenant_id=$1 AND member.collection_id=$2 AND ($3='' OR member.listing_id>nullif($3,'')::uuid) AND `+collectionPermissionSQL("resource.read", "listing.namespace_id")+` ORDER BY member.listing_id LIMIT $4`, a.TenantID, id, q.AfterID, q.Limit)
		if err != nil {
			return out, translateError("select collection members", err)
		}
		ids := []string{}
		for rows.Next() {
			var release string
			if err := rows.Scan(&release); err != nil {
				rows.Close()
				return out, translateError("read collection member identity", err)
			}
			ids = append(ids, release)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, translateError("iterate collection members", err)
		}
		summaries, err := readSearchRecords(ctx, tx, a.TenantID, ids)
		if err != nil {
			return out, err
		}
		for _, release := range ids {
			record, ok := summaries[release]
			if !ok {
				return out, catalog.E(catalog.CodeDependencyUnavailable, "collection summary is unavailable")
			}
			out.Items = append(out.Items, *record)
		}
		return out, nil
	})
}
func (p *PostgresCollectionStore) Save(ctx context.Context, a identity.Actor, next collections.Collection, expected int64, op collections.Operation, listingID string, e audit.Record) (collections.Collection, error) {
	if err := collectionActor(ctx, a); err != nil {
		return collections.Collection{}, err
	}
	return withTenantTx(ctx, p.repository.pool, a.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (collections.Collection, error) {
		if op == collections.Create || op == collections.Delete {
			if err := acquireCatalogStateLock(ctx, tx, "shared-collections", a.TenantID); err != nil {
				return collections.Collection{}, translateError("lock shared collection capacity", err)
			}
		}
		var old collections.Collection
		if expected > 0 {
			var err error
			old, err = scanCollection(tx.QueryRow(ctx, `SELECT `+curatedColumns+` FROM shared_collections WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, a.TenantID, next.ID))
			if err != nil {
				return collections.Collection{}, err
			}
		}
		if err := collections.ValidateSave(a, old, next, expected, op, listingID, e); err != nil {
			return collections.Collection{}, err
		}
		if op == collections.Create {
			var namespace string
			if err := tx.QueryRow(ctx, `SELECT id FROM namespaces WHERE tenant_id=$1 AND id=$2`, a.TenantID, next.NamespaceID).Scan(&namespace); err != nil {
				return collections.Collection{}, translateError("read collection owner namespace", err)
			}
			var total, scoped int
			if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM (SELECT id FROM shared_collections WHERE tenant_id=$1 LIMIT 1000) bounded),(SELECT count(*) FROM (SELECT id FROM shared_collections WHERE tenant_id=$1 AND namespace_id=$2 LIMIT 100) bounded)`, a.TenantID, next.NamespaceID).Scan(&total, &scoped); err != nil {
				return collections.Collection{}, translateError("check collection capacity", err)
			}
			if total >= collections.MaxTenantCollections || scoped >= collections.MaxNamespaceCollections {
				return collections.Collection{}, catalog.E(catalog.CodeConflict, "collection capacity is reached; remove a collection or contact an administrator")
			}
		}
		if op == collections.Add {
			namespace, err := readCollectionTarget(ctx, tx, a.TenantID, listingID)
			if err != nil {
				return collections.Collection{}, err
			}
			// The preparatory target binding must agree with the current canonical scope.
			allowed := false
			for _, check := range identity.RecordedPermissionChecks(ctx) {
				if check.Permission == "resource.read" && check.NamespaceID == namespace {
					allowed = true
				}
			}
			if !allowed {
				return collections.Collection{}, catalog.E(catalog.CodeForbidden, "current target read admission is required")
			}
			var duplicate bool
			var count int
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM shared_collection_members WHERE tenant_id=$1 AND collection_id=$2 AND listing_id=$3),(SELECT count(*) FROM (SELECT listing_id FROM shared_collection_members WHERE tenant_id=$1 AND collection_id=$2 LIMIT 200) bounded)`, a.TenantID, next.ID, listingID).Scan(&duplicate, &count); err != nil {
				return collections.Collection{}, translateError("check collection member capacity", err)
			}
			if duplicate {
				return collections.Collection{}, catalog.E(catalog.CodeConflict, "Listing is already a member")
			}
			if count >= collections.MaxMembers {
				return collections.Collection{}, catalog.E(catalog.CodeConflict, "collection capacity is reached; remove a member first")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO shared_collection_members(tenant_id,collection_id,listing_id) VALUES($1,$2,$3)`, a.TenantID, next.ID, listingID); err != nil {
				return collections.Collection{}, translateError("add collection member", err)
			}
		}
		if op == collections.Remove {
			tag, err := tx.Exec(ctx, `DELETE FROM shared_collection_members WHERE tenant_id=$1 AND collection_id=$2 AND listing_id=$3`, a.TenantID, next.ID, listingID)
			if err != nil {
				return collections.Collection{}, translateError("remove collection member", err)
			}
			if tag.RowsAffected() != 1 {
				return collections.Collection{}, catalog.E(catalog.CodeNotFound, "collection member was not found")
			}
		}
		switch op {
		case collections.Create:
			_, err := tx.Exec(ctx, `INSERT INTO shared_collections(tenant_id,id,namespace_id,name,description,revision,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, a.TenantID, next.ID, next.NamespaceID, next.Name, next.Description, next.Revision, next.CreatedAt, next.UpdatedAt)
			if err != nil {
				return collections.Collection{}, translateError("create shared collection", err)
			}
		case collections.Delete:
			if _, err := tx.Exec(ctx, `DELETE FROM shared_collection_members WHERE tenant_id=$1 AND collection_id=$2`, a.TenantID, next.ID); err != nil {
				return collections.Collection{}, translateError("delete collection members", err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM shared_collections WHERE tenant_id=$1 AND id=$2`, a.TenantID, next.ID); err != nil {
				return collections.Collection{}, translateError("delete shared collection", err)
			}
		case collections.Update, collections.Add, collections.Remove:
			if _, err := tx.Exec(ctx, `UPDATE shared_collections SET name=$3,description=$4,revision=$5,updated_at=$6 WHERE tenant_id=$1 AND id=$2`, a.TenantID, next.ID, next.Name, next.Description, next.Revision, next.UpdatedAt); err != nil {
				return collections.Collection{}, translateError("update shared collection", err)
			}
		}
		if err := insertAuditEvent(ctx, tx, e); err != nil {
			return collections.Collection{}, err
		}
		return next, nil
	})
}
