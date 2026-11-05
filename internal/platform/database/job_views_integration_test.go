//go:build integration

package database

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

func TestPostgresJobViewsPreserveDecoderAndBoundRetainedResults(t *testing.T) {
	db := newHousekeepingDatabase(t)
	const profile = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	worker, err := NewPostgresJobStore(db.workerJobs.repository, profile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	source := housekeepingSource(jobSourceID, housekeepingTenantA, now)
	if _, err := db.federation.CreateSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	const snapshotID = "00000000-0000-7000-8000-000000000008"
	const sourceResult = `{"id":"` + snapshotID + `","sourceId":"` + jobSourceID + `","state":"complete"}`
	const publicationResult = `{"id":"` + snapshotID + `","host":"catalog.example","generation":7,"state":"candidate"}`
	with := func(base, extra string) string { return strings.TrimSuffix(base, "}") + "," + extra + "}" }
	large := strings.Repeat("x", 1<<20)
	tests := []struct {
		name, kind, result string
		valid              bool
	}{
		{"source", "source.sync", sourceResult, true},
		{"publication", "publication.generate", publicationResult, true},
		{"semantic zero", "semantic.reindex", `{"generation":"@JOB@","members":0}`, true},
		{"semantic count", "semantic.reindex", `{"generation":"@JOB@","members":123}`, true},
		{"semantic maximum", "semantic.reindex", `{"generation":"@JOB@","members":9223372036854775807}`, true},
		{"semantic private fields", "semantic.reindex", `{"generation":"@JOB@","members":2,"private":"` + large + `"}`, true},
		{"semantic mismatched generation", "semantic.reindex", `{"generation":"` + snapshotID + `","members":1}`, false},
		{"semantic invalid generation", "semantic.reindex", `{"generation":true,"members":1}`, false},
		{"semantic missing count", "semantic.reindex", `{"generation":"@JOB@"}`, false},
		{"semantic null count", "semantic.reindex", `{"generation":"@JOB@","members":null}`, false},
		{"semantic string count", "semantic.reindex", `{"generation":"@JOB@","members":"1"}`, false},
		{"semantic negative count", "semantic.reindex", `{"generation":"@JOB@","members":-1}`, false},
		{"semantic fraction count", "semantic.reindex", `{"generation":"@JOB@","members":1.5}`, false},
		{"semantic overflow count", "semantic.reindex", `{"generation":"@JOB@","members":9223372036854775808}`, false},
		{"semantic huge count", "semantic.reindex", `{"generation":"@JOB@","members":` + strings.Repeat("9", 40) + `}`, false},
		{"semantic large generation", "semantic.reindex", `{"generation":"` + large + `","members":1}`, false},
		{"semantic null", "semantic.reindex", `null`, false},
		{"semantic array", "semantic.reindex", `[]`, false},
		{"incomplete", "source.sync", strings.Replace(sourceResult, "complete", "incomplete", 1), true},
		{"failed snapshot", "source.sync", strings.Replace(sourceResult, "complete", "failed", 1), true},
		{"large source history", "source.sync", with(sourceResult, `"retrievals":["`+large+`"]`), true},
		{"large publication document", "publication.generate", with(publicationResult, `"ard":{"entries":["`+strings.Repeat(large, 36)+`"]}`), true},
		{"host exact byte ceiling", "publication.generate", strings.Replace(publicationResult, "catalog.example", strings.Repeat("a", 253), 1), true},
		{"host one byte over", "publication.generate", strings.Replace(publicationResult, "catalog.example", strings.Repeat("a", 254), 1), false},
		{"UTF8 host exact byte ceiling", "publication.generate", strings.Replace(publicationResult, "catalog.example", strings.Repeat("é", 126)+"a", 1), true},
		{"active publication", "publication.generate", strings.Replace(publicationResult, "candidate", "active", 1), true},
		{"retired publication", "publication.generate", strings.Replace(publicationResult, "candidate", "retired", 1), true},
		{"canonical integer exponent", "publication.generate", strings.Replace(publicationResult, `"generation":7`, `"generation":1e1`, 1), true},
		{"null selected generation", "publication.generate", strings.Replace(publicationResult, `"generation":7`, `"generation":null`, 1), false},
		{"null selected ID", "publication.generate", strings.Replace(publicationResult, `"id":"`+snapshotID+`"`, `"id":null`, 1), false},
		{"invalid later alias", "source.sync", with(sourceResult, `"sourceid":"`+large+`"`), false},
		{"unknown large key", "source.sync", with(sourceResult, `"`+large+`":{"private":"`+large+`"}`), true},
		{"oversized selected host", "publication.generate", strings.Replace(publicationResult, "catalog.example", large, 1), false},
		{"host bytes not characters", "publication.generate", strings.Replace(publicationResult, "catalog.example", strings.Repeat("é", 127), 1), false},
		{"oversized selected ID", "source.sync", strings.Replace(sourceResult, snapshotID, large, 1), false},
		{"oversized selected source", "source.sync", strings.Replace(sourceResult, jobSourceID, large, 1), false},
		{"oversized state", "source.sync", strings.Replace(sourceResult, "complete", large, 1), false},
		{"unused large host", "source.sync", with(sourceResult, `"host":"`+large+`"`), true},
		{"unused large source", "publication.generate", with(publicationResult, `"sourceId":"`+large+`"`), true},
		{"unused host object", "source.sync", with(sourceResult, `"host":{"private":"`+large+`"}`), false},
		{"unused source array", "publication.generate", with(publicationResult, `"sourceId":[]`), false},
		{"unused generation string", "source.sync", with(sourceResult, `"generation":"7"`), false},
		{"unused generation fraction", "source.sync", with(sourceResult, `"generation":1.5`), false},
		{"unused generation decimal", "source.sync", with(sourceResult, `"generation":1.0`), false},
		{"unused generation overflow", "source.sync", with(sourceResult, `"generation":9223372036854775808`), false},
		{"unused generation huge", "source.sync", with(sourceResult, `"generation":`+strings.Repeat("9", 40)), false},
		{"unused generation minimum", "source.sync", with(sourceResult, `"generation":-9223372036854775808`), true},
		{"generation maximum", "publication.generate", strings.Replace(publicationResult, `"generation":7`, `"generation":9223372036854775807`, 1), true},
		{"null unused fields", "source.sync", with(sourceResult, `"host":null,"generation":null`), true},
		{"missing source", "source.sync", `{"id":"` + snapshotID + `","state":"complete"}`, false},
		{"upper case fields", "source.sync", `{"ID":"` + snapshotID + `","SOURCEID":"` + jobSourceID + `","STATE":"complete"}`, true},
		{"long s fold", "source.sync", strings.Replace(sourceResult, "state", "ſtate", 1), true},
		{"wrong type in alias", "source.sync", with(sourceResult, `"ID":false`), false},
		{"large earlier alias", "source.sync", with(sourceResult, `"ID":"`+large+`"`), true},
		{"null later alias", "source.sync", with(sourceResult, `"sourceid":null`), true},
		{"unknown result", "other.kind", publicationResult, false},
		{"null", "source.sync", `null`, false},
		{"array", "source.sync", `[]`, false},
		{"scalar", "source.sync", `true`, false},
		{"empty", "source.sync", `{}`, false},
	}
	retained := make(map[string]jobs.View, len(tests))
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := housekeepingJob(fmt.Sprintf("00000000-0000-7000-8000-%012d", 2000+index), "", now)
			job.Kind = test.kind
			if job.Kind == "semantic.reindex" {
				job.Payload = json.RawMessage(`{"profile":"` + profile + `","scope":"control"}`)
			}
			if job.Kind == "source.sync" {
				job.PayloadVersion = 2
				job.Payload = json.RawMessage(`{"sourceId":"` + source.ID + `","sourceRevision":1,"trigger":"manual"}`)
			}
			enqueued, _, err := db.apiJobs.Enqueue(t.Context(), job)
			if err != nil {
				t.Fatal(err)
			}
			queuedView, err := db.apiJobs.GetView(t.Context(), job.TenantID, job.ID)
			if err != nil || queuedView.State != jobs.StateQueued || queuedView.Result != nil || !reflect.DeepEqual(queuedView, jobs.PublicView(enqueued)) {
				t.Fatalf("queued metadata differs: %+v, %v", queuedView, err)
			}
			claimed, err := worker.Claim(t.Context(), "view-test", now, time.Minute)
			if err != nil || claimed.ID != job.ID {
				t.Fatalf("claim: %v", err)
			}
			runningView, err := db.apiJobs.GetView(t.Context(), job.TenantID, job.ID)
			if err != nil || runningView.State != jobs.StateRunning || runningView.Attempt != 1 || runningView.Result != nil || !reflect.DeepEqual(runningView, jobs.PublicView(claimed)) {
				t.Fatalf("running metadata differs: %+v, %v", runningView, err)
			}
			full, err := db.workerJobs.Complete(t.Context(), job.TenantID, job.ID, "view-test", now, json.RawMessage(strings.ReplaceAll(test.result, "@JOB@", job.ID)))
			if err != nil {
				t.Fatal(err)
			}
			// Compare the stored JSONB decoder, not pre-normalization JSON numbers
			// or case-alias ordering that PostgreSQL did not retain.
			want := jobs.PublicView(full)
			if (want.Result != nil) != test.valid {
				t.Fatalf("invalid decoder control: valid=%t, summary=%+v", test.valid, want.Result)
			}
			got, err := db.apiJobs.GetView(t.Context(), job.TenantID, job.ID)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("metadata changed decoder semantics: got=%+v want=%+v err=%v", got, want, err)
			}
			var projected json.RawMessage
			if err := db.operator.QueryRow(t.Context(), `SELECT `+jobResultProjectionSQL+` FROM jobs WHERE tenant_id=$1 AND id=$2`, job.TenantID, job.ID).Scan(&projected); err != nil || len(projected) > 1024 {
				t.Fatalf("SQL returned oversized result metadata: bytes=%d, err=%v", len(projected), err)
			}
			after, err := db.apiJobs.Get(t.Context(), job.TenantID, job.ID)
			if err != nil || !bytes.Equal(after.Result, full.Result) || !bytes.Equal(after.Payload, full.Payload) {
				t.Fatal("metadata read changed retained execution data")
			}
			retained[job.ID] = want
		})
	}
	var after *time.Time
	afterID := ""
	seen := map[string]bool{}
	for {
		page, err := db.apiJobs.ListViews(t.Context(), housekeepingTenantA, jobs.ListRequest{AfterCreatedAt: after, AfterID: afterID, Limit: 3})
		if err != nil || len(page) > 3 {
			t.Fatalf("public page: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, item := range page {
			if seen[item.ID] || !reflect.DeepEqual(item, retained[item.ID]) || afterID != "" && item.ID >= afterID {
				t.Fatalf("page duplicated, reordered or changed metadata for %s", item.ID)
			}
			seen[item.ID] = true
			afterID = item.ID
		}
		after = &page[len(page)-1].CreatedAt
	}
	if len(seen) != len(tests) {
		t.Fatalf("page lost jobs: %d/%d", len(seen), len(tests))
	}
	if _, err := db.apiJobs.GetView(t.Context(), housekeepingTenantB, afterID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant metadata read: %v", err)
	}
	if page, err := db.apiJobs.ListViews(t.Context(), housekeepingTenantB, jobs.ListRequest{Limit: 100}); err != nil || len(page) != 0 {
		t.Fatalf("cross-tenant page: %+v, %v", page, err)
	}
	// New completion metadata must still use current family/profile admission.
	for _, role := range []identity.Role{identity.RoleTenantAdmin, identity.RoleSourceAdmin} {
		actorCtx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "semantic-views-" + string(role), Roles: []identity.Role{role}})
		ctx, _, err := identity.WithCollectionContext(actorCtx)
		if err != nil {
			t.Fatal(err)
		}
		for _, selectedProfile := range []string{profile, "sha256:" + strings.Repeat("b", 64), ""} {
			q := jobs.ListRequest{Limit: 100, Kind: "semantic.reindex", State: jobs.StateSucceeded, Targets: jobs.ListTargets{Enforced: true, SemanticProfile: selectedProfile}}
			page, err := db.apiJobs.ListViews(ctx, housekeepingTenantA, q)
			if err != nil {
				t.Fatal(err)
			}
			if role != identity.RoleTenantAdmin || selectedProfile != profile {
				if len(page) != 0 {
					t.Fatalf("semantic family/profile disclosed results: %s, %q: %+v", role, selectedProfile, page)
				}
				continue
			}
			if len(page) != 16 {
				t.Fatalf("current semantic population: %d", len(page))
			}
			valid := 0
			for _, item := range page {
				if !reflect.DeepEqual(item, retained[item.ID]) {
					t.Fatalf("admitted completion changed: %+v", item)
				}
				if item.Result != nil {
					valid++
				}
			}
			if valid != 4 {
				t.Fatalf("admitted valid summaries=%d", valid)
			}
		}
	}
}
