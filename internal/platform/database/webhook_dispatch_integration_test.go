//go:build integration

package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type webhookIntegrationSender struct{ urls []string }

func (s *webhookIntegrationSender) Send(_ context.Context, request webhook.SendRequest) (webhook.SendResult, error) {
	s.urls = append(s.urls, request.URL)
	if request.URL == "https://receiver-a.example/events" {
		return webhook.SendResult{Status: 429, RetryAfter: "120"}, nil
	}
	return webhook.SendResult{Status: 204}, nil
}

func TestPostgresWebhookDeferralAllowsOtherTenantProgress(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := context.Background()
	apiRepo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	workerRepo, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	apiStore, _ := NewPostgresWebhookStore(apiRepo)
	workerStore, _ := NewPostgresWebhookStore(workerRepo)
	now := time.Now().UTC().Truncate(time.Microsecond)
	serviceClock := clock.NewFake(now)
	ids := id.Generator{}
	cipher, _ := webhook.NewAESGCM(make([]byte, 32))
	secret, _ := cipher.Encrypt([]byte("whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	eventIDs := make([]string, 0, 2)
	for index, tenant := range []string{housekeepingTenantA, housekeepingTenantB} {
		endpointID, _ := ids.New(now)
		endpoint := webhook.Endpoint{ID: endpointID, TenantID: tenant, Name: "Receiver", URL: fmt.Sprintf("https://receiver-%c.example/events", 'a'+index), EventTypes: []string{"*"}, Status: webhook.StatusActive, Revision: 1, SecretVersion: 1, SecretCiphertext: secret, CreatedBy: "integration-admin", CreatedAt: now, UpdatedAt: now}
		if _, err := apiStore.CreateEndpoint(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
		eventID, _ := ids.New(now.Add(time.Duration(index) * time.Millisecond))
		subject, _ := ids.New(now)
		data, err := events.MarshalReference("resource.created", "resource", subject, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.operator.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,is_internal,created_at) VALUES ($1,$2,$3,$4,$5,false,$6)`, eventID, tenant, events.CanonicalType("resource.created"), subject, data, now.Add(time.Duration(index)*time.Microsecond)); err != nil {
			t.Fatal(err)
		}
		eventIDs = append(eventIDs, eventID)
	}
	serviceClock.Advance(time.Millisecond)
	sender := &webhookIntegrationSender{}
	dispatcher, err := webhook.NewDispatcher(workerRepo, workerStore, cipher, sender, serviceClock, ids, "first-worker", time.Minute, 2, time.Millisecond, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := dispatcher.DispatchOnce(ctx); err != nil || n != 0 {
		t.Fatalf("first failed receiver: %d %v", n, err)
	}
	if n, err := dispatcher.DispatchOnce(ctx); err != nil || n != 1 {
		t.Fatalf("other tenant starved: %d %v", n, err)
	}
	if len(sender.urls) != 2 || sender.urls[1] != "https://receiver-b.example/events" {
		t.Fatal("other receiver did not progress")
	}
	if n, err := dispatcher.DispatchOnce(ctx); err != nil || n != 0 || len(sender.urls) != 2 {
		t.Fatalf("retry attempted too early: %d %v", n, err)
	}
	if err := workerRepo.DeferOutbox(ctx, housekeepingTenantB, eventIDs[0], "first-worker", serviceClock.Now(), serviceClock.Now().Add(time.Second)); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant deferral: %v", err)
	}
	if err := apiRepo.DeferOutbox(ctx, housekeepingTenantA, eventIDs[0], "first-worker", serviceClock.Now(), serviceClock.Now().Add(time.Second)); err == nil {
		t.Fatal("API obtained worker dispatch authority")
	}
	if _, err := db.operator.Exec(ctx, `UPDATE outbox_events SET dispatch_after=dispatch_after+interval '1 second' WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, eventIDs[0]); err == nil {
		t.Fatal("unleased direct scheduling was accepted")
	}
	serviceClock.Advance(121 * time.Second)
	restarted, err := webhook.NewDispatcher(workerRepo, workerStore, cipher, sender, serviceClock, ids, "restarted-worker", time.Minute, 2, time.Millisecond, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := restarted.DispatchOnce(ctx); err != nil || n != 1 {
		t.Fatalf("restart did not exhaust retry safely: %d %v", n, err)
	}
	var pending int
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending after completion=%d %v", pending, err)
	}
}

func TestPostgresWebhookEndpointSelectionIsBoundedAndTenantScoped(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := context.Background()
	repo, _ := NewPostgresRepository(db.api)
	store, _ := NewPostgresWebhookStore(repo)
	now := time.Now().UTC().Truncate(time.Microsecond)
	ids := id.Generator{}
	eventID, _ := ids.New(now)
	event := catalog.OutboxEvent{ID: eventID, TenantID: housekeepingTenantA, Type: events.CanonicalType("resource.created"), CreatedAt: now}
	for index := 0; index < 24; index++ {
		endpointID, _ := ids.New(now)
		tenant := housekeepingTenantA
		if index == 23 {
			tenant = housekeepingTenantB
		}
		endpoint := webhook.Endpoint{ID: endpointID, TenantID: tenant, Name: "Receiver", URL: fmt.Sprintf("https://receiver.example/%d", index), EventTypes: []string{"*"}, Status: webhook.StatusActive, Revision: 1, SecretVersion: 1, SecretCiphertext: []byte("selection-only"), CreatedBy: "integration-admin", CreatedAt: now, UpdatedAt: now}
		if _, err := store.CreateEndpoint(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{0, 10} {
		if _, err := store.ListPendingEndpoints(ctx, event, limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid bound accepted: %v", err)
		}
	}
	items, err := store.ListPendingEndpoints(ctx, event, 9)
	if err != nil || len(items) != 9 {
		t.Fatalf("bounded pending selection=%d %v", len(items), err)
	}
	for _, item := range items {
		if item.TenantID != housekeepingTenantA {
			t.Fatal("cross-tenant endpoint selected")
		}
	}
	event.CreatedAt = now.Add(-time.Second)
	if items, err := store.ListPendingEndpoints(ctx, event, 9); err != nil || len(items) != 0 {
		t.Fatalf("historical event selected new endpoints: %d %v", len(items), err)
	}
}

func TestWebhookMigrationRetainsRestoreConstraintQualification(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := context.Background()
	tx, err := db.operator.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	schema, _, err := currentCatalogSchema(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoreConstraints(ctx, tx, schema); err != nil {
		t.Fatal(err)
	}
}
