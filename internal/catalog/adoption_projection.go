package catalog

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"strings"
)

type AdoptionCandidate struct {
	ResourceID string
	Identifier string
	Kind       Kind
	Provenance AdoptionProvenance
}

// AdoptionLookup selects one exact external target. Local labels remain opaque;
// unversioned targets are verified against retained observations by federation.
type AdoptionLookup struct {
	SourceID       string
	Identifier     string
	Kind           Kind
	VersionPresent bool
	Version        string
}
type AdoptionReader interface {
	FindAdoptions(context.Context, string, []AdoptionLookup) ([]AdoptionCandidate, error)
}

// FindAdoptions projects only authorized resources for the selected identifiers.
func (m *MemoryRepository) FindAdoptions(ctx context.Context, tenant string, targets []AdoptionLookup) ([]AdoptionCandidate, error) {
	if len(targets) > 100 {
		return nil, E(CodeInvalidArgument, "adoption page exceeds its bound")
	}
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return nil, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]AdoptionCandidate, error) {
			return m.FindAdoptions(ctx, tenant, targets)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []AdoptionCandidate{}
	for _, target := range targets {
		matches := 0
		for _, resource := range m.resources {
			if resource.TenantID != tenant || resource.DeletedAt != nil || resource.CanonicalIdentifier != target.Identifier || resource.Kind != target.Kind {
				continue
			}
			allowed, err := identity.CollectionAllows(ctx, "resource.read", resource.NamespaceID)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			for _, revision := range m.versionRevisions {
				if revision.TenantID != tenant || revision.Adoption == nil || revision.Adoption.SourceID != target.SourceID {
					continue
				}
				version := m.versions[revision.ResourceVersionID]
				if version.ResourceID == resource.ID && (!target.VersionPresent || version.Version == strings.TrimSpace(target.Version)) {
					out = append(out, AdoptionCandidate{resource.ID, resource.CanonicalIdentifier, resource.Kind, *revision.Adoption})
					matches++
					break
				}
			}
			if matches == 2 {
				break
			}
		}
	}
	return out, nil
}
