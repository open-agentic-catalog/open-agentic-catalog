// Namespace and publisher persistence records revision-checked governance changes
// with audit and independently classified outbox evidence.

package database

import (
	"context"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// CreateNamespace inserts tenant-scoped namespace metadata and exactly bound
// creation evidence in one transaction.
func (p *PostgresRepository) CreateNamespace(ctx context.Context, namespace catalog.Namespace, audit catalog.AuditEvent, outbox catalog.OutboxEvent) (catalog.Namespace, error) {
	return withTenantTx(ctx, p.pool, namespace.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.Namespace, error) {
		if _, err := tx.Exec(ctx, `INSERT INTO namespaces(id,tenant_id,slug,display_name,description,revision,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, namespace.ID, namespace.TenantID, namespace.Slug, namespace.DisplayName, namespace.Description, namespace.Revision, namespace.CreatedAt, namespace.UpdatedAt); err != nil {
			return catalog.Namespace{}, translateError("create namespace", err)
		}
		if err := insertEvidence(ctx, tx, audit, outbox, expectEvidence(namespace.TenantID, "namespace", namespace.ID, "namespace.created")); err != nil {
			return catalog.Namespace{}, err
		}
		return namespace, nil
	})
}

// GetNamespace performs an exact tenant/ID lookup without consulting another
// tenant's namespaces.
func (p *PostgresRepository) GetNamespace(ctx context.Context, tenantID, namespaceID string) (catalog.Namespace, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.Namespace, error) {
		var item catalog.Namespace
		if err := tx.QueryRow(ctx, `SELECT id,tenant_id,slug,display_name,description,revision,created_at,updated_at FROM namespaces WHERE tenant_id=$1 AND id=$2`, tenantID, namespaceID).Scan(&item.ID, &item.TenantID, &item.Slug, &item.DisplayName, &item.Description, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return catalog.Namespace{}, translateError("read namespace", err)
		}
		return item, nil
	})
}

// ListNamespaces applies stable bytewise slug/ID ordering and the requested page
// ceiling at SQL selection.
func (p *PostgresRepository) ListNamespaces(ctx context.Context, tenantID string, request catalog.NamespaceListRequest) ([]catalog.Namespace, error) {
	return withTenantCollectionRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.Namespace, error) {
		statement, args := namespacePageStatement(ctx, tenantID, request)
		rows, err := tx.Query(ctx, statement, args...)
		if err != nil {
			return nil, translateError("list namespaces", err)
		}
		defer rows.Close()
		items := make([]catalog.Namespace, 0)
		for rows.Next() {
			var item catalog.Namespace
			if err := rows.Scan(&item.ID, &item.TenantID, &item.Slug, &item.DisplayName, &item.Description, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
				return nil, translateError("scan namespace", err)
			}
			items = append(items, item)
		}
		return items, translateError("iterate namespaces", rows.Err())
	})
}

// UpdateNamespace locks and checks the current revision, preserving the immutable
// slug and creation time while updating descriptive fields and evidence.
func (p *PostgresRepository) UpdateNamespace(ctx context.Context, params catalog.UpdateNamespaceParams) (catalog.Namespace, error) {
	return withTenantTx(ctx, p.pool, params.Namespace.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.Namespace, error) {
		var current catalog.Namespace
		if err := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,slug,display_name,description,revision,created_at,updated_at FROM namespaces WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, params.Namespace.TenantID, params.Namespace.ID).Scan(&current.ID, &current.TenantID, &current.Slug, &current.DisplayName, &current.Description, &current.Revision, &current.CreatedAt, &current.UpdatedAt); err != nil {
			return catalog.Namespace{}, translateError("read namespace", err)
		}
		if current.Revision != params.ExpectedRevision || params.Namespace.Revision != current.Revision+1 {
			return catalog.Namespace{}, catalog.E(catalog.CodePreconditionFailed, "namespace revision does not match")
		}
		params.Namespace.Slug = current.Slug
		params.Namespace.CreatedAt = current.CreatedAt
		if _, err := tx.Exec(ctx, `UPDATE namespaces SET display_name=$3,description=$4,revision=$5,updated_at=$6 WHERE tenant_id=$1 AND id=$2`, params.Namespace.TenantID, params.Namespace.ID, params.Namespace.DisplayName, params.Namespace.Description, params.Namespace.Revision, params.Namespace.UpdatedAt); err != nil {
			return catalog.Namespace{}, translateError("update namespace", err)
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.Namespace.TenantID, "namespace", params.Namespace.ID, "namespace.updated")); err != nil {
			return catalog.Namespace{}, err
		}
		return params.Namespace, nil
	})
}

// CreatePublisher serializes tenant publisher administration and rejects
// overlapping declared domains before atomic insertion and evidence.
func (p *PostgresRepository) CreatePublisher(ctx context.Context, publisher catalog.Publisher, audit catalog.AuditEvent, outbox catalog.OutboxEvent) (catalog.Publisher, error) {
	return withTenantTx(ctx, p.pool, publisher.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.Publisher, error) {
		if err := lockTenantState(ctx, tx, publisher.TenantID, "lock tenant publisher administration"); err != nil {
			return catalog.Publisher{}, err
		}
		var conflict bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM publishers WHERE tenant_id=$1 AND domains && $2::text[])`, publisher.TenantID, publisher.Domains).Scan(&conflict); err != nil {
			return catalog.Publisher{}, translateError("check publisher domains", err)
		}
		if conflict {
			return catalog.Publisher{}, catalog.E(catalog.CodeConflict, "publisher domain already belongs to another publisher")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO publishers(id,tenant_id,display_name,domains,approved_publication_domains,revision,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, publisher.ID, publisher.TenantID, publisher.DisplayName, publisher.Domains, publisher.ApprovedPublicationDomains, publisher.Revision, publisher.CreatedAt, publisher.UpdatedAt); err != nil {
			return catalog.Publisher{}, translateError("create publisher", err)
		}
		if err := insertEvidence(ctx, tx, audit, outbox, expectEvidence(publisher.TenantID, "publisher", publisher.ID, "publisher.created")); err != nil {
			return catalog.Publisher{}, err
		}
		return publisher, nil
	})
}

// ListPublishers returns the complete tenant publisher collection ordered by
// folded display name and ID; bounded API lists use ListPublisherPage.
func (p *PostgresRepository) ListPublishers(ctx context.Context, tenantID string) ([]catalog.Publisher, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.Publisher, error) {
		rows, err := tx.Query(ctx, `SELECT id,tenant_id,display_name,domains,approved_publication_domains,revision,created_at,updated_at FROM publishers WHERE tenant_id=$1 ORDER BY lower(display_name),id`, tenantID)
		if err != nil {
			return nil, translateError("list publishers", err)
		}
		defer rows.Close()
		items := make([]catalog.Publisher, 0)
		for rows.Next() {
			var item catalog.Publisher
			if err := rows.Scan(&item.ID, &item.TenantID, &item.DisplayName, &item.Domains, &item.ApprovedPublicationDomains, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
				return nil, translateError("scan publisher", err)
			}
			items = append(items, item)
		}
		return items, translateError("iterate publishers", rows.Err())
	})
}

