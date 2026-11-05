package webhook

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type admissionSenderFunc func(context.Context, SendRequest) (SendResult, error)

func (f admissionSenderFunc) Send(ctx context.Context, request SendRequest) (SendResult, error) {
	return f(ctx, request)
}

func TestWebhookTestAdmissionBoundsSlowSendsAndPreservesAuthorization(t *testing.T) {
	service, store, sink, ctx := auditFixture(t)
	created, err := service.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var sends atomic.Int32
	service.sender = admissionSenderFunc(func(ctx context.Context, _ SendRequest) (SendResult, error) {
		if sends.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return SendResult{}, ctx.Err()
			}
		}
		return SendResult{Status: http.StatusNoContent}, nil
	})
	first := make(chan struct{})
	var firstError error
	go func() { _, firstError = service.Test(ctx, created.Endpoint.ID, 1); close(first) }()
	t.Cleanup(func() {
		close(release)
		<-first
		if firstError != nil {
			t.Errorf("admitted send: %v", firstError)
		}
	})
	select {
	case <-entered:
	case <-first:
		t.Fatalf("admitted test returned before reaching the sender: %v", firstError)
	}
	for _, attempt := range []struct {
		context  func() context.Context
		revision int64
		code     catalog.ErrorCode
	}{
		{func() context.Context { return ctx }, 1, catalog.CodeWebhookTestBusy},
		{func() context.Context { return ctx }, 2, catalog.CodePreconditionFailed},
		{context.Background, 1, catalog.CodeForbidden},
		{func() context.Context { return adminContext("00000000-0000-7000-8000-000000000099") }, 1, catalog.CodeNotFound},
	} {
		if _, err := service.Test(attempt.context(), created.Endpoint.ID, attempt.revision); catalog.ErrorCodeOf(err) != attempt.code {
			t.Fatalf("expected %s, got %v", attempt.code, err)
		}
	}
	if sends.Load() != 1 || len(store.deliveries) != 0 || len(sink.records) != 1 {
		t.Fatal("rejected sends reached the receiver or wrote evidence")
	}
}

func TestWebhookTestAdmissionReleasesAfterCancellationAndFailure(t *testing.T) {
	for _, outcome := range []struct{ cancel, accepted bool }{{false, false}, {true, false}, {true, true}} {
		cancelSend := outcome.cancel
		service, store, sink, ctx := auditFixture(t)
		created, err := service.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
		if err != nil {
			t.Fatal(err)
		}
		attempt, cancel := context.WithCancel(ctx)
		sends := 0
		service.sender = admissionSenderFunc(func(context.Context, SendRequest) (SendResult, error) {
			sends++
			if cancelSend {
				cancel()
			}
			if outcome.accepted {
				// The receiver accepted before the caller cancelled. That external
				// effect cannot be undone even though no local outcome can commit.
				return SendResult{Status: http.StatusNoContent}, nil
			}
			return SendResult{}, errors.New("controlled receiver failure")
		})
		_, err = service.Test(attempt, created.Endpoint.ID, 1)
		cancel()
		if cancelSend && !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
		if sends != 1 {
			t.Fatal("the controlled receiver was not contacted exactly once")
		}
		if cancelSend {
			if len(store.deliveries) != 0 || len(sink.records) != 1 {
				t.Fatal("cancelled accepted send committed partial delivery or audit")
			}
		} else if err != nil || len(store.deliveries) != 1 || len(sink.records) != 2 {
			t.Fatal("completed transport failure did not retain its terminal outcome")
		}
		service.sender = &recordingSender{}
		delivery, err := service.Test(ctx, created.Endpoint.ID, 1)
		if err != nil || delivery.State != DeliverySucceeded {
			t.Fatalf("permit was not released: %v", err)
		}
	}
}

