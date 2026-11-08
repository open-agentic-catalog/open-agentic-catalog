package jobs

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"pgregory.net/rapid"
)

func TestPropertyJobLeaseHistories(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		fake := clock.NewFake(at)
		sink := &recordingJobEvidence{}
		store := NewMemoryStore(sink)
		model := make([]Job, 0, 3)
		for i := range 3 {
			job := Job{ID: fmt.Sprintf("00000000-0000-7000-8000-%012d", 10+i), TenantID: fmt.Sprintf("00000000-0000-7000-8000-%012d", 1+i%2), Kind: "source.sync",
				State: StateQueued, Priority: 3 - i, PayloadVersion: 2, Payload: json.RawMessage(`{"synthetic":true}`), AttemptLimit: 3,
				NotBefore: at, CreatedAt: at, UpdatedAt: at, DeduplicationKey: fmt.Sprintf("generated-%d", i)}
			if i == 0 {
				job.AttemptLimit = 1 // Reclaim is allowed even after exhaustion.
			} else if i == 2 {
				job.AttemptLimit = rapid.SampledFrom([]int{1, 3}).Draw(rt, "terminal attempt limit")
				job.NotBefore = at.Add(time.Second)
			}
			if _, reused, err := store.Enqueue(t.Context(), job); err != nil || reused {
				rt.Fatalf("valid enqueue failed: %v", err)
			}
			model = append(model, job)
		}
		type action struct {
			kind  string
			job   int
			wrong string
		}
		// This prefix guarantees the important transitions in every generated
		// history. The tail varies their interleaving and invalid neighbors.
		actions := make([]action, 0, 30)
		actions = append(actions, []action{{"claim", 0, ""}, {"heartbeat", 0, "owner"}, {"complete", 0, "tenant"},
			{"before-expiry", 0, ""}, {"heartbeat", 0, ""}, {"at-expiry", 0, ""}, {"complete", 0, ""},
			{"claim", 0, ""}, {"cancel", 0, ""}, {"complete", 0, ""}, {"cancel", 0, ""},
			{"claim", 1, ""}, {"retry", 1, ""}, {"claim", 2, ""}, {"cancel", 1, ""}, {"fail", 2, ""}}...)
		for range rapid.IntRange(1, 14).Draw(rt, "tail length") {
			actions = append(actions, action{rapid.SampledFrom([]string{"advance", "claim", "heartbeat", "complete", "retry", "fail", "cancel"}).Draw(rt, "action"),
				rapid.IntRange(0, 2).Draw(rt, "job"), rapid.SampledFrom([]string{"", "", "owner", "tenant"}).Draw(rt, "credential neighbor")})
		}
		claims := 0
		expectedEvidence := 0
		for step, operation := range actions {
			now := fake.Now()
			before := model[operation.job]
			owner, tenant := before.LeaseOwner, before.TenantID
			if operation.wrong == "owner" {
				owner = "unrelated-worker"
			}
			if operation.wrong == "tenant" {
				tenant = model[(operation.job+1)%2].TenantID
				if tenant == before.TenantID {
					tenant = "00000000-0000-7000-8000-000000000099"
				}
			}
			leased := before.State == StateRunning && before.LeaseExpiresAt != nil && now.Before(*before.LeaseExpiresAt) && owner == before.LeaseOwner && tenant == before.TenantID
			var got Job
			var err error
			wantCode := catalog.ErrorCode("")
			switch operation.kind {
			case "advance":
				fake.Advance(time.Duration(rapid.IntRange(0, 5).Draw(rt, "clock advance")) * time.Second)
			case "before-expiry", "at-expiry":
				if before.LeaseExpiresAt == nil {
					rt.Fatal("mandatory history lost its lease")
				}
				target := *before.LeaseExpiresAt
				if operation.kind == "before-expiry" {
					target = target.Add(-time.Nanosecond)
				}
				fake.Advance(target.Sub(now))
			case "claim":
				selected := -1
				for i, job := range model {
					pending := (job.State == StateQueued || job.State == StateWaiting) && !now.Before(job.NotBefore)
					abandoned := job.State == StateRunning && job.LeaseExpiresAt != nil && !now.Before(*job.LeaseExpiresAt)
					// All fixture priorities differ; the choice is independent of
					// the production scheduling comparator and crosses tenants.
					if (pending || abandoned) && (selected < 0 || job.Priority > model[selected].Priority) {
						selected = i
					}
				}
				newOwner := fmt.Sprintf("worker-%d", claims%2)
				claims++
				got, err = store.Claim(t.Context(), newOwner, now, 5*time.Second)
				if selected < 0 {
					wantCode = catalog.CodeNotFound
				} else {
					job := &model[selected]
					job.State, job.LeaseOwner = StateRunning, newOwner
					job.Attempt++
					expires := now.Add(5 * time.Second)
					job.LeaseExpiresAt, job.UpdatedAt = &expires, now
					job.SafeError, job.FailureClass = "", ""
					if !reflect.DeepEqual(got, *job) {
						rt.Fatalf("claim step %d got=%#v want=%#v err=%v", step, got, *job, err)
					}
				}
			case "cancel":
				actor := identity.Actor{TenantID: tenant, MembershipID: "00000000-0000-7000-8000-000000000080", RealmID: "00000000-0000-7000-8000-000000000081", Subject: "model", ActorType: identity.ActorHuman}
				anticipatedState, anticipatedFlag := before.State, before.CancellationRequested
				if before.State == StateQueued || before.State == StateWaiting {
					anticipatedState = StateCancelled
				} else if before.State == StateRunning {
					anticipatedFlag = true
				}
				record, recordErr := (&Service{ids: id.Generator{}}).newAuditRecord(t.Context(), actor, audit.ActionJobCancellationRequested, "job", before.ID, "Cancel generated work", jobCancellationFacts{Kind: before.Kind, BeforeState: before.State, AfterState: anticipatedState, CancellationRequested: anticipatedFlag, FailureClass: before.FailureClass}, now)
				if recordErr != nil {
					rt.Fatal(recordErr)
				}
				priorEvidence := len(sink.records)
				got, err = store.RequestCancellation(t.Context(), tenant, before.ID, now, record)
				if tenant != before.TenantID {
					wantCode = catalog.CodeNotFound
				} else {
					expectedEvidence++
					job := &model[operation.job]
					if job.State == StateQueued || job.State == StateWaiting {
						job.State, job.UpdatedAt = StateCancelled, now
					} else if job.State == StateRunning {
						job.CancellationRequested, job.UpdatedAt = true, now
					}
					if len(sink.records) != priorEvidence+1 {
						rt.Fatal("successful cancellation omitted evidence, including terminal no-op")
					}
					last := sink.records[priorEvidence]
					facts, ok := last.Details["facts"].(map[string]any)
					if !ok || facts["beforeState"] != string(before.State) || facts["afterState"] != string(job.State) || facts["cancellationRequested"] != job.CancellationRequested || last.TargetID != before.ID || last.TenantID != tenant || audit.ValidateNewRecord(last) != nil {
						rt.Fatalf("cancellation evidence disagrees with model: %+v", last)
					}
				}
			case "heartbeat", "complete", "retry", "fail":
				if !leased {
					wantCode = catalog.CodeConflict
				}
				switch operation.kind {
				case "heartbeat":
					progress := rapid.IntRange(0, 99).Draw(rt, "progress")
					got, err = store.Heartbeat(t.Context(), tenant, before.ID, owner, now, 5*time.Second, progress)
					if leased {
						expires := now.Add(5 * time.Second)
						model[operation.job].LeaseExpiresAt, model[operation.job].Progress, model[operation.job].UpdatedAt = &expires, progress, now
					}
				case "complete":
					result := json.RawMessage(fmt.Sprintf(`{"step":%d}`, step))
					got, err = store.Complete(t.Context(), tenant, before.ID, owner, now, result)
					if leased {
						model[operation.job].State, model[operation.job].Progress, model[operation.job].Result = StateSucceeded, 100, result
					}
				default:
					retryAt := time.Time{}
					if operation.kind == "retry" {
						retryAt = now.Add(2 * time.Second)
					}
					got, err = store.Fail(t.Context(), tenant, before.ID, owner, now, FailureDependency, "Synthetic failure.", retryAt)
					if leased {
						job := &model[operation.job]
						job.State = StateFailed
						if job.Attempt >= job.AttemptLimit {
							job.State = StateDeadLetter
						} else if !retryAt.IsZero() {
							job.State, job.NotBefore = StateWaiting, retryAt
						}
						if job.CancellationRequested {
							job.State = StateCancelled
						}
						job.FailureClass, job.SafeError = FailureDependency, "Synthetic failure."
					}
				}
				if leased && operation.kind != "heartbeat" {
					job := &model[operation.job]
					job.LeaseOwner, job.LeaseExpiresAt, job.UpdatedAt = "", nil, now
				}
			}
			actualCode := catalog.ErrorCode("")
			if err != nil {
				actualCode = catalog.ErrorCodeOf(err)
			}
			if actualCode != wantCode {
				rt.Fatalf("step %d %s code=%q want=%q err=%v", step, operation.kind, actualCode, wantCode, err)
			}
			if wantCode != "" && got.ID != "" {
				rt.Fatal("rejected transition disclosed a Job")
			}
			if len(sink.records) != expectedEvidence {
				rt.Fatalf("step %d %s audit count=%d want=%d", step, operation.kind, len(sink.records), expectedEvidence)
			}
			if got.ID != "" {
				found := false
				for _, expected := range model {
					if expected.ID == got.ID {
						found = true
						if !reflect.DeepEqual(got, expected) {
							rt.Fatalf("step %d returned Job differs from model: got=%#v want=%#v", step, got, expected)
						}
					}
				}
				if !found {
					rt.Fatal("transition returned an unmodeled Job")
				}
			}
			for _, expected := range model {
				retained, getErr := store.Get(t.Context(), expected.TenantID, expected.ID)
				if getErr != nil || !reflect.DeepEqual(retained, expected) {
					rt.Fatalf("step %d %s retained=%#v expected=%#v err=%v", step, operation.kind, retained, expected, getErr)
				}
				if _, denied := store.Get(t.Context(), "other-tenant", expected.ID); catalog.ErrorCodeOf(denied) != catalog.CodeNotFound {
					rt.Fatal("cross-tenant read disclosed a Job")
				}
			}
		}
	})
}
