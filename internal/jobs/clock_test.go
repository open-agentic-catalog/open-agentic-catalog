package jobs

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestMemoryJobBackwardClock(t *testing.T) {
	at := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	back := at.Add(-2 * time.Second)
	for _, mode := range []string{"queued cancellation", "running cancellation", "heartbeat and completion", "retry and claim"} {
		t.Run(mode, func(t *testing.T) {
			store := NewMemoryStore()
			job := Job{ID: "job", TenantID: "tenant", Kind: "test", State: StateQueued, NotBefore: at, CreatedAt: at, UpdatedAt: at, AttemptLimit: 3}
			if _, _, err := store.Enqueue(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			checkTime := func(got Job, err error) {
				t.Helper()
				if err != nil || !got.UpdatedAt.Equal(at) {
					t.Fatalf("transition time regressed: %+v, %v", got, err)
				}
			}
			if mode == "queued cancellation" {
				cancelled, err := store.RequestCancellation(t.Context(), job.TenantID, job.ID, back)
				checkTime(cancelled, err)
				if cancelled.State != StateCancelled {
					t.Fatal("queued Job was not cancelled")
				}
				return
			}
			claimed, err := store.Claim(t.Context(), "worker", at, time.Minute)
			checkTime(claimed, err)
			if mode == "running cancellation" {
				cancelled, err := store.RequestCancellation(t.Context(), job.TenantID, job.ID, back)
				checkTime(cancelled, err)
				if !cancelled.CancellationRequested || !cancelled.LeaseExpiresAt.Equal(*claimed.LeaseExpiresAt) {
					t.Fatal("cancellation changed the lease or lost the request")
				}
				failed, err := store.Fail(t.Context(), job.TenantID, job.ID, "worker", back, FailureCancelled, "cancelled", time.Time{})
				checkTime(failed, err)
				if failed.State != StateCancelled {
					t.Fatal("requested cancellation lost")
				}
				return
			}
			if mode == "retry and claim" {
				retryAt := back.Add(time.Second)
				waiting, err := store.Fail(t.Context(), job.TenantID, job.ID, "worker", back, FailureDependency, "unavailable", retryAt)
				checkTime(waiting, err)
				if waiting.State != StateWaiting || !waiting.NotBefore.Equal(at) {
					t.Fatal("retry scheduled before the retained transition")
				}
				if _, err := store.Claim(t.Context(), "replacement", retryAt, time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
					t.Fatalf("claim before retained transition time: %v", err)
				}
				reclaimed, err := store.Claim(t.Context(), "replacement", at, time.Minute)
				checkTime(reclaimed, err)
				if reclaimed.Attempt != 2 {
					t.Fatal("retry not claimed after clock recovery")
				}
				return
			}
			renewed, err := store.Heartbeat(t.Context(), job.TenantID, job.ID, "worker", back, time.Minute, 10)
			checkTime(renewed, err)
			if renewed.Progress != 10 || !renewed.LeaseExpiresAt.Equal(*claimed.LeaseExpiresAt) {
				t.Fatal("heartbeat lost progress or shortened the lease")
			}
			if _, err := store.Heartbeat(t.Context(), job.TenantID, job.ID, "worker", *claimed.LeaseExpiresAt, time.Minute, 20); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
				t.Fatalf("heartbeat renewed an expired lease: %v", err)
			}
			completed, err := store.Complete(t.Context(), job.TenantID, job.ID, "worker", back, json.RawMessage(`{"ok":true}`))
			checkTime(completed, err)
			if completed.State != StateSucceeded || completed.Progress != 100 || completed.LeaseExpiresAt != nil {
				t.Fatal("completion did not clear the live lease")
			}
		})
	}
}
