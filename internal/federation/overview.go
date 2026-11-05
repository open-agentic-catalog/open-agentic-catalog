package federation

import (
	"context"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func (m *Manager) IndexedOverview(ctx context.Context) (catalog.IndexedOverview, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "federation.discover")
	if err != nil {
		return catalog.IndexedOverview{}, err
	}
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		return catalog.IndexedOverview{}, catalog.PermissionError("indexed summary admission failed", err)
	}
	reader, ok := m.store.(catalog.IndexedInventoryReader)
	if !ok {
		return catalog.IndexedOverview{}, catalog.E(catalog.CodeDependencyUnavailable, "indexed summary is unavailable")
	}
	scope := catalog.NewOverviewScope(actor.TenantID, m.clock.Now())
	count, limited, err := reader.CountIndexedInventory(ctx, actor.TenantID)
	return catalog.IndexedOverview{OverviewScope: scope, Count: count, Limited: limited}, err
}

func indexedObservationEligible(tenant string, source Source, observation Observation) bool {
	return observation.TenantID == tenant && source.TenantID == tenant && observation.Freshness == FreshCurrent && observation.NormalizedEntryDigest != "" && observation.Compatibility.Structural == "valid" && observation.Compatibility.Support == "supported" && observation.Kind.Valid() && (source.Status == SourceActive || source.Status == SourceError) && (source.RetrievalMode == RetrievalIndexed || source.RetrievalMode == RetrievalHybrid) && scopeMatches(source.Scope, sourceEntry{Identifier: observation.Identifier, Version: observation.Version, VersionPresent: observation.VersionPresent, Type: observation.MediaType, Tags: observation.SearchTags})
}

func (m *MemoryStore) CountIndexedInventory(ctx context.Context, tenant string) (*int, bool, error) {
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return nil, false, err
	}
	if identity.NeedsCollectionAdmission(ctx) {
		type countResult struct {
			count   *int
			limited bool
		}
		out, err := identity.WithCollectionRead(ctx, func(bound context.Context) (countResult, error) {
			count, limited, err := m.CountIndexedInventory(bound, tenant)
			return countResult{count, limited}, err
		})
		return out.count, out.limited, err
	}
	allowed, err := identity.CollectionAllows(ctx, "federation.discover", "")
	if err != nil {
		return nil, false, err
	}
	if !allowed {
		return nil, false, catalog.E(catalog.CodeForbidden, "federation discovery permission is required")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	identities := map[catalog.IndexedIdentity]struct{}{}
	contributions := 0
	for _, observation := range m.observations {
		source, ok := m.sources[observation.SourceID]
		if !ok || !indexedObservationEligible(tenant, source, observation) {
			continue
		}
		contributions++
		if contributions > catalog.OverviewSelectionLimit {
			return nil, true, nil
		}
		identities[catalog.IndexedIdentity{Identifier: observation.Identifier, Version: observation.Version, VersionPresent: observation.VersionPresent}] = struct{}{}
	}
	count := len(identities)
	return &count, false, nil
}
