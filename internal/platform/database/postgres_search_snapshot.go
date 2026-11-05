// Combined Search uses one repeatable-read snapshot for local and indexed results
// before any external live discovery.

package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// SearchCatalogSnapshot keeps local and indexed selection, totals, hydration
// and cursor generation in one read-only snapshot. Concurrent commits cannot
// force a first-page reconciliation race. Live discovery follows after commit.
func (p *PostgresRepository) SearchCatalogSnapshot(ctx context.Context, tenantID string, local catalog.CatalogSearchRepositoryRequest, indexed catalog.FederatedCatalogSearchRequest) (catalog.CatalogSearchSnapshot, error) {
	return withTenantCollectionRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.CatalogSearchSnapshot, error) {
		localPage, err := searchCatalogTx(ctx, tx, tenantID, local)
		if err != nil {
			return catalog.CatalogSearchSnapshot{}, err
		}
		indexedPage, err := searchIndexedCatalogTx(ctx, tx, tenantID, indexed)
		if err != nil {
			return catalog.CatalogSearchSnapshot{}, err
		}
		return catalog.CatalogSearchSnapshot{Local: localPage, Indexed: indexedPage}, nil
	})
}
