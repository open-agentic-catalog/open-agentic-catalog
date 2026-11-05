package ratings

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type StateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}
type Overview struct {
	catalog.OverviewScope
	Total  catalog.BoundedCount `json:"total"`
	States []StateCount         `json:"states,omitempty"`
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, err := identity.RequirePermission(ctx, "ratings.moderate", "")
	if err != nil {
		return Overview{}, catalog.PermissionError("rating moderation is not permitted", err)
	}
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		return Overview{}, catalog.PermissionError("rating summary admission failed", err)
	}
	at := s.clock.Now()
	rows, err := s.store.ReadOverview(ctx, a.TenantID)
	if err != nil {
		return Overview{}, err
	}
	out := Overview{OverviewScope: catalog.NewOverviewScope(a.TenantID, at), Total: catalog.OverviewCount(len(rows))}
	if !out.Total.Limited {
		out.States = []StateCount{{State: "active"}, {State: "withdrawn"}, {State: "removed"}}
		for _, row := range rows {
			for i := range out.States {
				if out.States[i].State == row {
					out.States[i].Count++
				}
			}
		}
	}
	return out, nil
}

func (m *MemoryStore) ReadOverview(ctx context.Context, tenant string) ([]string, error) {
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return nil, err
	}
	if identity.NeedsCollectionAdmission(ctx) {
		return identity.WithCollectionRead(ctx, func(bound context.Context) ([]string, error) { return m.ReadOverview(bound, tenant) })
	}
	allowed, err := identity.CollectionAllows(ctx, "ratings.moderate", "")
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, catalog.E(catalog.CodeForbidden, "rating moderation permission is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	states := []string{}
	for _, record := range m.records {
		if record.TenantID == tenant {
			states = append(states, record.State)
		}
		if len(states) > catalog.OverviewSelectionLimit {
			break
		}
	}
	return states, nil
}
