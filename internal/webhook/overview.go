package webhook

import (
	"context"
	"sort"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type StateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

type EndpointOverview struct {
	catalog.OverviewScope
	Total  catalog.BoundedCount `json:"total"`
	States []StateCount         `json:"states,omitempty"`
}

// FailedEndpoint contains metadata only, without event bodies or receiver diagnostics.
type FailedEndpoint struct {
	DeliveryID  string        `json:"deliveryId"`
	State       DeliveryState `json:"state"`
	EndpointID  string        `json:"endpointId"`
	Name        string        `json:"name"`
	AttemptedAt time.Time     `json:"attemptedAt"`
}

type DeliveryOverview struct {
	catalog.OverviewScope
	Total           catalog.BoundedCount `json:"total"`
	States          []StateCount         `json:"states,omitempty"`
	FailedEndpoints []FailedEndpoint     `json:"failedEndpoints"`
}

type DeliveryOverviewRows struct {
	States          []string
	FailedEndpoints []FailedEndpoint
}

func overviewContext(ctx context.Context, permission identity.Permission) (context.Context, identity.Actor, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, permission)
	if err != nil {
		return ctx, actor, err
	}
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		return ctx, actor, catalog.PermissionError("webhook summary admission failed", err)
	}
	return ctx, actor, nil
}

func overviewStates(rows []string, states ...string) []StateCount {
	if len(rows) > catalog.OverviewSelectionLimit {
		return nil
	}
	counts := make([]StateCount, len(states))
	for i, state := range states {
		counts[i].State = state
		for _, row := range rows {
			if row == state {
				counts[i].Count++
			}
		}
	}
	return counts
}

func (s *Service) EndpointOverview(ctx context.Context) (EndpointOverview, error) {
	ctx, actor, err := overviewContext(ctx, "webhook.read")
	if err != nil {
		return EndpointOverview{}, err
	}
	at := s.clock.Now()
	rows, err := s.store.ReadEndpointOverview(ctx, actor.TenantID)
	if err != nil {
		return EndpointOverview{}, err
	}
	return EndpointOverview{OverviewScope: catalog.NewOverviewScope(actor.TenantID, at), Total: catalog.OverviewCount(len(rows)), States: overviewStates(rows, "active", "disabled")}, nil
}

func (s *Service) DeliveryOverview(ctx context.Context) (DeliveryOverview, error) {
	ctx, actor, err := overviewContext(ctx, "webhook.read_evidence")
	if err != nil {
		return DeliveryOverview{}, err
	}
	at := s.clock.Now()
	rows, err := s.store.ReadDeliveryOverview(ctx, actor.TenantID)
	if err != nil {
		return DeliveryOverview{}, err
	}
	return DeliveryOverview{OverviewScope: catalog.NewOverviewScope(actor.TenantID, at), Total: catalog.OverviewCount(len(rows.States)), States: overviewStates(rows.States, "pending", "waiting", "succeeded", "dead_letter"), FailedEndpoints: rows.FailedEndpoints}, nil
}

func memoryOverviewRead[T any](ctx context.Context, tenant string, permission identity.Permission, read func() (T, error)) (T, error) {
	var zero T
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return zero, err
	}
	if identity.NeedsCollectionAdmission(ctx) {
		return identity.WithCollectionRead(ctx, func(bound context.Context) (T, error) { return memoryOverviewRead(bound, tenant, permission, read) })
	}
	allowed, err := identity.CollectionAllows(ctx, permission, "")
	if err != nil {
		return zero, err
	}
	if !allowed {
		return zero, catalog.E(catalog.CodeForbidden, "webhook summary permission is required")
	}
	return read()
}

func (m *MemoryStore) ReadEndpointOverview(ctx context.Context, tenant string) ([]string, error) {
	return memoryOverviewRead(ctx, tenant, "webhook.read", func() ([]string, error) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		rows := []string{}
		for _, endpoint := range m.endpoints {
			if endpoint.TenantID == tenant {
				rows = append(rows, string(endpoint.Status))
			}
			if len(rows) > catalog.OverviewSelectionLimit {
				break
			}
		}
		return rows, nil
	})
}

func (m *MemoryStore) ReadDeliveryOverview(ctx context.Context, tenant string) (DeliveryOverviewRows, error) {
	return memoryOverviewRead(ctx, tenant, "webhook.read_evidence", func() (DeliveryOverviewRows, error) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		out := DeliveryOverviewRows{States: []string{}, FailedEndpoints: []FailedEndpoint{}}
		latest := map[string]Delivery{}
		for _, delivery := range m.deliveries {
			if delivery.TenantID != tenant || delivery.EventType == catalog.CanonicalEventType("webhook.test") {
				continue
			}
			if len(out.States) <= catalog.OverviewSelectionLimit {
				out.States = append(out.States, string(delivery.State))
			}
			endpoint, ok := m.endpoints[delivery.EndpointID]
			if !ok || endpoint.TenantID != tenant || endpoint.Status != StatusActive || delivery.Attempt == 0 || delivery.UpdatedAt.Before(endpoint.UpdatedAt) {
				continue
			}
			previous, exists := latest[endpoint.ID]
			if !exists || delivery.UpdatedAt.After(previous.UpdatedAt) || delivery.UpdatedAt.Equal(previous.UpdatedAt) && delivery.ID > previous.ID {
				latest[endpoint.ID] = delivery
			}
		}
		for endpoint, delivery := range latest {
			if delivery.State == DeliveryWaiting || delivery.State == DeliveryDeadLetter {
				out.FailedEndpoints = append(out.FailedEndpoints, FailedEndpoint{EndpointID: endpoint, Name: m.endpoints[endpoint].Name, AttemptedAt: delivery.UpdatedAt, DeliveryID: delivery.ID, State: delivery.State})
			}
		}
		sort.Slice(out.FailedEndpoints, func(i, j int) bool {
			a, b := out.FailedEndpoints[i], out.FailedEndpoints[j]
			if !a.AttemptedAt.Equal(b.AttemptedAt) {
				return a.AttemptedAt.After(b.AttemptedAt)
			}
			return a.EndpointID < b.EndpointID
		})
		out.FailedEndpoints = out.FailedEndpoints[:min(5, len(out.FailedEndpoints))]
		return out, nil
	})
}
