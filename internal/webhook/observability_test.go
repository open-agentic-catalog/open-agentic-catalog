package webhook

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

type attemptObservation struct {
	mode, outcome string
	duration      time.Duration
}
type attemptRecorder struct{ items []attemptObservation }

type dispatcherObserver struct {
	recordingOutboxObserver
	attemptRecorder
	retries []time.Duration
}

func (r *dispatcherObserver) WebhookRetryScheduled(delay time.Duration) {
	r.retries = append(r.retries, delay)
}

func (r *attemptRecorder) ObserveWebhookAttempt(mode, outcome string, elapsed time.Duration) {
	r.items = append(r.items, attemptObservation{mode, outcome, elapsed})
}

func TestObservedWebhookSenderClassifiesOnlySafeTransportOutcomes(t *testing.T) {
	for _, test := range []struct {
		status         int
		fail, canceled bool
		want           string
	}{
		{204, false, false, "accepted"}, {429, false, false, "retryable_response"}, {503, false, false, "retryable_response"}, {401, false, false, "terminal_response"}, {302, false, false, "terminal_response"}, {0, true, false, "transport_error"}, {0, true, true, "canceled"},
	} {
		recorder := &attemptRecorder{}
		ctx, cancel := context.WithCancel(t.Context())
		if test.canceled {
			cancel()
		}
		sender := observedSender{mode: "worker", observer: recorder, sender: admissionSenderFunc(func(context.Context, SendRequest) (SendResult, error) {
			if test.fail {
				return SendResult{}, errors.New("private transport detail")
			}
			return SendResult{Status: test.status}, nil
		})}
		_, _ = sender.Send(ctx, SendRequest{})
		cancel()
		if len(recorder.items) != 1 || recorder.items[0].outcome != test.want || recorder.items[0].mode != "worker" || recorder.items[0].duration < 0 {
			t.Fatalf("observed %#v, want %s", recorder.items, test.want)
		}
	}
}

func TestWebhookServiceObservesTestsWithoutChangingConstructorValidation(t *testing.T) {
	s, store, _, ctx := auditFixture(t)
	for _, observers := range [][]AttemptObserver{{nil}, {&attemptRecorder{}, &attemptRecorder{}}} {
		if _, err := NewService(store, s.cipher, s.sender, s.clock, s.ids, observers...); err == nil {
			t.Fatal("invalid observer accepted")
		}
	}
	created, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		status  int
		outcome string
		state   DeliveryState
	}{
		{"accepted", 204, "accepted", DeliverySucceeded},
		{"throttled", 429, "retryable_response", DeliveryDeadLetter},
		{"unavailable", 503, "retryable_response", DeliveryDeadLetter},
		{"transport failure", 0, "transport_error", DeliveryDeadLetter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &dispatcherObserver{}
			calls := 0
			sender := admissionSenderFunc(func(context.Context, SendRequest) (SendResult, error) {
				calls++
				if tc.status == 0 {
					return SendResult{}, errors.New("private transport detail")
				}
				return SendResult{Status: tc.status, RetryAfter: "120"}, nil
			})
			observed, err := NewService(store, s.cipher, sender, s.clock, s.ids, r)
			if err != nil {
				t.Fatal(err)
			}
			delivery, err := observed.Test(ctx, created.Endpoint.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			retained, err := store.GetDelivery(ctx, delivery.TenantID, delivery.EndpointID, delivery.EventID)
			if err != nil || retained.State != tc.state || retained.Attempt != 1 || retained.NextAttemptAt != nil || calls != 1 {
				t.Fatalf("API test must retain one terminal attempt: %+v, calls=%d, %v", retained, calls, err)
			}
			if len(r.items) != 1 || r.items[0].mode != "test" || r.items[0].outcome != tc.outcome || len(r.retries) != 0 {
				t.Fatalf("API test must observe only its sender outcome: %+v", r)
			}
		})
	}
}

