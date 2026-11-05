// The in-memory history repository implements tenant-scoped parent lookups and
// deterministic continuation ordering for retained lifecycle records.

package catalog

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"sort"
)

// HasPublishedIdentity implements local precedence without enumerating pages.
func (m *MemoryRepository) HasPublishedIdentity(ctx context.Context, tenantID, identifier, versionLabel string) (bool, error) {
	if err := identity.RequireCollectionTenant(ctx, tenantID); err != nil {
		return false, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (bool, error) {
			return m.HasPublishedIdentity(ctx, tenantID, identifier, versionLabel)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, release := range m.releases {
		listing := m.listings[release.ListingID]
		resource := m.resources[listing.ResourceID]
		if listing.TenantID != tenantID {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.read", listing.NamespaceID)
		if err != nil {
			return false, err
		}
		if !allowed {
			continue
		}
		revision := m.versionRevisions[release.VersionRevisionID]
		version := m.versions[revision.ResourceVersionID]
		if listing.CurrentReleaseID == release.ID && release.TenantID == tenantID && listing.TenantID == tenantID && resource.TenantID == tenantID && revision.TenantID == tenantID && version.TenantID == tenantID && resource.DeletedAt == nil && resource.CanonicalIdentifier == identifier && version.Version == versionLabel && (release.State == ReleasePublished || release.State == ReleaseDeprecated) {
			return true, nil
		}
	}
	return false, nil
}

// ListVersions returns a resource's versions in ascending ID order, scoped to
// its tenant and parent resource, matching the PostgreSQL history contract.
func (m *MemoryRepository) ListVersions(ctx context.Context, tenantID, parent string, request HistoryPageRequest) ([]ResourceVersion, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]ResourceVersion, error) {
			return m.ListVersions(ctx, tenantID, parent, request)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	resourceID := parent
	resource, exists := m.resources[resourceID]
	if !exists || resource.TenantID != tenantID || resource.DeletedAt != nil {
		return []ResourceVersion{}, nil
	}
	items := make([]ResourceVersion, 0)
	for _, item := range m.versions {
		if item.TenantID == tenantID && item.ResourceID == parent && item.ID > request.AfterID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if request.Limit > 0 && len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return clone(items), nil
}

// ListVersionRevisions returns ascending immutable revision summaries after the
// requested ID for the exact tenant parent.
func (m *MemoryRepository) ListVersionRevisions(ctx context.Context, tenantID, parent string, request HistoryPageRequest) ([]RevisionSummary, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]RevisionSummary, error) {
			return m.ListVersionRevisions(ctx, tenantID, parent, request)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	version := m.versions[parent]
	if version.TenantID != tenantID {
		return []RevisionSummary{}, nil
	}
	resourceID := version.ResourceID
	resource, exists := m.resources[resourceID]
	if !exists || resource.TenantID != tenantID || resource.DeletedAt != nil {
		return []RevisionSummary{}, nil
	}
	items := make([]RevisionSummary, 0)
	for _, item := range m.versionRevisions {
		if item.TenantID == tenantID && item.ResourceVersionID == parent && item.ID > request.AfterID {
			items = append(items, RevisionSummary{ID: item.ID, RevisionNumber: item.RevisionNumber, Digest: item.ContentSetDigest, CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if request.Limit > 0 && len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return clone(items), nil
}

// ListListings returns a bounded ascending-ID page of listings for the exact
// tenant resource.
func (m *MemoryRepository) ListListings(ctx context.Context, tenantID, parent string, request HistoryPageRequest) ([]CatalogListing, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]CatalogListing, error) {
			return m.ListListings(ctx, tenantID, parent, request)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	resourceID := parent
	resource, exists := m.resources[resourceID]
	if !exists || resource.TenantID != tenantID || resource.DeletedAt != nil {
		return []CatalogListing{}, nil
	}
	items := make([]CatalogListing, 0)
	for _, item := range m.listings {
		if item.TenantID == tenantID && item.ResourceID == parent && item.ID > request.AfterID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if request.Limit > 0 && len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return clone(items), nil
}

// ListListingRevisions returns ascending immutable metadata summaries for the
// exact tenant listing.
func (m *MemoryRepository) ListListingRevisions(ctx context.Context, tenantID, parent string, request HistoryPageRequest) ([]RevisionSummary, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]RevisionSummary, error) {
			return m.ListListingRevisions(ctx, tenantID, parent, request)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	listing := m.listings[parent]
	if listing.TenantID != tenantID {
		return []RevisionSummary{}, nil
	}
	resourceID := listing.ResourceID
	resource, exists := m.resources[resourceID]
	if !exists || resource.TenantID != tenantID || resource.DeletedAt != nil {
		return []RevisionSummary{}, nil
	}
	items := make([]RevisionSummary, 0)
	for _, item := range m.listingRevisions {
		if item.TenantID == tenantID && item.ListingID == parent && item.ID > request.AfterID {
			items = append(items, RevisionSummary{ID: item.ID, RevisionNumber: item.RevisionNumber, Digest: item.Digest, CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if request.Limit > 0 && len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return clone(items), nil
}

// ListReleases returns a bounded ascending-ID page of retained releases for the
// tenant listing.
func (m *MemoryRepository) ListReleases(ctx context.Context, tenantID, parent string, request HistoryPageRequest) ([]ListingRelease, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]ListingRelease, error) {
			return m.ListReleases(ctx, tenantID, parent, request)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	listing := m.listings[parent]
	if listing.TenantID != tenantID {
		return []ListingRelease{}, nil
	}
	resourceID := listing.ResourceID
	resource, exists := m.resources[resourceID]
	if !exists || resource.TenantID != tenantID || resource.DeletedAt != nil {
		return []ListingRelease{}, nil
	}
	items := make([]ListingRelease, 0)
	for _, item := range m.releases {
		if item.TenantID == tenantID && item.ListingID == parent && item.ID > request.AfterID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if request.Limit > 0 && len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return clone(items), nil
}

// ListSubmissions applies tenant, optional listing and state filters before
// returning the bounded ID-ordered page.
func (m *MemoryRepository) ListSubmissions(ctx context.Context, tenantID string, request SubmissionListRequest) ([]ReviewSubmission, error) {
	if err := identity.RequireCollectionTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]ReviewSubmission, error) {
			return m.ListSubmissions(ctx, tenantID, request)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]ReviewSubmission, 0)
	for _, item := range m.submissions {
		listing, ok := m.listings[item.ListingID]
		if !ok || listing.TenantID != tenantID {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.read_history", listing.NamespaceID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		resource, ok := m.resources[listing.ResourceID]
		if !ok || resource.TenantID != tenantID || resource.DeletedAt != nil {
			continue
		}
		if item.TenantID == tenantID && item.ID > request.AfterID && (request.ListingID == "" || item.ListingID == request.ListingID) && (request.State == "" || item.State == request.State) {
			if request.Unreleased && m.submissionReleasedLocked(tenantID, item.ID) {
				continue
			}
			item.CanonicalIdentifier, item.SubmittedDisplayName = "", ""
			metadata, err := identity.CollectionAllows(ctx, "resource.read", listing.NamespaceID)
			if err != nil {
				return nil, err
			}
			content, err := identity.CollectionAllows(ctx, "resource.read_content", listing.NamespaceID)
			if err != nil {
				return nil, err
			}
			if metadata {
				item.CanonicalIdentifier = resource.CanonicalIdentifier
			}
			if revision, ok := m.listingRevisions[item.ListingRevisionID]; content && ok && revision.TenantID == tenantID && revision.ListingID == item.ListingID {
				item.SubmittedDisplayName = revision.DisplayName
			}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if request.Limit > 0 && len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return clone(items), nil
}
