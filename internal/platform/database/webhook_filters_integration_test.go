//go:build integration

package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

func TestPostgresWebhookFiltersBeforeLimitsAndAtomicAudit(t *testing.T) {
	db := newHousekeepingDatabase(t)
	// The exact spelling contract must not depend on an operator's default
	// collation. Only this disposable fixture uses a case-insensitive column.
	if _, err := db.operator.Exec(t.Context(), `CREATE COLLATION webhook_filter_ci (provider=icu,locale='und-u-ks-level2',deterministic=false); ALTER TABLE webhook_endpoints ALTER COLUMN name TYPE text COLLATE webhook_filter_ci`); err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, context.Background(), identity.Actor{TenantID: housekeepingTenantA, Subject: "webhook-filter-review", MembershipID: "00000000-0000-7000-8000-000000000901", RealmID: "00000000-0000-7000-8000-000000000902", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresWebhookStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	cipher, _ := webhook.NewAESGCM(make([]byte, 32))
	serviceClock := clock.NewFake(time.Now().UTC().Truncate(time.Microsecond))
	s, err := webhook.NewService(store, cipher, &webhookIntegrationSender{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	endpoints := make([]webhook.Endpoint, 0, 4)
	for i, name := range []string{"Alpha", "Exact", "Exact", "Zulu"} {
		c, err := s.Create(ctx, webhook.CreateInput{Name: name, URL: fmt.Sprintf("https://receiver.example/%d", i), EventTypes: []string{"*"}})
		if err != nil {
			t.Fatal(err)
		}
		endpoints = append(endpoints, c.Endpoint)
	}
	serviceClock.Advance(time.Second)
	if _, err := s.Disable(ctx, endpoints[2].ID, 1); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		status webhook.Status
		count  int
	}{{"Exact", "", 2}, {"Exact", webhook.StatusActive, 1}, {"Exact", webhook.StatusDisabled, 1}, {"exact", "", 0}, {"Zulu", webhook.StatusDisabled, 0}} {
		got, err := s.List(ctx, webhook.EndpointListRequest{Name: tc.name, Status: tc.status, Limit: 1})
		if err != nil || len(got) != tc.count {
			t.Fatalf("exact SQL endpoint filter count=%d want=%d: %v", len(got), tc.count, err)
		}
	}
	both, err := s.List(ctx, webhook.EndpointListRequest{Name: "Exact", Limit: 1})
	if err != nil || len(both) != 2 {
		t.Fatal("exact SQL first page failed")
	}
	got, err := s.List(ctx, webhook.EndpointListRequest{Name: "Exact", AfterName: "Exact", AfterID: both[0].ID, Limit: 1})
	if err != nil || len(got) != 1 || got[0].ID != both[1].ID {
		t.Fatal("SQL duplicate-name continuation failed")
	}
	deliveries := make([]webhook.Delivery, 0, 3)
	for range 3 {
		serviceClock.Advance(time.Second)
		d, err := s.Test(ctx, endpoints[1].ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		deliveries = append(deliveries, d)
	}
	old := deliveries[0]
	for _, r := range []webhook.DeliveryListRequest{{DeliveryID: old.ID}, {EventID: old.EventID}, {DeliveryID: old.ID, EventID: old.EventID, State: old.State}} {
		r.EndpointID = endpoints[1].ID
		r.Limit = 1
		items, err := s.ListDeliveries(ctx, r)
		if err != nil || len(items) != 1 || items[0].ID != old.ID {
			t.Fatalf("SQL off-page exact filter lost match: %v", err)
		}
	}
	for _, r := range []webhook.DeliveryListRequest{{DeliveryID: old.ID, EventID: deliveries[1].EventID}, {State: webhook.DeliveryWaiting}, {EndpointID: endpoints[0].ID, DeliveryID: old.ID}} {
		if r.EndpointID == "" {
			r.EndpointID = endpoints[1].ID
		}
		r.Limit = 1
		items, err := s.ListDeliveries(ctx, r)
		if err != nil || len(items) != 0 {
			t.Fatal("SQL filter intersection crossed parent or state")
		}
	}
	// Exact API login remains under forced RLS; a foreign tenant cannot select
	// a known delivery or endpoint by supplying its identifiers as filters.
	other := identity.WithActor(context.Background(), identity.Actor{TenantID: housekeepingTenantB})
	if items, err := store.ListEndpointPage(other, housekeepingTenantB, webhook.EndpointListRequest{Name: "Exact", Limit: 2}); err != nil || len(items) != 0 {
		t.Fatal("SQL endpoint filter crossed tenant")
	}
	if items, err := store.ListDeliveries(other, housekeepingTenantB, webhook.DeliveryListRequest{EndpointID: old.EndpointID, DeliveryID: old.ID, Limit: 2}); err != nil || len(items) != 0 {
		t.Fatal("SQL delivery filter crossed tenant")
	}
	// Owner-only fixture setup establishes a mutable waiting lineage. Ordinary
	// reads must show its current outcome rather than fabricating send history.
	ids := id.Generator{}
	waitingID, _ := ids.New(serviceClock.Now())
	eventID, _ := ids.New(serviceClock.Now())
	if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,event_id,event_type,state,attempt,safe_error,next_attempt_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'waiting',1,'temporary failure',$6,$7,$7)`, waitingID, housekeepingTenantA, old.EndpointID, eventID, old.EventType, serviceClock.Now().Add(time.Second), serviceClock.Now()); err != nil {
		t.Fatal(err)
	}
	r := webhook.DeliveryListRequest{EndpointID: old.EndpointID, EventID: eventID, State: webhook.DeliveryWaiting, Limit: 1}
	if items, err := s.ListDeliveries(ctx, r); err != nil || len(items) != 1 || items[0].ID != waitingID {
		t.Fatal("waiting SQL filter failed")
	}
	serviceClock.Advance(time.Second)
	if _, err := db.operator.Exec(ctx, `UPDATE webhook_deliveries SET state='succeeded',attempt=2,response_status=204,safe_error='',next_attempt_at=NULL,updated_at=$3 WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, waitingID, serviceClock.Now()); err != nil {
		t.Fatal(err)
	}
	if items, err := s.ListDeliveries(ctx, r); err != nil || len(items) != 0 {
		t.Fatal("obsolete waiting outcome remained")
	}
	r.State = webhook.DeliverySucceeded
	if items, err := s.ListDeliveries(ctx, r); err != nil || len(items) != 1 || items[0].ID != waitingID || items[0].Attempt != 2 {
		t.Fatal("current outcome lost lineage")
	}
	var before int
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='webhook.deliveries_read'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(ctx, `CREATE FUNCTION reject_filtered_webhook_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected filtered audit failure'; END $$; CREATE TRIGGER reject_filtered_webhook_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_filtered_webhook_audit()`); err != nil {
		t.Fatal(err)
	}
	if items, err := s.ListDeliveries(ctx, r); err == nil || items != nil {
		t.Fatal("filtered private page escaped failed audit transaction")
	}
	var after int
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='webhook.deliveries_read'`).Scan(&after); err != nil || after != before {
		t.Fatal("failed filtered read committed partial audit")
	}
}
