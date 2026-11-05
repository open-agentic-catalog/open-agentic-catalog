package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestMemoryClaimGivesFixedEqualPriorityWorkBoundedProgressDuringArrivals(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	enqueue := func(idValue, tenantID string, createdAt time.Time) {
		t.Helper()
		_, existing, err := store.Enqueue(context.Background(), Job{ID: idValue, TenantID: tenantID, Kind: "test", State: StateQueued, Priority: 10, NotBefore: now, AttemptLimit: 1, DeduplicationKey: idValue, CreatedAt: createdAt, UpdatedAt: createdAt})
		if err != nil || existing {
			t.Fatalf("enqueue %s existing=%t, %v", idValue, existing, err)
		}
	}
	for index := 0; index < 4; index++ {
		enqueue(fmt.Sprintf("tenant-a-initial-%d", index), "tenant-a", now.Add(time.Duration(index)*time.Nanosecond))
	}
	enqueue("tenant-b-fixed", "tenant-b", now.Add(4*time.Nanosecond))
	claimedAt := -1
	for turn := 0; turn < 5; turn++ {
		claimed, err := store.Claim(context.Background(), fmt.Sprintf("worker-%d", turn), now.Add(time.Second), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if claimed.ID == "tenant-b-fixed" {
			claimedAt = turn
		}
		if _, err := store.Complete(context.Background(), claimed.TenantID, claimed.ID, claimed.LeaseOwner, now.Add(time.Second), json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		enqueue(fmt.Sprintf("tenant-a-arrival-%d", turn), "tenant-a", now.Add(time.Duration(10+turn)*time.Nanosecond))
	}
	if claimedAt != 4 {
		t.Fatalf("fixed tenant-b Job claimed on turn %d, want 4", claimedAt)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Claim(cancelled, "worker", now.Add(time.Second), time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled claim returned %v", err)
	}
}

func TestValidateActionablePayloadUsesCurrentWorkerContracts(t *testing.T) {
	t.Parallel()
	sourceID := "00000000-0000-7000-8000-000000000001"
	for name, job := range map[string]Job{
		"automatic source":    {Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":2,"trigger":"automatic"}`)},
		"single-host publish": {Kind: "publication.generate", PayloadVersion: 1, Payload: json.RawMessage(`{}`)},
		"named-host publish":  {Kind: "publication.generate", PayloadVersion: 1, Payload: json.RawMessage(`{"host":"catalog.example.com"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateActionablePayload(job); err != nil {
				t.Fatalf("valid actionable payload returned %v", err)
			}
		})
	}
	for name, job := range map[string]Job{
		"obsolete source version": {Kind: "source.sync", PayloadVersion: 1, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1,"trigger":"manual"}`)},
		"unknown kind":            {Kind: "future.work", PayloadVersion: 1, Payload: json.RawMessage(`{}`)},
		"source version":          {Kind: "source.sync", PayloadVersion: 3, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1,"trigger":"manual"}`)},
		"source trigger":          {Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1,"trigger":"source.sync"}`)},
		"source unknown field":    {Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1,"trigger":"manual","secret":"x"}`)},
		"source duplicate key":    {Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceId":"` + sourceID + `","sourceRevision":1,"trigger":"manual"}`)},
		"publish version":         {Kind: "publication.generate", PayloadVersion: 2, Payload: json.RawMessage(`{}`)},
		"publish unknown field":   {Kind: "publication.generate", PayloadVersion: 1, Payload: json.RawMessage(`{"host":"catalog.example.com","secret":"x"}`)},
		"publish oversized host":  {Kind: "publication.generate", PayloadVersion: 1, Payload: json.RawMessage(`{"host":"` + strings.Repeat("x", 254) + `"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateActionablePayload(job); err == nil {
				t.Fatal("invalid actionable payload was accepted")
			}
		})
	}
}

func TestMemoryEnqueueRequiresRetainedTerminalRetryParent(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	child := Job{ID: "child", TenantID: "tenant-a", Kind: "test", ParentJobID: "missing", State: StateQueued, DeduplicationKey: "child"}
	if _, _, err := store.Enqueue(context.Background(), child); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing retry parent returned %v", err)
	}
	store.jobs["cross-tenant"] = Job{ID: "cross-tenant", TenantID: "tenant-b", State: StateFailed}
	child.ParentJobID = "cross-tenant"
	if _, _, err := store.Enqueue(context.Background(), child); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant retry parent returned %v", err)
	}
	store.jobs["active"] = Job{ID: "active", TenantID: "tenant-a", State: StateRunning}
	child.ParentJobID = "active"
	if _, _, err := store.Enqueue(context.Background(), child); catalog.ErrorCodeOf(err) != catalog.CodeInvalidTransition {
		t.Fatalf("active retry parent returned %v", err)
	}
	store.jobs["failed"] = Job{ID: "failed", TenantID: "tenant-a", State: StateFailed}
	child.ParentJobID = "failed"
	if enqueued, existing, err := store.Enqueue(context.Background(), child); err != nil || existing || enqueued.ParentJobID != "failed" {
		t.Fatalf("terminal retry parent enqueue = %#v existing=%t, %v", enqueued, existing, err)
	}
}

type heartbeatFailureStore struct {
	*MemoryStore
	err   error
	calls atomic.Int32
}

type failFailureStore struct {
	Store
	err error
}

func (s failFailureStore) Fail(context.Context, string, string, string, time.Time, FailureClass, string, time.Time) (Job, error) {
	return Job{}, s.err
}

type recordingJobObserver struct {
	started  []string
	finished [][2]string
	results  []AttemptResult
	retries  []jobRetryObservation
}

type jobRetryObservation struct {
	kind  string
	delay time.Duration
}

type failingJobEvidence struct{ err error }

func (f failingJobEvidence) AppendAuditEvent(context.Context, audit.Record) error { return f.err }

type recordingJobEvidence struct{ records []audit.Record }

func (r *recordingJobEvidence) AppendAuditEvent(_ context.Context, record audit.Record) error {
	r.records = append(r.records, record)
	return nil
}

func TestJobAuditFinalizersRejectUnboundOrInconsistentEvidence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	actor := identity.Actor{
		TenantID:     "00000000-0000-7000-8000-000000000001",
		Subject:      "fixture",
		RealmID:      "00000000-0000-7000-8000-000000000002",
		MembershipID: "00000000-0000-7000-8000-000000000003",
		ActorType:    identity.ActorHuman,
	}
	service := &Service{ids: id.Generator{}}
	jobID := "00000000-0000-7000-8000-000000000004"
	sourceID := "00000000-0000-7000-8000-000000000005"
	record, err := service.newAuditRecord(context.Background(), actor, audit.ActionSourceSyncRequested, "federation_source", sourceID, "", sourceSyncRequestFacts{SourceRevision: 2, JobID: jobID, Trigger: "manual"}, now)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: jobID, TenantID: actor.TenantID, Kind: "source.sync", Actor: actor.MembershipID, PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":2,"trigger":"manual"}`), State: StateQueued, AttemptLimit: 5, DeduplicationKey: "source.sync:" + sourceID + ":2", CreatedAt: now, UpdatedAt: now}
	finalized, err := FinalizeEnqueueEvidence(record, job, true)
	if err != nil {
		t.Fatal(err)
	}
	facts, _ := finalized.Details["facts"].(map[string]any)
	if facts["deduplicated"] != true || facts["jobId"] != jobID || finalized.AfterDigest == "" {
		t.Fatalf("finalized enqueue evidence = %#v", finalized)
	}
	automaticJob := cloneJob(job)
	automaticJob.Actor = audit.InitiatorSourceMaintainer
	automaticJob.Payload = json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":2,"trigger":"automatic"}`)
	automaticFinalized, err := FinalizeEnqueueEvidence(record, automaticJob, true)
	if err != nil {
		t.Fatal(err)
	}
	automaticFacts, _ := automaticFinalized.Details["facts"].(map[string]any)
	if automaticFacts["trigger"] != "manual" || automaticFacts["deduplicated"] != true || automaticFacts["jobId"] != jobID {
		t.Fatalf("manual request reused by automatic Job = %#v", automaticFinalized)
	}
	for name, inconsistentJob := range map[string]Job{
		"automatic Job with membership initiator": func() Job {
			value := cloneJob(automaticJob)
			value.Actor = actor.MembershipID
			return value
		}(),
		"manual Job with automatic initiator": func() Job {
			value := cloneJob(job)
			value.Actor = audit.InitiatorSourceMaintainer
			return value
		}(),
		"manual Job with unstable initiator": func() Job {
			value := cloneJob(job)
			value.Actor = "external-subject"
			return value
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := FinalizeEnqueueEvidence(record, inconsistentJob, true); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("misattributed reused synchronization Job returned %v", err)
			}
		})
	}
	if _, err := FinalizeEnqueueEvidence(record, automaticJob, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("new manual request accepted an automatic Job payload: %v", err)
	}
	if _, err := FinalizeEnqueueEvidence(audit.Record{}, job, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("empty enqueue evidence = %v", err)
	}

	other, err := audit.NewRecord("00000000-0000-7000-8000-000000000006", actor.TenantID, audit.Principal{ID: actor.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: actor.RealmID}, "namespace.created", "namespace", actor.TenantID, "", audit.Execution{}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FinalizeEnqueueEvidence(other, job, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("unrelated enqueue evidence = %v", err)
	}

	inconsistent := record
	inconsistent.Details = make(map[string]any, len(record.Details))
	for name, value := range record.Details {
		inconsistent.Details[name] = value
	}
	inconsistent.Details["facts"] = map[string]any{"sourceRevision": int64(2), "jobId": jobID, "trigger": "automatic", "deduplicated": false}
	if _, err := FinalizeEnqueueEvidence(inconsistent, job, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("inconsistent enqueue evidence returned %v", err)
	}
	wrongTenant := record
	wrongTenant.TenantID = "00000000-0000-7000-8000-000000000099"
	if _, err := FinalizeEnqueueEvidence(wrongTenant, job, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("cross-tenant enqueue evidence returned %v", err)
	}
	missingDetails := record
	missingDetails.Details = nil
	if _, err := FinalizeEnqueueEvidence(missingDetails, job, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("missing enqueue details returned %v", err)
	}
	for name, mutate := range map[string]func(*Job){
		"payload source": func(value *Job) {
			value.Payload = json.RawMessage(`{"sourceId":"00000000-0000-7000-8000-000000000099","sourceRevision":2,"trigger":"manual"}`)
		},
		"payload revision": func(value *Job) {
			value.Payload = json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":3,"trigger":"manual"}`)
		},
		"payload trigger": func(value *Job) {
			value.Payload = json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":2,"trigger":"automatic"}`)
		},
		"payload version": func(value *Job) { value.PayloadVersion = 1 },
		"payload member": func(value *Job) {
			value.Payload = json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":2,"trigger":"manual","extra":true}`)
		},
		"duplicate payload member": func(value *Job) {
			value.Payload = json.RawMessage(`{"sourceId":"` + sourceID + `","sourceId":"` + sourceID + `","sourceRevision":2,"trigger":"manual"}`)
		},
		"malformed payload": func(value *Job) {
			value.Payload = json.RawMessage(`{"sourceId":`)
		},
		"deduplication key": func(value *Job) { value.DeduplicationKey = "wrong" },
		"new actor":         func(value *Job) { value.Actor = "00000000-0000-7000-8000-000000000099" },
	} {
		t.Run(name, func(t *testing.T) {
			inconsistentJob := cloneJob(job)
			mutate(&inconsistentJob)
			if _, err := FinalizeEnqueueEvidence(record, inconsistentJob, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("mis-bound synchronization Job returned %v", err)
			}
		})
	}

	retryJob := job
	retryJob.ID = "00000000-0000-7000-8000-000000000007"
	retryJob.ParentJobID = job.ID
	retryRecord, err := service.newAuditRecord(context.Background(), actor, audit.ActionJobRetried, "job", job.ID, "Retry reviewed work", jobRetryFacts{Kind: job.Kind, BeforeState: StateFailed, AfterState: StateQueued, ParentJobID: job.ID, ReplacementJobID: retryJob.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if finalized, err := FinalizeEnqueueEvidence(retryRecord, retryJob, false); err != nil || finalized.AfterDigest == "" {
		t.Fatalf("retry enqueue evidence = %#v, %v", finalized, err)
	}
	wrongRetryTarget := retryRecord
	wrongRetryTarget.TargetID = "00000000-0000-7000-8000-000000000099"
	if _, err := FinalizeEnqueueEvidence(wrongRetryTarget, retryJob, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("mis-bound retry evidence returned %v", err)
	}
	wrongRetryReplacement := retryRecord
	wrongRetryReplacement.Details = make(map[string]any, len(retryRecord.Details))
	for name, value := range retryRecord.Details {
		wrongRetryReplacement.Details[name] = value
	}
	wrongRetryReplacement.Details["facts"] = map[string]any{"kind": job.Kind, "beforeState": string(StateFailed), "afterState": string(StateQueued), "parentJobId": job.ID, "replacementJobId": "00000000-0000-7000-8000-000000000099"}
	if _, err := FinalizeEnqueueEvidence(wrongRetryReplacement, retryJob, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("mis-bound retry replacement returned %v", err)
	}
	wrongRetryKind := retryJob
	wrongRetryKind.Kind = "publication.generate"
	if _, err := FinalizeEnqueueEvidence(retryRecord, wrongRetryKind, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("mis-bound retry kind returned %v", err)
	}
	wrongRetryActor := retryJob
	wrongRetryActor.Actor = "00000000-0000-7000-8000-000000000099"
	if _, err := FinalizeEnqueueEvidence(retryRecord, wrongRetryActor, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("misattributed retry Job returned %v", err)
	}
	if _, err := FinalizeEnqueueEvidence(retryRecord, retryJob, true); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("deduplicated retry evidence returned %v", err)
	}

	cancelRecord, err := service.newAuditRecord(context.Background(), actor, audit.ActionJobCancellationRequested, "job", jobID, "Cancel obsolete work", jobCancellationFacts{Kind: job.Kind, BeforeState: StateQueued, AfterState: StateCancelled}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FinalizeCancellationEvidence(audit.Record{}, job, job); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("empty cancellation evidence = %v", err)
	}
	before := job
	before.State = StateRunning
	after := before
	after.State = StateCancelled
	if _, err := FinalizeCancellationEvidence(cancelRecord, before, after); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("inconsistent cancellation evidence returned %v", err)
	}
	matchingAfter := before
	matchingAfter.CancellationRequested = true
	matchingCancel, err := service.newAuditRecord(context.Background(), actor, audit.ActionJobCancellationRequested, "job", jobID, "Cancel obsolete work", cancellationFacts(before, matchingAfter), now)
	if err != nil {
		t.Fatal(err)
	}
	if finalized, err := FinalizeCancellationEvidence(matchingCancel, before, matchingAfter); err != nil || finalized.BeforeDigest == "" || finalized.AfterDigest == "" {
		t.Fatalf("matching cancellation evidence = %#v, %v", finalized, err)
	}
	wrongCancelTarget := matchingCancel
	wrongCancelTarget.TargetID = "00000000-0000-7000-8000-000000000099"
	if _, err := FinalizeCancellationEvidence(wrongCancelTarget, before, matchingAfter); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("mis-bound cancellation evidence returned %v", err)
	}
	missingCancelDetails := matchingCancel
	missingCancelDetails.Details = nil
	if _, err := FinalizeCancellationEvidence(missingCancelDetails, before, matchingAfter); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("missing cancellation details returned %v", err)
	}
	if _, err := FinalizeCancellationEvidence(other, before, matchingAfter); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("unrelated cancellation evidence returned %v", err)
	}
	otherAfter := matchingAfter
	otherAfter.ID = "00000000-0000-7000-8000-000000000099"
	if _, err := FinalizeCancellationEvidence(matchingCancel, before, otherAfter); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("cross-Job cancellation evidence returned %v", err)
	}
}

