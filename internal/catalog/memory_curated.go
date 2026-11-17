package catalog

import (
	"context"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// CollectionEntries resolves only the supplied bounded local Listing references.
// It holds one data snapshot; permission admission precedes the repository lock.
func (m *MemoryRepository) CollectionEntries(ctx context.Context, tenantID string, listingIDs []string) ([]CatalogSummary, string, error) {
	if identity.NeedsReadAdmission(ctx) {
		type snapshot struct {
			items      []CatalogSummary
			generation string
		}
		out, err := identity.WithReadAdmission(ctx, func(bound context.Context) (snapshot, error) {
			items, generation, err := m.CollectionEntries(bound, tenantID, listingIDs)
			return snapshot{items, generation}, err
		})
		return out.items, out.generation, err
	}
	if len(listingIDs) > 200 {
		return nil, "", E(CodeInvalidArgument, "collection reference bound exceeded")
	}
	if err := identity.RequireCollectionTenant(ctx, tenantID); err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := []CatalogSummary{}
	for _, id := range listingIDs {
		listing := m.listings[id]
		if listing.TenantID != tenantID {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.read", listing.NamespaceID)
		if err != nil {
			return nil, "", err
		}
		if !allowed {
			continue
		}
		if record, ok := m.catalogRecordLocked(tenantID, listing, false); ok {
			items = append(items, clone(record.Summary()))
		}
	}
	return items, catalogSearchGeneration(items), nil
}
