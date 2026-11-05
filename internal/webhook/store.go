// Webhook persistence keeps tenant-scoped endpoint and delivery records with
// transition evidence and claim-fenced worker completion.

package webhook

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"sort"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Store persists endpoint configuration and at-least-once delivery attempts;
// public notification authority remains in the Catalog outbox.
type Store interface {
	ReadEndpointOverview(context.Context, string) ([]string, error)
	ReadDeliveryOverview(context.Context, string) (DeliveryOverviewRows, error)
	NotificationState(context.Context, string) (catalog.NotificationState, error)
	CreateEndpoint(context.Context, Endpoint, ...audit.Record) (Endpoint, error)
	GetEndpoint(context.Context, string, string) (Endpoint, error)
	ListPendingEndpoints(context.Context, catalog.OutboxEvent, int) ([]Endpoint, error)
	ListEndpointPage(context.Context, string, EndpointListRequest) ([]Endpoint, error)
	UpdateEndpoint(context.Context, Endpoint, int64, ...audit.Record) (Endpoint, error)
	GetDelivery(context.Context, string, string, string) (Delivery, error)
	SaveDelivery(context.Context, Delivery, ...audit.Record) (Delivery, error)
	SaveClaimedDelivery(context.Context, Delivery, string, catalog.Clock, audit.Record) (Delivery, error)
	ListDeliveries(context.Context, string, DeliveryListRequest, ...DeliveryReadEvidence) ([]Delivery, error)
}

type MemoryStore struct {
	evidence     EvidenceSink
	mu           sync.RWMutex
	endpoints    map[string]Endpoint
	deliveries   map[string]Delivery
	notification map[string]catalog.NotificationState
}

// NewMemoryStore initializes development endpoint and delivery indexes with an
// optional single audit sink.
func NewMemoryStore(evidence ...EvidenceSink) *MemoryStore {
	store := &MemoryStore{endpoints: make(map[string]Endpoint), deliveries: make(map[string]Delivery), notification: make(map[string]catalog.NotificationState), evidence: catalog.NewMemoryRepository()}
	if len(evidence) == 1 {
		store.evidence = evidence[0]
	} else if len(evidence) > 1 {
		store.evidence = nil
	}
	return store
}

// CreateEndpoint validates supplied evidence and endpoint admission before
// retaining a tenant endpoint.
func (m *MemoryStore) CreateEndpoint(ctx context.Context, endpoint Endpoint, records ...audit.Record) (Endpoint, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Endpoint
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.CreateEndpoint(ctx, endpoint, records...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.endpoints {
		if existing.TenantID == endpoint.TenantID && existing.URL == endpoint.URL && existing.Status == StatusActive {
			return Endpoint{}, catalog.E(catalog.CodeConflict, "an active webhook already uses this URL")
		}
	}
	if err := ValidateEndpointEvidence(records, endpoint, audit.ActionWebhookCreated); err != nil {
		return Endpoint{}, err
	}
	if err := m.appendAudit(ctx, records); err != nil {
		return Endpoint{}, err
	}
	m.endpoints[endpoint.ID] = cloneEndpoint(endpoint)
	return cloneEndpoint(endpoint), nil
}

// GetEndpoint returns endpoint state only for the explicit tenant and ID.
func (m *MemoryStore) GetEndpoint(_ context.Context, tenantID, endpointID string) (Endpoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	endpoint, ok := m.endpoints[endpointID]
	if !ok || endpoint.TenantID != tenantID {
		return Endpoint{}, catalog.E(catalog.CodeNotFound, "webhook endpoint was not found")
	}
	return cloneEndpoint(endpoint), nil
}

// ListEndpointPage filters tenant endpoints before the limit and returns them
// in ascending name and ID order.
func (m *MemoryStore) ListEndpointPage(_ context.Context, tenantID string, request EndpointListRequest) ([]Endpoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Endpoint, 0)
	for _, endpoint := range m.endpoints {
		if endpoint.TenantID == tenantID && (request.Name == "" || endpoint.Name == request.Name) && (request.Status == "" || endpoint.Status == request.Status) && (request.AfterName == "" || endpoint.Name > request.AfterName || endpoint.Name == request.AfterName && endpoint.ID > request.AfterID) {
			items = append(items, cloneEndpoint(endpoint))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].ID < items[j].ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items, nil
}

// ListPendingEndpoints selects active event subscribers whose eligible delivery
// is not already terminal.
func (m *MemoryStore) ListPendingEndpoints(_ context.Context, event catalog.OutboxEvent, limit int) ([]Endpoint, error) {
	if limit < 1 || limit > dispatchEndpointLimit+1 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "webhook dispatch selection limit is invalid")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Endpoint, 0)
	for _, endpoint := range m.endpoints {
		if endpoint.TenantID != event.TenantID || endpoint.Status != StatusActive || endpoint.CreatedAt.After(event.CreatedAt) || !eventMatches(endpoint.EventTypes, event.Type) {
			continue
		}
		delivery := m.deliveries[deliveryKey(endpoint.ID, event.ID)]
		if delivery.State != DeliverySucceeded && delivery.State != DeliveryDeadLetter {
			items = append(items, cloneEndpoint(endpoint))
		}
	}
	due := func(endpoint Endpoint) time.Time {
		if at := m.deliveries[deliveryKey(endpoint.ID, event.ID)].NextAttemptAt; at != nil {
			return *at
		}
		return event.CreatedAt
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := due(items[i]), due(items[j])
		if !left.Equal(right) {
			return left.Before(right)
		}
		return items[i].ID < items[j].ID
	})
	return items[:min(limit, len(items))], nil
}

