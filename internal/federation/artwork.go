package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// ArtworkTarget names a tenant-local presentation choice without adopting or
// rewriting the external resource. Tenant is always supplied by authentication.
type ArtworkTarget struct {
	SourceID               string       `json:"sourceId"`
	PresentationGeneration int64        `json:"presentationGeneration"`
	Identifier             string       `json:"identifier"`
	VersionPresent         bool         `json:"versionPresent"`
	Version                string       `json:"version,omitempty"`
	Kind                   catalog.Kind `json:"kind"`
}

type ArtworkMode string

const (
	ArtworkInherit  ArtworkMode = "inherit"
	ArtworkReplace  ArtworkMode = "replace"
	ArtworkSuppress ArtworkMode = "suppress"
)

type ArtworkIconSelection struct {
	Mode  ArtworkMode    `json:"mode"`
	Asset *artwork.Asset `json:"asset,omitempty"`
}
type ArtworkScreenshotSelection struct {
	Mode   ArtworkMode     `json:"mode"`
	Assets []artwork.Asset `json:"assets,omitempty"`
}
type ArtworkOverrideInput struct {
	SourceRevision int64                      `json:"-"`
	Icon           ArtworkIconSelection       `json:"icon"`
	Screenshots    ArtworkScreenshotSelection `json:"screenshots"`
	Reason         string                     `json:"reason"`
}
type ArtworkOverride struct {
	ID          string                     `json:"id"`
	TenantID    string                     `json:"tenantId"`
	Target      ArtworkTarget              `json:"target"`
	Revision    int64                      `json:"revision"`
	Icon        ArtworkIconSelection       `json:"icon"`
	Screenshots ArtworkScreenshotSelection `json:"screenshots"`
	Reason      string                     `json:"reason"`
	CreatedBy   string                     `json:"createdBy"`
	CreatedAt   time.Time                  `json:"createdAt"`
	Digest      string                     `json:"digest"`
}

// ArtworkStore keeps current heads separate from immutable administrative history.
// All methods use the explicit tenant, including worker-capable implementations.
type ArtworkStore interface {
	SaveArtworkOverride(context.Context, ArtworkOverride, int64, int64, MutationEvidence) (ArtworkOverride, error)
	GetArtworkOverride(context.Context, string, ArtworkTarget) (ArtworkOverride, error)
	ListArtworkOverrideHistory(context.Context, string, ArtworkTarget, string, int) ([]ArtworkOverride, error)
}

func EffectivePresentationGeneration(source Source) int64 {
	if source.PresentationGeneration < 1 {
		return 1
	}
	return source.PresentationGeneration
}
func NextPresentationGeneration(before, after Source) int64 {
	generation := EffectivePresentationGeneration(before)
	if before.Type != after.Type || before.Endpoint != after.Endpoint || before.RegistrySearchURL != after.RegistrySearchURL || before.RegistryListURL != after.RegistryListURL || before.ProfileID != after.ProfileID {
		generation++
	}
	return generation
}
func ValidateArtworkTarget(target ArtworkTarget) error {
	identifier, err := catalog.ParseAIRIdentifier(target.Identifier)
	if err != nil || identifier.String() != target.Identifier || len(target.Identifier) > 512 || !identity.ValidTenantID(target.SourceID) || target.PresentationGeneration < 1 || !target.Kind.Valid() || !validDiscoveryVersion(target.Version) || !safeRetrievalText(target.Version, 4*128, true) || !target.VersionPresent && target.Version != "" {
		return catalog.E(catalog.CodeInvalidArgument, "artwork target is invalid")
	}
	return nil
}
func ArtworkTargetKey(target ArtworkTarget) string {
	return fmt.Sprintf("%s/%d/%s/%s", target.SourceID, target.PresentationGeneration, sourceScopedObservationKey(target.Identifier, target.Version, target.VersionPresent, "", "", ""), target.Kind)
}
func (m *Manager) artworkStore() (ArtworkStore, error) {
	store, ok := m.store.(ArtworkStore)
	if !ok {
		return nil, catalog.E(catalog.CodeDependencyUnavailable, "artwork overrides are unavailable")
	}
	return store, nil
}
func (m *Manager) GetArtworkOverride(ctx context.Context, target ArtworkTarget) (ArtworkOverride, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return ArtworkOverride{}, err
	}
	if err = ValidateArtworkTarget(target); err != nil {
		return ArtworkOverride{}, err
	}
	store, err := m.artworkStore()
	if err != nil {
		return ArtworkOverride{}, err
	}
	return store.GetArtworkOverride(ctx, actor.TenantID, target)
}

