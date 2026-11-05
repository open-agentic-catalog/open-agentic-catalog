package webhook

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

type cancellingWebhookSender struct{ cancel context.CancelFunc }

type changedSelectionStore struct {
	Store
	readErr error
}

func (s changedSelectionStore) ListPendingEndpoints(ctx context.Context, event catalog.OutboxEvent, limit int) ([]Endpoint, error) {
	items, err := s.Store.ListPendingEndpoints(ctx, event, limit)
	if err != nil || len(items) == 0 {
		return items, err
	}
	changed := items[0]
	changed.Status = StatusDisabled
	changed.Revision++
	if _, err := s.Store.UpdateEndpoint(ctx, changed, items[0].Revision); err != nil {
		return nil, err
	}
	return items, nil
}

func (s changedSelectionStore) GetEndpoint(ctx context.Context, tenant, id string) (Endpoint, error) {
	if s.readErr != nil {
		return Endpoint{}, s.readErr
	}
	return s.Store.GetEndpoint(ctx, tenant, id)
}

func TestDispatcherRechecksEndpointAfterSelection(t *testing.T) {
	for _, readErr := range []error{nil, errors.New("endpoint reload failed")} {
		now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
		serviceClock := clock.NewFake(now)
		cipher, _ := NewAESGCM(keyBytes(8))
		store := NewMemoryStore()
		sender := &recordingSender{}
		service, _ := NewService(store, cipher, sender, serviceClock, &sequenceIDs{})
		ctx := adminContext("00000000-0000-7000-8000-000000000001")
		created, err := service.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
		if err != nil {
			t.Fatal(err)
		}
		event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000500", TenantID: created.Endpoint.TenantID, Type: catalog.CanonicalEventType("resource.created"), CreatedAt: now}
		outbox := &memoryOutbox{events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time)}
		dispatcher, _ := newTestDispatcher(outbox, changedSelectionStore{Store: store, readErr: readErr}, cipher, sender, serviceClock, &sequenceIDs{}, "worker", time.Minute, 3, time.Second, time.Hour)
		n, err := dispatcher.DispatchOnce(ctx)
		if !errors.Is(err, readErr) || len(sender.Requests()) != 0 || (readErr == nil && n != 1) || (readErr != nil && n != 0) {
			t.Fatalf("stale selection: completed=%d err=%v sends=%d", n, err, len(sender.Requests()))
		}
	}
}

func (s cancellingWebhookSender) Send(ctx context.Context, _ SendRequest) (SendResult, error) {
	s.cancel()
	return SendResult{}, ctx.Err()
}

func TestCancelledWebhookSendDoesNotConsumeAttempt(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	cipher, _ := NewAESGCM(keyBytes(8))
	store := NewMemoryStore()
	ctx, cancel := context.WithCancel(adminContext("00000000-0000-7000-8000-000000000001"))
	defer cancel()
	service, _ := NewService(store, cipher, cancellingWebhookSender{cancel}, serviceClock, &sequenceIDs{})
	created, err := service.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Test(ctx, created.Endpoint.ID, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled test: %v", err)
	}
	if len(store.deliveries) != 0 {
		t.Fatal("cancelled test persisted a terminal delivery")
	}
	event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000500", TenantID: created.Endpoint.TenantID, Type: catalog.CanonicalEventType("webhook.test"), Subject: created.Endpoint.ID, Data: []byte(`{"action":"webhook.test","targetType":"webhook_endpoint","targetId":"` + created.Endpoint.ID + `"}`), CreatedAt: now}
	outbox := &memoryOutbox{events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time)}
	dispatchCtx, dispatchCancel := context.WithCancel(context.Background())
	defer dispatchCancel()
	dispatcher, _ := newTestDispatcher(outbox, store, cipher, cancellingWebhookSender{dispatchCancel}, serviceClock, &sequenceIDs{}, "worker", time.Minute, 3, time.Second, time.Hour)
	if _, err := dispatcher.DispatchOnce(dispatchCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled dispatch: %v", err)
	}
	if len(store.deliveries) != 0 || len(outbox.published) != 0 {
		t.Fatal("cancelled dispatch consumed or acknowledged an attempt")
	}
}

