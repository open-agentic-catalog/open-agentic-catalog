package webhook

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

type failingCipher struct{}

func (failingCipher) Encrypt([]byte) ([]byte, error) { return nil, errors.New("encrypt failed") }
func (failingCipher) Decrypt([]byte) ([]byte, error) { return nil, errors.New("decrypt failed") }

type recordingOutboxObserver struct {
	batches []struct {
		processed int
		failed    bool
	}
}

func (o *recordingOutboxObserver) OutboxBatch(processed int, err error) {
	o.batches = append(o.batches, struct {
		processed int
		failed    bool
	}{processed: processed, failed: err != nil})
}

func TestOutboxProcessorsReportBoundedBatchOutcomes(t *testing.T) {
	serviceClock := clock.NewFake(time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC))
	outbox := &memoryOutbox{published: make(map[string]time.Time)}
	observer := &recordingOutboxObserver{}
	drainer, err := NewDrainer(outbox, serviceClock, "observer-worker", time.Minute, time.Second, observer)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := drainer.DrainOnce(context.Background()); err != nil || processed != 0 {
		t.Fatalf("idle drain = %d, %v", processed, err)
	}
	outbox.claimErr = errors.New("private database failure")
	if _, err := drainer.DrainOnce(context.Background()); err == nil {
		t.Fatal("failed drain returned no error")
	}
	if len(observer.batches) != 2 || observer.batches[0].processed != 0 || observer.batches[0].failed || !observer.batches[1].failed {
		t.Fatalf("drainer observations = %#v", observer.batches)
	}
	if _, err := NewDrainer(outbox, serviceClock, "observer-worker", time.Minute, time.Second, observer, observer); err == nil {
		t.Fatal("drainer accepted more than one observer")
	}

	store := NewMemoryStore()
	cipher, err := NewAESGCM(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newTestDispatcher(outbox, store, cipher, &recordingSender{}, serviceClock, &sequenceIDs{}, "observer-dispatcher", time.Minute, 3, time.Second, time.Hour, observer, observer); err == nil {
		t.Fatal("dispatcher accepted more than one observer")
	}
}

type dispatcherFaultOutbox struct {
	Outbox
	claimErr     error
	renewErr     error
	renewErrCall int
	renewCalls   int
	publishErr   error
	releaseErr   error
	afterPublish func()
}

func (o *dispatcherFaultOutbox) ClaimOutbox(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]catalog.OutboxEvent, error) {
	if o.claimErr != nil {
		return nil, o.claimErr
	}
	return o.Outbox.ClaimOutbox(ctx, owner, now, lease, limit)
}

func (o *dispatcherFaultOutbox) RenewOutbox(ctx context.Context, tenantID, eventID, owner string, now time.Time, lease time.Duration) error {
	o.renewCalls++
	if o.renewErr != nil && (o.renewErrCall == 0 || o.renewCalls == o.renewErrCall) {
		return o.renewErr
	}
	return o.Outbox.RenewOutbox(ctx, tenantID, eventID, owner, now, lease)
}

func (o *dispatcherFaultOutbox) MarkOutboxPublished(ctx context.Context, tenantID, eventID, owner string, now time.Time) error {
	if o.publishErr != nil {
		return o.publishErr
	}
	if err := o.Outbox.MarkOutboxPublished(ctx, tenantID, eventID, owner, now); err != nil {
		return err
	}
	if o.afterPublish != nil {
		o.afterPublish()
	}
	return nil
}

func (o *dispatcherFaultOutbox) ReleaseOutbox(ctx context.Context, tenantID, eventID, owner string) error {
	if o.releaseErr != nil {
		return o.releaseErr
	}
	return o.Outbox.ReleaseOutbox(ctx, tenantID, eventID, owner)
}

func (o *dispatcherFaultOutbox) DeferOutbox(ctx context.Context, tenantID, eventID, owner string, now, notBefore time.Time) error {
	if o.releaseErr != nil {
		return o.releaseErr
	}
	return o.Outbox.DeferOutbox(ctx, tenantID, eventID, owner, now, notBefore)
}

type dispatcherFaultStore struct {
	Store
	listErr error
	getErr  error
	saveErr error
}

