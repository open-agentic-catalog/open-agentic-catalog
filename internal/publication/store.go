// Publication persistence retains immutable candidates and fences
// active-pointer changes with expected state and atomic transition evidence.

package publication

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"sort"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
)

type EvidenceSink interface {
	AppendEvidence(context.Context, audit.Record, *catalog.OutboxEvent) error
}

// MutationEvidence binds a snapshot transition to its atomic audit/outbox facts.
type MutationEvidence struct {
	Audit  audit.Record
	Outbox *catalog.OutboxEvent
	// ExpectedActiveID fences activation/rollback against a concurrent pointer
	// change: nil skips the check, while a pointer to "" requires no active row.
	ExpectedActiveID *string
}

// Store persists immutable publication candidates and atomically moves the one
// active pointer together with its audit and notification evidence.
type Store interface {
	// NextGeneration proposes a tenant-and-host-scoped generation; it does not reserve it.
	// SaveCandidate must reject a generation already inserted by another writer.
	NextGeneration(context.Context, string, string) (int64, error)
	SaveCandidate(context.Context, Snapshot, ...MutationEvidence) (Snapshot, error)
	Activate(context.Context, string, string, time.Time, ...MutationEvidence) (Snapshot, error)
	Deactivate(context.Context, string, string, time.Time, ...MutationEvidence) (Snapshot, error)
	Rollback(context.Context, string, string, time.Time, ...MutationEvidence) (Snapshot, error)
	Get(context.Context, string, string) (Snapshot, error)
	Active(context.Context, string) (Snapshot, error)
	ListSummaries(context.Context, string, SnapshotListRequest) ([]SnapshotSummary, error)
}

// Deactivate retires the exact active tenant snapshot and restores prior state
// if transition evidence fails.
func (m *MemoryStore) Deactivate(ctx context.Context, tenantID, snapshotID string, at time.Time, evidence ...MutationEvidence) (Snapshot, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Snapshot
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.Deactivate(ctx, tenantID, snapshotID, at, evidence...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	idValue := m.active[tenantID]
	if idValue == "" || idValue != snapshotID {
		return Snapshot{}, catalog.E(catalog.CodeNotFound, "no active publication snapshot exists")
	}
	key := memorySnapshotKey(tenantID, idValue)
	snapshot := m.snapshots[key]
	snapshot.State = SnapshotRetired
	m.snapshots[key] = snapshot
	delete(m.active, tenantID)
	if err := m.appendEvidence(ctx, tenantID, snapshotID, evidence, audit.ActionPublicationUnpublished); err != nil {
		snapshot.State = SnapshotActive
		m.snapshots[key] = snapshot
		m.active[tenantID] = idValue
		return Snapshot{}, err
	}
	return cloneSnapshot(snapshot), nil
}

// Rollback restores a retired snapshot under the expected active pointer and
// rolls back both pointer changes if evidence fails.
func (m *MemoryStore) Rollback(ctx context.Context, tenantID, snapshotID string, at time.Time, evidence ...MutationEvidence) (Snapshot, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Snapshot
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.Rollback(ctx, tenantID, snapshotID, at, evidence...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	key := memorySnapshotKey(tenantID, snapshotID)
	snapshot, ok := m.snapshots[key]
	if !ok || snapshot.TenantID != tenantID {
		return Snapshot{}, catalog.E(catalog.CodeNotFound, "publication snapshot was not found")
	}
	if snapshot.State != SnapshotRetired {
		return Snapshot{}, catalog.E(catalog.CodeInvalidTransition, "only a retired snapshot can be restored")
	}
	targetBefore := cloneSnapshot(snapshot)
	priorID := m.active[tenantID]
	if err := validateExpectedActive(evidence, priorID); err != nil {
		return Snapshot{}, err
	}
	var prior Snapshot
	if priorID != "" {
		priorKey := memorySnapshotKey(tenantID, priorID)
		prior = m.snapshots[priorKey]
		prior.State = SnapshotRetired
		m.snapshots[priorKey] = prior
	}
	snapshot.State = SnapshotActive
	snapshot.ActivatedAt = &at
	m.snapshots[key] = snapshot
	m.active[tenantID] = snapshotID
	if err := m.appendEvidence(ctx, tenantID, snapshotID, evidence, audit.ActionPublicationRolledBack); err != nil {
		m.snapshots[key] = targetBefore
		if priorID == "" {
			delete(m.active, tenantID)
		} else {
			prior.State = SnapshotActive
			m.snapshots[memorySnapshotKey(tenantID, priorID)] = prior
			m.active[tenantID] = priorID
		}
		return Snapshot{}, err
	}
	return cloneSnapshot(snapshot), nil
}

type MemoryStore struct {
	mu          sync.RWMutex
	snapshots   map[string]Snapshot
	active      map[string]string
	generation  map[string]int64
	generations map[string]map[int64]struct{}
	evidence    EvidenceSink
}

// NewMemoryStore creates empty candidate and generation indexes with an
// optional single evidence sink.
func NewMemoryStore(evidence ...EvidenceSink) *MemoryStore {
	store := &MemoryStore{snapshots: make(map[string]Snapshot), active: make(map[string]string), generation: make(map[string]int64), generations: make(map[string]map[int64]struct{})}
	if len(evidence) == 1 {
		store.evidence = evidence[0]
	}
	return store
}

// NextGeneration proposes the next tenant/host generation without reserving it
// against concurrent candidate creation.
func (m *MemoryStore) NextGeneration(_ context.Context, tenantID, host string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.generation[tenantID+"\x00"+host] + 1, nil
}

// SaveCandidate enforces document bounds and unique tenant/host generation,
// retaining candidate evidence before success.
func (m *MemoryStore) SaveCandidate(ctx context.Context, snapshot Snapshot, evidence ...MutationEvidence) (Snapshot, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Snapshot
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.SaveCandidate(ctx, snapshot, evidence...)
			return operationErr
		})
		return admitted0, err
	}

	if err := ValidateSnapshotBounds(snapshot); err != nil {
		return Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	generationKey := snapshot.TenantID + "\x00" + snapshot.Host
	if snapshot.Generation < 1 {
		return Snapshot{}, catalog.E(catalog.CodeConflict, "publication generation must be positive")
	}
	if m.generations[generationKey] == nil {
		m.generations[generationKey] = make(map[int64]struct{})
	}
	if _, exists := m.generations[generationKey][snapshot.Generation]; exists {
		return Snapshot{}, catalog.E(catalog.CodeConflict, "publication generation already exists")
	}
	previousGeneration := m.generation[generationKey]
	m.snapshots[memorySnapshotKey(snapshot.TenantID, snapshot.ID)] = cloneSnapshot(snapshot)
	m.generations[generationKey][snapshot.Generation] = struct{}{}
	if snapshot.Generation > previousGeneration {
		m.generation[generationKey] = snapshot.Generation
	}
	if err := m.appendEvidence(ctx, snapshot.TenantID, snapshot.ID, evidence, audit.ActionPublicationCandidateCreated); err != nil {
		delete(m.snapshots, memorySnapshotKey(snapshot.TenantID, snapshot.ID))
		delete(m.generations[generationKey], snapshot.Generation)
		m.generation[generationKey] = previousGeneration
		return Snapshot{}, err
	}
	return cloneSnapshot(snapshot), nil
}

