package federation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestSourceOverviewLatestRevisionAndCompleteHistory(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	manager := &Manager{store: store, clock: clock.NewFake(at)}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	for _, source := range []Source{{ID: "failing", Revision: 2, Status: SourceError, RetrievalMode: RetrievalIndexed, LastCompleteSnapshotID: "old"}, {ID: "live", Revision: 1, Status: SourceActive, RetrievalMode: RetrievalLive}, {ID: "new", Revision: 2, Status: SourcePaused, RetrievalMode: RetrievalIndexed, LastCompleteSnapshotID: "old-new"}, {ID: "removed", Status: SourceRemoved}, {ID: "success", Revision: 1, Status: SourceActive, RetrievalMode: RetrievalIndexed}} {
		source.TenantID = "tenant"
		source.DisplayName = source.ID
		store.sources[source.ID] = source
	}
	stamp := at.Add(-time.Hour)
	earlier := stamp.Add(-time.Hour)
	for _, s := range []Snapshot{{ID: "old", SourceID: "failing", SourceRevision: 1, State: SnapshotComplete, CompletedAt: &earlier}, {ID: "failed", SourceID: "failing", SourceRevision: 2, State: SnapshotFailed, CompletedAt: &stamp}, {ID: "old-new", SourceID: "new", SourceRevision: 1, State: SnapshotComplete, CompletedAt: &earlier}, {ID: "old-failure", SourceID: "success", SourceRevision: 1, State: SnapshotFailed, CompletedAt: &earlier}, {ID: "success", SourceID: "success", SourceRevision: 1, State: SnapshotComplete, CompletedAt: &stamp}, {ID: "interrupted", SourceID: "new", SourceRevision: 2, State: SnapshotRunning}} {
		s.TenantID = "tenant"
		store.snapshots[s.ID] = s
	}
	out, err := manager.SourceOverview(ctx)
	if err != nil || out.Total.Value != 4 || len(out.Problems) != 1 || out.Problems[0].SourceID != "failing" || out.Problems[0].LastComplete == nil || out.Problems[0].LastComplete.Revision != 1 || *out.HistoricalComplete != 2 {
		t.Fatal(out, err)
	}
	want := map[string]int{"not_applicable": 1, "not_yet_synced": 1, "complete": 1, "failed": 1, "incomplete": 0}
	for _, row := range out.Sync {
		if row.Count != want[row.State] {
			t.Fatal(row)
		}
	}
	// A newer success must hide the failure, without changing stored last-complete membership here.
	later := stamp.Add(time.Minute)
	store.snapshots["recovered"] = Snapshot{ID: "recovered", TenantID: "tenant", SourceID: "failing", SourceRevision: 2, State: SnapshotComplete, CompletedAt: &later}
	out, err = manager.SourceOverview(ctx)
	if err != nil || len(out.Problems) != 0 || store.sources["failing"].LastCompleteSnapshotID != "old" {
		t.Fatal(out, err)
	}
	other := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "other", Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	out, err = manager.SourceOverview(other)
	if err != nil || out.Total.Value != 0 || len(out.Problems) != 0 {
		t.Fatal(out, err)
	}
	if _, err = manager.SourceOverview(context.Background()); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
}

func TestSourceOverviewLimitAndIndependentProblemList(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	manager := &Manager{store: store, clock: clock.NewFake(at)}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	for i := range catalog.OverviewSelectionLimit + 1 {
		key := fmt.Sprintf("%05d", i)
		store.sources[key] = Source{ID: key, TenantID: "tenant", Revision: 1, Status: SourceActive, RetrievalMode: RetrievalIndexed}
		if i < 7 {
			stamp := at.Add(time.Duration(i) * time.Second)
			store.snapshots[key] = Snapshot{ID: key, TenantID: "tenant", SourceID: key, SourceRevision: 1, State: SnapshotFailed, CompletedAt: &stamp}
		}
	}
	out, err := manager.SourceOverview(ctx)
	if err != nil || !out.Total.Limited || out.States != nil || out.Sync != nil || out.HistoricalComplete != nil || len(out.Problems) != 5 || out.Problems[0].SourceID != "00006" {
		t.Fatal(out, err)
	}
}