// SetArtworkOverride accepts only assets already sealed by the shared image
// validator; transport handlers must never accept client-supplied sealed facts.
func (m *Manager) SetArtworkOverride(ctx context.Context, target ArtworkTarget, input ArtworkOverrideInput, expectedRevision int64) (ArtworkOverride, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.manage")
	if err != nil {
		return ArtworkOverride{}, err
	}
	if err = ValidateArtworkTarget(target); err != nil {
		return ArtworkOverride{}, err
	}
	if expectedRevision < 0 || input.SourceRevision < 1 {
		return ArtworkOverride{}, catalog.E(catalog.CodeInvalidArgument, "artwork revision is invalid")
	}
	reason, err := audit.NormalizeReason(audit.ActionSourceArtworkRevised, input.Reason)
	if err != nil {
		return ArtworkOverride{}, catalog.E(catalog.CodeInvalidArgument, "artwork reason is invalid")
	}
	if err = validateArtworkSelection(input); err != nil {
		return ArtworkOverride{}, err
	}
	source, err := m.store.GetSource(ctx, actor.TenantID, target.SourceID)
	if err != nil {
		return ArtworkOverride{}, err
	}
	if source.Revision != input.SourceRevision || EffectivePresentationGeneration(source) != target.PresentationGeneration {
		return ArtworkOverride{}, catalog.E(catalog.CodePreconditionFailed, "source presentation generation changed")
	}
	if scopeExcludesIdentifier(source.Scope, target.Identifier) && (input.Icon.Mode != ArtworkInherit || input.Screenshots.Mode != ArtworkInherit) {
		return ArtworkOverride{}, catalog.E(catalog.CodePreconditionFailed, "source scope excludes this resource")
	}
	store, err := m.artworkStore()
	if err != nil {
		return ArtworkOverride{}, err
	}
	previous, err := store.GetArtworkOverride(ctx, actor.TenantID, target)
	if err != nil && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		return ArtworkOverride{}, err
	}
	if previous.Revision != expectedRevision {
		return ArtworkOverride{}, catalog.E(catalog.CodePreconditionFailed, "artwork revision does not match")
	}
	now := catalog.TransitionTime(m.clock.Now(), previous.CreatedAt)
	idValue, err := m.ids.New(now)
	if err != nil {
		return ArtworkOverride{}, err
	}
	revision := ArtworkOverride{ID: idValue, TenantID: actor.TenantID, Target: target, Revision: expectedRevision + 1, Icon: input.Icon, Screenshots: input.Screenshots, Reason: reason, CreatedBy: actor.Subject, CreatedAt: now}
	revision.Digest = artworkOverrideDigest(revision)
	evidence, err := m.mutationEvidence(ctx, actor, audit.ActionSourceArtworkRevised, "source_artwork", revision.ID, reason, map[string]any{"sourceId": target.SourceID, "generation": target.PresentationGeneration, "revision": revision.Revision, "iconMode": string(input.Icon.Mode), "screenshotsMode": string(input.Screenshots.Mode)}, previous.Digest, revision.Digest, "", "", now)
	if err != nil {
		return ArtworkOverride{}, err
	}
	return store.SaveArtworkOverride(ctx, revision, expectedRevision, source.Revision, evidence)
}
func (m *Manager) ArtworkOverrideHistory(ctx context.Context, target ArtworkTarget, afterID string, limit int) ([]ArtworkOverride, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return nil, err
	}
	if err = ValidateArtworkTarget(target); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 || afterID != "" && !identity.ValidTenantID(afterID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "artwork history page is invalid")
	}
	store, err := m.artworkStore()
	if err != nil {
		return nil, err
	}
	return store.ListArtworkOverrideHistory(ctx, actor.TenantID, target, afterID, limit+1)
}
func artworkOverrideDigest(value ArtworkOverride) string {
	value.Digest = ""
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return observationDigest(raw)
}
func validateArtworkSelection(input ArtworkOverrideInput) error {
	validMode := func(mode ArtworkMode) bool {
		return mode == ArtworkInherit || mode == ArtworkReplace || mode == ArtworkSuppress
	}
	if !validMode(input.Icon.Mode) || !validMode(input.Screenshots.Mode) || (input.Icon.Mode == ArtworkReplace) != (input.Icon.Asset != nil) || input.Screenshots.Mode != ArtworkReplace && len(input.Screenshots.Assets) > 0 || input.Screenshots.Mode == ArtworkReplace && len(input.Screenshots.Assets) == 0 || len(input.Screenshots.Assets) > 8 {
		return catalog.E(catalog.CodeInvalidArgument, "artwork selection is invalid")
	}
	if err := artwork.ValidateDescriptor(artwork.Descriptor{SchemaVersion: 1, Icon: input.Icon.Asset, Screenshots: input.Screenshots.Assets}, artwork.HardLimits()); err != nil {
		return catalog.E(catalog.CodeInvalidArgument, "artwork asset lacks validated facts")
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 32768 {
		return catalog.E(catalog.CodeInvalidArgument, "artwork selection exceeds its bound")
	}
	return nil
}
func cloneArtworkOverride(value ArtworkOverride) ArtworkOverride {
	cloneAsset := func(asset artwork.Asset) artwork.Asset {
		if asset.Rights != nil {
			rights := *asset.Rights
			asset.Rights = &rights
		}
		return asset
	}
	if value.Icon.Asset != nil {
		asset := cloneAsset(*value.Icon.Asset)
		value.Icon.Asset = &asset
	}
	value.Screenshots.Assets = append([]artwork.Asset(nil), value.Screenshots.Assets...)
	for i := range value.Screenshots.Assets {
		value.Screenshots.Assets[i] = cloneAsset(value.Screenshots.Assets[i])
	}
	return value
}

func (m *MemoryStore) GetArtworkOverride(_ context.Context, tenant string, target ArtworkTarget) (ArtworkOverride, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, ok := m.artworkHeads[tenant+"/"+ArtworkTargetKey(target)]
	if !ok {
		return ArtworkOverride{}, catalog.E(catalog.CodeNotFound, "artwork override was not found")
	}
	return cloneArtworkOverride(value), nil
}
func (m *MemoryStore) SaveArtworkOverride(ctx context.Context, value ArtworkOverride, expected, sourceRevision int64, evidence MutationEvidence) (ArtworkOverride, error) {
	if err := ValidateArtworkOverrideIntegrity(value); err != nil {
		return ArtworkOverride{}, err
	}
	if identity.NeedsPermissionAdmission(ctx) {
		var result ArtworkOverride
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var err error
			result, err = m.SaveArtworkOverride(ctx, value, expected, sourceRevision, evidence)
			return err
		})
		return result, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	source, ok := m.sources[value.Target.SourceID]
	if !ok || source.TenantID != value.TenantID || source.Status == SourceRemoved || source.Revision != sourceRevision || EffectivePresentationGeneration(source) != value.Target.PresentationGeneration {
		return ArtworkOverride{}, catalog.E(catalog.CodePreconditionFailed, "source presentation changed")
	}
	key := value.TenantID + "/" + ArtworkTargetKey(value.Target)
	previous := m.artworkHeads[key]
	if previous.Revision != expected || value.Revision != expected+1 {
		return ArtworkOverride{}, catalog.E(catalog.CodePreconditionFailed, "artwork revision does not match")
	}
	if err := ValidateArtworkMutationEvidence(value, previous.Digest, evidence); err != nil {
		return ArtworkOverride{}, err
	}
	if m.artworkHeads == nil {
		m.artworkHeads = make(map[string]ArtworkOverride)
	}
	if m.artworkRevisions == nil {
		m.artworkRevisions = make(map[string]ArtworkOverride)
	}
	if err := m.appendEvidence(ctx, value.TenantID, value.ID, []MutationEvidence{evidence}, audit.ActionSourceArtworkRevised); err != nil {
		return ArtworkOverride{}, err
	}
	m.artworkHeads[key] = cloneArtworkOverride(value)
	m.artworkRevisions[value.ID] = cloneArtworkOverride(value)
	return cloneArtworkOverride(value), nil
}
func (m *MemoryStore) ListArtworkOverrideHistory(_ context.Context, tenant string, target ArtworkTarget, after string, limit int) ([]ArtworkOverride, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := []ArtworkOverride{}
	var afterRevision int64
	if after != "" {
		prior, ok := m.artworkRevisions[after]
		if !ok || prior.TenantID != tenant || prior.Target != target {
			return result, nil
		}
		afterRevision = prior.Revision
	}
	for _, value := range m.artworkRevisions {
		if value.TenantID == tenant && value.Target == target && value.Revision > afterRevision {
			result = append(result, cloneArtworkOverride(value))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Revision < result[j].Revision })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// ArtworkCandidates is a compact, rebuildable projection; declarations grant no
// network authority. Exact native and unknown entry evidence remains retained.
type ArtworkCandidates struct {
	Publisher  *artwork.Descriptor `json:"publisher,omitempty"`
	NativeIcon *artwork.AssetInput `json:"nativeIcon,omitempty"`
}

func ExtractArtworkCandidates(unknown map[string]any, data json.RawMessage) json.RawMessage {
	candidates := ArtworkCandidates{}
	raw, err := json.Marshal(unknown)
	if err != nil {
		raw = nil
	}
	candidates.Publisher, _ = artwork.Extract(raw, artwork.HardLimits())
	if candidates.Publisher == nil {
		candidates.Publisher, _ = artwork.ExtractAI(raw, artwork.HardLimits())
	}

	var native struct {
		IconURL string              `json:"iconUrl"`
		Icons   []nativeArtworkIcon `json:"icons"`
	}
	if len(data) > 0 && json.Unmarshal(data, &native) == nil {
		if validNativeArtworkURL(native.IconURL) {
			candidates.NativeIcon = &artwork.AssetInput{URL: native.IconURL, Alt: "Source-provided resource icon"}
		} else {
			candidates.NativeIcon = selectNativeArtworkIcon(native.Icons)
		}
	}

	if candidates.Publisher == nil && candidates.NativeIcon == nil {
		return nil
	}
	raw, err = json.Marshal(candidates)
	if err != nil || len(raw) > 32768 {
		return nil
	}
	return raw
}

type ArtworkReadRequest struct {
	Target      ArtworkTarget `json:"target"`
	EntryDigest string        `json:"entryDigest"`
	Live        bool          `json:"live"`
}
type ArtworkReadContext struct {
	EntryDigest string
	Source      Source
	Override    *ArtworkOverride
	Candidates  ArtworkCandidates
	Tags        []string
	Eligible    bool
}
type ArtworkReadStore interface {
	ArtworkReadBatch(context.Context, string, []ArtworkReadRequest) (map[ArtworkTarget]ArtworkReadContext, error)
}

// ArtworkSourceContext checks current source admission for an already signed
// live target. The caller must verify the receipt before invoking this method.
func (m *Manager) ArtworkSourceContext(ctx context.Context, target ArtworkTarget, sourceRevision int64, live bool) (Source, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "federation.discover")
	if err != nil {
		return Source{}, err
	}
	if err = ValidateArtworkTarget(target); err != nil {
		return Source{}, err
	}
	source, err := m.store.GetSource(ctx, actor.TenantID, target.SourceID)
	if err != nil {
		return Source{}, err
	}
	if scopeExcludesIdentifier(source.Scope, target.Identifier) || EffectivePresentationGeneration(source) != target.PresentationGeneration || sourceRevision > 0 && source.Revision != sourceRevision || !m.sourceUsesCurrentPolicy(source) || live && (source.Status != SourceActive || !source.Live.Enabled) || !live && source.Status != SourceActive && source.Status != SourceError {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source artwork admission changed")
	}
	return source, nil
}