// GetPublisher reads one tenant-owned publisher with a sorted distinct projection
// of its retained approved domain fields.
func (p *PostgresRepository) GetPublisher(ctx context.Context, tenantID, publisherID string) (catalog.Publisher, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.Publisher, error) {
		var item catalog.Publisher
		if err := tx.QueryRow(ctx, `SELECT id,tenant_id,display_name,domains,approved_publication_domains,revision,created_at,updated_at FROM publishers WHERE tenant_id=$1 AND id=$2`, tenantID, publisherID).Scan(&item.ID, &item.TenantID, &item.DisplayName, &item.Domains, &item.ApprovedPublicationDomains, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return catalog.Publisher{}, translateError("read publisher", err)
		}
		return item, nil
	})
}

// ListPublisherPage selects a SQL-bounded page ordered by exact display name and
// ID, preserving its cursor comparison order.
func (p *PostgresRepository) ListPublisherPage(ctx context.Context, tenantID string, request catalog.PublisherListRequest) ([]catalog.Publisher, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.Publisher, error) {
		statement, args := publisherPageStatement(tenantID, request)
		rows, err := tx.Query(ctx, statement, args...)
		if err != nil {
			return nil, translateError("list publisher page", err)
		}
		defer rows.Close()
		items := make([]catalog.Publisher, 0)
		for rows.Next() {
			var item catalog.Publisher
			if err := rows.Scan(&item.ID, &item.TenantID, &item.DisplayName, &item.Domains, &item.ApprovedPublicationDomains, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
				return nil, translateError("scan publisher", err)
			}
			items = append(items, item)
		}
		return items, translateError("iterate publisher page", rows.Err())
	})
}