func TestDispatcherRetryObservationRequiresPersistedWaitingDelivery(t *testing.T) {
	for _, scenario := range []string{"terminal", "exhausted", "save failure", "cancelled send", "defer failure"} {
		t.Run(scenario, func(t *testing.T) {
			d, store, _, _, outbox, endpoint := dispatcherAuditFixture(t)
			observer := &dispatcherObserver{}
			d.observer = observer
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("private storage detail")
			d.sender = admissionSenderFunc(func(context.Context, SendRequest) (SendResult, error) {
				if scenario == "cancelled send" {
					cancel()
				}
				status := 503
				if scenario == "terminal" {
					status = 401
				}
				return SendResult{Status: status, RetryAfter: "120"}, nil
			})
			switch scenario {
			case "exhausted":
				d.maxAttempts = 1
			case "save failure":
				d.store = dispatcherFaultStore{Store: store, saveErr: failure}
			case "defer failure":
				d.outbox = &dispatcherFaultOutbox{Outbox: outbox, releaseErr: failure}
			}
			_, err := d.DispatchOnce(ctx)
			wantErr := error(nil)
			if scenario == "cancelled send" {
				wantErr = context.Canceled
			} else if scenario == "save failure" || scenario == "defer failure" {
				wantErr = failure
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("dispatch = %v, want %v", err, wantErr)
			}
			retained, readErr := store.GetDelivery(t.Context(), endpoint.TenantID, endpoint.ID, outbox.events[0].ID)
			switch scenario {
			case "defer failure":
				if readErr != nil || retained.State != DeliveryWaiting || len(observer.retries) != 1 || observer.retries[0] != 2*time.Minute {
					t.Fatalf("persisted retry lost on later deferral error: %+v, %v, %v", retained, readErr, observer.retries)
				}
			case "save failure", "cancelled send":
				if catalog.ErrorCodeOf(readErr) != catalog.CodeNotFound || len(observer.retries) != 0 {
					t.Fatalf("uncommitted retry was observed: %v, %v", readErr, observer.retries)
				}
			default:
				if readErr != nil || retained.State != DeliveryDeadLetter || len(observer.retries) != 0 {
					t.Fatalf("terminal attempt was observed as retry: %+v, %v, %v", retained, readErr, observer.retries)
				}
			}
		})
	}
}

func TestDispatcherObservesActualSendsOnly(t *testing.T) {
	d, store, _, sender, outbox, endpoint := dispatcherAuditFixture(t)
	sender.results = []SendResult{{Status: 503, RetryAfter: "120"}, {Status: 204}}
	observer := &dispatcherObserver{}
	serviceClock, ok := d.clock.(*clock.Fake)
	if !ok {
		t.Fatal("dispatcher fixture requires a fake clock")
	}
	slowSender := admissionSenderFunc(func(ctx context.Context, request SendRequest) (SendResult, error) {
		serviceClock.Advance(2 * time.Second)
		return sender.Send(ctx, request)
	})
	observed, err := NewDispatcher(outbox, store, d.cipher, slowSender, d.clock, d.ids, d.owner, d.lease, d.maxAttempts, d.poll, d.retryCeiling, observer)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if completed, err := observed.DispatchOnce(t.Context()); err != nil || completed != 0 {
			t.Fatalf("retrying or idle batch = %d, %v", completed, err)
		}
	}
	if len(observer.items) != 1 || observer.items[0].mode != "worker" || observer.items[0].outcome != "retryable_response" || len(sender.Requests()) != 1 {
		t.Fatalf("waiting retry counted as a send: %#v", observer.items)
	}
	delivery, err := store.GetDelivery(t.Context(), endpoint.TenantID, endpoint.ID, outbox.events[0].ID)
	if err != nil || delivery.NextAttemptAt == nil {
		t.Fatalf("retry schedule: %#v, %v", delivery, err)
	}
	if len(observer.retries) != 1 || observer.retries[0] != 2*time.Minute || observer.retries[0] != delivery.NextAttemptAt.Sub(delivery.UpdatedAt) {
		t.Fatalf("retry observation must match persisted delay after slow send: %v", observer.retries)
	}
	serviceClock.Advance(delivery.NextAttemptAt.Sub(serviceClock.Now()))
	if completed, err := observed.DispatchOnce(t.Context()); err != nil || completed != 1 {
		t.Fatalf("completed batch = %d, %v", completed, err)
	}
	if len(observer.items) != 2 || observer.items[1].mode != "worker" || observer.items[1].outcome != "accepted" || len(sender.Requests()) != 2 {
		t.Fatalf("recovery observations = %#v", observer.items)
	}
	if len(observer.retries) != 1 {
		t.Fatalf("idle/successful dispatch recorded another retry: %v", observer.retries)
	}
	if len(observer.batches) != 3 {
		t.Fatalf("batch observations = %#v", observer.batches)
	}
	for i, batch := range observer.batches {
		want := 0
		if i == 2 {
			want = 1
		}
		if batch.failed || batch.processed != want {
			t.Fatalf("batch %d = %#v", i, batch)
		}
	}
}
