//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/examples/webhookreceiver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/webhooksender"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type claimSender func(context.Context, webhook.SendRequest) (webhook.SendResult, error)

func (f claimSender) Send(ctx context.Context, request webhook.SendRequest) (webhook.SendResult, error) {
	return f(ctx, request)
}

type webhookClaimFixture struct {
	db          *housekeepingDatabase
	repo        *PostgresRepository
	api, worker *PostgresWebhookStore
	clock       *clock.Fake
	cipher      webhook.Cipher
	endpoint    webhook.Endpoint
	eventID     string
}

func newWebhookClaimFixture(t *testing.T) webhookClaimFixture {
	t.Helper()
	db := newHousekeepingDatabase(t)
	apiRepo, _ := NewPostgresRepository(db.api)
	repo, _ := NewPostgresRepository(db.worker)
	apiStore, _ := NewPostgresWebhookStore(apiRepo)
	workerStore, _ := NewPostgresWebhookStore(repo)
	now := time.Now().UTC().Truncate(time.Microsecond)
	ids := id.Generator{}
	cipher, _ := webhook.NewAESGCM(make([]byte, 32))
	secret, err := cipher.Encrypt([]byte("whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	if err != nil {
		t.Fatal(err)
	}
	endpointID, _ := ids.New(now)
	endpoint := webhook.Endpoint{ID: endpointID, TenantID: housekeepingTenantA, Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}, Status: webhook.StatusActive, Revision: 1, SecretVersion: 1, SecretCiphertext: secret, CreatedBy: "integration", CreatedAt: now, UpdatedAt: now}
	if _, err := apiStore.CreateEndpoint(context.Background(), endpoint); err != nil {
		t.Fatal(err)
	}
	eventID, _ := ids.New(now)
	data, err := events.MarshalReference("resource.created", "resource", endpointID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(context.Background(), `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,is_internal,created_at) VALUES ($1,$2,$3,$4,$5,false,$6)`, eventID, housekeepingTenantA, events.CanonicalType("resource.created"), endpointID, data, now); err != nil {
		t.Fatal(err)
	}
	return webhookClaimFixture{db, repo, apiStore, workerStore, clock.NewFake(now), cipher, endpoint, eventID}
}

func (f webhookClaimFixture) dispatcher(t *testing.T, outbox webhook.Outbox, sender webhook.Sender) *webhook.Dispatcher {
	t.Helper()
	d, err := webhook.NewDispatcher(outbox, f.worker, f.cipher, sender, f.clock, id.Generator{}, "original-owner", time.Minute, 3, time.Millisecond, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f webhookClaimFixture) counts(t *testing.T, deliveries, audits int) {
	t.Helper()
	var actualDeliveries, actualAudits int
	if err := f.db.operator.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM webhook_deliveries WHERE tenant_id=$1),(SELECT count(*) FROM audit_events WHERE tenant_id=$1)`, housekeepingTenantA).Scan(&actualDeliveries, &actualAudits); err != nil || actualDeliveries != deliveries || actualAudits != audits {
		t.Fatalf("delivery/audit counts %d/%d, want %d/%d: %v", actualDeliveries, actualAudits, deliveries, audits, err)
	}
}

func TestWebhookOutcomeRequiresLiveOwnedClaim(t *testing.T) {
	for _, scenario := range []string{"live", "expired", "reassigned", "acknowledged", "audit-failure"} {
		t.Run(scenario, func(t *testing.T) {
			f := newWebhookClaimFixture(t)
			sender := claimSender(func(ctx context.Context, _ webhook.SendRequest) (webhook.SendResult, error) {
				switch scenario {
				case "expired", "reassigned":
					f.clock.Advance(2 * time.Minute)
					if scenario == "reassigned" {
						items, err := f.repo.ClaimOutbox(ctx, "replacement-owner", f.clock.Now(), time.Minute, 1)
						if err != nil || len(items) != 1 || items[0].ID != f.eventID {
							t.Fatalf("replacement claim: %#v %v", items, err)
						}
					}
				case "acknowledged":
					if err := f.repo.MarkOutboxPublished(ctx, housekeepingTenantA, f.eventID, "original-owner", f.clock.Now()); err != nil {
						t.Fatal(err)
					}
				case "audit-failure":
					if _, err := f.db.operator.Exec(ctx, `CREATE FUNCTION reject_claim_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_claim_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_claim_audit()`); err != nil {
						t.Fatal(err)
					}
				}
				return webhook.SendResult{Status: 204}, nil
			})
			n, err := f.dispatcher(t, f.repo, sender).DispatchOnce(context.Background())
			if scenario == "live" {
				if err != nil || n != 1 {
					t.Fatalf("live outcome: %d %v", n, err)
				}
				f.counts(t, 1, 1)
				return
			}
			if err == nil || n != 0 {
				t.Fatalf("invalid outcome accepted: %d %v", n, err)
			}
			if scenario != "audit-failure" && catalog.ErrorCodeOf(err) != catalog.CodeConflict {
				t.Fatalf("wrong claim error: %v", err)
			}
			f.counts(t, 0, 0)
		})
	}
}

type failAcknowledgement struct {
	*PostgresRepository
}

func (f failAcknowledgement) MarkOutboxPublished(context.Context, string, string, string, time.Time) error {
	return errors.New("injected acknowledgement loss")
}

func TestWebhookCommittedOutcomeSurvivesAcknowledgementLoss(t *testing.T) {
	f := newWebhookClaimFixture(t)
	sends := 0
	sender := claimSender(func(context.Context, webhook.SendRequest) (webhook.SendResult, error) {
		sends++
		return webhook.SendResult{Status: 204}, nil
	})
	if n, err := f.dispatcher(t, failAcknowledgement{f.repo}, sender).DispatchOnce(context.Background()); err == nil || n != 0 {
		t.Fatalf("acknowledgement failure: %d %v", n, err)
	}
	f.counts(t, 1, 1)
	f.clock.Advance(2 * time.Minute)
	if n, err := f.dispatcher(t, f.repo, sender).DispatchOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("recovery: %d %v", n, err)
	}
	if sends != 1 {
		t.Fatalf("terminal outcome resent %d times", sends)
	}
	f.counts(t, 1, 1)
}

func TestWebhookRefusedOutcomePreservesPreviousAttempt(t *testing.T) {
	for _, scenario := range []string{"expired", "reassigned", "audit-failure"} {
		t.Run(scenario, func(t *testing.T) {
			f := newWebhookClaimFixture(t)
			ctx := context.Background()
			first := claimSender(func(context.Context, webhook.SendRequest) (webhook.SendResult, error) {
				return webhook.SendResult{Status: 503}, nil
			})
			if n, err := f.dispatcher(t, f.repo, first).DispatchOnce(ctx); err != nil || n != 0 {
				t.Fatalf("initial retry: %d %v", n, err)
			}
			before, err := f.worker.GetDelivery(ctx, housekeepingTenantA, f.endpoint.ID, f.eventID)
			if err != nil || before.State != webhook.DeliveryWaiting || before.Attempt != 1 {
				t.Fatalf("initial outcome: %#v %v", before, err)
			}
			f.clock.Advance(2 * time.Minute)
			second := claimSender(func(ctx context.Context, _ webhook.SendRequest) (webhook.SendResult, error) {
				if scenario == "audit-failure" {
					if _, err := f.db.operator.Exec(ctx, `CREATE FUNCTION reject_retry_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_retry_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_retry_audit()`); err != nil {
						t.Fatal(err)
					}
				} else {
					f.clock.Advance(2 * time.Minute)
					if scenario == "reassigned" {
						if items, err := f.repo.ClaimOutbox(ctx, "replacement", f.clock.Now(), time.Minute, 1); err != nil || len(items) != 1 {
							t.Fatalf("replacement claim: %#v %v", items, err)
						}
					}
				}
				return webhook.SendResult{Status: 204}, nil
			})
			if n, err := f.dispatcher(t, f.repo, second).DispatchOnce(ctx); err == nil || n != 0 {
				t.Fatalf("invalid retry outcome accepted: %d %v", n, err)
			}
			after, err := f.worker.GetDelivery(ctx, housekeepingTenantA, f.endpoint.ID, f.eventID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused outcome changed prior evidence: before=%#v after=%#v err=%v", before, after, err)
			}
			f.counts(t, 1, 1)
		})
	}
}

func TestWebhookReplacementWorkerCommitsWhileOriginalSendIsPaused(t *testing.T) {
	f := newWebhookClaimFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	original := f.dispatcher(t, f.repo, claimSender(func(ctx context.Context, _ webhook.SendRequest) (webhook.SendResult, error) {
		close(started)
		select {
		case <-release:
			return webhook.SendResult{Status: 503}, nil
		case <-ctx.Done():
			return webhook.SendResult{}, ctx.Err()
		}
	}))
	done := make(chan error, 1)
	go func() {
		_, err := original.DispatchOnce(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	f.clock.Advance(2 * time.Minute)
	replacement, err := webhook.NewDispatcher(f.repo, f.worker, f.cipher, claimSender(func(context.Context, webhook.SendRequest) (webhook.SendResult, error) {
		return webhook.SendResult{Status: 204}, nil
	}), f.clock, id.Generator{}, "replacement", time.Minute, 3, time.Millisecond, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := replacement.DispatchOnce(ctx); err != nil || n != 1 {
		t.Fatalf("replacement blocked by in-flight HTTPS: %d %v", n, err)
	}
	close(release)
	if err := <-done; catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("original overwrote replacement outcome: %v", err)
	}
	retained, err := f.worker.GetDelivery(ctx, housekeepingTenantA, f.endpoint.ID, f.eventID)
	if err != nil || retained.State != webhook.DeliverySucceeded || retained.ResponseStatus != 204 || retained.Attempt != 1 {
		t.Fatalf("retained replacement: %#v %v", retained, err)
	}
	f.counts(t, 1, 1)
}

func TestWebhookClaimWriteDeniesOtherTenantAndAPIRole(t *testing.T) {
	f := newWebhookClaimFixture(t)
	ctx := context.Background()
	if _, err := f.repo.ClaimOutbox(ctx, "owner", f.clock.Now(), time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	delivery := webhook.Delivery{TenantID: housekeepingTenantB, EndpointID: f.endpoint.ID, EventID: f.eventID}
	if _, err := f.worker.SaveClaimedDelivery(ctx, delivery, "owner", f.clock, audit.Record{}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant claim: %v", err)
	}
	delivery.TenantID = housekeepingTenantA
	if _, err := f.api.SaveClaimedDelivery(ctx, delivery, "owner", f.clock, audit.Record{}); err == nil {
		t.Fatal("API acquired worker claim authority")
	} else {
		// An audit-validation error must not masquerade as a database-role denial.
		requireInsufficientPrivilege(t, err)
	}
	f.counts(t, 0, 0)
}

func TestWebhookClaimExpiryAfterDatabaseLockWait(t *testing.T) {
	for _, boundary := range []string{"outbox-row", "audit-insert"} {
		t.Run(boundary, func(t *testing.T) {
			f := newWebhookClaimFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			items, err := f.repo.ClaimOutbox(ctx, "owner", f.clock.Now(), 2*time.Second, 1)
			if err != nil || len(items) != 1 {
				t.Fatalf("claim: %#v %v", items, err)
			}
			blocker, err := f.db.operator.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			var blockerPID int
			if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			if boundary == "outbox-row" {
				_, err = blocker.Exec(ctx, `SELECT id FROM outbox_events WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, housekeepingTenantA, f.eventID)
			} else {
				// Delivery INSERT succeeds, but the atomic audit INSERT must wait.
				_, err = blocker.Exec(ctx, `LOCK TABLE audit_events IN SHARE MODE`)
			}
			if err != nil {
				t.Fatal(err)
			}
			deliveryID, _ := (id.Generator{}).New(f.clock.Now())
			delivery := webhook.Delivery{ID: deliveryID, TenantID: housekeepingTenantA, EndpointID: f.endpoint.ID, EventID: f.eventID, EventType: items[0].Type, State: webhook.DeliverySucceeded, Attempt: 1, ResponseStatus: 204, CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now()}
			auditID, _ := (id.Generator{}).New(f.clock.Now())
			record, err := audit.NewRecord(auditID, delivery.TenantID, audit.Principal{ID: "catalog-worker", Type: audit.PrincipalServiceExecutor}, audit.ActionWebhookDeliveryAttempted, "webhook_delivery", delivery.ID, "", audit.Execution{}, map[string]any{"endpointId": delivery.EndpointID, "eventId": delivery.EventID, "attempt": 1, "state": string(delivery.State), "httpStatus": 204}, delivery.UpdatedAt)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := f.worker.SaveClaimedDelivery(ctx, delivery, "owner", f.clock, record)
				done <- err
			}()
			// Observe a real wait, not merely a timer or an advanced service clock.
			for {
				var waiting bool
				if err := f.db.operator.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("write returned before reaching lock boundary: %v", err)
				case <-time.After(5 * time.Millisecond):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if _, err := f.db.operator.Exec(ctx, `SELECT pg_sleep(greatest(0,extract(epoch FROM $1::timestamptz-clock_timestamp()))+0.02)`, items[0].LeaseExpiresAt); err != nil {
				t.Fatal(err)
			}
			if err := blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; catalog.ErrorCodeOf(err) != catalog.CodeConflict {
				t.Fatalf("expired lock-wait outcome: %v", err)
			}
			f.counts(t, 0, 0)
		})
	}
}

