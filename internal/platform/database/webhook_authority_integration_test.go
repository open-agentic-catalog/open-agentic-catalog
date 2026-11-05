//go:build integration

package database

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPostgresWebhookNotificationUnprivilegedOwner(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := t.Context()
	var schema, originalOwner string
	if err := db.operator.QueryRow(ctx, `SELECT current_schema(),current_user`).Scan(&schema, &originalOwner); err != nil {
		t.Fatal(err)
	}
	owner := schema + "_owner"
	qualifiedOwner := pgx.Identifier{owner}.Sanitize()
	if _, err := db.operator.Exec(ctx, `CREATE ROLE `+qualifiedOwner+` NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT`); err != nil {
		t.Fatal(err)
	}
	tables := []string{"outbox_events", "webhook_deliveries", "webhook_notification_state"}
	functions := []string{"admit_webhook_notification", "record_webhook_delivery_loss"}
	changeOwner := func(ctx context.Context, owner string) error {
		for _, table := range tables {
			if _, err := db.operator.Exec(ctx, `ALTER TABLE `+pgx.Identifier{schema, table}.Sanitize()+` OWNER TO `+pgx.Identifier{owner}.Sanitize()); err != nil {
				return err
			}
		}
		for _, function := range functions {
			if _, err := db.operator.Exec(ctx, `ALTER FUNCTION `+pgx.Identifier{schema, function}.Sanitize()+`() OWNER TO `+pgx.Identifier{owner}.Sanitize()); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() {
		if err := changeOwner(context.Background(), originalOwner); err != nil {
			t.Error(err)
		}
		if _, err := db.operator.Exec(context.Background(), `REVOKE ALL ON SCHEMA `+pgx.Identifier{schema}.Sanitize()+` FROM `+qualifiedOwner+`; DROP ROLE `+qualifiedOwner); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.operator.Exec(ctx, `GRANT USAGE ON SCHEMA `+pgx.Identifier{schema}.Sanitize()+` TO `+qualifiedOwner); err != nil {
		t.Fatal(err)
	}
	if err := changeOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
	// FORCE RLS still applies to this non-superuser table owner. The fixed
	// functions must work with the caller's explicit tenant context, without
	// runtime role membership, a BYPASSRLS owner or database-wide TEMP changes.
	_, err := withTenantTx(ctx, db.api, housekeepingTenantA, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
		_, err := tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at)
SELECT uuidv7(),$1,'com.open.agentic.catalog.resource.created.v1',$2,
jsonb_build_object('action','resource.created','targetType','resource','targetId',$2::text),clock_timestamp()
FROM generate_series(1,1001)`, housekeepingTenantA, "00000000-0000-7000-8000-000000000099")
		return struct{}{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = withTenantTx(ctx, db.worker, housekeepingTenantA, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
		// A delivery insert needs an existing tenant-bound endpoint. The
		// trusted fixture owner creates it; only the loss write uses worker.
		var event string
		if err := tx.QueryRow(ctx, `SELECT id FROM outbox_events WHERE tenant_id=$1 AND published_at IS NULL LIMIT 1`, housekeepingTenantA).Scan(&event); err != nil {
			return struct{}{}, err
		}
		const endpoint = "00000000-0000-7000-8000-000000000100"
		if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_endpoints(id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at)
VALUES($1,$2,'Receiver','https://receiver.example/events',ARRAY['*'],'active',1,1,'fixture','test',clock_timestamp(),clock_timestamp())`, endpoint, housekeepingTenantA); err != nil {
			return struct{}{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,event_id,event_type,state,attempt,response_status,safe_error,created_at,updated_at)
VALUES(uuidv7(),$1,$2,$3,'com.open.agentic.catalog.resource.created.v1','dead_letter',1,401,'receiver_rejected',clock_timestamp(),clock_timestamp())`, housekeepingTenantA, endpoint, event); err != nil {
			return struct{}{}, err
		}
		var capacity, losses int64
		if err := tx.QueryRow(ctx, `SELECT capacity_loss_count,delivery_loss_count FROM webhook_notification_state WHERE tenant_id=$1`, housekeepingTenantA).Scan(&capacity, &losses); err != nil {
			return struct{}{}, err
		}
		if capacity != 1 || losses != 1 {
			return struct{}{}, fmt.Errorf("owner-authorized counters: capacity=%d delivery=%d", capacity, losses)
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, drift := range []struct {
		name, alteration string
	}{
		{"valid-owner", ""},
		{"invoker-drift", " SECURITY INVOKER"},
		{"owner-drift", " OWNER TO " + pgx.Identifier{originalOwner}.Sanitize()},
	} {
		t.Run(drift.name, func(t *testing.T) {
			tx, err := db.operator.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if drift.alteration != "" {
				if _, err := tx.Exec(ctx, `ALTER FUNCTION `+pgx.Identifier{schema, "admit_webhook_notification"}.Sanitize()+`() `+drift.alteration); err != nil {
					t.Fatal(err)
				}
			}
			err = verifyRestoreTriggers(ctx, tx, schema)
			if drift.alteration == "" && err != nil || drift.alteration != "" && err == nil {
				t.Fatalf("restore trigger execution-property check: %v", err)
			}
		})
	}
}
