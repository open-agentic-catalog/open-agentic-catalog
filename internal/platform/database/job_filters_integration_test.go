//go:build integration

package database

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

func TestPostgresJobFiltersBeforeBoundsAndCurrentAdmission(t *testing.T) {
	db := newHousekeepingDatabase(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	source := housekeepingSource(jobSourceID, housekeepingTenantA, at)
	if _, err := db.federation.CreateSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	parent := housekeepingJob("00000000-0000-7000-8000-000000003000", "", at)
	parent.Kind, parent.PayloadVersion = "source.sync", 2
	parent.Payload = json.RawMessage(`{"sourceId":"` + source.ID + `","sourceRevision":1,"trigger":"manual"}`)
	if _, _, err := db.apiJobs.Enqueue(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.workerJobs.Claim(t.Context(), "filters", at, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := db.workerJobs.Fail(t.Context(), parent.TenantID, parent.ID, "filters", at, jobs.FailureInternal, "Controlled terminal parent", time.Time{}); err != nil {
		t.Fatal(err)
	}
	want := []string{}
	for i := range 40 {
		job := housekeepingJob(fmt.Sprintf("00000000-0000-7000-8000-%012d", 3001+i), "", at.Add(time.Duration(i+1)*time.Second))
		job.Kind, job.PayloadVersion, job.Payload = parent.Kind, parent.PayloadVersion, parent.Payload
		if i < 3 {
			job.ParentJobID = parent.ID
			want = append([]string{job.ID}, want...)
		}
		if i == 38 {
			job.Kind, job.PayloadVersion, job.Payload = "publication.generate", 1, json.RawMessage(`{"host":"catalog.example"}`)
		}
		if i == 39 {
			// Retained Jobs can outlive their source; this schema-valid row is
			// intentionally inserted without pretending enqueue admitted it.
			job.Payload = json.RawMessage(`{"sourceId":"00000000-0000-7000-8000-000000009999","sourceRevision":1,"trigger":"manual"}`)
			if _, err := db.operator.Exec(t.Context(), `INSERT INTO jobs(id,tenant_id,kind,actor,payload_version,payload,state,not_before,attempt_limit,deduplication_key,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, job.ID, job.TenantID, job.Kind, job.Actor, job.PayloadVersion, job.Payload, job.State, job.NotBefore, job.AttemptLimit, job.DeduplicationKey, job.CreatedAt, job.UpdatedAt); err != nil {
				t.Fatal(err)
			}
		} else if _, _, err := db.apiJobs.Enqueue(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}
	actorCtx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "filtered-source-reader", Roles: []identity.Role{identity.RoleSourceAdmin}})
	ctx, _, err := identity.WithCollectionContext(actorCtx)
	if err != nil {
		t.Fatal(err)
	}
	q := jobs.ListRequest{Limit: 2, Kind: "source.sync", State: jobs.StateQueued, ParentJobID: parent.ID, Targets: jobs.ListTargets{Enforced: true}}
	first, err := db.apiJobs.ListViews(ctx, housekeepingTenantA, q)
	if err != nil || len(first) != 2 || first[0].ID != want[0] || first[1].ID != want[1] {
		t.Fatalf("off-page children: %+v, %v", first, err)
	}
	q.AfterCreatedAt, q.AfterID = &first[1].CreatedAt, first[1].ID
	last, err := db.apiJobs.ListViews(ctx, housekeepingTenantA, q)
	if err != nil || len(last) != 1 || last[0].ID != want[2] {
		t.Fatalf("continuation: %+v, %v", last, err)
	}
	q.AfterCreatedAt, q.AfterID = nil, ""
	for _, change := range []func(*jobs.ListRequest){
		func(q *jobs.ListRequest) { q.Kind = "unknown.kind" },
		func(q *jobs.ListRequest) { q.Kind = "Source.sync" },
		func(q *jobs.ListRequest) { q.State = jobs.StateSucceeded },
		func(q *jobs.ListRequest) { q.ParentJobID = "00000000-0000-7000-8000-000000009999" },
		func(q *jobs.ListRequest) { q.Kind = "publication.generate"; q.ParentJobID = "" },
	} {
		changed := q
		change(&changed)
		if page, err := db.apiJobs.ListViews(ctx, housekeepingTenantA, changed); err != nil || len(page) != 0 {
			t.Fatalf("literal/absent/family filter: %+v, %+v, %v", changed, page, err)
		}
	}
	q.ParentJobID = ""
	page, err := db.apiJobs.ListViews(ctx, housekeepingTenantA, q)
	if err != nil || len(page) != 2 || page[0].Kind != "source.sync" || page[0].ID == "00000000-0000-7000-8000-000000003040" {
		t.Fatalf("current target preselection: %+v %v", page, err)
	}
	if _, err := db.apiJobs.ListViews(ctx, housekeepingTenantB, q); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("cross-tenant principal admitted: %v", err)
	}
	if page, err := db.apiJobs.ListViews(t.Context(), housekeepingTenantB, q); err != nil || len(page) != 0 {
		t.Fatalf("other tenant sees children: %+v %v", page, err)
	}
}
