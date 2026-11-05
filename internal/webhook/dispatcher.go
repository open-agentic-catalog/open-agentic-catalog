// Dispatch consumes leased outbox notifications, refreshes endpoint eligibility
// and persists delivery outcomes only while the claim remains valid.

package webhook

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	eventcontract "github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Outbox owns durable notification claims. Selection may span tenants, but each
// follow-up operation binds the exact tenant, event and claim owner.
type Outbox interface {
	ClaimOutbox(context.Context, string, time.Time, time.Duration, int) ([]catalog.OutboxEvent, error)
	RenewOutbox(context.Context, string, string, string, time.Time, time.Duration) error
	MarkOutboxPublished(context.Context, string, string, string, time.Time) error
	ReleaseOutbox(context.Context, string, string, string) error
	DeferOutbox(context.Context, string, string, string, time.Time, time.Time) error
}

// OutboxObserver receives only a completed batch count and terminal error.
// Implementations must not retain event data or provider errors.
type OutboxObserver interface {
	OutboxBatch(int, error)
}

// Dispatcher delivers leased public notifications serially with bounded retry.
// Sends cannot share a transaction with receiver acceptance; a crash or lost
// claim can cause duplicate delivery even when local evidence is fenced.
type Dispatcher struct {
	outbox       Outbox
	store        Store
	cipher       Cipher
	sender       Sender
	clock        Clock
	ids          IDGenerator
	owner        string
	lease        time.Duration
	maxAttempts  int
	poll         time.Duration
	retryCeiling time.Duration
	observer     OutboxObserver
}

// NewDispatcher validates durable delivery dependencies, lease/retry limits and
// the optional observer before constructing a serial dispatcher.
func NewDispatcher(outbox Outbox, store Store, cipher Cipher, sender Sender, clock Clock, ids IDGenerator, owner string, lease time.Duration, maxAttempts int, poll time.Duration, retryCeiling time.Duration, observers ...OutboxObserver) (*Dispatcher, error) {
	if outbox == nil || store == nil || cipher == nil || sender == nil || clock == nil || ids == nil || owner == "" || lease <= 0 || maxAttempts < 1 || maxAttempts > 20 || poll <= 0 {
		return nil, errors.New("webhook dispatcher dependencies and limits are required")
	}
	if len(observers) > 1 {
		return nil, errors.New("at most one outbox observer is supported")
	}
	if retryCeiling < 5*time.Second || retryCeiling > time.Hour {
		return nil, errors.New("webhook retry ceiling must be between five seconds and one hour")
	}
	var observer OutboxObserver
	if len(observers) == 1 {
		observer = observers[0]
	}
	if attempts, ok := observer.(AttemptObserver); ok {
		sender = observedSender{sender: sender, observer: attempts, mode: "worker"}
	}
	return &Dispatcher{outbox: outbox, store: store, cipher: cipher, sender: sender, clock: clock, ids: ids, owner: owner, lease: lease, maxAttempts: maxAttempts, poll: poll, retryCeiling: retryCeiling, observer: observer}, nil
}

