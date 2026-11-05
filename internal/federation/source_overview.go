package federation

import (
	"context"
	"sort"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type SourceStateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}
type SyncSummary struct {
	Revision    int64         `json:"revision"`
	State       SnapshotState `json:"state"`
	CompletedAt time.Time     `json:"completedAt"`
}
type SourceSyncProblem struct {
	SourceID     string       `json:"sourceId"`
	DisplayName  string       `json:"displayName"`
	Revision     int64        `json:"revision"`
	Latest       SyncSummary  `json:"latest"`
	LastComplete *SyncSummary `json:"lastComplete,omitempty"`
}
type SourceOverview struct {
	catalog.OverviewScope
	Total              catalog.BoundedCount `json:"total"`
	States             []SourceStateCount   `json:"states,omitempty"`
	Sync               []SourceStateCount   `json:"sync,omitempty"`
	HistoricalComplete *int                 `json:"historicalComplete,omitempty"`
	Problems           []SourceSyncProblem  `json:"problems"`
}
type SourceOverviewRow struct {
	Status, Sync       string
	HistoricalComplete bool
}
type SourceOverviewRows struct {
	Items    []SourceOverviewRow
	Problems []SourceSyncProblem
}
type SourceOverviewReader interface {
	ReadSourceOverview(context.Context, string) (SourceOverviewRows, error)
}

func (m *Manager) SourceOverview(ctx context.Context) (SourceOverview, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return SourceOverview{}, err
	}
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		return SourceOverview{}, catalog.PermissionError("source summary admission failed", err)
	}
	reader, ok := m.store.(SourceOverviewReader)
	if !ok {
		return SourceOverview{}, catalog.E(catalog.CodeDependencyUnavailable, "source summary is unavailable")
	}
	at := m.clock.Now()
	rows, err := reader.ReadSourceOverview(ctx, actor.TenantID)
	if err != nil {
		return SourceOverview{}, err
	}
	out := SourceOverview{OverviewScope: catalog.NewOverviewScope(actor.TenantID, at), Total: catalog.OverviewCount(len(rows.Items)), Problems: rows.Problems}
	if !out.Total.Limited {
		history := 0
		for _, state := range []string{"active", "paused", "error"} {
			out.States = append(out.States, SourceStateCount{State: state})
		}
		for _, state := range []string{"not_applicable", "not_yet_synced", "complete", "incomplete", "failed"} {
			out.Sync = append(out.Sync, SourceStateCount{State: state})
		}
		for _, row := range rows.Items {
			for i := range out.States {
				if out.States[i].State == row.Status {
					out.States[i].Count++
				}
			}
			for i := range out.Sync {
				if out.Sync[i].State == row.Sync {
					out.Sync[i].Count++
				}
			}
			if row.HistoricalComplete {
				history++
			}
		}
		out.HistoricalComplete = &history
	}
	return out, nil
}

func (m *MemoryStore) ReadSourceOverview(ctx context.Context, tenant string) (SourceOverviewRows, error) {
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return SourceOverviewRows{}, err
	}
	if identity.NeedsCollectionAdmission(ctx) {
		return identity.WithCollectionRead(ctx, func(bound context.Context) (SourceOverviewRows, error) { return m.ReadSourceOverview(bound, tenant) })
	}
	allowed, err := identity.CollectionAllows(ctx, "source.read", "")
	if err != nil {
		return SourceOverviewRows{}, err
	}
	if !allowed {
		return SourceOverviewRows{}, catalog.E(catalog.CodeForbidden, "source permission is required")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := SourceOverviewRows{Problems: []SourceSyncProblem{}}
	for _, source := range m.sources {
		if source.TenantID != tenant || source.Status == SourceRemoved {
			continue
		}
		var latest *SyncSummary
		var latestSnapshotID string
		for _, snapshot := range m.snapshots {
			if snapshot.TenantID != tenant || snapshot.SourceID != source.ID || snapshot.SourceRevision != source.Revision || snapshot.CompletedAt == nil {
				continue
			}
			if latest == nil || snapshot.CompletedAt.After(latest.CompletedAt) || snapshot.CompletedAt.Equal(latest.CompletedAt) && snapshot.ID > latestSnapshotID {
				latest = &SyncSummary{Revision: snapshot.SourceRevision, State: snapshot.State, CompletedAt: *snapshot.CompletedAt}
				latestSnapshotID = snapshot.ID
			}
		}
		var complete *SyncSummary
		if s, ok := m.snapshots[source.LastCompleteSnapshotID]; ok && s.TenantID == tenant && s.SourceID == source.ID && s.State == SnapshotComplete && s.CompletedAt != nil {
			complete = &SyncSummary{Revision: s.SourceRevision, State: s.State, CompletedAt: *s.CompletedAt}
		}
		row := SourceOverviewRow{Status: string(source.Status), Sync: "not_yet_synced", HistoricalComplete: complete != nil && complete.Revision != source.Revision}
		if source.RetrievalMode != RetrievalIndexed && source.RetrievalMode != RetrievalHybrid {
			row.Sync = "not_applicable"
		} else if latest != nil {
			row.Sync = string(latest.State)
		}
		if len(out.Items) <= catalog.OverviewSelectionLimit {
			out.Items = append(out.Items, row)
		}
		if (row.Sync == "failed" || row.Sync == "incomplete") && latest != nil {
			out.Problems = append(out.Problems, SourceSyncProblem{SourceID: source.ID, DisplayName: source.DisplayName, Revision: source.Revision, Latest: *latest, LastComplete: complete})
		}
	}
	sort.Slice(out.Problems, func(i, j int) bool {
		a, b := out.Problems[i], out.Problems[j]
		if !a.Latest.CompletedAt.Equal(b.Latest.CompletedAt) {
			return a.Latest.CompletedAt.After(b.Latest.CompletedAt)
		}
		return a.SourceID < b.SourceID
	})
	if len(out.Problems) > 5 {
		out.Problems = out.Problems[:5]
	}
	return out, nil
}
