package federation

import (
	"context"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type AdoptVersionInput struct {
	ObservationID          string `json:"observationId"`
	SnapshotID             string `json:"snapshotId"`
	RetrievalDigest        string `json:"retrievalDigest"`
	EntryLocation          string `json:"entryLocation"`
	PriorVersionRevisionID string `json:"priorVersionRevisionId"`
}

type versionAdopter interface {
	AdoptionVersionProof(context.Context, string, string) (catalog.Resource, catalog.ResourceVersion, catalog.AdoptionProvenance, error)
	FindVersionAdoptionTarget(context.Context, string, string, catalog.Kind, string) (catalog.AdoptionTarget, error)
	AdoptExternalVersion(context.Context, catalog.ExternalVersionAdoptionInput) (catalog.ExternalVersionAdoption, error)
}

// WithVersionAdoption enables this explicit authoring command at startup.
// Its zero/default value leaves both target lookup and append disabled.
func WithVersionAdoption(enabled bool) ManagerOption {
	return func(m *Manager) error { m.versionAdoptionEnabled = enabled; return nil }
}

func (m *Manager) VersionAdoptionEnabled() bool { return m != nil && m.versionAdoptionEnabled }

func (m *Manager) versionAdopter(ctx context.Context) (versionAdopter, identity.Actor, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, err := permissionActor(ctx, "source.read_evidence")
	if err != nil {
		return nil, a, err
	}
	if !m.VersionAdoptionEnabled() {
		return nil, a, catalog.E(catalog.CodeForbidden, "upstream Version adoption is disabled by the administrator")
	}
	v, ok := m.adopter.(versionAdopter)
	if !ok {
		return nil, a, catalog.E(catalog.CodeDependencyUnavailable, "upstream Version adoption is unavailable")
	}
	return v, a, nil
}

func (m *Manager) VersionAdoptionTarget(ctx context.Context, observationID string) (catalog.AdoptionTarget, error) {
	ctx = identity.WithPermissionContext(ctx)
	v, a, err := m.versionAdopter(ctx)
	if err != nil {
		return catalog.AdoptionTarget{}, err
	}
	obs, err := m.store.GetObservationMetadata(ctx, a.TenantID, observationID)
	if err != nil {
		return catalog.AdoptionTarget{}, err
	}
	if !obs.VersionPresent || strings.TrimSpace(obs.Version) == "" {
		return catalog.AdoptionTarget{}, catalog.E(catalog.CodePreconditionFailed, "a versioned observation is required")
	}
	target, err := v.FindVersionAdoptionTarget(ctx, obs.SourceID, obs.Identifier, obs.Kind, strings.TrimSpace(obs.Version))
	if err != nil {
		return catalog.AdoptionTarget{}, err
	}
	_, _, proof, err := v.AdoptionVersionProof(ctx, target.ResourceID, target.PriorVersionRevisionID)
	if err != nil {
		return catalog.AdoptionTarget{}, err
	}
	if err = m.admitPriorVersion(ctx, a.TenantID, proof, obs); err != nil {
		return catalog.AdoptionTarget{}, err
	}
	target.UpstreamVersion = obs.Version
	return target, nil
}

func (m *Manager) admitPriorVersion(ctx context.Context, tenant string, proof catalog.AdoptionProvenance, selected Observation) error {
	old, err := m.store.GetObservationMetadata(ctx, tenant, proof.ObservationID)
	if err != nil {
		return err
	}
	// Freshness belongs to the NEW observation; old retained proof survives
	// upstream withdrawal and never needs to be the current local candidate.
	if old.SourceID != selected.SourceID || proof.SourceID != selected.SourceID || old.Identifier != selected.Identifier || old.Kind != selected.Kind || !old.VersionPresent || strings.TrimSpace(old.Version) == "" || strings.TrimSpace(old.Version) == strings.TrimSpace(selected.Version) {
		return catalog.E(catalog.CodePreconditionFailed, "the prior adoption must be a different declared version from the same Source")
	}
	return nil
}

func (m *Manager) AdoptVersion(ctx context.Context, resourceID string, input AdoptVersionInput) (catalog.ExternalVersionAdoption, error) {
	ctx = identity.WithPermissionContext(ctx)
	v, a, err := m.versionAdopter(ctx)
	if err != nil {
		return catalog.ExternalVersionAdoption{}, err
	}
	_, _, proof, err := v.AdoptionVersionProof(ctx, resourceID, input.PriorVersionRevisionID)
	if err != nil {
		return catalog.ExternalVersionAdoption{}, err
	}
	obs, err := m.store.GetObservationMetadata(ctx, a.TenantID, input.ObservationID)
	if err != nil {
		return catalog.ExternalVersionAdoption{}, err
	}
	if !obs.VersionPresent || strings.TrimSpace(obs.Version) == "" {
		return catalog.ExternalVersionAdoption{}, catalog.E(catalog.CodePreconditionFailed, "a declared upstream version is required")
	}
	if err = m.admitPriorVersion(ctx, a.TenantID, proof, obs); err != nil {
		return catalog.ExternalVersionAdoption{}, err
	}
	selection, err := m.prepareAdoption(ctx, input.ObservationID, AdoptInput{SnapshotID: input.SnapshotID, RetrievalDigest: input.RetrievalDigest, EntryLocation: input.EntryLocation})
	if err != nil {
		return catalog.ExternalVersionAdoption{}, err
	}
	return v.AdoptExternalVersion(ctx, catalog.ExternalVersionAdoptionInput{ResourceID: resourceID, PriorVersionRevisionID: input.PriorVersionRevisionID, Selection: selection})
}
