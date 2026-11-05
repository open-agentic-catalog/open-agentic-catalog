//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPostgresWebhookNotificationAdmission(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := t.Context()
	const tenant = housekeepingTenantA
	const subject = "00000000-0000-7000-8000-000000000099"
	const insertSQL = `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at,published_at)
VALUES(uuidv7(),$1,'com.open.agentic.catalog.resource.created.v1',$2,
jsonb_build_object('action','resource.created','targetType','resource','targetId',$2::text),clock_timestamp(),CASE WHEN $3 THEN clock_timestamp() END)`
	insert := func(pool *pgxpool.Pool, tenantID string, disabled bool) error {
		_, err := withTenantTx(ctx, pool, tenantID, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
			_, err := tx.Exec(ctx, insertSQL, tenantID, subject, disabled)
			return struct{}{}, err
		})
		return err
	}
	// A single multi-row command must also observe its own prior inserts.
	if _, err := db.operator.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at)
SELECT uuidv7(),$1,'com.open.agentic.catalog.resource.created.v1',$2,
jsonb_build_object('action','resource.created','targetType','resource','targetId',$2::text),clock_timestamp()
FROM generate_series(1,$3)`, tenant, subject, catalog.PendingWebhookEventLimit-2); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errorsFound := make(chan error, 20)
	for n := range 20 {
		pool := db.api
		if n%2 == 0 {
			pool = db.worker
		}
		group.Go(func() { errorsFound <- insert(pool, tenant, false) })
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	readState := func(tenantID string) catalog.NotificationState {
		t.Helper()
		// Recreate the adapter each time: no process-local capacity authority.
		store, err := NewPostgresWebhookStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.NotificationState(ctx, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	state := readState(tenant)
	if state.PendingEvents != 1000 || state.PendingEventLimit != 1000 || state.CapacityLossCount != 18 || state.LastCapacityLossAt == nil || state.DeliveryLossCount != 0 {
		t.Fatalf("concurrent admission: %#v", state)
	}
	var pending, retained int
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FILTER(WHERE published_at IS NULL),count(*) FROM outbox_events WHERE tenant_id=$1`, tenant).Scan(&pending, &retained); err != nil || pending != 1000 || retained != 1018 {
		t.Fatalf("required outbox evidence: %d/%d %v", pending, retained, err)
	}
	if err := insert(db.api, tenant, true); err != nil {
		t.Fatal(err)
	}
	if err := insert(db.api, housekeepingTenantB, false); err != nil {
		t.Fatal(err)
	}
	if got := readState(tenant); got.CapacityLossCount != 18 {
		t.Fatal("disabled write consumed notification capacity")
	}
	if got := readState(housekeepingTenantB); got.PendingEvents != 1 || got.CapacityLossCount != 0 {
		t.Fatal("other tenant could not progress")
	}
	// A single statement that crosses the ceiling must count its own earlier
	// rows under either runtime identity. Roll back each trial so both roles
	// start with the same one-event tenant and no retained loss indication.
	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"api", db.api}, {"worker", db.worker}} {
		t.Run("multi-row-capacity-"+role.name, func(t *testing.T) {
			rollbackTrial := errors.New("roll back multi-row capacity trial")
			_, err := withTenantTx(ctx, role.pool, housekeepingTenantB, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
				_, err := tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at)
SELECT uuidv7(),$1,'com.open.agentic.catalog.resource.created.v1',$2,
jsonb_build_object('action','resource.created','targetType','resource','targetId',$2::text),clock_timestamp()
FROM generate_series(1,$3)`, housekeepingTenantB, subject, catalog.PendingWebhookEventLimit+2)
				if err != nil {
					return struct{}{}, err
				}
				var pending, retained, losses int64
				if err := tx.QueryRow(ctx, `SELECT
(SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND published_at IS NULL AND NOT is_internal),
(SELECT count(*) FROM outbox_events WHERE tenant_id=$1),
capacity_loss_count FROM webhook_notification_state WHERE tenant_id=$1`, housekeepingTenantB).Scan(&pending, &retained, &losses); err != nil {
					return struct{}{}, err
				}
				if pending != catalog.PendingWebhookEventLimit || retained != catalog.PendingWebhookEventLimit+3 || losses != 3 {
					return struct{}{}, fmt.Errorf("multi-row admission: pending=%d retained=%d losses=%d", pending, retained, losses)
				}
				return struct{}{}, rollbackTrial
			})
			if !errors.Is(err, rollbackTrial) {
				t.Fatalf("multi-row capacity trial: %v", err)
			}
			if got := readState(housekeepingTenantB); got.PendingEvents != 1 || got.CapacityLossCount != 0 {
				t.Fatal("rolled-back multi-row trial retained events or loss state")
			}
		})
	}

	// A real Catalog repository mutation keeps its domain, audit and outbox
	// transaction even when the optional notification is retired at capacity.
	now := time.Now().UTC().Truncate(time.Microsecond)
	namespace := catalog.Namespace{ID: "00000000-0000-7000-8000-000000000501", TenantID: tenant, Slug: "capacity", DisplayName: "Capacity", Revision: 1, CreatedAt: now, UpdatedAt: now}
	record := integrationAudit(t, "00000000-0000-7000-8000-000000000502", tenant, "capacity-test", "namespace.created", "namespace", namespace.ID, "", now)
	outbox := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000503", TenantID: tenant, Type: catalog.CanonicalEventType("namespace.created"), Subject: namespace.ID, Data: []byte(fmt.Sprintf(`{"action":"namespace.created","targetType":"namespace","targetId":%q}`, namespace.ID)), CreatedAt: now}
	if _, err := repo.CreateNamespace(ctx, namespace, record, outbox); err != nil {
		t.Fatal(err)
	}
	if got := readState(tenant); got.CapacityLossCount != 19 {
		t.Fatal("domain producer bypassed capacity")
	}
	var domainRows, auditRows, outboxRows int
	if err := db.operator.QueryRow(ctx, `SELECT (SELECT count(*) FROM namespaces WHERE tenant_id=$1 AND id=$2),
(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND id=$3),
(SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND id=$4 AND published_at IS NOT NULL)`, tenant, namespace.ID, record.ID, outbox.ID).Scan(&domainRows, &auditRows, &outboxRows); err != nil || domainRows != 1 || auditRows != 1 || outboxRows != 1 {
		t.Fatalf("atomic domain/evidence at capacity: %d/%d/%d %v", domainRows, auditRows, outboxRows, err)
	}
	rollback := errors.New("controlled transaction rollback")
	_, err = withTenantTx(ctx, db.api, tenant, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
		if _, err := tx.Exec(ctx, insertSQL, tenant, subject, false); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, rollback
	})
	if !errors.Is(err, rollback) || readState(tenant).CapacityLossCount != 19 {
		t.Fatal("rolled-back write retained loss state")
	}
	namespace.ID = "00000000-0000-7000-8000-000000000504"
	namespace.Slug = "failed-audit"
	record.TargetID = namespace.ID // Reusing the committed audit identity forces an actual evidence failure.
	outbox.ID = "00000000-0000-7000-8000-000000000505"
	outbox.Subject = namespace.ID
	outbox.Data = []byte(fmt.Sprintf(`{"action":"namespace.created","targetType":"namespace","targetId":%q}`, namespace.ID))
	if _, err := repo.CreateNamespace(ctx, namespace, record, outbox); err == nil {
		t.Fatal("database audit failure was hidden by saturation policy")
	}
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM namespaces WHERE tenant_id=$1 AND id=$2`, tenant, namespace.ID).Scan(&domainRows); err != nil || domainRows != 0 || readState(tenant).CapacityLossCount != 19 {
		t.Fatal("failed audit committed domain/loss state")
	}

	// A stale repeatable-read snapshot cannot evade admission after another
	// writer commits. Its normal serialization error retains no event or loss.
	tx, err := db.api.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setTenantContext(ctx, tx, tenant); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT admission_revision FROM webhook_notification_state WHERE tenant_id=$1`, tenant).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := insert(db.worker, tenant, false); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, insertSQL, tenant, subject, false)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "40001" {
		t.Fatalf("stale snapshot admission: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readState(tenant); got.PendingEvents != 1000 || got.CapacityLossCount != 20 {
		t.Fatalf("stale writer changed capacity: %#v", got)
	}
	// Claim and retry deferral do not release capacity; acknowledgement does.
	workerRepo, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	// Capacity accounting does not test clock synchronization. Derive a logical
	// claim instant from this fixture and acknowledge within that same lease.
	var claimAt time.Time
	if err := db.operator.QueryRow(ctx, "SELECT max(created_at)+interval '1 second' FROM outbox_events").Scan(&claimAt); err != nil {
		t.Fatal(err)
	}
	claimed, err := workerRepo.ClaimOutbox(ctx, "capacity-worker", claimAt, time.Minute, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v", err)
	}
	if readState(tenant).PendingEvents != 1000 {
		t.Fatal("claim released capacity")
	}
	if _, err := db.operator.Exec(ctx, `UPDATE outbox_events SET published_at=$3,lease_owner=NULL,lease_expires_at=NULL WHERE tenant_id=$1 AND id=$2`, claimed[0].TenantID, claimed[0].ID, claimAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := readState(tenant); got.PendingEvents != 999 {
		t.Fatalf("acknowledged event did not release slot: %#v", got)
	}
	if err := insert(db.worker, tenant, false); err != nil {
		t.Fatal(err)
	}
	if got := readState(tenant); got.PendingEvents != 1000 || got.CapacityLossCount != 20 {
		t.Fatal("released capacity was not reusable")
	}

	// Exact runtime grants never expose payloads or permit a counter reset.
	if _, err := db.api.Exec(ctx, `SELECT data FROM outbox_events`); err == nil {
		t.Fatal("API acquired outbox payload reads")
	}
	for _, query := range []string{
		`UPDATE webhook_notification_state SET capacity_loss_count=0,last_capacity_loss_at=NULL WHERE tenant_id=$1`,
		`UPDATE webhook_notification_state SET admission_revision=admission_revision+1 WHERE tenant_id=$1`,
		`DELETE FROM webhook_notification_state WHERE tenant_id=$1`,
	} {
		_, err := withTenantTx(ctx, db.api, tenant, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) { _, err := tx.Exec(ctx, query, tenant); return struct{}{}, err })
		if err == nil {
			t.Fatalf("runtime directly changed reconciliation state: %s", query)
		}
	}
	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"api", db.api}, {"worker", db.worker}} {
		t.Run("temporary-trigger-cannot-forge-notification-state-"+role.name, func(t *testing.T) {
			_, err := withTenantTx(ctx, role.pool, tenant, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
				var schema string
				if err := tx.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
					return struct{}{}, err
				}
				_, err := tx.Exec(ctx, `CREATE TEMP TABLE notification_guard_probe(id integer);
CREATE FUNCTION pg_temp.notification_guard_probe() RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN UPDATE `+pgx.Identifier{schema, "webhook_notification_state"}.Sanitize()+` SET admission_revision=admission_revision+1; RETURN NEW; END $body$;
CREATE TRIGGER notification_guard_probe AFTER INSERT ON notification_guard_probe
FOR EACH ROW EXECUTE FUNCTION pg_temp.notification_guard_probe()`)
				if err != nil {
					t.Error("runtime-owned temporary trigger setup failed")
					return struct{}{}, fmt.Errorf("create runtime-owned temporary trigger: %w", err)
				}
				_, err = tx.Exec(ctx, `INSERT INTO notification_guard_probe VALUES (1)`)
				return struct{}{}, err
			})
			if err == nil {
				t.Fatal("runtime temporary trigger bypassed notification state guard")
			}
			requireInsufficientPrivilege(t, err)
		})
	}
	_, err = withTenantRead(ctx, db.api, tenant, func(tx pgx.Tx) (struct{}, error) {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM webhook_notification_state WHERE tenant_id=$1`, housekeepingTenantB).Scan(&count); err != nil {
			return struct{}{}, err
		}
		if count != 0 {
			return struct{}{}, errors.New("cross-tenant notification state exposed")
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Keep the schema verifier fail-closed while deliberately pinning the
	// new migration's constraint and trigger inventories from real PostgreSQL.
	inspection, err := db.operator.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inspection.Rollback(context.Background()) }()
	var schema string
	if err := inspection.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoreConstraints(ctx, inspection, schema); err != nil {
		t.Error(err)
	}
	if err := verifyRestoreTriggers(ctx, inspection, schema); err != nil {
		t.Error(err)
	}
}