func (s dispatcherFaultStore) ListPendingEndpoints(ctx context.Context, event catalog.OutboxEvent, limit int) ([]Endpoint, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.Store.ListPendingEndpoints(ctx, event, limit)
}

func (s dispatcherFaultStore) GetDelivery(ctx context.Context, tenantID, endpointID, eventID string) (Delivery, error) {
	if s.getErr != nil {
		return Delivery{}, s.getErr
	}
	return s.Store.GetDelivery(ctx, tenantID, endpointID, eventID)
}

func (s dispatcherFaultStore) SaveDelivery(ctx context.Context, delivery Delivery, records ...audit.Record) (Delivery, error) {
	if s.saveErr != nil {
		return Delivery{}, s.saveErr
	}
	return s.Store.SaveDelivery(ctx, delivery, records...)
}

type dispatcherFailingIDs struct{ err error }

func (i dispatcherFailingIDs) New(time.Time) (string, error) { return "", i.err }

func TestWebhookConstructorsAndAuthorizationBoundaries(t *testing.T) {
	t.Parallel()

	serviceClock := clock.NewFake(time.Now())
	store := NewMemoryStore()
	cipher, err := NewAESGCM(keyBytes(4))
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{}
	ids := &sequenceIDs{}
	invalidServiceConstructors := []func() (*Service, error){
		func() (*Service, error) { return NewService(nil, cipher, sender, serviceClock, ids) },
		func() (*Service, error) { return NewService(store, nil, sender, serviceClock, ids) },
		func() (*Service, error) { return NewService(store, cipher, nil, serviceClock, ids) },
		func() (*Service, error) { return NewService(store, cipher, sender, nil, ids) },
		func() (*Service, error) { return NewService(store, cipher, sender, serviceClock, nil) },
	}
	for index, create := range invalidServiceConstructors {
		if _, err := create(); err == nil {
			t.Fatalf("service accepted missing dependency %d", index)
		}
	}
	service, err := NewService(store, cipher, sender, serviceClock, ids)
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"create":  func() error { _, err := service.Create(context.Background(), CreateInput{}); return err },
		"get":     func() error { _, err := service.Get(context.Background(), "missing"); return err },
		"list":    func() error { _, err := service.List(context.Background(), EndpointListRequest{Limit: 20}); return err },
		"update":  func() error { _, err := service.Update(context.Background(), "missing", PatchInput{}, 1); return err },
		"disable": func() error { _, err := service.Disable(context.Background(), "missing", 1); return err },
		"rotate": func() error {
			_, err := service.RotateSecret(context.Background(), "missing", 1, RotateInput{})
			return err
		},
		"test": func() error { _, err := service.Test(context.Background(), "missing", 1); return err },
		"deliveries": func() error {
			_, err := service.ListDeliveries(context.Background(), DeliveryListRequest{EndpointID: "00000000-0000-7000-8000-000000000001", Limit: 10})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
				t.Fatalf("unauthorized call returned %v", err)
			}
		})
	}

	outbox := &memoryOutbox{published: make(map[string]time.Time)}
	dispatcherArgs := struct {
		outbox      Outbox
		store       Store
		cipher      Cipher
		sender      Sender
		clock       Clock
		ids         IDGenerator
		owner       string
		lease       time.Duration
		maxAttempts int
		poll        time.Duration
	}{outbox, store, cipher, sender, serviceClock, ids, "owner", time.Minute, 3, time.Millisecond}
	invalid := []func() (*Dispatcher, error){
		func() (*Dispatcher, error) {
			return newTestDispatcher(nil, dispatcherArgs.store, dispatcherArgs.cipher, dispatcherArgs.sender, dispatcherArgs.clock, dispatcherArgs.ids, dispatcherArgs.owner, dispatcherArgs.lease, dispatcherArgs.maxAttempts, dispatcherArgs.poll, time.Hour)
		},
		func() (*Dispatcher, error) {
			return newTestDispatcher(dispatcherArgs.outbox, nil, dispatcherArgs.cipher, dispatcherArgs.sender, dispatcherArgs.clock, dispatcherArgs.ids, dispatcherArgs.owner, dispatcherArgs.lease, dispatcherArgs.maxAttempts, dispatcherArgs.poll, time.Hour)
		},
		func() (*Dispatcher, error) {
			return newTestDispatcher(dispatcherArgs.outbox, dispatcherArgs.store, nil, dispatcherArgs.sender, dispatcherArgs.clock, dispatcherArgs.ids, dispatcherArgs.owner, dispatcherArgs.lease, dispatcherArgs.maxAttempts, dispatcherArgs.poll, time.Hour)
		},
		func() (*Dispatcher, error) {
			return newTestDispatcher(dispatcherArgs.outbox, dispatcherArgs.store, dispatcherArgs.cipher, nil, dispatcherArgs.clock, dispatcherArgs.ids, dispatcherArgs.owner, dispatcherArgs.lease, dispatcherArgs.maxAttempts, dispatcherArgs.poll, time.Hour)
		},
		func() (*Dispatcher, error) {
			return newTestDispatcher(dispatcherArgs.outbox, dispatcherArgs.store, dispatcherArgs.cipher, dispatcherArgs.sender, nil, dispatcherArgs.ids, dispatcherArgs.owner, dispatcherArgs.lease, dispatcherArgs.maxAttempts, dispatcherArgs.poll, time.Hour)
		},
		func() (*Dispatcher, error) {
			return newTestDispatcher(dispatcherArgs.outbox, dispatcherArgs.store, dispatcherArgs.cipher, dispatcherArgs.sender, dispatcherArgs.clock, nil, dispatcherArgs.owner, dispatcherArgs.lease, dispatcherArgs.maxAttempts, dispatcherArgs.poll, time.Hour)
		},
		func() (*Dispatcher, error) {
			return newTestDispatcher(dispatcherArgs.outbox, dispatcherArgs.store, dispatcherArgs.cipher, dispatcherArgs.sender, dispatcherArgs.clock, dispatcherArgs.ids, "", dispatcherArgs.lease, dispatcherArgs.maxAttempts, dispatcherArgs.poll, time.Hour)
		},
		func() (*Dispatcher, error) {
			return newTestDispatcher(dispatcherArgs.outbox, dispatcherArgs.store, dispatcherArgs.cipher, dispatcherArgs.sender, dispatcherArgs.clock, dispatcherArgs.ids, dispatcherArgs.owner, 0, dispatcherArgs.maxAttempts, dispatcherArgs.poll, time.Hour)
		},
		func() (*Dispatcher, error) {
			return newTestDispatcher(dispatcherArgs.outbox, dispatcherArgs.store, dispatcherArgs.cipher, dispatcherArgs.sender, dispatcherArgs.clock, dispatcherArgs.ids, dispatcherArgs.owner, dispatcherArgs.lease, 21, dispatcherArgs.poll, time.Hour)
		},
		func() (*Dispatcher, error) {
			return newTestDispatcher(dispatcherArgs.outbox, dispatcherArgs.store, dispatcherArgs.cipher, dispatcherArgs.sender, dispatcherArgs.clock, dispatcherArgs.ids, dispatcherArgs.owner, dispatcherArgs.lease, dispatcherArgs.maxAttempts, 0, time.Hour)
		},
	}
	for index, create := range invalid {
		if _, err := create(); err == nil {
			t.Fatalf("dispatcher accepted invalid dependency set %d", index)
		}
	}
	dispatcher, err := newTestDispatcher(outbox, store, cipher, sender, serviceClock, ids, "owner", time.Minute, 3, time.Millisecond, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := dispatcher.Run(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled dispatcher returned %v", err)
	}
	for index, create := range []func() (*Drainer, error){
		func() (*Drainer, error) { return NewDrainer(nil, serviceClock, "owner", time.Minute, time.Second) },
		func() (*Drainer, error) { return NewDrainer(outbox, nil, "owner", time.Minute, time.Second) },
		func() (*Drainer, error) { return NewDrainer(outbox, serviceClock, "", time.Minute, time.Second) },
		func() (*Drainer, error) { return NewDrainer(outbox, serviceClock, "owner", 0, time.Second) },
		func() (*Drainer, error) { return NewDrainer(outbox, serviceClock, "owner", time.Minute, 0) },
	} {
		if _, err := create(); err == nil {
			t.Fatalf("drainer accepted invalid dependency set %d", index)
		}
	}
}

