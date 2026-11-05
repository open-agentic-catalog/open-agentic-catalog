package jobs

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func sourceEnqueueEvidence(t *testing.T) (audit.Record, Job) {
	t.Helper()
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	job := Job{ID: "00000000-0000-7000-8000-000000000004", TenantID: "00000000-0000-7000-8000-000000000001", Actor: "00000000-0000-7000-8000-000000000002", Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"00000000-0000-7000-8000-000000000005","sourceRevision":2,"trigger":"manual"}`), DeduplicationKey: "source.sync:00000000-0000-7000-8000-000000000005:2", State: StateQueued, AttemptLimit: 5, CreatedAt: at, UpdatedAt: at}
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000006", job.TenantID, audit.Principal{ID: job.Actor, Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: "00000000-0000-7000-8000-000000000003"}, audit.ActionSourceSyncRequested, "federation_source", "00000000-0000-7000-8000-000000000005", "", audit.Execution{}, map[string]any{"sourceRevision": 2, "jobId": job.ID, "trigger": "manual", "deduplicated": false}, at)
	if err != nil {
		t.Fatal(err)
	}
	return record, job
}

func TestSourceEnqueueEvidenceReusesOnlyActiveJobs(t *testing.T) {
	t.Parallel()
	for _, trigger := range []string{"manual", "automatic"} {
		for _, state := range []State{StateQueued, StateRunning, StateWaiting, StateSucceeded, StateFailed, StateCancelled, StateDeadLetter} {
			t.Run(trigger+"/"+string(state), func(t *testing.T) {
				record, job := sourceEnqueueEvidence(t)
				// Reuse binds another command's Job but keeps this request's actor.
				job.ID = "00000000-0000-7000-8000-000000000007"
				job.Actor = "00000000-0000-7000-8000-000000000008"
				if trigger == "automatic" {
					job.Actor = audit.InitiatorSourceMaintainer
					job.Payload = json.RawMessage(`{"sourceId":"00000000-0000-7000-8000-000000000005","sourceRevision":2,"trigger":"automatic"}`)
				}
				// Prove all payload, identity, action and fact bindings succeed
				// before isolating the reused Job state guard.
				control, err := FinalizeEnqueueEvidence(record, job, true)
				wantFacts := map[string]any{"sourceRevision": json.Number("2"), "jobId": job.ID, "trigger": "manual", "deduplicated": true}
				if err != nil || control.Actor != record.Actor || control.TargetID != "00000000-0000-7000-8000-000000000005" || !reflect.DeepEqual(control.Details["facts"], wantFacts) {
					t.Fatalf("valid queued reuse = %+v, %v", control, err)
				}
				job.State = state
				before := cloneJob(job)
				got, err := FinalizeEnqueueEvidence(record, job, true)
				if state == StateQueued || state == StateRunning || state == StateWaiting {
					wantDigest := audit.DigestBytes([]byte(`{"id":"00000000-0000-7000-8000-000000000007","kind":"source.sync","state":"` + string(state) + `","progress":0,"attempt":0,"attemptLimit":5,"cancellationRequested":false}`))
					if err != nil || got.AfterDigest != wantDigest || !reflect.DeepEqual(got.Details["facts"], wantFacts) || got.Actor != record.Actor {
						t.Fatalf("active reuse = %+v, %v", got, err)
					}
				} else {
					var domainErr *catalog.DomainError
					if !errors.As(err, &domainErr) || domainErr.Code != catalog.CodeInternal || domainErr.Message != "source synchronization request evidence does not match the Job" || !reflect.DeepEqual(got, audit.Record{}) {
						t.Fatalf("terminal reuse = %+v, %v", got, err)
					}
				}
				if !reflect.DeepEqual(job, before) {
					t.Fatal("finalization changed retained Job history")
				}
			})
		}
	}
}

func TestJobFinalizersRejectInvalidFinalAuditAttribution(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"source enqueue", "semantic enqueue", "retry enqueue", "cancellation"} {
		for _, invalid := range []string{"realm", "mixed principal", "authorization digest"} {
			t.Run(operation+"/"+invalid, func(t *testing.T) {
				record, job := sourceEnqueueEvidence(t)
				var before Job
				wantFacts := map[string]any{"sourceRevision": json.Number("2"), "jobId": job.ID, "trigger": "manual", "deduplicated": false}
				switch operation {
				case "semantic enqueue":
					record, job = semanticEnqueueEvidence(t)
					wantFacts = map[string]any{"profile": "sha256:" + strings.Repeat("a", 64), "scope": "control", "jobId": job.ID, "deduplicated": false}
				case "retry enqueue":
					job.ParentJobID = "00000000-0000-7000-8000-000000000007"
					record.Action, record.TargetType, record.TargetID, record.Reason = audit.ActionJobRetried, "job", job.ParentJobID, "Retry reviewed work"
					wantFacts = map[string]any{"kind": "source.sync", "beforeState": "failed", "afterState": "queued", "parentJobId": job.ParentJobID, "replacementJobId": job.ID}
					record.Details["facts"] = maps.Clone(wantFacts)
				case "cancellation":
					before = cloneJob(job)
					job.State = StateCancelled
					record.Action, record.TargetType, record.TargetID, record.Reason = audit.ActionJobCancellationRequested, "job", job.ID, "Cancel reviewed work"
					wantFacts = map[string]any{"kind": "source.sync", "beforeState": "queued", "afterState": "cancelled", "cancellationRequested": false}
					record.Details["facts"] = maps.Clone(wantFacts)
				}
				finalize := func(value audit.Record) (audit.Record, error) {
					if operation == "cancellation" {
						return FinalizeCancellationEvidence(value, before, job)
					}
					return FinalizeEnqueueEvidence(value, job, false)
				}
				control, err := finalize(record)
				parentField := ""
				if operation == "retry enqueue" {
					parentField = `,"parentJobId":"00000000-0000-7000-8000-000000000007"`
				}
				wantAfter := audit.DigestBytes([]byte(`{"id":"00000000-0000-7000-8000-000000000004","kind":"` + job.Kind + `"` + parentField + `,"state":"` + string(job.State) + `","progress":0,"attempt":0,"attemptLimit":5,"cancellationRequested":false}`))
				wantBefore := ""
				if operation == "cancellation" {
					wantBefore = audit.DigestBytes([]byte(`{"id":"00000000-0000-7000-8000-000000000004","kind":"source.sync","state":"queued","progress":0,"attempt":0,"attemptLimit":5,"cancellationRequested":false}`))
				}
				if err != nil || audit.ValidateNewRecord(control) != nil || control.Actor != job.Actor || control.ID != record.ID || control.Reason != record.Reason || control.BeforeDigest != wantBefore || control.AfterDigest != wantAfter || !reflect.DeepEqual(control.Details["facts"], wantFacts) {
					t.Fatalf("valid control = %+v, %v", control, err)
				}
				switch invalid {
				case "realm":
					record.Details["realmId"] = "external-realm"
				case "mixed principal":
					record.Details["currentUser"] = "database-operator"
				case "authorization digest":
					record.Details["authorizationDigest"] = "not-a-digest"
				}
				// Only attribution changes: action-specific facts and row binding
				// remain valid, so the final shared audit validator must reject it.
				got, err := finalize(record)
				stage := "validate Job enqueue evidence"
				if operation == "cancellation" {
					stage = "validate Job cancellation evidence"
				}
				var domainErr *catalog.DomainError
				if !errors.As(err, &domainErr) || domainErr.Code != catalog.CodeInternal || domainErr.Message != stage || domainErr.Cause == nil || !reflect.DeepEqual(got, audit.Record{}) {
					t.Fatalf("invalid final attribution = %+v, %v", got, err)
				}
				sink := &recordingJobEvidence{}
				store := NewMemoryStore(sink)
				if operation == "cancellation" {
					store.jobs[before.ID] = cloneJob(before)
					result, err := store.RequestCancellation(t.Context(), before.TenantID, before.ID, before.UpdatedAt, record)
					if catalog.ErrorCodeOf(err) != catalog.CodeInternal || result.ID != "" || len(store.jobs) != 1 || !reflect.DeepEqual(store.jobs[before.ID], before) {
						t.Fatalf("invalid cancellation changed state: %+v, %v", result, err)
					}
				} else {
					wantJobs := map[string]Job{}
					if operation == "retry enqueue" {
						parent := cloneJob(job)
						parent.ID, parent.ParentJobID, parent.State = job.ParentJobID, "", StateFailed
						store.jobs[parent.ID] = cloneJob(parent)
						wantJobs[parent.ID] = parent
					}
					result, existing, err := store.Enqueue(t.Context(), job, record)
					if catalog.ErrorCodeOf(err) != catalog.CodeInternal || result.ID != "" || existing || !reflect.DeepEqual(store.jobs, wantJobs) {
						t.Fatalf("invalid enqueue retained Job: %+v, %t, %v", result, existing, err)
					}
				}
				if len(sink.records) != 0 {
					t.Fatal("invalid attribution reached the evidence sink")
				}
			})
		}
	}
}
