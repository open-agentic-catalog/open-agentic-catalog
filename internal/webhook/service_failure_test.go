package webhook

import (
	"context"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestWebhookServicePaginationValidation(t *testing.T) {
	s, _, sink, ctx := auditFixture(t)
	created, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := s.Test(ctx, created.Endpoint.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		limit    int
		position bool
		id       string
		valid    bool
	}{
		{"minimum", 1, false, "", true},
		{"maximum", 100, false, "", true},
		{"cursor", 1, true, delivery.ID, true},
		{"zero", 0, false, "", false},
		{"negative", -1, false, "", false},
		{"over_limit", 101, false, "", false},
		{"missing_position", 1, false, delivery.ID, false},
		{"missing_id", 1, true, "", false},
		{"invalid_id", 1, true, "not-a-uuid", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := ""
			var updated *time.Time
			if test.position {
				name = created.Endpoint.Name
				updated = &delivery.UpdatedAt
			}
			audits := len(sink.records)
			endpoints, endpointErr := s.List(ctx, EndpointListRequest{Limit: test.limit, AfterName: name, AfterID: test.id})
			deliveries, deliveryErr := s.ListDeliveries(ctx, DeliveryListRequest{EndpointID: created.Endpoint.ID, Limit: test.limit, AfterUpdatedAt: updated, AfterID: test.id})
			if !test.valid {
				if catalog.ErrorCodeOf(endpointErr) != catalog.CodeInvalidArgument || catalog.ErrorCodeOf(deliveryErr) != catalog.CodeInvalidArgument || endpoints != nil || deliveries != nil || len(sink.records) != audits {
					t.Fatalf("invalid page returned data or audit: %v, %v", endpointErr, deliveryErr)
				}
				return
			}
			if endpointErr != nil || deliveryErr != nil || len(sink.records) != audits+1 {
				t.Fatalf("valid page failed or missed access evidence: %v, %v", endpointErr, deliveryErr)
			}
			if test.position {
				if len(deliveries) != 0 {
					t.Fatal("cursor repeated its delivery")
				}
			} else if len(endpoints) != 1 || endpoints[0].ID != created.Endpoint.ID || len(deliveries) != 1 || deliveries[0].ID != delivery.ID {
				t.Fatal("valid page lost endpoint or delivery")
			}
		})
	}
}

func TestWebhookMutationsRejectMissingAndOtherTenantEndpoints(t *testing.T) {
	for _, operation := range []string{"update", "disable", "rotate"} {
		for _, scope := range []string{"missing", "other_tenant"} {
			t.Run(operation+"/"+scope, func(t *testing.T) {
				s, store, sink, ctx := auditFixture(t)
				created, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
				if err != nil {
					t.Fatal(err)
				}
				mutate := func(ctx context.Context, id string) error {
					switch operation {
					case "update":
						name := "Renamed"
						_, err := s.Update(ctx, id, PatchInput{Name: &name}, 1)
						return err
					case "disable":
						_, err := s.Disable(ctx, id, 1)
						return err
					default:
						_, err := s.RotateSecret(ctx, id, 1, RotateInput{})
						return err
					}
				}
				deniedCtx, id := ctx, "00000000-0000-7000-8000-000000000099"
				if scope == "other_tenant" {
					deniedCtx = adminContext("00000000-0000-7000-8000-000000000002")
					id = created.Endpoint.ID
				}
				if err := mutate(deniedCtx, id); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
					t.Fatalf("inaccessible endpoint returned %v", err)
				}
				retained, err := store.GetEndpoint(ctx, created.Endpoint.TenantID, created.Endpoint.ID)
				if err != nil || !reflect.DeepEqual(retained, created.Endpoint) || len(sink.records) != 1 {
					t.Fatal("rejected mutation changed endpoint or audit")
				}
				if err := mutate(ctx, created.Endpoint.ID); err != nil || len(sink.records) != 2 {
					t.Fatalf("own-tenant control failed: %v", err)
				}
			})
		}
	}
}

func TestWebhookPreparationFailureDoesNotSendOrPersist(t *testing.T) {
	for _, scenario := range []string{"endpoint_id", "encryption", "test_event_id", "test_delivery_id"} {
		t.Run(scenario, func(t *testing.T) {
			s, store, sink, ctx := auditFixture(t)
			sender := &recordingSender{}
			s.sender = sender
			input := CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}}
			var err error
			wantEndpoints := 0
			switch scenario {
			case "endpoint_id":
				s.ids = &auditFailingIDs{failAt: 1}
				_, err = s.Create(ctx, input)
			case "encryption":
				s.cipher = failingCipher{}
				_, err = s.Create(ctx, input)
			default:
				created, createErr := s.Create(ctx, input)
				if createErr != nil {
					t.Fatal(createErr)
				}
				wantEndpoints = 1
				failAt := 1
				if scenario == "test_delivery_id" {
					failAt = 2
				}
				s.ids = &auditFailingIDs{failAt: failAt}
				_, err = s.Test(ctx, created.Endpoint.ID, 1)
			}
			if err == nil || len(sender.Requests()) != 0 || len(store.endpoints) != wantEndpoints || len(sink.records) != wantEndpoints || len(store.deliveries) != 0 {
				t.Fatal("preparation failure sent or committed partial work")
			}
		})
	}
}

func TestWebhookInputRejectsCredentialAliasesAndUnregisteredEvents(t *testing.T) {
	s, store, sink, ctx := auditFixture(t)
	for _, name := range []string{"ACCESS_TOKEN", "Api.Key", "authorization", "credential", "password", "secret", "sig", "signature", "token", "X-Amz-Credential", "X_Amz_Security_Token", "X-Amz-Signature"} {
		input := CreateInput{Name: "Receiver", URL: "https://receiver.example/events?" + url.QueryEscape(name) + "=synthetic", EventTypes: []string{"*"}}
		if _, err := s.Create(ctx, input); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("credential alias %s accepted: %v", name, err)
		}
	}
	if _, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"future.event"}}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("unknown public event accepted: %v", err)
	}
	if len(store.endpoints) != 0 || len(sink.records) != 0 {
		t.Fatal("rejected input left state")
	}
	created, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events?region=eu", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	badURL := "http://receiver.example/events"
	if _, err := s.Update(ctx, created.Endpoint.ID, PatchInput{URL: &badURL}, 1); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid URL patch accepted: %v", err)
	}
	retained, err := store.GetEndpoint(ctx, created.Endpoint.TenantID, created.Endpoint.ID)
	if err != nil || !reflect.DeepEqual(retained, created.Endpoint) || len(sink.records) != 1 {
		t.Fatal("invalid patch changed endpoint or audit")
	}
	if items, err := s.ListDeliveries(ctx, DeliveryListRequest{EndpointID: "not-a-uuid", Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || items != nil {
		t.Fatal("invalid private page identifier accepted")
	}
}
