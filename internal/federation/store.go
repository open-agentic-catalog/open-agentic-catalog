// Federation persistence separates immutable observation occurrences from the
// current complete snapshot and validates evidence before replacing source
// state.

package federation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
)

// EvidenceSink is the narrow shared audit/outbox inventory used by the
// in-memory composition and by outbound operations that retain no domain row.
type EvidenceSink interface {
	AppendAuditEvent(context.Context, audit.Record) error
	AppendEvidence(context.Context, audit.Record, *catalog.OutboxEvent) error
}

// MutationEvidence carries atomic audit/outbox facts and, when needed, the
// observation membership checked during preparation outside the write lock.
type MutationEvidence struct {
	Audit                  audit.Record
	Outbox                 *catalog.OutboxEvent
	ExpectedObservationSet *ObservationSet
}

// ObservationSet fingerprints current membership, not all retained history.
// Store mutations use it to reject a change prepared against a replaced set.
type ObservationSet struct {
	Count  int
	Digest string
}

// Store is the tenant-scoped persistence boundary for source configuration and
// immutable observation history. CommitSync is the sole snapshot commit seam.
type Store interface {
	SaveInspection(context.Context, Inspection, ...MutationEvidence) (Inspection, error)
	GetInspection(context.Context, string, string) (Inspection, error)
	SaveChangePreview(context.Context, ChangePreview, ...MutationEvidence) (ChangePreview, error)
	GetChangePreview(context.Context, string, string) (ChangePreview, error)
	CreateSource(context.Context, Source, ...MutationEvidence) (Source, error)
	UpdateSource(context.Context, Source, int64, ...MutationEvidence) (Source, error)
	GetSource(context.Context, string, string) (Source, error)
	SourceEndpointConnected(context.Context, string, string, string) (bool, error)
	ListSourcePage(context.Context, string, SourceListRequest) ([]Source, error)
	ListDiscoveryLiveSources(context.Context, string, string, DiscoveryAudience, string, int) ([]Source, bool, error)
	ListDiscoveryReferralSources(context.Context, string, string, DiscoveryAudience, string, int) ([]Source, bool, error)
	CurrentObservationSet(context.Context, string, string, int) (ObservationSet, bool, error)
	GetRetrievalState(context.Context, string, string, int64, string) (RetrievalState, bool, error)
	// CommitSync appends immutable evidence and advances source state together,
	// guarded by the expected source revision. Partial/failed runs must preserve
	// membership from the last complete snapshot rather than infer withdrawals.
	CommitSync(context.Context, Snapshot, []Observation, Source, int64, ...MutationEvidence) (Snapshot, Source, error)
	GetSnapshot(context.Context, string, string) (Snapshot, error)
	ListSnapshots(context.Context, string, string, int) ([]Snapshot, error)
	ListSnapshotPage(context.Context, string, SnapshotListRequest) ([]Snapshot, error)
	CompareSnapshots(context.Context, string, SnapshotComparisonRequest) (SnapshotComparison, error)
	GetObservation(context.Context, string, string) (Observation, error)
	ObservationHasSnapshotEvidence(context.Context, string, string, string, string, int64, string) (bool, error)
	// Metadata reads omit RawEntry, NormalizedEntry and Occurrence. Call
	// GetObservation or ListCurrentObservations when retained bytes are needed.
	GetObservationMetadata(context.Context, string, string) (Observation, error)
	ListCurrentObservations(context.Context, string, string, int) ([]Observation, bool, error)
	ListObservationPage(context.Context, string, ObservationListRequest) ([]Observation, error)
	ListObservationGroupMembers(context.Context, string, string, string, bool, int) ([]Observation, error)
	ObservationGroupConflicts(context.Context, string, string, string, bool, []string) (bool, map[string]bool, error)
	SearchObservationPage(context.Context, string, ObservationSearchRequest) ([]SearchResult, error)
	SearchIndexedCatalog(context.Context, string, catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error)
	ListCatalogLiveSources(context.Context, string, string, int) ([]Source, bool, error)
	GetObservationOccurrence(context.Context, string, string, string, string, string) (ObservationOccurrence, error)
	ListObservationOccurrences(context.Context, string, string, int) ([]ObservationOccurrence, int, error)
	ListObservationOccurrencePage(context.Context, string, OccurrenceListRequest) ([]ObservationOccurrence, error)
	ObservationOccurrenceSummaries(context.Context, string, []string) (map[string]OccurrenceSummary, error)
}

type MemoryStore struct {
	presentationGenerations map[string]map[int64]int64
	artworkHeads            map[string]ArtworkOverride
	artworkRevisions        map[string]ArtworkOverride
	localCatalog            interface {
		HasPublishedIdentity(context.Context, string, string, string) (bool, error)
	}
	mu           sync.RWMutex
	inspections  map[string]Inspection
	previews     map[string]ChangePreview
	sources      map[string]Source
	snapshots    map[string]Snapshot
	observations map[string]Observation
	occurrences  map[string]ObservationOccurrence
	retrievals   map[string]RetrievalState
	evidence     EvidenceSink
}

// NewMemoryStore creates empty source, snapshot, observation and retrieval
// indexes with an optional single evidence sink.
func NewMemoryStore(evidence ...EvidenceSink) *MemoryStore {
	store := &MemoryStore{inspections: make(map[string]Inspection), previews: make(map[string]ChangePreview), sources: make(map[string]Source), snapshots: make(map[string]Snapshot), observations: make(map[string]Observation), occurrences: make(map[string]ObservationOccurrence), retrievals: make(map[string]RetrievalState)}
	if len(evidence) == 1 {
		store.evidence = evidence[0]
	}
	return store
}

// BindLocalCatalog installs the development Catalog's local-precedence reader.
// Composition calls this once before starting request/worker goroutines.
func (m *MemoryStore) BindLocalCatalog(reader interface {
	HasPublishedIdentity(context.Context, string, string, string) (bool, error)
}) {
	m.localCatalog = reader
}

