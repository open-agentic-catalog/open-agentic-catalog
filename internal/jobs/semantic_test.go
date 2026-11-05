// Semantic Job tests verify reasoned administration and strict executable payloads without services.
package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// TestSemanticReindexEvidence binds new/deduplicated Jobs and refuses untrusted payloads.
func TestSemanticReindexEvidence(t *testing.T) {
	sink := &recordingJobEvidence{}
	store := NewMemoryStore(sink)
	service, err := NewService(store, clock.NewFake(time.Now()), id.Generator{}, WithAuditEvidence())
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	payload := catalog.SemanticReindexInput{Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", SourceGeneration: "1"}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	input := EnqueueInput{Kind: "semantic.reindex", PayloadVersion: 1, Payload: raw, DeduplicationKey: payload.Profile + ":" + payload.Scope, SemanticReason: "Rebuild current metadata"}
	first, existing, err := service.Enqueue(ctx, input)
	if err != nil || existing {
		t.Fatalf("enqueue=%+v %v %v", first, existing, err)
	}
	second, existing, err := service.Enqueue(ctx, input)
	if err != nil || !existing || second.ID != first.ID || len(sink.records) != 2 {
		t.Fatalf("dedup=%+v %v %v", second, existing, err)
	}
	for i, record := range sink.records {
		if record.Action != audit.ActionSemanticReindexRequested || record.TargetID != first.ID || audit.ValidateNewRecord(record) != nil {
			t.Fatalf("unbound evidence=%+v", record)
		}
		wantFacts := map[string]any{"profile": payload.Profile, "scope": payload.Scope, "jobId": first.ID, "deduplicated": i == 1}
		if record.Actor != actor.MembershipID || !reflect.DeepEqual(record.Details["facts"], wantFacts) {
			t.Fatalf("incorrect enqueue attribution=%+v", record)
		}
	}
	actor.Roles = []identity.Role{identity.RoleReader}
	if _, _, err := service.Enqueue(identityfixture.WithActor(t.Context(), actor), input); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("reader admitted")
	}
	for _, bad := range []string{`{}`, strings.Replace(string(raw), `"1"`, `"01"`, 1), string(raw[:len(raw)-1]) + `,"unknown":true}`, string(raw[:len(raw)-1]) + `,"scope":"control"}`} {
		input.Payload = []byte(bad)
		if _, _, err := service.Enqueue(ctx, input); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("accepted bad payload=%s err=%v", bad, err)
		}
	}
	if len(store.jobs) != 1 || len(sink.records) != 2 {
		t.Fatal("invalid command changed state")
	}
	if err := ValidateActionablePayload(first); err != nil {
		t.Fatal(err)
	}
	first.PayloadVersion = 2
	if err := ValidateActionablePayload(first); err == nil {
		t.Fatal("accepted unknown executable version")
	}
}

// The finalizer receives a valid command record and the Job selected by the
// atomic store operation. Each case owns fresh maps and payload bytes.
func semanticEnqueueEvidence(t *testing.T) (audit.Record, Job) {
	t.Helper()
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}}
	payload := catalog.SemanticReindexInput{Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", SourceGeneration: "1"}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "00000000-0000-7000-8000-000000000004", TenantID: actor.TenantID, Actor: actor.MembershipID, Kind: "semantic.reindex", PayloadVersion: 1, Payload: raw, DeduplicationKey: payload.Profile + ":" + payload.Scope, State: StateQueued, AttemptLimit: 5, NotBefore: at, CreatedAt: at, UpdatedAt: at}
	service := &Service{ids: id.Generator{}}
	record, err := service.newAuditRecord(t.Context(), actor, audit.ActionSemanticReindexRequested, "job", job.ID, "Rebuild current metadata", semanticReindexFacts{Profile: payload.Profile, Scope: payload.Scope, JobID: job.ID}, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.ValidateNewRecord(record); err != nil {
		t.Fatal(err)
	}
	return record, job
}

func TestSemanticEnqueueFinalizerRejectsMismatchedBindings(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		for _, mode := range []string{"kind", "payload version", "malformed payload", "deduplication key", "profile", "scope", "new target", "new actor", "running", "waiting", "succeeded", "failed", "cancelled", "dead letter"} {
			if existing && (mode == "new target" || mode == "new actor" || mode == "running" || mode == "waiting") {
				continue // Active reuse deliberately retains another command's Job.
			}
			name := "new/" + mode
			if existing {
				name = "reused/" + mode
			}
			t.Run(name, func(t *testing.T) {
				record, job := semanticEnqueueEvidence(t)
				switch mode {
				case "kind":
					job.Kind = "source.sync"
				case "payload version":
					job.PayloadVersion = 2
				case "malformed payload":
					job.Payload = json.RawMessage(`{"profile":`)
				case "deduplication key":
					job.DeduplicationKey += ":different"
				case "profile", "scope":
					payload, err := catalog.DecodeSemanticReindex(job.Payload, job.PayloadVersion)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "profile" {
						payload.Profile = "sha256:" + strings.Repeat("b", 64)
					} else {
						payload.Scope = "public:00000000-0000-7000-8000-000000000005"
					}
					job.Payload, err = json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					// Keep the Job internally coherent so this tests its binding
					// to the request, rather than the earlier deduplication guard.
					job.DeduplicationKey = payload.Profile + ":" + payload.Scope
				case "new target":
					job.ID = "00000000-0000-7000-8000-000000000006"
				case "new actor":
					job.Actor = "00000000-0000-7000-8000-000000000007"
				case "running":
					job.State = StateRunning
				case "waiting":
					job.State = StateWaiting
				case "succeeded":
					job.State = StateSucceeded
				case "failed":
					job.State = StateFailed
				case "cancelled":
					job.State = StateCancelled
				case "dead letter":
					job.State = StateDeadLetter
				}
				beforeJob := cloneJob(job)
				before, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				got, err := FinalizeEnqueueEvidence(record, job, existing)
				if catalog.ErrorCodeOf(err) != catalog.CodeInternal || !reflect.DeepEqual(got, audit.Record{}) {
					t.Fatalf("unbound evidence=%+v error=%v", got, err)
				}
				after, err := json.Marshal(record)
				if err != nil || !bytes.Equal(before, after) || !reflect.DeepEqual(job, beforeJob) {
					t.Fatal("rejected finalization rewrote its inputs")
				}
			})
		}
	}
}

