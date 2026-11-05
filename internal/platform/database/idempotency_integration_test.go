//go:build integration

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestAtomicCommandCrashRecoveryAndRepair(t *testing.T) {
	baseDSN := os.Getenv("CATALOG_TEST_DATABASE_DSN")
	if baseDSN == "" {
		t.Skip("CATALOG_TEST_DATABASE_DSN is not set")
	}
	ctx := context.Background()
	adminPool, err := Open(ctx, baseDSN, testPoolConfig(8))
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	schema := "catalog_atomic_" + suffix
	apiRole := "catalog_atomic_api_" + suffix
	workerRole := "catalog_atomic_worker_" + suffix
	apiPassword := "catalog-atomic-api-" + suffix
	workerPassword := "catalog-atomic-worker-" + suffix
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = adminPool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		_, _ = adminPool.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{apiRole}.Sanitize())
		_, _ = adminPool.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{workerRole}.Sanitize())
	}()
	testDSN := withSearchPath(t, baseDSN, schema+",public")
	ownerPool, err := Open(ctx, testDSN, testPoolConfig(8))
	if err != nil {
		t.Fatal(err)
	}
	defer ownerPool.Close()
	if _, err := applyMigrationsForTest(ctx, ownerPool); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE ROLE ` + pgx.Identifier{apiRole}.Sanitize() + ` LOGIN PASSWORD '` + apiPassword + `' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS`,
		`CREATE ROLE ` + pgx.Identifier{workerRole}.Sanitize() + ` LOGIN PASSWORD '` + workerPassword + `' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT BYPASSRLS`,
	} {
		if _, err := adminPool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reconcileRuntimePrivilegesForTest(ctx, ownerPool, apiRole, workerRole); err != nil {
		t.Fatal(err)
	}
	apiPool := rolePool(t, testDSN, apiRole, apiPassword)
	defer apiPool.Close()
	workerPool := rolePool(t, testDSN, workerRole, workerPassword)
	defer workerPool.Close()
	repository, err := NewPostgresRepository(apiPool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdempotencyStore(repository)
	if err != nil {
		t.Fatal(err)
	}

	const (
		tenantID    = "00000000-0000-7000-8000-000000000101"
		otherTenant = "00000000-0000-7000-8000-000000000102"
		namespaceID = "00000000-0000-7000-8000-000000000103"
	)
	if _, err := withTenantTx(ctx, ownerPool, tenantID, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
		if _, insertErr := tx.Exec(ctx, `INSERT INTO tenants(id,display_name) VALUES ($1,'Atomic integration tenant')`, tenantID); insertErr != nil {
			return struct{}{}, insertErr
		}
		_, insertErr := tx.Exec(ctx, `INSERT INTO tenant_identity_realms(id,tenant_id,issuer,tenancy_mode,state,revision,created_at,updated_at) VALUES ('00000000-0000-7000-8000-000000000106',$1,'https://identity.atomic.example','dedicated','active',1,clock_timestamp(),clock_timestamp())`, tenantID)
		return struct{}{}, insertErr
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	namespace := catalog.Namespace{ID: namespaceID, TenantID: tenantID, Slug: "atomic", DisplayName: "Atomic", Revision: 1, CreatedAt: now, UpdatedAt: now}
	namespaceAudit := integrationAudit(t, "00000000-0000-7000-8000-000000000104", tenantID, "setup", "namespace.created", "namespace", namespace.ID, "", now)
	namespaceOutbox := integrationOutbox(t, "00000000-0000-7000-8000-000000000105", namespaceAudit)
	if _, err := repository.CreateNamespace(ctx, namespace, namespaceAudit, namespaceOutbox); err != nil {
		t.Fatal(err)
	}

	newResource := func(idValue, auditID, outboxID, identifier string) catalog.CreateResourceParams {
		resource := catalog.Resource{ID: idValue, TenantID: tenantID, NamespaceID: namespaceID, CanonicalIdentifier: identifier, Kind: catalog.KindAgent, CreatedBy: "contributor", Revision: 1, CreatedAt: now, UpdatedAt: now}
		audit := integrationAudit(t, auditID, tenantID, "contributor", "resource.created", "resource", idValue, "", now)
		outbox := integrationOutbox(t, outboxID, audit)
		return catalog.CreateResourceParams{Resource: resource, Audit: audit, Outbox: outbox}
	}
	expiry := time.Now().UTC().Add(time.Hour)
	authorizationDigest := digestText("atomic integration authorization")

	t.Run("concurrent replicas serialize one command execution", func(t *testing.T) {
		secondStore, err := NewPostgresIdempotencyStore(repository)
		if err != nil {
			t.Fatal(err)
		}
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/resources", Key: "atomic-concurrent", AuthorizationDigest: authorizationDigest}
		started := make(chan struct{})
		release := make(chan struct{})
		released := false
		releaseFirst := func() {
			if !released {
				close(release)
				released = true
			}
		}
		defer releaseFirst()
		type executionResult struct {
			execution idempotency.Execution
			err       error
		}
		firstResult := make(chan executionResult, 1)
		var calls atomic.Int32
		go func() {
			execution, executeErr := store.Execute(ctx, scope, "digest-concurrent", expiry, func(context.Context) idempotency.Response {
				calls.Add(1)
				close(started)
				<-release
				return idempotency.Response{Digest: "digest-concurrent", Status: 202, Body: []byte(`{"state":"accepted"}`)}
			})
			firstResult <- executionResult{execution: execution, err: executeErr}
		}()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("first replica did not obtain and execute its reservation")
		}

		pending, err := secondStore.Execute(ctx, scope, "digest-concurrent", expiry, func(context.Context) idempotency.Response {
			calls.Add(1)
			return idempotency.Response{Digest: "digest-concurrent", Status: 500}
		})
		if err != nil || !pending.Pending || pending.Replayed || calls.Load() != 1 {
			t.Fatalf("concurrent replica result = %#v, calls %d, %v", pending, calls.Load(), err)
		}
		releaseFirst()
		var first executionResult
		select {
		case first = <-firstResult:
		case <-time.After(5 * time.Second):
			t.Fatal("first replica did not complete after release")
		}
		if first.err != nil || first.execution.Pending || first.execution.Replayed || first.execution.Response.Status != 202 {
			t.Fatalf("first replica result = %#v, %v", first.execution, first.err)
		}
		replay, err := secondStore.Execute(ctx, scope, "digest-concurrent", expiry, func(context.Context) idempotency.Response {
			calls.Add(1)
			return idempotency.Response{Digest: "digest-concurrent", Status: 500}
		})
		if err != nil || !replay.Replayed || replay.Pending || replay.Response.Status != 202 || calls.Load() != 1 {
			t.Fatalf("completed cross-replica replay = %#v, calls %d, %v", replay, calls.Load(), err)
		}
	})

	t.Run("expired released reservation can bind a new request", func(t *testing.T) {
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/resources", Key: "expired-released-reuse", AuthorizationDigest: authorizationDigest}
		oldReservationID := "00000000-0000-7000-8000-000000000107"
		if _, err := ownerPool.Exec(ctx, `
