package webhook

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type failingNotificationEvidence struct {
	*catalog.MemoryRepository
	err error
}

func (e failingNotificationEvidence) NotificationState(context.Context, string) (catalog.NotificationState, error) {
	return catalog.NotificationState{}, e.err
}

func TestNotificationStateDelegatedPermission(t *testing.T) {
	for _, test := range []struct {
		name      string
		template  string
		actorType identity.ActorType
		allowed   bool
	}{
		{"webhook administrator", "webhook_admin", identity.ActorHuman, true},
		{"service webhook administrator", "webhook_admin", identity.ActorService, true},
		{"reader", "reader", identity.ActorHuman, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, _, _ := auditFixture(t)
			repository := catalog.NewMemoryRepository()
			service.store = NewMemoryStore(repository)
			const tenantID = "00000000-0000-7000-8000-000000000001"
			const issuer = "urn:catalog:notification-unit"
			store, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenantID, Issuer: issuer, Subject: "receiver", ActorType: test.actorType, RoleTemplates: []string{test.template}}})
			if err != nil {
				t.Fatal(err)
			}
			actor, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: tenantID, Issuer: issuer, Subject: "receiver"})
			if err != nil {
				t.Fatal(err)
			}
			ctx := identity.WithActor(t.Context(), actor)
			state, err := service.NotificationState(ctx)
			if test.allowed {
				if err != nil || state.PendingEventLimit != catalog.PendingWebhookEventLimit {
					t.Fatalf("delegated reconciliation: state=%+v err=%v", state, err)
				}
			} else if catalog.ErrorCodeOf(err) != catalog.CodeForbidden || state != (catalog.NotificationState{}) {
				t.Fatalf("unauthorized reconciliation disclosed state: %+v err=%v", state, err)
			}
			events, auditErr := repository.AuditEvents(t.Context(), tenantID, catalog.AuditEventListRequest{Limit: 1})
			if auditErr != nil || len(events) != 0 {
				t.Fatalf("aggregate reconciliation manufactured audit evidence: events=%v err=%v", events, auditErr)
			}
		})
	}
}

func TestNotificationStateReadFailures(t *testing.T) {
	t.Run("unauthorized", func(t *testing.T) {
		service, _, _, _ := auditFixture(t)
		service.store = NewMemoryStore(failingNotificationEvidence{catalog.NewMemoryRepository(), errors.New("reader must not be reached")})
		if _, err := service.NotificationState(t.Context()); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
			t.Fatalf("authorization must precede reconciliation reads: %v", err)
		}
	})
	t.Run("missing-reader", func(t *testing.T) {
		service, _, _, ctx := auditFixture(t)
		if _, err := service.NotificationState(ctx); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
			t.Fatalf("missing reader must not report zero losses: %v", err)
		}
	})
	t.Run("reader-error", func(t *testing.T) {
		service, _, _, ctx := auditFixture(t)
		failure := errors.New("controlled reconciliation read failure")
		service.store = NewMemoryStore(failingNotificationEvidence{catalog.NewMemoryRepository(), failure})
		if _, err := service.NotificationState(ctx); !errors.Is(err, failure) {
			t.Fatalf("reader failure was hidden: %v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		service, _, _, ctx := auditFixture(t)
		service.store = NewMemoryStore(catalog.NewMemoryRepository())
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := service.NotificationState(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled read: %v", err)
		}
	})
}

func TestNotificationStateRetainsTenantLossCheckpointsWithoutAliasing(t *testing.T) {
	service, _, _, ctx := auditFixture(t)
	store := NewMemoryStore(catalog.NewMemoryRepository())
	service.store = store
	created, err := service.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	now := service.clock.Now().UTC().Truncate(time.Microsecond).Add(2 * time.Minute)
	for _, test := range []struct {
		state    DeliveryState
		typeName string
		at       time.Time
	}{
		{DeliveryDeadLetter, "resource.created", now},
		{DeliveryDeadLetter, "resource.created", now.Add(-time.Minute)},
		{DeliveryDeadLetter, "webhook.test", now.Add(time.Minute)},
		{DeliverySucceeded, "resource.created", now.Add(time.Minute)},
		{DeliveryWaiting, "resource.created", now.Add(time.Minute)},
	} {
		identifier, err := service.ids.New(test.at)
		if err != nil {
			t.Fatal(err)
		}
		delivery := Delivery{ID: identifier, TenantID: created.Endpoint.TenantID, EndpointID: created.Endpoint.ID,
			EventID: identifier, EventType: catalog.CanonicalEventType(test.typeName), State: test.state,
			Attempt: 1, ResponseStatus: http.StatusServiceUnavailable, CreatedAt: test.at, UpdatedAt: test.at}
		if test.state == DeliverySucceeded {
			delivery.ResponseStatus = http.StatusNoContent
		} else {
			delivery.SafeError = "receiver_unavailable"
		}
		if test.state == DeliveryWaiting {
			next := test.at.Add(time.Minute)
			delivery.NextAttemptAt = &next
		}
		record, err := deliveryAudit(ctx, service.ids, delivery, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveDelivery(ctx, delivery, record); err != nil {
			t.Fatal(err)
		}
	}
	state, err := service.NotificationState(ctx)
	if err != nil || state.DeliveryLossCount != 2 || state.LastDeliveryLossAt == nil || !state.LastDeliveryLossAt.Equal(now) || state.CapacityLossCount != 0 || state.PendingEvents != 0 || state.PendingEventLimit != catalog.PendingWebhookEventLimit {
		t.Fatalf("incorrect retained checkpoint: %#v %v", state, err)
	}
	// Neither caller mutation nor another tenant's query may change or expose
	// this checkpoint; reads are not loss acknowledgements.
	*state.LastDeliveryLossAt = time.Time{}
	state.DeliveryLossCount = 0
	again, err := service.NotificationState(ctx)
	if err != nil || again.DeliveryLossCount != 2 || again.LastDeliveryLossAt == nil || !again.LastDeliveryLossAt.Equal(now) {
		t.Fatalf("read exposed mutable state or reset losses: %#v %v", again, err)
	}
	other, err := service.NotificationState(adminContext("00000000-0000-7000-8000-000000000002"))
	if err != nil || other.DeliveryLossCount != 0 || other.LastDeliveryLossAt != nil {
		t.Fatalf("cross-tenant checkpoint disclosure: %#v %v", other, err)
	}
}
