//go:build integration

package database

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type claimPersistenceClock func() time.Time

func (c claimPersistenceClock) Now() time.Time { return c() }

func TestWebhookClaimPersistenceFailuresPreserveEvidence(t *testing.T) {
	for _, scenario := range []string{"empty-owner", "nil-clock", "invalid-evidence", "invalid-transition", "conflicting-identity", "cancel-before-write", "cancel-after-write"} {
		t.Run(scenario, func(t *testing.T) {
			f := newWebhookClaimFixture(t)
			ctx := t.Context()
			sender := claimSender(func(context.Context, webhook.SendRequest) (webhook.SendResult, error) {
				return webhook.SendResult{Status: 503}, nil
			})
			if n, err := f.dispatcher(t, f.repo, sender).DispatchOnce(ctx); err != nil || n != 0 {
				t.Fatalf("seed waiting evidence: %d %v", n, err)
			}
			before, err := f.worker.GetDelivery(ctx, housekeepingTenantA, f.endpoint.ID, f.eventID)
			if err != nil {
				t.Fatal(err)
			}
			f.clock.Advance(2 * time.Minute)
			if items, err := f.repo.ClaimOutbox(ctx, "owner", f.clock.Now(), time.Minute, 1); err != nil || len(items) != 1 {
				t.Fatalf("claim waiting event: %d %v", len(items), err)
			}
			delivery := before
			delivery.Attempt++
			delivery.State = webhook.DeliverySucceeded
			delivery.ResponseStatus = 204
			delivery.SafeError = ""
			delivery.NextAttemptAt = nil
			delivery.UpdatedAt = f.clock.Now()
			switch scenario {
			case "invalid-transition":
				delivery.Attempt++
			case "conflicting-identity":
				delivery.ID, err = (id.Generator{}).New(f.clock.Now())
				if err != nil {
					t.Fatal(err)
				}
			}
			auditID, err := (id.Generator{}).New(f.clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			record, err := audit.NewRecord(auditID, delivery.TenantID, audit.Principal{ID: "catalog-worker", Type: audit.PrincipalServiceExecutor}, audit.ActionWebhookDeliveryAttempted, "webhook_delivery", delivery.ID, "", audit.Execution{}, map[string]any{"endpointId": delivery.EndpointID, "eventId": delivery.EventID, "attempt": delivery.Attempt, "state": string(delivery.State), "httpStatus": 204}, delivery.UpdatedAt)
			if err != nil {
				t.Fatal(err)
			}
			owner := "owner"
			var serviceClock catalog.Clock = f.clock
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			clockCalls := 0
			cancelAt := 0
			switch scenario {
			case "empty-owner":
				owner = ""
			case "nil-clock":
				serviceClock = nil
			case "invalid-evidence":
				record.TargetID = f.endpoint.ID
			case "cancel-before-write":
				cancelAt = 1
			case "cancel-after-write":
				cancelAt = 2
			}
			if cancelAt != 0 {
				serviceClock = claimPersistenceClock(func() time.Time {
					clockCalls++
					if clockCalls == cancelAt {
						cancel()
					}
					return f.clock.Now()
				})
			}
			_, err = f.worker.SaveClaimedDelivery(callCtx, delivery, owner, serviceClock, record)
			if err == nil {
				t.Fatal("invalid outcome committed")
			}
			if cancelAt != 0 && (!errors.Is(err, context.Canceled) || clockCalls != cancelAt) {
				t.Fatalf("cancellation at check %d: calls=%d err=%v", cancelAt, clockCalls, err)
			}
			if (scenario == "empty-owner" || scenario == "nil-clock") && catalog.ErrorCodeOf(err) != catalog.CodeConflict {
				t.Fatalf("invalid claim input: %v", err)
			}
			if (scenario == "invalid-evidence" || scenario == "conflicting-identity") && catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("unbound evidence: %v", err)
			}
			if scenario == "invalid-transition" {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "webhook delivery lifecycle transition is invalid" {
					t.Fatalf("attempt progression was not rejected by the lifecycle guard: %v", err)
				}
			}
			after, err := f.worker.GetDelivery(ctx, housekeepingTenantA, f.endpoint.ID, f.eventID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("failed save changed prior delivery: %v", err)
			}
			f.counts(t, 1, 1)
		})
	}
}

