package webhook

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

func dispatcherAuditFixture(t *testing.T) (*Dispatcher, *MemoryStore, *webhookAuditSink, *recordingSender, *memoryOutbox, Endpoint) {
	t.Helper()
	s, store, sink, ctx := auditFixture(t)
	created, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := created.Endpoint
	data, err := events.MarshalReference("resource.created", "resource", endpoint.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000999", TenantID: endpoint.TenantID, Type: events.CanonicalType("resource.created"), Subject: endpoint.ID, Data: data, CreatedAt: s.clock.Now()}
	outbox := &memoryOutbox{events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time)}
	sender := &recordingSender{}
	d, err := newTestDispatcher(outbox, store, s.cipher, sender, s.clock, s.ids, "test-worker", time.Minute, 3, time.Second, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return d, store, sink, sender, outbox, endpoint
}

func TestDispatcherRestartRetainsScheduleAndUsesCurrentAttemptLimit(t *testing.T) {
	d, store, sink, sender, outbox, endpoint := dispatcherAuditFixture(t)
	sender.results = []SendResult{{Status: 429, RetryAfter: "120"}, {Status: 503}}
	ctx := context.Background()
	now := d.clock.Now()
	if n, err := d.DispatchOnce(ctx); err != nil || n != 0 {
		t.Fatalf("initial dispatch = %d, %v", n, err)
	}
	retained, err := store.GetDelivery(ctx, endpoint.TenantID, endpoint.ID, outbox.events[0].ID)
	if err != nil || retained.Attempt != 1 || retained.NextAttemptAt == nil || !retained.NextAttemptAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("retained retry = %#v, %v", retained, err)
	}
	audits := len(sink.records)
	// A replacement dispatcher has a lower cap and budget. It must honor the
	// persisted timestamp, then apply its current limit to the next failure.
	restarted, err := newTestDispatcher(outbox, store, d.cipher, sender, d.clock, d.ids, "replacement-worker", d.lease, 2, d.poll, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	serviceClock, ok := d.clock.(*clock.Fake)
	if !ok {
		t.Fatal("dispatcher fixture requires a fake clock")
	}
	serviceClock.Advance(2*time.Minute - time.Nanosecond)
	if n, err := restarted.DispatchOnce(ctx); err != nil || n != 0 || len(sender.Requests()) != 1 || len(sink.records) != audits {
		t.Fatalf("early dispatch changed sends or evidence: %d, %v", n, err)
	}
	got, err := store.GetDelivery(ctx, endpoint.TenantID, endpoint.ID, retained.EventID)
	if err != nil || !reflect.DeepEqual(got, retained) {
		t.Fatalf("restart rewrote retained delivery: %#v, %v", got, err)
	}
	serviceClock.Advance(time.Nanosecond)
	if n, err := restarted.DispatchOnce(ctx); err != nil || n != 1 {
		t.Fatalf("due dispatch = %d, %v", n, err)
	}
	got, err = store.GetDelivery(ctx, endpoint.TenantID, endpoint.ID, retained.EventID)
	if err != nil || got.ID != retained.ID || got.Attempt != 2 || got.State != DeliveryDeadLetter || got.NextAttemptAt != nil || got.ResponseStatus != 503 || len(sink.records) != audits+1 {
		t.Fatalf("current limit did not exhaust retained delivery: %#v, %v", got, err)
	}
	requests := sender.Requests()
	if len(requests) != 2 || requests[0].Headers["X-Catalog-Delivery-ID"] != requests[1].Headers["X-Catalog-Delivery-ID"] || !bytes.Equal(requests[0].Body, requests[1].Body) {
		t.Fatal("retry changed delivery identity or event bytes")
	}
	// A larger budget on a later restart cannot revive a terminal delivery.
	restarted, err = newTestDispatcher(outbox, store, d.cipher, sender, d.clock, d.ids, "later-worker", d.lease, 20, d.poll, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := restarted.DispatchOnce(ctx); err != nil || n != 0 || len(sender.Requests()) != 2 || len(sink.records) != audits+1 {
		t.Fatalf("restart revived terminal work: %d, %v", n, err)
	}
}

// Models another committed delivery outcome between endpoint selection and the
// exact delivery read, without claiming to qualify PostgreSQL lease ownership.
type selectionInterleavingStore struct {
	Store
	afterSelection func() error
}

func (s selectionInterleavingStore) ListPendingEndpoints(ctx context.Context, event catalog.OutboxEvent, limit int) ([]Endpoint, error) {
	items, err := s.Store.ListPendingEndpoints(ctx, event, limit)
	if err != nil {
		return nil, err
	}
	if err := s.afterSelection(); err != nil {
		return nil, err
	}
	return items, nil
}

func TestDispatcherRechecksDeliveryAfterEndpointSelection(t *testing.T) {
	for _, state := range []DeliveryState{DeliverySucceeded, DeliveryDeadLetter, DeliveryWaiting} {
		t.Run(string(state), func(t *testing.T) {
			d, store, sink, sender, outbox, endpoint := dispatcherAuditFixture(t)
			next := d.clock.Now().Add(2 * time.Minute)
			delivery := Delivery{ID: "00000000-0000-7000-8000-000000000998", TenantID: endpoint.TenantID, EndpointID: endpoint.ID, EventID: outbox.events[0].ID, EventType: outbox.events[0].Type, Attempt: 1, State: state, ResponseStatus: 410, CreatedAt: d.clock.Now(), UpdatedAt: d.clock.Now()}
			if state == DeliverySucceeded {
				delivery.ResponseStatus = 204
			}
			if state == DeliveryWaiting {
				delivery.ResponseStatus = 429
				delivery.NextAttemptAt = &next
			}
			d.store = selectionInterleavingStore{Store: store, afterSelection: func() error {
				_, err := store.SaveDelivery(context.Background(), delivery)
				return err
			}}
			n, err := d.DispatchOnce(context.Background())
			if err != nil || len(sender.Requests()) != 0 || len(sink.records) != 1 {
				t.Fatalf("unexpected send or evidence: n=%d err=%v sends=%d audits=%d", n, err, len(sender.Requests()), len(sink.records))
			}
			if state == DeliveryWaiting {
				if n != 0 || len(outbox.published) != 0 || outbox.events[0].DispatchAfter == nil || !outbox.events[0].DispatchAfter.Equal(next) {
					t.Fatal("waiting delivery was acknowledged or its schedule lost")
				}
			} else if n != 1 || len(outbox.published) != 1 {
				t.Fatal("terminal delivery was not acknowledged")
			}
		})
	}
}

func TestDispatcherAuditFailureDoesNotAcknowledgeSentRequest(t *testing.T) {
	for _, failure := range []string{"identifier", "persistence"} {
		t.Run(failure, func(t *testing.T) {
			d, store, sink, sender, outbox, endpoint := dispatcherAuditFixture(t)
			observer := &recordingOutboxObserver{}
			d.observer = observer
			if failure == "identifier" {
				d.ids = &auditFailingIDs{failAt: 2}
			} else {
				sink.fail = true
			}
			n, err := d.DispatchOnce(context.Background())
			if err == nil || n != 0 || len(sender.Requests()) != 1 || len(outbox.published) != 0 || len(sink.records) != 1 {
				t.Fatal("audit failure hid a sent request or acknowledged incomplete evidence")
			}
			if _, err := store.GetDelivery(context.Background(), endpoint.TenantID, endpoint.ID, outbox.events[0].ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("failed outcome persisted: %v", err)
			}
			if len(observer.batches) != 1 || !observer.batches[0].failed || observer.batches[0].processed != 0 {
				t.Fatal("audit failure was not reported as failed batch")
			}
		})
	}
}

func TestDispatcherReportsOnlyCompletedBatchCount(t *testing.T) {
	for _, scenario := range []string{"idle", "succeeded", "retry_waiting", "claim_failure"} {
		t.Run(scenario, func(t *testing.T) {
			d, _, _, sender, outbox, _ := dispatcherAuditFixture(t)
			observer := &recordingOutboxObserver{}
			d.observer = observer
			switch scenario {
			case "idle":
				outbox.events = nil
			case "retry_waiting":
				sender.results = []SendResult{{Status: 429, RetryAfter: "120"}}
			case "claim_failure":
				outbox.claimErr = errors.New("injected claim failure")
			}
			n, err := d.DispatchOnce(context.Background())
			want := 0
			if scenario == "succeeded" {
				want = 1
			}
			if n != want || (err != nil) != (scenario == "claim_failure") || len(observer.batches) != 1 || observer.batches[0].processed != want || observer.batches[0].failed != (err != nil) {
				t.Fatalf("batch reporting: n=%d err=%v observations=%v", n, err, observer.batches)
			}
			// A queued retry is not an acknowledged delivery. This observer is
			// deliberately not the pending F-08 receiver-health metric contract.
			if scenario == "retry_waiting" && (len(outbox.published) != 0 || outbox.events[0].DispatchAfter == nil) {
				t.Fatal("waiting retry lost durable scheduling")
			}
		})
	}
}

func TestDisabledDrainerRunReturnsFailureAndContinuesProductiveWork(t *testing.T) {
	for _, failure := range []bool{false, true} {
		d, _, _, _, outbox, _ := dispatcherAuditFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		fault := errors.New("injected drainer failure")
		wrapped := &dispatcherFaultOutbox{Outbox: outbox, afterPublish: cancel}
		if failure {
			wrapped.claimErr = fault
		}
		drainer, err := NewDrainer(wrapped, d.clock, "disabled-worker", time.Minute, time.Hour)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		err = drainer.Run(ctx)
		cancel()
		if failure {
			if !errors.Is(err, fault) || len(outbox.published) != 0 {
				t.Fatal("drainer swallowed failure")
			}
		} else if !errors.Is(err, context.Canceled) || len(outbox.published) != 1 {
			t.Fatal("productive drain did not acknowledge then stop")
		}
	}
}