// Activate promotes only a candidate, retires the prior active snapshot and
// restores both on evidence failure.
func (m *MemoryStore) Activate(ctx context.Context, tenantID, snapshotID string, at time.Time, evidence ...MutationEvidence) (Snapshot, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Snapshot
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.Activate(ctx, tenantID, snapshotID, at, evidence...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	key := memorySnapshotKey(tenantID, snapshotID)
	snapshot, ok := m.snapshots[key]
	if !ok || snapshot.TenantID != tenantID {
		return Snapshot{}, catalog.E(catalog.CodeNotFound, "publication snapshot was not found")
	}
	if snapshot.State != SnapshotCandidate {
		return Snapshot{}, catalog.E(catalog.CodeInvalidTransition, "only a candidate snapshot can be activated")
	}
	priorID := m.active[tenantID]
	if err := validateExpectedActive(evidence, priorID); err != nil {
		return Snapshot{}, err
	}
	var prior Snapshot
	if priorID != "" {
		priorKey := memorySnapshotKey(tenantID, priorID)
		prior = m.snapshots[priorKey]
		prior.State = SnapshotRetired
		m.snapshots[priorKey] = prior
	}
	snapshot.State = SnapshotActive
	snapshot.ActivatedAt = &at
	m.snapshots[key] = snapshot
	m.active[tenantID] = snapshotID
	if err := m.appendEvidence(ctx, tenantID, snapshotID, evidence, audit.ActionPublicationActivated); err != nil {
		snapshot.State = SnapshotCandidate
		snapshot.ActivatedAt = nil
		m.snapshots[key] = snapshot
		if priorID == "" {
			delete(m.active, tenantID)
		} else {
			prior.State = SnapshotActive
			m.snapshots[memorySnapshotKey(tenantID, priorID)] = prior
			m.active[tenantID] = priorID
		}
		return Snapshot{}, err
	}
	return cloneSnapshot(snapshot), nil
}

// appendEvidence validates the expected publication action and exact snapshot
// bindings before retaining audit/outbox evidence.
func (m *MemoryStore) appendEvidence(ctx context.Context, tenantID, targetID string, values []MutationEvidence, expectedAction string) error {
	if err := ValidateMutationEvidence(tenantID, targetID, values, expectedAction); err != nil {
		return err
	}
	if len(values) == 0 || values[0].Audit.ID == "" {
		return nil
	}
	if m.evidence == nil {
		return catalog.E(catalog.CodeInternal, "publication evidence is not configured")
	}
	return m.evidence.AppendEvidence(ctx, values[0].Audit, values[0].Outbox)
}

// ValidateMutationEvidence binds an atomic publication evidence bundle to the
// exact tenant and immutable snapshot changed by its store operation.
func ValidateMutationEvidence(tenantID, targetID string, values []MutationEvidence, expectedActions ...string) error {
	if len(values) == 0 {
		return nil
	}
	if len(values) != 1 {
		return catalog.E(catalog.CodeInternal, "publication evidence is ambiguous")
	}
	value := values[0]
	if value.Audit.ID == "" {
		if value.Outbox != nil {
			return catalog.E(catalog.CodeInternal, "publication notification is missing its audit evidence")
		}
		return nil
	}
	if value.Audit.TenantID != tenantID || value.Audit.TargetID != targetID {
		return catalog.E(catalog.CodeInternal, "publication audit evidence does not match its mutation")
	}
	if len(expectedActions) > 0 && !containsPublicationAction(expectedActions, value.Audit.Action) {
		return catalog.E(catalog.CodeInternal, "publication audit action does not match its store operation")
	}
	if err := audit.ValidateNewRecord(value.Audit); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "publication audit evidence is invalid", err)
	}
	publicAction := publicationNotificationAction(value.Audit.Action)
	expectedType := publicationNotificationType(value.Audit.Action)
	switch {
	case value.Audit.Action == audit.ActionPublicationCandidateCreated && value.Outbox != nil:
		return catalog.E(catalog.CodeInternal, "audit-only publication candidate has a notification")
	case value.Audit.Action == audit.ActionPublicationCandidateCreated:
	case expectedType == "":
		return catalog.E(catalog.CodeInternal, "publication mutation has an unexpected audit action")
	case value.Outbox == nil:
		return catalog.E(catalog.CodeInternal, "publication mutation is missing its required notification")
	case value.Outbox.Type != expectedType:
		return catalog.E(catalog.CodeInternal, "publication notification type does not match its audit action")
	}
	if value.Outbox != nil {
		facts, _ := value.Audit.Details["facts"].(map[string]any)
		host, _ := facts["host"].(string)
		if value.Outbox.ID == "" || value.Outbox.Internal || !value.Outbox.CreatedAt.Equal(value.Audit.At) || value.Outbox.PublishedAt != nil && !value.Outbox.PublishedAt.Equal(value.Audit.At) || value.Outbox.LeaseOwner != "" || value.Outbox.LeaseExpiresAt != nil {
			return catalog.E(catalog.CodeInternal, "publication notification metadata does not match its audit evidence")
		}
		if err := events.ValidateReferenceData(value.Outbox.Data, publicAction, value.Audit.TargetType, targetID, value.Audit.AfterDigest, host); err != nil {
			return catalog.Wrap(catalog.CodeInternal, "publication notification data does not match its audit evidence", err)
		}
	}
	if value.Outbox != nil {
		if value.Outbox.TenantID != tenantID || value.Outbox.Subject != targetID {
			return catalog.E(catalog.CodeInternal, "publication notification does not match its mutation")
		}
	}
	return nil
}

