// Source synchronization handler tests isolate progress, attribution and retry
// decisions from durable stores, source retrieval and the worker loop.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

func sourceSyncTestJob(trigger string) jobs.Job {
	return jobs.Job{ID: "00000000-0000-7000-8000-000000000011", TenantID: "00000000-0000-7000-8000-000000000001", PayloadVersion: 2,
		Payload: json.RawMessage(`{"sourceId":"source-id","sourceRevision":7,"trigger":"` + trigger + `"}`)}
}

func assertJobOperationOutcome(t *testing.T, metrics *observability.Registry, operation, outcome string) {
	t.Helper()
	var got []string
	for line := range strings.SplitSeq(string(metrics.Render(context.Background())), "\n") {
		if strings.HasPrefix(line, "open_agentic_catalog_operations_total{") {
			got = append(got, line)
		}
	}
	var want []string
	if outcome != "" {
		want = []string{`open_agentic_catalog_operations_total{operation="` + operation + `",outcome="` + outcome + `"} 1`}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("operation metrics = %v, want %v", got, want)
	}
}

func TestSourceSyncHandlerRejectsInputBeforeWork(t *testing.T) {
	for _, payload := range []string{
		``, `null`, `[]`, `{}`, `{"sourceId":"source-id","sourceRevision":7,"trigger":"manual"} {}`,
		`{"sourceId":"","sourceRevision":7,"trigger":"manual"}`,
		`{"sourceId":"source-id","sourceRevision":0,"trigger":"manual"}`,
		`{"sourceId":"source-id","sourceRevision":-1,"trigger":"manual"}`,
		`{"sourceId":"source-id","sourceRevision":7,"trigger":"untrusted"}`,
	} {
		t.Run(payload, func(t *testing.T) {
			metrics := observability.New(time.Now)
			handler := sourceSyncJobHandler(func(context.Context, string, int64) (federation.Snapshot, error) {
				t.Fatal("invalid input reached source synchronization")
				return federation.Snapshot{}, nil
			}, metrics)
			job := sourceSyncTestJob("manual")
			job.Payload = json.RawMessage(payload)
			result, err := handler(context.Background(), job, func(int) error {
				t.Fatal("invalid input reported progress")
				return nil
			})
			var failure *jobs.HandlerError
			if result != nil || !errors.As(err, &failure) || failure.Class != jobs.FailureInvalidInput || failure.Safe != "Source sync job payload is invalid." || failure.RetryAfter != 0 {
				t.Fatalf("invalid input result = %#v, %v", result, err)
			}
			assertJobOperationOutcome(t, metrics, "source_sync", "")
		})
	}
}

func TestSourceSyncHandlerPreservesResultAndJobAttribution(t *testing.T) {
	for _, trigger := range []string{"manual", "automatic"} {
		for _, state := range []federation.SnapshotState{federation.SnapshotComplete, federation.SnapshotIncomplete} {
			t.Run(trigger+"/"+string(state), func(t *testing.T) {
				job := sourceSyncTestJob(trigger)
				execution := audit.JobExecution{ID: job.ID, Kind: "source.sync", Attempt: 1, Initiator: "00000000-0000-7000-8000-000000000021"}
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx := audit.WithJobExecution(identity.WithActor(parent, identity.Actor{TenantID: "other-tenant", Subject: "caller"}), execution)
				want := federation.Snapshot{ID: "snapshot-id", TenantID: job.TenantID, SourceID: "source-id", SourceRevision: 7, State: state,
					MemberCount: 4, Findings: []catalog.Finding{{Code: "retained-finding", Message: strings.Repeat("private snapshot diagnostic", 40000)}}}
				metrics := observability.New(time.Now)
				var steps []int
				calls := 0
				handler := sourceSyncJobHandler(func(syncCtx context.Context, sourceID string, revision int64) (federation.Snapshot, error) {
					calls++
					if sourceID != "source-id" || revision != 7 || !slices.Equal(steps, []int{10}) || syncCtx.Done() == nil || parent.Err() != nil {
						t.Fatalf("sync inputs = %q / %d, progress %v, cancellation preserved %t", sourceID, revision, steps, syncCtx.Done() == parent.Done())
					}
					actor, ok := identity.FromContext(syncCtx)
					if !ok || actor.TenantID != job.TenantID || actor.Subject != audit.PrincipalIDCatalogWorker || len(actor.Roles) != 0 {
						t.Fatalf("worker actor = %#v, present %t", actor, ok)
					}
					gotExecution, ok := audit.JobExecutionFrom(syncCtx)
					wantExecution := execution
					wantExecution.Trigger = trigger
					if !ok || gotExecution != wantExecution {
						t.Fatalf("job execution = %#v, present %t", gotExecution, ok)
					}
					return want, nil
				}, metrics)
				result, err := claimedHandler(t, "source.sync", handler)(ctx, job, func(value int) error { steps = append(steps, value); return nil })
				reference := jobs.SnapshotReference{ID: want.ID, SourceID: want.SourceID, State: string(state)}
				if err != nil || !reflect.DeepEqual(result, reference) || calls != 1 || !slices.Equal(steps, []int{10, 100}) {
					t.Fatalf("sync result = %#v, %v; calls %d, progress %v", result, err, calls, steps)
				}
				raw, err := json.Marshal(result)
				if err != nil || string(raw) != fmt.Sprintf(`{"id":"snapshot-id","sourceId":"source-id","state":%q}`, state) || len(want.Findings[0].Message) < 1<<19 {
					t.Fatalf("snapshot was changed or duplicated into the Job result: %s, %v", raw, err)
				}
				original, _ := audit.JobExecutionFrom(ctx)
				actor, _ := identity.FromContext(ctx)
				if original != execution || actor.TenantID != "other-tenant" {
					t.Fatal("handler mutated parent attribution")
				}
				outcome := "succeeded"
				if state == federation.SnapshotIncomplete {
					outcome = "partial"
				}
				assertJobOperationOutcome(t, metrics, "source_sync", outcome)
			})
		}
	}
}