// UpdatePublisher revision-checks declared domains, forbids removing approved or
// actively published domains and preserves existing publication approvals.
func (p *PostgresRepository) UpdatePublisher(ctx context.Context, params catalog.UpdatePublisherParams) (catalog.Publisher, error) {
	return withTenantTx(ctx, p.pool, params.Publisher.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.Publisher, error) {
		if err := lockTenantState(ctx, tx, params.Publisher.TenantID, "lock tenant publisher administration"); err != nil {
			return catalog.Publisher{}, err
		}
		var current catalog.Publisher
		if err := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,display_name,domains,approved_publication_domains,revision,created_at,updated_at FROM publishers WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, params.Publisher.TenantID, params.Publisher.ID).Scan(&current.ID, &current.TenantID, &current.DisplayName, &current.Domains, &current.ApprovedPublicationDomains, &current.Revision, &current.CreatedAt, &current.UpdatedAt); err != nil {
			return catalog.Publisher{}, translateError("read publisher", err)
		}
		if current.Revision != params.ExpectedRevision || params.Publisher.Revision != current.Revision+1 {
			return catalog.Publisher{}, catalog.E(catalog.CodePreconditionFailed, "publisher revision does not match")
		}
		removed := make([]string, 0)
		for _, domain := range current.Domains {
			if !stringSliceContains(params.Publisher.Domains, domain) {
				removed = append(removed, domain)
			}
		}
		for _, domain := range current.ApprovedPublicationDomains {
			if stringSliceContains(removed, domain) {
				return catalog.Publisher{}, catalog.E(catalog.CodeConflict, "an approved publication domain must be revoked before it is removed")
			}
		}
		if len(removed) > 0 {
			var activePublication bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(
				  SELECT 1
				    FROM catalog_listings listing
				    JOIN listing_releases release ON release.tenant_id=listing.tenant_id AND release.id=listing.current_release_id AND release.state IN ('published','deprecated')
				    JOIN listing_revisions revision ON revision.tenant_id=release.tenant_id AND revision.id=release.listing_revision_id AND revision.publisher_id=$2
				    JOIN resources resource ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id AND resource.deleted_at IS NULL
				   WHERE listing.tenant_id=$1 AND split_part(resource.canonical_identifier,':',3)=ANY($3::text[]))`, params.Publisher.TenantID, params.Publisher.ID, removed).Scan(&activePublication); err != nil {
				return catalog.Publisher{}, translateError("check publisher active publications", err)
			}
			if activePublication {
				return catalog.Publisher{}, catalog.E(catalog.CodeConflict, "a declared domain with an active publication cannot be removed")
			}
		}
		var conflict bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM publishers WHERE tenant_id=$1 AND id<>$2 AND domains && $3::text[])`, params.Publisher.TenantID, params.Publisher.ID, params.Publisher.Domains).Scan(&conflict); err != nil {
			return catalog.Publisher{}, translateError("check publisher domains", err)
		}
		if conflict {
			return catalog.Publisher{}, catalog.E(catalog.CodeConflict, "publisher domain already belongs to another publisher")
		}
		params.Publisher.ApprovedPublicationDomains = append([]string{}, current.ApprovedPublicationDomains...)
		params.Publisher.CreatedAt = current.CreatedAt
		if _, err := tx.Exec(ctx, `UPDATE publishers SET display_name=$3,domains=$4,revision=$5,updated_at=$6 WHERE tenant_id=$1 AND id=$2`, params.Publisher.TenantID, params.Publisher.ID, params.Publisher.DisplayName, params.Publisher.Domains, params.Publisher.Revision, params.Publisher.UpdatedAt); err != nil {
			return catalog.Publisher{}, translateError("update publisher", err)
		}
		if err := insertEvidence(ctx, tx, params.Audit, params.Outbox, expectEvidence(params.Publisher.TenantID, "publisher", params.Publisher.ID, "publisher.updated")); err != nil {
			return catalog.Publisher{}, err
		}
		return params.Publisher, nil
	})
}