// containsPublicationAction checks exact action membership in the allowed
// transition vocabulary.
func containsPublicationAction(actions []string, action string) bool {
	for _, candidate := range actions {
		if action == candidate {
			return true
		}
	}
	return false
}

// publicationNotificationType maps a publication audit action to its public
// notification type.
func publicationNotificationType(action string) string {
	publicAction := publicationNotificationAction(action)
	if publicAction == "" {
		return ""
	}
	return catalog.CanonicalEventType(publicAction)
}

// publicationNotificationAction selects the public action corresponding to an
// audit lifecycle event.
func publicationNotificationAction(action string) string {
	switch action {
	case audit.ActionPublicationActivated, audit.ActionPublicationRolledBack:
		return "publication.activated"
	case audit.ActionPublicationUnpublished:
		return "publication.unpublished"
	default:
		return ""
	}
}

// validateExpectedActive checks optional pointer fences, distinguishing no
// fence from a required empty active pointer.
func validateExpectedActive(values []MutationEvidence, current string) error {
	if len(values) == 0 || values[0].ExpectedActiveID == nil {
		return nil
	}
	if *values[0].ExpectedActiveID != current {
		return catalog.E(catalog.CodePreconditionFailed, "active publication changed during the command")
	}
	return nil
}

// Get returns a copied retained snapshot for the exact tenant and snapshot ID.
func (m *MemoryStore) Get(_ context.Context, tenantID, snapshotID string) (Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snapshot, ok := m.snapshots[memorySnapshotKey(tenantID, snapshotID)]
	if !ok || snapshot.TenantID != tenantID {
		return Snapshot{}, catalog.E(catalog.CodeNotFound, "publication snapshot was not found")
	}
	return cloneSnapshot(snapshot), nil
}

