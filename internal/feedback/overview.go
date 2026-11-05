package feedback

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"time"
)

var overviewCategories = [...]string{"problem", "documentation", "suggestion", "positive", "other"}

type CategoryCount struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}
type Overview struct {
	catalog.OverviewScope
	Open         catalog.BoundedCount `json:"open"`
	Categories   []CategoryCount      `json:"categories,omitempty"`
	OldestOpenAt *time.Time           `json:"oldestOpenAt,omitempty"`
}
type OverviewRows struct {
	Categories   []string
	OldestOpenAt *time.Time
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, err := actor(ctx, false, true)
	if err != nil {
		return Overview{}, err
	}
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		return Overview{}, catalog.PermissionError("feedback summary admission failed", err)
	}
	at := s.clock.Now()
	rows, err := s.store.ReadOverview(ctx, a.TenantID)
	if err != nil {
		return Overview{}, err
	}
	out := Overview{OverviewScope: catalog.NewOverviewScope(a.TenantID, at), Open: catalog.OverviewCount(len(rows.Categories)), OldestOpenAt: rows.OldestOpenAt}
	if !out.Open.Limited {
		out.Categories = make([]CategoryCount, len(overviewCategories))
		for i, category := range overviewCategories {
			out.Categories[i].Category = category
			for _, row := range rows.Categories {
				if row == category {
					out.Categories[i].Count++
				}
			}
		}
	}
	return out, nil
}

func (m *MemoryStore) ReadOverview(ctx context.Context, tenant string) (OverviewRows, error) {
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return OverviewRows{}, err
	}
	if identity.NeedsCollectionAdmission(ctx) {
		return identity.WithCollectionRead(ctx, func(bound context.Context) (OverviewRows, error) { return m.ReadOverview(bound, tenant) })
	}
	allowed, err := identity.CollectionAllows(ctx, "feedback.moderate", "")
	if err != nil {
		return OverviewRows{}, err
	}
	if !allowed {
		return OverviewRows{}, catalog.E(catalog.CodeForbidden, "feedback moderation permission is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := OverviewRows{Categories: []string{}}
	if m.reader == nil {
		return out, catalog.E(catalog.CodeInternal, "feedback context reader is unavailable")
	}
	for _, record := range m.records {
		if record.TenantID != tenant || record.State != "open" {
			continue
		}
		if _, err := m.reader.GetResource(ctx, tenant, record.ResourceID); err != nil {
			if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
				continue
			}
			return out, err
		}
		if len(out.Categories) <= catalog.OverviewSelectionLimit {
			out.Categories = append(out.Categories, record.Category)
		}
		if out.OldestOpenAt == nil || record.CreatedAt.Before(*out.OldestOpenAt) {
			at := record.CreatedAt
			out.OldestOpenAt = &at
		}
	}
	return out, nil
}
