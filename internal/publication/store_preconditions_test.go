package publication

import (
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPublicationTransitionsFenceTheExpectedActiveSnapshot(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	const targetID = "00000000-0000-7000-8000-000000000002"
	const priorID = "00000000-0000-7000-8000-000000000003"
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	empty, prior, stale := "", priorID, "00000000-0000-7000-8000-000000000004"
	for _, rollback := range []bool{false, true} {
		operation := "activate"
		if rollback {
			operation = "rollback"
		}
		for _, test := range []struct {
			name      string
			withPrior bool
			expected  *string
			allowed   bool
		}{
			{"empty matches absence", false, &empty, true},
			{"stale after withdrawal", false, &prior, false},
			{"empty rejects replacement", true, &empty, false},
			{"stale rejects replacement", true, &stale, false},
			{"exact active matches", true, &prior, true},
			{"nil skips comparison", true, nil, true},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				sink := &recordingPublicationEvidence{}
				store := NewMemoryStore(sink)
				target := Snapshot{ID: targetID, TenantID: tenantID, Host: "catalog.example.com", Generation: 1, State: SnapshotCandidate, CreatedAt: now}
				if _, err := store.SaveCandidate(t.Context(), target); err != nil {
					t.Fatal(err)
				}
				if rollback {
					if _, err := store.Activate(publicationContext(t.Context(), tenantID), tenantID, targetID, now); err != nil {
						t.Fatal(err)
					}
					if _, err := store.Deactivate(t.Context(), tenantID, targetID, now); err != nil {
						t.Fatal(err)
					}
				}
				if test.withPrior {
					if _, err := store.SaveCandidate(t.Context(), Snapshot{ID: priorID, TenantID: tenantID, Host: target.Host, Generation: 2, State: SnapshotCandidate, CreatedAt: now}); err != nil {
						t.Fatal(err)
					}
					if _, err := store.Activate(publicationContext(t.Context(), tenantID), tenantID, priorID, now); err != nil {
						t.Fatal(err)
					}
				}
				before, err := store.Get(publicationContext(t.Context(), tenantID), tenantID, targetID)
				if err != nil {
					t.Fatal(err)
				}
				priorBefore, priorErr := store.Active(t.Context(), tenantID)
				if test.withPrior && priorErr != nil || !test.withPrior && catalog.ErrorCodeOf(priorErr) != catalog.CodeNotFound {
					t.Fatalf("initial active snapshot: %v", priorErr)
				}
				transition, action := store.Activate, audit.ActionPublicationActivated
				if rollback {
					transition, action = store.Rollback, audit.ActionPublicationRolledBack
				}
				at := now.Add(time.Second)
				evidence := currentMutationTestEvidence(t, tenantID, targetID, action, at)
				evidence.ExpectedActiveID = test.expected
				result, err := transition(t.Context(), tenantID, targetID, at, evidence)
				if !test.allowed {
					if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || result.ID != "" {
						t.Fatalf("stale transition returned %#v, %v", result, err)
					}
					after, getErr := store.Get(publicationContext(t.Context(), tenantID), tenantID, targetID)
					if getErr != nil || !reflect.DeepEqual(after, before) {
						t.Fatalf("rejected transition changed target: %#v, %v", after, getErr)
					}
					active, activeErr := store.Active(t.Context(), tenantID)
					if test.withPrior {
						if activeErr != nil || !reflect.DeepEqual(active, priorBefore) {
							t.Fatalf("rejected transition changed active snapshot: %#v, %v", active, activeErr)
						}
					} else if catalog.ErrorCodeOf(activeErr) != catalog.CodeNotFound {
						t.Fatalf("rejected transition created an active pointer: %v", activeErr)
					}
					if len(sink.audit) != 0 || len(sink.outbox) != 0 {
						t.Fatal("rejected transition emitted evidence")
					}
					return
				}
				if err != nil || result.ID != targetID || result.State != SnapshotActive || result.ActivatedAt == nil || !result.ActivatedAt.Equal(at) {
					t.Fatalf("matching transition returned %#v, %v", result, err)
				}
				active, err := store.Active(t.Context(), tenantID)
				if err != nil || !reflect.DeepEqual(active, result) {
					t.Fatalf("matching transition did not install its active pointer: %#v, %v", active, err)
				}
				if test.withPrior {
					retired, err := store.Get(publicationContext(t.Context(), tenantID), tenantID, priorID)
					priorBefore.State = SnapshotRetired
					if err != nil || !reflect.DeepEqual(retired, priorBefore) {
						t.Fatalf("replacement changed more than prior state: %#v, %v", retired, err)
					}
				}
				if len(sink.audit) != 1 || len(sink.outbox) != 1 || sink.audit[0].Action != action || sink.outbox[0].Subject != targetID {
					t.Fatal("successful transition omitted its exact evidence")
				}
			})
		}
	}
}

func TestPublicationRejectedCandidateDoesNotConsumeGeneration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	candidate := Snapshot{ID: "00000000-0000-7000-8000-000000000002", TenantID: "00000000-0000-7000-8000-000000000001", Host: "catalog.example.com", Generation: 1, State: SnapshotCandidate, CreatedAt: now}
	for _, missingSink := range []bool{false, true} {
		name := "invalid evidence"
		if missingSink {
			name = "missing evidence sink"
		}
		t.Run(name, func(t *testing.T) {
			sink := &recordingPublicationEvidence{}
			store := NewMemoryStore(sink)
			if missingSink {
				store = NewMemoryStore()
			}
			evidence := currentMutationTestEvidence(t, candidate.TenantID, candidate.ID, audit.ActionPublicationCandidateCreated, now)
			if !missingSink {
				evidence.Audit.TargetID = "00000000-0000-7000-8000-000000000003"
			}
			if result, err := store.SaveCandidate(t.Context(), candidate, evidence); catalog.ErrorCodeOf(err) != catalog.CodeInternal || result.ID != "" {
				t.Fatalf("invalid candidate returned %#v, %v", result, err)
			}
			if _, err := store.Get(publicationContext(t.Context(), candidate.TenantID), candidate.TenantID, candidate.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("failed candidate was retained: %v", err)
			}
			if len(sink.audit) != 0 || len(sink.outbox) != 0 {
				t.Fatal("rejected candidate emitted evidence")
			}
			generation, err := store.NextGeneration(t.Context(), candidate.TenantID, candidate.Host)
			if err != nil || generation != 1 {
				t.Fatalf("failed candidate consumed generation: %d, %v", generation, err)
			}
			// Repair the evidence boundary and retry without bypassing audit persistence.
			store.evidence = sink
			evidence = currentMutationTestEvidence(t, candidate.TenantID, candidate.ID, audit.ActionPublicationCandidateCreated, now)
			if _, err := store.SaveCandidate(t.Context(), candidate, evidence); err != nil {
				t.Fatalf("retry after rejected candidate: %v", err)
			}
			if len(sink.audit) != 1 || sink.audit[0].TargetID != candidate.ID || len(sink.outbox) != 0 {
				t.Fatal("candidate retry did not commit exactly one audit-only record")
			}
			generation, err = store.NextGeneration(t.Context(), candidate.TenantID, candidate.Host)
			if err != nil || generation != 2 {
				t.Fatalf("successful candidate did not advance generation: %d, %v", generation, err)
			}
		})
	}
}
