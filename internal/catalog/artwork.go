package catalog

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// ListingArtwork projects only the reviewed reserved extension. Other listing
// extensions remain opaque and never grant image retrieval authority.
func ListingArtwork(extensions map[string]any) *artwork.Presentation {
	value, exists := extensions[artwork.IRI]
	if !exists {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	descriptor, err := artwork.DecodeDescriptor(raw, artwork.HardLimits())
	if err != nil {
		return nil
	}
	return &artwork.Presentation{Descriptor: &descriptor, IconSource: "listing", ScreenshotsSource: "listing"}
}

func (s *Service) sealListingArtwork(ctx context.Context, tenant string, extensions map[string]any) (map[string]any, error) {
	value, exists := extensions[artwork.IRI]
	if !exists {
		return extensions, nil
	}
	if s.artwork == nil {
		return nil, E(CodeDependencyUnavailable, "listing artwork is unavailable")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, E(CodeInvalidArgument, "listing artwork is invalid")
	}
	input, err := artwork.DecodeInput(raw, s.artwork.Limits())
	if err != nil {
		return nil, E(CodeInvalidArgument, "listing artwork is invalid")
	}
	descriptor, err := s.artwork.Seal(ctx, tenant, input)
	if err != nil {
		if errors.Is(err, artwork.ErrInvalid) {
			return nil, E(CodeInvalidArgument, "listing artwork failed image validation")
		}
		return nil, E(CodeDependencyUnavailable, "listing artwork could not be verified")
	}
	result := make(map[string]any, len(extensions))
	for key, value := range extensions {
		result[key] = value
	}
	result[artwork.IRI] = descriptor
	return result, nil
}

// ReadListingArtwork admits current release readers or authorized content
// reviewers. An old or withdrawn revision is never available through browse.
func (s *Service) ReadListingArtwork(ctx context.Context, revisionID string) (*artwork.Presentation, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	revision, err := s.repo.GetListingRevision(ctx, actor.TenantID, revisionID)
	if err != nil {
		return nil, err
	}
	listing, err := s.repo.GetListing(ctx, actor.TenantID, revision.ListingID)
	if err != nil {
		return nil, err
	}
	if listing.CurrentReleaseID != "" {
		record, readErr := s.GetCatalogRecord(ctx, listing.CurrentReleaseID)
		if readErr == nil && record.ListingRevision.ID == revisionID {
			return ListingArtwork(record.ListingRevision.Extensions), nil
		}
	}
	if _, err := requirePermission(ctx, "resource.read_content", listing.NamespaceID); err != nil {
		return nil, err
	}
	return ListingArtwork(revision.Extensions), nil
}
