package webhook

import (
	"context"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

// Existing fault fixtures keep the outbox and audit sink independently
// injectable. Bind them here so the worker path uses the same claim authority
// for dispatch and outcome persistence, as the real composition root does.
func newTestDispatcher(outbox Outbox, store Store, cipher Cipher, sender Sender, clock Clock, ids IDGenerator, owner string, lease time.Duration, attempts int, poll, ceiling time.Duration, observers ...OutboxObserver) (*Dispatcher, error) {
	var bind func(Outbox, Store)
	bind = func(outbox Outbox, store Store) {
		if wrapped, ok := outbox.(*dispatcherFaultOutbox); ok {
			bind(wrapped.Outbox, store)
			return
		}
		switch wrapped := store.(type) {
		case dispatcherFaultStore:
			bind(outbox, wrapped.Store)
		case *dispatcherFaultStore:
			bind(outbox, wrapped.Store)
		case changedSelectionStore:
			bind(outbox, wrapped.Store)
		case selectionInterleavingStore:
			bind(outbox, wrapped.Store)
		case *MemoryStore:
			if box, ok := outbox.(*memoryOutbox); ok && wrapped.evidence != box {
				box.evidence = wrapped.evidence
				wrapped.evidence = box
			}
		}
	}
	bind(outbox, store)
	return NewDispatcher(outbox, store, cipher, sender, clock, ids, owner, lease, attempts, poll, ceiling, observers...)
}

func (m *memoryOutbox) AppendAuditEvent(ctx context.Context, record audit.Record) error {
	return m.evidence.AppendAuditEvent(ctx, record)
}

func (m *memoryOutbox) AppendClaimedOutboxAudit(ctx context.Context, tenantID, eventID, owner string, clock catalog.Clock, record audit.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.eventBelongsToTenant(eventID, tenantID) || m.leases[eventID] != owner || !m.expiries[eventID].After(clock.Now()) {
		return catalog.E(catalog.CodeConflict, "outbox claim is not live")
	}
	if _, published := m.published[eventID]; published {
		return catalog.E(catalog.CodeConflict, "outbox claim is terminal")
	}
	return m.evidence.AppendAuditEvent(ctx, record)
}

func (s dispatcherFaultStore) SaveClaimedDelivery(ctx context.Context, delivery Delivery, owner string, clock catalog.Clock, record audit.Record) (Delivery, error) {
	if s.saveErr != nil {
		return Delivery{}, s.saveErr
	}
	return s.Store.SaveClaimedDelivery(ctx, delivery, owner, clock, record)
}

func TestMemoryClaimedDeliveryBindingAndRecovery(t *testing.T) {
	for _, scenario := range []string{"live", "retry", "terminal", "wrong-identity", "wrong-type", "wrong-created", "wrong-attempt", "older-update", "wrong-tenant", "missing-endpoint", "invalid-evidence", "missing-sink", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
			serviceClock := clock.NewFake(now)
			repo := catalog.NewMemoryRepository()
			store := NewMemoryStore(repo)
			delivery := Delivery{ID: "00000000-0000-7000-8000-000000000101", TenantID: "00000000-0000-7000-8000-000000000001", EndpointID: "00000000-0000-7000-8000-000000000201", EventID: "00000000-0000-7000-8000-000000000301", EventType: "com.open.agentic.catalog.resource.created.v1", State: DeliverySucceeded, Attempt: 1, ResponseStatus: 204, CreatedAt: now, UpdatedAt: now}
			store.endpoints[delivery.EndpointID] = Endpoint{ID: delivery.EndpointID, TenantID: delivery.TenantID}

			seed, err := audit.NewRecord("00000000-0000-7000-8000-000000000401", delivery.TenantID, audit.Principal{ID: "catalog-api", Type: audit.PrincipalServiceExecutor}, "resource.created", "resource", delivery.EndpointID, "", audit.Execution{}, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			event := catalog.OutboxEvent{ID: delivery.EventID, TenantID: delivery.TenantID, Type: delivery.EventType, Subject: delivery.EndpointID, Data: testPublicReference(t, "resource", delivery.EndpointID), CreatedAt: now}
			if err := repo.AppendEvidence(context.Background(), seed, &event); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.ClaimOutbox(context.Background(), "owner", now, time.Minute, 1); err != nil {
				t.Fatal(err)
			}
			previous := delivery
			previous.State = DeliveryWaiting
			if scenario == "retry" || scenario == "terminal" || scenario == "wrong-identity" || scenario == "wrong-type" || scenario == "wrong-created" || scenario == "wrong-attempt" || scenario == "older-update" {
				delivery.Attempt = 2
				if scenario == "terminal" {
					previous.State = DeliverySucceeded
				}
				store.deliveries[deliveryKey(delivery.EndpointID, delivery.EventID)] = previous
			}
			switch scenario {
			case "wrong-identity":
				delivery.ID = "00000000-0000-7000-8000-000000000102"
			case "wrong-type":
				delivery.EventType = "changed"
			case "wrong-created":
				delivery.CreatedAt = now.Add(time.Second)
			case "wrong-attempt":
				delivery.Attempt = 3
			case "older-update":
				delivery.UpdatedAt = now.Add(-time.Second)
			case "wrong-tenant":
				delivery.TenantID = "00000000-0000-7000-8000-000000000002"
			case "missing-endpoint":
				delete(store.endpoints, delivery.EndpointID)
			case "missing-sink":
				store.evidence = nil
			case "expired":
				serviceClock.Advance(time.Minute)
			}
			record, err := deliveryAudit(context.Background(), &sequenceIDs{}, delivery, true)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "invalid-evidence" {
				record.TargetID = "invalid"
			}
			_, err = store.SaveClaimedDelivery(context.Background(), delivery, "owner", serviceClock, record)
			if scenario == "live" || scenario == "retry" {
				if err != nil || store.deliveries[deliveryKey(delivery.EndpointID, delivery.EventID)].Attempt != delivery.Attempt {
					t.Fatalf("valid outcome: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid outcome persisted")
				}
				if retained, exists := store.deliveries[deliveryKey(delivery.EndpointID, delivery.EventID)]; exists && retained != previous {
					t.Fatalf("invalid outcome changed retained delivery: %#v", retained)
				}
			}
		})
	}
}
