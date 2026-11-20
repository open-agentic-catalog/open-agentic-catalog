package catalog

import (
	"context"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// ReleaseObservation identifies the current release and its disposition at read time.
type ReleaseObservation struct {
	ReleaseID string `json:"releaseId"`
	Revision  int64  `json:"revision"`
}

func (s *Service) CurrentListingRelease(ctx context.Context, listingID string) (ReleaseObservation, error) {
	ctx = identity.WithPermissionContext(ctx)
	listing, err := s.GetListing(ctx, listingID)
	if err != nil {
		return ReleaseObservation{}, err
	}
	return s.repo.CurrentListingRelease(ctx, listing.TenantID, listing.ID)
}

func (m *MemoryRepository) CurrentListingRelease(ctx context.Context, tenant, id string) (ReleaseObservation, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (ReleaseObservation, error) { return m.CurrentListingRelease(ctx, tenant, id) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	listing := m.listings[id]
	release := m.releases[listing.CurrentReleaseID]
	resource := m.resources[listing.ResourceID]
	if listing.TenantID != tenant || resource.TenantID != tenant || resource.DeletedAt != nil || release.TenantID != tenant || release.ListingID != id || (release.State != ReleasePublished && release.State != ReleaseDeprecated) {
		return ReleaseObservation{}, E(CodeNotFound, "followed Listing is unavailable")
	}
	return ReleaseObservation{ReleaseID: release.ID, Revision: release.Revision}, nil
}