func TestDispatcherDependencyFailuresNeverAcknowledgeAnEvent(t *testing.T) {
	failure := errors.New("injected dispatcher dependency failure")
	tests := []struct {
		name      string
		internal  bool
		configure func(*dispatcherFaultOutbox, *dispatcherFaultStore, *recordingSender) IDGenerator
	}{
		{name: "claim", configure: func(outbox *dispatcherFaultOutbox, _ *dispatcherFaultStore, _ *recordingSender) IDGenerator {
			outbox.claimErr = failure
			return nil
		}},
		{name: "initial lease renewal", configure: func(outbox *dispatcherFaultOutbox, _ *dispatcherFaultStore, _ *recordingSender) IDGenerator {
			outbox.renewErr = failure
			outbox.renewErrCall = 1
			return nil
		}},
		{name: "internal event acknowledgement", internal: true, configure: func(outbox *dispatcherFaultOutbox, _ *dispatcherFaultStore, _ *recordingSender) IDGenerator {
			outbox.publishErr = failure
			return nil
		}},
		{name: "endpoint listing", configure: func(_ *dispatcherFaultOutbox, store *dispatcherFaultStore, _ *recordingSender) IDGenerator {
			store.listErr = failure
			return nil
		}},
		{name: "delivery lookup", configure: func(_ *dispatcherFaultOutbox, store *dispatcherFaultStore, _ *recordingSender) IDGenerator {
			store.getErr = failure
			return nil
		}},
		{name: "delivery identifier", configure: func(_ *dispatcherFaultOutbox, _ *dispatcherFaultStore, _ *recordingSender) IDGenerator {
			return dispatcherFailingIDs{err: failure}
		}},
		{name: "delivery lease renewal", configure: func(outbox *dispatcherFaultOutbox, _ *dispatcherFaultStore, _ *recordingSender) IDGenerator {
			outbox.renewErr = failure
			outbox.renewErrCall = 2
			return nil
		}},
		{name: "delivery persistence", configure: func(_ *dispatcherFaultOutbox, store *dispatcherFaultStore, _ *recordingSender) IDGenerator {
			store.saveErr = failure
			return nil
		}},
		{name: "completed event acknowledgement", configure: func(outbox *dispatcherFaultOutbox, _ *dispatcherFaultStore, _ *recordingSender) IDGenerator {
			outbox.publishErr = failure
			return nil
		}},
		{name: "retry release", configure: func(outbox *dispatcherFaultOutbox, _ *dispatcherFaultStore, sender *recordingSender) IDGenerator {
			outbox.releaseErr = failure
			sender.results = []SendResult{{Status: http.StatusServiceUnavailable}}
			return nil
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
			serviceClock := clock.NewFake(now)
			store := NewMemoryStore()
			cipher, err := NewAESGCM(keyBytes(8))
			if err != nil {
				t.Fatal(err)
			}
			sender := &recordingSender{}
			ids := &sequenceIDs{}
			service, err := NewService(store, cipher, sender, serviceClock, ids)
			if err != nil {
				t.Fatal(err)
			}
			tenantID := "00000000-0000-7000-8000-000000000001"
			if _, err := service.Create(adminContext(tenantID), CreateInput{Name: "Catalog events", URL: "https://hooks.example.com/catalog", EventTypes: []string{"*"}}); err != nil {
				t.Fatal(err)
			}
			event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000201", TenantID: tenantID, Type: catalog.CanonicalEventType("resource.created"), Subject: "00000000-0000-7000-8000-000000000202", Data: testPublicReference(t, "resource", "00000000-0000-7000-8000-000000000202"), Internal: test.internal, CreatedAt: now.Add(time.Second)}
			baseOutbox := &memoryOutbox{events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time)}
			faultOutbox := &dispatcherFaultOutbox{Outbox: baseOutbox}
			faultStore := &dispatcherFaultStore{Store: store}
			dispatcherIDs := test.configure(faultOutbox, faultStore, sender)
			if dispatcherIDs == nil {
				dispatcherIDs = ids
			}
			dispatcher, err := newTestDispatcher(faultOutbox, faultStore, cipher, sender, serviceClock, dispatcherIDs, "dispatcher-one", time.Minute, 3, time.Millisecond, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			completed, err := dispatcher.DispatchOnce(context.Background())
			if !errors.Is(err, failure) || completed != 0 {
				t.Fatalf("dispatch = %d, %v", completed, err)
			}
			if _, acknowledged := baseOutbox.published[event.ID]; acknowledged {
				t.Fatal("event was acknowledged after a dependency failure")
			}
		})
	}
}