// SaveChangePreview retains an administrator's proposed source configuration
// with its supplied evidence.
func (m *MemoryStore) SaveChangePreview(ctx context.Context, preview ChangePreview, evidence ...MutationEvidence) (ChangePreview, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 ChangePreview
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.SaveChangePreview(ctx, preview, evidence...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.previews[preview.Digest] = preview
	if err := m.appendEvidence(ctx, preview.TenantID, preview.ID, evidence, audit.ActionSourceChangePreviewCreated); err != nil {
		delete(m.previews, preview.Digest)
		return ChangePreview{}, err
	}
	return preview, nil
}

// GetChangePreview resolves a retained preview only within its tenant and
// digest binding.
func (m *MemoryStore) GetChangePreview(_ context.Context, tenantID, digest string) (ChangePreview, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	preview, ok := m.previews[digest]
	if !ok || preview.TenantID != tenantID {
		return ChangePreview{}, catalog.E(catalog.CodeNotFound, "source change preview was not found")
	}
	return preview, nil
}

// SaveInspection retains inspection results and supplied audit evidence
// together.
func (m *MemoryStore) SaveInspection(ctx context.Context, inspection Inspection, evidence ...MutationEvidence) (Inspection, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Inspection
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.SaveInspection(ctx, inspection, evidence...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.inspections[inspection.ID] = inspection
	if err := m.appendEvidence(ctx, inspection.TenantID, inspection.ID, evidence, audit.ActionSourceInspected); err != nil {
		delete(m.inspections, inspection.ID)
		return Inspection{}, err
	}
	return inspection, nil
}

// GetInspection resolves retained inspection evidence for the explicit tenant.
func (m *MemoryStore) GetInspection(_ context.Context, tenantID, idValue string) (Inspection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inspection, ok := m.inspections[idValue]
	if !ok || inspection.TenantID != tenantID {
		return Inspection{}, catalog.E(catalog.CodeNotFound, "source inspection was not found")
	}
	return inspection, nil
}

// CreateSource verifies the tenant inspection binding and endpoint uniqueness
// before retaining source evidence.
func (m *MemoryStore) CreateSource(ctx context.Context, source Source, evidence ...MutationEvidence) (Source, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Source
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.CreateSource(ctx, source, evidence...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if source.InspectionID != "" {
		inspection, ok := m.inspections[source.InspectionID]
		if !ok || inspection.TenantID != source.TenantID || !inspection.ExpiresAt.After(source.CreatedAt) {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "source inspection is missing or expired")
		}
	}
	for _, existing := range m.sources {
		if existing.TenantID == source.TenantID && existing.Endpoint == source.Endpoint && existing.Status != SourceRemoved {
			return Source{}, catalog.E(catalog.CodeConflict, "source endpoint is already connected")
		}
	}
	stored := source
	stored.InspectionID = ""
	if m.presentationGenerations == nil {
		m.presentationGenerations = map[string]map[int64]int64{}
	}
	if m.presentationGenerations[source.ID] == nil {
		m.presentationGenerations[source.ID] = map[int64]int64{}
	}
	m.presentationGenerations[source.ID][source.Revision] = EffectivePresentationGeneration(source)
	m.sources[source.ID] = stored
	if err := m.appendEvidence(ctx, source.TenantID, source.ID, evidence, audit.ActionSourceConnected); err != nil {
		delete(m.sources, source.ID)
		return Source{}, err
	}
	return source, nil
}

// UpdateSource applies a single optimistic revision change and restores prior
// state if supplied evidence fails.
func (m *MemoryStore) UpdateSource(ctx context.Context, source Source, expectedRevision int64, evidence ...MutationEvidence) (Source, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Source
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.UpdateSource(ctx, source, expectedRevision, evidence...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.sources[source.ID]
	if !ok || current.TenantID != source.TenantID {
		return Source{}, catalog.E(catalog.CodeNotFound, "federation source was not found")
	}
	if expectedRevision > 0 && current.Revision != expectedRevision {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source revision does not match")
	}
	if source.Revision != current.Revision+1 {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source update must create the next immutable revision")
	}
	if source.InspectionID != "" {
		inspection, exists := m.inspections[source.InspectionID]
		if !exists || inspection.TenantID != source.TenantID || !inspection.ExpiresAt.After(source.UpdatedAt) {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "source inspection is missing or expired")
		}
	}
	if expected := expectedObservationSet(evidence); expected != nil {
		currentSet, overflow := m.currentObservationSetLocked(source.TenantID, source.ID, MaxSynchronizationEntries)
		if overflow {
			return Source{}, catalog.E(catalog.CodeWorkLimitExceeded, "current source observations exceed the supported update limit")
		}
		if currentSet != *expected {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "source observations changed after the consequence preview")
		}
	}
	previousObservations := cloneMap(m.observations)
	if source.PresentationGeneration != 0 || NextPresentationGeneration(current, source) != EffectivePresentationGeneration(current) {
		source.PresentationGeneration = NextPresentationGeneration(current, source)
	}
	stored := source
	stored.InspectionID = ""
	if m.presentationGenerations == nil {
		m.presentationGenerations = map[string]map[int64]int64{}
	}
	if m.presentationGenerations[source.ID] == nil {
		m.presentationGenerations[source.ID] = map[int64]int64{}
	}
	m.presentationGenerations[source.ID][source.Revision] = EffectivePresentationGeneration(source)
	m.sources[source.ID] = stored
	if source.Status == SourceRemoved {
		for key, observation := range m.observations {
			if observation.TenantID == source.TenantID && observation.SourceID == source.ID {
				observation.Freshness = FreshSourceRemoved
				m.observations[key] = observation
			}
		}
	}
	if err := m.appendEvidence(ctx, source.TenantID, source.ID, evidence, audit.ActionSourceRevisionActivated, audit.ActionSourceTested, audit.ActionSourcePaused, audit.ActionSourceResumed, audit.ActionSourceRemoved); err != nil {
		m.sources[source.ID] = current
		m.observations = previousObservations
		return Source{}, err
	}
	return source, nil
}

// GetSource returns source state only within the explicit tenant.
func (m *MemoryStore) GetSource(_ context.Context, tenantID, idValue string) (Source, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	source, ok := m.sources[idValue]
	if !ok || source.TenantID != tenantID || source.Status == SourceRemoved {
		return Source{}, catalog.E(catalog.CodeNotFound, "federation source was not found")
	}
	return source, nil
}

// SourceEndpointConnected reports whether another nonremoved tenant source
// already uses the same nonempty Registry Search endpoint.
func (m *MemoryStore) SourceEndpointConnected(ctx context.Context, tenantID, exceptSourceID, endpoint string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, source := range m.sources {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if source.TenantID == tenantID && source.ID != exceptSourceID && source.Status != SourceRemoved && source.RegistrySearchURL != "" && sameEndpoint(source.RegistrySearchURL, endpoint) {
			return true, nil
		}
	}
	return false, nil
}

// ListSourcePage returns bounded nonremoved tenant sources in ascending
// display-name/ID order after the supplied cursor boundary.
func (m *MemoryStore) ListSourcePage(_ context.Context, tenantID string, request SourceListRequest) ([]Source, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Source, 0)
	for _, source := range m.sources {
		after := request.AfterDisplayName == "" || source.DisplayName > request.AfterDisplayName || source.DisplayName == request.AfterDisplayName && source.ID > request.AfterID
		if source.TenantID == tenantID && source.Status != SourceRemoved && after &&
			(request.DisplayName == "" || source.DisplayName == request.DisplayName) &&
			(request.Status == "" || source.Status == request.Status) {
			items = append(items, source)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].DisplayName != items[j].DisplayName {
			return items[i].DisplayName < items[j].DisplayName
		}
		return items[i].ID < items[j].ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items, nil
}

// ListDiscoveryLiveSources returns bounded sources approved for live
// participation at the requested host, audience and policy.
func (m *MemoryStore) ListDiscoveryLiveSources(ctx context.Context, tenantID, host string, audience DiscoveryAudience, policyDigest string, limit int) ([]Source, bool, error) {
	return m.listDiscoverySources(ctx, tenantID, host, audience, policyDigest, limit, false)
}

// ListDiscoveryReferralSources returns bounded sources approved for referral
// disclosure at the requested host, audience and policy.
func (m *MemoryStore) ListDiscoveryReferralSources(ctx context.Context, tenantID, host string, audience DiscoveryAudience, policyDigest string, limit int) ([]Source, bool, error) {
	return m.listDiscoverySources(ctx, tenantID, host, audience, policyDigest, limit, true)
}

// listDiscoverySources applies tenant, host, audience and policy eligibility
// before selecting bounded live or referral participation.
func (m *MemoryStore) listDiscoverySources(ctx context.Context, tenantID, host string, audience DiscoveryAudience, policyDigest string, limit int, referrals bool) ([]Source, bool, error) {
	if limit < 1 {
		return nil, false, catalog.E(catalog.CodeInvalidArgument, "discovery source limit must be positive")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Source, 0, limit+1)
	for _, source := range m.sources {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		participates := source.Live.Enabled && !source.Live.ReferralOnly
		if referrals {
			participates = source.Referrals.Enabled
		}
		liveRetrievalEligible := source.RetrievalMode == RetrievalLive || source.RetrievalMode == RetrievalHybrid
		if source.TenantID != tenantID || source.Status != SourceActive || !referrals && !liveRetrievalEligible || source.RegistrySearchURL == "" || !participates || !sourceEligibleForDiscovery(source, host, audience) {
			continue
		}
		items = append(items, source)
		sort.Slice(items, func(i, j int) bool { return liveSourceSelectionLess(items[i], items[j], policyDigest) })
		if len(items) > limit+1 {
			items = items[:limit+1]
		}
	}
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	return items, truncated, nil
}

// CurrentObservationSet returns a deterministic current-membership digest and
// reports overflow before accepting a partial set.
func (m *MemoryStore) CurrentObservationSet(ctx context.Context, tenantID, sourceID string, limit int) (ObservationSet, bool, error) {
	if limit < 1 {
		return ObservationSet{}, false, catalog.E(catalog.CodeInvalidArgument, "current observation-set limit must be positive")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return ObservationSet{}, false, err
	}
	result, overflow := m.currentObservationSetLocked(tenantID, sourceID, limit)
	return result, overflow, nil
}

// currentObservationSetLocked hashes sorted current observation IDs and entry
// digests, reporting count overflow while the caller holds the lock.
func (m *MemoryStore) currentObservationSetLocked(tenantID, sourceID string, limit int) (ObservationSet, bool) {
	members := make([]string, 0, limit+1)
	for _, observation := range m.observations {
		if observation.TenantID == tenantID && observation.SourceID == sourceID && observation.Freshness == FreshCurrent {
			members = append(members, observation.ID+":"+observation.ExactEntryDigest)
			if len(members) > limit {
				return ObservationSet{}, true
			}
		}
	}
	sort.Strings(members)
	return ObservationSet{Count: len(members), Digest: audit.DigestBytes([]byte(strings.Join(members, "\n")))}, false
}

// GetRetrievalState returns copied conditional-retrieval bytes for the exact
// tenant, source revision and request key.
func (m *MemoryStore) GetRetrievalState(_ context.Context, tenantID, sourceID string, sourceRevision int64, requestKey string) (RetrievalState, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.retrievals[retrievalStateKey(tenantID, sourceID, sourceRevision, requestKey)]
	if !ok {
		return RetrievalState{}, false, nil
	}
	state.Body = append([]byte(nil), state.Body...)
	state.VaryNames = append([]string(nil), state.VaryNames...)
	return state, true, nil
}

// CommitSync mirrors the PostgreSQL atomic boundary for deterministic unit and
// in-process tests, including rollback of evidence-sink failures.
func (m *MemoryStore) CommitSync(ctx context.Context, snapshot Snapshot, observations []Observation, source Source, expectedSourceRevision int64, evidence ...MutationEvidence) (Snapshot, Source, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Snapshot
		var admitted1 Source
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, admitted1, operationErr = m.CommitSync(ctx, snapshot, observations, source, expectedSourceRevision, evidence...)
			return operationErr
		})
		return admitted0, admitted1, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.sources[snapshot.SourceID]
	if !ok || current.TenantID != snapshot.TenantID || source.ID != current.ID || source.TenantID != current.TenantID {
		return Snapshot{}, Source{}, catalog.E(catalog.CodeNotFound, "federation source was not found")
	}
	if current.Revision != expectedSourceRevision || source.Revision != expectedSourceRevision || snapshot.SourceRevision != expectedSourceRevision {
		return Snapshot{}, Source{}, catalog.E(catalog.CodePreconditionFailed, "source changed while the sync was running")
	}
	syncStatus := source.Status
	syncSnapshotID := source.LastCompleteSnapshotID
	syncAt := source.LastSyncAt
	syncError := source.LastError
	syncUpdatedAt := source.UpdatedAt
	source = current
	source.Status = syncStatus
	source.LastCompleteSnapshotID = syncSnapshotID
	source.LastSyncAt = syncAt
	source.LastError = syncError
	source.UpdatedAt = syncUpdatedAt
	if snapshot.ReconcileCurrentView && (current.Type != SourceRegistry || current.RegistryListURL == "" || current.RetrievalMode != RetrievalIndexed && current.RetrievalMode != RetrievalHybrid) {
		return Snapshot{}, Source{}, catalog.E(catalog.CodeInvalidArgument, "current-view reconciliation requires an inspected Registry List source")
	}
	if err := ValidateSnapshotCommit(snapshot, observations); err != nil {
		return Snapshot{}, Source{}, err
	}
	if _, exists := m.snapshots[snapshot.ID]; exists {
		return Snapshot{}, Source{}, catalog.E(catalog.CodeConflict, "source snapshot already exists")
	}
	previousSources := cloneMap(m.sources)
	previousSnapshots := cloneMap(m.snapshots)
	previousObservations := cloneMap(m.observations)
	previousOccurrences := cloneMap(m.occurrences)
	previousRetrievals := cloneMap(m.retrievals)
	snapshot.Withdrawn = 0
	incoming := make(map[string]map[string]struct{}, len(observations))
	incomingExact := make(map[string]map[string]struct{}, len(observations))
	for _, observation := range observations {
		if incoming[observation.SourceKey] == nil {
			incoming[observation.SourceKey] = make(map[string]struct{})
		}
		incoming[observation.SourceKey][observation.IdentityDigest] = struct{}{}
		if incomingExact[observation.SourceKey] == nil {
			incomingExact[observation.SourceKey] = make(map[string]struct{})
		}
		incomingExact[observation.SourceKey][observation.ExactEntryDigest] = struct{}{}
	}
	if snapshot.WithdrawalAuthority || snapshot.ReconcileCurrentView {
		for key, observation := range m.observations {
			if observation.TenantID != snapshot.TenantID || observation.SourceID != snapshot.SourceID || observation.Freshness != FreshCurrent {
				continue
			}
			if _, remainsCurrent := incoming[observation.SourceKey][observation.IdentityDigest]; remainsCurrent {
				continue
			}
			_, sourceKeyPresent := incoming[observation.SourceKey]
			_, sameExact := incomingExact[observation.SourceKey][observation.ExactEntryDigest]
			if snapshot.WithdrawalAuthority && (!sourceKeyPresent || sameExact) {
				observation.Freshness = FreshWithdrawn
				snapshot.Withdrawn++
			} else {
				observation.Freshness = FreshStale
			}
			m.observations[key] = observation
		}
	}
	m.snapshots[snapshot.ID] = snapshot
	for _, observation := range observations {
		if observation.Freshness == FreshCurrent {
			for key, existing := range m.observations {
				_, remainsCurrent := incoming[observation.SourceKey][existing.IdentityDigest]
				if existing.TenantID == observation.TenantID && existing.SourceID == observation.SourceID && existing.SourceKey == observation.SourceKey && !remainsCurrent && existing.Freshness == FreshCurrent {
					existing.Freshness = FreshStale
					m.observations[key] = existing
				}
			}
		}
		key := observationKey(observation)
		occurrence := observation.Occurrence
		if existing, ok := m.observations[key]; ok {
			existing.RatingsMetadata = append([]byte(nil), observation.RatingsMetadata...)
			existing.ArtworkMetadata = append([]byte(nil), observation.ArtworkMetadata...)
			existing.LastSeenAt = maxTime(existing.LastSeenAt, observation.LastSeenAt)
			if observation.Freshness == FreshCurrent {
				existing.Freshness = FreshCurrent
			}
			observation = existing
		}
		occurrence.ObservationID = observation.ID
		observation.Occurrence = ObservationOccurrence{}
		observation.controlFilters = ProjectObservationControlFilters(observation.NormalizedEntry)
		m.observations[key] = observation
		m.occurrences[occurrenceKey(snapshot.TenantID, occurrence)] = occurrence
	}
	for _, requestKey := range snapshot.RetrievalInvalidations {
		delete(m.retrievals, retrievalStateKey(snapshot.TenantID, snapshot.SourceID, snapshot.SourceRevision, requestKey))
	}
	for _, state := range snapshot.RetrievalStates {
		state.Body = append([]byte(nil), state.Body...)
		state.VaryNames = append([]string(nil), state.VaryNames...)
		m.retrievals[retrievalStateKey(state.TenantID, state.SourceID, state.SourceRevision, state.RequestKey)] = state
	}
	m.sources[source.ID] = source
	if len(evidence) == 1 && evidence[0].Audit.ID != "" {
		finalized, err := FinalizeSyncEvidence(evidence[0], snapshot)
		if err != nil {
			m.sources = previousSources
			m.snapshots = previousSnapshots
			m.observations = previousObservations
			m.occurrences = previousOccurrences
			m.retrievals = previousRetrievals
			return Snapshot{}, Source{}, err
		}
		evidence[0] = finalized
	}
	if err := m.appendEvidence(ctx, snapshot.TenantID, snapshot.ID, evidence, audit.ActionSourceSyncCompleted, audit.ActionSourceSyncFailed); err != nil {
		m.sources = previousSources
		m.snapshots = previousSnapshots
		m.observations = previousObservations
		m.occurrences = previousOccurrences
		m.retrievals = previousRetrievals
		return Snapshot{}, Source{}, err
	}
	return snapshot, source, nil
}