// ResolveArtwork decorates only the final selected consumer page using one
// bounded tenant-scoped batch. It never changes membership, ranking or cursors.
func (m *Manager) ResolveArtwork(ctx context.Context, items []catalog.CatalogSearchResult) ([]catalog.CatalogSearchResult, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "federation.discover")
	if err != nil {
		return nil, err
	}
	if len(items) > 100 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "artwork page exceeds its bound")
	}
	store, ok := m.store.(ArtworkReadStore)
	if !ok {
		return items, nil
	}
	requests := []ArtworkReadRequest{}
	for _, item := range items {
		var request ArtworkReadRequest
		request.Target = ArtworkTarget{Identifier: item.Identifier, Version: item.Version, VersionPresent: item.VersionPresent, Kind: item.Kind}
		switch {
		case item.Indexed != nil:
			request.Target.SourceID = item.Indexed.PresentationSourceID
			request.Target.PresentationGeneration = item.Indexed.PresentationGeneration
			request.EntryDigest = item.Indexed.NormalizedEntryDigest
		case item.Live != nil:
			request.Live = true
			request.Target.SourceID = item.Live.SourceID
			request.Target.PresentationGeneration = item.Live.PresentationGeneration
			request.EntryDigest = observationDigest(item.Live.Entry)
		default:
			continue
		}
		if ValidateArtworkTarget(request.Target) == nil {
			requests = append(requests, request)
		}
	}
	contexts, err := store.ArtworkReadBatch(ctx, actor.TenantID, requests)
	if err != nil {
		return nil, err
	}
	result := append([]catalog.CatalogSearchResult(nil), items...)
	for index := range result {
		item := &result[index]
		target := ArtworkTarget{Identifier: item.Identifier, Version: item.Version, VersionPresent: item.VersionPresent, Kind: item.Kind}
		switch {
		case item.Indexed != nil:
			target.SourceID = item.Indexed.PresentationSourceID
			target.PresentationGeneration = item.Indexed.PresentationGeneration
		case item.Live != nil:
			target.SourceID = item.Live.SourceID
			target.PresentationGeneration = item.Live.PresentationGeneration
		default:
			continue
		}
		read, exists := contexts[target]
		if !exists || !read.Eligible || !m.sourceUsesCurrentPolicy(read.Source) {
			continue
		}
		if item.Live != nil {
			if read.Source.Revision != item.Live.SourceRevision || EffectivePresentationGeneration(read.Source) != item.Live.PresentationGeneration || read.Source.PolicyDigest != item.Live.PolicyDigest || read.Source.CredentialRevision != item.Live.CredentialRevision || read.Source.Status != SourceActive || !read.Source.Live.Enabled {
				continue
			}
			var entry map[string]json.RawMessage
			if json.Unmarshal(item.Live.Entry, &entry) != nil {
				continue
			}
			live := *item.Live
			live.ArtworkAdmitted = true
			if read.Override != nil {
				live.ArtworkOverrideDigest = read.Override.Digest
			} else {
				live.ArtworkOverrideDigest = ""
			}
			item.Live = &live
			var unknown map[string]any
			_ = json.Unmarshal(item.Live.Entry, &unknown)
			raw := ExtractArtworkCandidates(unknown, entry["data"])
			_ = json.Unmarshal(raw, &read.Candidates)
		}
		item.Artwork = resolveArtworkPresentation(read)
	}
	return result, nil
}
func resolveArtworkPresentation(read ArtworkReadContext) *artwork.Presentation {
	descriptor := artwork.Descriptor{SchemaVersion: 1}
	presentation := &artwork.Presentation{Descriptor: &descriptor}
	if read.Source.ArtworkEnabled {
		if read.Candidates.Publisher != nil {
			descriptor = *read.Candidates.Publisher
			if descriptor.Icon != nil {
				presentation.IconSource = "publisher"
			}
			if len(descriptor.Screenshots) > 0 {
				presentation.ScreenshotsSource = "publisher"
			}
		}
		if descriptor.Icon == nil && read.Candidates.NativeIcon != nil {
			descriptor.Icon = &artwork.Asset{AssetInput: *read.Candidates.NativeIcon, RightsAssessment: artwork.NotAssessed}
			presentation.IconSource = "native"
			presentation.MutableIcon = true
		}
	}
	if override := read.Override; override != nil {
		switch override.Icon.Mode {
		case ArtworkInherit:
			// Preserve the current source candidate.
		case ArtworkReplace:
			descriptor.Icon = override.Icon.Asset
			presentation.IconSource = "local_override"
			presentation.MutableIcon = false
		case ArtworkSuppress:
			descriptor.Icon = nil
			presentation.IconSource = "local_override"
			presentation.MutableIcon = false
		}
		switch override.Screenshots.Mode {
		case ArtworkInherit:
			// Preserve the current source candidates.
		case ArtworkReplace:
			descriptor.Screenshots = override.Screenshots.Assets
			presentation.ScreenshotsSource = "local_override"
		case ArtworkSuppress:
			descriptor.Screenshots = nil
			presentation.ScreenshotsSource = "local_override"
		}
	}
	if descriptor.Icon == nil && len(descriptor.Screenshots) == 0 && presentation.IconSource == "" && presentation.ScreenshotsSource == "" {
		return nil
	}
	return presentation
}
func (m *MemoryStore) ArtworkReadBatch(_ context.Context, tenant string, requests []ArtworkReadRequest) (map[ArtworkTarget]ArtworkReadContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(requests) > 100 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "artwork batch exceeds its bound")
	}
	result := map[ArtworkTarget]ArtworkReadContext{}
	for _, request := range requests {
		source, ok := m.sources[request.Target.SourceID]
		if !ok || source.TenantID != tenant || source.Status == SourceRemoved || EffectivePresentationGeneration(source) != request.Target.PresentationGeneration {
			continue
		}
		read := ArtworkReadContext{Source: source, Eligible: request.Live}
		if !request.Live {
			for _, observation := range m.observations {
				if observation.TenantID == tenant && observation.SourceID == source.ID && observation.Identifier == request.Target.Identifier && observation.Version == request.Target.Version && observation.VersionPresent == request.Target.VersionPresent && observation.Kind == request.Target.Kind && (request.EntryDigest == "" || observation.NormalizedEntryDigest == request.EntryDigest) && observation.Freshness == FreshCurrent && observation.Completeness == CompleteEntry && observation.Compatibility.Structural == "valid" && observation.Compatibility.Support == "supported" && (source.Status == SourceActive || source.Status == SourceError) && (source.RetrievalMode == RetrievalIndexed || source.RetrievalMode == RetrievalHybrid) && scopeMatches(source.Scope, sourceEntry{Identifier: observation.Identifier, Version: observation.Version, Type: observation.MediaType, Tags: observation.SearchTags}) {
					read.Eligible = true
					read.EntryDigest = observation.NormalizedEntryDigest
					_ = json.Unmarshal(observation.ArtworkMetadata, &read.Candidates)
					break
				}
			}
		}
		if value, ok := m.artworkHeads[tenant+"/"+ArtworkTargetKey(request.Target)]; ok {
			cloned := cloneArtworkOverride(value)
			read.Override = &cloned
		}
		result[request.Target] = read
	}
	return result, nil
}