func TestDispatcherRestartRecognizesTerminalDeliveries(t *testing.T) {
	for _, state := range []DeliveryState{DeliverySucceeded, DeliveryDeadLetter} {
		t.Run(string(state), func(t *testing.T) {
			now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
			serviceClock := clock.NewFake(now)
			store := NewMemoryStore()
			cipher, err := NewAESGCM(keyBytes(8))
			if err != nil {
				t.Fatal(err)
			}
			sender := &recordingSender{}
			ids := &sequenceIDs{}
			service, err := NewService(store, cipher, sender, serviceClock, ids)
			if err != nil {
				t.Fatal(err)
			}
			tenantID := "00000000-0000-7000-8000-000000000001"
			created, err := service.Create(adminContext(tenantID), CreateInput{Name: "Catalog events", URL: "https://hooks.example.com/catalog", EventTypes: []string{"*"}})
			if err != nil {
				t.Fatal(err)
			}
			event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000211", TenantID: tenantID, Type: catalog.CanonicalEventType("resource.created"), Subject: "00000000-0000-7000-8000-000000000212", CreatedAt: now.Add(time.Second)}
			if _, err := store.SaveDelivery(context.Background(), Delivery{ID: "00000000-0000-7000-8000-000000000213", TenantID: tenantID, EndpointID: created.Endpoint.ID, EventID: event.ID, EventType: event.Type, State: state, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			outbox := &memoryOutbox{events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time)}
			dispatcher, err := newTestDispatcher(outbox, store, cipher, sender, serviceClock, ids, "dispatcher-one", time.Minute, 3, time.Millisecond, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if completed, err := dispatcher.DispatchOnce(context.Background()); err != nil || completed != 1 {
				t.Fatalf("dispatch = %d, %v", completed, err)
			}
			if len(sender.Requests()) != 0 {
				t.Fatal("terminal delivery was sent again after restart")
			}
			if _, acknowledged := outbox.published[event.ID]; !acknowledged {
				t.Fatal("event with terminal deliveries was not acknowledged")
			}
		})
	}
}

func TestDispatcherRunPropagatesFailuresAndRepollsAfterProgress(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	store := NewMemoryStore()
	cipher, err := NewAESGCM(keyBytes(8))
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{}
	ids := &sequenceIDs{}
	failure := errors.New("claim failed")
	failing, err := newTestDispatcher(&dispatcherFaultOutbox{Outbox: &memoryOutbox{published: make(map[string]time.Time)}, claimErr: failure}, store, cipher, sender, serviceClock, ids, "dispatcher-one", time.Minute, 3, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := failing.Run(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("run returned %v", err)
	}

	tenantID := "00000000-0000-7000-8000-000000000001"
	event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000221", TenantID: tenantID, Type: "internal.test", Internal: true, CreatedAt: now}
	baseOutbox := &memoryOutbox{events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time)}
	ctx, cancel := context.WithCancel(context.Background())
	faultOutbox := &dispatcherFaultOutbox{Outbox: baseOutbox, afterPublish: cancel}
	dispatcher, err := newTestDispatcher(faultOutbox, store, cipher, sender, serviceClock, ids, "dispatcher-one", time.Minute, 3, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run after progress returned %v", err)
	}
	if _, acknowledged := baseOutbox.published[event.ID]; !acknowledged {
		t.Fatal("productive dispatch was not acknowledged before cancellation")
	}
}

func TestWebhookInputAndServiceStateGuardrails(t *testing.T) {
	serviceClock := clock.NewFake(time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC))
	store := NewMemoryStore()
	cipher, _ := NewAESGCM(keyBytes(6))
	service, err := NewService(store, cipher, &recordingSender{}, serviceClock, &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := adminContext("00000000-0000-7000-8000-000000000001")
	invalid := []CreateInput{
		{Name: "", URL: "https://hooks.example.com", EventTypes: []string{"*"}},
		{Name: "Hook", URL: "http://hooks.example.com", EventTypes: []string{"*"}},
		{Name: "Hook", URL: "https://user:pass@hooks.example.com", EventTypes: []string{"*"}}, //nolint:gosec // This adversarial fixture proves user-info credentials are rejected.
		{Name: "Hook", URL: "https://hooks.example.com/path#secret", EventTypes: []string{"*"}},
		{Name: "Hook", URL: "https://hooks.example.com/path?bad=%zz", EventTypes: []string{"*"}},
		{Name: "Hook", URL: "https://hooks.example.com", EventTypes: nil},
		{Name: "Hook", URL: "https://hooks.example.com", EventTypes: []string{"invalid event"}},
		{Name: "Hook", URL: "https://hooks.example.com", EventTypes: []string{"RESOURCE.CREATED"}},
		{Name: "Hook", URL: "https://hooks.example.com", EventTypes: []string{" resource.created"}},
		{Name: "Hook", URL: "https://hooks.example.com", EventTypes: []string{strings.Repeat("x", 161)}},
	}
	for index, input := range invalid {
		if _, err := service.Create(ctx, input); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid endpoint %d returned %v", index, err)
		}
	}
	first, err := service.Create(ctx, CreateInput{Name: " Beta ", URL: "https://hooks.example.com/beta", EventTypes: []string{"resource.created", "com.open.agentic.catalog.resource.created.v1"}})
	if err != nil || first.Endpoint.Name != "Beta" || len(first.Endpoint.EventTypes) != 1 {
		t.Fatalf("first endpoint = %#v, %v", first, err)
	}
	second, err := service.Create(ctx, CreateInput{Name: "Alpha", URL: "https://hooks.example.com/alpha", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, CreateInput{Name: "Duplicate", URL: first.Endpoint.URL, EventTypes: []string{"*"}}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("duplicate endpoint URL returned %v", err)
	}
	if got, err := service.Get(ctx, first.Endpoint.ID); err != nil || got.ID != first.Endpoint.ID {
		t.Fatalf("get endpoint = %#v, %v", got, err)
	}
	if _, err := service.Get(adminContext("00000000-0000-7000-8000-000000000002"), first.Endpoint.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant endpoint read returned %v", err)
	}
	if endpoints, err := service.List(ctx, EndpointListRequest{Limit: 20}); err != nil || len(endpoints) != 2 || endpoints[0].ID != second.Endpoint.ID {
		t.Fatalf("endpoint list = %#v, %v", endpoints, err)
	}
	if _, err := service.Update(ctx, first.Endpoint.ID, PatchInput{}, first.Endpoint.Revision); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("empty patch returned %v", err)
	}
	conflictingURL := second.Endpoint.URL
	if _, err := service.Update(ctx, first.Endpoint.ID, PatchInput{URL: &conflictingURL}, first.Endpoint.Revision); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("conflicting patch URL returned %v", err)
	}
	newURL := "https://hooks.example.com/updated"
	eventTypes := []string{"publisher.created"}
	updated, err := service.Update(ctx, first.Endpoint.ID, PatchInput{URL: &newURL, EventTypes: &eventTypes}, first.Endpoint.Revision)
	if err != nil || updated.URL != newURL || len(updated.EventTypes) != 1 {
		t.Fatalf("updated endpoint = %#v, %v", updated, err)
	}
	if _, err := service.Update(ctx, first.Endpoint.ID, PatchInput{URL: &newURL, EventTypes: &eventTypes}, updated.Revision); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("no-op endpoint update returned %v", err)
	}
	if _, err := service.RotateSecret(ctx, first.Endpoint.ID, updated.Revision-1, RotateInput{}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale secret rotation returned %v", err)
	}
	disabled, err := service.Disable(ctx, first.Endpoint.ID, updated.Revision)
	if err != nil || disabled.Status != StatusDisabled {
		t.Fatalf("disabled endpoint = %#v, %v", disabled, err)
	}
	idempotent, err := service.Disable(ctx, first.Endpoint.ID, disabled.Revision)
	if err != nil || idempotent.Revision != disabled.Revision {
		t.Fatalf("idempotent disable = %#v, %v", idempotent, err)
	}
	if _, err := service.ListDeliveries(ctx, DeliveryListRequest{EndpointID: "00000000-0000-7000-8000-000000000099", Limit: 10}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing endpoint deliveries returned %v", err)
	}

	store.deliveries[deliveryKey(second.Endpoint.ID, "event-old")] = Delivery{ID: "delivery-old", TenantID: "00000000-0000-7000-8000-000000000001", EndpointID: second.Endpoint.ID, EventID: "event-old", UpdatedAt: serviceClock.Now()}
	store.deliveries[deliveryKey(second.Endpoint.ID, "event-new")] = Delivery{ID: "delivery-new", TenantID: "00000000-0000-7000-8000-000000000001", EndpointID: second.Endpoint.ID, EventID: "event-new", UpdatedAt: serviceClock.Now().Add(time.Minute)}
	store.deliveries[deliveryKey(second.Endpoint.ID, "event-other")] = Delivery{ID: "delivery-other", TenantID: "00000000-0000-7000-8000-000000000002", EndpointID: second.Endpoint.ID, EventID: "event-other", UpdatedAt: serviceClock.Now().Add(2 * time.Minute)}
	deliveries, err := service.ListDeliveries(ctx, DeliveryListRequest{EndpointID: second.Endpoint.ID, Limit: 1})
	if err != nil || len(deliveries) != 2 || deliveries[0].ID != "delivery-new" || deliveries[1].ID != "delivery-old" {
		t.Fatalf("bounded deliveries = %#v, %v", deliveries, err)
	}
	all, err := service.ListDeliveries(ctx, DeliveryListRequest{EndpointID: second.Endpoint.ID, Limit: 20})
	if err != nil || len(all) != 2 {
		t.Fatalf("endpoint deliveries = %#v, %v", all, err)
	}
}