func TestDispatcherBoundedContinuationAndSlowPolling(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	cipher, _ := NewAESGCM(keyBytes(8))
	secret, _ := cipher.Encrypt([]byte("whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	store := NewMemoryStore()
	ctx := context.Background()
	event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000500", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("resource.created"), Subject: "00000000-0000-7000-8000-000000000002", Data: []byte(`{"action":"resource.created","targetType":"resource","targetId":"00000000-0000-7000-8000-000000000002"}`), CreatedAt: now}
	for i := 0; i < 25; i++ {
		endpoint := Endpoint{ID: fmt.Sprintf("00000000-0000-7000-8000-%012d", 100+i), TenantID: event.TenantID, Name: "Receiver", URL: fmt.Sprintf("https://receiver.example/%d", i), Status: StatusActive, EventTypes: []string{"*"}, SecretVersion: 1, SecretCiphertext: secret, CreatedAt: now}
		if _, err := store.CreateEndpoint(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
	}
	outbox := &memoryOutbox{events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time)}
	sender := &recordingSender{}
	dispatcher, err := newTestDispatcher(outbox, store, cipher, sender, serviceClock, &sequenceIDs{}, "worker", time.Minute, 3, 2*time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		n, err := dispatcher.DispatchOnce(ctx)
		if err != nil || len(sender.Requests()) != min((i+1)*8, 25) || (i < 3 && n != 0) || (i == 3 && n != 1) {
			t.Fatalf("slice %d: completed=%d requests=%d err=%v", i, n, len(sender.Requests()), err)
		}
		if i < 3 && (outbox.events[0].DispatchAfter == nil || !outbox.events[0].DispatchAfter.Equal(serviceClock.Now().Add(time.Hour))) {
			t.Fatal("slow poll exceeded the durable deferral limit")
		}
		serviceClock.Advance(time.Hour)
	}
	if items, err := store.ListPendingEndpoints(ctx, event, 9); err != nil || len(items) != 0 {
		t.Fatal("terminal deliveries remained in pending selection")
	}
	if _, err := store.ListPendingEndpoints(ctx, event, 10); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("unbounded endpoint selection accepted")
	}
	if earlierAttempt(now, now.Add(time.Second)) != now || earlierAttempt(now, now.Add(-time.Second)) != now.Add(-time.Second) {
		t.Fatal("earliest retry selection is incorrect")
	}
	for _, ceiling := range []time.Duration{0, 4 * time.Second, time.Hour + time.Second} {
		if _, err := newTestDispatcher(outbox, store, cipher, sender, serviceClock, &sequenceIDs{}, "worker", time.Minute, 3, time.Second, ceiling); err == nil {
			t.Fatal("invalid retry ceiling accepted")
		}
	}
}

func TestPendingEndpointsOrderByRetryAndFilterBeforeLimit(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	event := catalog.OutboxEvent{ID: "event", TenantID: "tenant", Type: catalog.CanonicalEventType("resource.created"), CreatedAt: now}
	for i, endpoint := range []Endpoint{
		{ID: "later", TenantID: "tenant", Status: StatusActive, EventTypes: []string{"*"}, CreatedAt: now},
		{ID: "first", TenantID: "tenant", Status: StatusActive, EventTypes: []string{"*"}, CreatedAt: now},
		{ID: "future", TenantID: "tenant", Status: StatusActive, EventTypes: []string{"*"}, CreatedAt: now.Add(time.Second)},
		{ID: "foreign", TenantID: "foreign", Status: StatusActive, EventTypes: []string{"*"}, CreatedAt: now},
	} {
		endpoint.URL = fmt.Sprintf("https://receiver.example/%d", i)
		if _, err := store.CreateEndpoint(context.Background(), endpoint); err != nil {
			t.Fatal(err)
		}
	}
	for endpoint, after := range map[string]time.Duration{"later": 2 * time.Minute, "first": time.Minute} {
		at := now.Add(after)
		if _, err := store.SaveDelivery(context.Background(), Delivery{EndpointID: endpoint, EventID: event.ID, TenantID: event.TenantID, State: DeliveryPending, NextAttemptAt: &at}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListPendingEndpoints(context.Background(), event, 1)
	if err != nil || len(items) != 1 || items[0].ID != "first" {
		t.Fatalf("bounded due order: %v %v", items, err)
	}
}