type ArtworkConsequenceStore interface {
	ArtworkOverrideCount(context.Context, string, string, int64, int) (int, bool, error)
}

func (m *MemoryStore) ArtworkOverrideCount(_ context.Context, tenant, source string, generation int64, limit int) (int, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, head := range m.artworkHeads {
		if head.TenantID == tenant && head.Target.SourceID == source && head.Target.PresentationGeneration == generation {
			count++
			if count > limit {
				return limit, true, nil
			}
		}
	}
	return count, false, nil
}

// ValidateArtworkOverrideIntegrity verifies retained presentation authority.
func ValidateArtworkOverrideIntegrity(value ArtworkOverride) error {
	if !identity.ValidTenantID(value.ID) || !identity.ValidTenantID(value.TenantID) || value.Revision < 1 || value.CreatedBy == "" || value.CreatedAt.IsZero() || !validPolicyDigest(value.Digest) || value.Digest != artworkOverrideDigest(value) {
		return catalog.E(catalog.CodeDependencyUnavailable, "stored artwork integrity check failed")
	}
	if err := ValidateArtworkTarget(value.Target); err != nil {
		return catalog.E(catalog.CodeDependencyUnavailable, "stored artwork target is invalid")
	}
	if err := validateArtworkSelection(ArtworkOverrideInput{Icon: value.Icon, Screenshots: value.Screenshots, Reason: value.Reason}); err != nil {
		return catalog.E(catalog.CodeDependencyUnavailable, "stored artwork selection is invalid")
	}
	return nil
}

