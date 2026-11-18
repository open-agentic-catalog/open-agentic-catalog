package catalog

import (
	"context"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// AdoptionTarget identifies one retained adoption without returning native content.
type AdoptionTarget struct {
	ResourceID             string `json:"resourceId"`
	NamespaceID            string `json:"namespaceId"`
	PriorVersionRevisionID string `json:"priorVersionRevisionId"`
	Version                string `json:"version"`
	UpstreamVersion        string `json:"upstreamVersion"`
}

type ExternalVersionAdoptionInput struct {
	ResourceID             string
	PriorVersionRevisionID string
	Selection              ExternalObservationAdoptionInput
}

type ExternalVersionAdoption struct {
	ResourceID      string          `json:"resourceId"`
	Version         ResourceVersion `json:"version"`
	VersionRevision VersionRevision `json:"versionRevision"`
	Evidence        EvidenceSet     `json:"evidence"`
}

type AdoptExternalVersionParams struct {
	CreateVersionParams
	Resource                Resource
	PriorVersionRevisionID  string
	SourceAdmissionRevision int64
}

// AdoptionVersionProof requires current exact-namespace edit authority even for
// a historical proof. The caller must separately admit current source evidence.
func (s *Service) AdoptionVersionProof(ctx context.Context, resourceID, revisionID string) (Resource, ResourceVersion, AdoptionProvenance, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return Resource{}, ResourceVersion{}, AdoptionProvenance{}, err
	}
	r, err := s.repo.GetResource(ctx, actor.TenantID, resourceID)
	if err != nil {
		return Resource{}, ResourceVersion{}, AdoptionProvenance{}, err
	}
	if _, err = requirePermission(ctx, "resource.edit", r.NamespaceID); err != nil {
		return Resource{}, ResourceVersion{}, AdoptionProvenance{}, err
	}
	v, proof, err := s.repo.GetVersionAdoptionProof(ctx, actor.TenantID, r.ID, revisionID)
	if err != nil {
		return Resource{}, ResourceVersion{}, AdoptionProvenance{}, err
	}
	return r, v, proof, nil
}

// FindVersionAdoptionTarget selects at most one currently editable same-source
// destination. Store selection is bounded; native content is never projected.
func (s *Service) FindVersionAdoptionTarget(ctx context.Context, source, identifier string, kind Kind, selectedVersion string) (AdoptionTarget, error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return AdoptionTarget{}, err
	}
	return s.repo.FindVersionAdoptionTarget(ctx, actor.TenantID, source, identifier, kind, selectedVersion)
}

// AdoptExternalVersion creates only a child Version and its immutable evidence.
// It deliberately uses Version-created evidence, preserving Resource origin.
func (s *Service) AdoptExternalVersion(ctx context.Context, input ExternalVersionAdoptionInput) (ExternalVersionAdoption, error) {
	ctx = identity.WithPermissionContext(ctx)
	r, prior, proof, err := s.AdoptionVersionProof(ctx, input.ResourceID, input.PriorVersionRevisionID)
	if err != nil {
		return ExternalVersionAdoption{}, err
	}
	selection := input.Selection
	label := strings.TrimSpace(selection.Version)
	if r.CanonicalIdentifier != selection.CanonicalIdentifier || r.Kind != selection.Kind || proof.SourceID != selection.Provenance.SourceID || prior.Version == label {
		return ExternalVersionAdoption{}, E(CodePreconditionFailed, "the selected different version must match this Resource and its prior Source")
	}
	if label == "" || !validTextLength(label, 128) {
		return ExternalVersionAdoption{}, E(CodeInvalidArgument, "a nonempty upstream version is required")
	}
	if err := validateAdoptionProvenance(selection.Provenance); err != nil {
		return ExternalVersionAdoption{}, err
	}
	actor, err := requirePermission(ctx, "resource.edit", r.NamespaceID)
	if err != nil {
		return ExternalVersionAdoption{}, err
	}
	now := s.clock.Now()
	id, err := s.ids.New(now)
	if err != nil {
		return ExternalVersionAdoption{}, Wrap(CodeInternal, "create adopted Version identifier", err)
	}
	v := ResourceVersion{ID: id, TenantID: actor.TenantID, ResourceID: r.ID, Version: label, Revision: 1, CreatedAt: now, UpdatedAt: now}
	revision, evidence, documents, err := s.buildVersionRevision(ctx, actor, r.Kind, id, label, 1, now, []RepresentationInput{selection.Representation})
	if err != nil {
		return ExternalVersionAdoption{}, err
	}
	selection.Provenance.AdoptedBy, selection.Provenance.AdoptedAt = actor.Subject, now
	revision.Adoption = &selection.Provenance
	v.CandidateRevisionID = revision.ID
	a, o, err := s.evidence(ctx, actor, "resource_version.created", "resource_version", id, map[string]any{"version": v, "revision": revision, "evidence": evidence})
	if err != nil {
		return ExternalVersionAdoption{}, err
	}
	params := AdoptExternalVersionParams{CreateVersionParams: CreateVersionParams{Version: v, VersionRevision: revision, Documents: documents, Evidence: evidence, Audit: a, Outbox: o}, Resource: r, PriorVersionRevisionID: input.PriorVersionRevisionID, SourceAdmissionRevision: selection.SourceAdmissionRevision}
	var result ExternalVersionAdoption
	commit := func(ctx context.Context) error {
		var e error
		result, e = s.repo.AdoptExternalVersion(ctx, params)
		return e
	}
	if selection.AdmitCommit == nil {
		err = commit(ctx)
	} else {
		err = selection.AdmitCommit(ctx, commit)
	}
	return result, err
}