// Run dispatches immediately, continues while records complete, and polls
// otherwise until cancellation or an error.
func (d *Dispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.poll)
	defer ticker.Stop()
	for {
		completed, err := d.DispatchOnce(ctx)
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

// DispatchOnce claims at most one outbox record and processes a bounded
// endpoint set. It acknowledges only terminal work and defers retryable work
// under the lease.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (completed int, resultErr error) {
	if d.observer != nil {
		defer func() { d.observer.OutboxBatch(completed, resultErr) }()
	}
	defer safeerr.Recover(&resultErr)
	events, err := d.outbox.ClaimOutbox(ctx, d.owner, d.clock.Now(), d.lease, 1)
	if err != nil {
		return 0, err
	}
	for _, event := range events {
		if err := d.outbox.RenewOutbox(ctx, event.TenantID, event.ID, d.owner, d.clock.Now(), d.lease); err != nil {
			return completed, err
		}
		if event.Internal || !eventcontract.IsPublicType(event.Type) {
			if err := d.outbox.MarkOutboxPublished(ctx, event.TenantID, event.ID, d.owner, d.clock.Now()); err != nil {
				return completed, err
			}
			completed++
			continue
		}
		// The repository claim is bounded to one event; finish after lease-checked
		// delivery persistence and acknowledgement/defer, including failures.
		ctx, endTrace := catalog.StartTrace(ctx, "webhook dispatch", catalog.TraceAttempt, event.CausalTrace)
		defer func() {
			outcome := "ok"
			if resultErr != nil {
				outcome = "error"
			}
			endTrace(outcome)
		}()
		defer safeerr.Recover(&resultErr)
		endpoints, err := d.store.ListPendingEndpoints(ctx, event, dispatchEndpointLimit+1)
		if err != nil {
			return completed, err
		}
		allTerminal := len(endpoints) <= dispatchEndpointLimit
		var nextAttempt time.Time
		if !allTerminal {
			endpoints = endpoints[:dispatchEndpointLimit]
			nextAttempt = d.clock.Now().Add(d.poll)
		}
		for _, endpoint := range endpoints {
			// Earlier recipients may have been slow. Re-read immediately before
			// preparing this send so a queued snapshot cannot outlive revocation.
			endpoint, err = d.store.GetEndpoint(ctx, event.TenantID, endpoint.ID)
			if err != nil {
				return completed, err
			}
			if endpoint.Status != StatusActive || event.CreatedAt.Before(endpoint.CreatedAt) || !eventMatches(endpoint.EventTypes, event.Type) {
				continue
			}
			delivery, err := d.store.GetDelivery(ctx, event.TenantID, endpoint.ID, event.ID)
			if err != nil && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				return completed, err
			}
			if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
				now := d.clock.Now()
				deliveryID, idErr := d.ids.New(now)
				if idErr != nil {
					return completed, idErr
				}
				delivery = Delivery{ID: deliveryID, TenantID: event.TenantID, EndpointID: endpoint.ID, EventID: event.ID, EventType: event.Type, State: DeliveryPending, CreatedAt: now, UpdatedAt: now}
			}
			if delivery.State == DeliverySucceeded || delivery.State == DeliveryDeadLetter {
				continue
			}
			if delivery.NextAttemptAt != nil && delivery.NextAttemptAt.After(d.clock.Now()) {
				allTerminal = false
				nextAttempt = earlierAttempt(nextAttempt, *delivery.NextAttemptAt)
				continue
			}
			if err := d.outbox.RenewOutbox(ctx, event.TenantID, event.ID, d.owner, d.clock.Now(), d.lease); err != nil {
				return completed, err
			}
			delivery = deliver(ctx, d.sender, d.cipher, endpoint, event, delivery, d.maxAttempts, d.clock, d.retryCeiling)
			if err := ctx.Err(); err != nil {
				return completed, err
			}
			evidence, err := deliveryAudit(ctx, d.ids, delivery, true)
			if err != nil {
				return completed, err
			}
			persisted, err := d.store.SaveClaimedDelivery(ctx, delivery, d.owner, d.clock, evidence)
			if err != nil {
				return completed, err
			}
			if observer, ok := d.observer.(RetryObserver); ok && persisted.State == DeliveryWaiting && persisted.NextAttemptAt != nil {
				observer.WebhookRetryScheduled(persisted.NextAttemptAt.Sub(persisted.UpdatedAt))
			}
			if delivery.State != DeliverySucceeded && delivery.State != DeliveryDeadLetter {
				allTerminal = false
				if delivery.NextAttemptAt != nil {
					nextAttempt = earlierAttempt(nextAttempt, *delivery.NextAttemptAt)
				}
			}
		}
		if allTerminal {
			if err := d.outbox.MarkOutboxPublished(ctx, event.TenantID, event.ID, d.owner, d.clock.Now()); err != nil {
				return completed, err
			}
			completed++
		} else {
			now := d.clock.Now()
			if nextAttempt.IsZero() || !nextAttempt.After(now) {
				nextAttempt = now.Add(d.poll)
			}
			// Poll cadence may exceed the repository's one-hour scheduling bound.
			if nextAttempt.After(now.Add(time.Hour)) {
				nextAttempt = now.Add(time.Hour)
			}
			if err := d.outbox.DeferOutbox(ctx, event.TenantID, event.ID, d.owner, now, nextAttempt); err != nil {
				return completed, err
			}
		}
	}
	return completed, nil
}

const dispatchEndpointLimit = 8

// earlierAttempt returns the earliest scheduled time, treating a zero current
// value as unset.
func earlierAttempt(current, candidate time.Time) time.Time {
	if current.IsZero() || candidate.Before(current) {
		return candidate
	}
	return current
}

// eventMatches reports whether an endpoint subscribes to the exact event type
// or the wildcard.
func eventMatches(filters []string, eventType string) bool {
	return slices.Contains(filters, "*") || slices.Contains(filters, eventType)
}