// ApprovePublisherPublicationDomain locks the publisher, checks any supplied
// positive revision and declared-domain membership, then adds approval and atomic
// evidence.
func (p *PostgresRepository) ApprovePublisherPublicationDomain(ctx context.Context, params catalog.PublisherDomainApproval) (catalog.Publisher, error) {
	tenantID, publisherID, domain, expectedRevision, audit, outbox := params.TenantID, params.PublisherID, params.Domain, params.ExpectedRevision, params.Audit, params.Outbox
	return withTenantTx(ctx, p.pool, tenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.Publisher, error) {
		var publisher catalog.Publisher
		if err := tx.QueryRow(ctx, `SELECT id,tenant_id,display_name,domains,approved_publication_domains,revision,created_at,updated_at FROM publishers WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, publisherID).Scan(&publisher.ID, &publisher.TenantID, &publisher.DisplayName, &publisher.Domains, &publisher.ApprovedPublicationDomains, &publisher.Revision, &publisher.CreatedAt, &publisher.UpdatedAt); err != nil {
			return catalog.Publisher{}, translateError("read publisher", err)
		}
		if expectedRevision > 0 && publisher.Revision != expectedRevision {
			return catalog.Publisher{}, catalog.E(catalog.CodePreconditionFailed, "publisher revision does not match")
		}
		if !stringSliceContains(publisher.Domains, domain) {
			return catalog.Publisher{}, catalog.E(catalog.CodeInvalidArgument, "domain is not declared by this publisher")
		}
		if params.Policy.Mode == catalog.DomainVerificationRequired {
			proof, err := readPublisherVerification(ctx, tx, tenantID, publisherID, domain, true)
			if err != nil {
				return catalog.Publisher{}, err
			}
			audit, err = catalog.PublisherApprovalAudit(params, proof)
			if err != nil {
				return catalog.Publisher{}, err
			}
		}

		if !stringSliceContains(publisher.ApprovedPublicationDomains, domain) {
			publisher.ApprovedPublicationDomains = append(publisher.ApprovedPublicationDomains, domain)
			sort.Strings(publisher.ApprovedPublicationDomains)
			publisher.Revision++
			publisher.UpdatedAt = audit.At
			if _, err := tx.Exec(ctx, `UPDATE publishers SET approved_publication_domains=$3,revision=$4,updated_at=$5 WHERE tenant_id=$1 AND id=$2`, tenantID, publisher.ID, publisher.ApprovedPublicationDomains, publisher.Revision, publisher.UpdatedAt); err != nil {
				return catalog.Publisher{}, translateError("approve publisher publication domain", err)
			}
		}
		if err := insertEvidence(ctx, tx, audit, outbox, expectEvidence(tenantID, "publisher", publisherID, "publisher.publication_domain_approved")); err != nil {
			return catalog.Publisher{}, err
		}
		return publisher, nil
	})
}

// RevokePublisherPublicationDomain serializes publisher authority and binds the
// exact affected release inventory into the same committed audit evidence.
func (p *PostgresRepository) RevokePublisherPublicationDomain(ctx context.Context, tenantID, publisherID, domain string, expectedRevision int64, audit catalog.AuditEvent, outbox catalog.OutboxEvent) (catalog.Publisher, []string, error) {
	type result struct {
		publisher catalog.Publisher
		affected  []string
	}
	value, err := withTenantTx(ctx, p.pool, tenantID, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) (result, error) {
		var publisher catalog.Publisher
		if err := tx.QueryRow(ctx, `SELECT id,tenant_id,display_name,domains,approved_publication_domains,revision,created_at,updated_at FROM publishers WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, publisherID).Scan(&publisher.ID, &publisher.TenantID, &publisher.DisplayName, &publisher.Domains, &publisher.ApprovedPublicationDomains, &publisher.Revision, &publisher.CreatedAt, &publisher.UpdatedAt); err != nil {
			return result{}, translateError("read publisher", err)
		}
		if expectedRevision > 0 && publisher.Revision != expectedRevision {
			return result{}, catalog.E(catalog.CodePreconditionFailed, "publisher revision does not match")
		}
		if !stringSliceContains(publisher.ApprovedPublicationDomains, domain) {
			return result{}, catalog.E(catalog.CodeInvalidTransition, "publication domain is not approved")
		}
		rows, err := tx.Query(ctx, `
			SELECT release.id::text,resource.canonical_identifier
			  FROM catalog_listings listing
			  JOIN listing_releases release ON release.tenant_id=listing.tenant_id AND release.id=listing.current_release_id AND release.state IN ('published','deprecated')
			  JOIN listing_revisions revision ON revision.tenant_id=listing.tenant_id AND revision.id=release.listing_revision_id AND revision.publisher_id=$1
			  JOIN resources resource ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id AND resource.deleted_at IS NULL
			 WHERE listing.tenant_id=$2
			 ORDER BY release.id`, publisherID, tenantID)
		if err != nil {
			return result{}, translateError("analyze publication-domain releases", err)
		}
		affected := make([]string, 0)
		for rows.Next() {
			var releaseID, identifierText string
			if err := rows.Scan(&releaseID, &identifierText); err != nil {
				rows.Close()
				return result{}, translateError("scan publication-domain release", err)
			}
			identifier, parseErr := catalog.ParseAIRIdentifier(identifierText)
			if parseErr == nil && identifier.Publisher == domain {
				affected = append(affected, releaseID)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return result{}, translateError("iterate publication-domain releases", err)
		}
		rows.Close()
		audit, err = catalog.CompletePublisherDomainRevocationAudit(audit, domain, affected)
		if err != nil {
			return result{}, err
		}
		approved := make([]string, 0, len(publisher.ApprovedPublicationDomains)-1)
		for _, candidate := range publisher.ApprovedPublicationDomains {
			if candidate != domain {
				approved = append(approved, candidate)
			}
		}
		publisher.ApprovedPublicationDomains = approved
		publisher.Revision++
		publisher.UpdatedAt = audit.At
		if _, err := tx.Exec(ctx, `UPDATE publishers SET approved_publication_domains=$2,revision=$3,updated_at=$4 WHERE id=$1 AND tenant_id=$5`, publisher.ID, publisher.ApprovedPublicationDomains, publisher.Revision, publisher.UpdatedAt, tenantID); err != nil {
			return result{}, translateError("revoke publisher publication domain", err)
		}
		if err := insertEvidence(ctx, tx, audit, outbox, expectEvidence(tenantID, "publisher", publisherID, "publisher.publication_domain_revoked")); err != nil {
			return result{}, err
		}
		return result{publisher: publisher, affected: affected}, nil
	})
	return value.publisher, value.affected, err
}

