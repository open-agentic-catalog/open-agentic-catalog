package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

type webhookAuditSink struct {
	records []audit.Record
	fail    bool
}

type auditFailingIDs struct {
	base   sequenceIDs
	calls  int
	failAt int
}

func (g *auditFailingIDs) New(at time.Time) (string, error) {
	g.calls++
	if g.calls == g.failAt {
		return "", errors.New("injected audit identifier failure")
	}
	return g.base.New(at)
}

func (s *webhookAuditSink) AppendAuditEvent(_ context.Context, record audit.Record) error {
	if s.fail {
		return errors.New("injected audit failure")
	}
	s.records = append(s.records, record)
	return nil
}

func auditFixture(t *testing.T) (*Service, *MemoryStore, *webhookAuditSink, context.Context) {
	t.Helper()
	sink := &webhookAuditSink{}
	store := NewMemoryStore(sink)
	cipher, err := NewAESGCM(keyBytes(3))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, cipher, &recordingSender{}, clock.NewFake(time.Date(2026, 9, 6, 12, 0, 0, 123456789, time.UTC)), &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, sink, adminContext("00000000-0000-7000-8000-000000000001")
}

func TestWebhookAuditLifecycleAndExactReadEvidence(t *testing.T) {
	s, _, sink, ctx := auditFixture(t)
	created, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://private-receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	name := "Renamed"
	updated, err := s.Update(ctx, created.Endpoint.ID, PatchInput{Name: &name}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.Test(ctx, updated.ID, updated.Revision); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.ListDeliveries(ctx, DeliveryListRequest{EndpointID: updated.ID, Limit: 1})
	if err != nil || len(items) != 2 {
		t.Fatalf("page: %d %v", len(items), err)
	}
	raw, err := json.Marshal(items[:1])
	if err != nil {
		t.Fatal(err)
	}
	read := sink.records[len(sink.records)-1]
	if err := matchWebhookFacts(read, map[string]any{"deliveryCount": 1, "deliveriesDigest": audit.DigestBytes(raw)}); err != nil {
		t.Fatal(err)
	}
	rotated, err := s.RotateSecret(ctx, updated.ID, updated.Revision, RotateInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Disable(ctx, updated.ID, rotated.Endpoint.Revision); err != nil {
		t.Fatal(err)
	}
	want := []string{audit.ActionWebhookCreated, audit.ActionWebhookUpdated, audit.ActionWebhookDeliveryAttempted, audit.ActionWebhookDeliveryAttempted, audit.ActionWebhookDeliveriesRead, audit.ActionWebhookSecretRotated, audit.ActionWebhookDisabled}
	got := make([]string, 0, len(sink.records))
	for _, record := range sink.records {
		got = append(got, record.Action)
		if err := audit.ValidateNewRecord(record); err != nil {
			t.Fatal(err)
		}
		if record.Actor != "00000000-0000-7000-8000-000000000501" || record.At.Nanosecond()%1000 != 0 {
			t.Fatal("unstable attribution or timestamp")
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"private-receiver.example", created.Secret, rotated.Secret, "Renamed"} {
			if strings.Contains(string(encoded), private) {
				t.Fatal("audit retained private endpoint material")
			}
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("actions=%v", got)
	}
	before := len(sink.records)
	if _, err := s.ListDeliveries(adminContext("00000000-0000-7000-8000-000000000002"), DeliveryListRequest{EndpointID: updated.ID, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant read=%v", err)
	}
	if len(sink.records) != before {
		t.Fatal("denied read emitted successful access evidence")
	}
}

func TestWebhookAuditFailurePreventsStateOrDisclosure(t *testing.T) {
	s, store, sink, ctx := auditFixture(t)
	input := CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}}
	sink.fail = true
	if _, err := s.Create(ctx, input); err == nil || len(store.endpoints) != 0 {
		t.Fatal("creation survived audit failure")
	}
	sink.fail = false
	created, err := s.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	sink.fail = true
	name := "Renamed"
	if _, err := s.Update(ctx, created.Endpoint.ID, PatchInput{Name: &name}, 1); err == nil {
		t.Fatal("update survived audit failure")
	}
	if _, err := s.Disable(ctx, created.Endpoint.ID, 1); err == nil {
		t.Fatal("disable survived audit failure")
	}
	if _, err := s.RotateSecret(ctx, created.Endpoint.ID, 1, RotateInput{}); err == nil {
		t.Fatal("rotation survived audit failure")
	}
	if _, err := s.Test(ctx, created.Endpoint.ID, 1); err == nil || len(store.deliveries) != 0 {
		t.Fatal("delivery result survived audit failure")
	}
	retained, err := store.GetEndpoint(ctx, created.Endpoint.TenantID, created.Endpoint.ID)
	if err != nil || retained.Revision != 1 || retained.SecretVersion != 1 || retained.Status != StatusActive || retained.Name != input.Name {
		t.Fatal("failed audit changed endpoint")
	}
	sink.fail = false
	if _, err := s.Test(ctx, created.Endpoint.ID, 1); err != nil {
		t.Fatal(err)
	}
	sink.fail = true
	if items, err := s.ListDeliveries(ctx, DeliveryListRequest{EndpointID: created.Endpoint.ID, Limit: 10}); err == nil || items != nil {
		t.Fatal("private data released despite audit failure")
	}
	if len(sink.records) != 2 {
		t.Fatal("failed operations left audit records")
	}
}

func TestWebhookAuditRejectsMismatchedAndForgedEvidence(t *testing.T) {
	s, _, sink, ctx := auditFixture(t)
	created, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	record := sink.records[0]
	for _, change := range []func(*audit.Record){
		func(r *audit.Record) { r.TenantID = "00000000-0000-7000-8000-000000000002" },
		func(r *audit.Record) { r.TargetID = "00000000-0000-7000-8000-000000000999" },
		func(r *audit.Record) { r.TargetType = "webhook_delivery" },
		func(r *audit.Record) { r.Action = audit.ActionWebhookUpdated },
		func(r *audit.Record) { r.At = r.At.Add(time.Second) },
		func(r *audit.Record) { r.FormatVersion = nil },
		func(r *audit.Record) { r.Details = map[string]any{} },
	} {
		bad := record
		change(&bad)
		if err := ValidateEndpointEvidence([]audit.Record{bad}, created.Endpoint, audit.ActionWebhookCreated); err == nil {
			t.Fatal("mismatched evidence accepted")
		}
	}
	if err := ValidateEndpointEvidence([]audit.Record{record, record}, created.Endpoint, audit.ActionWebhookCreated); err == nil {
		t.Fatal("ambiguous evidence accepted")
	}
	badEndpoint := created.Endpoint
	badEndpoint.Revision++
	if err := ValidateEndpointEvidence([]audit.Record{record}, badEndpoint, audit.ActionWebhookCreated); err == nil {
		t.Fatal("unbound revision accepted")
	}
	forged, err := webhookAudit(ctx, s.ids, created.Endpoint.TenantID, audit.ActionWebhookDeliveriesRead, "webhook_endpoint", created.Endpoint.ID, map[string]any{"deliveryCount": 1, "deliveriesDigest": audit.DigestBytes([]byte("forged"))}, s.clock.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDeliveryReadEvidence(created.Endpoint.TenantID, created.Endpoint.ID, []Delivery{}, 2, []DeliveryReadEvidence{func([]Delivery) (audit.Record, error) { return forged, nil }}); err == nil {
		t.Fatal("forged page evidence accepted")
	}
	for _, builders := range [][]DeliveryReadEvidence{{nil}, {nil, nil}} {
		if _, err := PrepareDeliveryReadEvidence(created.Endpoint.TenantID, created.Endpoint.ID, nil, 2, builders); err == nil {
			t.Fatal("ambiguous read evidence accepted")
		}
	}
}

func TestWebhookWorkerAuditUsesCodeOwnedPrincipal(t *testing.T) {
	s, _, _, ctx := auditFixture(t)
	delivery := Delivery{ID: "00000000-0000-7000-8000-000000000101", TenantID: "00000000-0000-7000-8000-000000000001", EndpointID: "00000000-0000-7000-8000-000000000102", EventID: "00000000-0000-7000-8000-000000000103", Attempt: 1, State: DeliverySucceeded, ResponseStatus: 204, UpdatedAt: s.clock.Now()}
	record, err := deliveryAudit(ctx, s.ids, delivery, true)
	if err != nil {
		t.Fatal(err)
	}
	if record.Actor != audit.PrincipalIDCatalogWorker || record.Details["principalType"] != audit.PrincipalServiceExecutor || record.Details["authorizationDigest"] != nil {
		t.Fatal("worker inherited caller authorization")
	}
	if err := ValidateDeliveryEvidence([]audit.Record{record}, delivery); err != nil {
		t.Fatal(err)
	}
	delivery.Attempt++
	if err := ValidateDeliveryEvidence([]audit.Record{record}, delivery); err == nil {
		t.Fatal("unbound attempt accepted")
	}
	if _, err := deliveryAudit(context.Background(), s.ids, delivery, false); err == nil {
		t.Fatal("missing API attribution accepted")
	}
}

func TestWebhookAuditPreparationFailuresFailClosed(t *testing.T) {
	for _, operation := range []string{"create", "update", "disable", "rotate", "test", "list"} {
		t.Run(operation, func(t *testing.T) {
			s, store, sink, ctx := auditFixture(t)
			input := CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}}
			created, err := s.Create(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			failAt := 1
			if operation == "create" {
				failAt = 2
			}
			if operation == "test" {
				failAt = 3
			}
			s.ids = &auditFailingIDs{failAt: failAt}
			switch operation {
			case "create":
				input.URL = "https://new.example/events"
				_, err = s.Create(ctx, input)
			case "update":
				name := "Changed"
				_, err = s.Update(ctx, created.Endpoint.ID, PatchInput{Name: &name}, 1)
			case "disable":
				_, err = s.Disable(ctx, created.Endpoint.ID, 1)
			case "rotate":
				_, err = s.RotateSecret(ctx, created.Endpoint.ID, 1, RotateInput{})
			case "test":
				_, err = s.Test(ctx, created.Endpoint.ID, 1)
			case "list":
				_, err = s.ListDeliveries(ctx, DeliveryListRequest{EndpointID: created.Endpoint.ID, Limit: 1})
			}
			if err == nil || len(sink.records) != 1 || len(store.endpoints) != 1 || len(store.deliveries) != 0 {
				t.Fatal("failed audit preparation changed state or succeeded")
			}
		})
	}
	s, store, _, ctx := auditFixture(t)
	store.evidence = nil
	if _, err := s.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}}); err == nil || len(store.endpoints) != 0 {
		t.Fatal("missing audit sink accepted")
	}
	if _, err := webhookAudit(ctx, s.ids, "00000000-0000-7000-8000-000000000001", "unknown", "webhook_endpoint", "00000000-0000-7000-8000-000000000001", nil, s.clock.Now(), false); err == nil {
		t.Fatal("invalid audit action accepted")
	}
	items := []Delivery{{CreatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	if _, err := s.deliveryReadAudit(ctx, "00000000-0000-7000-8000-000000000001", "00000000-0000-7000-8000-000000000002", 1)(items); err == nil {
		t.Fatal("unencodable private page released")
	}
	if err := matchWebhookFacts(audit.Record{Details: map[string]any{"facts": make(chan int)}}, nil); err == nil {
		t.Fatal("unencodable evidence accepted")
	}
}
