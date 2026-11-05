//go:build integration

package database

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestPostgresWebhookOverviewLatestRetentionAndBounds(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresWebhookStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	reader := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "ordinary-reader", Roles: []identity.Role{identity.RoleReader}})
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "webhook-overview", Roles: []identity.Role{identity.RoleTenantAdmin}})
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := func(n int, tenant, status string) string {
		t.Helper()
		key := fmt.Sprintf("00000000-0000-7000-8000-%012d", n)
		_, err := db.operator.Exec(t.Context(), `INSERT INTO webhook_endpoints(id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at) VALUES($1,$2,$3,'https://receiver.example/'||$1::uuid::text,ARRAY['*'],$4,1,1,'fixture','fixture',$5,$5)`, key, tenant, fmt.Sprintf("Receiver %d", n), status, at)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	add := func(n int, key, tenant, state string, test bool, stamp time.Time) string {
		t.Helper()
		typ := catalog.CanonicalEventType("resource.created")
		if test {
			typ = catalog.CanonicalEventType("webhook.test")
		}
		id := fmt.Sprintf("00000000-0000-7000-9000-%012d", n)
		_, err := db.operator.Exec(t.Context(), `INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,event_id,event_type,state,attempt,response_status,safe_error,next_attempt_at,created_at,updated_at) VALUES($1,$2,$3,$1,$4,$5,CASE WHEN $5='pending' THEN 0 ELSE 1 END,CASE WHEN $5='succeeded' THEN 204 ELSE NULL END,CASE WHEN $5 IN ('waiting','dead_letter') THEN 'fixture failure' ELSE '' END,CASE WHEN $5='waiting' THEN $6::timestamptz+interval '1 hour' ELSE NULL END,$7,$6)`, id, tenant, key, typ, state, stamp, at.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for i := range 7 {
		key := endpoint(i+1, housekeepingTenantA, "active")
		add(i+1, key, housekeepingTenantA, "waiting", false, at)
	}
	recovered := endpoint(8, housekeepingTenantA, "active")
	add(8, recovered, housekeepingTenantA, "dead_letter", false, at)
	add(9, recovered, housekeepingTenantA, "succeeded", false, at) // Descending ID wins equal times.
	disabled := endpoint(9, housekeepingTenantA, "disabled")
	add(10, disabled, housekeepingTenantA, "pending", false, at)
	// Retain a newer failed attempt too. Disabled endpoints count without appearing as current failures.
	add(15, disabled, housekeepingTenantA, "dead_letter", false, at.Add(time.Minute))
	other := endpoint(10, housekeepingTenantB, "active")
	add(11, other, housekeepingTenantB, "dead_letter", false, at)
	add(14, "00000000-0000-7000-8000-000000000002", housekeepingTenantA, "pending", false, at.Add(time.Minute))
	add(12, fmt.Sprintf("00000000-0000-7000-8000-%012d", 1), housekeepingTenantA, "succeeded", true, at.Add(time.Minute))
	states, err := store.ReadEndpointOverview(ctx, housekeepingTenantA)
	if err != nil || len(states) != 9 {
		t.Fatal(states, err)
	}
	rows, err := store.ReadDeliveryOverview(ctx, housekeepingTenantA)
	if err != nil || len(rows.States) != 12 || len(rows.FailedEndpoints) != 5 || rows.FailedEndpoints[0].EndpointID != "00000000-0000-7000-8000-000000000001" || rows.FailedEndpoints[0].DeliveryID != "00000000-0000-7000-9000-000000000001" || rows.FailedEndpoints[0].State != "waiting" {
		t.Fatal(rows, err)
	}
	for _, failure := range rows.FailedEndpoints {
		if failure.EndpointID == disabled {
			t.Fatal("disabled endpoint appeared as a current failure", failure)
		}
	}
	key := "00000000-0000-7000-8000-000000000001"
	later := add(13, key, housekeepingTenantA, "succeeded", false, at.Add(time.Minute))
	rows, err = store.ReadDeliveryOverview(ctx, housekeepingTenantA)
	if err != nil || rows.FailedEndpoints[0].EndpointID == key {
		t.Fatal(rows, err)
	}
	// Owned retention removes the newer row, exposing the retained earlier failure.
	// Retention requires an acknowledged, unleased parent and an elapsed prospective marker.
	if _, err = db.operator.Exec(t.Context(), `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at,published_at) VALUES($1::uuid,$2,'com.open.agentic.catalog.resource.created.v1',$1::uuid::text,jsonb_build_object('action','resource.created','targetType','resource','targetId',$1::uuid::text),$3,$3)`, later, housekeepingTenantA, at); err != nil {
		t.Fatal(err)
	}
	if _, err = db.operator.Exec(t.Context(), `UPDATE webhook_deliveries SET cleanup_after=clock_timestamp()+interval '1 millisecond' WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, later); err != nil {
		t.Fatal(err)
	}
	if _, err = db.operator.Exec(t.Context(), `SELECT pg_sleep(0.01)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.operator.Exec(t.Context(), `DELETE FROM webhook_deliveries WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, later); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ReadDeliveryOverview(ctx, housekeepingTenantA)
	if err != nil || rows.FailedEndpoints[0].EndpointID != key {
		t.Fatal(rows, err)
	}
	if _, err = db.operator.Exec(t.Context(), `UPDATE webhook_endpoints SET name='Edited receiver',revision=revision+1,updated_at=$3 WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, key, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ReadDeliveryOverview(ctx, housekeepingTenantA)
	if err != nil || rows.FailedEndpoints[0].EndpointID == key {
		t.Fatal(rows, err)
	}
	otherCtx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "other-webhooks", Roles: []identity.Role{identity.RoleTenantAdmin}})
	otherCtx, _, err = identity.WithCollectionContext(otherCtx)
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := store.ReadDeliveryOverview(otherCtx, housekeepingTenantB)
	if err != nil || len(isolated.States) != 1 || len(isolated.FailedEndpoints) != 1 || isolated.FailedEndpoints[0].EndpointID != other {
		t.Fatal(isolated, err)
	}
	if _, err = store.ReadDeliveryOverview(otherCtx, housekeepingTenantA); err == nil {
		t.Fatal("cross-tenant collection context accepted")
	}
	reader, _, err = identity.WithCollectionContext(reader)
	if err != nil {
		t.Fatal(err)
	}
	denied, err := store.ReadDeliveryOverview(reader, housekeepingTenantA)
	if err != nil || len(denied.States) != 0 || len(denied.FailedEndpoints) != 0 {
		t.Fatal(denied, err)
	}
	deniedEndpoints, err := store.ReadEndpointOverview(reader, housekeepingTenantA)
	if err != nil || len(deniedEndpoints) != 0 {
		t.Fatal(deniedEndpoints, err)
	}
	// SQL selection returns one sentinel, rather than hydrating all retained rows.
	_, err = db.operator.Exec(t.Context(), `INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,event_id,event_type,state,attempt,created_at,updated_at) SELECT uuidv7(),$1,$2,uuidv7(),'com.open.agentic.catalog.resource.created.v1','pending',0,$3,$3 FROM generate_series(1,10002)`, housekeepingTenantA, disabled, at)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = store.ReadDeliveryOverview(ctx, housekeepingTenantA)
	if err != nil || len(rows.States) != catalog.OverviewSelectionLimit+1 || len(rows.FailedEndpoints) != 5 {
		t.Fatal(len(rows.States), rows.FailedEndpoints, err)
	}
	_, err = db.operator.Exec(t.Context(), `INSERT INTO webhook_endpoints(id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at) SELECT uuidv7(),$1,'Scale receiver','https://receiver.example/'||n,ARRAY['*'],'disabled',1,1,'fixture','fixture',$2,$2 FROM generate_series(1,10002) n`, housekeepingTenantA, at)
	if err != nil {
		t.Fatal(err)
	}
	states, err = store.ReadEndpointOverview(ctx, housekeepingTenantA)
	if err != nil || len(states) != catalog.OverviewSelectionLimit+1 {
		t.Fatal(len(states), err)
	}
	logWebhookOverviewPlan(t, db, ctx)
}

func logWebhookOverviewPlan(t *testing.T, db *housekeepingDatabase, ctx context.Context) {
	t.Helper()
	_, err := withTenantCollectionRead(ctx, db.api, housekeepingTenantA, func(tx pgx.Tx) (struct{}, error) {
		rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE,BUFFERS) "+webhookOverviewFailuresSQL, housekeepingTenantA, catalog.CanonicalEventType("webhook.test"))
		if err != nil {
			return struct{}{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err = rows.Scan(&line); err != nil {
				return struct{}{}, err
			}
			t.Log(line)
		}
		return struct{}{}, rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}