// stringSliceContains tests exact membership without normalization, using already
// canonical domain values.
func stringSliceContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// Exact predicates are emitted only for supplied filters so cached generic plans
// can use the selective indexes too. Values always remain bound parameters.
func namespacePageStatement(ctx context.Context, tenantID string, request catalog.NamespaceListRequest) (string, []any) {
	args := []any{tenantID, request.AfterSlug, request.AfterID, request.Limit}
	statement := `SELECT id,tenant_id,slug,display_name,description,revision,created_at,updated_at FROM namespaces WHERE tenant_id=$1 AND ` + collectionPermissionSQL("namespace.read", "namespaces.id")
	if request.Slug != "" {
		args = append(args, request.Slug)
		statement += ` AND slug COLLATE "C"=$` + strconv.Itoa(len(args)) + ` COLLATE "C"`
	}
	if request.DisplayName != "" {
		args = append(args, request.DisplayName)
		statement += ` AND display_name COLLATE "C"=$` + strconv.Itoa(len(args)) + ` COLLATE "C"`
	}
	statement += ` AND ($2='' OR slug COLLATE "C">$2 COLLATE "C" OR (slug=$2 AND id>nullif($3,'')::uuid)) ORDER BY slug COLLATE "C",id LIMIT $4`
	return collectionStatement(ctx, statement), args
}

func publisherPageStatement(tenantID string, request catalog.PublisherListRequest) (string, []any) {
	args := []any{tenantID, request.AfterDisplayName, request.AfterID, request.Limit}
	statement := `SELECT id,tenant_id,display_name,domains,approved_publication_domains,revision,created_at,updated_at FROM publishers WHERE tenant_id=$1`
	if request.DisplayName != "" {
		args = append(args, request.DisplayName)
		statement += ` AND display_name COLLATE "C"=$` + strconv.Itoa(len(args)) + ` COLLATE "C"`
	}
	if request.DeclaredDomain != "" {
		args = append(args, []string{request.DeclaredDomain})
		statement += ` AND domains @> $` + strconv.Itoa(len(args)) + `::text[]`
	}
	statement += ` AND ($2='' OR display_name COLLATE "C">$2 COLLATE "C" OR (display_name=$2 AND id>nullif($3,'')::uuid)) ORDER BY display_name COLLATE "C",id LIMIT $4`
	return statement, args
}
