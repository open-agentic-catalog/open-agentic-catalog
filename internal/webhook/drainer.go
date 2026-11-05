// Disabled-webhook draining acknowledges bounded outbox batches without sending
// notifications or requiring signing dependencies.

package webhook

import (
	"context"
	"errors"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const drainBatchSize = 32

// Drainer acknowledges leased outbox records when webhook delivery is disabled.
// It sends nothing and needs no signing key. Re-enabling delivery cannot replay
// acknowledged history, so enabled emitters must not overlap disabled drainers.
type Drainer struct {
	outbox   Outbox
	clock    Clock
	owner    string
	lease    time.Duration
	poll     time.Duration
	observer OutboxObserver
}

// NewDrainer validates claim ownership, positive timing and the optional
// observer without requiring delivery dependencies.
func NewDrainer(outbox Outbox, clock Clock, owner string, lease, poll time.Duration, observers ...OutboxObserver) (*Drainer, error) {
	if outbox == nil || clock == nil || owner == "" || lease <= 0 || poll <= 0 {
		return nil, errors.New("disabled-webhook outbox drainer dependencies and limits are required")
	}
	if len(observers) > 1 {
		return nil, errors.New("at most one outbox observer is supported")
	}
	var observer OutboxObserver
	if len(observers) == 1 {
		observer = observers[0]
	}
	return &Drainer{outbox: outbox, clock: clock, owner: owner, lease: lease, poll: poll, observer: observer}, nil
}

// Run drains immediately and polls when no records complete, returning
// cancellation or the first claim/acknowledgement error.
func (d *Drainer) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.poll)
	defer ticker.Stop()
	for {
		completed, err := d.DrainOnce(ctx)
		if err != nil {
			return err
		}
		if completed > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// DrainOnce acknowledges one bounded batch, renewing ownership before each
// acknowledgement. The count excludes a record whose acknowledgement failed.
func (d *Drainer) DrainOnce(ctx context.Context) (completed int, resultErr error) {
	if d.observer != nil {
		defer func() { d.observer.OutboxBatch(completed, resultErr) }()
	}
	defer safeerr.Recover(&resultErr)
	events, err := d.outbox.ClaimOutbox(ctx, d.owner, d.clock.Now(), d.lease, drainBatchSize)
	if err != nil {
		return 0, err
	}
	for _, event := range events {
		if err := d.outbox.RenewOutbox(ctx, event.TenantID, event.ID, d.owner, d.clock.Now(), d.lease); err != nil {
			return completed, err
		}
		if err := d.outbox.MarkOutboxPublished(ctx, event.TenantID, event.ID, d.owner, d.clock.Now()); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}
