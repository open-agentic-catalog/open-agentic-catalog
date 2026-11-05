//go:build integration

package database

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresSourceOverviewRetainsHistoryAcrossFailureAndRevision(t *testing.T) {
	db := newHousekeepingDatabase(t)
	at := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	source, err := db.federation.CreateSource(t.Context(), housekeepingSource("00000000-0000-7000-8000-000000009100", housekeepingTenantA, at))
	if err != nil {
		t.Fatal(err)
	}
	workerRepo, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	workerStore, err := NewPostgresFederationStore(workerRepo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "overview-source-reader", Roles: []identity.Role{identity.RoleSourceAdmin}})
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(state federation.SnapshotState, n int) {
		t.Helper()
		completed := at.Add(time.Duration(n) * time.Minute)
		key, genErr := (id.Generator{}).New(completed)
		if genErr != nil {
			t.Fatal(genErr)
		}
		snapshot := federation.Snapshot{ID: key, TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, State: state, EnumerationComplete: state == federation.SnapshotComplete, StartedAt: at, CompletedAt: &completed, PolicyID: source.PolicyID, PolicyRevision: source.PolicyRevision, PolicyDigest: source.PolicyDigest}
		source.LastSyncAt = &completed
		source.UpdatedAt = completed
		if state == federation.SnapshotComplete {
			source.LastCompleteSnapshotID = key
			source.Status = federation.SourceActive
		} else {
			source.Status = federation.SourceError
		}
		_, source, err = workerStore.CommitSync(t.Context(), snapshot, nil, source, source.Revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	commit(federation.SnapshotComplete, 1)
	completeID := source.LastCompleteSnapshotID
	// A long failed history must preserve the last complete snapshot and
	// select the newest attempt without returning or hydrating that history.
	for n := 2; n <= 1000; n++ {
		commit(federation.SnapshotFailed, n)
	}
	rows, err := db.federation.ReadSourceOverview(ctx, housekeepingTenantA)
	if err != nil || len(rows.Items) != 1 || rows.Items[0].Sync != "failed" || len(rows.Problems) != 1 || rows.Problems[0].LastComplete == nil || !rows.Problems[0].LastComplete.CompletedAt.Equal(at.Add(time.Minute)) || source.LastCompleteSnapshotID != completeID || !rows.Problems[0].Latest.CompletedAt.Equal(at.Add(1000*time.Minute)) {
		t.Fatal(rows, err)
	}
	commit(federation.SnapshotComplete, 1001)
	rows, err = db.federation.ReadSourceOverview(ctx, housekeepingTenantA)
	if err != nil || len(rows.Problems) != 0 || rows.Items[0].Sync != "complete" {
		t.Fatal(rows, err)
	}
	source.Revision++
	source.DisplayName = "Revised source"
	source.UpdatedAt = at.Add(1002 * time.Minute)
	source, err = db.federation.UpdateSource(t.Context(), source, source.Revision-1)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = db.federation.ReadSourceOverview(ctx, housekeepingTenantA)
	if err != nil || rows.Items[0].Sync != "not_yet_synced" || !rows.Items[0].HistoricalComplete || len(rows.Problems) != 0 {
		t.Fatal(rows, err)
	}
	commit(federation.SnapshotIncomplete, 1003)
	rows, err = db.federation.ReadSourceOverview(ctx, housekeepingTenantA)
	if err != nil || len(rows.Problems) != 1 || rows.Problems[0].Latest.Revision != 2 || rows.Problems[0].LastComplete.Revision != 1 {
		t.Fatal(rows, err)
	}
	source.Status = federation.SourceRemoved
	source.Revision++
	source.UpdatedAt = at.Add(1004 * time.Minute)
	if _, err = db.federation.UpdateSource(t.Context(), source, source.Revision-1); err != nil {
		t.Fatal(err)
	}
	rows, err = db.federation.ReadSourceOverview(ctx, housekeepingTenantA)
	if err != nil || len(rows.Items) != 0 || len(rows.Problems) != 0 {
		t.Fatal(rows, err)
	}
	live := housekeepingSource("00000000-0000-7000-8000-000000009101", housekeepingTenantA, at)
	live.RetrievalMode = federation.RetrievalLive
	if _, err = db.federation.CreateSource(t.Context(), live); err != nil {
		t.Fatal(err)
	}
	rows, err = db.federation.ReadSourceOverview(ctx, housekeepingTenantA)
	if err != nil || len(rows.Items) != 1 || rows.Items[0].Sync != "not_applicable" || len(rows.Problems) != 0 {
		t.Fatal(rows, err)
	}
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "other-source-reader", Roles: []identity.Role{identity.RoleSourceAdmin}})
	other, _, err = identity.WithCollectionContext(other)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = db.federation.ReadSourceOverview(other, housekeepingTenantB)
	if err != nil || len(rows.Items) != 0 {
		t.Fatal(rows, err)
	}
}