INSERT INTO idempotency_keys(
  tenant_id,actor,route,idempotency_key,request_digest,expires_at,created_at,reservation_id,
  protocol_version,authorization_digest,started_at,released_at,repair_reason)
VALUES ($1,$2,$3,$4,'old-request',clock_timestamp()-interval '1 hour',clock_timestamp()-interval '2 hours',$5,
        2,$6,clock_timestamp()-interval '2 hours',clock_timestamp()-interval '1 hour','reviewed interrupted request')`,
			scope.TenantID, scope.Actor, scope.Route, scope.Key, oldReservationID, digestText("old authorization")); err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		execution, err := store.Execute(ctx, scope, "new-request", expiry, func(context.Context) idempotency.Response {
			calls.Add(1)
			return idempotency.Response{Digest: "new-request", Status: 202, Body: []byte(`{"state":"accepted"}`)}
		})
		if err != nil || execution.Replayed || execution.Pending || execution.ReservationID == oldReservationID || calls.Load() != 1 {
			t.Fatalf("expired released reuse = %#v, calls %d, %v", execution, calls.Load(), err)
		}
		var rows, released int
		var storedDigest string
		if err := ownerPool.QueryRow(ctx, `
SELECT count(*),count(*) FILTER (WHERE released_at IS NOT NULL),min(request_digest)
  FROM idempotency_keys
 WHERE tenant_id=$1 AND actor=$2 AND route=$3 AND idempotency_key=$4`,
			scope.TenantID, scope.Actor, scope.Route, scope.Key).Scan(&rows, &released, &storedDigest); err != nil || rows != 1 || released != 0 || storedDigest != "new-request" {
			t.Fatalf("rebound released row = rows %d released %d digest %q, %v", rows, released, storedDigest, err)
		}

		unexpired := scope
		unexpired.Key = "unexpired-released-reuse"
		if _, err := ownerPool.Exec(ctx, `
INSERT INTO idempotency_keys(
  tenant_id,actor,route,idempotency_key,request_digest,expires_at,created_at,reservation_id,
  protocol_version,authorization_digest,started_at,released_at,repair_reason)
VALUES ($1,$2,$3,$4,'bound-request',clock_timestamp()+interval '1 hour',clock_timestamp()-interval '1 hour',$5,
        2,$6,clock_timestamp()-interval '1 hour',clock_timestamp(),'reviewed interrupted request')`,
			unexpired.TenantID, unexpired.Actor, unexpired.Route, unexpired.Key, "00000000-0000-7000-8000-000000000108", unexpired.AuthorizationDigest); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Execute(ctx, unexpired, "different-request", expiry, func(context.Context) idempotency.Response {
			t.Fatal("unexpired released reservation accepted a different request")
			return idempotency.Response{}
		}); catalog.ErrorCodeOf(err) != catalog.CodeIdempotencyKeyReused {
			t.Fatalf("unexpired released request returned %v", err)
		}

		pendingKey := "api-cannot-release"
		if _, err := ownerPool.Exec(ctx, `
INSERT INTO idempotency_keys(
  tenant_id,actor,route,idempotency_key,request_digest,expires_at,created_at,reservation_id,
  protocol_version,authorization_digest,execution_token,started_at)
VALUES ($1,$2,$3,$4,'pending-request',clock_timestamp()-interval '1 hour',clock_timestamp()-interval '2 hours',$5,
        2,$6,$7,clock_timestamp()-interval '2 hours')`,
			scope.TenantID, scope.Actor, scope.Route, pendingKey, "00000000-0000-7000-8000-000000000109", scope.AuthorizationDigest, "00000000-0000-7000-8000-000000000110"); err != nil {
			t.Fatal(err)
		}
		if _, err := withTenantTx(ctx, apiPool, tenantID, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
			_, updateErr := tx.Exec(ctx, `
UPDATE idempotency_keys
   SET released_at=clock_timestamp(),repair_reason='API role attempted release',execution_token=NULL
 WHERE actor=$1 AND route=$2 AND idempotency_key=$3`, scope.Actor, scope.Route, pendingKey)
			return struct{}{}, updateErr
		}); err == nil || !strings.Contains(err.Error(), "lifecycle transition is invalid") {
			t.Fatalf("API role could perform repair-only release: %v", err)
		}
		if _, err := ownerPool.Exec(ctx, `
UPDATE idempotency_keys
   SET released_at=clock_timestamp(),repair_reason='integration authorization check complete',execution_token=NULL
 WHERE tenant_id=$1 AND actor=$2 AND route=$3 AND idempotency_key=$4`, tenantID, scope.Actor, scope.Route, pendingKey); err != nil {
			t.Fatal(err)
		}
		if _, err := ownerPool.Exec(ctx, `DELETE FROM idempotency_keys WHERE tenant_id=$1 AND actor=$2 AND route=$3 AND idempotency_key=$4`, tenantID, scope.Actor, scope.Route, pendingKey); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("expired completed response becomes a new atomic command", func(t *testing.T) {
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/resources", Key: "expired-completed-reuse", AuthorizationDigest: authorizationDigest}
		oldReservationID := "00000000-0000-7000-8000-000000000191"
		if _, err := ownerPool.Exec(ctx, `