// ValidateLiveArtworkOverride rechecks mutable local presentation after receipt admission.
// The receipt supplies context, never authorization.
func (m *Manager) ValidateLiveArtworkOverride(ctx context.Context, target ArtworkTarget, expectedDigest string) error {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "federation.discover")
	if err != nil {
		return err
	}
	if err = ValidateArtworkTarget(target); err != nil {
		return err
	}

	if target.VersionPresent {
		collectionCtx, _, collectionErr := identity.WithCollectionContext(ctx)
		if collectionErr != nil {
			return catalog.PermissionError("current Catalog authority is required", collectionErr)
		}
		if local, ok := m.store.(artworkLocalIdentityStore); ok {
			shadowed, readErr := local.HasPublishedIdentity(collectionCtx, actor.TenantID, target.Identifier, target.Version)
			if readErr != nil {
				return readErr
			}
			if shadowed {
				return catalog.E(catalog.CodeNotFound, "live artwork target unavailable")
			}
		}
	}
	store, err := m.artworkStore()
	if err != nil {
		return err
	}
	current, err := store.GetArtworkOverride(ctx, actor.TenantID, target)
	if catalog.ErrorCodeOf(err) == catalog.CodeNotFound && expectedDigest == "" {
		return nil
	}
	if err != nil && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		return err
	}
	if current.Digest != expectedDigest {
		return catalog.E(catalog.CodePreconditionFailed, "artwork presentation changed")
	}
	return nil
}