func TestJobAuditConstructionAndFactConversionFailuresAreSafe(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	actor := identity.Actor{
		TenantID:     "00000000-0000-7000-8000-000000000001",
		Subject:      "fixture",
		RealmID:      "00000000-0000-7000-8000-000000000002",
		MembershipID: "00000000-0000-7000-8000-000000000003",
		ActorType:    identity.ActorHuman,
	}
	service := &Service{ids: id.Generator{}}
	if record, err := service.newAuditRecord(context.Background(), actor, "namespace.created", "namespace", actor.TenantID, "", func() {}, now); err == nil || record.ID != "" {
		t.Fatalf("unencodable Job audit facts = %#v, %v", record, err)
	}
	service.ids = id.Generator{Reader: bytes.NewReader(nil)}
	if record, err := service.newAuditRecord(context.Background(), actor, "namespace.created", "namespace", actor.TenantID, "", nil, now); err == nil || record.ID != "" {
		t.Fatalf("Job audit identifier failure = %#v, %v", record, err)
	}
	service.ids = id.Generator{}
	if record, err := service.newAuditRecord(context.Background(), actor, "audit.unknown", "namespace", actor.TenantID, "", nil, now); err == nil || record.ID != "" {
		t.Fatalf("invalid Job audit action = %#v, %v", record, err)
	}

	for _, testCase := range []struct {
		value any
		want  int64
	}{
		{value: json.Number("7"), want: 7},
		{value: int64(8), want: 8},
		{value: int(9), want: 9},
		{value: "10", want: 0},
	} {
		record := audit.Record{Details: map[string]any{"facts": map[string]any{"value": testCase.value}}}
		if got := factInt64(record, "value"); got != testCase.want {
			t.Fatalf("factInt64(%T(%v)) = %d, want %d", testCase.value, testCase.value, got, testCase.want)
		}
	}
	if got := factString(audit.Record{}, "missing"); got != "" {
		t.Fatalf("missing string fact = %q", got)
	}
}

