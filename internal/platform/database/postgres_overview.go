package database

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

var resourceInventoryCountsSQL = `WITH selected AS MATERIALIZED (SELECT r.kind FROM resources r WHERE r.tenant_id=$1 AND r.deleted_at IS NULL AND ` + collectionPermissionSQL("resource.read", "r.namespace_id") + ` LIMIT $2)
SELECT count(*),count(*) FILTER(WHERE kind='agent'),count(*) FILTER(WHERE kind='mcp_server'),count(*) FILTER(WHERE kind='skill'),count(*) FILTER(WHERE kind='plugin') FROM selected`

var releaseInventoryCountsSQL = `SELECT count(*) FROM (SELECT release.id FROM catalog_listings listing JOIN resources r ON r.tenant_id=listing.tenant_id AND r.id=listing.resource_id
JOIN listing_releases release ON release.tenant_id=listing.tenant_id AND release.id=listing.current_release_id AND release.listing_id=listing.id
WHERE listing.tenant_id=$1 AND r.deleted_at IS NULL AND release.state=$3 AND ` + collectionPermissionSQL("resource.read_history", "r.namespace_id") + ` LIMIT $2) selected`

func (p *PostgresRepository) ReadInventoryCounts(ctx context.Context, tenant string, measure catalog.InventoryMeasure) (catalog.InventoryCounts, error) {
	return withTenantCollectionRead(ctx, p.pool, tenant, func(tx pgx.Tx) (catalog.InventoryCounts, error) {
		var out catalog.InventoryCounts
		limit := catalog.OverviewSelectionLimit + 1
		var query string
		switch measure {
		case catalog.InventoryResources:
			query := resourceInventoryCountsSQL
			var total, agents, servers, skills, plugins int
			if err := tx.QueryRow(ctx, collectionStatement(ctx, query), tenant, limit).Scan(&total, &agents, &servers, &skills, &plugins); err != nil {
				return out, translateError("count resource inventory", err)
			}
			out.Total = catalog.OverviewCount(total)
			if !out.Total.Limited {
				out.Kinds = []catalog.KindCount{{Kind: catalog.KindAgent, Count: agents}, {Kind: catalog.KindMCPServer, Count: servers}, {Kind: catalog.KindSkill, Count: skills}, {Kind: catalog.KindPlugin, Count: plugins}}
			}
			return out, nil
		case catalog.InventoryReleases:
			query := releaseInventoryCountsSQL
			for _, state := range []catalog.ReleaseState{catalog.ReleasePublished, catalog.ReleaseDeprecated} {
				var count int
				if err := tx.QueryRow(ctx, collectionStatement(ctx, query), tenant, limit, state).Scan(&count); err != nil {
					return out, translateError("count current releases", err)
				}
				if state == catalog.ReleasePublished {
					out.Published = catalog.OverviewCount(count)
				} else {
					out.Deprecated = catalog.OverviewCount(count)
				}
			}
			return out, nil
		case catalog.InventoryNamespaces:
			query = `SELECT count(*) FROM (SELECT id FROM namespaces WHERE tenant_id=$1 AND ` + collectionPermissionSQL("namespace.read", "namespaces.id") + ` LIMIT $2) selected`
		case catalog.InventoryPublishers:
			query = `SELECT count(*) FROM (SELECT id FROM publishers WHERE tenant_id=$1 LIMIT $2) selected`
		default:
			return out, catalog.E(catalog.CodeInvalidArgument, "inventory measure is invalid")
		}
		var count int
		if err := tx.QueryRow(ctx, collectionStatement(ctx, query), tenant, limit).Scan(&count); err != nil {
			return out, translateError("count directory inventory", err)
		}
		out.Total = catalog.OverviewCount(count)
		return out, nil
	})
}

func (p *PostgresFederationStore) CountIndexedInventory(ctx context.Context, tenant string) (*int, bool, error) {
	type result struct {
		count   *int
		limited bool
	}
	out, err := withTenantCollectionRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (result, error) {
		if _, err := readCatalogSearchGeneration(ctx, tx, tenant); err != nil {
			return result{}, err
		}
		// This is indexed-only discovery. Local participation cannot shadow it.
		query := indexedInventoryCountsSQL()
		var contributions, identities int
		if err := tx.QueryRow(ctx, collectionStatement(ctx, query), tenant, catalog.OverviewSelectionLimit+1).Scan(&contributions, &identities); err != nil {
			return result{}, translateError("count indexed inventory", err)
		}
		if contributions > catalog.OverviewSelectionLimit {
			return result{limited: true}, nil
		}
		return result{count: &identities}, nil
	})
	return out.count, out.limited, err
}

func indexedInventoryCountsSQL() string {
	eligible := strings.Replace(indexedCurrentEligibleFromSQL, "AND NOT (observation.version_present AND EXISTS (", "AND NOT (false AND observation.version_present AND EXISTS (", 1)
	return indexedApprovedSourcesCTE + `, contributions AS MATERIALIZED (SELECT observation.identifier,coalesce(observation.version,'') AS version,observation.version_present ` + eligible + ` LIMIT $2)
SELECT count(*),count(DISTINCT (identifier,version_present,version)) FROM contributions`
}