// Active returns a copied snapshot selected by the tenant's active pointer.
func (m *MemoryStore) Active(_ context.Context, tenantID string) (Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id := m.active[tenantID]
	if id == "" {
		return Snapshot{}, catalog.E(catalog.CodeNotFound, "no active publication snapshot exists")
	}
	return cloneSnapshot(m.snapshots[memorySnapshotKey(tenantID, id)]), nil
}

// memorySnapshotKey includes both tenant and immutable snapshot ID in the
// in-memory index.
func memorySnapshotKey(tenantID, snapshotID string) string {
	return tenantID + "\x00" + snapshotID
}

// ListSummaries returns bounded tenant snapshot metadata in descending
// generation and ID order.
func (m *MemoryStore) ListSummaries(_ context.Context, tenantID string, request SnapshotListRequest) ([]SnapshotSummary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]SnapshotSummary, 0)
	for _, snapshot := range m.snapshots {
		beforeCursor := request.AfterGeneration == 0 || snapshot.Generation < request.AfterGeneration || snapshot.Generation == request.AfterGeneration && snapshot.ID < request.AfterID
		if snapshot.TenantID == tenantID && beforeCursor && (request.State == "" || snapshot.State == request.State) {
			items = append(items, SummarizeSnapshot(snapshot))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Generation != items[j].Generation {
			return items[i].Generation > items[j].Generation
		}
		return items[i].ID > items[j].ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items, nil
}

// cloneSnapshot copies retained documents and mutable collection fields so
// readers cannot alter stored publication evidence.
func cloneSnapshot(value Snapshot) Snapshot {
	copyValue := value
	copyValue.ARD.Entries = cloneEntries(value.ARD.Entries)
	copyValue.AICatalog.Entries = cloneAICatalogEntries(value.AICatalog.Entries)
	copyValue.Exclusions = make([]Exclusion, len(value.Exclusions))
	for index, exclusion := range value.Exclusions {
		copyValue.Exclusions[index] = exclusion
		copyValue.Exclusions[index].Profiles = append([]string(nil), exclusion.Profiles...)
		copyValue.Exclusions[index].Reasons = append([]string(nil), exclusion.Reasons...)
	}
	copyValue.ArtifactIDs = append([]string{}, value.ArtifactIDs...)
	return copyValue
}

// cloneAICatalogEntries copies AI Catalog entries and nested extension values.
func cloneAICatalogEntries(entries []AICatalogEntry) []AICatalogEntry {
	result := make([]AICatalogEntry, len(entries))
	for index, entry := range entries {
		result[index] = entry
		result[index].Data = append(json.RawMessage(nil), entry.Data...)
		result[index].Tags = append([]string(nil), entry.Tags...)
		if entry.Extensions != nil {
			result[index].Extensions = make(map[string]any, len(entry.Extensions))
			for key, value := range entry.Extensions {
				result[index].Extensions[key] = cloneJSONValue(value)
			}
		}
	}
	return result
}

// cloneEntries copies ARD entries and their nested raw data and metadata.
func cloneEntries(entries []Entry) []Entry {
	result := make([]Entry, len(entries))
	for index, entry := range entries {
		result[index] = entry
		result[index].Context = cloneJSONValue(entry.Context)
		result[index].Data = append([]byte(nil), entry.Data...)
		result[index].RepresentativeQueries = append([]string(nil), entry.RepresentativeQueries...)
		result[index].Capabilities = append([]string(nil), entry.Capabilities...)
		result[index].Tags = append([]string(nil), entry.Tags...)
		result[index].Subjects = append([]string(nil), entry.Subjects...)
		if entry.Metadata != nil {
			result[index].Metadata = make(map[string]any, len(entry.Metadata))
			for key, value := range entry.Metadata {
				result[index].Metadata[key] = cloneJSONValue(value)
			}
		}
	}
	return result
}

// cloneJSONValue recursively copies JSON-shaped discovery metadata.
func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			result[key] = cloneJSONValue(child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = cloneJSONValue(child)
		}
		return result
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
