package webhook

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestWebhookOverviewLatestProductionAttemptAndEndpointUpdate(t *testing.T) {
	s, store, sink, ctx := auditFixture(t)
	const tenant = "00000000-0000-7000-8000-000000000001"
	at := s.clock.Now().Add(-time.Hour)
	for _, ep := range []Endpoint{{ID: "failed", Status: StatusActive}, {ID: "recovered", Status: StatusActive}, {ID: "edited", Status: StatusActive}, {ID: "disabled", Status: StatusDisabled}, {ID: "no-attempt", Status: StatusActive}, {ID: "other", TenantID: "other", Status: StatusActive}} {
		if ep.TenantID == "" {
			ep.TenantID = tenant
		}
		ep.Name = ep.ID
		ep.UpdatedAt = at
		store.endpoints[ep.ID] = ep
	}
	add := func(id, endpoint string, state DeliveryState, attempt int, stamp time.Time, test bool) {
		t.Helper()
		d := Delivery{ID: id, EndpointID: endpoint, TenantID: store.endpoints[endpoint].TenantID, EventID: id, EventType: catalog.CanonicalEventType("resource.created"), State: state, Attempt: attempt, UpdatedAt: stamp}
		if test {
			d.EventType = catalog.CanonicalEventType("webhook.test")
		}
		store.deliveries[deliveryKey(endpoint, id)] = d
	}
	add("1", "failed", DeliveryWaiting, 1, at, false)                   // Equality with endpoint update is included.
	add("2", "failed", DeliverySucceeded, 1, at.Add(time.Minute), true) // Tests do not hide a production failure.
	add("1", "recovered", DeliveryDeadLetter, 1, at, false)
	add("2", "recovered", DeliverySucceeded, 1, at, false) // Equal time resolves by descending ID.
	add("1", "edited", DeliveryDeadLetter, 1, at.Add(-time.Second), false)
	add("1", "disabled", DeliveryPending, 0, at, false)
	add("1", "no-attempt", DeliveryPending, 0, at.Add(time.Minute), false)
	add("4", "failed", DeliveryPending, 0, at.Add(2*time.Minute), false) // Unattempted work does not hide an earlier attempted failure.
	add("1", "other", DeliveryDeadLetter, 1, at, false)
	endpoints, err := s.EndpointOverview(ctx)
	if err != nil || endpoints.Total.Value != 5 || endpoints.Total.Limited || !reflect.DeepEqual(endpoints.States, []StateCount{{"active", 4}, {"disabled", 1}}) {
		t.Fatal(endpoints, err)
	}
	out, err := s.DeliveryOverview(ctx)
	if err != nil || out.Total.Value != 7 || out.Total.Limited || !reflect.DeepEqual(out.States, []StateCount{{"pending", 3}, {"waiting", 1}, {"succeeded", 1}, {"dead_letter", 2}}) || len(out.FailedEndpoints) != 1 || out.FailedEndpoints[0].EndpointID != "failed" || out.FailedEndpoints[0].DeliveryID != "1" || out.FailedEndpoints[0].State != DeliveryWaiting || !out.FailedEndpoints[0].AttemptedAt.Equal(at) {
		t.Fatal(out, err)
	}
	if len(sink.records) != 0 {
		t.Fatal("metadata summary manufactured evidence", sink.records)
	}
	// A later production success removes the failure. Retention can expose it again.
	add("3", "failed", DeliverySucceeded, 1, at.Add(time.Minute), false)
	out, err = s.DeliveryOverview(ctx)
	if err != nil || len(out.FailedEndpoints) != 0 {
		t.Fatal(out, err)
	}
	delete(store.deliveries, deliveryKey("failed", "3"))
	out, err = s.DeliveryOverview(ctx)
	if err != nil || len(out.FailedEndpoints) != 1 {
		t.Fatal(out, err)
	}
	disabled := store.endpoints["failed"]
	disabled.Status = StatusDisabled
	store.endpoints["failed"] = disabled
	out, err = s.DeliveryOverview(ctx)
	if err != nil || len(out.FailedEndpoints) != 0 || out.Total.Value != 7 || out.States[1].Count != 1 {
		t.Fatal("disabled endpoints keep retained rows without a current failure card", out, err)
	}
	other := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "other", Subject: "operator", Roles: []identity.Role{identity.RoleTenantAdmin}})
	out, err = s.DeliveryOverview(other)
	if err != nil || out.Total.Value != 1 || len(out.FailedEndpoints) != 1 || out.FailedEndpoints[0].EndpointID != "other" {
		t.Fatal(out, err)
	}
}