// AdminArtworkSourceContext admits a current indexed target under source.manage.
// Administrative presentation does not require the separate consumer role.
func (m *Manager) AdminArtworkSourceContext(ctx context.Context, target ArtworkTarget) (Source, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.manage")
	if err != nil {
		return Source{}, err
	}
	if err = ValidateArtworkTarget(target); err != nil {
		return Source{}, err
	}
	store, ok := m.store.(ArtworkReadStore)
	if !ok {
		return Source{}, catalog.E(catalog.CodeDependencyUnavailable, "artwork context unavailable")
	}
	contexts, err := store.ArtworkReadBatch(ctx, actor.TenantID, []ArtworkReadRequest{{Target: target}})
	if err != nil {
		return Source{}, err
	}
	current, ok := contexts[target]
	if !ok || !current.Eligible || !m.sourceUsesCurrentPolicy(current.Source) {
		return Source{}, catalog.E(catalog.CodeNotFound, "artwork target unavailable")
	}
	return current.Source, nil
}

// ValidateArtworkMutationEvidence binds the append-only presentation transition
// to its attributed audit envelope, with no notification side effects.
func ValidateArtworkMutationEvidence(value ArtworkOverride, before string, evidence MutationEvidence) error {
	if evidence.Audit.ID == "" || evidence.Audit.AfterDigest != value.Digest || evidence.Audit.BeforeDigest != before || evidence.Audit.Reason != value.Reason || !evidence.Audit.At.Equal(value.CreatedAt) {
		return catalog.E(catalog.CodeInternal, "artwork evidence does not bind its transition")
	}
	if err := ValidateMutationEvidence(value.TenantID, value.ID, []MutationEvidence{evidence}, audit.ActionSourceArtworkRevised); err != nil {
		return err
	}
	facts, ok := evidence.Audit.Details["facts"].(map[string]any)
	if !ok || fmt.Sprint(facts["sourceId"]) != value.Target.SourceID || fmt.Sprint(facts["generation"]) != fmt.Sprint(value.Target.PresentationGeneration) || fmt.Sprint(facts["revision"]) != fmt.Sprint(value.Revision) || fmt.Sprint(facts["iconMode"]) != string(value.Icon.Mode) || fmt.Sprint(facts["screenshotsMode"]) != string(value.Screenshots.Mode) {
		return catalog.E(catalog.CodeInternal, "artwork audit facts do not match its transition")
	}
	return nil
}