func TestWebhookDeliveryFailureModesAndBackoffCap(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000010", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("resource.created"), Subject: "00000000-0000-7000-8000-000000000011", Data: testPublicReference(t, "resource", "00000000-0000-7000-8000-000000000011"), CreatedAt: now}
	endpoint := Endpoint{ID: "endpoint", TenantID: "tenant", URL: "https://hooks.example.com", SecretVersion: 1, SecretCiphertext: []byte("encrypted")}
	delivery := Delivery{ID: "delivery", TenantID: "tenant", EndpointID: endpoint.ID, EventID: event.ID, State: DeliveryPending}
	failed := deliver(context.Background(), &recordingSender{}, failingCipher{}, endpoint, event, delivery, 2, clock.NewFake(now), time.Hour)
	if failed.State != DeliveryWaiting || failed.SafeError == "" || failed.NextAttemptAt == nil {
		t.Fatalf("cipher failure = %#v", failed)
	}
	cipher, _ := NewAESGCM(keyBytes(8))
	secret, _ := cipher.Encrypt([]byte("whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	endpoint.SecretCiphertext = secret
	event.Data = []byte(`{`)
	failed = deliver(context.Background(), &recordingSender{}, cipher, endpoint, event, delivery, 1, clock.NewFake(now), time.Hour)
	if failed.State != DeliveryDeadLetter || failed.SafeError == "" {
		t.Fatalf("CloudEvent failure = %#v", failed)
	}
	event.Data = testPublicReference(t, "resource", event.Subject)
	failed = deliver(context.Background(), &recordingSender{results: []SendResult{{Status: http.StatusBadGateway}}}, cipher, endpoint, event, delivery, 3, clock.NewFake(now), time.Hour)
	if failed.State != DeliveryWaiting || failed.ResponseStatus != http.StatusBadGateway {
		t.Fatalf("HTTP failure = %#v", failed)
	}
	failed = deliver(context.Background(), &recordingSender{errors: []error{errors.New("offline")}}, cipher, endpoint, event, failed, 3, clock.NewFake(now.Add(time.Minute)), time.Hour)
	if failed.State != DeliveryWaiting || failed.ResponseStatus != 0 {
		t.Fatalf("network failure retained a stale HTTP status = %#v", failed)
	}
	for _, attempt := range []int{20, int(^uint(0) >> 1)} {
		if delay := webhookBackoff(attempt); delay != time.Hour {
			t.Fatalf("attempt %d: capped webhook backoff = %s", attempt, delay)
		}
	}
}