INSERT INTO idempotency_keys(
  tenant_id,actor,route,idempotency_key,request_digest,atomic_response_status,atomic_response_headers,atomic_response_body,
  expires_at,created_at,reservation_id,protocol_version,authorization_digest,execution_token,started_at,completed_at)
VALUES ($1,$2,$3,$4,'old-request',201,'{}','{"old":true}',
        clock_timestamp()-interval '1 hour',clock_timestamp()-interval '2 hours',$5,2,$6,uuidv7(),
        clock_timestamp()-interval '2 hours',clock_timestamp()-interval '1 hour')`,
			scope.TenantID, scope.Actor, scope.Route, scope.Key, oldReservationID, authorizationDigest); err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		execution, err := store.Execute(ctx, scope, "current-request", expiry, func(context.Context) idempotency.Response {
			calls.Add(1)
			return idempotency.Response{Digest: "current-request", Status: 202, Body: []byte(`{"state":"accepted"}`)}
		})
		if err != nil || execution.Replayed || execution.Pending || execution.ReservationID == oldReservationID || execution.Response.Status != 202 || calls.Load() != 1 {
			t.Fatalf("expired completed reuse = %#v, calls %d, %v", execution, calls.Load(), err)
		}

		var reservationID, storedDigest, storedAuthorization string
		var protocol int
		var atomicStatus *int
		var releasedAt *time.Time
		if err := ownerPool.QueryRow(ctx, `