type nativeArtworkIcon struct {
	Src      string   `json:"src"`
	MimeType string   `json:"mimeType"`
	Theme    string   `json:"theme"`
	Sizes    []string `json:"sizes"`
}

func validNativeArtworkURL(value string) bool {
	return catalog.ValidExternalHTTPSURL(value) && artwork.ValidateAsset(artwork.Asset{AssetInput: artwork.AssetInput{URL: value}}, artwork.HardLimits(), true, true) == nil
}

// Prefer declared PNG, then JPEG, then unspecified format; neutral, light,
// then dark theme; square 32-512px declarations; finally original order.
// These remain mutable hints: no byte digest, dimensions or rights are invented.
func selectNativeArtworkIcon(icons []nativeArtworkIcon) *artwork.AssetInput {
	bestRank := 1000
	var selected *artwork.AssetInput
	for _, icon := range icons[:min(len(icons), 16)] {
		if !validNativeArtworkURL(icon.Src) {
			continue
		}
		format := 2
		switch icon.MimeType {
		case "image/png":
			format = 0
		case "image/jpeg":
			format = 1
		case "":
		default:
			continue
		}
		theme := 0
		switch icon.Theme {
		case "":
		case "light":
			theme = 1
		case "dark":
			theme = 2
		default:
			continue
		}
		size := 1
		for _, declared := range icon.Sizes[:min(len(icon.Sizes), 16)] {
			w, h, ok := strings.Cut(declared, "x")
			if !ok {
				continue
			}
			width, e1 := strconv.Atoi(w)
			height, e2 := strconv.Atoi(h)
			if e1 == nil && e2 == nil && width == height && width >= 32 && width <= 512 {
				size = 0
				break
			}
		}
		rank := format*6 + theme*2 + size
		if rank < bestRank {
			bestRank = rank
			selected = &artwork.AssetInput{URL: icon.Src, Alt: "Source-provided resource icon"}
		}
	}
	return selected
}

