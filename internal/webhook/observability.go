// Sender observations translate transport results into fixed outcome names
// before reporting duration, without recording endpoint or response data.

package webhook

import (
	"context"
	"time"
)

// AttemptObserver accepts only code-owned transport outcomes, never endpoint
// identities, URLs, response bodies, headers, or error strings.
type AttemptObserver interface {
	ObserveWebhookAttempt(mode, outcome string, elapsed time.Duration)
}

// RetryObserver receives a delay only after a waiting delivery is persisted.
// It never receives receiver identity, headers, event data or error text.
type RetryObserver interface {
	WebhookRetryScheduled(time.Duration)
}

type observedSender struct {
	sender   Sender
	observer AttemptObserver
	mode     string
}

// Send preserves the underlying transport result while reporting only a fixed
// outcome class and elapsed time.
func (s observedSender) Send(ctx context.Context, request SendRequest) (SendResult, error) {
	started := time.Now()
	result, err := s.sender.Send(ctx, request)
	outcome := "terminal_response"
	switch {
	case ctx.Err() != nil:
		outcome = "canceled"
	case err != nil:
		outcome = "transport_error"
	case result.Status >= 200 && result.Status < 300:
		outcome = "accepted"
	case retryableStatus(result.Status):
		outcome = "retryable_response"
	}
	s.observer.ObserveWebhookAttempt(s.mode, outcome, time.Since(started))
	return result, err
}
