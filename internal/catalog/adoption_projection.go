package catalog

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"slices"
)

type AdoptionCandidate struct {
	ResourceID string
	Identifier string
	Kind       Kind
	Provenance AdoptionProvenance
}
type AdoptionReader interface {
	FindAdoptions(context.Context, string, []string) ([]AdoptionCandidate, error)
}

// FindAdoptions projects only authorized resources for the selected identifiers.
func (m *MemoryRepository) FindAdoptions(ctx context.Context, tenant string, identifiers []string) ([]AdoptionCandidate, error) {
	if len(identifiers) > 100 {
		return nil, E(CodeInvalidArgument, "adoption page exceeds its bound")
	}
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return nil, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]AdoptionCandidate, error) {
			return m.FindAdoptions(ctx, tenant, identifiers)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []AdoptionCandidate{}
	for _, resource := range m.resources {
		if resource.TenantID != tenant || resource.DeletedAt != nil || !slices.Contains(identifiers, resource.CanonicalIdentifier) {
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
			if revision.TenantID != tenant || revision.Adoption == nil {
				continue
			}
			version := m.versions[revision.ResourceVersionID]
			if version.ResourceID == resource.ID {
				out = append(out, AdoptionCandidate{resource.ID, resource.CanonicalIdentifier, resource.Kind, *revision.Adoption})
			}
		}
	}
	return out, nil
}
