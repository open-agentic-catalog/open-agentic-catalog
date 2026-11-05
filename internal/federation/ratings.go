package federation

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"net/url"
)

type RatingSignalTarget struct {
	Target      ratings.Target `json:"target"`
	EntryDigest string         `json:"entryDigest"`
}
type RatingSignals struct {
	SourceEndpoint     string
	Peer               *ratings.Summary
	ImportedResourceID string
	ChangedEntry       bool
}
type RatingSignalStore interface {
	RatingSignals(context.Context, string, []RatingSignalTarget) (map[string]RatingSignals, error)
}

func ExtractRatingSummary(unknown map[string]any, source Source, identifier string, present bool, version string) json.RawMessage {
	value := unknown[ratings.IRI]
	if value == nil {
		if extensions, ok := unknown["extensions"].(map[string]any); ok {
			value = extensions[ratings.IRI]
		}
	}
	if literal, ok := value.(map[string]any); ok {
		if literal["@type"] == "@json" {
			value = literal["@value"]
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	u, err := url.Parse(source.Endpoint)
	if err != nil {
		return nil
	}
	summary := ratings.DecodeSummary(raw, "https://"+u.Host, identifier, present, version)
	if summary == nil {
		return nil
	}
	raw, err = json.Marshal(summary)
	if err != nil {
		return nil
	}
	return raw
}
func (m *Manager) ResolveRatingSignals(ctx context.Context, items []catalog.CatalogSearchResult) ([]catalog.CatalogSearchResult, error) {
	store, ok := m.store.(RatingSignalStore)
	if !ok {
		return items, nil
	}
	ctx, a, err := identity.WithCollectionContext(ctx)
	if err != nil {
		return nil, catalog.PermissionError("current discovery authority is required", err)
	}
	if len(items) > 100 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "rating signals page exceeds its bound")
	}
	requests := []RatingSignalTarget{}
	for _, item := range items {
		if target, ok := signalTarget(item); ok {
			digest := ""
			if item.Indexed != nil {
				digest = item.Indexed.NormalizedEntryDigest
			} else if item.Live != nil {
				digest = observationDigest(item.Live.Entry)
			}
			requests = append(requests, RatingSignalTarget{target, digest})
		}
	}
	if len(requests) == 0 {
		return items, nil
	}
	signals, err := store.RatingSignals(ctx, a.TenantID, requests)
	if err != nil {
		return nil, err
	}
	for i := range items {
		item := &items[i]
		target, ok := signalTarget(*item)
		if !ok {
			continue
		}
		signal := signals[ratings.Key(target)]
		if item.Live != nil {
			var unknown map[string]any
			if json.Unmarshal(item.Live.Entry, &unknown) == nil {
				source := Source{Endpoint: signal.SourceEndpoint}
				raw := ExtractRatingSummary(unknown, source, item.Identifier, item.VersionPresent, item.Version)
				if len(raw) > 0 {
					_ = json.Unmarshal(raw, &signal.Peer)
				}
			}
		}
		if signal.Peer != nil {
			summary := signal.Peer
			average := summary.Average()
			item.PeerRatings = &catalog.PeerRatingSummary{Reporter: summary.Reporter, Count: summary.Count, Average: average, MeasuredAt: summary.MeasuredAt}
		}
		if signal.ImportedResourceID != "" {
			item.ImportedHere = &catalog.ImportedResource{ResourceID: signal.ImportedResourceID, ChangedEntry: signal.ChangedEntry}
		}
	}
	return items, nil
}
func signalTarget(item catalog.CatalogSearchResult) (ratings.Target, bool) {
	target := ratings.Target{Origin: "foreign", Identifier: item.Identifier, VersionPresent: item.VersionPresent, Version: item.Version, Kind: item.Kind}
	switch {
	case item.Indexed != nil:
		target.SourceID = item.Indexed.PresentationSourceID
		target.PresentationGeneration = item.Indexed.PresentationGeneration
	case item.Live != nil:
		target.SourceID = item.Live.SourceID
		target.PresentationGeneration = item.Live.PresentationGeneration
	default:
		return target, false
	}
	return target, ratings.ValidateTarget(target) == nil
}
func (m *MemoryStore) RatingSignals(ctx context.Context, tenant string, requests []RatingSignalTarget) (map[string]RatingSignals, error) {
	if len(requests) > 100 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "rating signals exceed their bound")
	}
	identifiers := make([]string, len(requests))
	for i, r := range requests {
		identifiers[i] = r.Target.Identifier
	}
	candidates := []catalog.AdoptionCandidate{}
	if reader, ok := m.localCatalog.(catalog.AdoptionReader); ok {
		var err error
		candidates, err = reader.FindAdoptions(ctx, tenant, identifiers)
		if err != nil {
			return nil, err
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]RatingSignals{}
	for _, request := range requests {
		target := request.Target
		source, ok := m.sources[target.SourceID]
		if !ok || source.TenantID != tenant || EffectivePresentationGeneration(source) != target.PresentationGeneration || source.Status != SourceActive && source.Status != SourceError {
			continue
		}
		signal := RatingSignals{SourceEndpoint: source.Endpoint}
		for _, obs := range m.observations {
			if obs.TenantID == tenant && obs.SourceID == target.SourceID && obs.Identifier == target.Identifier && obs.VersionPresent == target.VersionPresent && obs.Version == target.Version && obs.Kind == target.Kind && obs.Freshness == FreshCurrent && obs.NormalizedEntryDigest == request.EntryDigest && len(obs.RatingsMetadata) > 0 {
				origin, parseErr := url.Parse(source.Endpoint)
				if parseErr == nil && obs.Completeness == CompleteEntry && obs.Compatibility.Structural == "valid" && obs.Compatibility.Support == "supported" {
					signal.Peer = ratings.DecodeSummary(obs.RatingsMetadata, "https://"+origin.Host, target.Identifier, target.VersionPresent, target.Version)
				}
			}
		}
		matched := map[string]bool{}
		for _, candidate := range candidates {
			p := candidate.Provenance
			obs, ok := m.observations[p.ObservationID]
			generation := m.presentationGenerations[p.SourceID][p.SourceRevision]
			if generation == 0 && p.SourceRevision == source.Revision {
				generation = EffectivePresentationGeneration(source)
			}
			if !ok || obs.TenantID != tenant || p.SourceID != target.SourceID || generation != target.PresentationGeneration || candidate.Identifier != target.Identifier || candidate.Kind != target.Kind || obs.VersionPresent != target.VersionPresent || obs.Version != target.Version {
				continue
			}
			matched[candidate.ResourceID] = true
			signal.ImportedResourceID = candidate.ResourceID
			signal.ChangedEntry = request.EntryDigest != "" && p.NormalizedEntryDigest != request.EntryDigest
		}
		if len(matched) != 1 {
			signal.ImportedResourceID = ""
		}
		out[ratings.Key(target)] = signal
	}
	return out, nil
}