// UpdateEndpoint enforces the expected revision and matching transition
// evidence before replacing endpoint state.
func (m *MemoryStore) UpdateEndpoint(ctx context.Context, endpoint Endpoint, expectedRevision int64, records ...audit.Record) (Endpoint, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Endpoint
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.UpdateEndpoint(ctx, endpoint, expectedRevision, records...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.endpoints[endpoint.ID]
	if !ok || current.TenantID != endpoint.TenantID {
		return Endpoint{}, catalog.E(catalog.CodeNotFound, "webhook endpoint was not found")
	}
	if current.Revision != expectedRevision {
		return Endpoint{}, catalog.E(catalog.CodePreconditionFailed, "webhook revision does not match")
	}
	if err := ValidateEndpointEvidence(records, endpoint, EndpointChangeAction(current, endpoint)); err != nil {
		return Endpoint{}, err
	}
	for _, existing := range m.endpoints {
		if existing.ID != endpoint.ID && existing.TenantID == endpoint.TenantID && existing.URL == endpoint.URL && existing.Status == StatusActive && endpoint.Status == StatusActive {
			return Endpoint{}, catalog.E(catalog.CodeConflict, "an active webhook already uses this URL")
		}
	}
	if err := m.appendAudit(ctx, records); err != nil {
		return Endpoint{}, err
	}
	m.endpoints[endpoint.ID] = cloneEndpoint(endpoint)
	return cloneEndpoint(endpoint), nil
}

// GetDelivery looks up the tenant, endpoint and event tuple used for delivery
// deduplication.
func (m *MemoryStore) GetDelivery(_ context.Context, tenantID, endpointID, eventID string) (Delivery, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	delivery, ok := m.deliveries[deliveryKey(endpointID, eventID)]
	if !ok || delivery.TenantID != tenantID {
		return Delivery{}, catalog.E(catalog.CodeNotFound, "webhook delivery was not found")
	}
	return delivery, nil
}

// SaveDelivery retains a delivery and its supplied audit evidence together for
// synchronous test sends.
func (m *MemoryStore) SaveDelivery(ctx context.Context, delivery Delivery, records ...audit.Record) (Delivery, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Delivery
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.SaveDelivery(ctx, delivery, records...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ValidateDeliveryEvidence(records, delivery); err != nil {
		return Delivery{}, err
	}
	if err := m.appendAudit(ctx, records); err != nil {
		return Delivery{}, err
	}
	if old := m.deliveries[deliveryKey(delivery.EndpointID, delivery.EventID)]; old.State != DeliveryDeadLetter {
		m.recordNotificationLoss(delivery)
	}
	m.deliveries[deliveryKey(delivery.EndpointID, delivery.EventID)] = delivery
	return delivery, nil
}

// ClaimEvidenceSink commits audit only while the exact outbox claim remains
// live. The memory store holds its own lock through this commit and the
// infallible delivery assignment, so readers cannot observe a partial outcome.
type ClaimEvidenceSink interface {
	AppendClaimedOutboxAudit(context.Context, string, string, string, catalog.Clock, audit.Record) error
}

// SaveClaimedDelivery persists worker delivery evidence only while the exact
// tenant/event outbox claim remains owned.
func (m *MemoryStore) SaveClaimedDelivery(ctx context.Context, delivery Delivery, owner string, clock catalog.Clock, record audit.Record) (Delivery, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Delivery
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.SaveClaimedDelivery(ctx, delivery, owner, clock, record)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ValidateDeliveryEvidence([]audit.Record{record}, delivery); err != nil {
		return Delivery{}, err
	}
	endpoint, exists := m.endpoints[delivery.EndpointID]
	if !exists || endpoint.TenantID != delivery.TenantID {
		return Delivery{}, catalog.E(catalog.CodeConflict, "webhook delivery endpoint is unavailable")
	}
	key := deliveryKey(delivery.EndpointID, delivery.EventID)
	if old, exists := m.deliveries[key]; exists {
		if old.TenantID != delivery.TenantID || old.ID != delivery.ID || old.EventType != delivery.EventType || !old.CreatedAt.Equal(delivery.CreatedAt) || old.State == DeliverySucceeded || old.State == DeliveryDeadLetter || delivery.Attempt != old.Attempt+1 || delivery.UpdatedAt.Before(old.UpdatedAt) {
			return Delivery{}, catalog.E(catalog.CodeConflict, "webhook delivery transition is invalid")
		}
	}
	claims, ok := m.evidence.(ClaimEvidenceSink)
	if !ok {
		return Delivery{}, catalog.E(catalog.CodeInternal, "webhook claim evidence sink is unavailable")
	}
	if err := claims.AppendClaimedOutboxAudit(ctx, delivery.TenantID, delivery.EventID, owner, clock, record); err != nil {
		return Delivery{}, err
	}
	m.recordNotificationLoss(delivery)
	m.deliveries[key] = delivery
	return delivery, nil
}

// ListDeliveries returns bounded endpoint history and validates access evidence
// against the visible page.
func (m *MemoryStore) ListDeliveries(ctx context.Context, tenantID string, request DeliveryListRequest, evidence ...DeliveryReadEvidence) ([]Delivery, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Delivery, 0)
	for _, delivery := range m.deliveries {
		beforeCursor := request.AfterUpdatedAt == nil || delivery.UpdatedAt.Before(*request.AfterUpdatedAt) || delivery.UpdatedAt.Equal(*request.AfterUpdatedAt) && delivery.ID < request.AfterID
		if delivery.TenantID == tenantID && (request.EndpointID == "" || delivery.EndpointID == request.EndpointID) && (request.DeliveryID == "" || delivery.ID == request.DeliveryID) && (request.EventID == "" || delivery.EventID == request.EventID) && (request.State == "" || delivery.State == request.State) && beforeCursor {
			items = append(items, delivery)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].ID > items[j].ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	records, err := PrepareDeliveryReadEvidence(tenantID, request.EndpointID, items, request.Limit, evidence)
	if err != nil {
		return nil, err
	}
	if err := m.appendAudit(ctx, records); err != nil {
		return nil, err
	}
	return items, nil
}

// cloneEndpoint copies subscriptions and ciphertext to prevent callers from
// mutating retained endpoint data.
func cloneEndpoint(endpoint Endpoint) Endpoint {
	copyValue := endpoint
	copyValue.EventTypes = append([]string(nil), endpoint.EventTypes...)
	copyValue.SecretCiphertext = append([]byte(nil), endpoint.SecretCiphertext...)
	return copyValue
}

// deliveryKey binds endpoint and event IDs into an unambiguous in-memory
// delivery index key.
func deliveryKey(endpointID, eventID string) string { return endpointID + "\x00" + eventID }