func TestWebhookHTTPSPostgresRetryAndAcknowledgementRecovery(t *testing.T) {
	f := newWebhookClaimFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type receipt struct {
		valid               bool
		eventID, deliveryID string
		digest              [32]byte
	}
	receipts := make(chan receipt, 3)
	readReceipt := func() receipt {
		t.Helper()
		select {
		case received := <-receipts:
			return received
		case <-ctx.Done():
			t.Fatal("HTTPS receiver did not observe a request before the test deadline")
			return receipt{}
		}
	}
	var requests atomic.Int32
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
		valid := err == nil && len(body) <= 8192 && webhookreceiver.Verify("whsec_"+strings.Repeat("a", 43), 1, r.Header, body, f.clock.Now()) == nil
		receipts <- receipt{valid, r.Header.Get("Ce-Id"), r.Header.Get("X-Catalog-Delivery-Id"), sha256.Sum256(body)}
		attempt := requests.Add(1)
		switch {
		case !valid:
			w.WriteHeader(http.StatusUnauthorized)
		case attempt == 1:
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer receiver.Close()
	f.endpoint.URL = receiver.URL + "/events"
	f.endpoint.Revision++
	if _, err := f.api.UpdateEndpoint(ctx, f.endpoint, 1); err != nil {
		t.Fatal(err)
	}
	sender, err := webhooksender.NewWithPolicy(fetcher.RoutingPolicy{ID: "claim-review", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64), Public: fetcher.PublicRoute{Mode: fetcher.PublicDisabled}, Timeout: time.Second, MaxResponseBytes: 1024, PrivateRoutes: []fetcher.PrivateRoute{{Name: "receiver", Origins: []string{receiver.URL}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, AssignedTenantIDs: []string{housekeepingTenantA}, TrustBundlePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: receiver.Certificate().Raw}), AllowTestAddresses: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := f.dispatcher(t, f.repo, sender).DispatchOnce(ctx); n != 0 || err != nil {
		t.Fatalf("HTTPS retry: %d %v", n, err)
	}
	first := readReceipt()
	if !first.valid || first.eventID != f.eventID || first.deliveryID == "" {
		t.Fatal("receiver could not verify signed event identity")
	}
	f.counts(t, 1, 1)
	if n, err := f.dispatcher(t, f.repo, sender).DispatchOnce(ctx); n != 0 || err != nil || requests.Load() != 1 {
		t.Fatalf("retry ran before its due time: %d %v", n, err)
	}
	f.clock.Advance(2 * time.Minute)
	if n, err := f.dispatcher(t, failAcknowledgement{f.repo}, sender).DispatchOnce(ctx); n != 0 || err == nil {
		t.Fatalf("lost acknowledgement: %d %v", n, err)
	}
	second := readReceipt()
	if !second.valid || first.eventID != second.eventID || first.deliveryID != second.deliveryID || first.digest != second.digest {
		t.Fatal("retried delivery changed its immutable event identity or body")
	}
	f.counts(t, 1, 2)
	f.clock.Advance(2 * time.Minute)
	if n, err := f.dispatcher(t, f.repo, sender).DispatchOnce(ctx); n != 1 || err != nil || requests.Load() != 2 {
		t.Fatalf("recovery resent a committed terminal outcome: %d %v", n, err)
	}
	retained, err := f.worker.GetDelivery(ctx, housekeepingTenantA, f.endpoint.ID, f.eventID)
	if err != nil || retained.State != webhook.DeliverySucceeded || retained.Attempt != 2 || retained.ResponseStatus != http.StatusNoContent {
		t.Fatalf("retained HTTPS outcome: %#v %v", retained, err)
	}
	f.counts(t, 1, 2)
}
