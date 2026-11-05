package webhook

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestWebhookStoreRejectsEvidenceBeforeChangingState(t *testing.T) {
	s, store, sink, ctx := auditFixture(t)
	created, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := created.Endpoint
	record := sink.records[0]
	other := endpoint
	other.ID = "00000000-0000-7000-8000-000000000888"
	other.URL = "https://another.example/events"
	if _, err := store.CreateEndpoint(ctx, other, record); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("mismatched creation evidence: %v", err)
	}
	other.TenantID = "00000000-0000-7000-8000-000000000002"
	if _, err := store.UpdateEndpoint(ctx, other, 1, record); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing endpoint update: %v", err)
	}
	other.ID = endpoint.ID
	if _, err := store.UpdateEndpoint(ctx, other, 1, record); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant update: %v", err)
	}
	if _, err := store.UpdateEndpoint(ctx, endpoint, 2, record); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale revision: %v", err)
	}
	updated := endpoint
	updated.Name = "Changed"
	updated.Revision++
	if _, err := store.UpdateEndpoint(ctx, updated, 1, record); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("creation audit reused for update: %v", err)
	}
	delivery, err := s.Test(ctx, endpoint.ID, endpoint.Revision)
	if err != nil {
		t.Fatal(err)
	}
	deliveryRecord := sink.records[1]
	changed := delivery
	changed.Attempt++
	if _, err := store.SaveDelivery(ctx, changed, deliveryRecord); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("unbound attempt: %v", err)
	}
	if _, err := store.SaveDelivery(ctx, changed, record); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("endpoint evidence reused for delivery: %v", err)
	}
	retained, err := store.GetEndpoint(ctx, endpoint.TenantID, endpoint.ID)
	if err != nil || !reflect.DeepEqual(retained, endpoint) {
		t.Fatal("rejected write changed endpoint")
	}
	retainedDelivery, err := store.GetDelivery(ctx, endpoint.TenantID, endpoint.ID, delivery.EventID)
	if err != nil || !reflect.DeepEqual(retainedDelivery, delivery) || len(sink.records) != 2 || len(store.endpoints) != 1 {
		t.Fatal("rejected write changed delivery or audit inventory")
	}
}

func TestWebhookStoreWithholdsForgedDeliveryPageEvidence(t *testing.T) {
	s, store, sink, ctx := auditFixture(t)
	created, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.Test(ctx, created.Endpoint.ID, 1); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"tenant", "target", "digest", "count", "limit"} {
		t.Run(mode, func(t *testing.T) {
			builder := s.deliveryReadAudit(ctx, created.Endpoint.TenantID, created.Endpoint.ID, 1)
			forged := func(items []Delivery) (audit.Record, error) {
				record, err := builder(items)
				if err != nil {
					return audit.Record{}, err
				}
				facts, ok := record.Details["facts"].(map[string]any)
				if !ok {
					t.Fatal("valid fixture omitted audit facts")
				}
				switch mode {
				case "tenant":
					record.TenantID = "00000000-0000-7000-8000-000000000002"
				case "target":
					record.TargetID = "00000000-0000-7000-8000-000000000888"
				case "digest":
					facts["deliveriesDigest"] = audit.DigestBytes([]byte("wrong page"))
				case "count":
					facts["deliveryCount"] = 0
				}
				return record, nil
			}
			limit := 2
			if mode == "limit" {
				limit = 1
			}
			items, err := store.ListDeliveries(ctx, created.Endpoint.TenantID, DeliveryListRequest{EndpointID: created.Endpoint.ID, Limit: limit}, forged)
			if catalog.ErrorCodeOf(err) != catalog.CodeInternal || items != nil || len(sink.records) != 4 {
				t.Fatalf("forged page leaked data or audit: items=%d err=%v audits=%d", len(items), err, len(sink.records))
			}
		})
	}
	items, err := store.ListDeliveries(ctx, created.Endpoint.TenantID, DeliveryListRequest{EndpointID: created.Endpoint.ID, Limit: 2}, s.deliveryReadAudit(ctx, created.Endpoint.TenantID, created.Endpoint.ID, 1))
	if err != nil || len(items) != 2 || len(sink.records) != 5 {
		t.Fatalf("valid bounded page rejected: %d %v", len(items), err)
	}
}

func TestWebhookEndpointPagesUseStableTiesAndReturnCopies(t *testing.T) {
	s, store, _, ctx := auditFixture(t)
	endpoints := make([]Endpoint, 0, 3)
	for i := range 3 {
		created, err := s.Create(ctx, CreateInput{Name: "Same name", URL: fmt.Sprintf("https://receiver.example/%d", i), EventTypes: []string{"*"}})
		if err != nil {
			t.Fatal(err)
		}
		endpoints = append(endpoints, created.Endpoint)
	}
	first, err := store.ListEndpointPage(ctx, endpoints[0].TenantID, EndpointListRequest{Limit: 1})
	if err != nil || len(first) != 1 || first[0].ID != endpoints[0].ID {
		t.Fatalf("first page: %v", err)
	}
	next, err := store.ListEndpointPage(ctx, endpoints[0].TenantID, EndpointListRequest{Limit: 1, AfterName: first[0].Name, AfterID: first[0].ID})
	if err != nil || len(next) != 1 || next[0].ID != endpoints[1].ID {
		t.Fatal("equal-name pagination skipped or duplicated endpoint")
	}
	first[0].EventTypes[0] = "changed"
	first[0].SecretCiphertext[0] ^= 1
	retained, err := store.GetEndpoint(ctx, endpoints[0].TenantID, endpoints[0].ID)
	if err != nil || !reflect.DeepEqual(retained, endpoints[0]) {
		t.Fatal("page mutation changed retained endpoint")
	}
	if items, err := store.ListEndpointPage(context.Background(), "00000000-0000-7000-8000-000000000002", EndpointListRequest{Limit: 1}); err != nil || len(items) != 0 {
		t.Fatal("page leaked another tenant's endpoint")
	}
}
