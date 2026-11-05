package jobs

import (
	"context"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type OverviewStateCount struct {
	State State `json:"state"`
	Count int   `json:"count"`
}
type OverviewKind struct {
	Kind   string               `json:"kind"`
	States []OverviewStateCount `json:"states"`
}
type Overview struct {
	catalog.OverviewScope
	Total       catalog.BoundedCount `json:"total"`
	Kinds       []OverviewKind       `json:"kinds,omitempty"`
	Due         catalog.BoundedCount `json:"due"`
	OldestDueAt *time.Time           `json:"oldestDueAt,omitempty"`
}
type OverviewItem struct {
	Kind  string
	State State
}
type OverviewRows struct {
	Items       []OverviewItem
	Due         int
	OldestDueAt *time.Time
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := identity.RequirePermission(ctx, "job.read", "")
	if err != nil {
		return Overview{}, catalog.PermissionError("current Job permission is required", err)
	}
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		return Overview{}, catalog.PermissionError("Job summary admission failed", err)
	}
	request := ListRequest{validateTarget: s.targetValidation}
	if s.listTargets != nil {
		request.Targets = s.listTargets(actor.TenantID)
	}
	at := s.clock.Now().UTC()
	// Resolve family visibility even for empty queues. Do not expose other families.
	kinds := []OverviewKind{}
	for _, kind := range []string{"source.sync", TrustKind, "publication.generate", "semantic.reindex"} {
		permission, _ := ReadPermission(kind)
		allowed, checkErr := identity.CollectionAllows(ctx, permission, "")
		if checkErr != nil {
			return Overview{}, catalog.PermissionError("Job family admission failed", checkErr)
		}
		if allowed {
			row := OverviewKind{Kind: kind}
			for _, state := range []State{StateQueued, StateRunning, StateWaiting, StateSucceeded, StateFailed, StateCancelled, StateDeadLetter} {
				row.States = append(row.States, OverviewStateCount{State: state})
			}
			kinds = append(kinds, row)
		}
	}
	rows, err := s.store.ReadJobOverview(ctx, actor.TenantID, request, at)
	if err != nil {
		return Overview{}, err
	}
	out := Overview{OverviewScope: catalog.NewOverviewScope(actor.TenantID, at), Total: catalog.OverviewCount(len(rows.Items)), Due: catalog.OverviewCount(rows.Due), OldestDueAt: rows.OldestDueAt}
	if !out.Total.Limited {
		out.Kinds = kinds
		for _, item := range rows.Items {
			for i := range out.Kinds {
				if out.Kinds[i].Kind == item.Kind {
					for j := range out.Kinds[i].States {
						if out.Kinds[i].States[j].State == item.State {
							out.Kinds[i].States[j].Count++
						}
					}
				}
			}
		}
	}
	return out, nil
}

func dueAt(job Job) *time.Time {
	switch job.State {
	case StateQueued, StateWaiting:
		return &job.NotBefore
	case StateRunning:
		return job.LeaseExpiresAt
	case StateSucceeded, StateFailed, StateCancelled, StateDeadLetter:
		return nil
	default:
		return nil
	}
}

func (m *MemoryStore) ReadJobOverview(ctx context.Context, tenant string, request ListRequest, at time.Time) (OverviewRows, error) {
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return OverviewRows{}, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(bound context.Context) (OverviewRows, error) {
			return m.ReadJobOverview(bound, tenant, request, at)
		})
	}
	// Memory is development-only. Release the Job lock before target-store reads.
	request.Limit = int(^uint(0) >> 1)
	items, err := m.visibleJobSnapshot(ctx, tenant, request)
	if err != nil {
		return OverviewRows{}, err
	}
	out := OverviewRows{}
	for _, job := range items {
		if _, known := ReadPermission(job.Kind); !known {
			continue
		}
		if request.validateTarget != nil {
			if err := request.validateTarget(ctx, job); err != nil {
				code := catalog.ErrorCodeOf(err)
				if code == catalog.CodeForbidden || code == catalog.CodeNotFound || code == catalog.CodeInvalidArgument {
					continue
				}
				return OverviewRows{}, err
			}
		}
		if len(out.Items) <= catalog.OverviewSelectionLimit {
			out.Items = append(out.Items, OverviewItem{Kind: job.Kind, State: job.State})
		}
		if due := dueAt(job); due != nil && !due.After(at) {
			if out.Due <= catalog.OverviewSelectionLimit {
				out.Due++
			}
			if out.OldestDueAt == nil || due.Before(*out.OldestDueAt) {
				stamp := *due
				out.OldestDueAt = &stamp
			}
		}
	}
	return out, nil
}
