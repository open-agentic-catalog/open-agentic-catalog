package webhook

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// filterIDs makes UUID letter-case checks deterministic, including event IDs.
type filterIDs struct{ sequenceIDs }

func (ids *filterIDs) New(at time.Time) (string, error) {
	value, err := ids.sequenceIDs.New(at)
	return strings.Replace(value, "00000000", "abcdefab", 1), err
}

func TestWebhookFiltersSelectBeforeLimitAndRetainDuplicateNames(t *testing.T) {
	s, store, sink, ctx := auditFixture(t)
	s.ids = &filterIDs{}
	maximumName := strings.Repeat("界", 160)
	endpoints := make([]Endpoint, 0, 5)
	for i, name := range []string{"Alpha", "Exact", "Exact", "Zulu", maximumName} {
		created, err := s.Create(ctx, CreateInput{Name: name, URL: fmt.Sprintf("https://receiver.example/%d", i), EventTypes: []string{"*"}})
		if err != nil {
			t.Fatal(err)
		}
		endpoints = append(endpoints, created.Endpoint)
	}
	if _, err := s.Disable(ctx, endpoints[2].ID, 1); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		status Status
		ids    []string
	}{
		{"Exact", "", []string{endpoints[1].ID, endpoints[2].ID}},
		{"Exact", StatusActive, []string{endpoints[1].ID}},
		{"Exact", StatusDisabled, []string{endpoints[2].ID}},
		{"", StatusDisabled, []string{endpoints[2].ID}},
		{maximumName, StatusActive, []string{endpoints[4].ID}},
		{"exact", "", nil}, {"Zulu", StatusDisabled, nil},
	} {
		items, err := s.List(ctx, EndpointListRequest{Name: tc.name, Status: tc.status, Limit: 1})
		if err != nil || len(items) != len(tc.ids) {
			t.Fatalf("filtered endpoint count=%d err=%v", len(items), err)
		}
		for i, want := range tc.ids {
			if items[i].ID != want {
				t.Fatal("filter lost exact endpoint")
			}
		}
	}
	items, err := s.List(ctx, EndpointListRequest{Name: "Exact", AfterName: "Exact", AfterID: endpoints[1].ID, Limit: 1})
	if err != nil || len(items) != 1 || items[0].ID != endpoints[2].ID {
		t.Fatal("duplicate-name continuation failed")
	}
	foreign, err := s.List(adminContext("00000000-0000-7000-8000-000000000002"), EndpointListRequest{Name: "Exact", Limit: 1})
	if err != nil || len(foreign) != 0 {
		t.Fatal("endpoint filter crossed tenant")
	}
	for _, r := range []EndpointListRequest{{Name: " "}, {Name: " Exact"}, {Name: strings.Repeat("é", 161)}, {Name: "bad\x00name"}, {Name: string([]byte{255})}, {Status: "ACTIVE"}} {
		r.Limit = 1
		if got, err := s.List(ctx, r); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || got != nil {
			t.Fatal("invalid endpoint filter accepted")
		}
	}
	deliveries := make([]Delivery, 0, 3)
	for range 3 {
		d, err := s.Test(ctx, endpoints[1].ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		deliveries = append(deliveries, d)
	}
	old := deliveries[0]
	for _, r := range []DeliveryListRequest{{DeliveryID: old.ID}, {EventID: old.EventID}, {DeliveryID: old.ID, EventID: old.EventID, State: DeliverySucceeded}, {DeliveryID: strings.ToUpper(old.ID)}, {EventID: strings.ToUpper(old.EventID)}, {DeliveryID: strings.ToUpper(old.ID), EventID: strings.ToUpper(old.EventID), State: DeliverySucceeded}} {
		r.EndpointID = endpoints[1].ID
		r.Limit = 1
		got, err := s.ListDeliveries(ctx, r)
		if err != nil || len(got) != 1 || got[0].ID != old.ID {
			t.Fatal("off-page delivery filter failed")
		}
	}
	page, err := s.ListDeliveries(ctx, DeliveryListRequest{EndpointID: endpoints[1].ID, State: DeliverySucceeded, Limit: 1})
	if err != nil || len(page) != 2 || page[0].ID != deliveries[2].ID || page[1].ID != deliveries[1].ID {
		t.Fatalf("filtered tied page lost order or sentinel: %v", err)
	}
	visible, err := json.Marshal(page[:1])
	if err != nil {
		t.Fatal(err)
	}
	if err := matchWebhookFacts(sink.records[len(sink.records)-1], map[string]any{"deliveryCount": 1, "deliveriesDigest": audit.DigestBytes(visible)}); err != nil {
		t.Fatal("filtered page audit included the continuation sentinel:", err)
	}
	continued, err := s.ListDeliveries(ctx, DeliveryListRequest{EndpointID: endpoints[1].ID, State: DeliverySucceeded, Limit: 1, AfterUpdatedAt: &page[0].UpdatedAt, AfterID: page[0].ID})
	if err != nil || len(continued) != 2 || continued[0].ID != deliveries[1].ID || continued[1].ID != old.ID {
		t.Fatalf("filtered tied continuation skipped or repeated a delivery: %v", err)
	}
	for _, r := range []DeliveryListRequest{{DeliveryID: old.ID, EventID: deliveries[1].EventID}, {State: DeliveryWaiting}, {DeliveryID: endpoints[0].ID}} {
		r.EndpointID = endpoints[1].ID
		r.Limit = 1
		got, err := s.ListDeliveries(ctx, r)
		if err != nil || len(got) != 0 {
			t.Fatal("delivery filters did not intersect")
		}
	}
	for _, r := range []DeliveryListRequest{{DeliveryID: "bad"}, {EventID: "bad"}, {State: "SUCCEEDED"}} {
		r.EndpointID = endpoints[1].ID
		r.Limit = 1
		before := len(sink.records)
		if got, err := s.ListDeliveries(ctx, r); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || got != nil || len(sink.records) != before {
			t.Fatal("invalid delivery filter disclosed or audited data")
		}
	}
	// A private-read audit failure must withhold even a filtered page.
	sink.fail = true
	if got, err := s.ListDeliveries(ctx, DeliveryListRequest{EndpointID: endpoints[1].ID, DeliveryID: old.ID, Limit: 1}); err == nil || got != nil {
		t.Fatal("failed audit disclosed filtered page")
	}
	sink.fail = false
	// Retention removes evidence; filters must not manufacture a replacement.
	store.mu.Lock()
	delete(store.deliveries, deliveryKey(old.EndpointID, old.EventID))
	store.mu.Unlock()
	if got, err := s.ListDeliveries(ctx, DeliveryListRequest{EndpointID: endpoints[1].ID, EventID: old.EventID, Limit: 1}); err != nil || len(got) != 0 {
		t.Fatal("retained gap was fabricated")
	}
}
