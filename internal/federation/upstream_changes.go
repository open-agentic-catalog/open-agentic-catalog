package federation

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// UpstreamChangesQuery pages currently eligible observations in UUID order.
type UpstreamChangesQuery struct {
	Check, SourceID, NamespaceID, ObservationID, AfterID string
	Limit                                                int
}

// UpstreamChange binds one current complete occurrence to a specific local adoption.
type UpstreamChange struct {
	Target              catalog.AdoptionTarget `json:"target"`
	Identifier          string                 `json:"identifier"`
	Kind                catalog.Kind           `json:"kind"`
	MediaType           string                 `json:"mediaType"`
	Outcome             string                 `json:"outcome"`
	Occurrence          ObservationOccurrence  `json:"occurrence"`
	PriorDigest         string                 `json:"priorDigest"`
	PriorProfile        string                 `json:"priorProfile"`
	CurrentProfile      string                 `json:"currentProfile"`
	SnapshotCompletedAt time.Time              `json:"snapshotCompletedAt"`
	LatestAttempt       *SourceSyncAttempt     `json:"latestAttempt,omitempty"`
}

type upstreamChangesReader interface {
	ListUpstreamChanges(context.Context, string, UpstreamChangesQuery) ([]UpstreamChange, error)
}

func (m *Manager) UpstreamChanges(ctx context.Context, q UpstreamChangesQuery) ([]UpstreamChange, error) {
	ctx = identity.WithPermissionContext(ctx)
	_, actor, err := m.versionAdopter(ctx)
	if err != nil {
		return nil, err
	}
	if q.Limit < 1 || q.Limit > 100 || (q.Check != "all" && q.Check != "entry_comparison" && q.Check != "unadopted_version") {
		return nil, catalog.E(catalog.CodeInvalidArgument, "invalid upstream change selection")
	}
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		return nil, catalog.PermissionError("upstream changes admission failed", err)
	}
	reader, ok := m.store.(upstreamChangesReader)
	if !ok {
		return nil, catalog.E(catalog.CodeDependencyUnavailable, "upstream changes inventory is unavailable")
	}
	return reader.ListUpstreamChanges(ctx, actor.TenantID, q)
}

func invalidUpstreamBinding() error {
	return catalog.E(catalog.CodeDependencyUnavailable, "retained upstream change evidence is inconsistent")
}

// Validate only scalar bindings. Inventory reads never acquire native content.
func (row UpstreamChange) Validate() error {
	o := row.Occurrence
	if !o.EvidenceComplete || o.SourceID == "" || o.SnapshotID == "" || o.ObservationID == "" ||
		!validEntryLocation(o.EntryLocation) || o.SourceRevision < 1 || o.ObservedAt.IsZero() ||
		row.SnapshotCompletedAt.IsZero() || o.ObservedAt.After(row.SnapshotCompletedAt) ||
		row.CurrentProfile == "" || row.PriorProfile == "" || !validPolicyDigest(o.RetrievalDigest) ||
		!validPolicyDigest(o.ExactEntryDigest) || !validPolicyDigest(o.NormalizedEntryDigest) || !validPolicyDigest(row.PriorDigest) ||
		o.NormalizationInputDigest != "" && !validPolicyDigest(o.NormalizationInputDigest) {
		return invalidUpstreamBinding()
	}
	return nil
}

func (m *MemoryStore) ListUpstreamChanges(ctx context.Context, tenant string, q UpstreamChangesQuery) ([]UpstreamChange, error) {
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return nil, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]UpstreamChange, error) { return m.ListUpstreamChanges(ctx, tenant, q) })
	}
	allowed, err := identity.CollectionAllows(ctx, "source.read_evidence", "")
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, catalog.E(catalog.CodeForbidden, "source evidence permission is required")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	resources, err := m.adoptedResources(ctx, tenant, q.NamespaceID, "")
	if err != nil {
		return nil, err
	}
	byID := map[string]Observation{}
	type identityKey struct{ source, identifier, version string }
	digests := map[identityKey]string{}
	conflicts := map[identityKey]bool{}
	for _, o := range m.observations {
		if o.TenantID != tenant {
			continue
		}
		byID[o.ID] = o
		if !o.VersionPresent || o.Freshness != FreshCurrent || o.NormalizedEntryDigest == "" {
			continue
		}
		k := identityKey{o.SourceID, o.Identifier, o.Version}
		if d := digests[k]; d != "" && d != o.NormalizedEntryDigest {
			conflicts[k] = true
		}
		digests[k] = o.NormalizedEntryDigest
	}
	current := map[string]ObservationOccurrence{}
	for _, occ := range m.occurrences {
		src := m.sources[occ.SourceID]
		if src.TenantID != tenant || occ.SnapshotID != src.LastCompleteSnapshotID {
			continue
		}
		existing, ok := current[occ.ObservationID]
		if !ok || occ.EntryLocation < existing.EntryLocation || occ.EntryLocation == existing.EntryLocation && occ.RetrievalDigest < existing.RetrievalDigest {
			current[occ.ObservationID] = occ
		}
	}
	rows := []UpstreamChange{}
	attempts := map[string]*SourceSyncAttempt{}
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		o := byID[id]
		src := m.sources[o.SourceID]
		occ, member := current[id]
		if !member && (src.LastCompleteSnapshotID == "" || o.SnapshotID != src.LastCompleteSnapshotID) {
			continue
		}

		if id <= q.AfterID || q.ObservationID != "" && id != q.ObservationID || q.SourceID != "" && o.SourceID != q.SourceID {
			continue
		}
		r := resources[o.Identifier]
		if r == nil || r.Kind != o.Kind || o.SourceID != src.ID || !o.VersionPresent || strings.TrimSpace(o.Version) == "" ||
			o.Completeness != CompleteEntry || o.Freshness != FreshCurrent || !sourceAcceptsVersionAppend(src.Status) ||
			(src.RetrievalMode != RetrievalIndexed && src.RetrievalMode != RetrievalHybrid) ||
			o.Compatibility.Structural != "valid" || o.Compatibility.Support != "supported" ||
			!scopeMatches(src.Scope, sourceEntry{Identifier: o.Identifier, Version: o.Version, VersionPresent: true, Type: o.MediaType, Tags: o.SearchTags}) ||
			conflicts[identityKey{o.SourceID, o.Identifier, o.Version}] {
			continue
		}
		row, err := m.upstreamChange(r, o, occ, byID, q.Check)
		if err != nil {
			return nil, err
		}
		if row.Outcome != "" {
			if _, cached := attempts[src.ID]; !cached {
				attempts[src.ID] = m.latestCompletedSync(src)
			}
			row.LatestAttempt = attempts[src.ID]
			rows = append(rows, row)
			if len(rows) == q.Limit+1 {
				break
			}
		}
	}
	return rows, nil
}