func TestPostgresJobOverviewMatchesFamilyTargetsAndDueWork(t *testing.T) {
	db := newHousekeepingDatabase(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	source, err := db.federation.CreateSource(t.Context(), housekeepingSource("00000000-0000-7000-8000-000000009200", housekeepingTenantA, at.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"source.sync", "source.sync", "publication.generate", "unknown.kind"} {
		key, genErr := (id.Generator{}).New(at.Add(time.Duration(i) * time.Nanosecond))
		if genErr != nil {
			t.Fatal(genErr)
		}
		job := housekeepingJob(key, "", at.Add(-time.Minute))
		job.Kind = kind
		if kind == "source.sync" {
			job.PayloadVersion = 2
			job.Payload = json.RawMessage(`{"sourceId":"` + source.ID + `","sourceRevision":1,"trigger":"manual"}`)
		}
		if i >= 1 {
			job.NotBefore = at.Add(time.Hour)
		}
		if _, _, err = db.apiJobs.Enqueue(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}
	service, err := jobs.NewService(db.apiJobs, clock.NewFake(at), id.Generator{}, jobs.WithListTargets(func(string) jobs.ListTargets { return jobs.ListTargets{Enforced: true} }))
	if err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "overview-job-reader", Roles: []identity.Role{identity.RoleSourceAdmin}})
	out, err := service.Overview(ctx)
	if err != nil || out.Total.Value != 2 || out.Due.Value != 1 || out.OldestDueAt == nil || !out.OldestDueAt.Equal(at.Add(-time.Minute)) {
		t.Fatal(out, err)
	}
	claimed, err := db.workerJobs.Claim(t.Context(), "overview", at, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	out, err = service.Overview(ctx)
	if err != nil || out.Due.Value != 0 || out.OldestDueAt != nil {
		t.Fatal(out, err)
	}
	bound, _, err := identity.WithCollectionContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := db.apiJobs.ReadJobOverview(bound, housekeepingTenantA, jobs.ListRequest{Targets: jobs.ListTargets{Enforced: true}}, at.Add(time.Minute))
	if err != nil || expired.Due != 1 || expired.OldestDueAt == nil || !expired.OldestDueAt.Equal(at.Add(time.Minute)) {
		t.Fatal("lease expiry did not make the running Job due", expired, err)
	}
	if _, err = db.workerJobs.Complete(t.Context(), claimed.TenantID, claimed.ID, "overview", at.Add(time.Second), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	out, err = service.Overview(ctx)
	if err != nil || out.Total.Value != 2 || out.Kinds[0].States[3].Count != 1 {
		t.Fatal(out, err)
	}
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "other-job-reader", Roles: []identity.Role{identity.RoleSourceAdmin}})
	out, err = service.Overview(other)
	if err != nil || out.Total.Value != 0 || out.Due.Value != 0 {
		t.Fatal(out, err)
	}
}
