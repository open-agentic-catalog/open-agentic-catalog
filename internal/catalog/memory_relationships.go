package catalog

import (
	"context"
	"sort"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func (m *MemoryRepository) ListReleaseRelationships(ctx context.Context, tenant, releaseID string, request RelationshipPageRequest) (RelationshipSelection, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (RelationshipSelection, error) {
			return m.ListReleaseRelationships(ctx, tenant, releaseID, request)
		})
	}
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return RelationshipSelection{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	release := m.releases[releaseID]
	listing := m.listings[release.ListingID]
	resource := m.resources[listing.ResourceID]
	missing := E(CodeNotFound, "release relationships were not found")
	if release.TenantID != tenant || resource.DeletedAt != nil {
		return RelationshipSelection{}, missing
	}
	allowed, err := identity.CollectionAllows(ctx, "resource.read", listing.NamespaceID)
	if err != nil {
		return RelationshipSelection{}, err
	}
	if !allowed {
		return RelationshipSelection{}, missing
	}
	if request.View == ReleaseDependencies {
		allowed, err = identity.CollectionAllows(ctx, "resource.read_history", listing.NamespaceID)
		if err != nil {
			return RelationshipSelection{}, err
		}
	} else {
		allowed = listing.CurrentReleaseID == releaseID && (release.State == ReleasePublished || release.State == ReleaseDeprecated)
	}
	if !allowed {
		return RelationshipSelection{}, missing
	}
	records, err := m.authorizedCatalogRecordsLocked(ctx, tenant)
	if err != nil {
		return RelationshipSelection{}, err
	}
	page := RelationshipSelection{Items: []ReleasedRelationship{}, Generation: RelationshipGeneration(ctx, catalogSearchGeneration(records))}
	if request.Generation != "" && request.Generation != page.Generation {
		return RelationshipSelection{}, E(CodeInvalidArgument, "release relationships changed; restart the read")
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Release.ID < records[j].Release.ID })
	targetVersion := m.versions[m.versionRevisions[release.VersionRevisionID].ResourceVersionID].Version
	destinations := make(map[string]*RelatedRelease)
	if request.View == OutgoingRelationships {
		for _, record := range records {
			destinations[record.Resource.CanonicalIdentifier] = relatedRelease(record)
		}
	}
	for _, record := range records {
		source := record.Release.ID
		if source < request.AfterReleaseID || (request.View == OutgoingRelationships && source != releaseID) {
			continue
		}
		for i, declaration := range m.listingRevisions[record.ListingRevision.ID].Relationships {
			if source == request.AfterReleaseID && i+1 <= request.AfterOrdinal {
				continue
			}
			var related *RelatedRelease
			if request.View == OutgoingRelationships {
				related = destinations[declaration.TargetIdentifier]
				if related != nil && declaration.TargetVersion != "" && related.Version != declaration.TargetVersion {
					related = nil
				}
			} else {
				if declaration.TargetIdentifier != resource.CanonicalIdentifier {
					continue
				}
				if request.View == ReleaseDependencies && (declaration.Type != "depends_on" || (declaration.TargetVersion != "" && declaration.TargetVersion != targetVersion)) {
					continue
				}
				related = relatedRelease(record)
			}
			page.Items = append(page.Items, ReleasedRelationship{Declaration: declaration, Related: related, SourceReleaseID: source, Ordinal: i + 1})
			if len(page.Items) > request.Limit {
				return page, nil
			}
		}
	}
	return page, nil
}

func relatedRelease(record CatalogSummary) *RelatedRelease {
	return &RelatedRelease{ReleaseID: record.Release.ID, ListingRevisionID: record.ListingRevision.ID, DisplayName: record.ListingRevision.DisplayName, Version: record.Version.Version}
}
