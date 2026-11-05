//go:build integration

package database

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

func TestPostgresWebhookAuditsCommitWithStateAndProtectPrivateReads(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := postgresFixtureContext(t, db, context.Background(), identity.Actor{TenantID: housekeepingTenantA, Subject: "private-subject", MembershipID: "00000000-0000-7000-8000-000000000901", RealmID: "00000000-0000-7000-8000-000000000902", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	principal, _ := identity.FromContext(ctx)
	var initialOutbox int
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&initialOutbox); err != nil {
		t.Fatal(err)
	}
	repo, _ := NewPostgresRepository(db.api)
	store, _ := NewPostgresWebhookStore(repo)
	cipher, _ := webhook.NewAESGCM(make([]byte, 32))
	serviceClock := clock.NewFake(time.Now().UTC().Truncate(time.Microsecond))
	s, err := webhook.NewService(store, cipher, &webhookIntegrationSender{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	input := webhook.CreateInput{Name: "Receiver", URL: "https://receiver-b.example/events", EventTypes: []string{"*"}}
	created, err := s.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := s.Test(ctx, created.Endpoint.ID, 1)
	if err != nil || delivery.State != webhook.DeliverySucceeded {
		t.Fatalf("test delivery: %s %v", delivery.State, err)
	}
	items, err := s.ListDeliveries(ctx, webhook.DeliveryListRequest{EndpointID: created.Endpoint.ID, Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("delivery page: %d %v", len(items), err)
	}
	// A regressed application clock reproduces this guard's generic failure,
	// but does not establish the cause of the historical packaged refusal.
	serviceClock.Advance(-time.Microsecond)
	commands, err := NewPostgresIdempotencyStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	scope := idempotency.Scope{TenantID: housekeepingTenantA, Actor: "membership:" + principal.RealmID + ":" + principal.MembershipID, Route: "POST /api/v1/webhooks/" + created.Endpoint.ID + ":rotate-secret", Key: "backdated-rotation", AuthorizationDigest: digestText("webhook-rotation-authority")}
	var rejected webhook.CreateResult
	var rotationErr error
	execution, err := commands.Execute(ctx, scope, "backdated-request", serviceClock.Now().Add(time.Hour), func(commandCtx context.Context) idempotency.Response {
		rejected, rotationErr = s.RotateSecret(commandCtx, created.Endpoint.ID, 1, webhook.RotateInput{})
		idempotency.AbortCommand(commandCtx, rotationErr)
		return idempotency.Response{Digest: "backdated-request", Status: 503}
	})
	if err != nil || execution.Replayed || execution.Pending || execution.Response.Status != 503 {
		t.Fatalf("rejected rotation receipt: %v", err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(rotationErr, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "webhook endpoint lifecycle transition is invalid" || catalog.ErrorCodeOf(rotationErr) != catalog.CodeDependencyUnavailable {
		t.Fatalf("backdated rotation did not reach the lifecycle guard: %v", rotationErr)
	}
	if safeerr.Stage(rotationErr) != "webhook.endpoint.time_regression" {
		t.Fatal("actual backdated endpoint transition lost its safe diagnostic stage")
	}
	if !reflect.DeepEqual(rejected, webhook.CreateResult{}) {
		t.Fatal("rejected rotation disclosed new signing material")
	}
	unchanged, err := store.GetEndpoint(ctx, housekeepingTenantA, created.Endpoint.ID)
	if err != nil || !reflect.DeepEqual(unchanged, created.Endpoint) {
		t.Fatalf("backdated rotation changed retained endpoint: %v", err)
	}
	var rotations int
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND target_id=$2 AND action='webhook.secret_rotated'`, housekeepingTenantA, created.Endpoint.ID).Scan(&rotations); err != nil || rotations != 0 {
		t.Fatalf("backdated rotation committed audit evidence: %d %v", rotations, err)
	}
	serviceClock.Advance(time.Microsecond)
	replay, err := commands.Execute(ctx, scope, "backdated-request", serviceClock.Now().Add(time.Hour), func(context.Context) idempotency.Response {
		t.Error("completed rejected rotation executed again after clock recovery")
		return idempotency.Response{Digest: "backdated-request", Status: 200}
	})
	if err != nil || !replay.Replayed || replay.Response.Status != 503 || replay.ReservationID != execution.ReservationID {
		t.Fatalf("rejected rotation replay: %v", err)
	}
	if _, err := db.operator.Exec(ctx, `CREATE FUNCTION reject_webhook_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_webhook_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_webhook_audit()`); err != nil {
		t.Fatal(err)
	}
	secondInput := input
	secondInput.URL = "https://another.example/events"
	if _, err := s.Create(ctx, secondInput); err == nil {
		t.Fatal("creation survived audit failure")
	}
	name := "Renamed"
	if _, err := s.Update(ctx, created.Endpoint.ID, webhook.PatchInput{Name: &name}, 1); err == nil {
		t.Fatal("update survived audit failure")
	}
	if _, err := s.RotateSecret(ctx, created.Endpoint.ID, 1, webhook.RotateInput{}); err == nil {
		t.Fatal("rotation survived audit failure")
	}
	if _, err := s.Disable(ctx, created.Endpoint.ID, 1); err == nil {
		t.Fatal("disable survived audit failure")
	}
	if _, err := s.Test(ctx, created.Endpoint.ID, 1); err == nil {
		t.Fatal("delivery outcome survived audit failure")
	}
	if items, err := s.ListDeliveries(ctx, webhook.DeliveryListRequest{EndpointID: created.Endpoint.ID, Limit: 10}); err == nil || items != nil {
		t.Fatal("private read released data without audit")
	}
	retained, err := store.GetEndpoint(ctx, housekeepingTenantA, created.Endpoint.ID)
	if err != nil || retained.Revision != 1 || retained.Status != webhook.StatusActive || retained.Name != input.Name || retained.SecretVersion != 1 {
		t.Fatal("failed audit changed endpoint")
	}
	assertCount := func(query string, want int) {
		t.Helper()
		var got int
		if err := db.operator.QueryRow(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("row count %d want %d: %v", got, want, err)
		}
	}
	assertCount(`SELECT count(*) FROM webhook_endpoints`, 1)
	assertCount(`SELECT count(*) FROM webhook_deliveries`, 1)
	assertCount(`SELECT count(*) FROM audit_events WHERE action LIKE 'webhook.%'`, 3)
	assertCount(`SELECT count(*) FROM outbox_events`, initialOutbox)
	if _, err := db.operator.Exec(ctx, `DROP TRIGGER reject_webhook_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Update(ctx, created.Endpoint.ID, webhook.PatchInput{Name: &name}, 1)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := s.RotateSecret(ctx, created.Endpoint.ID, updated.Revision, webhook.RotateInput{})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Endpoint.Revision != updated.Revision+1 || rotated.Endpoint.SecretVersion != 2 || rotated.Secret == "" || rotated.Secret == created.Secret || rotated.PreviousSecretExpiresAt == nil || !rotated.PreviousSecretExpiresAt.Equal(serviceClock.Now().Add(webhook.SigningOverlap)) {
		t.Fatal("recovered routine rotation omitted its new signer, revision or overlap")
	}
	emergency, err := s.RotateSecret(ctx, created.Endpoint.ID, rotated.Endpoint.Revision, webhook.RotateInput{RevokePrevious: true})
	if err != nil || emergency.PreviousSecretExpiresAt != nil || emergency.Endpoint.SecretVersion != 3 || emergency.Endpoint.Revision != rotated.Endpoint.Revision+1 || emergency.Secret == "" || emergency.Secret == rotated.Secret {
		t.Fatalf("emergency rotation after recovery: %v", err)
	}
	if _, err := s.Disable(ctx, created.Endpoint.ID, emergency.Endpoint.Revision); err != nil {
		t.Fatal(err)
	}
	records, err := repo.AuditEvents(ctx, housekeepingTenantA, catalog.AuditEventListRequest{Filters: audit.Query{Actor: principal.MembershipID}, Limit: 100})
	if err != nil || len(records) != 7 {
		t.Fatalf("audit inventory: %d %v", len(records), err)
	}
	for _, record := range records {
		if err := audit.ValidateNewRecord(record); err != nil {
			t.Fatal(err)
		}
		if record.Actor != principal.MembershipID {
			t.Fatal("unstable principal")
		}
	}
	other, err := repo.AuditEvents(ctx, housekeepingTenantB, catalog.AuditEventListRequest{Limit: 100})
	if err == nil || len(other) != 0 {
		t.Fatal("mismatched actor tenant was accepted")
	}
	other, err = repo.AuditEvents(context.Background(), housekeepingTenantB, catalog.AuditEventListRequest{Limit: 100})
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-tenant audit disclosure: %d %v", len(other), err)
	}
	if _, err := withTenantTx(ctx, db.worker, housekeepingTenantA, pgx.TxOptions{}, func(tx pgx.Tx) (int, error) {
		var n int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1`, housekeepingTenantA).Scan(&n)
		return n, err
	}); err == nil {
		t.Fatal("worker obtained audit read authority")
	}
	assertCount(`SELECT count(*) FROM outbox_events`, initialOutbox)
}
