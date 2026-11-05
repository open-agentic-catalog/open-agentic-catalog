package catalog

import (
	"context"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/overview"
)

// OverviewSelectionLimit applies independently to each new summary measure.
const OverviewSelectionLimit = overview.SelectionLimit

type BoundedCount = overview.BoundedCount

func OverviewCount(rows int) BoundedCount {
	return overview.Count(rows)
}

type KindCount struct {
	Kind  Kind `json:"kind"`
	Count int  `json:"count"`
}

type OverviewScope = overview.Scope

func NewOverviewScope(tenant string, at time.Time) OverviewScope {
	return overview.NewScope(tenant, at)
}

type ResourceOverview struct {
	OverviewScope
	Total BoundedCount `json:"total"`
	Kinds []KindCount  `json:"kinds,omitempty"`
}

type ReleaseOverview struct {
	OverviewScope
	Published  BoundedCount `json:"published"`
	Deprecated BoundedCount `json:"deprecated"`
}

type DirectoryOverview struct {
	OverviewScope
	Count BoundedCount `json:"count"`
}

type IndexedOverview struct {
	OverviewScope
	Count   *int `json:"count,omitempty"`
	Limited bool `json:"limited"`
}

type InventoryMeasure string

const (
	InventoryResources  InventoryMeasure = "resources"
	InventoryReleases   InventoryMeasure = "releases"
	InventoryNamespaces InventoryMeasure = "namespaces"
	InventoryPublishers InventoryMeasure = "publishers"
)

type InventoryCounts struct {
	Total, Published, Deprecated BoundedCount
	Kinds                        []KindCount
}

// InventoryReader selects authorized metadata before its fixed ceiling. It
// never hydrates native documents or pages the ordinary collection endpoints.
type InventoryReader interface {
	ReadInventoryCounts(context.Context, string, InventoryMeasure) (InventoryCounts, error)
}

// IndexedInventoryReader counts eligible contributions before deduplication.
// An exceeded contribution ceiling supplies no distinct-identity lower bound.
type IndexedInventoryReader interface {
	CountIndexedInventory(context.Context, string) (*int, bool, error)
}

func (s *Service) inventoryCounts(ctx context.Context, measure InventoryMeasure) (OverviewScope, InventoryCounts, error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return OverviewScope{}, InventoryCounts{}, err
	}
	if measure == InventoryPublishers {
		if _, err = requirePermission(ctx, "publisher.read", ""); err != nil {
			return OverviewScope{}, InventoryCounts{}, err
		}
	}
	reader, ok := s.repo.(InventoryReader)
	if !ok {
		return OverviewScope{}, InventoryCounts{}, E(CodeDependencyUnavailable, "inventory summary is unavailable")
	}
	scope := NewOverviewScope(actor.TenantID, s.clock.Now())
	counts, err := reader.ReadInventoryCounts(ctx, actor.TenantID, measure)
	return scope, counts, err
}

func (s *Service) ResourceOverview(ctx context.Context) (ResourceOverview, error) {
	scope, counts, err := s.inventoryCounts(ctx, InventoryResources)
	return ResourceOverview{OverviewScope: scope, Total: counts.Total, Kinds: counts.Kinds}, err
}

func (s *Service) ReleaseOverview(ctx context.Context) (ReleaseOverview, error) {
	scope, counts, err := s.inventoryCounts(ctx, InventoryReleases)
	return ReleaseOverview{OverviewScope: scope, Published: counts.Published, Deprecated: counts.Deprecated}, err
}

func (s *Service) DirectoryOverview(ctx context.Context, measure InventoryMeasure) (DirectoryOverview, error) {
	if measure != InventoryNamespaces && measure != InventoryPublishers {
		return DirectoryOverview{}, E(CodeInvalidArgument, "directory summary is invalid")
	}
	scope, counts, err := s.inventoryCounts(ctx, measure)
	return DirectoryOverview{OverviewScope: scope, Count: counts.Total}, err
}

func (m *MemoryRepository) ReadInventoryCounts(ctx context.Context, tenant string, measure InventoryMeasure) (InventoryCounts, error) {
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return InventoryCounts{}, err
	}
	if identity.NeedsCollectionAdmission(ctx) {
		return identity.WithCollectionRead(ctx, func(bound context.Context) (InventoryCounts, error) {
			return m.ReadInventoryCounts(bound, tenant, measure)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	counts := InventoryCounts{}
	total, published, deprecated := 0, 0, 0
	kinds := map[Kind]int{}
	allowed := func(permission identity.Permission, namespace string) (bool, error) {
		return identity.CollectionAllows(ctx, permission, namespace)
	}
	switch measure {
	case InventoryResources:
		for _, resource := range m.resources {
			if resource.TenantID != tenant || resource.DeletedAt != nil {
				continue
			}
			ok, err := allowed("resource.read", resource.NamespaceID)
			if err != nil {
				return counts, err
			}
			if !ok {
				continue
			}
			total++
			kinds[resource.Kind]++
			if total > OverviewSelectionLimit {
				break
			}
		}
		if total <= OverviewSelectionLimit {
			for _, kind := range []Kind{KindAgent, KindMCPServer, KindSkill, KindPlugin} {
				counts.Kinds = append(counts.Kinds, KindCount{Kind: kind, Count: kinds[kind]})
			}
		}
	case InventoryNamespaces:
		for _, namespace := range m.namespaces {
			if namespace.TenantID != tenant {
				continue
			}
			ok, err := allowed("namespace.read", namespace.ID)
			if err != nil {
				return counts, err
			}
			if !ok {
				continue
			}
			total++
			if total > OverviewSelectionLimit {
				break
			}
		}
	case InventoryPublishers:
		for _, publisher := range m.publishers {
			if publisher.TenantID == tenant {
				total++
				if total > OverviewSelectionLimit {
					break
				}
			}
		}
	case InventoryReleases:
		for _, listing := range m.listings {
			if listing.TenantID != tenant || listing.CurrentReleaseID == "" {
				continue
			}
			resource, ok := m.resources[listing.ResourceID]
			if !ok || resource.TenantID != tenant || resource.DeletedAt != nil {
				continue
			}
			ok, err := allowed("resource.read_history", resource.NamespaceID)
			if err != nil {
				return counts, err
			}
			if !ok {
				continue
			}
			release, ok := m.releases[listing.CurrentReleaseID]
			if !ok || release.TenantID != tenant || release.ListingID != listing.ID {
				continue
			}
			if release.State == ReleasePublished && published <= OverviewSelectionLimit {
				published++
			}
			if release.State == ReleaseDeprecated && deprecated <= OverviewSelectionLimit {
				deprecated++
			}
			if published > OverviewSelectionLimit && deprecated > OverviewSelectionLimit {
				break
			}
		}
	default:
		return counts, E(CodeInvalidArgument, "inventory measure is invalid")
	}
	counts.Total, counts.Published, counts.Deprecated = OverviewCount(total), OverviewCount(published), OverviewCount(deprecated)
	return counts, nil
}