// expectedObservationSet extracts the supplied observation-membership fence
// from transition evidence.
func expectedObservationSet(evidence []MutationEvidence) *ObservationSet {
	if len(evidence) != 1 {
		return nil
	}
	return evidence[0].ExpectedObservationSet
}

// appendEvidence validates exact tenant/target/action bindings before appending
// federation audit and notification evidence.
func (m *MemoryStore) appendEvidence(ctx context.Context, tenantID, targetID string, evidence []MutationEvidence, allowedActions ...string) error {
	if err := ValidateMutationEvidence(tenantID, targetID, evidence, allowedActions...); err != nil {
		return err
	}
	if len(evidence) == 0 {
		return nil
	}
	if evidence[0].Audit.ID == "" {
		return nil
	}
	if m.evidence == nil {
		return catalog.E(catalog.CodeInternal, "federation evidence sink is unavailable")
	}
	return m.evidence.AppendEvidence(ctx, evidence[0].Audit, evidence[0].Outbox)
}

// ValidateMutationEvidence binds an atomic federation evidence bundle to the
// exact tenant and object changed by its store operation.
func ValidateMutationEvidence(tenantID, targetID string, evidence []MutationEvidence, allowedActions ...string) error {
	if len(evidence) == 0 {
		return nil
	}
	if len(evidence) != 1 {
		return catalog.E(catalog.CodeInternal, "federation mutation has ambiguous evidence")
	}
	value := evidence[0]
	if value.Audit.ID == "" {
		if value.Outbox != nil {
			return catalog.E(catalog.CodeInternal, "federation notification is missing its audit evidence")
		}
		return nil
	}
	if value.Audit.TenantID != tenantID || value.Audit.TargetID != targetID {
		return catalog.E(catalog.CodeInternal, "federation audit evidence does not match its mutation")
	}
	if len(allowedActions) > 0 && !containsAuditAction(allowedActions, value.Audit.Action) {
		return catalog.E(catalog.CodeInternal, "federation audit action does not match its store operation")
	}
	if err := audit.ValidateNewRecord(value.Audit); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "federation audit evidence is invalid", err)
	}
	publicAction := federationNotificationAction(value.Audit.Action)
	expectedType := federationNotificationType(value.Audit.Action)
	switch {
	case expectedType == "" && !federationAuditOnlyMutation(value.Audit.Action):
		return catalog.E(catalog.CodeInternal, "federation mutation has an unexpected audit action")
	case expectedType == "" && value.Outbox != nil:
		return catalog.E(catalog.CodeInternal, "audit-only federation evidence has a notification")
	case expectedType != "" && value.Outbox == nil:
		return catalog.E(catalog.CodeInternal, "federation mutation is missing its required notification")
	case expectedType != "" && value.Outbox.Type != expectedType:
		return catalog.E(catalog.CodeInternal, "federation notification type does not match its audit action")
	}
	if value.Outbox != nil {
		if value.Outbox.ID == "" || value.Outbox.Internal || !value.Outbox.CreatedAt.Equal(value.Audit.At) || value.Outbox.PublishedAt != nil && !value.Outbox.PublishedAt.Equal(value.Audit.At) || value.Outbox.LeaseOwner != "" || value.Outbox.LeaseExpiresAt != nil {
			return catalog.E(catalog.CodeInternal, "federation notification metadata does not match its audit evidence")
		}
		if err := events.ValidateReferenceData(value.Outbox.Data, publicAction, value.Audit.TargetType, targetID, value.Audit.AfterDigest, ""); err != nil {
			return catalog.Wrap(catalog.CodeInternal, "federation notification data does not match its audit evidence", err)
		}
	}
	if value.Outbox != nil {
		if value.Outbox.TenantID != tenantID || value.Outbox.Subject != targetID {
			return catalog.E(catalog.CodeInternal, "federation notification does not match its mutation")
		}
	}
	return nil
}