func (m *MemoryStore) upstreamChange(r *catalog.AdoptedResourceInventory, o Observation, occ ObservationOccurrence, byID map[string]Observation, check string) (UpstreamChange, error) {
	var chosen string
	same := false
	for id, p := range r.Proofs {
		if p.SourceID != o.SourceID {
			continue
		}
		old, ok := byID[p.ObservationID]
		if !ok || old.Identifier != o.Identifier || old.SourceID != o.SourceID || old.Kind != o.Kind {
			return UpstreamChange{}, invalidUpstreamBinding()
		}
		if !old.VersionPresent || strings.TrimSpace(old.Version) == "" {
			continue
		}
		exact := old.Version == o.Version
		if !exact && (r.Labels[strings.TrimSpace(o.Version)] || strings.TrimSpace(old.Version) == strings.TrimSpace(o.Version)) {
			continue
		}
		if chosen == "" || exact && !same || exact == same && id < chosen {
			chosen, same = id, exact
		}
	}
	if chosen == "" || same && check == "unadopted_version" || !same && check == "entry_comparison" {
		return UpstreamChange{}, nil
	}
	p := r.Proofs[chosen]
	old := byID[p.ObservationID]
	prior := m.occurrences[occurrenceKey(o.TenantID, ObservationOccurrence{SnapshotID: p.SnapshotID, EntryLocation: p.EntryLocation, RetrievalDigest: p.RetrievalDigest})]
	snap := m.snapshots[occ.SnapshotID]
	if prior.ObservationID != old.ID || prior.SourceID != p.SourceID || !prior.EvidenceComplete ||
		prior.SourceRevision != p.SourceRevision || prior.ExactEntryDigest != p.ExactEntryDigest || prior.NormalizedEntryDigest != p.NormalizedEntryDigest ||
		old.NormalizationProfile != p.NormalizationProfile || old.NormalizedEntryDigest != p.NormalizedEntryDigest || old.ExactEntryDigest != p.ExactEntryDigest {
		return UpstreamChange{}, invalidUpstreamBinding()
	}
	if occ.ExactEntryDigest != o.ExactEntryDigest || occ.NormalizedEntryDigest != o.NormalizedEntryDigest ||
		snap.SourceID != o.SourceID || snap.TenantID != o.TenantID || snap.CompletedAt == nil ||
		snap.State != SnapshotComplete || !snap.EnumerationComplete || snap.SourceRevision != occ.SourceRevision {
		return UpstreamChange{}, invalidUpstreamBinding()
	}
	row := UpstreamChange{
		Target:     catalog.AdoptionTarget{ResourceID: r.ResourceID, NamespaceID: r.NamespaceID, PriorVersionRevisionID: chosen, Version: p.LocalVersion, UpstreamVersion: o.Version},
		Identifier: o.Identifier, Kind: o.Kind, MediaType: o.MediaType, Outcome: "unadopted_version", Occurrence: occ,
		PriorDigest: p.NormalizedEntryDigest, PriorProfile: p.NormalizationProfile, CurrentProfile: o.NormalizationProfile,
		SnapshotCompletedAt: *snap.CompletedAt,
	}

	if err := row.Validate(); err != nil {
		return UpstreamChange{}, err
	}
	if same {
		switch {
		case row.CurrentProfile != observationNormalizationProfile || row.PriorProfile != row.CurrentProfile:
			row.Outcome = "comparison_unavailable"
		case row.PriorDigest == occ.NormalizedEntryDigest:
			return UpstreamChange{}, nil
		default:
			row.Outcome = "changed_entry"
		}
	}
	return row, nil
}

func (m *MemoryStore) adoptedResources(ctx context.Context, tenant, namespace, identifier string) (map[string]*catalog.AdoptedResourceInventory, error) {
	local, ok := m.localCatalog.(interface {
		AdoptedResourceInventories(context.Context, string, string, string) (map[string]*catalog.AdoptedResourceInventory, error)
	})
	if !ok {
		return nil, catalog.E(catalog.CodeDependencyUnavailable, "local adoption evidence is unavailable")
	}
	return local.AdoptedResourceInventories(ctx, tenant, namespace, identifier)
}
