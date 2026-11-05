//go:build integration

package database

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

func TestPostgresJobBackwardClock(t *testing.T) {
	at := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	for index, mode := range []string{"queued cancellation", "running cancellation", "heartbeat and completion", "retry and claim"} {
		t.Run(mode, func(t *testing.T) {
			db := newHousekeepingDatabase(t)
			job := housekeepingJob(fmt.Sprintf("00000000-0000-7000-8000-%012d", 2900+index), "", at)
			job.AttemptLimit = 3
			if _, _, err := db.apiJobs.Enqueue(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			if mode == "queued cancellation" {
				cancelled, err := db.apiJobs.RequestCancellation(t.Context(), job.TenantID, job.ID, at.Add(-2*time.Second))
				if err != nil || cancelled.State != jobs.StateCancelled || !cancelled.UpdatedAt.Equal(at) {
					t.Fatalf("backward-clock cancellation: %+v, %v", cancelled, err)
				}
				return
			}
			claimed, err := db.workerJobs.Claim(t.Context(), "clock-worker", at, time.Minute)
			if err != nil || claimed.ID != job.ID {
				t.Fatalf("claim: %+v, %v", claimed, err)
			}
			back := at.Add(-2 * time.Second)
			if mode == "running cancellation" {
				cancelled, err := db.apiJobs.RequestCancellation(t.Context(), job.TenantID, job.ID, back)
				if err != nil || !cancelled.CancellationRequested || !cancelled.UpdatedAt.Equal(at) || !cancelled.LeaseExpiresAt.Equal(*claimed.LeaseExpiresAt) {
					t.Fatalf("running cancellation: %+v, %v", cancelled, err)
				}
				failed, err := db.workerJobs.Fail(t.Context(), job.TenantID, job.ID, "clock-worker", back, jobs.FailureCancelled, "cancelled", time.Time{})
				if err != nil || failed.State != jobs.StateCancelled || !failed.UpdatedAt.Equal(at) {
					t.Fatalf("cancelled completion: %+v, %v", failed, err)
				}
				return
			}
			if mode == "retry and claim" {
				retryAt := back.Add(time.Second)
				waiting, err := db.workerJobs.Fail(t.Context(), job.TenantID, job.ID, "clock-worker", back, jobs.FailureDependency, "dependency unavailable", retryAt)
				if err != nil || waiting.State != jobs.StateWaiting || !waiting.UpdatedAt.Equal(at) || !waiting.NotBefore.Equal(at) {
					t.Fatalf("retry after clock step: %+v, %v", waiting, err)
				}
				if _, err := db.workerJobs.Claim(t.Context(), "replacement", retryAt, time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
					t.Fatalf("claim before retained transition time: %v", err)
				}
				reclaimed, err := db.workerJobs.Claim(t.Context(), "replacement", at, time.Minute)
				if err != nil || reclaimed.ID != job.ID || reclaimed.Attempt != 2 || !reclaimed.UpdatedAt.Equal(at) {
					t.Fatalf("claim after clock catches up: %+v, %v", reclaimed, err)
				}
				return
			}
			renewed, err := db.workerJobs.Heartbeat(t.Context(), job.TenantID, job.ID, "clock-worker", back, time.Minute, 10)
			if err != nil || renewed.Progress != 10 || !renewed.UpdatedAt.Equal(at) || !renewed.LeaseExpiresAt.Equal(*claimed.LeaseExpiresAt) {
				t.Fatalf("backward-clock heartbeat: %+v, %v", renewed, err)
			}
			for _, fence := range []struct {
				tenant, owner string
				now           time.Time
			}{
				{housekeepingTenantB, "clock-worker", back},
				{job.TenantID, "other-worker", back},
				{job.TenantID, "clock-worker", *claimed.LeaseExpiresAt},
			} {
				if _, err := db.workerJobs.Heartbeat(t.Context(), fence.tenant, job.ID, fence.owner, fence.now, time.Minute, 20); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
					t.Fatalf("heartbeat bypassed lease fence: %v", err)
				}
				if _, err := db.workerJobs.Complete(t.Context(), fence.tenant, job.ID, fence.owner, fence.now, json.RawMessage(`{}`)); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
					t.Fatalf("completion bypassed lease fence: %v", err)
				}
			}
			if _, err := db.workerJobs.Claim(t.Context(), "other-worker", back, time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("live lease reclaimed after clock step: %v", err)
			}
			completed, err := db.workerJobs.Complete(t.Context(), job.TenantID, job.ID, "clock-worker", back, json.RawMessage(`{"ok":true}`))
			if err != nil || completed.State != jobs.StateSucceeded || !completed.UpdatedAt.Equal(at) || completed.LeaseExpiresAt != nil || completed.Progress != 100 {
				t.Fatalf("backward-clock completion: %+v, %v", completed, err)
			}
		})
	}
}
