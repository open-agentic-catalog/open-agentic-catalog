//go:build integration

package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestPostgresJobPanicRollsBackAndLeavesRecoverableLease(t *testing.T) {
	database := newHousekeepingDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := housekeepingJob("00000000-0000-7000-8000-000000000899", "", now)
	job.AttemptLimit = 3
	if _, _, err := database.apiJobs.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	// The application callback begins a real tenant transaction. Its mutation
	// must be rolled back before the worker converts the panic to a fatal result.
	worker, err := jobs.NewWorker(database.workerJobs, clock.NewFake(now), "panic-worker", time.Minute, time.Second, time.Hour, map[string]jobs.Handler{job.Kind: func(ctx context.Context, _ jobs.Job, _ func(int) error) (any, error) {
		return withTenantTx(ctx, database.worker, job.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (any, error) {
			// A worker-owned operational update is sufficient to demonstrate rollback;
			// the exact runtime role and real RLS/privileges stay in force.
			if _, err := tx.Exec(ctx, `UPDATE jobs SET progress=50 WHERE tenant_id=$1 AND id=$2`, job.TenantID, job.ID); err != nil {
				t.Fatal(err)
			}
			panic("SYNTHETIC_PRIVATE_TRANSACTION_PANIC")
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(t.Context()); !errors.Is(err, safeerr.ErrPanic) {
		t.Fatalf("panic classification: %v", err)
	}
	retained, err := database.apiJobs.Get(t.Context(), job.TenantID, job.ID)
	if err != nil || retained.State != jobs.StateRunning || retained.Progress != 0 || retained.Attempt != 1 || retained.SafeError != "" {
		t.Fatalf("panic committed progress/finalization: %+v %v", retained, err)
	}
	if _, err := database.workerJobs.Claim(t.Context(), "early-replacement", now.Add(30*time.Second), time.Minute); err == nil {
		t.Fatal("live lease reclaimed")
	}
	reclaimed, err := database.workerJobs.Claim(t.Context(), "replacement", now.Add(2*time.Minute), time.Minute)
	if err != nil || reclaimed.ID != job.ID || reclaimed.Attempt != 2 {
		t.Fatalf("lease not recoverable: %+v %v", reclaimed, err)
	}
	if err := database.worker.Ping(t.Context()); err != nil {
		t.Fatal("panic left connection unusable", err)
	}
}
