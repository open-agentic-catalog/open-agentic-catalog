//go:build integration

package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
)

func TestPostgresWebhookDeliveryRetention(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := t.Context()
	id := func(n int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", n) }
	const realType = "com.open.agentic.catalog.resource.created.v1"
	const testType = "com.open.agentic.catalog.webhook.test.v1"
	for i, tenant := range []string{housekeepingTenantA, housekeepingTenantB} {
		if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_endpoints(id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at)
VALUES ($1,$2,'Receiver','https://receiver.example/events',ARRAY['*'],'active',1,1,'fixture','test',clock_timestamp(),clock_timestamp())`, id(100+i), tenant); err != nil {
			t.Fatal(err)
		}
	}
	insertParent := func(n int, tenant string, published bool) {
		t.Helper()
		if _, err := db.operator.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at,published_at)
VALUES ($1::uuid,$2,$3,$1::text,jsonb_build_object('action','resource.created','targetType','resource','targetId',$1::text),clock_timestamp(),CASE WHEN $4 THEN clock_timestamp() END)`, id(n), tenant, realType, published); err != nil {
			t.Fatal(err)
		}
	}
	insertDelivery := func(n, parent, endpoint int, tenant, eventType, state string) {
		t.Helper()
		if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,event_id,event_type,state,attempt,response_status,safe_error,next_attempt_at,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,1,CASE WHEN $6='succeeded' THEN 204 ELSE 503 END,
 CASE WHEN $6='succeeded' THEN '' ELSE 'receiver_unavailable' END,
 CASE WHEN $6='waiting' THEN clock_timestamp()+interval '1 hour' END,clock_timestamp(),clock_timestamp())`, id(n), tenant, id(endpoint), id(parent), eventType, state); err != nil {
			t.Fatal(err)
		}
	}
	insertParent(200, housekeepingTenantA, true)
	insertParent(201, housekeepingTenantA, false)
	insertParent(202, housekeepingTenantB, true)
	insertParent(203, housekeepingTenantA, true)
	insertParent(204, housekeepingTenantA, true)
	insertParent(205, housekeepingTenantA, true)
	insertDelivery(300, 200, 100, housekeepingTenantA, realType, "succeeded")
	insertDelivery(301, 203, 100, housekeepingTenantA, realType, "waiting")
	insertDelivery(302, 204, 100, housekeepingTenantA, realType, "dead_letter")
	insertDelivery(303, 400, 100, housekeepingTenantA, testType, "succeeded")
	insertDelivery(304, 401, 100, housekeepingTenantA, testType, "dead_letter")
	insertDelivery(305, 202, 101, housekeepingTenantB, realType, "succeeded")
	insertDelivery(306, 201, 100, housekeepingTenantA, realType, "succeeded")
	insertDelivery(307, 499, 100, housekeepingTenantA, realType, "succeeded")
	insertDelivery(308, 202, 100, housekeepingTenantA, realType, "succeeded")
	insertDelivery(309, 402, 100, housekeepingTenantA, testType, "waiting")
	insertDelivery(310, 205, 100, housekeepingTenantA, testType, "succeeded")

	// Endpoint disable or changed subscriptions do not turn a last waiting
	// attempt into a success, nor prevent cleanup after parent acknowledgement.
	// Advance this fixture from its retained timestamp so a wall-clock correction
	// cannot violate the endpoint's monotonic lifecycle guard.
	if _, err := db.operator.Exec(ctx, `UPDATE webhook_endpoints SET status='disabled',revision=revision+1,updated_at=updated_at+interval '1 microsecond' WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, id(100)); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{300, 306, 307, 308, 309, 310} {
		if _, err := db.worker.Exec(ctx, `DELETE FROM webhook_deliveries WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, id(n)); err == nil {
			t.Fatalf("unmarked/protected delivery %d was deleted", n)
		}
	}
	for _, n := range []int{306, 307, 308, 309, 310} {
		if _, err := db.worker.Exec(ctx, `UPDATE webhook_deliveries SET cleanup_after=clock_timestamp()+interval '1 hour' WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, id(n)); err == nil {
			t.Fatalf("protected delivery %d was marked", n)
		}
	}
	for _, query := range []string{
		`SELECT cleanup_after FROM webhook_deliveries`,
		`UPDATE webhook_deliveries SET cleanup_after=clock_timestamp()+interval '1 hour'`,
		`DELETE FROM webhook_deliveries`,
	} {
		if _, err := db.api.Exec(ctx, query); err == nil {
			t.Fatalf("API acquired worker-only cleanup authority: %s", query)
		} else {
			requireInsufficientPrivilege(t, err)
		}
	}

	// A locked row is skipped without preventing another tenant's progress.
	lock, err := db.worker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(ctx) }()
	if _, err := lock.Exec(ctx, `SELECT id FROM webhook_deliveries WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, housekeepingTenantA, id(300)); err != nil {
		t.Fatal(err)
	}
	result, err := db.housekeeper.ReclaimWebhookDeliveries(ctx, time.Second, housekeeping.BatchLimit)
	if err != nil || result.Marked != 5 || result.Reclaimed != 0 {
		t.Fatalf("bounded mark skips locked row: %#v %v", result, err)
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	result, err = db.housekeeper.ReclaimWebhookDeliveries(ctx, time.Second, housekeeping.BatchLimit)
	if err != nil || result.Marked != 1 {
		t.Fatalf("mark released row: %#v %v", result, err)
	}
	for _, expression := range []string{"NULL", "cleanup_after+interval '1 hour'", "clock_timestamp()-interval '1 hour'"} {
		if _, err := db.worker.Exec(ctx, `UPDATE webhook_deliveries SET cleanup_after=`+expression+` WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, id(300)); err == nil {
			t.Fatal("cleanup marker was rewritten")
		}
	}
	// PostgreSQL owns these markers. Do not compare the container's wall
	// clock with the host clock: small offsets made this assertion flaky.
	waitForCleanup := func(query string) {
		t.Helper()
		waitStarted := time.Now()
		for {
			var elapsed bool
			if err := db.operator.QueryRow(ctx, query).Scan(&elapsed); err != nil {
				t.Fatal(err)
			}
			if elapsed {
				return
			}
			if time.Since(waitStarted) > 5*time.Second {
				t.Fatal("database cleanup markers did not elapse within the test bound")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitForCleanup(`SELECT max(cleanup_after)<=clock_timestamp() FROM webhook_deliveries`)
	result, err = db.housekeeper.ReclaimWebhookDeliveries(ctx, time.Hour, housekeeping.BatchLimit)
	if err != nil || result.Reclaimed != 6 || result.Marked != 0 {
		t.Fatalf("elapsed delivery cleanup: %#v %v", result, err)
	}
	var retained int
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries`).Scan(&retained); err != nil || retained != 5 {
		t.Fatalf("protected delivery count %d: %v", retained, err)
	}
	result, err = db.housekeeper.ReclaimAcknowledgedOutbox(ctx, 20*time.Millisecond, housekeeping.BatchLimit)
	if err != nil || result.Marked != 4 {
		t.Fatalf("parent cleanup after last delivery: %#v %v", result, err)
	}
	// A host sleep does not establish database eligibility for the parents.
	waitForCleanup(`SELECT max(cleanup_after)<=clock_timestamp() FROM outbox_events`)
	result, err = db.housekeeper.ReclaimAcknowledgedOutbox(ctx, time.Hour, housekeeping.BatchLimit)
	if err != nil || result.Reclaimed != 4 {
		t.Fatalf("eventual parent cleanup: %#v %v", result, err)
	}

	// Recognized test sends are valid restore evidence; arbitrary missing,
	// cross-tenant and mismatched parents remain integrity findings.
	insertDelivery(311, 403, 101, housekeepingTenantB, testType, "succeeded")
	tx, err := db.operator.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var schema string
	if err := tx.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoreTriggers(ctx, tx, schema); err != nil {
		t.Error(err)
	}
	rows, more, err := scanRestoreWebhookDeliveries(ctx, tx, housekeepingTenantB, "", 100)
	if err != nil || more || len(rows) != 1 || rows[0].finding != nil {
		t.Fatalf("synthetic test restore: %#v %t %v", rows, more, err)
	}
	rows, _, err = scanRestoreWebhookDeliveries(ctx, tx, housekeepingTenantA, "", 100)
	if err != nil || len(rows) != 5 {
		t.Fatalf("protected restore page: %d %v", len(rows), err)
	}
	for _, row := range rows {
		if (row.key == id(306)) != (row.finding == nil) {
			t.Fatalf("binding classification for %s: %#v", row.key, row.finding)
		}
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// A restored premature cleanup marker is an integrity finding even when
	// its exact event binding is valid. This owner-only corruption is rolled
	// back with the trigger definition, never used as an application repair.
	tamper, err := db.operator.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tamper.Rollback(ctx) }()
	if _, err := tamper.Exec(ctx, `ALTER TABLE webhook_deliveries DISABLE TRIGGER webhook_deliveries_lifecycle`); err != nil {
		t.Fatal(err)
	}
	if _, err := tamper.Exec(ctx, `UPDATE webhook_deliveries SET cleanup_after=clock_timestamp()+interval '1 hour' WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, id(306)); err != nil {
		t.Fatal(err)
	}
	if _, err := tamper.Exec(ctx, `ALTER TABLE webhook_deliveries ENABLE TRIGGER webhook_deliveries_lifecycle`); err != nil {
		t.Fatal(err)
	}
	rows, _, err = scanRestoreWebhookDeliveries(ctx, tamper, housekeepingTenantA, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.key == id(306) {
			found = row.finding != nil && row.finding.Code == "RESTORE_WEBHOOK_DELIVERY_CLEANUP_INVALID"
		}
	}
	if !found {
		t.Fatal("restore accepted premature delivery cleanup")
	}
	if err := tamper.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// Every marking opportunity is bounded in SQL, including synthetic sends.
	for n := 0; n < 101; n++ {
		insertDelivery(1000+n, 2000+n, 101, housekeepingTenantB, testType, "succeeded")
	}
	result, err = db.housekeeper.ReclaimWebhookDeliveries(ctx, time.Hour, housekeeping.BatchLimit)
	if err != nil || result.Marked != 100 || !result.Saturated {
		t.Fatalf("bounded synthetic page: %#v %v", result, err)
	}
	result, err = db.housekeeper.ReclaimWebhookDeliveries(ctx, time.Hour, housekeeping.BatchLimit)
	if err != nil || result.Marked != 2 || result.Saturated {
		t.Fatalf("synthetic continuation: %#v %v", result, err)
	}
}
