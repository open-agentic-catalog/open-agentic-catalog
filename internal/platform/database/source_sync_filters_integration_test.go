//go:build integration

// Current synchronization filtering runs under the real API role and RLS.
package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func TestPostgresSourceSyncFiltersCurrentRevisionPages(t *testing.T) {
	db := newHousekeepingDatabase(t)
	workerRepo, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewPostgresFederationStore(workerRepo)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	key := func(n int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", 880000+n) }
	sources := make([]federation.Source, 40)
	commit := func(i, n int, state federation.SnapshotState, completed time.Time) federation.Snapshot {
		t.Helper()
		s := sources[i]
		snapshot := federation.Snapshot{ID: key(1000 + i*10 + n), TenantID: s.TenantID, SourceID: s.ID, SourceRevision: s.Revision, State: state, EnumerationComplete: state == federation.SnapshotComplete, StartedAt: at, CompletedAt: &completed, PolicyID: s.PolicyID, PolicyRevision: s.PolicyRevision, PolicyDigest: s.PolicyDigest}
		s.LastSyncAt, s.UpdatedAt = &completed, completed
		if state == federation.SnapshotComplete {
			s.LastCompleteSnapshotID = snapshot.ID
		}
		retained, updated, e := worker.CommitSync(t.Context(), snapshot, nil, s, s.Revision)
		if e != nil {
			t.Fatal(e)
		}
		sources[i] = updated
		return retained
	}
	for i := range sources {
		s := housekeepingSource(key(i), housekeepingTenantA, at)
		s.DisplayName = "Earlier sources"
		if i < 3 {
			s.Status = federation.SourcePaused
		}
		if i >= 30 {
			s.DisplayName = "Target"
		}
		if i == 39 {
			s.TenantID = housekeepingTenantB
		}
		sources[i], err = db.federation.CreateSource(t.Context(), s)
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 || i >= 30 {
			commit(i, 1, federation.SnapshotComplete, at.Add(time.Minute))
			state := federation.SnapshotFailed
			if i == 38 {
				state = federation.SnapshotIncomplete
			}
			commit(i, 2, state, at.Add(2*time.Minute))
		}
	}
	// A newer queued retry has no completed snapshot. It must not suppress
	// the current failure or manufacture a new outcome.
	insertHousekeepingSourceJob(t, db, key(999), sources[30], `{"sourceId":"`+sources[30].ID+`","sourceRevision":1,"trigger":"automatic"}`)
	q := federation.SourceListRequest{DisplayName: "Target", Status: federation.SourceActive, SyncState: federation.SnapshotFailed, Limit: 3}
	read := func(want ...int) []federation.Source {
		t.Helper()
		rows, e := db.federation.ListSourcePage(t.Context(), housekeepingTenantA, q)
		if e != nil || len(rows) != len(want) {
			t.Fatalf("page %+v: %+v %v", q, rows, e)
		}
		for i, index := range want {
			r := rows[i]
			if r.ID != sources[index].ID || r.LatestCompletedSync == nil || r.LatestCompletedSync.Revision != r.Revision || r.LatestCompletedSync.State != q.SyncState || r.LatestCompletedSync.SnapshotID != key(1000+index*10+2) || r.LastCompleteSnapshotID != key(1000+index*10+1) {
				t.Fatal("lost bounded current-attempt or retained-complete reference", r)
			}
		}
		return rows
	}
	// Isolate the predicates: outcome-only includes earlier paused failures;
	// status and exact name each exclude them without consuming the page.
	q.DisplayName, q.Status = "", ""
	read(0, 1, 2)
	q.AfterDisplayName, q.AfterID = sources[2].DisplayName, sources[2].ID
	read(30, 31, 32)
	q.AfterDisplayName, q.AfterID = "", ""
	q.Status = federation.SourceActive
	read(30, 31, 32)
	q.DisplayName, q.Status = "Target", ""
	read(30, 31, 32)
	q.Status = federation.SourceActive
	other, err := db.federation.ListSourcePage(t.Context(), housekeepingTenantB, q)
	if err != nil || len(other) != 1 || other[0].ID != sources[39].ID {
		t.Fatal("foreign-tenant positive control", other, err)
	}
	first := read(30, 31, 32)
	q.AfterDisplayName, q.AfterID = first[2].DisplayName, first[2].ID
	read(33, 34, 35)
	q.AfterID = sources[35].ID
	read(36, 37)
	q.AfterID = sources[37].ID
	read()
	q.AfterDisplayName, q.AfterID = "", ""
	q.SyncState = federation.SnapshotIncomplete
	read(38)
	q.SyncState = federation.SnapshotFailed
	// A later completion removes a failure without rewriting its old evidence.
	failedID := key(1000 + 30*10 + 2)
	commit(30, 3, federation.SnapshotComplete, at.Add(3*time.Minute))
	read(31, 32, 33)
	retained, err := db.federation.GetSnapshot(t.Context(), housekeepingTenantA, failedID)
	if err != nil || retained.State != federation.SnapshotFailed {
		t.Fatal(retained, err)
	}
	for _, i := range []int{31, 32, 33} {
		s := sources[i]
		s.Revision++
		s.UpdatedAt = at.Add(time.Hour)
		if i == 32 {
			s.Status = federation.SourceRemoved
		}
		if i == 33 {
			s.RetrievalMode = federation.RetrievalLive
		}
		sources[i], err = db.federation.UpdateSource(t.Context(), s, s.Revision-1)
		if err != nil {
			t.Fatal(err)
		}
	}
	read(34, 35, 36)
	// Equal completion times use the larger immutable snapshot ID, independent
	// of insertion order. A later insert with a smaller ID must not replace it.
	tiedAt := at.Add(2 * time.Minute)
	incomplete := commit(34, 4, federation.SnapshotIncomplete, tiedAt)
	commit(34, 3, federation.SnapshotFailed, tiedAt)
	read(35, 36, 37)
	q.SyncState = federation.SnapshotIncomplete
	rows, err := db.federation.ListSourcePage(t.Context(), housekeepingTenantA, q)
	if err != nil || len(rows) != 2 {
		t.Fatal("completion-time tie selection", rows, err)
	}
	if rows[0].ID != sources[34].ID || rows[0].LatestCompletedSync == nil || rows[0].LatestCompletedSync.SnapshotID != incomplete.ID || rows[0].LatestCompletedSync.Revision != sources[34].Revision || rows[0].LatestCompletedSync.State != federation.SnapshotIncomplete || !rows[0].LatestCompletedSync.CompletedAt.Equal(tiedAt) || rows[0].LastCompleteSnapshotID != key(1000+34*10+1) || rows[1].ID != sources[38].ID {
		t.Fatal("tie did not select the larger snapshot ID while retaining complete history", rows)
	}
}