// containsAuditAction reports exact action membership in the allowed mutation
// vocabulary.
func containsAuditAction(actions []string, action string) bool {
	for _, candidate := range actions {
		if action == candidate {
			return true
		}
	}
	return false
}

// federationAuditOnlyMutation identifies transitions whose evidence must not
// manufacture a public notification.
func federationAuditOnlyMutation(action string) bool {
	return action == audit.ActionSourceArtworkRevised || action == audit.ActionSourceInspected || action == audit.ActionSourceChangePreviewCreated
}

// federationNotificationType maps an audited source transition to its public
// event type.
func federationNotificationType(action string) string {
	publicAction := federationNotificationAction(action)
	if publicAction == "" {
		return ""
	}
	return catalog.CanonicalEventType(publicAction)
}

// federationNotificationAction selects the public notification action
// corresponding to a source audit action.
func federationNotificationAction(action string) string {
	publicAction := action
	switch action {
	case audit.ActionSourceTested:
		publicAction = "source.revision.activated"
	case audit.ActionSourceConnected, audit.ActionSourceRevisionActivated, audit.ActionSourcePaused, audit.ActionSourceResumed, audit.ActionSourceRemoved, audit.ActionSourceSyncCompleted, audit.ActionSourceSyncFailed:
	default:
		return ""
	}
	return publicAction
}

// observationKey binds tenant, source and source-local observation identity in
// the in-memory index.
func observationKey(observation Observation) string {
	return observation.TenantID + "/" + observation.SourceID + "/" + observation.SourceKey + "/" + observation.IdentityDigest
}