func TestAuditSensitiveJobCommandFailuresDoNotChangeState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	actor := identity.Actor{
		TenantID:     "00000000-0000-7000-8000-000000000001",
		Subject:      "fixture",
		Roles:        []identity.Role{identity.RoleSourceAdmin},
		RealmID:      "00000000-0000-7000-8000-000000000002",
		MembershipID: "00000000-0000-7000-8000-000000000003",
		ActorType:    identity.ActorHuman,
	}
	ctx := identityfixture.WithActor(context.Background(), actor)
	store := NewMemoryStore()
	service := &Service{store: store, clock: clock.NewFake(now), ids: id.Generator{Reader: bytes.NewReader(nil)}, auditEvidence: true}
	input := EnqueueInput{Kind: "source.sync", Payload: json.RawMessage("{}"), DeduplicationKey: "source-1"}
	if job, existing, err := service.Enqueue(ctx, input); err == nil || existing || job.ID != "" || len(store.jobs) != 0 {
		t.Fatalf("Job identifier failure = %#v, %t, %v", job, existing, err)
	}

	service.ids = id.Generator{}
	input.SourceSyncAudit = &SourceSyncRequestAudit{SourceID: "not-a-uuid", SourceRevision: 1, Trigger: "manual"}
	if job, existing, err := service.Enqueue(ctx, input); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || existing || job.ID != "" || len(store.jobs) != 0 {
		t.Fatalf("invalid synchronization attribution = %#v, %t, %v", job, existing, err)
	}

	service.ids = id.Generator{Reader: bytes.NewReader(make([]byte, 16))}
	input.SourceSyncAudit.SourceID = "00000000-0000-7000-8000-000000000004"
	if job, existing, err := service.Enqueue(ctx, input); err == nil || existing || job.ID != "" || len(store.jobs) != 0 {
		t.Fatalf("audit identifier failure = %#v, %t, %v", job, existing, err)
	}

	corruptID := "00000000-0000-7000-8000-000000000010"
	store.jobs[corruptID] = Job{ID: corruptID, TenantID: actor.TenantID, Kind: "source.sync", State: State("corrupt"), CreatedAt: now, UpdatedAt: now}
	service.ids = id.Generator{}
	if job, err := service.Cancel(ctx, corruptID, "Cancel corrupt work"); catalog.ErrorCodeOf(err) != catalog.CodeInternal || job.ID != "" || store.jobs[corruptID].State != State("corrupt") {
		t.Fatalf("corrupt cancellation = %#v, %v", job, err)
	}

	failedID := "00000000-0000-7000-8000-000000000011"
	store.jobs[failedID] = Job{ID: failedID, TenantID: actor.TenantID, Kind: "source.sync", State: StateFailed, AttemptLimit: 3, CreatedAt: now, UpdatedAt: now}
	if job, err := service.Retry(ctx, failedID, ""); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || job.ID != "" || len(store.jobs) != 2 {
		t.Fatalf("reasonless retry = %#v, %v", job, err)
	}
	service.ids = id.Generator{Reader: bytes.NewReader(nil)}
	if job, err := service.Retry(ctx, failedID, "Retry reviewed work"); catalog.ErrorCodeOf(err) != catalog.CodeInternal || job.ID != "" || len(store.jobs) != 2 {
		t.Fatalf("retry identifier failure = %#v, %v", job, err)
	}
	if job, err := service.Cancel(ctx, failedID, ""); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || job.ID != "" {
		t.Fatalf("reasonless cancellation = %#v, %v", job, err)
	}
	if job, err := service.Cancel(ctx, failedID, "Cancel reviewed work"); catalog.ErrorCodeOf(err) != catalog.CodeInternal || job.ID != "" {
		t.Fatalf("cancellation audit identifier failure = %#v, %v", job, err)
	}
	service.ids = id.Generator{Reader: bytes.NewReader(make([]byte, 16))}
	if job, err := service.Retry(ctx, failedID, "Retry reviewed work"); catalog.ErrorCodeOf(err) != catalog.CodeInternal || job.ID != "" || len(store.jobs) != 2 || store.jobs[failedID].State != StateFailed {
		t.Fatalf("retry audit identifier failure changed state = %#v, %v", job, err)
	}
}

