// Current outcome units use retained metadata and injected authorization only.
package federation

import (
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestSourceSyncFiltersSelectCurrentCompletedAttemptsBeforePage(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	manager := &Manager{store: store, clock: clock.NewFake(at)}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	key := func(i int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", i) }
	for i := 1; i <= 40; i++ {
		source := Source{ID: key(i), TenantID: "tenant", DisplayName: "Earlier sources", Revision: 2, Status: SourceActive, RetrievalMode: RetrievalIndexed}
		if i > 30 {
			source.DisplayName = "Target"
		}
		snapshot := Snapshot{ID: key(100 + i), TenantID: source.TenantID, SourceID: source.ID, SourceRevision: 2, State: SnapshotComplete, CompletedAt: &at}
		if i > 30 {
			snapshot.State = SnapshotFailed
		}
		if i <= 3 {
			source.Status, snapshot.State = SourcePaused, SnapshotFailed
		}
		switch i {
		case 39:
			snapshot.State = SnapshotIncomplete
		case 40:
			source.RetrievalMode = RetrievalLive
		}
		store.sources[source.ID], store.snapshots[snapshot.ID] = source, snapshot
	}
	// A retry is newer work, not a completed outcome. A retained complete
	// snapshot remains a separate fact even while the current attempt failed.
	store.snapshots[key(200)] = Snapshot{ID: key(200), TenantID: "tenant", SourceID: key(31), SourceRevision: 2, State: SnapshotRunning, StartedAt: at.Add(time.Hour)}
	earlier := at.Add(-time.Hour)
	store.snapshots[key(201)] = Snapshot{ID: key(201), TenantID: "tenant", SourceID: key(31), SourceRevision: 1, State: SnapshotComplete, CompletedAt: &earlier}
	source := store.sources[key(31)]
	source.LastCompleteSnapshotID = key(201)
	store.sources[source.ID] = source
	q := SourceListRequest{DisplayName: "Target", Status: SourceActive, SyncState: SnapshotFailed, Limit: 3}
	read := func(want ...int) []Source {
		t.Helper()
		items, err := manager.ListSourcePage(ctx, q)
		if err != nil || len(items) != len(want) {
			t.Fatalf("page %+v: %+v %v", q, items, err)
		}
		for i, n := range want {
			row := items[i]
			if row.ID != key(n) || row.LatestCompletedSync == nil || row.LatestCompletedSync.SnapshotID != key(100+n) || row.LatestCompletedSync.Revision != 2 || row.LatestCompletedSync.State != q.SyncState || !row.LatestCompletedSync.CompletedAt.Equal(at) {
				t.Fatalf("row %d: %+v", i, row)
			}
		}
		return items
	}
	// Exercise each predicate independently. Earlier paused failures match the
	// outcome alone; healthy rows must not consume the bounded result page.
	q.DisplayName, q.Status = "", ""
	read(1, 2, 3, 31)
	q.Status = SourceActive
	read(31, 32, 33, 34)
	q.DisplayName, q.Status = "Target", ""
	read(31, 32, 33, 34)
	q.Status = SourceActive
	first := read(31, 32, 33, 34)
	if first[0].LastCompleteSnapshotID != key(201) || store.sources[key(31)].LatestCompletedSync != nil {
		t.Fatal("projection changed retained source state")
	}
	q.AfterDisplayName, q.AfterID = first[2].DisplayName, first[2].ID
	read(34, 35, 36, 37)
	q.AfterID = key(36)
	read(37, 38)
	q.AfterID = key(38)
	read()
	q.AfterDisplayName, q.AfterID = "", ""
	q.SyncState = SnapshotIncomplete
	read(39)
	q.SyncState = SnapshotFailed
	// More recent completion recovers, even while operational status differs.
	later := at.Add(time.Minute)
	store.snapshots[key(202)] = Snapshot{ID: key(202), TenantID: "tenant", SourceID: key(31), SourceRevision: 2, State: SnapshotComplete, CompletedAt: &later}
	read(32, 33, 34, 35)
	if store.sources[key(31)].LastCompleteSnapshotID != key(201) || store.snapshots[key(131)].State != SnapshotFailed {
		t.Fatal("current projection rewrote retained evidence")
	}
	// Larger snapshot ID resolves a completion-time tie. Configuration changes
	// and removals exclude old outcomes; foreign-tenant evidence never matches.
	store.snapshots[key(203)] = Snapshot{ID: key(203), TenantID: "tenant", SourceID: key(32), SourceRevision: 2, State: SnapshotIncomplete, CompletedAt: &at}
	for _, n := range []int{33, 34, 35} {
		s := store.sources[key(n)]
		switch n {
		case 33:
			s.Revision++
		case 34:
			s.Status = SourceRemoved
		case 35:
			s.TenantID = "other"
		}
		store.sources[s.ID] = s
	}
	read(36, 37, 38)
	overview, err := manager.SourceOverview(ctx)
	if err != nil || len(overview.Problems) != 5 {
		t.Fatal(overview, err)
	}
	for _, problem := range overview.Problems {
		if problem.SourceID == key(31) || problem.SourceID == key(33) || problem.SourceID == key(34) || problem.SourceID == key(35) || problem.SourceID == key(40) {
			t.Fatal("Overview disagrees with current outcome selection", problem)
		}
	}
	reader := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	if _, err := manager.ListSourcePage(reader, q); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("source authority bypassed", err)
	}
}