SELECT reservation_id::text,request_digest,authorization_digest,protocol_version,atomic_response_status,released_at
  FROM idempotency_keys
 WHERE tenant_id=$1 AND actor=$2 AND route=$3 AND idempotency_key=$4`,
			scope.TenantID, scope.Actor, scope.Route, scope.Key).Scan(&reservationID, &storedDigest, &storedAuthorization, &protocol, &atomicStatus, &releasedAt); err != nil {
			t.Fatal(err)
		}
		if reservationID != execution.ReservationID || storedDigest != "current-request" || storedAuthorization != authorizationDigest || protocol != 2 || atomicStatus == nil || *atomicStatus != 202 || releasedAt != nil {
			t.Fatalf("restarted completed row = reservation %q digest %q authorization %q protocol %d atomic %v released %v", reservationID, storedDigest, storedAuthorization, protocol, atomicStatus, releasedAt)
		}
	})

	t.Run("absent active snapshot preserves an earlier command mutation", func(t *testing.T) {
		publicationStore, err := NewPostgresPublicationStore(repository, "withdrawn.example.com")
		if err != nil {
			t.Fatal(err)
		}
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/resources", Key: "no-active-publication", AuthorizationDigest: authorizationDigest}
		params := newResource("00000000-0000-7000-8000-000000000901", "00000000-0000-7000-8000-000000000902", "00000000-0000-7000-8000-000000000903", "urn:air:catalog.example.com:atomic:no-active")
		execution, err := store.Execute(ctx, scope, "digest-no-active", expiry, func(commandContext context.Context) idempotency.Response {
			if _, err := repository.CreateResource(commandContext, params); err != nil {
				t.Fatal(err)
			}
			// Repeated withdrawal must treat this expected absence as a no-op
			// without poisoning the shared transaction containing the mutation.
			if _, err := publicationStore.Active(commandContext, tenantID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("active lookup: %v", err)
			}
			return idempotency.Response{Digest: "digest-no-active", Status: 201, Body: []byte(`{"ok":true}`)}
		})
		if err != nil || execution.Response.Status != 201 {
			t.Fatalf("command: %#v, %v", execution, err)
		}
		if _, err := repository.GetResource(ctx, tenantID, params.Resource.ID); err != nil {
			t.Fatalf("successful command lost mutation: %v", err)
		}
		if _, err := repository.GetResource(ctx, otherTenant, params.Resource.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("resource crossed tenant boundary: %v", err)
		}
		replay, err := store.Execute(ctx, scope, "digest-no-active", expiry, func(context.Context) idempotency.Response {
			t.Fatal("completed command executed again")
			return idempotency.Response{}
		})
		if err != nil || !replay.Replayed || string(replay.Response.Body) != string(execution.Response.Body) {
			t.Fatalf("replay: %#v, %v", replay, err)
		}
	})

	t.Run("swallowed repository failure cannot retain a success response", func(t *testing.T) {
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/resources", Key: "swallowed-failure", AuthorizationDigest: authorizationDigest}
		params := newResource("00000000-0000-7000-8000-000000000911", "00000000-0000-7000-8000-000000000912", "00000000-0000-7000-8000-000000000913", "urn:air:catalog.example.com:atomic:swallowed")
		_, err := store.Execute(ctx, scope, "digest-swallowed", expiry, func(commandContext context.Context) idempotency.Response {
			if _, err := repository.CreateResource(commandContext, params); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.GetResource(commandContext, tenantID, "00000000-0000-7000-8000-000000000999"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("expected repository failure: %v", err)
			}
			return idempotency.Response{Digest: "digest-swallowed", Status: 201, Body: []byte(`{"ok":true}`)}
		})
		if catalog.ErrorCodeOf(err) != catalog.CodeInternal {
			t.Fatalf("rolled-back command claimed success: %v", err)
		}
		var resources, audits, outbox, completed int
		if err := ownerPool.QueryRow(ctx, `SELECT
          (SELECT count(*) FROM resources WHERE tenant_id=$1 AND id=$2),
          (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND id=$3),
          (SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND id=$4),
          (SELECT count(*) FROM idempotency_keys WHERE tenant_id=$1 AND idempotency_key=$5 AND atomic_response_status IS NOT NULL)`,
			tenantID, params.Resource.ID, params.Audit.ID, params.Outbox.ID, scope.Key).Scan(&resources, &audits, &outbox, &completed); err != nil || resources != 0 || audits != 0 || outbox != 0 || completed != 0 {
			t.Fatalf("rolled-back state/evidence/response counts: %d/%d/%d/%d, %v", resources, audits, outbox, completed, err)
		}
		var reservationID string
		if err := ownerPool.QueryRow(ctx, `SELECT reservation_id::text FROM idempotency_keys WHERE tenant_id=$1 AND idempotency_key=$2`, tenantID, scope.Key).Scan(&reservationID); err != nil {
			t.Fatal(err)
		}
		plan, err := PlanIdempotencyRepair(ctx, ownerPool, tenantID, reservationID, "Release the rolled-back regression fixture")
		if err != nil || !plan.Eligible {
			t.Fatalf("rolled-back reservation not repairable: %#v, %v", plan, err)
		}
		if _, err := ApplyIdempotencyRepair(ctx, ownerPool, tenantID, reservationID, plan.Reason, plan.PlanDigest); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mutation and exact response commit together and replay once", func(t *testing.T) {
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/resources", Key: "atomic-success", AuthorizationDigest: authorizationDigest}
		params := newResource(
			"00000000-0000-7000-8000-000000000111",
			"00000000-0000-7000-8000-000000000112",
			"00000000-0000-7000-8000-000000000113",
			"urn:air:catalog.example.com:atomic:success",
		)
		var calls atomic.Int32
		var commandErr error
		execution, err := store.Execute(ctx, scope, "digest-success", expiry, func(commandContext context.Context) idempotency.Response {
			calls.Add(1)
			_, commandErr = repository.CreateResource(commandContext, params)
			return idempotency.Response{Digest: "digest-success", Status: 201, Headers: map[string][]string{"Location": {"/api/v1/resources/" + params.Resource.ID}}, Body: []byte(`{"id":"success"}`)}
		})
		if err != nil || commandErr != nil || execution.Replayed || execution.Response.Status != 201 || !uuidVersionSeven(execution.ReservationID) {
			t.Fatalf("atomic execution = %#v, command error %v, store error %v", execution, commandErr, err)
		}
		replay, err := store.Execute(ctx, scope, "digest-success", expiry, func(context.Context) idempotency.Response {
			calls.Add(1)
			return idempotency.Response{}
		})
		if err != nil || !replay.Replayed || replay.Response.Status != 201 || replay.Response.Headers["Location"][0] != execution.Response.Headers["Location"][0] || string(replay.Response.Body) != string(execution.Response.Body) || calls.Load() != 1 {
			t.Fatalf("atomic replay = %#v, calls %d, error %v", replay, calls.Load(), err)
		}
		if _, err := store.Execute(ctx, scope, "different-digest", expiry, func(context.Context) idempotency.Response { return idempotency.Response{} }); catalog.ErrorCodeOf(err) != catalog.CodeIdempotencyKeyReused {
			t.Fatalf("changed atomic command returned %v", err)
		}
		changedAuthorization := scope
		changedAuthorization.AuthorizationDigest = digestText("changed authorization")
		if _, err := store.Execute(ctx, changedAuthorization, "digest-success", expiry, func(context.Context) idempotency.Response {
			t.Fatal("authorization-changed replay executed the command")
			return idempotency.Response{}
		}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
			t.Fatalf("authorization-changed replay returned %v", err)
		}
		var resources, audits, outbox, completed int
		_, err = withTenantRead(ctx, ownerPool, tenantID, func(tx pgx.Tx) (struct{}, error) {
			return struct{}{}, tx.QueryRow(ctx, `
          SELECT
            (SELECT count(*) FROM resources WHERE id=$1),
            (SELECT count(*) FROM audit_events WHERE id=$2),
            (SELECT count(*) FROM outbox_events WHERE id=$3),
            (SELECT count(*) FROM idempotency_keys WHERE reservation_id=$4 AND atomic_response_status=201 AND completed_at IS NOT NULL)`,
				params.Resource.ID, params.Audit.ID, params.Outbox.ID, execution.ReservationID).Scan(&resources, &audits, &outbox, &completed)
		})
		if err != nil || resources != 1 || audits != 1 || outbox != 1 || completed != 1 {
			t.Fatalf("atomic evidence counts = resource %d audit %d outbox %d response %d, %v", resources, audits, outbox, completed, err)
		}
	})

	t.Run("caller cancellation after mutation still commits one authoritative response", func(t *testing.T) {
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/resources", Key: "atomic-cancel-after-mutation", AuthorizationDigest: authorizationDigest}
		params := newResource(
			"00000000-0000-7000-8000-000000000171",
			"00000000-0000-7000-8000-000000000172",
			"00000000-0000-7000-8000-000000000173",
			"urn:air:catalog.example.com:atomic:cancel-after-mutation",
		)
		requestContext, cancelRequest := context.WithCancel(ctx)
		var commandErr error
		execution, err := store.Execute(requestContext, scope, "digest-cancel-after-mutation", expiry, func(commandContext context.Context) idempotency.Response {
			_, commandErr = repository.CreateResource(commandContext, params)
			cancelRequest()
			return idempotency.Response{Digest: "digest-cancel-after-mutation", Status: 201, Body: []byte(`{"id":"cancel-after-mutation"}`)}
		})
		if err != nil || commandErr != nil || execution.Response.Status != 201 {
			t.Fatalf("cancellation-safe atomic completion = %#v, command error %v, store error %v", execution, commandErr, err)
		}
		replay, err := store.Execute(ctx, scope, "digest-cancel-after-mutation", expiry, func(context.Context) idempotency.Response {
			t.Fatal("completed canceled request executed twice")
			return idempotency.Response{}
		})
		if err != nil || !replay.Replayed || replay.Response.Status != 201 || string(replay.Response.Body) != string(execution.Response.Body) {
			t.Fatalf("cancellation-safe replay = %#v, %v", replay, err)
		}
		if _, err := repository.GetResource(ctx, tenantID, params.Resource.ID); err != nil {
			t.Fatalf("atomically completed canceled mutation was absent: %v", err)
		}
	})

	t.Run("bounded document and exact response commit together", func(t *testing.T) {
		resourceParams := newResource(
			"00000000-0000-7000-8000-000000000151",
			"00000000-0000-7000-8000-000000000152",
			"00000000-0000-7000-8000-000000000153",
			"urn:air:catalog.example.com:atomic:document",
		)
		if _, err := repository.CreateResource(ctx, resourceParams); err != nil {
			t.Fatal(err)
		}
		content := []byte("exact transactional document")
		digest := digestText(string(content))
		documentID := strings.TrimPrefix(digest, "sha256:")
		version := catalog.ResourceVersion{ID: "00000000-0000-7000-8000-000000000154", TenantID: tenantID, ResourceID: resourceParams.Resource.ID, Version: "1.0.0", Revision: 1, CreatedAt: now, UpdatedAt: now}
		revision := catalog.VersionRevision{ID: "00000000-0000-7000-8000-000000000155", TenantID: tenantID, ResourceVersionID: version.ID, RevisionNumber: 1, Representations: []catalog.Representation{{ID: "00000000-0000-7000-8000-000000000156", Role: catalog.RepresentationPrimary, MediaType: "text/plain", ProfileID: "atomic-document", ContentDigest: digest, Size: int64(len(content)), ArtifactID: documentID, ValidationState: "valid"}}, ContentSetDigest: digestText("atomic document set"), EvidenceSetID: "00000000-0000-7000-8000-000000000157", CreatedBy: "contributor", CreatedAt: now}
		evidence := catalog.EvidenceSet{ID: revision.EvidenceSetID, TenantID: tenantID, SubjectDigest: revision.ContentSetDigest, Digest: digestText("atomic document evidence"), State: catalog.EvidenceComplete, ValidationProfile: "catalog-validation-v7", PolicyVersion: "atomic-document", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, CreatedAt: now}
		document := catalog.DocumentBlob{ID: documentID, TenantID: tenantID, Digest: digest, MediaType: "text/plain", Size: int64(len(content)), Content: content, CreatedAt: now}
		audit := integrationAudit(t, "00000000-0000-7000-8000-000000000158", tenantID, "contributor", "resource_version.created", "resource_version", version.ID, "", now)
		outbox := integrationOutbox(t, "00000000-0000-7000-8000-000000000159", audit)
		params := catalog.CreateVersionParams{Version: version, VersionRevision: revision, Documents: []catalog.DocumentBlob{document}, Evidence: evidence, Audit: audit, Outbox: outbox}
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/resources/{resourceId}/versions", Key: "atomic-document", AuthorizationDigest: authorizationDigest}
		var calls atomic.Int32
		var commandErr error
		execution, err := store.Execute(ctx, scope, "digest-document", expiry, func(commandContext context.Context) idempotency.Response {
			calls.Add(1)
			_, _, _, commandErr = repository.CreateVersion(commandContext, params)
			return idempotency.Response{Digest: "digest-document", Status: 201, Body: []byte(`{"id":"document-version"}`)}
		})
		if err != nil || commandErr != nil || execution.Response.Status != 201 {
			t.Fatalf("atomic document execution = %#v, command error %v, store error %v", execution, commandErr, err)
		}
		replay, err := store.Execute(ctx, scope, "digest-document", expiry, func(context.Context) idempotency.Response {
			calls.Add(1)
			return idempotency.Response{}
		})
		if err != nil || !replay.Replayed || calls.Load() != 1 || string(replay.Response.Body) != string(execution.Response.Body) {
			t.Fatalf("atomic document replay = %#v, calls %d, %v", replay, calls.Load(), err)
		}
		var documents, references, versions, completed int
		_, err = withTenantRead(ctx, ownerPool, tenantID, func(tx pgx.Tx) (struct{}, error) {
			return struct{}{}, tx.QueryRow(ctx, `
          SELECT
            (SELECT count(*) FROM document_blobs WHERE id=$1),
            (SELECT count(*) FROM representations WHERE document_blob_id=$1),
            (SELECT count(*) FROM resource_versions WHERE id=$2),
            (SELECT count(*) FROM idempotency_keys WHERE reservation_id=$3 AND atomic_response_status=201 AND completed_at IS NOT NULL)`,
				documentID, version.ID, execution.ReservationID).Scan(&documents, &references, &versions, &completed)
		})
		if err != nil || documents != 1 || references != 1 || versions != 1 || completed != 1 {
			t.Fatalf("atomic document counts = document %d reference %d version %d response %d, %v", documents, references, versions, completed, err)
		}

		publicationStore, err := NewPostgresPublicationStore(repository, "atomic.example.com")
		if err != nil {
			t.Fatal(err)
		}
		snapshot := publication.Snapshot{
			ID:               "00000000-0000-7000-8000-000000000160",
			TenantID:         tenantID,
			Host:             "atomic.example.com",
			Audience:         publication.AudienceAnonymous,
			Generation:       1,
			ARDProfile:       "atomic-document",
			AICatalogProfile: "atomic-document",
			State:            publication.SnapshotCandidate,
			ARD:              publication.ARDManifest{Entries: []publication.Entry{}},
			AICatalog:        publication.AICatalog{SpecVersion: "1.0", Entries: []publication.AICatalogEntry{}},
			ETag:             `"atomic"`,
			AICatalogETag:    `"atomic-ai"`,
			CreatedAt:        now,
		}
		if _, err := publicationStore.SaveCandidate(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
		if _, err := publicationStore.Activate(ctx, tenantID, snapshot.ID, now); err != nil {
			t.Fatal(err)
		}
		renewedEvidence := catalog.EvidenceSet{ID: "00000000-0000-7000-8000-000000000161", TenantID: tenantID, SubjectDigest: revision.ContentSetDigest, Digest: digestText("atomic validation evidence"), State: catalog.EvidenceBlocked, ValidationProfile: "catalog-validation-v7", PolicyVersion: "atomic-document", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, CreatedAt: now.Add(time.Minute)}
		validationAudit := integrationAudit(t, "00000000-0000-7000-8000-000000000162", tenantID, "contributor", "version_revision.validated", "version_revision", revision.ID, "", renewedEvidence.CreatedAt)
		validationOutbox := integrationOutbox(t, "00000000-0000-7000-8000-000000000163", validationAudit)
		validationScope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/version-revisions/{revisionId}:validate", Key: "atomic-validation-withdrawal", AuthorizationDigest: authorizationDigest}
		var validationErr, withdrawalErr error
		validationExecution, err := store.Execute(ctx, validationScope, "digest-validation-withdrawal", expiry, func(commandContext context.Context) idempotency.Response {
			_, validationErr = repository.CreateValidationRun(commandContext, catalog.CreateValidationRunParams{TenantID: tenantID, VersionRevisionID: revision.ID, Evidence: renewedEvidence, Audit: validationAudit, Outbox: validationOutbox})
			if validationErr == nil {
				_, withdrawalErr = publicationStore.Deactivate(commandContext, tenantID, snapshot.ID, now.Add(time.Minute))
			}
			return idempotency.Response{Digest: "digest-validation-withdrawal", Status: 200, Body: []byte(`{"state":"blocked"}`)}
		})
		if err != nil || validationErr != nil || withdrawalErr != nil || validationExecution.Response.Status != 200 {
			t.Fatalf("atomic validation withdrawal = %#v, validation error %v, withdrawal error %v, store error %v", validationExecution, validationErr, withdrawalErr, err)
		}
		if _, err := repository.GetValidationEvidence(ctx, tenantID, revision.ID, renewedEvidence.ID); err != nil {
			t.Fatalf("committed validation evidence returned %v", err)
		}
		if _, err := publicationStore.Active(ctx, tenantID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("withdrawn publication remained active: %v", err)
		}
	})

	t.Run("aborted response rolls back mutation and retains failure", func(t *testing.T) {
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/resources", Key: "atomic-abort", AuthorizationDigest: authorizationDigest}
		params := newResource(
			"00000000-0000-7000-8000-000000000121",
			"00000000-0000-7000-8000-000000000122",
			"00000000-0000-7000-8000-000000000123",
			"urn:air:catalog.example.com:atomic:abort",
		)
		var commandErr error
		execution, err := store.Execute(ctx, scope, "digest-abort", expiry, func(commandContext context.Context) idempotency.Response {
			_, commandErr = repository.CreateResource(commandContext, params)
			idempotency.AbortCommand(commandContext, catalog.E(catalog.CodeInternal, "response encoding failed"))
			return idempotency.Response{Digest: "digest-abort", Status: 500, Body: []byte(`{"code":"INTERNAL"}`)}
		})
		if err != nil || commandErr != nil || execution.Response.Status != 500 {
			t.Fatalf("aborted command = %#v, command error %v, store error %v", execution, commandErr, err)
		}
		if _, err := repository.GetResource(ctx, tenantID, params.Resource.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("aborted command committed its resource: %v", err)
		}
		replay, err := store.Execute(ctx, scope, "digest-abort", expiry, func(context.Context) idempotency.Response {
			t.Fatal("aborted command replay executed again")
			return idempotency.Response{}
		})
		if err != nil || !replay.Replayed || replay.Response.Status != 500 {
			t.Fatalf("aborted command replay = %#v, %v", replay, err)
		}
	})

	t.Run("statement cancellation rolls back mutation and retains deadline response", func(t *testing.T) {
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/namespaces", Key: "atomic-statement-cancellation", AuthorizationDigest: authorizationDigest}
		blockedNamespace := catalog.Namespace{
			ID:          "00000000-0000-7000-8000-000000000181",
			TenantID:    tenantID,
			Slug:        "atomic-statement-cancellation",
			DisplayName: "Atomic statement cancellation",
			Revision:    1,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		blockedAudit := integrationAudit(t, "00000000-0000-7000-8000-000000000182", tenantID, "contributor", "namespace.created", "namespace", blockedNamespace.ID, "", now)
		blockedOutbox := integrationOutbox(t, "00000000-0000-7000-8000-000000000183", blockedAudit)

		lockConnection, err := ownerPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lockConnection.Release()
		lockTx, err := lockConnection.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackForCleanup(ctx, lockTx)()
		if _, err := lockTx.Exec(ctx, `LOCK TABLE namespaces IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}

		requestContext, cancelRequest := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancelRequest()
		var commandErr error
		execution, err := store.Execute(requestContext, scope, "digest-statement-cancellation", expiry, func(commandContext context.Context) idempotency.Response {
			_, commandErr = repository.CreateNamespace(commandContext, blockedNamespace, blockedAudit, blockedOutbox)
			idempotency.AbortCommand(commandContext, commandErr)
			return idempotency.Response{Digest: "digest-statement-cancellation", Status: 503, Body: []byte(`{"code":"REQUEST_DEADLINE_EXCEEDED"}`)}
		})
		if err != nil || commandErr == nil || execution.Response.Status != 503 {
			t.Fatalf("canceled statement execution = %#v, command error %v, store error %+v", execution, commandErr, err)
		}
		if err := lockTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.GetNamespace(ctx, tenantID, blockedNamespace.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("canceled statement committed its namespace: %v", err)
		}
		replay, err := store.Execute(ctx, scope, "digest-statement-cancellation", expiry, func(context.Context) idempotency.Response {
			t.Fatal("canceled statement replay executed again")
			return idempotency.Response{}
		})
		if err != nil || !replay.Replayed || replay.Response.Status != 503 || string(replay.Response.Body) != string(execution.Response.Body) {
			t.Fatalf("canceled statement replay = %#v, %v", replay, err)
		}
	})

	t.Run("invalid executor response rolls back before releasing its connection", func(t *testing.T) {
		scope := idempotency.Scope{TenantID: tenantID, Actor: "contributor", Route: "POST /api/v1/resources", Key: "atomic-invalid-response", AuthorizationDigest: authorizationDigest}
		params := newResource(
			"00000000-0000-7000-8000-000000000141",
			"00000000-0000-7000-8000-000000000142",
			"00000000-0000-7000-8000-000000000143",
			"urn:air:catalog.example.com:atomic:invalid-response",
		)
		_, err := store.Execute(ctx, scope, "digest-valid", expiry, func(commandContext context.Context) idempotency.Response {
			if _, createErr := repository.CreateResource(commandContext, params); createErr != nil {
				t.Fatal(createErr)
			}
			return idempotency.Response{Digest: "digest-invalid", Status: 201, Body: []byte(`{"id":"invalid"}`)}
		})
		if catalog.ErrorCodeOf(err) != catalog.CodeInternal {
			t.Fatalf("invalid executor response returned %v", err)
		}
		if _, err := repository.GetResource(ctx, tenantID, params.Resource.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("invalid executor response committed its resource: %v", err)
		}
		var reservationID string
		_, err = withTenantRead(ctx, ownerPool, tenantID, func(tx pgx.Tx) (struct{}, error) {
			return struct{}{}, tx.QueryRow(ctx, `SELECT reservation_id::text FROM idempotency_keys WHERE tenant_id=$1 AND idempotency_key=$2`, tenantID, scope.Key).Scan(&reservationID)
		})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := PlanIdempotencyRepair(ctx, ownerPool, tenantID, reservationID, "executor contract failed")
		if err != nil || !plan.Eligible || plan.State != "interrupted" || plan.Version != idempotencyRepairPlanVersion {
			t.Fatalf("invalid-response repair plan = %#v, %v", plan, err)
		}
		applied, err := ApplyIdempotencyRepair(ctx, ownerPool, tenantID, reservationID, plan.Reason, plan.PlanDigest)
		if err != nil || !applied.Applied || applied.AuditEventID == "" || applied.Plan.PlanDigest != plan.PlanDigest {
			t.Fatalf("applied audit-only repair = %#v, %v", applied, err)
		}
		var repairOutboxCount int
		_, err = withTenantRead(ctx, ownerPool, tenantID, func(tx pgx.Tx) (struct{}, error) {
			return struct{}{}, tx.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE subject=$1 AND event_type=$2`, reservationID, catalog.CanonicalEventType("idempotency.reservation_released")).Scan(&repairOutboxCount)
		})
		if err != nil || repairOutboxCount != 0 {
			t.Fatalf("audit-only repair outbox count = %d, %v", repairOutboxCount, err)
		}
	})

	t.Run("connection loss leaves no mutation and requires reviewed release", func(t *testing.T) {
		scope := idempotency.Scope{TenantID: strings.ToUpper(tenantID), Actor: "contributor", Route: "POST /api/v1/resources", Key: "atomic-crash-secret", AuthorizationDigest: authorizationDigest}
		params := newResource(
			"00000000-0000-7000-8000-000000000131",
			"00000000-0000-7000-8000-000000000132",
			"00000000-0000-7000-8000-000000000133",
			"urn:air:catalog.example.com:atomic:crash",
		)
		connection, err := apiPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		lockKey := idempotency.AdvisoryLockKey(scope)
		if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
			connection.Release()
			t.Fatal(err)
		}
		command := &commandTransaction{pool: apiPool, conn: connection, tenantID: tenantID}
		reservation, reservationID, _, err := store.reserveAtomic(ctx, command, scope, "digest-crash", expiry)
		if err != nil || !reservation.Acquired || reservationID == "" {
			connection.Release()
			t.Fatalf("crash reservation = %#v, id %q, %v", reservation, reservationID, err)
		}
		if _, err := repository.CreateResource(withCommandTransaction(ctx, command), params); err != nil {
			connection.Release()
			t.Fatal(err)
		}
		activePlan, err := PlanIdempotencyRepair(ctx, ownerPool, tenantID, reservationID, "process was interrupted")
		if err != nil || activePlan.State != "active" || activePlan.Eligible {
			connection.Release()
			t.Fatalf("active repair plan = %#v, %v", activePlan, err)
		}
		if err := connection.Conn().Close(ctx); err != nil {
			connection.Release()
			t.Fatal(err)
		}
		connection.Release()

		if _, err := repository.GetResource(ctx, tenantID, params.Resource.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("crashed command mutation survived: %v", err)
		}
		plan, err := PlanIdempotencyRepair(ctx, ownerPool, tenantID, reservationID, "process was interrupted")
		if err != nil || plan.State != "interrupted" || !plan.Eligible || strings.Contains(mustJSON(t, plan), scope.Key) {
			t.Fatalf("interrupted repair plan = %#v, %v", plan, err)
		}
		if _, err := PlanIdempotencyRepair(ctx, ownerPool, otherTenant, reservationID, "cross-tenant attempt"); err == nil {
			t.Fatal("cross-tenant repair plan found another tenant's reservation")
		}
		if _, err := ListPendingIdempotencyReservations(ctx, apiPool, tenantID, 10); err == nil {
			t.Fatal("API runtime role was authorized to run operator repair inventory")
		}
		if _, err := ListPendingIdempotencyReservations(ctx, workerPool, tenantID, 10); err == nil {
			t.Fatal("worker runtime role was authorized to run operator repair inventory")
		}
		report, err := ListPendingIdempotencyReservations(ctx, ownerPool, tenantID, 10)
		if err != nil || report.Count != 1 || report.Items[0].ReservationID != reservationID || !report.Items[0].Repairable {
			t.Fatalf("pending repair inventory = %#v, %v", report, err)
		}
		if _, err := ApplyIdempotencyRepair(ctx, ownerPool, tenantID, reservationID, plan.Reason, "sha256:"+strings.Repeat("0", 64)); err == nil {
			t.Fatal("stale repair plan digest was applied")
		}
		func() {
			lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			// A command can acquire its lock after the operator has reviewed a plan.
			// Pin an API session distinct from every owner-pool repair connection.
			holder, err := apiPool.Acquire(lockCtx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Release()
			if _, err := holder.Exec(lockCtx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := releaseCommandLock(lockCtx, holder, lockKey); err != nil {
					t.Errorf("release competing command lock: %v", err)
				}
			}()
			readUnchangedReservation := func() string {
				t.Helper()
				var row string
				var pending bool
				var auditCount, outboxCount int
				_, err := withTenantRead(lockCtx, ownerPool, tenantID, func(tx pgx.Tx) (struct{}, error) {
					return struct{}{}, tx.QueryRow(lockCtx, `
SELECT to_jsonb(reservation)::text,
       execution_token IS NOT NULL AND released_at IS NULL AND atomic_response_status IS NULL,
       (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND target_id=$2),
       (SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND subject=$2)
  FROM idempotency_keys reservation WHERE tenant_id=$1 AND reservation_id=$2::uuid`, tenantID, reservationID).
						Scan(&row, &pending, &auditCount, &outboxCount)
				})
				if err != nil || !pending || auditCount != 0 || outboxCount != 0 {
					t.Fatalf("active reservation changed: pending=%v audit=%d outbox=%d err=%v", pending, auditCount, outboxCount, err)
				}
				return row
			}
			before := readUnchangedReservation()
			rejected, err := ApplyIdempotencyRepair(lockCtx, ownerPool, tenantID, reservationID, plan.Reason, plan.PlanDigest)
			if err == nil || err.Error() != "idempotency reservation is still owned by an active command" || lockCtx.Err() != nil || rejected != (IdempotencyRepairResult{}) {
				t.Fatalf("active-command repair rejection = %#v, %v (context: %v)", rejected, err, lockCtx.Err())
			}
			if after := readUnchangedReservation(); after != before {
				t.Fatal("rejected repair changed the reservation or execution binding")
			}
		}()
		// Reuse the same digest after releasing the holder: contention must not
		// mutate the reservation or invalidate an otherwise current plan.
		applied, err := ApplyIdempotencyRepair(ctx, ownerPool, tenantID, reservationID, plan.Reason, plan.PlanDigest)
		if err != nil || !applied.Applied || !uuidVersionSeven(applied.AuditEventID) {
			t.Fatalf("applied repair = %#v, %v", applied, err)
		}
		var details string
		var releasedTokenCleared bool
		_, err = withTenantRead(ctx, ownerPool, tenantID, func(tx pgx.Tx) (struct{}, error) {
			return struct{}{}, tx.QueryRow(ctx, `
          SELECT
            (SELECT details::text FROM audit_events WHERE id=$1),
            (SELECT execution_token IS NULL FROM idempotency_keys WHERE tenant_id=$2 AND reservation_id=$3)`,
				applied.AuditEventID, tenantID, reservationID).Scan(&details, &releasedTokenCleared)
		})
		if err != nil || strings.Contains(details, scope.Key) || strings.Contains(details, plan.IdempotencyKeyDigest) || strings.Contains(details, plan.PlanDigest) || strings.Contains(details, scope.AuthorizationDigest) || !strings.Contains(details, `"principalType": "database_login"`) || !releasedTokenCleared {
			t.Fatalf("repair audit details = %q, execution token cleared %t, %v", details, releasedTokenCleared, err)
		}

		var calls atomic.Int32
		var commandErr error
		execution, err := store.Execute(ctx, scope, "digest-crash", expiry, func(commandContext context.Context) idempotency.Response {
			calls.Add(1)
			_, commandErr = repository.CreateResource(commandContext, params)
			return idempotency.Response{Digest: "digest-crash", Status: 201, Body: []byte(`{"id":"recovered"}`)}
		})
		if err != nil || commandErr != nil || execution.Response.Status != 201 || execution.ReservationID == reservationID || calls.Load() != 1 {
			t.Fatalf("recovered execution = %#v, calls %d, command error %v, store error %v", execution, calls.Load(), commandErr, err)
		}
		replay, err := store.Execute(ctx, scope, "digest-crash", expiry, func(context.Context) idempotency.Response {
			calls.Add(1)
			return idempotency.Response{}
		})
		if err != nil || !replay.Replayed || calls.Load() != 1 {
			t.Fatalf("recovered replay = %#v, calls %d, %v", replay, calls.Load(), err)
		}
		var resourceCount, repairAuditCount, repairOutboxCount int
		_, err = withTenantRead(ctx, ownerPool, tenantID, func(tx pgx.Tx) (struct{}, error) {
			return struct{}{}, tx.QueryRow(ctx, `
          SELECT
            (SELECT count(*) FROM resources WHERE id=$1),
            (SELECT count(*) FROM audit_events WHERE id=$2),
			(SELECT count(*) FROM outbox_events WHERE subject=$3 AND event_type=$4)`,
				params.Resource.ID, applied.AuditEventID, reservationID, catalog.CanonicalEventType("idempotency.reservation_released")).Scan(&resourceCount, &repairAuditCount, &repairOutboxCount)
		})
		if err != nil || resourceCount != 1 || repairAuditCount != 1 || repairOutboxCount != 0 {
			t.Fatalf("recovery evidence = resource %d audit %d outbox %d, %v", resourceCount, repairAuditCount, repairOutboxCount, err)
		}
	})

	t.Run("database refuses unsupported command protocols", func(t *testing.T) {
		for _, protocol := range []int{0, 1, 3} {
			_, err := ownerPool.Exec(ctx, `
INSERT INTO idempotency_keys(tenant_id,actor,route,idempotency_key,request_digest,expires_at,created_at,protocol_version,authorization_digest,execution_token)
VALUES ($1,'unsupported','POST /test',$2,'digest',clock_timestamp()+interval '1 hour',clock_timestamp(),$3,$4,uuidv7())`,
				tenantID, fmt.Sprint(protocol), protocol, authorizationDigest)
			var failure *pgconn.PgError
			if !errors.As(err, &failure) || failure.Code != "23514" || failure.ConstraintName != "idempotency_protocol_version_valid" {
				t.Fatalf("protocol %d rejection = %v", protocol, err)
			}
		}
	})

}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func uuidVersionSeven(value string) bool {
	return len(value) == 36 && value[14] == '7'
}
