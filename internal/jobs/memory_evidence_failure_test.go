package jobs

import (
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestJobMemoryRejectsMissingOrUnboundEvidenceWithoutChangingState(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing sink", "ambiguous evidence", "unbound evidence"} {
		for _, operation := range []string{"new enqueue", "deduplicated enqueue", "cancel running", "cancel terminal"} {
			t.Run(mode+"/"+operation, func(t *testing.T) {
				sink := &recordingJobEvidence{}
				store := NewMemoryStore(sink)
				if mode == "missing sink" {
					store.evidence = nil
				}
				at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
				job := Job{ID: "job", TenantID: "tenant", Kind: "test", State: StateQueued, DeduplicationKey: "same", CreatedAt: at, UpdatedAt: at}
				if operation == "cancel running" {
					job.State = StateRunning
				} else if operation == "cancel terminal" {
					job.State = StateSucceeded
				}
				if operation != "new enqueue" {
					store.jobs[job.ID] = cloneJob(job)
				}
				evidence := []audit.Record{{}}
				if mode == "ambiguous evidence" {
					evidence = append(evidence, audit.Record{})
				}
				var got Job
				var err error
				if operation == "new enqueue" || operation == "deduplicated enqueue" {
					var existing bool
					got, existing, err = store.Enqueue(t.Context(), job, evidence...)
					if existing {
						t.Fatal("failed enqueue reported successful deduplication")
					}
				} else {
					got, err = store.RequestCancellation(t.Context(), job.TenantID, job.ID, at.Add(time.Minute), evidence...)
				}
				if catalog.ErrorCodeOf(err) != catalog.CodeInternal || got.ID != "" || len(sink.records) != 0 {
					t.Fatalf("failed evidence leaked result: %+v, %v records=%d", got, err, len(sink.records))
				}
				if operation == "new enqueue" {
					if len(store.jobs) != 0 {
						t.Fatal("new Job survived failed evidence")
					}
				} else if len(store.jobs) != 1 || !reflect.DeepEqual(store.jobs[job.ID], job) {
					t.Fatal("existing Job changed after failed evidence")
				}
			})
		}
	}
}

func TestAuditedRunningJobCancellationIsCooperative(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	sink := &recordingJobEvidence{}
	store := NewMemoryStore(sink)
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Subject: "fixture", Roles: []identity.Role{identity.RoleSourceAdmin}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	job := Job{ID: "00000000-0000-7000-8000-000000000004", TenantID: actor.TenantID, Kind: "source.sync", State: StateRunning, LeaseOwner: "worker", CreatedAt: at, UpdatedAt: at}
	store.jobs[job.ID] = job
	service, err := NewService(store, clock.NewFake(at), id.Generator{}, WithAuditEvidence())
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Cancel(ctx, job.ID, "Cancel obsolete synchronization")
	if err != nil || got.State != StateRunning || !got.CancellationRequested || got.LeaseOwner != job.LeaseOwner || len(sink.records) != 1 {
		t.Fatalf("running cancellation=%+v, %v records=%d", got, err, len(sink.records))
	}
	if err := audit.ValidateNewRecord(sink.records[0]); err != nil {
		t.Fatal(err)
	}
	// A terminal no-op is still an audited command; failure to retain that
	// record must not be returned as successful cancellation.
	job.State = StateSucceeded
	store.jobs[job.ID] = job
	sentinel := errors.New("audit unavailable")
	store.evidence = failingJobEvidence{sentinel}
	if got, err := service.Cancel(ctx, job.ID, "Cancel obsolete synchronization"); !errors.Is(err, sentinel) || got.ID != "" || !reflect.DeepEqual(store.jobs[job.ID], job) {
		t.Fatalf("failed terminal cancellation changed state: %+v, %v", got, err)
	}
}

func TestJobBackoffAndSchedulingBounds(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		attempt int
		ceiling time.Duration
	}{
		{math.MinInt, 5 * time.Second},
		{-1, 5 * time.Second},
		{0, 5 * time.Second},
		{1, 5 * time.Second},
		{2, 15 * time.Second},
		{3, 45 * time.Second},
		{4, 135 * time.Second},
		{5, 405 * time.Second},
		{6, 1215 * time.Second},
		{7, time.Hour},
		{8, time.Hour},
		{20, time.Hour},
		{21, time.Hour},
		{100, time.Hour},
		{math.MaxInt, time.Hour},
	} {
		t.Run(fmt.Sprintf("attempt-%d", test.attempt), func(t *testing.T) {
			if got := backoffCeiling(test.attempt); got != test.ceiling {
				t.Fatalf("ceiling=%v, want %v", got, test.ceiling)
			}
			if got := backoff(test.attempt, time.Hour, 0); got < 0 || got > test.ceiling {
				t.Fatalf("jittered delay=%v, want [0,%v]", got, test.ceiling)
			}
		})
	}
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	older := Job{ID: "a", Priority: 1, CreatedAt: at.Add(-time.Hour), NotBefore: at}
	earlier := Job{ID: "z", Priority: 1, CreatedAt: at, NotBefore: at.Add(-time.Minute)}
	if !jobClaimLess(earlier, older) || jobClaimLess(older, earlier) {
		t.Fatal("eligible scheduling time did not precede creation-time/ID tie-breakers")
	}
}