func TestWebhookTestAdmissionReleasesAfterPreparationAndPersistenceFailures(t *testing.T) {
	for _, test := range []struct {
		name                               string
		failAt                             int
		cancelBefore, failAudit, failStore bool
		wantSends                          int
	}{
		{"canceled-before-send", 0, true, false, false, 0},
		{"event-identifier", 1, false, false, false, 0},
		{"delivery-identifier", 2, false, false, false, 0},
		{"audit-identifier", 3, false, false, false, 1},
		{"audit-persistence", 0, false, true, false, 1},
		{"delivery-persistence", 0, false, false, true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, store, sink, ctx := auditFixture(t)
			created, err := service.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
			if err != nil {
				t.Fatal(err)
			}
			service.ids = &auditFailingIDs{base: sequenceIDs{next: 100}, failAt: test.failAt}
			sink.fail = test.failAudit
			if test.failStore {
				service.store = dispatcherFaultStore{Store: store, saveErr: errors.New("controlled delivery persistence failure")}
			}
			sender := &recordingSender{}
			service.sender = sender
			attempt, cancel := context.WithCancel(ctx)
			defer cancel()
			if test.cancelBefore {
				cancel()
			}
			if _, err := service.Test(attempt, created.Endpoint.ID, 1); err == nil {
				t.Fatal("controlled test failure unexpectedly succeeded")
			} else if test.cancelBefore && !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled test: %v", err)
			}
			if len(sender.Requests()) != test.wantSends || len(store.deliveries) != 0 || len(sink.records) != 1 {
				t.Fatal("failed test produced incorrect transport or persisted evidence")
			}
			sink.fail = false
			service.store = store
			result, err := service.Test(ctx, created.Endpoint.ID, 1)
			if err != nil || result.State != DeliverySucceeded || len(store.deliveries) != 1 || len(sink.records) != 2 || len(sender.Requests()) != test.wantSends+1 {
				t.Fatalf("admission or persistence did not recover: %#v %v", result, err)
			}
		})
	}
}

type blockedAdmissionStore struct {
	Store
	entered, release chan struct{}
}

func (s blockedAdmissionStore) SaveDelivery(ctx context.Context, delivery Delivery, records ...audit.Record) (Delivery, error) {
	close(s.entered)
	select {
	case <-s.release:
		return s.Store.SaveDelivery(ctx, delivery, records...)
	case <-ctx.Done():
		return Delivery{}, ctx.Err()
	}
}

func TestWebhookTestAdmissionIncludesDeliveryPersistence(t *testing.T) {
	for _, cancelSave := range []bool{false, true} {
		name := "commit"
		if cancelSave {
			name = "cancel-at-persistence"
		}
		t.Run(name, func(t *testing.T) {
			service, store, sink, actor := auditFixture(t)
			ctx, cancel := context.WithTimeout(actor, 5*time.Second)
			defer cancel()
			created, err := service.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
			if err != nil {
				t.Fatal(err)
			}
			blocked := blockedAdmissionStore{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
			service.store = blocked
			sender := &recordingSender{}
			service.sender = sender
			done := make(chan struct{})
			var sendError error
			go func() { _, sendError = service.Test(ctx, created.Endpoint.ID, 1); close(done) }()
			release := sync.OnceFunc(func() { close(blocked.release) })
			defer func() { cancel(); release(); <-done }()
			select {
			case <-blocked.entered:
			case <-done:
				t.Fatalf("admitted test returned before reaching persistence: %v", sendError)
			case <-ctx.Done():
				t.Fatal("admitted test did not reach persistence")
			}
			if _, err := service.Test(ctx, created.Endpoint.ID, 1); catalog.ErrorCodeOf(err) != catalog.CodeWebhookTestBusy {
				t.Fatalf("admission ended before delivery/evidence persistence: %v", err)
			}
			if len(sender.Requests()) != 1 || len(store.deliveries) != 0 || len(sink.records) != 1 {
				t.Fatal("busy test sent or persisted another attempt")
			}
			wantDeliveries, wantAudits := 1, 2
			if cancelSave {
				cancel()
				wantDeliveries, wantAudits = 0, 1
			} else {
				release()
			}
			<-done
			if cancelSave && !errors.Is(sendError, context.Canceled) || !cancelSave && sendError != nil {
				t.Fatalf("unexpected persistence outcome: %v", sendError)
			}
			if len(store.deliveries) != wantDeliveries || len(sink.records) != wantAudits {
				t.Fatal("persistence completion retained incorrect delivery or audit evidence")
			}
			// Cancellation cannot recall the accepted send, but must release the
			// slot without a partial local commit or an automatic second send.
			if len(sender.Requests()) != 1 {
				t.Fatal("persistence completion retried the receiver")
			}
			service.store = store
			delivery, err := service.Test(actor, created.Endpoint.ID, 1)
			if err != nil || delivery.State != DeliverySucceeded || len(sender.Requests()) != 2 || len(store.deliveries) != wantDeliveries+1 || len(sink.records) != wantAudits+1 {
				t.Fatal("a deliberate next send did not recover the admission slot and commit exactly once")
			}
		})
	}
}