func TestWebhookEndpointRejectedWritesAreAtomic(t *testing.T) {
	for _, scenario := range []string{"duplicate-url", "create-evidence", "missing-endpoint", "update-evidence", "revision-jump", "timestamp-regression", "secret-version-jump", "secret-version-without-ciphertext", "ciphertext-without-version", "no-op"} {
		t.Run(scenario, func(t *testing.T) {
			f := newWebhookClaimFixture(t)
			if scenario == "secret-version-jump" {
				// Leave room under the audit version <= revision constraint so
				// only the SQL secret-version progression rejects this input.
				control := f.endpoint
				control.Name = "Updated receiver"
				control.Revision++
				var err error
				f.endpoint, err = f.api.UpdateEndpoint(t.Context(), control, f.endpoint.Revision)
				if err != nil {
					t.Fatalf("valid metadata control failed: %v", err)
				}
			}
			endpoint := f.endpoint
			action := audit.ActionWebhookUpdated
			create := scenario == "duplicate-url" || scenario == "create-evidence"
			if create || scenario == "missing-endpoint" {
				var err error
				endpoint.ID, err = (id.Generator{}).New(f.clock.Now())
				if err != nil {
					t.Fatal(err)
				}
			}
			if create {
				action = audit.ActionWebhookCreated
			} else {
				endpoint.Revision++
				endpoint.Name = "Attempted change"
			}
			if scenario == "revision-jump" {
				endpoint.Revision++
			}
			switch scenario {
			case "timestamp-regression":
				endpoint.UpdatedAt = endpoint.UpdatedAt.Add(-time.Microsecond)
			case "secret-version-jump", "secret-version-without-ciphertext":
				endpoint.SecretVersion++
				if scenario == "secret-version-jump" {
					endpoint.SecretVersion++
				}
			case "no-op":
				endpoint.Name = f.endpoint.Name
			}
			if scenario == "secret-version-jump" || scenario == "ciphertext-without-version" {
				var err error
				endpoint.SecretCiphertext, err = f.cipher.Encrypt([]byte("whsec_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
				if err != nil {
					t.Fatal(err)
				}
			}
			facts := map[string]any{"revision": endpoint.Revision, "secretVersion": endpoint.SecretVersion, "status": string(endpoint.Status)}
			if endpoint.SecretVersion != f.endpoint.SecretVersion {
				action = audit.ActionWebhookSecretRotated
				facts["revokePrevious"] = true
			}
			auditID, err := (id.Generator{}).New(f.clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			principal := audit.Principal{ID: "00000000-0000-7000-8000-000000000901", Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: "00000000-0000-7000-8000-000000000902"}
			record, err := audit.NewRecord(auditID, endpoint.TenantID, principal, action, "webhook_endpoint", endpoint.ID, "", audit.Execution{}, facts, endpoint.UpdatedAt)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "create-evidence" || scenario == "update-evidence" {
				record.TargetID = f.eventID
			}
			if create {
				_, err = f.api.CreateEndpoint(t.Context(), endpoint, record)
			} else {
				_, err = f.api.UpdateEndpoint(t.Context(), endpoint, f.endpoint.Revision, record)
			}
			if err == nil {
				t.Fatal("invalid endpoint mutation succeeded")
			}
			if scenario == "duplicate-url" && catalog.ErrorCodeOf(err) != catalog.CodeConflict {
				t.Fatalf("URL uniqueness error: %v", err)
			}
			if scenario == "missing-endpoint" && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("missing endpoint error: %v", err)
			}
			if (scenario == "create-evidence" || scenario == "update-evidence") && catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("evidence binding error: %v", err)
			}
			if scenario == "revision-jump" || scenario == "timestamp-regression" || scenario == "secret-version-jump" || scenario == "secret-version-without-ciphertext" || scenario == "ciphertext-without-version" || scenario == "no-op" {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "webhook endpoint lifecycle transition is invalid" {
					t.Fatalf("invalid transition was not rejected by the lifecycle guard: %v", err)
				}
			}
			retained, err := f.api.GetEndpoint(t.Context(), endpoint.TenantID, f.endpoint.ID)
			if err != nil || !reflect.DeepEqual(retained, f.endpoint) {
				t.Fatalf("rejected write changed retained endpoint: %v", err)
			}
			items, err := f.api.ListEndpointPage(t.Context(), endpoint.TenantID, webhook.EndpointListRequest{Limit: 10})
			if err != nil || len(items) != 1 {
				t.Fatalf("rejected write inserted an endpoint: %d %v", len(items), err)
			}
			f.counts(t, 0, 0)
		})
	}
}