func TestSourceSyncHandlerPropagatesProgressFailure(t *testing.T) {
	for _, failAt := range []int{10, 100} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			metrics := observability.New(time.Now)
			failure := errors.New("lease progress rejected")
			calls := 0
			handler := sourceSyncJobHandler(func(context.Context, string, int64) (federation.Snapshot, error) {
				calls++
				return federation.Snapshot{State: federation.SnapshotComplete}, nil
			}, metrics)
			var steps []int
			result, err := claimedHandler(t, "source.sync", handler)(context.Background(), sourceSyncTestJob("manual"), func(value int) error {
				steps = append(steps, value)
				if value == failAt {
					return failure
				}
				return nil
			})
			if result != nil || !errors.Is(err, failure) {
				t.Fatalf("progress failure = %#v, %v", result, err)
			}
			wantCalls, outcome, wantSteps := 0, "", []int{10}
			if failAt == 100 {
				wantCalls, outcome, wantSteps = 1, "succeeded", []int{10, 100}
			}
			if calls != wantCalls || !slices.Equal(steps, wantSteps) {
				t.Fatalf("sync calls = %d, progress %v", calls, steps)
			}
			assertJobOperationOutcome(t, metrics, "source_sync", outcome)
		})
	}
}

func TestSourceSyncHandlerRetainsClassifiedFailureWithoutResult(t *testing.T) {
	rateLimited := fmt.Errorf("transport wrapper: %w", &federation.SyncError{Class: federation.SyncFailureRateLimited, RetryAfter: 23 * time.Second, Safe: "private diagnostic"})
	for _, test := range []struct {
		name  string
		cause error
		class jobs.FailureClass
		safe  string
		retry time.Duration
	}{
		{"wrapped rate limit", rateLimited, jobs.FailureRateLimited, "Source synchronization was rate limited.", 23 * time.Second},
		{"unavailable with minimum", &federation.SyncError{Class: federation.SyncFailureTransient, RetryAfter: time.Minute}, jobs.FailureTransientUpstream, "Source synchronization did not complete within its safe bounds.", time.Minute},
		{"dependency", catalog.E(catalog.CodeDependencyUnavailable, "private diagnostic"), jobs.FailureDependency, "A required source synchronization dependency is unavailable.", 0},
		{"cancellation", context.Canceled, jobs.FailureCancelled, "Source synchronization was cancelled.", 0},
		{"deadline", context.DeadlineExceeded, jobs.FailureTransientUpstream, "Source synchronization did not complete within its safe bounds.", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			metrics := observability.New(time.Now)
			calls := 0
			handler := sourceSyncJobHandler(func(context.Context, string, int64) (federation.Snapshot, error) {
				calls++
				return federation.Snapshot{ID: "failed-snapshot", State: federation.SnapshotFailed}, test.cause
			}, metrics)
			var steps []int
			result, err := claimedHandler(t, "source.sync", handler)(context.Background(), sourceSyncTestJob("manual"), func(value int) error { steps = append(steps, value); return nil })
			var failure *jobs.HandlerError
			if result != nil || !errors.As(err, &failure) {
				t.Fatalf("sync failure = %#v, %v", result, err)
			}
			if failure.Class != test.class || failure.Safe != test.safe || failure.RetryAfter != test.retry || !errors.Is(failure.Cause, test.cause) {
				t.Fatalf("handler failure = %#v", failure)
			}
			if calls != 1 || !slices.Equal(steps, []int{10}) {
				t.Fatalf("sync calls = %d, progress %v", calls, steps)
			}
			assertJobOperationOutcome(t, metrics, "source_sync", "failed")
		})
	}
}
