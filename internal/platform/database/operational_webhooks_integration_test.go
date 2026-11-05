//go:build integration

// Webhook operational metrics qualify dispatch eligibility against real SQL.
package database

import (
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// TestPostgresWebhookOperationalDispatchBoundaries distinguishes due events
// from waiting recipients and keeps matching endpoints inside the event tenant.
func TestPostgresWebhookOperationalDispatchBoundaries(t *testing.T) {
	for _, test := range []struct {
		name                      string
		dispatch, retries, failed int64
	}{
		{name: "due event", dispatch: 1},
		{name: "two recipients", dispatch: 1},
		{name: "live claim"},
		{name: "claim expires now", dispatch: 1},
		{name: "future dispatch"},
		{name: "dispatch due now", dispatch: 1},
		{name: "endpoint created later"},
		{name: "disabled endpoint"},
		{name: "nonmatching filter"},
		{name: "other tenant endpoint"},
		{name: "future retry", retries: 1, failed: 1},
		{name: "retry due now", dispatch: 1, retries: 1, failed: 1},
		{name: "acknowledged event"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newHousekeepingDatabase(t)
			ctx := t.Context()
			repo, err := NewPostgresRepository(db.worker)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC)
			created := at.Add(-2 * time.Minute)
			const endpoint = "00000000-0000-7000-8000-000000000601"
			const event = "00000000-0000-7000-8000-000000000602"
			tenant, status, filter := housekeepingTenantA, "active", "*"
			endpointAt := created.Add(-time.Minute)
			switch test.name {
			case "endpoint created later":
				endpointAt = created.Add(time.Second)
			case "disabled endpoint":
				status = "disabled"
			case "nonmatching filter":
				filter = "com.open.agentic.catalog.resource.updated.v1"
			case "other tenant endpoint":
				tenant = housekeepingTenantB
			}
			if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_endpoints
 (id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at)
 VALUES($1,$2,'diagnostic receiver','https://receiver.example/events',ARRAY[$3]::text[],$4,1,1,'fixture','fixture',$5,$5)`, endpoint, tenant, filter, status, endpointAt); err != nil {
				t.Fatal(err)
			}
			if test.name == "two recipients" {
				if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_endpoints
 (id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at)
 VALUES(uuidv7(),$1,'second receiver','https://other.example/events',ARRAY['*'],'active',1,1,'fixture','fixture',$2,$2)`, tenant, endpointAt); err != nil {
					t.Fatal(err)
				}
			}
			var published *time.Time
			if test.name == "acknowledged event" {
				published = &at
			}
			if _, err := db.operator.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at,published_at)
 VALUES($1::uuid,$2,'com.open.agentic.catalog.resource.created.v1',($1::uuid)::text,
 jsonb_build_object('action','resource.created','targetType','resource','targetId',($1::uuid)::text),$3,$4)`, event, housekeepingTenantA, created, published); err != nil {
				t.Fatal(err)
			}
			switch test.name {
			case "live claim", "claim expires now", "future dispatch", "dispatch due now":
				expires := at.Add(time.Minute)
				if test.name == "claim expires now" {
					expires = at
				}
				if _, err := db.operator.Exec(ctx, `UPDATE outbox_events SET lease_owner='diagnostic-worker',lease_expires_at=$3 WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, event, expires); err != nil {
					t.Fatal(err)
				}
				if test.name == "future dispatch" || test.name == "dispatch due now" {
					due := at
					if test.name == "future dispatch" {
						due = at.Add(time.Second)
					}
					if _, err := db.operator.Exec(ctx, `UPDATE outbox_events SET lease_owner=NULL,lease_expires_at=NULL,dispatch_after=$3 WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, event, due); err != nil {
						t.Fatal(err)
					}
				}
			case "future retry", "retry due now":
				due := at
				if test.name == "future retry" {
					due = at.Add(time.Second)
				}
				if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_deliveries
 (id,tenant_id,endpoint_id,event_id,event_type,state,attempt,response_status,safe_error,next_attempt_at,created_at,updated_at)
 VALUES(uuidv7(),$1,$2,$3,'com.open.agentic.catalog.resource.created.v1','waiting',1,429,'receiver_unavailable',$4,$5,$5)`, housekeepingTenantA, endpoint, event, due, at.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			want := observability.WebhookSnapshot{DispatchableEvents: test.dispatch, WaitingRetries: test.retries, LastAttemptFailedReceivers: test.failed}
			if test.dispatch != 0 {
				want.OldestDispatchableAgeSec = 120
			}
			if got, err := webhookOperationalSnapshot(ctx, repo.pool, at); err != nil || got != want {
				t.Fatalf("webhook signals = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}
