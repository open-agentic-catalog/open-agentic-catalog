//go:build integration

package database

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

func TestWebhookReencryptionMaintenanceResumesWithoutChangingHistory(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := context.Background()
	oldKey, newKey := bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{8}, 32)
	oldCipher, _ := webhook.NewAESGCM(oldKey)
	newCipher, _ := webhook.NewAESGCM(newKey)
	raw := []byte("whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	encrypted, _ := oldCipher.Encrypt(raw)
	now := time.Now().UTC().Truncate(time.Microsecond)
	ids := id.Generator{}
	for i := 0; i < 3; i++ {
		endpointID, _ := ids.New(now)
		if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_endpoints(id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at) VALUES ($1,$2,'Receiver',$3,ARRAY['*'],'active',1,1,$4,'operator',$5,$5)`, endpointID, housekeepingTenantA, fmt.Sprintf("https://receiver.example/%d", i), encrypted, now); err != nil {
			t.Fatal(err)
		}
	}
	apiRepo, _ := NewPostgresRepository(db.api)
	apiStore, _ := NewPostgresWebhookStore(apiRepo)
	endpoints, err := apiStore.ListEndpointPage(ctx, housekeepingTenantA, webhook.EndpointListRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	first := endpoints[0]
	if _, err := withTenantTx(ctx, db.api, housekeepingTenantA, pgx.TxOptions{}, func(tx pgx.Tx) (bool, error) {
		_, err := tx.Exec(ctx, `UPDATE webhook_endpoints SET encrypted_secret=$3 WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, first.ID, append(bytes.Clone(encrypted), 1))
		return true, err
	}); err == nil {
		t.Fatal("runtime used owner-only rewrap exception")
	}
	if _, err := ReencryptWebhookSecrets(ctx, db.api, housekeepingTenantA, "", "Replace deployment key", 2, oldKey, newKey); err == nil {
		t.Fatal("API obtained maintenance authority")
	}
	if _, err := ReencryptWebhookSecrets(ctx, db.worker, housekeepingTenantA, "", "Replace deployment key", 2, oldKey, newKey); err == nil {
		t.Fatal("worker obtained maintenance authority")
	}
	func() {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		const missingTenant = "00000000-0000-7000-8000-000000000809"
		type maintenanceState struct {
			endpoints             string
			auditRows, outboxRows int
		}
		readState := func() maintenanceState {
			t.Helper()
			var state maintenanceState
			if err := db.operator.QueryRow(checkCtx, `
SELECT (SELECT jsonb_agg(to_jsonb(endpoint) ORDER BY id)::text FROM webhook_endpoints endpoint WHERE tenant_id=ANY($1::uuid[])),
       (SELECT count(*) FROM audit_events WHERE tenant_id=ANY($1::uuid[])),
       (SELECT count(*) FROM outbox_events WHERE tenant_id=ANY($1::uuid[]))`, []string{housekeepingTenantA, missingTenant}).
				Scan(&state.endpoints, &state.auditRows, &state.outboxRows); err != nil {
				t.Fatal(err)
			}
			return state
		}
		before := readState()
		missing, err := ReencryptWebhookSecrets(checkCtx, db.operator, missingTenant, "", "Verify missing tenant", 2, oldKey, newKey)
		if err == nil || err.Error() != "webhook re-encryption tenant was not found" || missing != (WebhookReencryptResult{TenantID: missingTenant}) {
			t.Fatalf("missing tenant replacement: %+v %v", missing, err)
		}
		if readState() != before {
			t.Fatal("missing tenant replacement changed endpoint history or evidence")
		}

		// A pinned transaction owns the same table lock taken by an endpoint
		// writer. The maintenance operation must use another physical session.
		holder, err := db.operator.BeginTx(checkCtx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackForCleanup(checkCtx, holder)()
		if _, err := holder.Exec(checkCtx, `LOCK TABLE webhook_endpoints IN ROW EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		blocked, err := ReencryptWebhookSecrets(checkCtx, db.operator, housekeepingTenantA, "", "Replace deployment key", 2, oldKey, newKey)
		if err == nil || err.Error() != "webhook re-encryption requires quiescent endpoint writers" || blocked != (WebhookReencryptResult{TenantID: housekeepingTenantA}) || checkCtx.Err() != nil {
			t.Fatalf("competing writer replacement: %+v %v (context: %v)", blocked, err, checkCtx.Err())
		}
		if holder.Conn().PgConn().TxStatus() != 'T' || readState() != before {
			t.Fatal("contended replacement lost its blocker or changed endpoint ciphertext, history or evidence")
		}
		if err := holder.Rollback(checkCtx); err != nil {
			t.Fatal(err)
		}
	}()
	result, err := ReencryptWebhookSecrets(ctx, db.operator, housekeepingTenantA, "", "Replace deployment key", 2, oldKey, newKey)
	if err != nil || result.Changed != 2 || result.Verified != 2 || !result.HasMore || result.AuditEventID == "" {
		t.Fatalf("first batch: %+v %v", result, err)
	}
	repeated, err := ReencryptWebhookSecrets(ctx, db.operator, housekeepingTenantA, "", "Resume interrupted key replacement", 2, oldKey, newKey)
	if err != nil || repeated.Changed != 0 || repeated.Verified != 2 || repeated.AfterID != result.AfterID {
		t.Fatalf("repeat: %+v %v", repeated, err)
	}
	last, err := ReencryptWebhookSecrets(ctx, db.operator, housekeepingTenantA, result.AfterID, "Finish key replacement", 2, oldKey, newKey)
	if err != nil || last.Changed != 1 || last.Verified != 1 || last.HasMore {
		t.Fatalf("last batch: %+v %v", last, err)
	}
	for _, before := range endpoints {
		after, err := apiStore.GetEndpoint(ctx, housekeepingTenantA, before.ID)
		if err != nil || after.Revision != before.Revision || after.SecretVersion != before.SecretVersion || !after.UpdatedAt.Equal(before.UpdatedAt) || after.Status != before.Status {
			t.Fatal("rewrap changed endpoint history")
		}
		plain, err := newCipher.Decrypt(after.SecretCiphertext)
		if err != nil || !bytes.Equal(plain, raw) {
			t.Fatal("replacement did not retain exact signer")
		}
	}
	var outboxCount int
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&outboxCount); err != nil || outboxCount != 0 {
		t.Fatal("maintenance manufactured a public notification")
	}
	if empty, err := ReencryptWebhookSecrets(ctx, db.operator, housekeepingTenantB, "", "Verify empty tenant", 2, oldKey, newKey); err != nil || empty.Verified != 0 || empty.AuditEventID != "" {
		t.Fatalf("empty tenant: %+v %v", empty, err)
	}
}

func TestWebhookReencryptionRollsBackOnAuditFailure(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := context.Background()
	oldKey, newKey := bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{8}, 32)
	cipher, _ := webhook.NewAESGCM(oldKey)
	encrypted, _ := cipher.Encrypt([]byte("whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	endpointID := "00000000-0000-7000-8000-000000000900"
	if _, err := db.operator.Exec(ctx, `INSERT INTO webhook_endpoints(id,tenant_id,name,url,event_types,status,revision,secret_version,encrypted_secret,created_by,created_at,updated_at) VALUES ($1,$2,'Receiver','https://receiver.example/events',ARRAY['*'],'disabled',1,1,$3,'operator',clock_timestamp(),clock_timestamp())`, endpointID, housekeepingTenantA, encrypted); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(ctx, `CREATE FUNCTION reject_webhook_rewrap_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_webhook_rewrap_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_webhook_rewrap_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := ReencryptWebhookSecrets(ctx, db.operator, housekeepingTenantA, "", "Replace key", 100, oldKey, newKey); err == nil {
		t.Fatal("audit failure did not abort replacement")
	}
	var retained []byte
	if err := db.operator.QueryRow(ctx, `SELECT encrypted_secret FROM webhook_endpoints WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, endpointID).Scan(&retained); err != nil || !bytes.Equal(retained, encrypted) {
		t.Fatal("failed batch changed ciphertext")
	}
	if _, err := db.operator.Exec(ctx, `DROP TRIGGER reject_webhook_rewrap_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	if result, err := ReencryptWebhookSecrets(ctx, db.operator, housekeepingTenantA, "", "Replace disabled endpoint key", 100, oldKey, newKey); err != nil || result.Changed != 1 {
		t.Fatalf("disabled endpoint replacement: %+v %v", result, err)
	}
}