// AdoptExternalVersion rechecks the exact immutable proof under the parent lock.
func (m *MemoryRepository) AdoptExternalVersion(ctx context.Context, p AdoptExternalVersionParams) (ExternalVersionAdoption, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var out ExternalVersionAdoption
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error { var e error; out, e = m.AdoptExternalVersion(ctx, p); return e })
		return out, err
	}
	if err := ValidateAdoptedVersionBinding(p); err != nil {
		return ExternalVersionAdoption{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.resources[p.Resource.ID]
	old, proofOK := m.versionRevisions[p.PriorVersionRevisionID]
	previous := m.versions[old.ResourceVersionID]
	if !ok || r.TenantID != p.Resource.TenantID || r.DeletedAt != nil || r.CanonicalIdentifier != p.Resource.CanonicalIdentifier || r.Kind != p.Resource.Kind || !proofOK || old.TenantID != r.TenantID || previous.ResourceID != r.ID || old.Adoption == nil || p.VersionRevision.Adoption == nil || old.Adoption.SourceID != p.VersionRevision.Adoption.SourceID || previous.Version == p.Version.Version {
		return ExternalVersionAdoption{}, E(CodePreconditionFailed, "prior adoption or Resource is no longer eligible")
	}
	v, revision, evidence, err := m.createVersionLocked(ctx, p.CreateVersionParams)
	if err != nil {
		return ExternalVersionAdoption{}, err
	}
	return ExternalVersionAdoption{ResourceID: r.ID, Version: v, VersionRevision: revision, Evidence: evidence}, nil
}

func (m *MemoryRepository) FindVersionAdoptionTarget(ctx context.Context, tenant, source, identifier string, kind Kind, selectedVersion string) (AdoptionTarget, error) {
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return AdoptionTarget{}, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (AdoptionTarget, error) {
			return m.FindVersionAdoptionTarget(ctx, tenant, source, identifier, kind, selectedVersion)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.resources {
		if r.TenantID != tenant || r.DeletedAt != nil || r.CanonicalIdentifier != identifier || r.Kind != kind {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.edit", r.NamespaceID)
		if err != nil {
			return AdoptionTarget{}, err
		}
		if !allowed {
			continue
		}
		for _, rev := range m.versionRevisions {
			v := m.versions[rev.ResourceVersionID]
			if rev.TenantID == tenant && rev.Adoption != nil && rev.Adoption.SourceID == source && v.ResourceID == r.ID && v.Version != selectedVersion {
				return AdoptionTarget{ResourceID: r.ID, NamespaceID: r.NamespaceID, PriorVersionRevisionID: rev.ID, Version: v.Version}, nil
			}
		}
	}
	return AdoptionTarget{}, E(CodeNotFound, "no editable same-source adoption is available")
}

// GetVersionAdoptionProof reads only exact same-parent metadata, never another
// Resource's native content or all of this Resource's version history.
func (m *MemoryRepository) GetVersionAdoptionProof(ctx context.Context, tenant, resourceID, revisionID string) (ResourceVersion, AdoptionProvenance, error) {
	if identity.NeedsReadAdmission(ctx) {
		type proofResult struct {
			v ResourceVersion
			p AdoptionProvenance
		}
		result, err := identity.WithReadAdmission(ctx, func(ctx context.Context) (proofResult, error) {
			v, p, e := m.GetVersionAdoptionProof(ctx, tenant, resourceID, revisionID)
			return proofResult{v, p}, e
		})
		return result.v, result.p, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	rev, ok := m.versionRevisions[revisionID]
	v := m.versions[rev.ResourceVersionID]
	if !ok || rev.TenantID != tenant || v.TenantID != tenant || v.ResourceID != resourceID || rev.Adoption == nil {
		return ResourceVersion{}, AdoptionProvenance{}, E(CodeNotFound, "prior adoption was not found")
	}
	return clone(v), *rev.Adoption, nil
}

// ValidateAdoptedVersionBinding protects the repository child-writer boundary.
func ValidateAdoptedVersionBinding(p AdoptExternalVersionParams) error {
	if p.Version.TenantID != p.Resource.TenantID || p.Version.ResourceID != p.Resource.ID || p.VersionRevision.TenantID != p.Resource.TenantID || p.VersionRevision.ResourceVersionID != p.Version.ID || p.VersionRevision.Adoption == nil {
		return E(CodeInvalidArgument, "adopted Version must belong to the selected Resource")
	}
	return nil
}