func TestPostgresWebhookOperationalLossAndEligibility(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := t.Context()
	repo, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	const endpoint = "00000000-0000-7000-8000-000000000601"
	const event = "00000000-0000-7000-8000-000000000602"
	const delivery = "00000000-0000-7000-8000-000000000603"
	if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_endpoints(id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at)
VALUES($1,$2,'receiver','https://receiver.example/events',ARRAY['*'],'active',1,1,'fixture','fixture',clock_timestamp(),clock_timestamp())`, endpoint, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at)
VALUES($1::uuid,$2,'com.open.agentic.catalog.resource.created.v1',$1::text,jsonb_build_object('action','resource.created','targetType','resource','targetId',$1::text),clock_timestamp())`, event, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	snapshot, err := webhookOperationalSnapshot(ctx, repo.pool, time.Now().UTC())
	if err != nil || snapshot.DispatchableEvents != 1 || snapshot.WaitingRetries != 0 {
		t.Fatalf("initial signals: %#v %v", snapshot, err)
	}
	if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,event_id,event_type,state,attempt,response_status,safe_error,next_attempt_at,created_at,updated_at)
VALUES($1,$2,$3,$4,'com.open.agentic.catalog.resource.created.v1','waiting',1,429,'receiver_unavailable',clock_timestamp()+interval '1 hour',clock_timestamp(),clock_timestamp())`, delivery, housekeepingTenantA, endpoint, event); err != nil {
		t.Fatal(err)
	}
	snapshot, err = webhookOperationalSnapshot(ctx, repo.pool, time.Now().UTC())
	if err != nil || snapshot.DispatchableEvents != 0 || snapshot.WaitingRetries != 1 || snapshot.LastAttemptFailedReceivers != 1 || snapshot.TerminalLostDeliveries != 0 {
		t.Fatalf("waiting signals: %#v %v", snapshot, err)
	}
	if _, err := db.operator.Exec(ctx, `UPDATE webhook_deliveries SET state='dead_letter',attempt=2,next_attempt_at=NULL,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, delivery); err != nil {
		t.Fatal(err)
	}
	snapshot, err = webhookOperationalSnapshot(ctx, repo.pool, time.Now().UTC())
	if err != nil || snapshot.WaitingRetries != 0 || snapshot.TerminalLostDeliveries != 1 || snapshot.ReconciliationTenants != 1 {
		t.Fatalf("terminal signals: %#v %v", snapshot, err)
	}
	claimed, err := repo.ClaimOutbox(ctx, "signal-worker", time.Now().UTC(), time.Minute, 1)
	if err != nil || len(claimed) != 1 || claimed[0].ID != event {
		t.Fatalf("signal fixture claim: %v", err)
	}
	if err := repo.MarkOutboxPublished(ctx, housekeepingTenantA, event, "signal-worker", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	result, err := db.housekeeper.ReclaimWebhookDeliveries(ctx, 10*time.Millisecond, 100)
	if err != nil || result.Marked != 1 {
		t.Fatalf("terminal retention mark: %#v %v", result, err)
	}
	// Marking and reclamation are separate statements: a short marker may
	// expire within the first sweep. Count both sweeps and let PostgreSQL
	// decide when its marker expires rather than relying on a host sleep.
	reclaimed := result.Reclaimed
	started := time.Now()
	for reclaimed == 0 && time.Since(started) < 5*time.Second {
		time.Sleep(10 * time.Millisecond)
		result, err = db.housekeeper.ReclaimWebhookDeliveries(ctx, time.Hour, 100)
		if err != nil || result.Marked != 0 {
			t.Fatalf("terminal retention sweep: %#v %v", result, err)
		}
		reclaimed += result.Reclaimed
	}
	if reclaimed != 1 {
		t.Fatalf("terminal retention reclaimed %d deliveries", reclaimed)
	}
	if result, err := db.housekeeper.ReclaimWebhookDeliveries(ctx, time.Hour, 100); err != nil || result.Reclaimed != 0 || result.Marked != 0 {
		t.Fatalf("repeated terminal retention: %#v %v", result, err)
	}
	snapshot, err = webhookOperationalSnapshot(ctx, repo.pool, time.Now().UTC())
	if err != nil || snapshot.TerminalLostDeliveries != 1 || snapshot.LastAttemptFailedReceivers != 0 {
		t.Fatalf("loss checkpoint after evidence cleanup: %#v %v", snapshot, err)
	}
	// A failed synthetic test is evidence, not a missed domain notification.
	if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,event_id,event_type,state,attempt,response_status,safe_error,created_at,updated_at)
VALUES(uuidv7(),$1,$2,uuidv7(),'com.open.agentic.catalog.webhook.test.v1','dead_letter',1,401,'receiver_rejected',clock_timestamp(),clock_timestamp())`, housekeepingTenantA, endpoint); err != nil {
		t.Fatal(err)
	}
	snapshot, err = webhookOperationalSnapshot(ctx, repo.pool, time.Now().UTC())
	if err != nil || snapshot.TerminalLostDeliveries != 1 || snapshot.LastAttemptFailedReceivers != 0 {
		t.Fatalf("synthetic failure polluted loss state: %#v %v", snapshot, err)
	}
}