func TestSemanticEnqueueReuseKeepsJobAndRequestAttributionSeparate(t *testing.T) {
	t.Parallel()
	for _, state := range []State{StateQueued, StateRunning, StateWaiting} {
		for _, initiator := range []string{"00000000-0000-7000-8000-000000000007", "semantic-maintenance"} {
			t.Run(string(state)+"/"+initiator, func(t *testing.T) {
				record, requested := semanticEnqueueEvidence(t)
				retained := cloneJob(requested)
				retained.ID = "00000000-0000-7000-8000-000000000008"
				retained.Actor, retained.State = initiator, state
				if state != StateQueued {
					retained.Progress, retained.Attempt = 25, 1
				}
				if state == StateRunning {
					retained.LeaseOwner = "worker"
					expires := retained.UpdatedAt.Add(time.Minute)
					retained.LeaseExpiresAt = &expires
				}
				// Active work is keyed by profile/scope. A newer request must
				// neither rewrite the in-flight payload nor lose its own actor.
				payload, err := catalog.DecodeSemanticReindex(requested.Payload, requested.PayloadVersion)
				if err != nil {
					t.Fatal(err)
				}
				payload.SourceGeneration = "2"
				requested.Payload, err = json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				sink := &recordingJobEvidence{}
				store := NewMemoryStore(sink)
				stored := cloneJob(retained)
				if retained.LeaseExpiresAt != nil {
					// Keep expected lease evidence independent of stored state.
					expires := *retained.LeaseExpiresAt
					stored.LeaseExpiresAt = &expires
				}
				store.jobs[retained.ID] = stored
				got, existing, err := store.Enqueue(t.Context(), requested, record)
				if err != nil || !existing || !reflect.DeepEqual(got, retained) || len(store.jobs) != 1 || !reflect.DeepEqual(store.jobs[retained.ID], retained) || len(sink.records) != 1 {
					t.Fatalf("reused Job=%+v existing=%t error=%v records=%d", got, existing, err, len(sink.records))
				}
				finalized := sink.records[0]
				if err := audit.ValidateNewRecord(finalized); err != nil {
					t.Fatal(err)
				}
				wantFacts := map[string]any{"profile": "sha256:" + strings.Repeat("a", 64), "scope": "control", "jobId": retained.ID, "deduplicated": true}
				if finalized.TargetID != retained.ID || finalized.Actor != record.Actor || finalized.ID != record.ID || finalized.Reason != record.Reason || !reflect.DeepEqual(finalized.Details["facts"], wantFacts) {
					t.Fatalf("reused Job lost request attribution: %+v", finalized)
				}
				wantDigest, err := jobStateDigest(retained)
				if err != nil || finalized.AfterDigest != wantDigest {
					t.Fatal("audit digest does not describe the retained Job state")
				}
				// Audit failure on another request must not report successful
				// reuse, replace the Job, or append a partial record.
				sentinel := errors.New("audit unavailable")
				store.evidence = failingJobEvidence{err: sentinel}
				failedRecord, _ := semanticEnqueueEvidence(t)
				got, existing, err = store.Enqueue(t.Context(), requested, failedRecord)
				if !errors.Is(err, sentinel) || got.ID != "" || existing || len(store.jobs) != 1 || !reflect.DeepEqual(store.jobs[retained.ID], retained) || len(sink.records) != 1 {
					t.Fatalf("failed reuse changed state: %+v existing=%t error=%v", got, existing, err)
				}
			})
		}
	}
}