func TestManualJobCommandsRetainOneBoundedAuditRecordPerCommand(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	evidence := &recordingJobEvidence{}
	store := NewMemoryStore(evidence)
	service, err := NewService(store, clock.NewFake(now), id.Generator{}, WithAuditEvidence())
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{
		TenantID:     "00000000-0000-7000-8000-000000000001",
		Subject:      "external-subject",
		Roles:        []identity.Role{identity.RoleSourceAdmin},
		RealmID:      "00000000-0000-7000-8000-000000000002",
		MembershipID: "00000000-0000-7000-8000-000000000003",
		ActorType:    identity.ActorHuman,
	}
	ctx := identityfixture.WithActor(context.Background(), actor)
	ctx = audit.WithExecution(ctx, audit.Execution{RequestID: "job-command-request", AuthorizationDigest: actor.AuthorizationDigest()})
	sourceID := "00000000-0000-7000-8000-000000000004"
	syncInput := EnqueueInput{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":2,"trigger":"manual"}`), DeduplicationKey: "source.sync:" + sourceID + ":2", SourceSyncAudit: &SourceSyncRequestAudit{SourceID: sourceID, SourceRevision: 2, Trigger: "manual"}}
	job, existing, err := service.Enqueue(ctx, syncInput)
	if err != nil || existing {
		t.Fatalf("source synchronization enqueue = %#v, %t, %v", job, existing, err)
	}
	reused, existing, err := service.Enqueue(ctx, syncInput)
	if err != nil || !existing || reused.ID != job.ID {
		t.Fatalf("source synchronization reuse = %#v, %t, %v", reused, existing, err)
	}
	if cancelled, err := service.Cancel(ctx, job.ID, "Cancel the obsolete synchronization"); err != nil || cancelled.State != StateCancelled {
		t.Fatalf("Job cancellation = %#v, %v", cancelled, err)
	}
	if cancelled, err := service.Cancel(ctx, job.ID, "Confirm the terminal cancellation"); err != nil || cancelled.State != StateCancelled {
		t.Fatalf("terminal Job cancellation = %#v, %v", cancelled, err)
	}

	failed := Job{ID: "00000000-0000-7000-8000-000000000010", TenantID: actor.TenantID, Kind: "source.sync", Actor: actor.MembershipID, PayloadVersion: 1, Payload: json.RawMessage(`{}`), State: StateFailed, FailureClass: FailurePermanentUpstream, AttemptLimit: 3, DeduplicationKey: "failed-source-sync", CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.Enqueue(ctx, failed); err != nil {
		t.Fatal(err)
	}
	retry, err := service.Retry(ctx, failed.ID, "Retry after reviewing the source")
	if err != nil || retry.ParentJobID != failed.ID || retry.State != StateQueued {
		t.Fatalf("Job retry = %#v, %v", retry, err)
	}

	wantActions := []string{audit.ActionSourceSyncRequested, audit.ActionSourceSyncRequested, audit.ActionJobCancellationRequested, audit.ActionJobCancellationRequested, audit.ActionJobRetried}
	if len(evidence.records) != len(wantActions) {
		t.Fatalf("Job command evidence count = %d, want %d: %#v", len(evidence.records), len(wantActions), evidence.records)
	}
	for index, want := range wantActions {
		record := evidence.records[index]
		if record.Action != want || record.Actor != actor.MembershipID || record.RequestID != "job-command-request" {
			t.Fatalf("Job command evidence %d = %#v", index, record)
		}
	}
	firstFacts, ok := evidence.records[0].Details["facts"].(map[string]any)
	if !ok {
		t.Fatalf("first synchronization request facts = %#v", evidence.records[0].Details["facts"])
	}
	reusedFacts, ok := evidence.records[1].Details["facts"].(map[string]any)
	if !ok {
		t.Fatalf("reused synchronization request facts = %#v", evidence.records[1].Details["facts"])
	}
	if firstFacts["deduplicated"] != false || reusedFacts["deduplicated"] != true || firstFacts["jobId"] != job.ID || reusedFacts["jobId"] != job.ID {
		t.Fatalf("source synchronization request facts = first %#v reused %#v", firstFacts, reusedFacts)
	}
	cancelFacts, ok := evidence.records[2].Details["facts"].(map[string]any)
	if !ok {
		t.Fatalf("cancellation facts = %#v", evidence.records[2].Details["facts"])
	}
	terminalFacts, ok := evidence.records[3].Details["facts"].(map[string]any)
	if !ok {
		t.Fatalf("terminal cancellation facts = %#v", evidence.records[3].Details["facts"])
	}
	if cancelFacts["beforeState"] != "queued" || cancelFacts["afterState"] != "cancelled" || terminalFacts["beforeState"] != "cancelled" || terminalFacts["afterState"] != "cancelled" {
		t.Fatalf("Job cancellation facts = transition %#v terminal %#v", cancelFacts, terminalFacts)
	}
}

func TestManualSynchronizationRequestReusesEquivalentAutomaticJob(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 30, 0, 0, time.UTC)
	evidence := &recordingJobEvidence{}
	store := NewMemoryStore(evidence)
	service, err := NewService(store, clock.NewFake(now), id.Generator{}, WithAuditEvidence())
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "external-subject", Roles: []identity.Role{identity.RoleSourceAdmin}, RealmID: "00000000-0000-7000-8000-000000000002", MembershipID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman}
	sourceID := "00000000-0000-7000-8000-000000000004"
	automatic := Job{ID: "00000000-0000-7000-8000-000000000005", TenantID: actor.TenantID, Kind: "source.sync", Actor: audit.InitiatorSourceMaintainer, PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":2,"trigger":"automatic"}`), State: StateQueued, NotBefore: now, AttemptLimit: 5, DeduplicationKey: "source.sync:" + sourceID + ":2", CreatedAt: now, UpdatedAt: now}
	if _, existing, err := store.Enqueue(context.Background(), automatic); err != nil || existing {
		t.Fatalf("seed automatic synchronization = %t, %v", existing, err)
	}
	ctx := identityfixture.WithActor(context.Background(), actor)
	request := EnqueueInput{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":2,"trigger":"manual"}`), DeduplicationKey: automatic.DeduplicationKey, SourceSyncAudit: &SourceSyncRequestAudit{SourceID: sourceID, SourceRevision: 2, Trigger: "manual"}}
	reused, existing, err := service.Enqueue(ctx, request)
	if err != nil || !existing || reused.ID != automatic.ID || len(evidence.records) != 1 {
		t.Fatalf("manual synchronization reuse = %#v, %t, %v, evidence %#v", reused, existing, err, evidence.records)
	}
	facts, _ := evidence.records[0].Details["facts"].(map[string]any)
	if facts["trigger"] != "manual" || facts["deduplicated"] != true || facts["jobId"] != automatic.ID {
		t.Fatalf("manual synchronization request evidence = %#v", evidence.records[0])
	}
}

func TestJobOperatorEvidenceFailureRollsBackMemoryTransitions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	sentinel := errors.New("audit unavailable")
	store := NewMemoryStore(failingJobEvidence{err: sentinel})
	service, err := NewService(store, clock.NewFake(now), id.Generator{}, WithAuditEvidence())
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{
		TenantID:     "00000000-0000-7000-8000-000000000001",
		Subject:      "external-subject",
		Roles:        []identity.Role{identity.RoleSourceAdmin},
		RealmID:      "00000000-0000-7000-8000-000000000002",
		MembershipID: "00000000-0000-7000-8000-000000000003",
		ActorType:    identity.ActorHuman,
	}
	ctx := identityfixture.WithActor(context.Background(), actor)
	sourceID := "00000000-0000-7000-8000-000000000004"
	if job, _, err := service.Enqueue(ctx, EnqueueInput{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1,"trigger":"manual"}`), DeduplicationKey: "source.sync:" + sourceID + ":1", SourceSyncAudit: &SourceSyncRequestAudit{SourceID: sourceID, SourceRevision: 1, Trigger: "manual"}}); !errors.Is(err, sentinel) || job.ID != "" || len(store.jobs) != 0 {
		t.Fatalf("failed enqueue changed state: job %#v jobs %#v err %v", job, store.jobs, err)
	}

	failed := Job{ID: "00000000-0000-7000-8000-000000000010", TenantID: actor.TenantID, Kind: "source.sync", Actor: actor.MembershipID, PayloadVersion: 1, Payload: json.RawMessage(`{}`), State: StateFailed, AttemptLimit: 3, DeduplicationKey: "failed", CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.Enqueue(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if retried, err := service.Retry(ctx, failed.ID, "Retry the reviewed failed synchronization"); !errors.Is(err, sentinel) || retried.ID != "" || len(store.jobs) != 1 {
		t.Fatalf("failed retry changed state: retry %#v jobs %#v err %v", retried, store.jobs, err)
	}

	queued := Job{ID: "00000000-0000-7000-8000-000000000011", TenantID: actor.TenantID, Kind: "source.sync", Actor: actor.MembershipID, PayloadVersion: 1, Payload: json.RawMessage(`{}`), State: StateQueued, AttemptLimit: 3, DeduplicationKey: "queued", CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.Enqueue(ctx, queued); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := service.Cancel(ctx, queued.ID, "Cancel the obsolete synchronization"); !errors.Is(err, sentinel) || cancelled.ID != "" {
		t.Fatalf("failed cancellation = %#v, %v", cancelled, err)
	}
	retained, err := store.Get(ctx, actor.TenantID, queued.ID)
	if err != nil || retained.State != StateQueued || retained.CancellationRequested {
		t.Fatalf("failed cancellation changed Job state: %#v, %v", retained, err)
	}
}

func (o *recordingJobObserver) JobStarted(kind string) { o.started = append(o.started, kind) }

func (o *recordingJobObserver) JobFinished(_ context.Context, kind string, result AttemptResult) {
	o.finished = append(o.finished, [2]string{kind, result.Outcome})
	o.results = append(o.results, result)
}

func (o *recordingJobObserver) JobRetryScheduled(kind string, delay time.Duration) {
	o.retries = append(o.retries, jobRetryObservation{kind, delay})
}

func TestWorkerReportsBoundedAttemptLifecycle(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	store := NewMemoryStore()
	service, err := NewService(store, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "operator", Roles: []identity.Role{identity.RoleTenantAdmin}})
	if _, _, err := service.Enqueue(ctx, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "observed-success"}); err != nil {
		t.Fatal(err)
	}
	observer := &recordingJobObserver{}
	worker, err := NewWorker(store, fakeClock, "observed-worker", time.Minute, time.Second, time.Hour, map[string]Handler{
		"source.sync": func(context.Context, Job, func(int) error) (any, error) { return map[string]bool{"ok": true}, nil },
	}, observer)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(observer.started) != 1 || observer.started[0] != "source.sync" || len(observer.finished) != 1 || observer.finished[0] != [2]string{"source.sync", "succeeded"} || len(observer.retries) != 0 || observer.results[0] != (AttemptResult{Outcome: "succeeded", Committed: true}) {
		t.Fatalf("worker observations = started %#v finished %#v", observer.started, observer.finished)
	}
	if _, err := NewWorker(store, fakeClock, "too-many-observers", time.Minute, time.Second, time.Hour, nil, observer, observer); err == nil {
		t.Fatal("worker accepted more than one observer")
	}

	if _, err := enqueueWorkerFixture(service, ctx, EnqueueInput{Kind: "unsupported.kind", Payload: json.RawMessage(`{}`), DeduplicationKey: "observed-unsupported"}); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(observer.finished) != 2 || observer.finished[1] != [2]string{"unsupported.kind", "unsupported"} || observer.results[1] != (AttemptResult{Outcome: "unsupported", FailureClass: FailureInvalidInput, Committed: true}) {
		t.Fatalf("unsupported observation = %#v", observer.finished)
	}

	failingStore := NewMemoryStore()
	failingService, err := NewService(failingStore, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enqueueWorkerFixture(failingService, ctx, EnqueueInput{Kind: "unsupported.kind", Payload: json.RawMessage(`{}`), DeduplicationKey: "unsupported-persistence-failure"}); err != nil {
		t.Fatal(err)
	}
	failureObserver := &recordingJobObserver{}
	worker, err = NewWorker(failFailureStore{Store: failingStore, err: catalog.E(catalog.CodeDependencyUnavailable, "database unavailable")}, fakeClock, "failure-worker", time.Minute, time.Second, time.Hour, nil, failureObserver)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("unsupported persistence failure = %v", err)
	}
	if len(failureObserver.finished) != 1 || failureObserver.finished[0] != [2]string{"unsupported.kind", "dependency_error"} || failureObserver.results[0].Committed {
		t.Fatalf("unsupported persistence observation = %#v", failureObserver.finished)
	}
}

func TestPublicViewExposesOnlyValidatedResultSummaries(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	base := Job{
		ID:               "00000000-0000-7000-8000-000000000001",
		TenantID:         "00000000-0000-7000-8000-000000000002",
		Kind:             "source.sync",
		Actor:            "private-actor",
		Payload:          json.RawMessage(`{"credential":"private"}`),
		State:            StateSucceeded,
		NotBefore:        now,
		AttemptLimit:     3,
		LeaseOwner:       "private-worker",
		DeduplicationKey: "private-deduplication-key",
		CreatedAt:        now,
		UpdatedAt:        now,
		Result:           json.RawMessage(`{"id":"00000000-0000-7000-8000-000000000003","sourceId":"00000000-0000-7000-8000-000000000004","state":"complete","private":"not exposed"}`),
	}
	view := PublicView(base)
	if view.Result == nil || view.Result.Type != "source_snapshot" || view.Result.SnapshotID != "00000000-0000-7000-8000-000000000003" || view.Result.SourceID != "00000000-0000-7000-8000-000000000004" || view.Result.State != "complete" {
		t.Fatalf("source result summary = %#v", view.Result)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-actor", "credential", "private-worker", "private-deduplication-key", "not exposed"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("private Job state leaked through the public view: %s", encoded)
		}
	}

	publicationJob := base
	publicationJob.Kind = "publication.generate"
	publicationJob.Result = json.RawMessage(`{"id":"00000000-0000-7000-8000-000000000005","host":"catalog.example.com","generation":7,"state":"candidate"}`)
	publicationView := PublicView(publicationJob)
	if publicationView.Result == nil || publicationView.Result.Type != "publication_snapshot" || publicationView.Result.Host != "catalog.example.com" || publicationView.Result.Generation != 7 || publicationView.Result.SnapshotID != "00000000-0000-7000-8000-000000000005" || publicationView.Result.State != "candidate" {
		t.Fatalf("publication result summary = %#v", publicationView.Result)
	}
	for _, result := range []string{
		`{"id":"not-a-uuid","host":"catalog.example.com","generation":7,"state":"candidate"}`,
		`{"id":"00000000-0000-7000-8000-000000000005","generation":7,"state":"candidate"}`,
		`{"id":"00000000-0000-7000-8000-000000000005","host":"catalog.example.com","generation":0,"state":"candidate"}`,
		`{"id":"00000000-0000-7000-8000-000000000005","host":"catalog.example.com","generation":7,"state":"running"}`,
	} {
		publicationJob.Result = json.RawMessage(result)
		if got := PublicView(publicationJob); got.Result != nil {
			t.Fatalf("invalid publication result exposed: %#v", got.Result)
		}
	}

	for _, host := range []string{strings.Repeat("a", 253), strings.Repeat("a", 254), strings.Repeat("é", 126) + "a", strings.Repeat("é", 127)} {
		candidate := publicationJob
		candidate.Result = json.RawMessage(fmt.Sprintf(`{"id":"00000000-0000-7000-8000-000000000005","host":%q,"generation":7,"state":"active"}`, host))
		got := PublicView(candidate)
		if len(host) <= 253 {
			want := ResultSummary{Type: "publication_snapshot", SnapshotID: "00000000-0000-7000-8000-000000000005", Host: host, Generation: 7, State: "active"}
			if got.Result == nil || *got.Result != want {
				t.Fatalf("valid host boundary lost summary: %+v", got.Result)
			}
		} else if got.Result != nil {
			t.Fatalf("oversized host exposed: %+v", got.Result)
		}
	}
	for _, extra := range []string{`"host":{}`, `"generation":"7"`, `"generation":1.0`} {
		candidate := base
		candidate.Result = append(json.RawMessage(nil), base.Result[:len(base.Result)-1]...)
		candidate.Result = append(candidate.Result, []byte(","+extra+"}")...)
		if got := PublicView(candidate); got.Result != nil {
			t.Fatalf("wrong unused scalar type exposed: %s", extra)
		}
	}

	invalidResults := []json.RawMessage{
		nil,
		json.RawMessage(`null`),
		json.RawMessage(`{`),
		json.RawMessage(`{"id":"not-a-uuid","sourceId":"00000000-0000-7000-8000-000000000004","state":"complete"}`),
		json.RawMessage(`{"id":"00000000-0000-7000-8000-000000000003","sourceId":"00000000-0000-7000-8000-000000000004","state":"running"}`),
	}
	for _, result := range invalidResults {
		candidate := base
		candidate.Result = result
		if got := PublicView(candidate); got.Result != nil {
			t.Fatalf("invalid result was exposed: %s as %#v", result, got.Result)
		}
	}
	unknown := base
	unknown.Kind = "unknown"
	if got := PublicView(unknown); got.Result != nil {
		t.Fatalf("unknown Job result was exposed: %#v", got.Result)
	}
}

func (s *heartbeatFailureStore) Heartbeat(context.Context, string, string, string, time.Time, time.Duration, int) (Job, error) {
	s.calls.Add(1)
	return Job{}, s.err
}

func TestJobDeduplicationCompletionRetryAndCancellation(t *testing.T) {
	fakeClock := clock.NewFake(time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC))
	store := NewMemoryStore()
	service, err := NewService(store, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "operator", Roles: []identity.Role{identity.RoleTenantAdmin}})
	input := EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{"value":1}`), DeduplicationKey: "same"}
	job, existing, err := service.Enqueue(ctx, input)
	if err != nil || existing {
		t.Fatalf("enqueue = %#v, existing=%v, err=%v", job, existing, err)
	}
	duplicate, existing, err := service.Enqueue(ctx, input)
	if err != nil || !existing || duplicate.ID != job.ID {
		t.Fatalf("dedupe = %#v, existing=%v, err=%v", duplicate, existing, err)
	}
	worker, err := NewWorker(store, fakeClock, "worker-one", time.Minute, time.Second, time.Hour, map[string]Handler{
		"source.sync": func(_ context.Context, _ Job, progress func(int) error) (any, error) {
			if err := progress(50); err != nil {
				return nil, err
			}
			return map[string]bool{"ok": true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	completed, err := service.Get(ctx, job.ID)
	if err != nil || completed.State != StateSucceeded || completed.Progress != 100 {
		t.Fatalf("completed job = %#v, %v", completed, err)
	}

	retryJob, _, err := service.Enqueue(ctx, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "retry", AttemptLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	retryWorker, err := NewWorker(store, fakeClock, "worker-two", time.Minute, time.Second, time.Hour, map[string]Handler{
		"source.sync": func(context.Context, Job, func(int) error) (any, error) {
			return nil, &HandlerError{Class: FailureTransientUpstream, Safe: "retry later", Cause: errors.New("upstream")}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := retryWorker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	waiting, err := service.Get(ctx, retryJob.ID)
	if err != nil || waiting.State != StateWaiting || waiting.FailureClass != FailureTransientUpstream {
		t.Fatalf("waiting job = %#v, %v", waiting, err)
	}
	retried, err := store.Claim(context.Background(), "worker-three", waiting.NotBefore, time.Minute)
	if err != nil || retried.ID != retryJob.ID || retried.State != StateRunning || retried.SafeError != "" || retried.FailureClass != "" {
		t.Fatalf("retried job = %#v, %v", retried, err)
	}

	failedJob, _, err := service.Enqueue(ctx, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{"attempt":1}`), DeduplicationKey: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	failedWorker, err := NewWorker(store, fakeClock, "worker-failed", time.Minute, time.Second, time.Hour, map[string]Handler{
		"source.sync": func(context.Context, Job, func(int) error) (any, error) {
			return nil, &HandlerError{Class: FailurePermanentUpstream, Safe: "permanent failure"}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := failedWorker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	retry, err := service.Retry(ctx, failedJob.ID, "Retry after reviewing the safe failure")
	if err != nil || retry.ParentJobID != failedJob.ID || retry.State != StateQueued || string(retry.Payload) != `{"attempt":1}` {
		t.Fatalf("manual retry = %#v, %v", retry, err)
	}

	cancelJob, _, err := service.Enqueue(ctx, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "cancel"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := service.Cancel(ctx, cancelJob.ID, "Cancel obsolete work")
	if err != nil || cancelled.State != StateCancelled {
		t.Fatalf("cancelled job = %#v, %v", cancelled, err)
	}
}

func TestWorkerHonorsConfiguredConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fakeClock := clock.NewFake(time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC))
		store := NewMemoryStore()
		service, err := NewService(store, fakeClock, id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "operator", Roles: []identity.Role{identity.RoleTenantAdmin}})
		for _, key := range []string{"one", "two"} {
			if _, _, err := service.Enqueue(ctx, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: key}); err != nil {
				t.Fatal(err)
			}
		}
		started := make(chan struct{}, 2)
		release := make(chan struct{})
		var active atomic.Int32
		var maximum atomic.Int32
		worker, err := NewWorker(store, fakeClock, "worker-parallel", time.Minute, time.Millisecond, time.Hour, map[string]Handler{
			"source.sync": func(ctx context.Context, _ Job, _ func(int) error) (any, error) {
				current := active.Add(1)
				defer active.Add(-1)
				for {
					prior := maximum.Load()
					if current <= prior || maximum.CompareAndSwap(prior, current) {
						break
					}
				}
				started <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return map[string]bool{"ok": true}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		runContext, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- worker.RunConcurrent(runContext, 2) }()
		synctest.Wait()
		if len(started) != 2 || active.Load() != 2 {
			t.Fatalf("parallel handlers started=%d active=%d, want 2 each", len(started), active.Load())
		}
		close(release)
		synctest.Wait()
		items, listErr := service.List(ctx, ListRequest{Limit: 10})
		if listErr != nil || len(items) != 2 || items[0].State != StateSucceeded || items[1].State != StateSucceeded {
			t.Fatalf("both parallel jobs must succeed: %#v, %v", items, listErr)
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("worker stopped with %v", err)
			}
		default:
			t.Fatal("parallel worker loops did not finish")
		}
		if active.Load() != 0 {
			t.Fatalf("active handlers after shutdown = %d", active.Load())
		}
		if maximum.Load() != 2 {
			t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
		}
	})
}

func TestWorkerRenewsLeaseWithoutProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fakeClock := clock.NewFake(time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC))
		store := NewMemoryStore()
		service, err := NewService(store, fakeClock, id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		tenantID := "00000000-0000-7000-8000-000000000001"
		actorContext := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "operator", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleReader}})
		job, _, err := service.Enqueue(actorContext, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "long"})
		if err != nil {
			t.Fatal(err)
		}
		started := make(chan struct{})
		release := make(chan struct{})
		leaseDuration := 90 * time.Millisecond
		worker, err := NewWorker(store, fakeClock, "worker-long", leaseDuration, time.Second, time.Hour, map[string]Handler{
			"source.sync": func(ctx context.Context, _ Job, _ func(int) error) (any, error) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return map[string]bool{"ok": true}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		runContext, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- worker.RunOnce(runContext) }()
		synctest.Wait()
		select {
		case <-started:
		default:
			t.Fatal("worker handler did not start")
		}
		running, err := store.Get(actorContext, job.TenantID, job.ID)
		if err != nil || running.LeaseExpiresAt == nil {
			t.Fatalf("running long job = %#v, %v", running, err)
		}
		originalExpiry := *running.LeaseExpiresAt
		fakeClock.Advance(leaseDuration / 3)
		// Advance the timer separately from the injected lease clock.
		time.Sleep(leaseDuration / 3)
		synctest.Wait()
		running, err = store.Get(actorContext, job.TenantID, job.ID)
		if err != nil || running.State != StateRunning || running.LeaseOwner != "worker-long" || running.Attempt != 1 || running.Progress != 0 || running.LeaseExpiresAt == nil || !running.LeaseExpiresAt.Equal(originalExpiry.Add(leaseDuration/3)) {
			t.Fatalf("lease was not renewed without progress: %#v, %v", running, err)
		}
		fakeClock.Advance(2*leaseDuration/3 + time.Nanosecond)
		if !fakeClock.Now().After(originalExpiry) {
			t.Fatalf("logical time %s did not pass original expiry %s", fakeClock.Now(), originalExpiry)
		}
		if _, err := store.Claim(context.Background(), "competing-worker", fakeClock.Now(), time.Second); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("competing claim after original lease window returned %v", err)
		}
		close(release)
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("renewing worker did not finish")
		}
		completed, err := store.Get(actorContext, job.TenantID, job.ID)
		if err != nil || completed.State != StateSucceeded || completed.Progress != 100 || completed.LeaseOwner != "" || completed.LeaseExpiresAt != nil || string(completed.Result) != `{"ok":true}` {
			t.Fatalf("completed long job = %#v, %v", completed, err)
		}
	})
}

func TestWorkerDetectsCancellationWithoutProgress(t *testing.T) {
	for _, returnSuccess := range []bool{false, true} {
		t.Run(fmt.Sprintf("handler-success-%t", returnSuccess), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				serviceClock := clock.NewFake(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC))
				store := NewMemoryStore()
				service, err := NewService(store, serviceClock, id.Generator{})
				if err != nil {
					t.Fatal(err)
				}
				tenantID := "00000000-0000-7000-8000-000000000001"
				actorContext := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "operator", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleReader}})
				job, _, err := service.Enqueue(actorContext, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "cancel-running"})
				if err != nil {
					t.Fatal(err)
				}
				started := make(chan struct{})
				worker, err := NewWorker(store, serviceClock, "worker-cancel", 300*time.Millisecond, time.Second, time.Hour, map[string]Handler{
					"source.sync": func(ctx context.Context, _ Job, _ func(int) error) (any, error) {
						close(started)
						<-ctx.Done()
						if returnSuccess {
							return map[string]bool{"ok": true}, nil
						}
						return nil, ctx.Err()
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				runContext, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- worker.RunOnce(runContext) }()
				synctest.Wait()
				select {
				case <-started:
				default:
					t.Fatal("worker handler did not start")
				}
				if _, err := service.Cancel(actorContext, job.ID, "Cancel long-running work"); err != nil {
					t.Fatal(err)
				}
				time.Sleep(100 * time.Millisecond)
				synctest.Wait()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatal("worker did not observe job cancellation")
				}
				cancelled, err := store.Get(actorContext, job.TenantID, job.ID)
				if err != nil || cancelled.State != StateCancelled || !cancelled.CancellationRequested || cancelled.FailureClass != FailureCancelled || len(cancelled.Result) != 0 || cancelled.LeaseOwner != "" || cancelled.LeaseExpiresAt != nil {
					t.Fatalf("cancelled running job = %#v, %v", cancelled, err)
				}
			})
		})
	}
}

func TestWorkerCancelsHandlerWhenLeaseHeartbeatFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		serviceClock := clock.NewFake(time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC))
		heartbeatErr := errors.New("lease heartbeat failed")
		store := &heartbeatFailureStore{MemoryStore: NewMemoryStore(), err: heartbeatErr}
		service, err := NewService(store, serviceClock, id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		actorContext := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "operator", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleReader}})
		job, _, err := service.Enqueue(actorContext, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "heartbeat-failure"})
		if err != nil {
			t.Fatal(err)
		}
		handlerCanceled := make(chan struct{})
		worker, err := NewWorker(store, serviceClock, "worker-heartbeat-failure", 30*time.Millisecond, time.Second, time.Hour, map[string]Handler{
			"source.sync": func(ctx context.Context, _ Job, _ func(int) error) (any, error) {
				<-ctx.Done()
				close(handlerCanceled)
				return nil, ctx.Err()
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		runContext, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- worker.RunOnce(runContext) }()
		synctest.Wait()
		time.Sleep(10 * time.Millisecond)
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, heartbeatErr) {
				t.Fatalf("heartbeat failure returned %v", err)
			}
		default:
			t.Fatal("worker did not finish after lease heartbeat failure")
		}
		select {
		case <-handlerCanceled:
		default:
			t.Fatal("handler was not canceled after lease heartbeat failure")
		}
		if store.calls.Load() == 0 {
			t.Fatal("worker did not attempt a lease heartbeat")
		}
		running, err := store.Get(actorContext, job.TenantID, job.ID)
		if err != nil || running.State != StateRunning || running.Attempt != 1 || running.LeaseOwner != "worker-heartbeat-failure" || running.LeaseExpiresAt == nil || !running.LeaseExpiresAt.Equal(serviceClock.Now().Add(30*time.Millisecond)) || running.CancellationRequested || len(running.Result) != 0 || running.FailureClass != "" {
			t.Fatalf("job after heartbeat failure = %#v, %v", running, err)
		}
	})
}

func TestWorkerShutdownAbandonsLeaseInsteadOfCancellingJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		serviceClock := clock.NewFake(time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC))
		store := NewMemoryStore()
		service, err := NewService(store, serviceClock, id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		tenantID := "00000000-0000-7000-8000-000000000001"
		actorContext := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "operator", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleReader}})
		job, _, err := service.Enqueue(actorContext, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "shutdown"})
		if err != nil {
			t.Fatal(err)
		}
		started := make(chan struct{})
		worker, err := NewWorker(store, serviceClock, "worker-shutdown", time.Second, time.Second, time.Hour, map[string]Handler{
			"source.sync": func(ctx context.Context, _ Job, _ func(int) error) (any, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		runContext, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- worker.RunOnce(runContext) }()
		synctest.Wait()
		select {
		case <-started:
		default:
			t.Fatal("worker handler did not start")
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("worker shutdown returned %v", err)
			}
		default:
			t.Fatal("worker did not finish during shutdown")
		}
		running, err := store.Get(actorContext, job.TenantID, job.ID)
		if err != nil || running.State != StateRunning || running.CancellationRequested || running.Attempt != 1 || running.LeaseOwner != "worker-shutdown" || running.LeaseExpiresAt == nil || !running.LeaseExpiresAt.Equal(serviceClock.Now().Add(time.Second)) || len(running.Result) != 0 || running.FailureClass != "" {
			t.Fatalf("job after worker shutdown = %#v, %v", running, err)
		}
		serviceClock.Advance(time.Second - time.Nanosecond)
		if _, err := store.Claim(t.Context(), "replacement-worker", serviceClock.Now(), time.Second); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("shutdown attempt reclaimed before expiry: %v", err)
		}
		serviceClock.Advance(time.Nanosecond)
		recovered, err := store.Claim(t.Context(), "replacement-worker", serviceClock.Now(), time.Second)
		if err != nil || recovered.ID != job.ID || recovered.TenantID != tenantID || recovered.State != StateRunning || recovered.Attempt != 2 || recovered.LeaseOwner != "replacement-worker" || recovered.CancellationRequested {
			t.Fatalf("shutdown attempt not recoverable at expiry: %#v, %v", recovered, err)
		}
	})
}

func TestWorkerCarriesOnlyStableJobInitiatorsIntoAuditContext(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name          string
		actor         string
		wantInitiator string
	}{
		{name: "Catalog membership", actor: "00000000-0000-7000-8000-000000000003", wantInitiator: "00000000-0000-7000-8000-000000000003"},
		{name: "automatic source maintainer", actor: audit.InitiatorSourceMaintainer, wantInitiator: audit.InitiatorSourceMaintainer},
		{name: "external subject", actor: "external-token-subject"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := NewMemoryStore()
			job := Job{ID: "00000000-0000-7000-8000-000000000010", TenantID: "00000000-0000-7000-8000-000000000001", Kind: "source.sync", Actor: test.actor, Payload: json.RawMessage(`{}`), State: StateQueued, NotBefore: now, AttemptLimit: 1, DeduplicationKey: "initiator", CreatedAt: now, UpdatedAt: now}
			if _, _, err := store.Enqueue(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			var execution audit.JobExecution
			worker, err := NewWorker(store, clock.NewFake(now), "initiator-worker", time.Minute, time.Second, time.Hour, map[string]Handler{
				"source.sync": func(ctx context.Context, _ Job, _ func(int) error) (any, error) {
					execution, _ = audit.JobExecutionFrom(ctx)
					return map[string]bool{"ok": true}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if execution.ID != job.ID || execution.Kind != job.Kind || execution.Attempt != 1 || execution.Initiator != test.wantInitiator {
				t.Fatalf("worker audit execution = %#v", execution)
			}
		})
	}
}

func TestJobReadsAndCommandsRequireCurrentAuthority(t *testing.T) {
	fakeClock := clock.NewFake(time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC))
	store := NewMemoryStore()
	service, err := NewService(store, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	sourceAdmin := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin, identity.RoleReader}})
	job, _, err := service.Enqueue(sourceAdmin, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "source-one"})
	if err != nil {
		t.Fatal(err)
	}
	withoutReader := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "authenticated"})
	if _, err := service.Get(withoutReader, job.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("job read without reader role returned %v", err)
	}
	reader := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	if _, err := service.Cancel(reader, job.ID, "Cancel unauthorized work"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("job cancellation without source authority returned %v", err)
	}
	cancelled, err := service.Cancel(sourceAdmin, job.ID, "Cancel source synchronization")
	if err != nil || cancelled.State != StateCancelled {
		t.Fatalf("authorized cancellation = %#v, %v", cancelled, err)
	}
}

func TestJobServiceRejectsInvalidCommandsAndPreservesScheduling(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	store := NewMemoryStore()
	if _, err := NewService(store, fakeClock, id.Generator{}, nil); err == nil {
		t.Fatal("nil Job service option accepted")
	}
	optionError := errors.New("option failed")
	if _, err := NewService(store, fakeClock, id.Generator{}, func(*Service) error { return optionError }); !errors.Is(err, optionError) {
		t.Fatalf("option error lost: %v", err)
	}
	for _, dependencies := range []struct {
		store Store
		clock Clock
		ids   IDGenerator
	}{
		{clock: fakeClock, ids: id.Generator{}},
		{store: store, ids: id.Generator{}},
		{store: store, clock: fakeClock},
	} {
		if _, err := NewService(dependencies.store, dependencies.clock, dependencies.ids); err == nil {
			t.Fatal("incomplete job service dependencies were accepted")
		}
	}
	service, err := NewService(store, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Enqueue(context.Background(), EnqueueInput{}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("anonymous enqueue returned %v", err)
	}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "operator", Roles: []identity.Role{identity.RoleReader, identity.RoleTenantAdmin}}
	actorContext := identityfixture.WithActor(context.Background(), actor)
	for name, input := range map[string]EnqueueInput{
		"missing kind":    {Payload: json.RawMessage(`{}`), DeduplicationKey: "key"},
		"invalid payload": {Kind: "test", Payload: json.RawMessage(`{`), DeduplicationKey: "key"},
		"attempt limit":   {Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "key", AttemptLimit: 21},
		"priority":        {Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "key", Priority: 101},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, enqueueErr := service.Enqueue(actorContext, input); catalog.ErrorCodeOf(enqueueErr) != catalog.CodeInvalidArgument {
				t.Fatalf("invalid enqueue returned %v", enqueueErr)
			}
		})
	}
	future := now.Add(time.Hour)
	scheduled, _, err := service.Enqueue(actorContext, EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "future", NotBefore: &future})
	if err != nil || !scheduled.NotBefore.Equal(future) || scheduled.PayloadVersion != 1 || scheduled.AttemptLimit != 5 {
		t.Fatalf("scheduled job = %#v, %v", scheduled, err)
	}
	if _, err := service.List(context.Background(), ListRequest{Limit: 10}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("anonymous list returned %v", err)
	}
	if jobs, err := service.List(actorContext, ListRequest{Limit: 20}); err != nil || len(jobs) != 1 {
		t.Fatalf("default list = %#v, %v", jobs, err)
	}
	for _, request := range []ListRequest{{Limit: 0}, {Limit: 101}, {Limit: 1, AfterID: scheduled.ID}, {Limit: 1, AfterCreatedAt: &now}, {Limit: 1, AfterCreatedAt: &now, AfterID: "invalid"}} {
		if _, err := service.List(actorContext, request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid Job page %#v: %v", request, err)
		}
	}
	if _, err := service.Cancel(context.Background(), scheduled.ID, "Cancel work"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("anonymous cancellation returned %v", err)
	}
	if _, err := service.Retry(context.Background(), scheduled.ID, "Retry work"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("anonymous retry returned %v", err)
	}
	if _, err := service.Retry(actorContext, scheduled.ID, "Retry work"); catalog.ErrorCodeOf(err) != catalog.CodeInvalidTransition {
		t.Fatalf("queued retry returned %v", err)
	}
	if err := requireJobPermission(actorContext, "custom", "cancel"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("unknown Job obtained user management authority")
	}

	if got := (&HandlerError{Safe: "safe failure"}).Error(); got != "safe failure" {
		t.Fatalf("safe handler error = %q", got)
	}
	cause := errors.New("upstream failure")
	if got := (&HandlerError{Safe: "safe failure", Cause: cause}).Error(); got != cause.Error() {
		t.Fatalf("causal handler error = %q", got)
	}
	if got := (Job{Kind: "source.sync", ID: scheduled.ID}).String(); got != "source.sync/"+scheduled.ID {
		t.Fatalf("job string = %q", got)
	}
}

func TestWorkerAndMemoryStoreFailureBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	store := NewMemoryStore()
	for _, options := range []struct {
		store Store
		clock Clock
		owner string
		lease time.Duration
		poll  time.Duration
	}{
		{clock: fakeClock, owner: "worker", lease: time.Minute, poll: time.Second},
		{store: store, owner: "worker", lease: time.Minute, poll: time.Second},
		{store: store, clock: fakeClock, lease: time.Minute, poll: time.Second},
		{store: store, clock: fakeClock, owner: "worker", poll: time.Second},
		{store: store, clock: fakeClock, owner: "worker", lease: time.Minute},
	} {
		if _, err := NewWorker(options.store, options.clock, options.owner, options.lease, options.poll, time.Hour, nil); err == nil {
			t.Fatal("incomplete worker dependencies were accepted")
		}
	}
	worker, err := NewWorker(store, fakeClock, "worker", time.Minute, time.Millisecond, time.Hour, map[string]Handler{})
	if err != nil {
		t.Fatal(err)
	}
	for _, concurrency := range []int{0, 129} {
		if err := worker.RunConcurrent(context.Background(), concurrency); err == nil {
			t.Fatalf("worker concurrency %d was accepted", concurrency)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.RunConcurrent(canceled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled worker returned %v", err)
	}

	service, err := NewService(store, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	actorContext := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "tenant", Subject: "operator", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleReader}})
	unknown, err := enqueueWorkerFixture(service, actorContext, EnqueueInput{Kind: "unknown", Payload: json.RawMessage(`{}`), DeduplicationKey: "unknown", AttemptLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Get(actorContext, unknown.TenantID, unknown.ID)
	if err != nil || failed.State != StateDeadLetter || failed.FailureClass != FailureInvalidInput {
		t.Fatalf("unsupported job = %#v, %v", failed, err)
	}
	terminal, err := store.RequestCancellation(actorContext, unknown.TenantID, unknown.ID, fakeClock.Now())
	if err != nil || terminal.State != StateDeadLetter {
		t.Fatalf("terminal cancellation = %#v, %v", terminal, err)
	}

	invalidProgress, err := enqueueWorkerFixture(service, actorContext, EnqueueInput{Kind: "invalid-progress", Payload: json.RawMessage(`{}`), DeduplicationKey: "invalid-progress", AttemptLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	progressWorker, err := NewWorker(store, fakeClock, "progress-worker", time.Minute, time.Second, time.Hour, map[string]Handler{
		"invalid-progress": func(_ context.Context, _ Job, progress func(int) error) (any, error) {
			return nil, progress(101)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := progressWorker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	invalidProgress, err = store.Get(actorContext, invalidProgress.TenantID, invalidProgress.ID)
	if err != nil || invalidProgress.State != StateDeadLetter || invalidProgress.FailureClass != FailureInternal {
		t.Fatalf("invalid progress job = %#v, %v", invalidProgress, err)
	}

	unserializable, err := enqueueWorkerFixture(service, actorContext, EnqueueInput{Kind: "unserializable", Payload: json.RawMessage(`{}`), DeduplicationKey: "unserializable", AttemptLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	marshalWorker, err := NewWorker(store, fakeClock, "marshal-worker", time.Minute, time.Second, time.Hour, map[string]Handler{
		"unserializable": func(context.Context, Job, func(int) error) (any, error) { return make(chan struct{}), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := marshalWorker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	unserializable, err = store.Get(actorContext, unserializable.TenantID, unserializable.ID)
	if err != nil || unserializable.State != StateDeadLetter || unserializable.SafeError != "Job result could not be serialized." {
		t.Fatalf("unserializable result job = %#v, %v", unserializable, err)
	}

	for index := range 3 {
		job := Job{ID: string(rune('a' + index)), TenantID: "tenant", Kind: "ordered", Payload: json.RawMessage(`{}`), State: StateQueued, Priority: index, NotBefore: now, AttemptLimit: 1, DeduplicationKey: string(rune('a' + index)), CreatedAt: now.Add(time.Duration(index) * time.Second)}
		if _, _, err := store.Enqueue(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	if listed, err := store.List(context.Background(), "tenant", ListRequest{Limit: 1}); err != nil || len(listed) != 1 {
		t.Fatalf("bounded store list = %#v, %v", listed, err)
	}
	claimed, err := store.Claim(context.Background(), "lease-owner", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Heartbeat(context.Background(), claimed.TenantID, claimed.ID, "other-owner", now, time.Minute, 10); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("foreign heartbeat returned %v", err)
	}
	if _, err := store.Complete(context.Background(), claimed.TenantID, claimed.ID, "other-owner", now, nil); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("foreign completion returned %v", err)
	}
	if _, err := store.Fail(context.Background(), claimed.TenantID, claimed.ID, "other-owner", now, FailureInternal, "failed", time.Time{}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("foreign failure returned %v", err)
	}
	if _, err := store.Heartbeat(context.Background(), "other-tenant", claimed.ID, "lease-owner", now, time.Minute, 10); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant heartbeat returned %v", err)
	}
	if _, err := store.Complete(context.Background(), "other-tenant", claimed.ID, "lease-owner", now, nil); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant completion returned %v", err)
	}
	if _, err := store.Fail(context.Background(), "other-tenant", claimed.ID, "lease-owner", now, FailureInternal, "failed", time.Time{}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant failure returned %v", err)
	}
	if _, err := store.RequestCancellation(context.Background(), "other-tenant", claimed.ID, now); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant cancellation returned %v", err)
	}
	if _, err := store.Claim(context.Background(), "replacement-owner", now.Add(2*time.Second), time.Minute); err != nil {
		t.Fatalf("expired lease was not reclaimable: %v", err)
	}
}
