package webhook

import (
	"context"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

type pauseAfterOutcomeStore struct {
	Store
	clock *clock.Fake
}

func (s pauseAfterOutcomeStore) SaveClaimedDelivery(ctx context.Context, delivery Delivery, owner string, serviceClock catalog.Clock, record audit.Record) (Delivery, error) {
	result, err := s.Store.SaveClaimedDelivery(ctx, delivery, owner, serviceClock, record)
	if err == nil {
		s.clock.Advance(20 * time.Second)
	}
	return result, err
}

func TestDispatcherSchedulesInFutureAfterOutcomePersistencePause(t *testing.T) {
	d, store, _, sender, outbox, _ := dispatcherAuditFixture(t)
	serviceClock, ok := d.clock.(*clock.Fake)
	if !ok {
		t.Fatal("fixture does not use a fake clock")
	}
	d.store = pauseAfterOutcomeStore{Store: store, clock: serviceClock}
	sender.results = []SendResult{{Status: 503}, {Status: 204}}
	if n, err := d.DispatchOnce(t.Context()); err != nil || n != 0 {
		t.Fatalf("delayed persistence: %d %v", n, err)
	}
	if due := outbox.events[0].DispatchAfter; due == nil || !due.Equal(serviceClock.Now().Add(d.poll)) {
		t.Fatalf("expired retry date was not advanced to the next poll: %v", due)
	}
	if n, err := d.DispatchOnce(t.Context()); err != nil || n != 0 || len(sender.Requests()) != 1 {
		t.Fatalf("retry bypassed durable deferral: %d %v", n, err)
	}
	serviceClock.Advance(d.poll)
	if n, err := d.DispatchOnce(t.Context()); err != nil || n != 1 || len(sender.Requests()) != 2 {
		t.Fatalf("deferred retry did not recover: %d %v", n, err)
	}
}

func TestSyntheticDeliveryIdentifierFailureHasNoSideEffects(t *testing.T) {
	s, store, sink, ctx := auditFixture(t)
	created, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	s.ids = &auditFailingIDs{failAt: 2}
	if _, err := s.Test(ctx, created.Endpoint.ID, 1); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("delivery identifier failure: %v", err)
	}
	sender, ok := s.sender.(*recordingSender)
	if !ok {
		t.Fatal("fixture does not use a recording sender")
	}
	if len(store.deliveries) != 0 || len(sink.records) != 1 || len(sender.Requests()) != 0 {
		t.Fatal("identifier failure sent a request or retained partial evidence")
	}
}

func TestAmbiguousMemoryEvidenceSinksFailClosed(t *testing.T) {
	store := NewMemoryStore(&webhookAuditSink{}, &webhookAuditSink{})
	s, _, _, ctx := auditFixture(t)
	s.store = store
	if _, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}}); catalog.ErrorCodeOf(err) != catalog.CodeInternal || len(store.endpoints) != 0 {
		t.Fatalf("ambiguous audit authority accepted creation: %v", err)
	}
}
