// Package housekeeping coordinates bounded reclamation of disposable
// operational PostgreSQL state. It owns cadence and fairness while the
// database adapter owns exact eligibility, locking, and lifecycle guards.
package housekeeping

import (
	"context"
	"errors"
	"time"
)

// BatchLimit is the fixed maximum selected by any one cleanup operation. It is
// intentionally code-defined until representative load evidence justifies a
// deployment setting.
const BatchLimit = 100

// Class is the fixed low-cardinality cleanup vocabulary used by metrics.
type Class string

const (
	ClassIdempotency      Class = "idempotency"
	ClassSourcePreview    Class = "source_preview"
	ClassSourceInspection Class = "source_inspection"
	ClassSourceState      Class = "source_coordination"
	ClassTerminalJob      Class = "terminal_job"
	ClassWebhookDelivery  Class = "webhook_delivery"
	ClassOutbox           Class = "acknowledged_outbox"
)

// Result reports only bounded aggregate facts about one attempted batch.
// OldestEligibleAge is derived from the selected page, never a whole-table
// backlog scan.
type Result struct {
	Reclaimed         int64
	Marked            int64
	Saturated         bool
	OldestEligibleAge time.Duration
}

// Store performs one transactionally guarded operation for each cleanup
// class. Implementations must not return record identifiers or private values.
type Store interface {
	ReclaimExpiredIdempotency(context.Context, int) (Result, error)
	ReclaimExpiredSourcePreviews(context.Context, int) (Result, error)
	ReclaimUnusedSourceInspections(context.Context, int) (Result, error)
	ReclaimObsoleteSourceCoordination(context.Context, int) (Result, error)
	ReclaimTerminalJobs(context.Context, time.Duration, int) (Result, error)
	ReclaimWebhookDeliveries(context.Context, time.Duration, int) (Result, error)
	ReclaimAcknowledgedOutbox(context.Context, time.Duration, int) (Result, error)
}

// Observer accepts only the fixed class, aggregate result, outcome, and
// duration. Errors and row identities never cross this boundary.
type Observer interface {
	ObserveHousekeepingBatch(Class, string, Result, time.Duration)
	ObserveHousekeepingCycle(string, time.Duration)
}

// Runner offers every class one bounded operation per cycle before waiting for
// the next cadence. A failed class does not prevent later classes in the same
// cycle from making progress.
type Runner struct {
	store           Store
	interval        time.Duration
	jobRetention    time.Duration
	outboxRetention time.Duration
	observer        Observer
}

// New constructs a durable housekeeping runner.
func New(store Store, interval, jobRetention, outboxRetention time.Duration, observers ...Observer) (*Runner, error) {
	if store == nil || interval <= 0 || jobRetention <= 0 || outboxRetention <= 0 {
		return nil, errors.New("housekeeping store and positive timing are required")
	}
	if len(observers) > 1 {
		return nil, errors.New("at most one housekeeping observer is supported")
	}
	var observer Observer
	if len(observers) == 1 {
		if observers[0] == nil {
			return nil, errors.New("housekeeping observer is required when supplied")
		}
		observer = observers[0]
	}
	return &Runner{store: store, interval: interval, jobRetention: jobRetention, outboxRetention: outboxRetention, observer: observer}, nil
}

// Run performs one cycle immediately and then at the configured cadence until
// cancellation or a cycle error. The process supervisor retries classified
// database outages independently from the other worker loops.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.RunOnce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				return err
			}
		}
	}
}

// RunOnce executes one fair class-ordered cycle and joins failures only after
// all classes have received their opportunity.
func (r *Runner) RunOnce(ctx context.Context) error {
	started := time.Now()
	operations := []struct {
		class Class
		run   func(context.Context) (Result, error)
	}{
		{ClassIdempotency, func(ctx context.Context) (Result, error) { return r.store.ReclaimExpiredIdempotency(ctx, BatchLimit) }},
		{ClassSourcePreview, func(ctx context.Context) (Result, error) {
			return r.store.ReclaimExpiredSourcePreviews(ctx, BatchLimit)
		}},
		{ClassSourceInspection, func(ctx context.Context) (Result, error) {
			return r.store.ReclaimUnusedSourceInspections(ctx, BatchLimit)
		}},
		{ClassSourceState, func(ctx context.Context) (Result, error) {
			return r.store.ReclaimObsoleteSourceCoordination(ctx, BatchLimit)
		}},
		{ClassTerminalJob, func(ctx context.Context) (Result, error) {
			return r.store.ReclaimTerminalJobs(ctx, r.jobRetention, BatchLimit)
		}},
		{ClassWebhookDelivery, func(ctx context.Context) (Result, error) {
			return r.store.ReclaimWebhookDeliveries(ctx, r.outboxRetention, BatchLimit)
		}},
		{ClassOutbox, func(ctx context.Context) (Result, error) {
			return r.store.ReclaimAcknowledgedOutbox(ctx, r.outboxRetention, BatchLimit)
		}},
	}
	var cycleErr error
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			cycleErr = errors.Join(cycleErr, err)
			break
		}
		operationStarted := time.Now()
		result, err := operation.run(ctx)
		outcome := "succeeded"
		if err != nil {
			outcome = "failed"
			cycleErr = errors.Join(cycleErr, err)
		}
		if r.observer != nil {
			r.observer.ObserveHousekeepingBatch(operation.class, outcome, result, time.Since(operationStarted))
		}
	}
	if r.observer != nil {
		outcome := "succeeded"
		if cycleErr != nil {
			outcome = "failed"
		}
		r.observer.ObserveHousekeepingCycle(outcome, time.Since(started))
	}
	return cycleErr
}

// Noop is the context-blocking adapter for the process-local development
// runtime, which has no durable state pressure to reclaim.
type Noop struct{}

// Run waits for cancellation without attempting durable cleanup.
func (Noop) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