// AdmitRating selects the requested current contribution, not another peer's
// contribution with the same AIR. It performs no network request.
func (m *Manager) AdmitRating(ctx context.Context, input ratings.Admission) (ratings.Admission, error) {
	ctx = identity.WithPermissionContext(ctx)
	target := ArtworkTarget{SourceID: input.Target.SourceID, PresentationGeneration: input.Target.PresentationGeneration, Identifier: input.Target.Identifier, VersionPresent: input.Target.VersionPresent, Version: input.Target.Version, Kind: input.Target.Kind}
	source, err := m.ArtworkSourceContext(ctx, target, input.SourceRevision, input.Live)
	if err != nil {
		return input, err
	}
	input.SourceRevision = source.Revision
	if !input.Live {
		actor, _ := identity.FromContext(ctx)
		store, ok := m.store.(ArtworkReadStore)
		if !ok {
			return input, catalog.E(catalog.CodeDependencyUnavailable, "rating source admission is unavailable")
		}
		values, err := store.ArtworkReadBatch(ctx, actor.TenantID, []ArtworkReadRequest{{Target: target}})
		if err != nil {
			return input, err
		}
		selected, ok := values[target]
		if !ok || !selected.Eligible || selected.Source.Revision != source.Revision || selected.EntryDigest == "" {
			return input, catalog.E(catalog.CodePreconditionFailed, "rated contribution is unavailable")
		}
		input.EntryDigest = selected.EntryDigest
	}
	input.ReleaseID, input.NamespaceID = "", ""
	return input, nil
}