func TestWebhookOverviewCeilingsAndIndependentFailures(t *testing.T) {
	s, store, _, ctx := auditFixture(t)
	const tenant = "00000000-0000-7000-8000-000000000001"
	at := s.clock.Now()
	for i := range catalog.OverviewSelectionLimit + 1 {
		key := fmt.Sprintf("%05d", i)
		store.endpoints[key] = Endpoint{ID: key, TenantID: tenant, Name: key, Status: StatusActive, UpdatedAt: at}
		store.deliveries[key] = Delivery{ID: key, TenantID: tenant, EndpointID: key, State: DeliveryDeadLetter, Attempt: 1, UpdatedAt: at, EventType: catalog.CanonicalEventType("resource.created")}
	}
	endpoints, err := s.EndpointOverview(ctx)
	if err != nil || !endpoints.Total.Limited || endpoints.Total.Value != 10000 || endpoints.States != nil {
		t.Fatal(endpoints, err)
	}
	out, err := s.DeliveryOverview(ctx)
	if err != nil || !out.Total.Limited || out.Total.Value != 10000 || out.States != nil || len(out.FailedEndpoints) != 5 || out.FailedEndpoints[0].EndpointID != "00000" {
		t.Fatal(out, err)
	}
	delete(store.endpoints, "10000")
	delete(store.deliveries, "10000")
	endpoints, err = s.EndpointOverview(ctx)
	if err != nil || endpoints.Total.Limited || endpoints.States[0].Count != 10000 {
		t.Fatal(endpoints, err)
	}
	out, err = s.DeliveryOverview(ctx)
	if err != nil || out.Total.Limited || out.States[3].Count != 10000 {
		t.Fatal(out, err)
	}
}

type failedOverviewStore struct{ Store }

func (failedOverviewStore) ReadEndpointOverview(context.Context, string) ([]string, error) {
	return nil, errors.New("injected endpoint read failure")
}
func (failedOverviewStore) ReadDeliveryOverview(context.Context, string) (DeliveryOverviewRows, error) {
	return DeliveryOverviewRows{}, errors.New("injected delivery read failure")
}

func TestWebhookOverviewEmptyDeniedAndFailed(t *testing.T) {
	s, _, _, ctx := auditFixture(t)
	endpoints, err := s.EndpointOverview(ctx)
	if err != nil || endpoints.Total.Value != 0 || len(endpoints.States) != 2 {
		t.Fatal(endpoints, err)
	}
	out, err := s.DeliveryOverview(ctx)
	if err != nil || out.Total.Value != 0 || len(out.States) != 4 || out.FailedEndpoints == nil {
		t.Fatal(out, err)
	}
	for _, permission := range []string{"none", "reader"} {
		denied := t.Context()
		if permission == "reader" {
			denied = identityfixture.WithActor(denied, identity.Actor{TenantID: "other", Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
		}
		if _, err = s.EndpointOverview(denied); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
			t.Fatal(err)
		}
		if _, err = s.DeliveryOverview(denied); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
			t.Fatal(err)
		}
	}
	s.store = failedOverviewStore{s.store}
	if _, err = s.EndpointOverview(ctx); err == nil {
		t.Fatal("endpoint failure became zero")
	}
	if _, err = s.DeliveryOverview(ctx); err == nil {
		t.Fatal("delivery failure became zero")
	}
}