type artworkLocalIdentityStore interface {
	HasPublishedIdentity(context.Context, string, string, string) (bool, error)
}

func (m *MemoryStore) HasPublishedIdentity(ctx context.Context, tenant, identifier, version string) (bool, error) {
	m.mu.RLock()
	local := m.localCatalog
	m.mu.RUnlock()
	if local == nil {
		return false, nil
	}
	return local.HasPublishedIdentity(ctx, tenant, identifier, version)
}

// ValidateArtworkMetadata bounds the rebuildable projection independently of
// immutable source evidence. Only sealed publisher facts or mutable native
// hints can enter the indexed presentation batch.
func ValidateArtworkMetadata(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var candidates ArtworkCandidates
	if len(raw) > 32768 || jsondoc.Decode(raw, &candidates) != nil {
		return catalog.E(catalog.CodeInvalidArgument, "artwork candidate projection is invalid")
	}
	if candidates.Publisher != nil {
		if err := artwork.ValidateDescriptor(*candidates.Publisher, artwork.HardLimits()); err != nil {
			return catalog.E(catalog.CodeInvalidArgument, "publisher artwork projection is invalid")
		}
	}
	if candidates.NativeIcon != nil {
		if err := artwork.ValidateAsset(artwork.Asset{AssetInput: *candidates.NativeIcon}, artwork.HardLimits(), true, true); err != nil {
			return catalog.E(catalog.CodeInvalidArgument, "native artwork projection is invalid")
		}
	}
	return nil
}
