package catalog

import (
	"context"
	"maps"
	"slices"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// ListListingMetadataGaps selects current releases within the same permission snapshot as the page.
func (m *MemoryRepository) ListListingMetadataGaps(ctx context.Context, tenant string, q ListingMetadataQuery) ([]ListingMetadataGap, error) {
	if identity.NeedsCollectionAdmission(ctx) {
		return identity.WithCollectionRead(ctx, func(ctx context.Context) ([]ListingMetadataGap, error) {
			return m.ListListingMetadataGaps(ctx, tenant, q)
		})
	}
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := []ListingMetadataGap{}
	for _, id := range slices.Sorted(maps.Keys(m.listings)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		l := m.listings[id]
		if l.TenantID != tenant || id <= q.AfterID || l.CurrentReleaseID == "" || q.NamespaceID != "" && q.NamespaceID != l.NamespaceID {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.read", l.NamespaceID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		r := m.resources[l.ResourceID]
		if r.TenantID != tenant || r.NamespaceID != l.NamespaceID || r.ID != l.ResourceID {
			return nil, E(CodeDependencyUnavailable, "invalid released Listing resource")
		}
		if r.DeletedAt != nil || q.Kind != "" && q.Kind != r.Kind {
			continue
		}
		rel := m.releases[l.CurrentReleaseID]
		if rel.TenantID != tenant || rel.ListingID != id || rel.ID != l.CurrentReleaseID {
			return nil, E(CodeDependencyUnavailable, "invalid current Listing release")
		}
		if rel.State != ReleasePublished && rel.State != ReleaseDeprecated {
			continue
		}
		lr := m.listingRevisions[rel.ListingRevisionID]
		vr := m.versionRevisions[rel.VersionRevisionID]
		v := m.versions[vr.ResourceVersionID]
		if lr.ID != rel.ListingRevisionID || vr.ID != rel.VersionRevisionID || v.ID != vr.ResourceVersionID || lr.TenantID != tenant || lr.ListingID != id || lr.VersionRevisionID != rel.VersionRevisionID || vr.TenantID != tenant || v.TenantID != tenant || v.ResourceID != r.ID {
			return nil, E(CodeDependencyUnavailable, "invalid released Listing revision")
		}
		missing := ListingMetadataChecks{
			Documentation: q.Checks.Documentation && lr.DocumentationURL == "",
			Support:       q.Checks.Support && lr.SupportURL == "",
			License:       q.Checks.License && lr.License == "",
		}
		if missing == (ListingMetadataChecks{}) {
			continue
		}
		items = append(items, ListingMetadataGap{ListingID: id, NamespaceID: l.NamespaceID, ResourceID: r.ID, ReleaseID: rel.ID, ListingRevisionID: lr.ID, Kind: r.Kind, DisplayName: lr.DisplayName, Version: v.Version, State: rel.State, Missing: missing.Fields()})
		if len(items) == q.Limit {
			break
		}
	}
	return items, nil
}