// retrievalStateKey separates conditional responses by tenant, source revision
// and request identity.
func retrievalStateKey(tenantID, sourceID string, sourceRevision int64, requestKey string) string {
	return tenantID + "/" + sourceID + "/" + strconv.FormatInt(sourceRevision, 10) + "/" + requestKey
}

// occurrenceKey binds tenant, snapshot, retrieval digest and entry location for
// exact provenance lookup.
func occurrenceKey(tenantID string, occurrence ObservationOccurrence) string {
	return tenantID + "/" + occurrence.SnapshotID + "/" + occurrence.RetrievalDigest + "/" + occurrence.EntryLocation
}

// maxTime returns the later timestamp when reconciling retained first/last-seen
// state.
func maxTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}

// cloneMap copies a map's entries for rollback bookkeeping without deep-copying
// its values.
func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	result := make(map[K]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

// GetSnapshot resolves an immutable sync snapshot for the explicit tenant.
func (m *MemoryStore) GetSnapshot(_ context.Context, tenantID, idValue string) (Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snapshot, ok := m.snapshots[idValue]
	if !ok || snapshot.TenantID != tenantID {
		return Snapshot{}, catalog.E(catalog.CodeNotFound, "source snapshot was not found")
	}
	return snapshot, nil
}

// ListSnapshots returns newest-first tenant snapshots, optionally restricted to
// a source; a positive limit bounds the result.
func (m *MemoryStore) ListSnapshots(_ context.Context, tenantID, sourceID string, limit int) ([]Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Snapshot, 0)
	for _, snapshot := range m.snapshots {
		if snapshot.TenantID == tenantID && (sourceID == "" || snapshot.SourceID == sourceID) {
			items = append(items, snapshot)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].StartedAt.Equal(items[j].StartedAt) {
			return items[i].StartedAt.After(items[j].StartedAt)
		}
		return items[i].ID > items[j].ID
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// ListSnapshotPage returns a bounded source snapshot page ordered by descending
// start time and ID.
func (m *MemoryStore) ListSnapshotPage(_ context.Context, tenantID string, request SnapshotListRequest) ([]Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Snapshot, 0)
	for _, snapshot := range m.snapshots {
		before := request.AfterStartedAt == nil || snapshot.StartedAt.Before(*request.AfterStartedAt) || snapshot.StartedAt.Equal(*request.AfterStartedAt) && snapshot.ID < request.AfterID
		if snapshot.TenantID == tenantID && snapshot.SourceID == request.SourceID && before {
			items = append(items, snapshot)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].StartedAt.Equal(items[j].StartedAt) {
			return items[i].StartedAt.After(items[j].StartedAt)
		}
		return items[i].ID > items[j].ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items, nil
}

// ListCurrentObservations returns the complete current source set only when it
// fits limit, otherwise reporting overflow.
func (m *MemoryStore) ListCurrentObservations(ctx context.Context, tenantID, sourceID string, limit int) ([]Observation, bool, error) {
	if limit < 1 {
		return nil, false, catalog.E(catalog.CodeInvalidArgument, "current observation limit must be positive")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Observation, 0, limit+1)
	for _, observation := range m.observations {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if observation.TenantID == tenantID && observation.SourceID == sourceID && observation.Freshness == FreshCurrent {
			items = append(items, observation)
			if len(items) > limit {
				return nil, true, nil
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, false, nil
}

// ListObservationPage filters tenant observations and returns the requested
// stable continuation page.
func (m *MemoryStore) ListObservationPage(_ context.Context, tenantID string, request ObservationListRequest) ([]Observation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Observation, 0)
	for _, observation := range m.observations {
		if observation.TenantID != tenantID || request.SourceID != "" && observation.SourceID != request.SourceID || request.CurrentOnly && observation.Freshness != FreshCurrent || !observationAfterCursor(observation, request) {
			continue
		}
		items = append(items, observation)
	}
	sortObservationPage(items)
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	result := make([]Observation, len(items))
	for i, item := range items {
		result[i] = observationMetadata(item)
	}
	return result, nil
}

// GetObservation resolves an external observation only within the explicit
// tenant.
func (m *MemoryStore) GetObservation(_ context.Context, tenantID, observationID string) (Observation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, observation := range m.observations {
		if observation.TenantID == tenantID && observation.ID == observationID {
			return observation, nil
		}
	}
	return Observation{}, catalog.E(catalog.CodeNotFound, "source observation was not found")
}

// ObservationHasSnapshotEvidence checks retained occurrence authority without
// relying on an observation's immutable first snapshot or loading documents.
func (m *MemoryStore) ObservationHasSnapshotEvidence(_ context.Context, tenantID, observationID, sourceID, snapshotID string, revision int64, exactDigest string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for key, occurrence := range m.occurrences {
		if strings.HasPrefix(key, tenantID+"/") && occurrence.ObservationID == observationID && occurrence.SourceID == sourceID && occurrence.SnapshotID == snapshotID && occurrence.SourceRevision == revision && occurrence.ExactEntryDigest == exactDigest && occurrence.EvidenceComplete {
			return true, nil
		}
	}
	return false, nil
}

// GetObservationMetadata checks the tenant parent without returning documents.
// Exact reads, adoption and synchronization keep using GetObservation.
func (m *MemoryStore) GetObservationMetadata(ctx context.Context, tenantID, observationID string) (Observation, error) {
	item, err := m.GetObservation(ctx, tenantID, observationID)
	if err != nil {
		return Observation{}, err
	}
	return observationMetadata(item), nil
}

// observationMetadata copies the metadata projection independently of retained
// document buffers and mutable metadata slices.
func observationMetadata(item Observation) Observation {
	item.RawEntryAvailable = len(item.RawEntry) > 0
	item.RawEntry, item.NormalizedEntry = nil, nil
	item.Occurrence = ObservationOccurrence{}
	item.controlFilters = ObservationControlFilters{}
	item.Compatibility.Findings = slices.Clone(item.Compatibility.Findings)
	item.SearchTags = slices.Clone(item.SearchTags)
	item.SearchQueries = slices.Clone(item.SearchQueries)
	return item
}

// ListObservationGroupMembers selects bounded current members sharing identity
// and version-presence semantics.
func (m *MemoryStore) ListObservationGroupMembers(_ context.Context, tenantID, identifier, version string, versionPresent bool, limit int) ([]Observation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Observation, 0)
	for _, observation := range m.observations {
		if observation.TenantID == tenantID && observation.Identifier == identifier && observation.VersionPresent == versionPresent && observation.Version == version && observation.NormalizedEntryDigest != "" && observation.Freshness == FreshCurrent {
			items = append(items, observation)
		}
	}
	sortObservations(items)
	if len(items) > limit {
		items = items[:limit]
	}
	result := make([]Observation, len(items))
	for i, item := range items {
		result[i] = observationMetadata(item)
	}
	return result, nil
}

// ObservationGroupConflicts compares current normalized digests and kinds and
// reports per-source content conflicts.
func (m *MemoryStore) ObservationGroupConflicts(_ context.Context, tenantID, identifier, version string, versionPresent bool, sourceIDs []string) (bool, map[string]bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	digests := make(map[string]struct{})
	kinds := make(map[catalog.Kind]struct{})
	selected := make(map[string]struct{}, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		selected[sourceID] = struct{}{}
	}
	bySource := make(map[string]map[string]struct{}, len(sourceIDs))
	for _, observation := range m.observations {
		if observation.TenantID != tenantID || observation.Identifier != identifier || observation.VersionPresent != versionPresent || observation.Version != version || observation.NormalizedEntryDigest == "" || observation.Freshness != FreshCurrent {
			continue
		}
		digests[observation.NormalizedEntryDigest] = struct{}{}
		kinds[observation.Kind] = struct{}{}
		if _, ok := selected[observation.SourceID]; ok {
			if bySource[observation.SourceID] == nil {
				bySource[observation.SourceID] = make(map[string]struct{})
			}
			bySource[observation.SourceID][observation.NormalizedEntryDigest] = struct{}{}
		}
	}
	sourceConflicts := make(map[string]bool, len(bySource))
	for sourceID, values := range bySource {
		sourceConflicts[sourceID] = len(values) > 1
	}
	return len(digests) > 1 || len(kinds) > 1, sourceConflicts, nil
}

// SearchObservationPage searches current normalized observations with stable
// score/identifier/ID continuation.
func (m *MemoryStore) SearchObservationPage(_ context.Context, tenantID string, request ObservationSearchRequest) ([]SearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]SearchResult, 0)
	for _, observation := range m.observations {
		source, ok := m.sources[observation.SourceID]
		if !ok || source.TenantID != tenantID || source.Status != SourceActive && source.Status != SourceError || source.RetrievalMode != RetrievalIndexed && source.RetrievalMode != RetrievalHybrid || !scopeMatches(source.Scope, sourceEntry{Identifier: observation.Identifier, Version: observation.Version, Type: observation.MediaType, Tags: observation.SearchTags}) {
			continue
		}
		if observation.TenantID != tenantID || observation.Freshness != FreshCurrent || observation.NormalizedEntryDigest == "" || len(request.Query.Kinds) > 0 && !slices.Contains(request.Query.Kinds, observation.Kind) || len(request.Query.Tags) > 0 && !hasAll(observation.SearchTags, request.Query.Tags) {
			continue
		}
		resultScore := 100
		if request.Query.Text != "" {
			document := catalog.SearchDocument{Identifier: observation.Identifier, DisplayName: observation.SearchDisplayName, Description: observation.SearchDescription, Tags: observation.SearchTags, RepresentativeQueries: observation.SearchQueries}
			if catalog.CoreLexicalScore(document, request.Query.Text) == 0 {
				continue
			}
			resultScore = 80
		}
		after := request.AfterID == "" || resultScore < request.AfterScore || resultScore == request.AfterScore && (observation.Identifier > request.AfterIdentifier || observation.Identifier == request.AfterIdentifier && observation.ID > request.AfterID)
		if after {
			items = append(items, SearchResult{Observation: observation, Score: resultScore, Source: observation.SourceID})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		if items[i].Observation.Identifier != items[j].Observation.Identifier {
			return items[i].Observation.Identifier < items[j].Observation.Identifier
		}
		return items[i].Observation.ID < items[j].Observation.ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	result := make([]SearchResult, len(items))
	for i, item := range items {
		item.Observation = observationMetadata(item.Observation)
		result[i] = item
	}
	return result, nil
}

// SearchIndexedCatalog mirrors the database search contract: collapse identical
// observations, retain conflicts, then apply the stable score/identity order.
func (m *MemoryStore) SearchIndexedCatalog(ctx context.Context, tenantID string, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
	if err := identity.RequireCollectionTenant(ctx, tenantID); err != nil {
		return catalog.FederatedCatalogSearchPage{}, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (catalog.FederatedCatalogSearchPage, error) {
			return m.SearchIndexedCatalog(ctx, tenantID, request)
		})
	}
	allowed, err := identity.CollectionAllows(ctx, "federation.discover", "")
	if err != nil {
		return catalog.FederatedCatalogSearchPage{}, err
	}
	if !allowed {
		return catalog.FederatedCatalogSearchPage{}, catalog.E(catalog.CodeForbidden, "federation discovery permission is required")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	generation := m.catalogSearchGenerationLocked(tenantID)
	if request.ExpectedGeneration != "" && request.ExpectedGeneration != generation {
		return catalog.FederatedCatalogSearchPage{}, catalog.E(catalog.CodeInvalidArgument, "catalog cursor is stale")
	}
	if !request.Query.IncludesOrigin(catalog.SearchResultIndexed) || request.Query.Visibility != "" && request.Query.Visibility != catalog.VisibilityTenant {
		return catalog.FederatedCatalogSearchPage{Indexed: []catalog.CatalogSearchResult{}, Generation: generation}, nil
	}
	type identityKey struct {
		identifier     string
		version        string
		versionPresent bool
	}
	type memberKey struct {
		identityKey
		digest string
	}
	eligible := make([]Observation, 0)
	identityDigests := make(map[identityKey]map[string]struct{})
	sourceDigests := make(map[identityKey]map[string]map[string]struct{})
	for _, observation := range m.observations {
		source, ok := m.sources[observation.SourceID]
		if exact := request.ExactIdentity; exact != nil && (observation.Identifier != exact.Identifier || observation.Version != exact.Version || observation.VersionPresent != exact.VersionPresent) {
			continue
		}
		if !ok || !indexedObservationEligible(tenantID, source, observation) {
			continue
		}

		if request.Query.IncludesOrigin(catalog.SearchResultLocal) && m.localCatalog != nil && observation.VersionPresent {
			shadowed, err := m.localCatalog.HasPublishedIdentity(ctx, tenantID, observation.Identifier, observation.Version)
			if err != nil {
				return catalog.FederatedCatalogSearchPage{}, err
			}
			if shadowed {
				continue
			}
		}
		eligible = append(eligible, observation)
		identity := identityKey{identifier: observation.Identifier, version: observation.Version, versionPresent: observation.VersionPresent}
		if identityDigests[identity] == nil {
			identityDigests[identity] = make(map[string]struct{})
		}
		identityDigests[identity][observation.NormalizedEntryDigest] = struct{}{}
		if sourceDigests[identity] == nil {
			sourceDigests[identity] = make(map[string]map[string]struct{})
		}
		if sourceDigests[identity][observation.SourceID] == nil {
			sourceDigests[identity][observation.SourceID] = make(map[string]struct{})
		}
		sourceDigests[identity][observation.SourceID][observation.NormalizedEntryDigest] = struct{}{}
	}
	members := make(map[memberKey][]Observation)
	for _, observation := range eligible {
		if !observation.controlFilters.matches(request.Query) {
			continue
		}
		if len(request.Query.Kinds) > 0 && !containsKind(request.Query.Kinds, observation.Kind) || len(request.Query.Tags) > 0 && !containsAllStrings(observation.SearchTags, request.Query.Tags) || request.Query.Publisher != "" && request.Query.Publisher != observation.SearchPublisher {
			continue
		}
		score := catalog.CoreLexicalScore(catalog.SearchDocument{Identifier: observation.Identifier, DisplayName: observation.SearchDisplayName, Description: observation.SearchDescription, Tags: observation.SearchTags, RepresentativeQueries: observation.SearchQueries}, request.Query.Text)
		if request.Query.Text != "" && score == 0 {
			continue
		}
		identity := identityKey{identifier: observation.Identifier, version: observation.Version, versionPresent: observation.VersionPresent}
		key := memberKey{identityKey: identity, digest: observation.NormalizedEntryDigest}
		members[key] = append(members[key], observation)
	}
	items := make([]catalog.CatalogSearchResult, 0, len(members))
	for key, observations := range members {
		observation := observations[0]
		sourceSet := make(map[string]struct{}, len(observations))
		sameSourceConflict := false
		for _, value := range observations {
			sourceSet[value.SourceID] = struct{}{}
			if len(sourceDigests[key.identityKey][value.SourceID]) > 1 {
				sameSourceConflict = true
			}
		}
		sourceIDs := make([]string, 0, len(sourceSet))
		for sourceID := range sourceSet {
			sourceIDs = append(sourceIDs, sourceID)
		}
		sort.Strings(sourceIDs)
		sourceCount := len(sourceIDs)
		if len(sourceIDs) > MaxCatalogSearchAttributions {
			sourceIDs = sourceIDs[:MaxCatalogSearchAttributions]
		}
		score := catalog.CoreLexicalScore(catalog.SearchDocument{Identifier: observation.Identifier, DisplayName: observation.SearchDisplayName, Description: observation.SearchDescription, Tags: observation.SearchTags, RepresentativeQueries: observation.SearchQueries}, request.Query.Text)
		groupID, err := ObservationGroupID(observation.Identifier, observation.Version, observation.VersionPresent)
		if err != nil {
			return catalog.FederatedCatalogSearchPage{}, err
		}
		displayName := observation.SearchDisplayName
		if displayName == "" {
			displayName = observation.Identifier
		}
		items = append(items, catalog.CatalogSearchResult{
			ID: catalog.IndexedSearchResultID(observation.Identifier, observation.Version, observation.VersionPresent), Type: catalog.SearchResultIndexed,
			Identifier: observation.Identifier, Version: observation.Version, VersionPresent: observation.VersionPresent, Kind: observation.Kind, DisplayName: displayName, Description: observation.SearchDescription, Tags: append([]string{}, observation.SearchTags...), Publisher: observation.SearchPublisher, Score: score,
			Indexed: &catalog.IndexedSearchEvidence{PresentationSourceRevision: m.sources[sourceIDs[0]].Revision, PresentationSourceID: sourceIDs[0], PresentationGeneration: EffectivePresentationGeneration(m.sources[sourceIDs[0]]), ObservationGroupID: groupID, NormalizedEntryDigest: observation.NormalizedEntryDigest, SourceIDs: sourceIDs, SourceCount: sourceCount, SourcesTruncated: sourceCount > len(sourceIDs), Conflict: len(identityDigests[key.identityKey]) > 1, SameSourceConflict: sameSourceConflict},
		})
	}
	sort.Slice(items, func(i, j int) bool {
		left := catalog.CatalogSearchResultPosition(items[i])
		right := catalog.CatalogSearchResultPosition(items[j])
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if left.DisplayName != right.DisplayName {
			return left.DisplayName < right.DisplayName
		}
		return items[i].Indexed.NormalizedEntryDigest < items[j].Indexed.NormalizedEntryDigest
	})
	deduplicated := make([]catalog.CatalogSearchResult, 0, len(items))
	seenIdentities := make(map[identityKey]struct{}, len(items))
	for _, item := range items {
		identity := identityKey{identifier: item.Identifier, version: item.Version, versionPresent: item.VersionPresent}
		if _, exists := seenIdentities[identity]; exists {
			continue
		}
		seenIdentities[identity] = struct{}{}
		deduplicated = append(deduplicated, item)
	}
	items = deduplicated
	if request.IdentityProbes != nil {
		if len(request.IdentityProbes) > catalog.LiveSearchWindow {
			return catalog.FederatedCatalogSearchPage{}, catalog.E(catalog.CodeInvalidArgument, "identity probe exceeds its bound")
		}
		matches := []catalog.IndexedIdentity{}
		for _, probe := range request.IdentityProbes {
			for _, item := range items {
				if probe == (catalog.IndexedIdentity{Identifier: item.Identifier, Version: item.Version, VersionPresent: item.VersionPresent}) {
					matches = append(matches, probe)
					break
				}
			}
		}
		return catalog.FederatedCatalogSearchPage{Generation: generation, Matches: matches}, nil
	}
	sortCatalogSearchResults(items, request.Query.Order)
	total := len(items)
	if request.After != nil {
		items = catalogResultsAfterPosition(items, *request.After, request.Query.Order)
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 20
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return catalog.FederatedCatalogSearchPage{Indexed: items, IndexedTotal: total, Generation: generation, HasMore: hasMore}, nil
}

// ListCatalogLiveSources selects bounded tenant sources eligible for
// authenticated live Catalog search under the current policy.
func (m *MemoryStore) ListCatalogLiveSources(ctx context.Context, tenantID, policyDigest string, limit int) ([]Source, bool, error) {
	if limit < 1 {
		return nil, false, catalog.E(catalog.CodeInvalidArgument, "live source limit must be positive")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Source, 0, limit+1)
	for _, source := range m.sources {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if source.TenantID == tenantID && source.Status == SourceActive && (source.RetrievalMode == RetrievalLive || source.RetrievalMode == RetrievalHybrid) && source.Live.Enabled && !source.Live.ReferralOnly && source.RegistrySearchURL != "" {
			items = append(items, source)
			sort.Slice(items, func(i, j int) bool { return liveSourceSelectionLess(items[i], items[j], policyDigest) })
			if len(items) > limit+1 {
				items = items[:limit+1]
			}
		}
	}
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	return items, truncated, nil
}

// Current-policy sources sort before stale evidence so a bounded fan-out
// cannot be consumed entirely by diagnostics for obsolete policy revisions.
// Stale sources remain in the bounded tail when room exists so callers can
// still report their safe policy_stale diagnostic.
func liveSourceSelectionLess(left, right Source, policyDigest string) bool {
	leftCurrent := left.PolicyDigest == policyDigest
	rightCurrent := right.PolicyDigest == policyDigest
	if leftCurrent != rightCurrent {
		return leftCurrent
	}
	return left.ID < right.ID
}

// catalogSearchGenerationLocked fingerprints tenant source/observation state
// for indexed-search continuation.
func (m *MemoryStore) catalogSearchGenerationLocked(tenantID string) string {
	values := make([]string, 0)
	for _, source := range m.sources {
		if source.TenantID == tenantID {
			values = append(values, "source\x00"+source.ID+"\x00"+strconv.FormatInt(source.Revision, 10)+"\x00"+string(source.Status)+"\x00"+string(source.RetrievalMode))
		}
	}
	for _, observation := range m.observations {
		if observation.TenantID == tenantID {
			values = append(values, "observation\x00"+observation.ID+"\x00"+string(observation.Freshness)+"\x00"+observation.NormalizedEntryDigest+"\x00"+observation.LastSeenAt.UTC().Format(time.RFC3339Nano))
		}
	}
	sort.Strings(values)
	hash := sha256.New()
	_, _ = hash.Write([]byte("indexed-catalog-search-generation-v1\x00"))
	for _, value := range values {
		_, _ = hash.Write([]byte(value + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// containsKind reports whether a kind filter includes the candidate resource
// kind.
func containsKind(values []catalog.Kind, candidate catalog.Kind) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// containsAllStrings reports whether every requested exact value appears in the
// indexed projection.
func containsAllStrings(values, candidates []string) bool {
	for _, candidate := range candidates {
		if !containsString(values, candidate) {
			return false
		}
	}
	return true
}

// sortCatalogSearchResults applies the shared deterministic score/name/ID
// ordering to external results.
func sortCatalogSearchResults(items []catalog.CatalogSearchResult, order ...catalog.SearchOrder) {
	sort.Slice(items, func(i, j int) bool {
		return catalog.CatalogSearchPositionAfter(catalog.CatalogSearchResultPosition(items[j], order...), catalog.CatalogSearchResultPosition(items[i], order...), order...)
	})
}

// catalogSearchResultBefore compares two external results using the shared
// search position.
func catalogSearchResultBefore(leftResult, rightResult catalog.CatalogSearchResult) bool {
	left := catalog.CatalogSearchResultPosition(leftResult)
	right := catalog.CatalogSearchResultPosition(rightResult)
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.DisplayName != right.DisplayName {
		return left.DisplayName < right.DisplayName
	}
	return left.ResultID < right.ResultID
}

// catalogResultsAfterPosition keeps only results strictly after the signed
// continuation position.
func catalogResultsAfterPosition(items []catalog.CatalogSearchResult, after catalog.CatalogSearchPosition, order ...catalog.SearchOrder) []catalog.CatalogSearchResult {
	for index, item := range items {
		if catalog.CatalogSearchPositionAfter(catalog.CatalogSearchResultPosition(item, order...), after, order...) {
			return items[index:]
		}
	}
	return nil
}

// GetObservationOccurrence resolves the exact tenant
// observation/snapshot/retrieval/location provenance tuple.
func (m *MemoryStore) GetObservationOccurrence(_ context.Context, tenantID, observationID, snapshotID, retrievalDigest, entryLocation string) (ObservationOccurrence, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	occurrence, ok := m.occurrences[occurrenceKey(tenantID, ObservationOccurrence{SnapshotID: snapshotID, RetrievalDigest: retrievalDigest, EntryLocation: entryLocation})]
	if !ok || occurrence.ObservationID != observationID {
		return ObservationOccurrence{}, catalog.E(catalog.CodeNotFound, "observation occurrence was not found")
	}
	return occurrence, nil
}

// ListObservationOccurrences returns a bounded occurrence sample and the total
// retained count for the tenant observation.
func (m *MemoryStore) ListObservationOccurrences(_ context.Context, tenantID, observationID string, limit int) ([]ObservationOccurrence, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]ObservationOccurrence, 0)
	for key, occurrence := range m.occurrences {
		if strings.HasPrefix(key, tenantID+"/") && occurrence.ObservationID == observationID {
			items = append(items, occurrence)
		}
	}
	sortObservationOccurrences(items)
	total := len(items)
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, total, nil
}

// ListObservationOccurrencePage returns bounded retained occurrences after the
// supplied stable position.
func (m *MemoryStore) ListObservationOccurrencePage(_ context.Context, tenantID string, request OccurrenceListRequest) ([]ObservationOccurrence, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]ObservationOccurrence, 0)
	for key, occurrence := range m.occurrences {
		before := request.AfterObservedAt == nil || occurrence.ObservedAt.Before(*request.AfterObservedAt) || occurrence.ObservedAt.Equal(*request.AfterObservedAt) && (occurrence.SnapshotID < request.AfterSnapshotID || occurrence.SnapshotID == request.AfterSnapshotID && (occurrence.RetrievalDigest > request.AfterRetrievalDigest || occurrence.RetrievalDigest == request.AfterRetrievalDigest && occurrence.EntryLocation > request.AfterEntryLocation))
		if strings.HasPrefix(key, tenantID+"/") && occurrence.ObservationID == request.ObservationID && before {
			items = append(items, occurrence)
		}
	}
	sortObservationOccurrences(items)
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items, nil
}

// ObservationOccurrenceSummaries counts and selects latest evidence for the
// bounded group in one locked pass, without collecting entire histories.
func (m *MemoryStore) ObservationOccurrenceSummaries(_ context.Context, tenantID string, observationIDs []string) (map[string]OccurrenceSummary, error) {
	if len(observationIDs) > MaxObservationGroupContributions {
		return nil, catalog.E(catalog.CodeInvalidArgument, "observation summary selection exceeds the group bound")
	}
	result := make(map[string]OccurrenceSummary, len(observationIDs))
	for _, observationID := range observationIDs {
		result[observationID] = OccurrenceSummary{}
	}
	if len(result) == 0 {
		return result, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for key, occurrence := range m.occurrences {
		summary, selected := result[occurrence.ObservationID]
		if !selected || !strings.HasPrefix(key, tenantID+"/") {
			continue
		}
		if summary.Total == 0 || observationOccurrenceLess(occurrence, summary.Latest) {
			summary.Latest = occurrence
		}
		summary.Total++
		result[occurrence.ObservationID] = summary
	}
	for _, summary := range result {
		if summary.Total == 0 {
			return nil, catalog.E(catalog.CodeNotFound, "observation occurrence was not found")
		}
	}
	return result, nil
}

// observationAfterCursor compares identity, version presence, version and ID
// using observation-page ordering.
func observationAfterCursor(observation Observation, request ObservationListRequest) bool {
	if request.AfterID == "" {
		return true
	}
	if observation.Identifier != request.AfterIdentifier {
		return observation.Identifier > request.AfterIdentifier
	}
	if observation.VersionPresent != request.AfterVersionPresent {
		return !observation.VersionPresent && request.AfterVersionPresent
	}
	if observation.Version != request.AfterVersion {
		return observation.Version > request.AfterVersion
	}
	return observation.ID > request.AfterID
}

// sortObservationOccurrences orders provenance by newest observation time and
// stable snapshot/retrieval/location ties.
func sortObservationOccurrences(items []ObservationOccurrence) {
	sort.Slice(items, func(i, j int) bool { return observationOccurrenceLess(items[i], items[j]) })
}

// observationOccurrenceLess preserves the complete newest-first history order.
func observationOccurrenceLess(left, right ObservationOccurrence) bool {
	if !left.ObservedAt.Equal(right.ObservedAt) {
		return left.ObservedAt.After(right.ObservedAt)
	}
	if left.SnapshotID != right.SnapshotID {
		return left.SnapshotID > right.SnapshotID
	}
	if left.RetrievalDigest != right.RetrievalDigest {
		return left.RetrievalDigest < right.RetrievalDigest
	}
	return left.EntryLocation < right.EntryLocation
}

// sortObservationPage applies the stable identity/version-presence/version/ID
// ordering used by observation continuations.
func sortObservationPage(items []Observation) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Identifier != items[j].Identifier {
			return items[i].Identifier < items[j].Identifier
		}
		if items[i].VersionPresent != items[j].VersionPresent {
			return items[i].VersionPresent
		}
		if items[i].Version != items[j].Version {
			return items[i].Version < items[j].Version
		}
		return items[i].ID < items[j].ID
	})
}

// sortObservations orders source observations consistently for retained set
// processing.
func sortObservations(items []Observation) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Identifier != items[j].Identifier {
			return items[i].Identifier < items[j].Identifier
		}
		if items[i].VersionPresent != items[j].VersionPresent {
			return items[i].VersionPresent
		}
		if items[i].Version != items[j].Version {
			return items[i].Version < items[j].Version
		}
		if items[i].SourceID != items[j].SourceID {
			return items[i].SourceID < items[j].SourceID
		}
		if items[i].NormalizedEntryDigest != items[j].NormalizedEntryDigest {
			return items[i].NormalizedEntryDigest < items[j].NormalizedEntryDigest
		}
		if items[i].ExactEntryDigest != items[j].ExactEntryDigest {
			return items[i].ExactEntryDigest < items[j].ExactEntryDigest
		}
		return items[i].ID < items[j].ID
	})
}
