//go:build integration

package database

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	catalogevents "github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	platformfetcher "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

type postgresExternalFetcher struct {
	at           time.Time
	body         []byte
	err          error
	policyDigest string
	requests     int
}

type postgresFederationFetcher struct {
	result       platformfetcher.Result
	beforeReturn func() error
	policyDigest string
}

func (f *postgresFederationFetcher) Fetch(_ context.Context, request platformfetcher.Request) (platformfetcher.Result, error) {
	if f.beforeReturn != nil {
		beforeReturn := f.beforeReturn
		f.beforeReturn = nil
		if err := beforeReturn(); err != nil {
			return platformfetcher.Result{}, err
		}
	}
	result := f.result
	if result.FinalURL == "" {
		result.FinalURL = request.URL
	}
	if result.BodyDigest == "" {
		sum := sha256.Sum256(result.Body)
		result.BodyDigest = fmt.Sprintf("sha256:%x", sum)
	}
	if result.PolicyID == "" {
		result.PolicyID = "integration-policy"
	}
	if result.PolicyRevision == 0 {
		result.PolicyRevision = 1
	}
	if result.PolicyDigest == "" {
		result.PolicyDigest = f.policyDigest
	}
	result.NetworkProfile = request.NetworkProfile
	return result, nil
}

func (f *postgresFederationFetcher) PolicyDigest() string { return f.policyDigest }

type postgresIndexedCatalogSearcher struct {
	store *PostgresFederationStore
}

func integrationAudit(t *testing.T, idValue, tenantID, actor, action, targetType, targetID, reason string, at time.Time) catalog.AuditEvent {
	t.Helper()
	var facts map[string]any
	if action == audit.ActionAuditExported {
		facts = map[string]any{
			"rowCount":       0,
			"eventsDigest":   audit.DigestBytes([]byte("[]")),
			"occurredFrom":   at.Add(-time.Minute).Format(time.RFC3339Nano),
			"occurredBefore": at.Format(time.RFC3339Nano),
		}
	}
	record, err := audit.NewRecord(idValue, tenantID, audit.Principal{ID: "integration-" + actor, Type: audit.PrincipalServiceExecutor}, action, targetType, targetID, reason, audit.Execution{}, facts, at)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func integrationOutbox(t *testing.T, idValue string, record catalog.AuditEvent) catalog.OutboxEvent {
	t.Helper()
	data, err := catalogevents.MarshalReference(record.Action, record.TargetType, record.TargetID, record.AfterDigest)
	if err != nil {
		t.Fatal(err)
	}
	return catalog.OutboxEvent{ID: idValue, TenantID: record.TenantID, Type: catalog.CanonicalEventType(record.Action), Subject: record.TargetID, Data: data, CreatedAt: record.At}
}

func requireInsufficientPrivilege(t *testing.T, err error) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "42501" {
		t.Fatalf("expected PostgreSQL insufficient_privilege, got %v", err)
	}
}

func requireCatalogColumnInventory(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT class.relname,array_agg(attribute.attname ORDER BY attribute.attnum)::text[]
  FROM pg_catalog.pg_class class
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=class.relnamespace
  JOIN pg_catalog.pg_attribute attribute ON attribute.attrelid=class.oid
 WHERE namespace.nspname=$1 AND class.relkind IN ('r','p')
   AND attribute.attnum>0 AND NOT attribute.attisdropped
 GROUP BY class.relname
 ORDER BY class.relname`, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	actual := make(map[string][]string, len(catalogTableColumns))
	for rows.Next() {
		var table string
		var columns []string
		if err := rows.Scan(&table, &columns); err != nil {
			t.Fatal(err)
		}
		actual[table] = columns
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(catalogTableColumns) {
		t.Fatalf("database has %d Catalog tables, column manifest has %d: database=%v", len(actual), len(catalogTableColumns), slices.Sorted(maps.Keys(actual)))
	}
	for table, expected := range catalogTableColumns {
		if !slices.Equal(actual[table], expected) {
			t.Fatalf("Catalog column inventory for %s = %#v, want %#v", table, actual[table], expected)
		}
	}
}

func requireAuditInvestigationPlans(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, targetID string, from, before time.Time) {
	t.Helper()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(ctx, tx)()
	if err := setTenantContext(ctx, tx, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode=force_custom_plan`); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		actor    string
		action   string
		request  string
		typeName string
		objectID string
		from     *time.Time
		before   *time.Time
		index    string
	}{
		{name: "actor", actor: "00000000-0000-7000-8000-000000000001", index: "audit_tenant_actor_time"},
		{name: "action", action: "resource.created", index: "audit_tenant_action_time"},
		{name: "request", request: "plan-request", index: "audit_tenant_request_time"},
		{name: "target", typeName: "resource", objectID: targetID, index: "audit_tenant_target_time"},
		{name: "time", from: &from, before: &before, index: "audit_tenant_time"},
	}
	for _, test := range tests {
		rows, err := tx.Query(ctx, `
EXPLAIN (COSTS OFF)
SELECT format_version,id,tenant_id,actor,action,target_type,target_id,reason,request_id,before_digest,after_digest,details,occurred_at
  FROM audit_events
 WHERE tenant_id=$1 AND ($2::timestamptz IS NULL OR occurred_at<$2 OR (occurred_at=$2 AND id<nullif($3,'')::uuid))
	   AND ($4='' OR (octet_length(actor)<=512 AND actor=$4))
	   AND ($5='' OR (octet_length(action)<=160 AND action=$5))
	   AND ($6='' OR (request_id<>'' AND octet_length(request_id)<=128 AND request_id=$6))
	   AND ($7='' OR (octet_length(target_type)<=160 AND octet_length(target_id)<=512 AND target_type=$7 AND target_id=$8))
   AND ($9::timestamptz IS NULL OR occurred_at >= $9)
   AND ($10::timestamptz IS NULL OR occurred_at < $10)
 ORDER BY occurred_at DESC,id DESC LIMIT $11`, tenantID, (*time.Time)(nil), "", test.actor, test.action, test.request, test.typeName, test.objectID, test.from, test.before, 101)
		if err != nil {
			t.Fatalf("explain audit %s filter: %v", test.name, err)
		}
		var lines []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			lines = append(lines, line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		plan := strings.Join(lines, "\n")
		if !strings.Contains(plan, test.index) {
			t.Fatalf("audit %s filter plan does not use %s:\n%s", test.name, test.index, plan)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func (s postgresIndexedCatalogSearcher) SearchFederatedCatalog(ctx context.Context, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return catalog.FederatedCatalogSearchPage{}, catalog.E(catalog.CodeUnauthenticated, "authentication is required")
	}
	return s.store.SearchIndexedCatalog(ctx, actor.TenantID, request)
}

func (f *postgresExternalFetcher) PolicyDigest() string { return f.policyDigest }

func (f *postgresExternalFetcher) FetchExternalRepresentation(_ context.Context, request catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	f.requests++
	if f.err != nil {
		return catalog.ExternalRepresentationFetchResult{}, f.err
	}
	sum := sha256.Sum256(f.body)
	return catalog.ExternalRepresentationFetchResult{
		RequestedURL:   request.URL,
		FinalURL:       request.URL,
		Status:         200,
		MediaType:      request.MediaType,
		Body:           append([]byte(nil), f.body...),
		ContentDigest:  fmt.Sprintf("sha256:%x", sum),
		PolicyID:       "integration-policy",
		PolicyRevision: 9,
		PolicyDigest:   f.policyDigest,
		RetrievedAt:    f.at,
	}, nil
}

func TestRuntimeConnectionsRequireReadWriteSessions(t *testing.T) {
	baseDSN := os.Getenv("CATALOG_TEST_DATABASE_DSN")
	if baseDSN == "" {
		t.Skip("CATALOG_TEST_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := Open(ctx, baseDSN, testPoolConfig(2))
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	role := "catalog_runtime_read_write_test_" + suffix
	password := "catalog-runtime-read-write-" + suffix
	roleIdentifier := pgx.Identifier{role}.Sanitize()
	if _, err := adminPool.Exec(ctx, `CREATE ROLE `+roleIdentifier+` LOGIN PASSWORD '`+password+`' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = adminPool.Exec(context.Background(), `DROP ROLE IF EXISTS `+roleIdentifier)
	}()
	parsed, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword(role, password)
	roleDSN := parsed.String()
	verifyPoolRecycling(t, roleDSN, role)

	observer := &recordingQueryObserver{}
	runtimeSettings := testPoolConfig(2)
	runtimeSettings.MaxConnLifetime = 45 * time.Minute
	runtimeSettings.MaxConnIdleTime = 3 * time.Minute
	runtimeSettings.HealthCheckPeriod = 15 * time.Second
	runtimePool, err := OpenRuntimeObserved(ctx, roleDSN, runtimeSettings, observer)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimePool.Close()
	runtimeRepository, err := NewPostgresRepository(runtimePool)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeRepository.Ping(ctx); err != nil {
		t.Fatalf("writable runtime readiness failed: %v", err)
	}
	if !slices.ContainsFunc(observer.observations, func(observation queryObservation) bool { return observation.operation == "select" }) {
		t.Fatal("runtime pool did not retain query observation")
	}
	canceledReadiness, cancelReadiness := context.WithCancel(context.Background())
	cancelReadiness()
	if err := runtimeRepository.Ping(canceledReadiness); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("canceled runtime readiness error = %v", err)
	}
	var timezone string
	if err := runtimePool.QueryRow(ctx, `SHOW TIME ZONE`).Scan(&timezone); err != nil || timezone != "UTC" {
		t.Fatalf("runtime timezone = %q, %v", timezone, err)
	}

	held, err := runtimePool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if _, err := adminPool.Exec(ctx, `ALTER ROLE `+roleIdentifier+` SET default_transaction_read_only=on`); err != nil {
		t.Fatal(err)
	}
	laterCtx, cancelLater := context.WithTimeout(ctx, time.Second)
	later, laterErr := runtimePool.Acquire(laterCtx)
	cancelLater()
	if laterErr == nil {
		later.Release()
		t.Fatal("runtime pool admitted a later read-only physical connection")
	}

	operatorPool, err := Open(ctx, roleDSN, testPoolConfig(1))
	if err != nil {
		t.Fatalf("generic operator connection rejected a read-only session: %v", err)
	}
	var transactionReadOnly string
	if err := operatorPool.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&transactionReadOnly); err != nil || transactionReadOnly != "on" {
		operatorPool.Close()
		t.Fatalf("operator transaction_read_only = %q, %v", transactionReadOnly, err)
	}
	operatorRepository, err := NewPostgresRepository(operatorPool)
	if err != nil {
		operatorPool.Close()
		t.Fatal(err)
	}
	if err := operatorRepository.Ping(ctx); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		operatorPool.Close()
		t.Fatalf("read-only runtime readiness error = %v", err)
	}
	operatorPool.Close()

	readOnlyCtx, cancelReadOnly := context.WithTimeout(ctx, 3*time.Second)
	readOnlyPool, readOnlyErr := OpenRuntimeObserved(readOnlyCtx, roleDSN, testPoolConfig(1), nil)
	cancelReadOnly()
	if readOnlyPool != nil {
		readOnlyPool.Close()
	}
	if readOnlyErr == nil {
		t.Fatal("runtime opening accepted a read-only session")
	}
	if _, err := adminPool.Exec(ctx, `ALTER ROLE `+roleIdentifier+` RESET default_transaction_read_only`); err != nil {
		t.Fatal(err)
	}
	recoveredPool, err := OpenRuntimeObserved(ctx, roleDSN, testPoolConfig(1), nil)
	if err != nil {
		t.Fatalf("runtime opening did not recover after restoring read/write sessions: %v", err)
	}
	defer recoveredPool.Close()
	recoveredRepository, err := NewPostgresRepository(recoveredPool)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoveredRepository.Ping(ctx); err != nil {
		t.Fatalf("recovered runtime readiness failed: %v", err)
	}
}

func TestMigrationsFreshInstallAndUnsupportedStateRefusal(t *testing.T) {
	baseDSN := os.Getenv("CATALOG_TEST_DATABASE_DSN")
	if baseDSN == "" {
		t.Skip("CATALOG_TEST_DATABASE_DSN is not set")
	}
	ctx := context.Background()
	adminPool, err := Open(ctx, baseDSN, testPoolConfig(4))
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	t.Run("fresh install", func(t *testing.T) {
		schema := "catalog_migration_fresh_" + suffix
		if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		defer func(cleanupCtx context.Context) {
			_, _ = adminPool.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		}(ctx)
		pool, err := Open(ctx, withSearchPath(t, baseDSN, schema+",public"), testPoolConfig(4))
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		applied, err := applyMigrationsForTest(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		if len(applied) != 47 || applied[0].Name != "000001_catalog.sql" || applied[1].Name != "000002_publication_domain_approval.sql" || applied[2].Name != "000003_atomic_command_results.sql" || applied[3].Name != "000004_catalog_documents.sql" || applied[4].Name != "000005_skill_packages.sql" || applied[5].Name != "000006_identity_and_discovery.sql" || applied[6].Name != "000007_controlled_federation.sql" || applied[7].Name != "000008_renewable_validation.sql" || applied[8].Name != "000009_https_representations.sql" || applied[9].Name != "000010_observation_provenance.sql" || applied[10].Name != "000011_bounded_search.sql" || applied[11].Name != "000012_practical_administration.sql" || applied[12].Name != "000013_automatic_source_maintenance.sql" || applied[13].Name != "000014_api_contract_strictness.sql" || applied[14].Name != "000015_runtime_privileges_and_history.sql" || applied[15].Name != "000016_audit_investigation_export.sql" || applied[16].Name != "000017_bounded_search_repair.sql" || applied[17].Name != "000018_operational_housekeeping.sql" || applied[18].Name != "000019_webhook_dispatch_schedule.sql" || applied[19].Name != "000020_webhook_secret_reencryption.sql" || applied[20].Name != "000021_current_command_protocol.sql" || applied[21].Name != "000022_current_evidence_baseline.sql" || applied[28].Name != "000029_local_search_projections.sql" || applied[29].Name != "000030_indexed_search_projections.sql" || applied[30].Name != "000031_agent_plugins.sql" || applied[39].Name != "000040_unicode_version_limits.sql" || applied[42].Name != "000043_personal_library.sql" || applied[43].Name != "000044_principal_and_group_guards.sql" || applied[44].Name != "000045_directory_lookup.sql" || applied[45].Name != "000046_publisher_search_order.sql" || applied[46].Name != "000047_publisher_domain_verification.sql" {
			t.Fatalf("fresh migration result = %#v", applied)
		}
		requireCatalogColumnInventory(t, ctx, pool, schema)
		var nullable string
		if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name='publishers' AND column_name='approved_publication_domains'`, schema).Scan(&nullable); err != nil || nullable != "NO" {
			t.Fatalf("fresh approved-publication-domain column = %q, %v", nullable, err)
		}
		if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name='idempotency_keys' AND column_name='reservation_id'`, schema).Scan(&nullable); err != nil || nullable != "NO" {
			t.Fatalf("fresh idempotency reservation column = %q, %v", nullable, err)
		}
		if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name='representations' AND column_name='document_blob_id'`, schema).Scan(&nullable); err != nil || nullable != "YES" {
			t.Fatalf("fresh representation document column = %q, %v", nullable, err)
		}
		if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name='representations' AND column_name='external_reference_only'`, schema).Scan(&nullable); err != nil || nullable != "NO" {
			t.Fatalf("fresh external-reference marker = %q, %v", nullable, err)
		}
		if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name='evidence_sets' AND column_name='external_retrievals'`, schema).Scan(&nullable); err != nil || nullable != "NO" {
			t.Fatalf("fresh external retrieval evidence = %q, %v", nullable, err)
		}
		if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name='resources' AND column_name='deleted_at'`, schema).Scan(&nullable); err != nil || nullable != "YES" {
			t.Fatalf("fresh resource deletion marker = %q, %v", nullable, err)
		}
		var internalDefault string
		if err := pool.QueryRow(ctx, `SELECT is_nullable,column_default FROM information_schema.columns WHERE table_schema=$1 AND table_name='outbox_events' AND column_name='is_internal'`, schema).Scan(&nullable, &internalDefault); err != nil || nullable != "NO" || internalDefault != "false" {
			t.Fatalf("fresh internal-outbox marker = nullable %q default %q, %v", nullable, internalDefault, err)
		}
		var membershipStateConstraint string
		if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=$1 AND t.relname='tenant_memberships' AND c.conname='tenant_memberships_state_check'`, schema).Scan(&membershipStateConstraint); err != nil || !strings.Contains(membershipStateConstraint, "offboarded") {
			t.Fatalf("fresh membership-state constraint = %q, %v", membershipStateConstraint, err)
		}
		var rowSecurity, forceRowSecurity bool
		if err := pool.QueryRow(ctx, `SELECT c.relrowsecurity,c.relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='document_blobs'`, schema).Scan(&rowSecurity, &forceRowSecurity); err != nil || !rowSecurity || !forceRowSecurity {
			t.Fatalf("fresh document RLS = enabled %t forced %t, %v", rowSecurity, forceRowSecurity, err)
		}
		for _, table := range []string{"artifact_references", "skill_file_inventory", "tenant_identity_realms", "tenant_memberships", "namespace_memberships", "role_assignments", "federation_source_revisions", "federation_source_audiences", "federation_source_referrals", "observation_occurrences", "source_retrieval_documents", "source_retrieval_states", "source_retrieval_attempts", "source_maintenance_state", "version_validation_runs"} {
			if err := pool.QueryRow(ctx, `SELECT c.relrowsecurity,c.relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, schema, table).Scan(&rowSecurity, &forceRowSecurity); err != nil || !rowSecurity || !forceRowSecurity {
				t.Fatalf("fresh %s RLS = enabled %t forced %t, %v", table, rowSecurity, forceRowSecurity, err)
			}
		}
		var sizeConstraint string
		if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=$1 AND t.relname='document_blobs' AND c.conname='document_blobs_size_valid'`, schema).Scan(&sizeConstraint); err != nil || !strings.Contains(sizeConstraint, fmt.Sprintf("%d", catalog.MaxCatalogDocumentBytes)) {
			t.Fatalf("fresh document size constraint = %q, %v", sizeConstraint, err)
		}
		const tenantID = "00000000-0000-7000-8000-000000000041"
		if _, err := withTenantTx(ctx, pool, tenantID, pgx.TxOptions{}, func(pgx.Tx) (struct{}, error) { return struct{}{}, nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,display_name) VALUES ($1,'Audit guard tenant')`, tenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO audit_events(format_version,id,tenant_id,actor,action,target_type,target_id,details,occurred_at)
VALUES (1,'00000000-0000-7000-8000-000000000045',$1::uuid,'catalog-api','namespace.created','namespace',$1::uuid::text,'{"principalType":"service_executor"}',clock_timestamp())`, tenantID); err != nil {
			t.Fatalf("valid direct version-1 audit insert: %v", err)
		}
		auditGuardCases := []struct {
			name       string
			id         string
			version    any
			actor      string
			reason     string
			requestID  string
			after      string
			details    string
			constraint string
		}{
			{name: "missing version", id: "00000000-0000-7000-8000-000000000046", actor: "catalog-api", details: `{"principalType":"service_executor"}`, constraint: "audit_events_format_version_required"},
			{name: "oversized actor", id: "00000000-0000-7000-8000-000000000047", version: 1, actor: strings.Repeat("a", audit.MaxActorBytes+1), details: `{"principalType":"service_executor"}`, constraint: "audit_events_indexed_text_shape"},
			{name: "invalid reason", id: "00000000-0000-7000-8000-000000000048", version: 1, actor: "catalog-api", reason: "line\nbreak", details: `{"principalType":"service_executor"}`, constraint: "audit_events_reason_shape"},
			{name: "invalid request ID", id: "00000000-0000-7000-8000-000000000049", version: 1, actor: "catalog-api", requestID: "request/one", details: `{"principalType":"service_executor"}`, constraint: "audit_events_request_id_shape"},
			{name: "invalid digest", id: "00000000-0000-7000-8000-000000000050", version: 1, actor: "catalog-api", after: "sha256:no", details: `{"principalType":"service_executor"}`, constraint: "audit_events_digest_shape"},
			{name: "missing principal", id: "00000000-0000-7000-8000-000000000051", version: 1, actor: "catalog-api", details: `{}`, constraint: "audit_events_details_shape"},
			{name: "oversized details", id: "00000000-0000-7000-8000-000000000052", version: 1, actor: "catalog-api", details: `{"principalType":"service_executor","facts":{"value":"` + strings.Repeat("x", audit.MaxDetailsBytes) + `"}}`, constraint: "audit_events_details_shape"},
			{name: "invalid membership attribution", id: "00000000-0000-7000-8000-000000000053", version: 1, actor: "not-a-membership", details: `{"principalType":"catalog_membership","actorType":"human","realmId":"00000000-0000-7000-8000-000000000001"}`, constraint: "audit_events_membership_attribution"},
			{name: "membership contains database attribution", id: "00000000-0000-7000-8000-000000000054", version: 1, actor: "00000000-0000-7000-8000-000000000001", details: `{"principalType":"catalog_membership","actorType":"human","realmId":"00000000-0000-7000-8000-000000000001","currentUser":"catalog_api"}`, constraint: "audit_events_membership_attribution"},
			{name: "service contains membership attribution", id: "00000000-0000-7000-8000-000000000055", version: 1, actor: "catalog-api", details: `{"principalType":"service_executor","realmId":"00000000-0000-7000-8000-000000000001"}`, constraint: "audit_events_service_attribution"},
			{name: "database contains request attribution", id: "00000000-0000-7000-8000-000000000056", version: 1, actor: "catalog_migration", details: `{"principalType":"database_login","currentUser":"catalog_migration","commandId":"00000000-0000-7000-8000-000000000001"}`, constraint: "audit_events_database_attribution"},
			{name: "invalid operator label", id: "00000000-0000-7000-8000-000000000057", version: 1, actor: "catalog_migration", details: `{"principalType":"database_login","currentUser":"catalog_migration","operatorLabel":"line\nbreak"}`, constraint: "audit_events_operator_label"},
			{name: "non-string optional attribution", id: "00000000-0000-7000-8000-000000000058", version: 1, actor: "catalog-api", details: `{"principalType":"service_executor","authorizationDigest":null}`, constraint: "audit_events_details_shape"},
			{name: "numeric database authority", id: "00000000-0000-7000-8000-000000000059", version: 1, actor: "catalog_migration", details: `{"principalType":"database_login","currentUser":123}`, constraint: "audit_events_details_shape"},
			{name: "numeric operator label", id: "00000000-0000-7000-8000-000000000060", version: 1, actor: "catalog_migration", details: `{"principalType":"database_login","currentUser":"catalog_migration","operatorLabel":123}`, constraint: "audit_events_details_shape"},
		}
		for _, invalid := range auditGuardCases {
			_, err := pool.Exec(ctx, `
INSERT INTO audit_events(format_version,id,tenant_id,actor,action,target_type,target_id,reason,request_id,after_digest,details,occurred_at)
VALUES ($1,$2,$3::uuid,$4,'namespace.created','namespace',$3::uuid::text,$5,$6,$7,$8::jsonb,clock_timestamp())`, invalid.version, invalid.id, tenantID, invalid.actor, invalid.reason, invalid.requestID, invalid.after, invalid.details)
			var postgresError *pgconn.PgError
			if !errors.As(err, &postgresError) || postgresError.Code != "23514" || postgresError.ConstraintName != invalid.constraint {
				t.Fatalf("invalid audit row %q returned %v", invalid.name, err)
			}
		}
		invalidRows := []struct {
			name                string
			key                 string
			authorizationDigest any
			executionToken      any
			releasedAt          any
			repairReason        any
		}{
			{name: "missing authorization digest", key: "missing-authorization", executionToken: "00000000-0000-7000-8000-000000000042"},
			{name: "released reservation retains execution token", key: "released-with-token", authorizationDigest: "sha256:" + strings.Repeat("a", 64), executionToken: "00000000-0000-7000-8000-000000000043", releasedAt: time.Now().UTC(), repairReason: "reviewed repair"},
			{name: "released reservation has no reason", key: "released-without-reason", authorizationDigest: "sha256:" + strings.Repeat("a", 64), releasedAt: time.Now().UTC()},
		}
		for _, invalid := range invalidRows {
			_, err := withTenantTx(ctx, pool, tenantID, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
				_, insertErr := tx.Exec(ctx, `
          INSERT INTO idempotency_keys(
            tenant_id,actor,route,idempotency_key,request_digest,expires_at,created_at,protocol_version,
            authorization_digest,execution_token,released_at,repair_reason
			) VALUES ($1,$2,$3,$4,$5,clock_timestamp()+interval '1 hour',clock_timestamp(),2,$6,$7,$8,$9)`,
					tenantID, "migration-test", "POST /api/v1/resources", invalid.key, "request-digest",
					invalid.authorizationDigest, invalid.executionToken, invalid.releasedAt, invalid.repairReason)
				return struct{}{}, insertErr
			})
			var postgresError *pgconn.PgError
			if !errors.As(err, &postgresError) || (postgresError.Code != "23514" && !(invalid.authorizationDigest == nil && postgresError.Code == "23502" && postgresError.ColumnName == "authorization_digest")) {
				t.Fatalf("invalid v2 row %q returned %v", invalid.name, err)
			}
		}
	})

	t.Run("unsupported development state is refused without mutation", func(t *testing.T) {
		schema := "catalog_migration_upgrade_" + suffix
		if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		defer func(cleanupCtx context.Context) {
			_, _ = adminPool.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		}(ctx)
		pool, err := Open(ctx, withSearchPath(t, baseDSN, schema+",public"), testPoolConfig(4))
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()

		entries, err := migrations.Files.ReadDir(".")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "000021" {
				continue
			}
			body, readErr := migrations.Files.ReadFile(entry.Name())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, err := pool.Exec(ctx, string(body)); err != nil {
				t.Fatal(err)
			}
			var version int
			if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err != nil {
				t.Fatal(err)
			}
			checksum := sha256.Sum256(body)
			if _, err := pool.Exec(ctx, `INSERT INTO catalog_schema_migrations(version,checksum) VALUES ($1,$2)`, version, fmt.Sprintf("%x", checksum)); err != nil {
				t.Fatal(err)
			}
		}
		const tenantID = "00000000-0000-7000-8000-000000000051"
		if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,display_name,created_at) VALUES ($1,'unsupported development fixture',clock_timestamp())`, tenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO idempotency_keys(tenant_id,actor,route,idempotency_key,request_digest,expires_at,created_at,protocol_version)
VALUES ($1,'old-writer','POST /test','old-key','old-digest',clock_timestamp()+interval '1 hour',clock_timestamp(),1)`, tenantID); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := applyMigrationsForTest(ctx, pool); err == nil || !strings.Contains(err.Error(), "unsupported pre-release idempotency state") {
				t.Fatalf("obsolete command migration returned %v", err)
			}
			var version, protocol int
			var digest string
			if err := pool.QueryRow(ctx, `SELECT (SELECT max(version) FROM catalog_schema_migrations),protocol_version,request_digest FROM idempotency_keys WHERE tenant_id=$1`, tenantID).Scan(&version, &protocol, &digest); err != nil || version != 20 || protocol != 1 || digest != "old-digest" {
				t.Fatalf("refusal changed retained state: schema=%d protocol=%d digest=%q err=%v", version, protocol, digest, err)
			}
		}

	})
	t.Run("unsupported inspection state is refused without mutation", func(t *testing.T) {
		schema := "catalog_migration_inspection_" + suffix
		if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		defer func(cleanupCtx context.Context) {
			_, _ = adminPool.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		}(ctx)
		pool, err := Open(ctx, withSearchPath(t, baseDSN, schema+",public"), testPoolConfig(4))
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()

		entries, err := migrations.Files.ReadDir(".")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "000022" {
				continue
			}
			body, readErr := migrations.Files.ReadFile(entry.Name())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, err := pool.Exec(ctx, string(body)); err != nil {
				t.Fatal(err)
			}
			var version int
			if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err != nil {
				t.Fatal(err)
			}
			checksum := sha256.Sum256(body)
			if _, err := pool.Exec(ctx, `INSERT INTO catalog_schema_migrations(version,checksum) VALUES ($1,$2)`, version, fmt.Sprintf("%x", checksum)); err != nil {
				t.Fatal(err)
			}
		}
		const tenantID = "00000000-0000-7000-8000-000000000051"
		if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,display_name,created_at) VALUES ($1,'unsupported development fixture',clock_timestamp())`, tenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO source_inspections(id,tenant_id,document,expires_at,created_at)
VALUES ('00000000-0000-7000-8000-000000000052',$1,'{"old":"untouched"}',clock_timestamp()+interval '1 hour',clock_timestamp())`, tenantID); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := applyMigrationsForTest(ctx, pool); err == nil || !strings.Contains(err.Error(), "unsupported pre-release inspection tracking") {
				t.Fatalf("obsolete inspection migration returned %v", err)
			}
			var version int
			var unchanged bool
			if err := pool.QueryRow(ctx, `SELECT (SELECT max(version) FROM catalog_schema_migrations),
cleanup_tracking_version IS NULL AND document='{"old":"untouched"}'::jsonb FROM source_inspections WHERE tenant_id=$1`, tenantID).Scan(&version, &unchanged); err != nil || version != 21 || !unchanged {
				t.Fatalf("refusal changed retained state: schema=%d unchanged=%t err=%v", version, unchanged, err)
			}
		}

	})
}

func TestPostgresDurabilityRLSLeasesAndStores(t *testing.T) {
	baseDSN := os.Getenv("CATALOG_TEST_DATABASE_DSN")
	if baseDSN == "" {
		t.Skip("CATALOG_TEST_DATABASE_DSN is not set")
	}
	ctx := context.Background()
	adminPool, err := Open(ctx, baseDSN, testPoolConfig(4))
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	schema := "catalog_test_" + suffix
	apiRole := "Catalog-API-test-" + suffix
	workerRole := "Catalog-Worker-test-" + suffix
	apiPassword := "catalog-api-test-" + suffix
	workerPassword := "catalog-worker-test-" + suffix
	parentRole := "catalog_runtime_parent_test_" + suffix
	unrelatedOwnerRole := "catalog_unrelated_owner_test_" + suffix
	const (
		tenantA        = "00000000-0000-7000-8000-000000000001"
		tenantB        = "00000000-0000-7000-8000-000000000002"
		identityIssuer = "https://identity.integration.example"
	)
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE EXTENSION vector WITH SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = adminPool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		_, _ = adminPool.Exec(context.Background(), `REVOKE `+pgx.Identifier{parentRole}.Sanitize()+` FROM `+pgx.Identifier{apiRole}.Sanitize())
		_, _ = adminPool.Exec(context.Background(), `REVOKE `+pgx.Identifier{apiRole}.Sanitize()+` FROM `+pgx.Identifier{parentRole}.Sanitize())
		_, _ = adminPool.Exec(context.Background(), `ALTER DEFAULT PRIVILEGES FOR ROLE `+pgx.Identifier{unrelatedOwnerRole}.Sanitize()+` REVOKE SELECT ON TABLES FROM PUBLIC`)
		_, _ = adminPool.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{parentRole}.Sanitize())
		_, _ = adminPool.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{unrelatedOwnerRole}.Sanitize())
		_, _ = adminPool.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{apiRole}.Sanitize())
		_, _ = adminPool.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{workerRole}.Sanitize())
	}()
	testDSN := withSearchPath(t, baseDSN, schema+",public")
	migrationPool, err := Open(ctx, testDSN, testPoolConfig(4))
	if err != nil {
		t.Fatal(err)
	}
	defer migrationPool.Close()
	if _, err := applyMigrationsForTest(ctx, migrationPool); err != nil {
		migrationPool.Close()
		t.Fatal(err)
	}
	requireCatalogColumnInventory(t, ctx, migrationPool, schema)
	assertMigrationLockAvailable := func(label string) {
		probe, acquireErr := adminPool.Acquire(ctx)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		defer probe.Release()
		var acquired bool
		if queryErr := probe.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, migrationAdvisoryLockKey).Scan(&acquired); queryErr != nil || !acquired {
			t.Fatalf("migration advisory lock was not released after %s: acquired=%v err=%v", label, acquired, queryErr)
		}
		if cleanupErr := releaseMigrationLock(ctx, probe.Conn()); cleanupErr != nil {
			t.Fatalf("migration advisory lock probe cleanup after %s: %v", label, cleanupErr)
		}
	}
	assertMigrationLockAvailable("Apply")
	func() {
		connection, acquireErr := migrationPool.Acquire(ctx)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		defer connection.Release()
		if _, lockErr := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLockKey); lockErr != nil {
			t.Fatal(lockErr)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if cleanupErr := releaseMigrationLock(canceled, connection.Conn()); cleanupErr != nil {
			t.Fatalf("release migration lock after caller cancellation: %v", cleanupErr)
		}
	}()
	assertMigrationLockAvailable("canceled cleanup")
	func() {
		connection, acquireErr := migrationPool.Acquire(ctx)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		defer connection.Release()
		if cleanupErr := releaseMigrationLock(ctx, connection.Conn()); cleanupErr == nil || !connection.Conn().IsClosed() {
			t.Fatalf("missing advisory lock cleanup returned %v, closed=%v", cleanupErr, connection.Conn().IsClosed())
		}
	}()
	plan, err := Plan(ctx, migrationPool)
	if err != nil || len(plan) != 47 || slices.ContainsFunc(plan, func(migration Migration) bool { return !migration.Applied }) {
		migrationPool.Close()
		t.Fatalf("migration plan = %#v, %v", plan, err)
	}
	provisionedA, err := ProvisionIdentity(ctx, migrationPool, IdentityProvisioningInput{
		TenantID:          tenantA,
		TenantDisplayName: "Integration tenant A",
		Issuer:            identityIssuer,
		TenancyMode:       "dedicated",
		Subject:           "admin-a",
		ActorType:         identity.ActorHuman,
		Roles:             []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor},
		Operator:          "integration-operator",
		Reason:            "  bootstrap the dedicated integration tenant  ",
		AcknowledgeOutbox: true,
	})
	if err != nil {
		migrationPool.Close()
		t.Fatal(err)
	}
	provisionedAReplay, err := ProvisionIdentity(ctx, migrationPool, IdentityProvisioningInput{
		TenantID:          tenantA,
		TenantDisplayName: "Integration tenant A",
		Issuer:            identityIssuer,
		TenancyMode:       "dedicated",
		Subject:           "admin-a",
		ActorType:         identity.ActorHuman,
		Roles:             []identity.Role{identity.RoleContributor, identity.RoleTenantAdmin},
		Operator:          "integration-operator",
		Reason:            "  bootstrap the dedicated integration tenant  ",
		AcknowledgeOutbox: true,
	})
	if err != nil || provisionedAReplay.RealmID != provisionedA.RealmID || provisionedAReplay.MembershipID != provisionedA.MembershipID || len(provisionedAReplay.Assignments) != 2 {
		migrationPool.Close()
		t.Fatalf("repeat identity provisioning = %#v, %v", provisionedAReplay, err)
	}
	for _, assignment := range provisionedAReplay.Assignments {
		if assignment.GrantReason != "bootstrap the dedicated integration tenant" {
			migrationPool.Close()
			t.Fatalf("normalized provisioning grant reason = %q", assignment.GrantReason)
		}
	}
	var provisioningAuditCount, normalizedProvisioningAuditCount int
	if err := migrationPool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE reason='bootstrap the dedicated integration tenant') FROM audit_events WHERE tenant_id=$1 AND action='identity.membership_provisioned' AND target_id=$2`, tenantA, provisionedA.MembershipID).Scan(&provisioningAuditCount, &normalizedProvisioningAuditCount); err != nil || provisioningAuditCount != 2 || normalizedProvisioningAuditCount != provisioningAuditCount {
		migrationPool.Close()
		t.Fatalf("normalized provisioning audit reasons = %d/%d, %v", normalizedProvisioningAuditCount, provisioningAuditCount, err)
	}
	provisionedB, err := ProvisionIdentity(ctx, migrationPool, IdentityProvisioningInput{
		TenantID:             tenantB,
		TenantDisplayName:    "Integration tenant B",
		Issuer:               identityIssuer,
		TenancyMode:          "shared",
		ExternalOrganization: "organization-b",
		Subject:              "admin-b",
		ActorType:            identity.ActorHuman,
		Roles:                []identity.Role{identity.RoleTenantAdmin},
		Operator:             "integration-operator",
		Reason:               "bootstrap the shared integration tenant",
		AcknowledgeOutbox:    true,
	})
	if err != nil {
		migrationPool.Close()
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
	reapplied, privilegeReports, err := ApplyAndReconcileRuntimePrivileges(ctx, migrationPool, apiRole, workerRole)
	if err != nil {
		migrationPool.Close()
		t.Fatal(err)
	}
	if len(reapplied) != 0 {
		migrationPool.Close()
		t.Fatalf("already-current combined migration reapplied %#v", reapplied)
	}
	if len(privilegeReports) != 2 || !privilegeReports[0].Compliant || !privilegeReports[1].Compliant {
		migrationPool.Close()
		t.Fatalf("runtime privilege reports = %#v", privilegeReports)
	}
	assertMigrationLockAvailable("ApplyAndReconcileRuntimePrivileges")
	t.Run("migration apply lock failures", func(t *testing.T) {
		testMigrationApplyLockFailures(t, migrationPool, adminPool, schema, apiRole, workerRole)
	})
	assertMigrationLockAvailable("failed migration apply")
	migrationPool.Close()
	singleConnectionMigrationPool, err := Open(ctx, testDSN, testPoolConfig(1))
	if err != nil {
		t.Fatal(err)
	}
	reapplied, privilegeReports, err = ApplyAndReconcileRuntimePrivileges(ctx, singleConnectionMigrationPool, apiRole, workerRole)
	singleConnectionMigrationPool.Close()
	if err != nil || len(reapplied) != 0 || len(privilegeReports) != 2 || !privilegeReports[0].Compliant || !privilegeReports[1].Compliant {
		t.Fatalf("single-connection combined migration = %#v / %#v, %v", reapplied, privilegeReports, err)
	}
	apiPool := rolePool(t, testDSN, apiRole, apiPassword)
	defer apiPool.Close()
	workerPool := rolePool(t, testDSN, workerRole, workerPassword)
	defer workerPool.Close()
	operatorPool, err := Open(ctx, testDSN, testPoolConfig(4))
	if err != nil {
		t.Fatal(err)
	}
	defer operatorPool.Close()
	apiAttributes, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI)
	if err != nil || apiAttributes.Name != apiRole || apiAttributes.BypassRLS {
		t.Fatalf("API role attributes = %#v, %v", apiAttributes, err)
	}
	workerAttributes, err := VerifyRuntimeRole(ctx, workerPool, RuntimeRoleWorker)
	if err != nil || workerAttributes.Name != workerRole || !workerAttributes.BypassRLS {
		t.Fatalf("worker role attributes = %#v, %v", workerAttributes, err)
	}
	var beforeRestoreVerificationRows int64
	if err := operatorPool.QueryRow(ctx, `SELECT sum(row_count) FROM (SELECT count(*) AS row_count FROM audit_events UNION ALL SELECT count(*) FROM tenants UNION ALL SELECT count(*) FROM outbox_events) retained`).Scan(&beforeRestoreVerificationRows); err != nil {
		t.Fatal(err)
	}
	restoreOptions := RestoreVerificationOptions{TenantID: tenantA, RecoveryPoint: "integration-current", APIRole: apiRole, WorkerRole: workerRole, Limit: 2}
	t.Run("restore verification cancellation during a read", func(t *testing.T) {
		testInterruptedRestoreRead(t, operatorPool, "tenants", func(readCtx context.Context) (any, error) {
			return VerifyRestoredCatalog(readCtx, operatorPool, restoreOptions)
		})
	})
	restorePages := 0
	for {
		report, verifyErr := VerifyRestoredCatalog(ctx, operatorPool, restoreOptions)
		if verifyErr != nil {
			t.Fatalf("verify current restored Catalog page %d: %v", restorePages+1, verifyErr)
		}
		restorePages++
		if report.TenantID != tenantA || report.SchemaVersion != 47 || report.PrivilegeManifestVersion != runtimePrivilegeManifestVersion || report.Integrity != restoreIntegrityValid || report.Processed > restoreOptions.Limit {
			t.Fatalf("restore verification report = %#v", report)
		}
		if report.Traversal == restoreTraversalDone {
			break
		}
		if report.Traversal != restoreTraversalMore || report.Continuation == "" {
			t.Fatalf("restore verification continuation report = %#v", report)
		}
		restoreOptions.Cursor = report.Continuation
		if restorePages > 32 {
			t.Fatal("restore verification did not complete within the bounded current fixture")
		}
	}
	if restorePages < 2 {
		t.Fatal("restore verification fixture did not exercise continuation")
	}
	cancelledRestoreContext, cancelRestore := context.WithCancel(ctx)
	cancelRestore()
	if _, err := VerifyRestoredCatalog(cancelledRestoreContext, operatorPool, RestoreVerificationOptions{TenantID: tenantA, RecoveryPoint: "integration-cancelled", APIRole: apiRole, WorkerRole: workerRole, Limit: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled restore verification returned %v", err)
	}
	assertRestoreSchemaFinding := func(t *testing.T, code string, mutate, restore func() error) {
		t.Helper()
		if err := mutate(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := restore(); err != nil {
				t.Errorf("restore schema after %s: %v", code, err)
			}
		}()
		options := RestoreVerificationOptions{TenantID: tenantA, RecoveryPoint: "integration-schema-drift", APIRole: apiRole, WorkerRole: workerRole, Limit: 2}
		report, err := VerifyRestoredCatalog(ctx, operatorPool, options)
		if err != nil || report.Integrity != restoreIntegrityInvalid || !slices.ContainsFunc(report.Findings, func(finding RestoreFinding) bool { return finding.Code == code }) {
			t.Fatalf("schema drift %s report = %#v, %v", code, report, err)
		}
	}
	t.Run("restore verifier detects disabled guard", func(t *testing.T) {
		assertRestoreSchemaFinding(t, "RESTORE_GUARD_INVENTORY_INVALID",
			func() error {
				_, err := operatorPool.Exec(ctx, `ALTER TABLE audit_events DISABLE TRIGGER audit_event_insert_guard`)
				return err
			},
			func() error {
				_, err := operatorPool.Exec(ctx, `ALTER TABLE audit_events ENABLE TRIGGER audit_event_insert_guard`)
				return err
			})
	})
	t.Run("restore verifier detects RLS policy drift", func(t *testing.T) {
		assertRestoreSchemaFinding(t, "RESTORE_RLS_POLICY_INVALID",
			func() error {
				_, err := operatorPool.Exec(ctx, `DROP POLICY tenant_isolation ON publishers`)
				return err
			},
			func() error {
				_, err := operatorPool.Exec(ctx, `CREATE POLICY tenant_isolation ON publishers USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)`)
				return err
			})
	})
	t.Run("restore verifier detects constraint drift", func(t *testing.T) {
		assertRestoreSchemaFinding(t, "RESTORE_CONSTRAINT_INVENTORY_INVALID",
			func() error {
				_, err := operatorPool.Exec(ctx, `ALTER TABLE source_snapshots DROP CONSTRAINT source_snapshots_member_count_check`)
				return err
			},
			func() error {
				_, err := operatorPool.Exec(ctx, `ALTER TABLE source_snapshots ADD CONSTRAINT source_snapshots_member_count_check CHECK (member_count >= 0)`)
				return err
			})
	})
	t.Run("restore verifier detects repair index drift", func(t *testing.T) {
		assertRestoreSchemaFinding(t, "RESTORE_SEARCH_REPAIR_INDEX_INVALID",
			func() error {
				_, err := operatorPool.Exec(ctx, `DROP INDEX observations_catalog_search_projection_missing`)
				return err
			},
			func() error {
				_, err := operatorPool.Exec(ctx, `CREATE INDEX observations_catalog_search_projection_missing ON observations (tenant_id,id) WHERE normalized_entry_digest IS NOT NULL AND (search_display_name IS NULL OR search_description IS NULL OR search_tags IS NULL OR search_representative_queries IS NULL OR search_publisher IS NULL OR search_vector IS NULL OR search_vector_version<>2 OR search_filter_version<>1)`)
				return err
			})
	})
	t.Run("restore verifier detects runtime role drift", func(t *testing.T) {
		assertRestoreSchemaFinding(t, "RESTORE_RUNTIME_ROLE_ATTRIBUTES_INVALID",
			func() error {
				_, err := adminPool.Exec(ctx, `ALTER ROLE `+pgx.Identifier{apiRole}.Sanitize()+` CREATEDB`)
				return err
			},
			func() error {
				_, err := adminPool.Exec(ctx, `ALTER ROLE `+pgx.Identifier{apiRole}.Sanitize()+` NOCREATEDB`)
				return err
			})
	})
	var afterRestoreVerificationRows int64
	if err := operatorPool.QueryRow(ctx, `SELECT sum(row_count) FROM (SELECT count(*) AS row_count FROM audit_events UNION ALL SELECT count(*) FROM tenants UNION ALL SELECT count(*) FROM outbox_events) retained`).Scan(&afterRestoreVerificationRows); err != nil || afterRestoreVerificationRows != beforeRestoreVerificationRows {
		t.Fatalf("read-only restore verification changed retained rows: before=%d after=%d err=%v", beforeRestoreVerificationRows, afterRestoreVerificationRows, err)
	}
	assumedAPIPool := assumedRolePool(t, testDSN, apiRole)
	defer assumedAPIPool.Close()
	if _, err := VerifyRuntimeRole(ctx, assumedAPIPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "authenticate directly") {
		t.Fatalf("schema-owner session assuming the API role returned %v", err)
	}
	if _, err := VerifyRuntimeRole(ctx, adminPool, RuntimeRoleAPI); err == nil {
		t.Fatal("superuser database identity was accepted as an API role")
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleWorker); err == nil {
		t.Fatal("NOBYPASSRLS database identity was accepted as a worker role")
	}
	var deniedCount int
	requireInsufficientPrivilege(t, apiPool.QueryRow(ctx, `SELECT count(*) FROM catalog_schema_migrations`).Scan(&deniedCount))
	requireInsufficientPrivilege(t, workerPool.QueryRow(ctx, `SELECT count(*) FROM tenant_memberships`).Scan(&deniedCount))
	requireInsufficientPrivilege(t, apiPool.QueryRow(ctx, `SELECT vector_dims('[1,2]'::vector)`).Scan(&deniedCount))
	requireInsufficientPrivilege(t, workerPool.QueryRow(ctx, `SELECT vector_dims('[1,2]'::vector)`).Scan(&deniedCount))
	if _, err := apiPool.Exec(ctx, `CREATE TABLE runtime_role_must_not_create(id integer)`); err == nil {
		t.Fatal("API role created an object in the Catalog schema")
	} else {
		requireInsufficientPrivilege(t, err)
	}
	if _, err := operatorPool.Exec(ctx, `CREATE TABLE future_runtime_table(id integer)`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `CREATE VIEW future_runtime_view AS SELECT id FROM future_runtime_table`); err != nil {
		t.Fatal(err)
	}
	requireInsufficientPrivilege(t, apiPool.QueryRow(ctx, `SELECT count(*) FROM future_runtime_table`).Scan(&deniedCount))
	requireInsufficientPrivilege(t, workerPool.QueryRow(ctx, `SELECT count(*) FROM future_runtime_table`).Scan(&deniedCount))
	if _, err := operatorPool.Exec(ctx, `CREATE FUNCTION future_runtime_probe() RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT 1'`); err != nil {
		t.Fatal(err)
	}
	requireInsufficientPrivilege(t, apiPool.QueryRow(ctx, `SELECT future_runtime_probe()`).Scan(&deniedCount))
	requireInsufficientPrivilege(t, workerPool.QueryRow(ctx, `SELECT future_runtime_probe()`).Scan(&deniedCount))
	if _, err := operatorPool.Exec(ctx, `CREATE SEQUENCE future_runtime_sequence`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT USAGE ON SEQUENCE future_runtime_sequence TO `+pgx.Identifier{apiRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected privilege on sequence") {
		t.Fatalf("unreconciled future sequence verification returned %v", err)
	}
	privilegeReports, err = reconcileRuntimePrivilegesForTest(ctx, operatorPool, apiRole, workerRole)
	if err != nil || len(privilegeReports) != 2 || !privilegeReports[0].Compliant || !privilegeReports[1].Compliant {
		t.Fatalf("future-object privilege reconciliation = %#v, %v", privilegeReports, err)
	}
	requireInsufficientPrivilege(t, apiPool.QueryRow(ctx, `SELECT future_runtime_probe()`).Scan(&deniedCount))
	requireInsufficientPrivilege(t, workerPool.QueryRow(ctx, `SELECT future_runtime_probe()`).Scan(&deniedCount))
	requireInsufficientPrivilege(t, apiPool.QueryRow(ctx, `SELECT nextval('future_runtime_sequence')`).Scan(&deniedCount))
	if _, err := operatorPool.Exec(ctx, `GRANT SELECT ON TABLE catalog_schema_migrations TO `+pgx.Identifier{apiRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected SELECT") {
		t.Fatalf("unexpected API grant verification returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `REVOKE SELECT ON TABLE catalog_schema_migrations FROM `+pgx.Identifier{apiRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT INSERT ON TABLE namespaces TO `+pgx.Identifier{apiRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected table INSERT on namespaces") {
		t.Fatalf("table-wide API insert verification returned %v", err)
	}
	privilegeReports, err = reconcileRuntimePrivilegesForTest(ctx, operatorPool, apiRole, workerRole)
	if err != nil || len(privilegeReports) != 2 || !privilegeReports[0].Compliant || !privilegeReports[1].Compliant {
		t.Fatalf("table-wide grant reconciliation = %#v, %v", privilegeReports, err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT SELECT ON TABLE tenants TO `+pgx.Identifier{apiRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected table SELECT on tenants") {
		t.Fatalf("table-wide API select verification returned %v", err)
	}
	privilegeReports, err = reconcileRuntimePrivilegesForTest(ctx, operatorPool, apiRole, workerRole)
	if err != nil || len(privilegeReports) != 2 || !privilegeReports[0].Compliant || !privilegeReports[1].Compliant {
		t.Fatalf("table-wide select reconciliation = %#v, %v", privilegeReports, err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT SELECT (id) ON TABLE tenants TO `+pgx.Identifier{apiRole}.Sanitize()+` WITH GRANT OPTION`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected grant option on table tenants") {
		t.Fatalf("API column grant-option verification returned %v", err)
	}
	privilegeReports, err = reconcileRuntimePrivilegesForTest(ctx, operatorPool, apiRole, workerRole)
	if err != nil || len(privilegeReports) != 2 || !privilegeReports[0].Compliant || !privilegeReports[1].Compliant {
		t.Fatalf("grant-option reconciliation = %#v, %v", privilegeReports, err)
	}
	if _, err := operatorPool.Exec(ctx, `ALTER TABLE tenants ADD COLUMN future_private_value text`); err != nil {
		t.Fatal(err)
	}
	requireInsufficientPrivilege(t, apiPool.QueryRow(ctx, `SELECT future_private_value FROM tenants LIMIT 1`).Scan(new(string)))
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err != nil {
		t.Fatalf("unlisted future column was not fail-closed: %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `ALTER TABLE tenants DROP COLUMN future_private_value`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `ALTER TABLE tenants RENAME COLUMN display_name TO missing_display_name`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "missing Catalog column tenants.display_name") {
		t.Fatalf("missing required column verification returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `ALTER TABLE tenants RENAME COLUMN missing_display_name TO display_name`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT EXECUTE ON FUNCTION future_runtime_probe() TO `+pgx.Identifier{workerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, workerPool, RuntimeRoleWorker); err == nil || !strings.Contains(err.Error(), "unexpected EXECUTE") {
		t.Fatalf("unexpected worker function grant verification returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `REVOKE EXECUTE ON FUNCTION future_runtime_probe() FROM `+pgx.Identifier{workerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT SELECT ON TABLE future_runtime_view TO `+pgx.Identifier{workerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, workerPool, RuntimeRoleWorker); err == nil || !strings.Contains(err.Error(), "unexpected SELECT on future_runtime_view.id") {
		t.Fatalf("unexpected worker view grant verification returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `REVOKE SELECT ON TABLE future_runtime_view FROM `+pgx.Identifier{workerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT USAGE ON SCHEMA `+pgx.Identifier{schema}.Sanitize()+` TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected PUBLIC privilege on Catalog schema") {
		t.Fatalf("PUBLIC Catalog schema grant verification returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `REVOKE USAGE ON SCHEMA `+pgx.Identifier{schema}.Sanitize()+` FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT SELECT ON TABLE tenants TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected PUBLIC privilege on table tenants") {
		t.Fatalf("PUBLIC Catalog table grant verification returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `REVOKE SELECT ON TABLE tenants FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT EXECUTE ON FUNCTION catalog_core_lexical_query_v1(text) TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected PUBLIC EXECUTE on catalog_core_lexical_query_v1(text)") {
		t.Fatalf("PUBLIC Catalog function grant verification returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `REVOKE EXECUTE ON FUNCTION catalog_core_lexical_query_v1(text) FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT EXECUTE ON FUNCTION digest(bytea,text) TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, workerPool, RuntimeRoleWorker); err == nil || !strings.Contains(err.Error(), "unexpected PUBLIC EXECUTE on pgcrypto function digest(bytea, text)") {
		t.Fatalf("PUBLIC pgcrypto function grant verification returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `REVOKE EXECUTE ON FUNCTION digest(bytea,text) FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `GRANT EXECUTE ON FUNCTION vector_dims(vector) TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected PUBLIC EXECUTE on vector function vector_dims(vector)") {
		t.Fatalf("PUBLIC pgvector function grant verification returned %v", err)
	}
	privilegeReports, err = reconcileRuntimePrivilegesForTest(ctx, operatorPool, apiRole, workerRole)
	if err != nil || len(privilegeReports) != 2 || !privilegeReports[0].Compliant || !privilegeReports[1].Compliant {
		t.Fatalf("pgvector PUBLIC grant reconciliation = %#v, %v", privilegeReports, err)
	}
	requireInsufficientPrivilege(t, apiPool.QueryRow(ctx, `SELECT vector_dims('[1,2]'::vector)`).Scan(&deniedCount))
	if _, err := operatorPool.Exec(ctx, `GRANT EXECUTE ON FUNCTION vector_dims(vector) TO `+pgx.Identifier{apiRole}.Sanitize()+` WITH GRANT OPTION`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected grant option on vector function vector_dims(vector)") {
		t.Fatalf("pgvector function grant-option verification returned %v", err)
	}
	privilegeReports, err = reconcileRuntimePrivilegesForTest(ctx, operatorPool, apiRole, workerRole)
	if err != nil || len(privilegeReports) != 2 || !privilegeReports[0].Compliant || !privilegeReports[1].Compliant {
		t.Fatalf("pgvector grant-option reconciliation = %#v, %v", privilegeReports, err)
	}
	requireInsufficientPrivilege(t, apiPool.QueryRow(ctx, `SELECT vector_dims('[1,2]'::vector)`).Scan(&deniedCount))
	if _, err := adminPool.Exec(ctx, `CREATE ROLE `+pgx.Identifier{parentRole}.Sanitize()+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	hiddenExtensionFunctions, err := inspectExtensionFunctionPrivileges(ctx, operatorPool, parentRole, parentRole)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(hiddenExtensionFunctions, func(function extensionFunctionPrivilege) bool { return function.extension == "vector" }) {
		t.Fatal("extension inspection included pgvector from a schema the inspected role cannot use")
	}
	if _, err := adminPool.Exec(ctx, `GRANT `+pgx.Identifier{parentRole}.Sanitize()+` TO `+pgx.Identifier{apiRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "must not participate") {
		t.Fatalf("API role membership verification returned %v", err)
	}
	if _, err := adminPool.Exec(ctx, `REVOKE `+pgx.Identifier{parentRole}.Sanitize()+` FROM `+pgx.Identifier{apiRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `GRANT `+pgx.Identifier{apiRole}.Sanitize()+` TO `+pgx.Identifier{parentRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "must not participate") {
		t.Fatalf("API role delegation verification returned %v", err)
	}
	if _, err := adminPool.Exec(ctx, `REVOKE `+pgx.Identifier{apiRole}.Sanitize()+` FROM `+pgx.Identifier{parentRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `DROP ROLE `+pgx.Identifier{parentRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err != nil {
		t.Fatalf("restored API privilege verification returned %v", err)
	}
	if _, err := VerifyRuntimeRole(ctx, workerPool, RuntimeRoleWorker); err != nil {
		t.Fatalf("restored worker privilege verification returned %v", err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE ROLE `+pgx.Identifier{unrelatedOwnerRole}.Sanitize()+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `ALTER DEFAULT PRIVILEGES FOR ROLE `+pgx.Identifier{unrelatedOwnerRole}.Sanitize()+` GRANT SELECT ON TABLES TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err != nil {
		t.Fatalf("unrelated owner's default privileges affected Catalog API verification: %v", err)
	}
	if _, err := VerifyRuntimeRole(ctx, workerPool, RuntimeRoleWorker); err != nil {
		t.Fatalf("unrelated owner's default privileges affected Catalog worker verification: %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `ALTER DEFAULT PRIVILEGES GRANT SELECT ON TABLES TO `+pgx.Identifier{apiRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, apiPool, RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "unexpected future-object default privileges") {
		t.Fatalf("Catalog migration-owner table default drift verification returned %v", err)
	}
	privilegeReports, err = reconcileRuntimePrivilegesForTest(ctx, operatorPool, apiRole, workerRole)
	if err != nil || len(privilegeReports) != 2 || !privilegeReports[0].Compliant || !privilegeReports[1].Compliant {
		t.Fatalf("table default-privilege reconciliation = %#v, %v", privilegeReports, err)
	}
	if _, err := operatorPool.Exec(ctx, `ALTER DEFAULT PRIVILEGES GRANT EXECUTE ON FUNCTIONS TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeRole(ctx, workerPool, RuntimeRoleWorker); err == nil || !strings.Contains(err.Error(), "unexpected future-object default privileges") {
		t.Fatalf("Catalog migration-owner built-in function default drift verification returned %v", err)
	}
	privilegeReports, err = reconcileRuntimePrivilegesForTest(ctx, operatorPool, apiRole, workerRole)
	if err != nil || len(privilegeReports) != 2 || !privilegeReports[0].Compliant || !privilegeReports[1].Compliant {
		t.Fatalf("function default-privilege reconciliation = %#v, %v", privilegeReports, err)
	}
	if _, err := workerPool.Exec(ctx, `SELECT catalog_core_lexical_query_v1('worker must not parse searches')`); err == nil {
		t.Fatal("worker role executed the API-only lexical query function")
	}
	if _, err := workerPool.Exec(ctx, `SELECT catalog_core_lexical_vector_v1('worker ingestion')`); err != nil {
		t.Fatalf("worker role could not execute its ingestion lexical function: %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `DROP FUNCTION future_runtime_probe()`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `DROP VIEW future_runtime_view`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `DROP TABLE future_runtime_table`); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `DROP SEQUENCE future_runtime_sequence`); err != nil {
		t.Fatal(err)
	}
	var migrationRole string
	if err := operatorPool.QueryRow(ctx, `SELECT current_user`).Scan(&migrationRole); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeRoleBindings(ctx, operatorPool, migrationRole, workerRole); err == nil || !strings.Contains(err.Error(), "distinct from the migration role") {
		t.Fatalf("migration identity accepted as an application role: %v", err)
	}
	apiRepository, err := NewPostgresRepository(apiPool)
	if err != nil {
		t.Fatal(err)
	}
	workerRepository, err := NewPostgresRepository(workerPool)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	generator := id.Generator{}
	createNamespace := func(tenantID, slug string) catalog.Namespace {
		namespaceID, idErr := generator.New(now)
		if idErr != nil {
			t.Fatal(idErr)
		}
		auditID, _ := generator.New(now.Add(time.Nanosecond))
		outboxID, _ := generator.New(now.Add(2 * time.Nanosecond))
		namespace := catalog.Namespace{ID: namespaceID, TenantID: tenantID, Slug: slug, DisplayName: strings.ToUpper(slug), Revision: 1, CreatedAt: now, UpdatedAt: now}
		audit := integrationAudit(t, auditID, tenantID, "test", "namespace.created", "namespace", namespaceID, "", now)
		outbox := integrationOutbox(t, outboxID, audit)
		created, createErr := apiRepository.CreateNamespace(ctx, namespace, audit, outbox)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return created
	}
	namespaceA := createNamespace(tenantA, "alpha")
	namespaceB := createNamespace(tenantB, "beta")
	if _, err := ProvisionIdentity(ctx, apiPool, IdentityProvisioningInput{
		TenantID:          "00000000-0000-7000-8000-000000000003",
		TenantDisplayName: "Unauthorized tenant",
		Issuer:            identityIssuer,
		TenancyMode:       "dedicated",
		Subject:           "unauthorized-admin",
		ActorType:         identity.ActorHuman,
		Roles:             []identity.Role{identity.RoleTenantAdmin},
		Operator:          "api-role",
		Reason:            "this must be rejected",
	}); err == nil {
		t.Fatal("least-privilege API role was allowed to provision an identity realm")
	}
	identityStore, err := NewPostgresIdentityStore(apiRepository)
	if err != nil {
		t.Fatal(err)
	}
	dedicatedActor, err := identityStore.Resolve(ctx, identity.Principal{
		Issuer:   identityIssuer,
		Subject:  provisionedA.Subject,
		TenantID: tenantA,
		Claims:   map[string]string{"scope": "catalog.read", "roles": "publisher,security_reviewer"},
	})
	if err != nil || dedicatedActor.TenantID != tenantA || dedicatedActor.RealmID != provisionedA.RealmID || dedicatedActor.MembershipID != provisionedA.MembershipID || dedicatedActor.ActorType != identity.ActorHuman || len(dedicatedActor.Roles) != 2 || dedicatedActor.Roles[0] != identity.RoleContributor || dedicatedActor.Roles[1] != identity.RoleTenantAdmin {
		t.Fatalf("dedicated identity resolution = %#v, %v", dedicatedActor, err)
	}
	sharedActor, err := identityStore.Resolve(ctx, identity.Principal{
		Issuer:               identityIssuer,
		Subject:              provisionedB.Subject,
		ExternalOrganization: "organization-b",
	})
	if err != nil || sharedActor.TenantID != tenantB || sharedActor.RealmID != provisionedB.RealmID || sharedActor.MembershipID != provisionedB.MembershipID || len(sharedActor.Roles) != 1 || sharedActor.Roles[0] != identity.RoleTenantAdmin {
		t.Fatalf("shared identity resolution = %#v, %v", sharedActor, err)
	}
	realmsA, err := ListIdentityRealms(ctx, operatorPool, tenantA, "", 1)
	if err != nil || len(realmsA.Items) != 1 || realmsA.Total != 1 || realmsA.Items[0].ID != provisionedA.RealmID || realmsA.NextAfter != "" {
		t.Fatalf("tenant A identity realms = %#v, %v", realmsA, err)
	}
	if _, err := ListIdentityRealms(ctx, apiPool, tenantA, "", 10); err == nil {
		t.Fatal("least-privilege API role listed operator-owned identity realms")
	}
	if _, err := TransitionIdentityRealm(ctx, operatorPool, IdentityRealmTransitionInput{TenantID: tenantA, RealmID: provisionedA.RealmID, ExpectedRevision: 1, TargetState: identity.RealmSuspended, Operator: "integration-operator", Reason: "unsafe\nreason", TenantDiscoveryDisabled: true, AcknowledgeOutbox: true}); err == nil || !strings.Contains(err.Error(), "reason is invalid") {
		t.Fatalf("unsafe identity realm reason returned %v", err)
	}
	if _, err := TransitionIdentityRealm(ctx, operatorPool, IdentityRealmTransitionInput{TenantID: tenantA, RealmID: provisionedA.RealmID, ExpectedRevision: 1, TargetState: identity.RealmSuspended, Operator: "integration-operator", Reason: "exercise safe tenant shutdown", TenantDiscoveryDisabled: false, AcknowledgeOutbox: true}); err == nil {
		t.Fatal("final identity realm suspension ignored enabled discovery configuration")
	}
	suspendedRealm, err := TransitionIdentityRealm(ctx, operatorPool, IdentityRealmTransitionInput{TenantID: tenantA, RealmID: provisionedA.RealmID, ExpectedRevision: 1, TargetState: identity.RealmSuspended, Operator: "integration-operator", Reason: "  exercise safe tenant shutdown  ", TenantDiscoveryDisabled: true, AcknowledgeOutbox: true})
	if err != nil || suspendedRealm.Realm.State != identity.RealmSuspended || suspendedRealm.Realm.Revision != 2 || !suspendedRealm.TenantAccessSuspended {
		t.Fatalf("suspended identity realm = %#v, %v", suspendedRealm, err)
	}
	var suspendedRealmReason string
	if err := operatorPool.QueryRow(ctx, `SELECT reason FROM audit_events WHERE tenant_id=$1 AND action='identity.realm.suspended' AND target_id=$2 ORDER BY occurred_at DESC,id DESC LIMIT 1`, tenantA, provisionedA.RealmID).Scan(&suspendedRealmReason); err != nil || suspendedRealmReason != "exercise safe tenant shutdown" {
		t.Fatalf("normalized realm suspension reason = %q, %v", suspendedRealmReason, err)
	}
	if _, err := identityStore.Resolve(ctx, identity.Principal{Issuer: identityIssuer, Subject: provisionedA.Subject, TenantID: tenantA}); identity.ErrorCodeOf(err) != identity.CodeUnauthenticated {
		t.Fatalf("suspended identity realm resolved: %v", err)
	}
	suspendedPublicationStore, err := NewPostgresPublicationStore(apiRepository, "suspended.example.com")
	if err != nil {
		t.Fatal(err)
	}
	suspendedCandidate := publication.Snapshot{ID: "00000000-0000-7000-8000-000000000901", TenantID: tenantA, Host: "suspended.example.com", Audience: publication.AudienceAnonymous, Generation: 1, ARDProfile: "ard-test", AICatalogProfile: "ai-test", State: publication.SnapshotCandidate, ARD: publication.ARDManifest{Entries: []publication.Entry{}}, AICatalog: publication.AICatalog{SpecVersion: "1.0", Entries: []publication.AICatalogEntry{}}, ETag: `"suspended-one"`, AICatalogETag: `"suspended-two"`, CreatedAt: now}
	if _, err := suspendedPublicationStore.SaveCandidate(ctx, suspendedCandidate); err != nil {
		t.Fatal(err)
	}
	if _, err := suspendedPublicationStore.Activate(ctx, tenantA, suspendedCandidate.ID, now); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
		t.Fatalf("publication activated while the final identity realm was suspended: %v", err)
	}
	reactivatedRealm, err := TransitionIdentityRealm(ctx, operatorPool, IdentityRealmTransitionInput{TenantID: tenantA, RealmID: provisionedA.RealmID, ExpectedRevision: 2, TargetState: identity.RealmActive, Operator: "integration-operator", Reason: "restore tenant access", TenantDiscoveryDisabled: true, AcknowledgeOutbox: true})
	if err != nil || reactivatedRealm.Realm.State != identity.RealmActive || reactivatedRealm.Realm.Revision != 3 || reactivatedRealm.TenantAccessSuspended {
		t.Fatalf("reactivated identity realm = %#v, %v", reactivatedRealm, err)
	}
	dedicatedActor, err = identityStore.Resolve(ctx, identity.Principal{Issuer: identityIssuer, Subject: provisionedA.Subject, TenantID: tenantA})
	if err != nil || !dedicatedActor.HasRole(identity.RoleTenantAdmin, "") {
		t.Fatalf("reactivated realm identity = %#v, %v", dedicatedActor, err)
	}
	operatorEventRows, err := operatorPool.Query(ctx, `
		SELECT id::text,tenant_id::text,event_type,subject,data,created_at
		  FROM outbox_events
		 WHERE tenant_id=$1 AND event_type=ANY($2::text[])
		 ORDER BY created_at,id`, tenantA, []string{
		catalogevents.CanonicalType("identity.membership.provisioned"),
		catalogevents.CanonicalType("identity.realm.suspended"),
		catalogevents.CanonicalType("identity.realm.reactivated"),
	})
	if err != nil {
		t.Fatal(err)
	}
	seenOperatorTypes := make(map[string]bool)
	for operatorEventRows.Next() {
		var eventID, eventTenantID, eventType, subject string
		var data []byte
		var createdAt time.Time
		if err := operatorEventRows.Scan(&eventID, &eventTenantID, &eventType, &subject, &data, &createdAt); err != nil {
			operatorEventRows.Close()
			t.Fatal(err)
		}
		var reference catalogevents.NotificationReference
		var fields map[string]any
		if err := json.Unmarshal(data, &reference); err != nil {
			operatorEventRows.Close()
			t.Fatalf("decode operator event %s: %v", eventType, err)
		}
		if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 3 || catalogevents.CanonicalType(reference.Action) != eventType || reference.TargetID != subject {
			operatorEventRows.Close()
			t.Fatalf("operator event %s has an unsafe reference payload: %s", eventType, data)
		}
		if _, err := catalogevents.Reference(reference.Action, reference.TargetType, reference.TargetID, reference.Digest); err != nil {
			operatorEventRows.Close()
			t.Fatalf("operator event %s has an invalid reference payload: %v", eventType, err)
		}
		cloudEvent, err := catalogevents.MarshalCloudEvent(eventID, eventTenantID, eventType, subject, createdAt, data)
		if err != nil {
			operatorEventRows.Close()
			t.Fatalf("operator event %s does not match the public schema: %v", eventType, err)
		}
		if err := catalogevents.Validate(cloudEvent); err != nil {
			operatorEventRows.Close()
			t.Fatalf("validate operator event %s: %v", eventType, err)
		}
		seenOperatorTypes[eventType] = true
	}
	if err := operatorEventRows.Err(); err != nil {
		operatorEventRows.Close()
		t.Fatal(err)
	}
	operatorEventRows.Close()
	for _, eventType := range []string{
		catalogevents.CanonicalType("identity.membership.provisioned"),
		catalogevents.CanonicalType("identity.realm.suspended"),
		catalogevents.CanonicalType("identity.realm.reactivated"),
	} {
		if !seenOperatorTypes[eventType] {
			t.Errorf("operator event %s was not persisted", eventType)
		}
	}
	for name, principal := range map[string]identity.Principal{
		"unknown dedicated tenant":    {Issuer: identityIssuer, Subject: provisionedA.Subject, TenantID: tenantB},
		"unknown shared organization": {Issuer: identityIssuer, Subject: provisionedB.Subject, ExternalOrganization: "organization-unknown"},
		"unknown member":              {Issuer: identityIssuer, Subject: "unknown-member", TenantID: tenantA},
		"ambiguous tenant selector":   {Issuer: identityIssuer, Subject: provisionedA.Subject, TenantID: tenantA, ExternalOrganization: "organization-b"},
	} {
		if _, err := identityStore.Resolve(ctx, principal); identity.ErrorCodeOf(err) != identity.CodeUnauthenticated {
			t.Fatalf("%s identity resolution returned %v", name, err)
		}
	}
	identityCursorKey := sha256.Sum256([]byte("postgres-identity-test-key"))
	identityClock := clock.NewFake(now)
	identityService, err := identity.NewService(identity.ServiceOptions{Store: identityStore, Clock: identityClock, IDs: generator, CursorKey: identityCursorKey[:], AcknowledgeOutboxAtCreation: true})
	if err != nil {
		t.Fatal(err)
	}
	dedicatedAdminContext := identity.WithActor(ctx, dedicatedActor)
	serviceAssignment, err := identityService.GrantRole(dedicatedAdminContext, identity.GrantRoleInput{
		Subject:     "integration-service",
		ActorType:   identity.ActorService,
		Role:        identity.RoleContributor,
		NamespaceID: namespaceA.ID,
		Reason:      "allow the integration service to author in alpha",
	}, "identity-integration-grant")
	if err != nil {
		t.Fatal(err)
	}
	serviceActor, err := identityStore.Resolve(ctx, identity.Principal{Issuer: identityIssuer, Subject: "integration-service", TenantID: tenantA})
	if err != nil || serviceActor.ActorType != identity.ActorService || len(serviceActor.Roles) != 0 || len(serviceActor.Namespaces[namespaceA.ID]) != 1 || serviceActor.Namespaces[namespaceA.ID][0] != identity.RoleContributor {
		t.Fatalf("namespace-scoped service identity = %#v, %v", serviceActor, err)
	}
	firstMembershipPage, err := identityService.ListMemberships(dedicatedAdminContext, 1, "")
	if err != nil || len(firstMembershipPage.Items) != 1 || firstMembershipPage.NextCursor == "" {
		t.Fatalf("first PostgreSQL membership page = %#v, %v", firstMembershipPage, err)
	}
	secondMembershipPage, err := identityService.ListMemberships(dedicatedAdminContext, 1, firstMembershipPage.NextCursor)
	if err != nil || len(secondMembershipPage.Items) != 1 || secondMembershipPage.NextCursor != "" {
		t.Fatalf("second PostgreSQL membership page = %#v, %v", secondMembershipPage, err)
	}
	serviceMembership, err := identityService.GetMembership(dedicatedAdminContext, serviceActor.MembershipID)
	if err != nil || serviceMembership.Subject != "integration-service" {
		t.Fatalf("PostgreSQL membership read = %#v, %v", serviceMembership, err)
	}
	if _, err := identityService.GetMembership(dedicatedAdminContext, provisionedB.MembershipID); identity.ErrorCodeOf(err) != identity.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL membership read returned %v", err)
	}
	// A corrected wall clock must not trip the database's immutable-history
	// guard. Only the injected service clock changes, never the host clock.
	identityClock.Advance(-2 * time.Second)
	suspendedMembership, err := identityService.SuspendMembership(dedicatedAdminContext, serviceMembership.ID, "temporary integration hold", "identity-membership-suspend", serviceMembership.Revision)
	if err != nil || suspendedMembership.Membership.State != identity.MembershipSuspended {
		t.Fatalf("suspended PostgreSQL membership = %#v, %v", suspendedMembership, err)
	}
	if !suspendedMembership.Membership.UpdatedAt.Equal(serviceMembership.UpdatedAt) {
		t.Fatal("membership suspension did not retain its stored timestamp floor")
	}
	if _, err := identityStore.Resolve(ctx, identity.Principal{Issuer: identityIssuer, Subject: "integration-service", TenantID: tenantA}); identity.ErrorCodeOf(err) != identity.CodeUnauthenticated {
		t.Fatalf("suspended PostgreSQL membership resolved: %v", err)
	}
	reactivatedMembership, err := identityService.ReactivateMembership(dedicatedAdminContext, serviceMembership.ID, "restore integration access", "identity-membership-reactivate", suspendedMembership.Membership.Revision)
	if err != nil || reactivatedMembership.Membership.State != identity.MembershipActive {
		t.Fatalf("reactivated PostgreSQL membership = %#v, %v", reactivatedMembership, err)
	}
	if !reactivatedMembership.Membership.UpdatedAt.Equal(serviceMembership.UpdatedAt) {
		t.Fatal("membership reactivation did not retain its stored timestamp floor")
	}
	assignmentsA, err := identityService.ListRoleAssignments(dedicatedAdminContext, identity.RoleAssignmentListRequest{Limit: 100})
	if err != nil || len(assignmentsA) != 3 {
		t.Fatalf("tenant A role assignments = %#v, %v", assignmentsA, err)
	}
	firstAssignmentPage, err := identityStore.ListRoleAssignments(ctx, tenantA, identity.RoleAssignmentListRequest{Limit: 2})
	if err != nil || len(firstAssignmentPage) == 0 {
		t.Fatalf("assignment fixture = %#v, %v", firstAssignmentPage, err)
	}
	selectedMember := firstAssignmentPage[0].MembershipID
	memberAssignments, memberErr := identityStore.ListRoleAssignments(ctx, tenantA, identity.RoleAssignmentListRequest{MembershipID: selectedMember, Limit: 2})
	if memberErr != nil || len(memberAssignments) == 0 || len(memberAssignments) > 2 {
		t.Fatalf("member assignment page = %#v, %v", memberAssignments, memberErr)
	}
	for _, assignment := range memberAssignments {
		if assignment.MembershipID != selectedMember {
			t.Fatal("member filter leaked another membership")
		}
	}
	otherAssignments, memberErr := identityStore.ListRoleAssignments(ctx, tenantB, identity.RoleAssignmentListRequest{MembershipID: selectedMember, Limit: 2})
	if memberErr != nil || len(otherAssignments) != 0 {
		t.Fatalf("cross-tenant member filter = %#v, %v", otherAssignments, memberErr)
	}
	if err != nil || len(firstAssignmentPage) != 2 {
		t.Fatalf("first PostgreSQL role-assignment page = %#v, %v", firstAssignmentPage, err)
	}
	assignmentPosition := firstAssignmentPage[len(firstAssignmentPage)-1]
	secondAssignmentPage, err := identityStore.ListRoleAssignments(ctx, tenantA, identity.RoleAssignmentListRequest{AfterSubject: assignmentPosition.Subject, AfterNamespaceID: assignmentPosition.NamespaceID, AfterRole: assignmentPosition.Role, AfterID: assignmentPosition.ID, Limit: 2})
	if err != nil || len(secondAssignmentPage) != 1 || secondAssignmentPage[0].ID == firstAssignmentPage[0].ID || secondAssignmentPage[0].ID == firstAssignmentPage[1].ID {
		t.Fatalf("second PostgreSQL role-assignment page = %#v, %v", secondAssignmentPage, err)
	}
	assignmentPosition = secondAssignmentPage[0]
	finalAssignmentPage, err := identityStore.ListRoleAssignments(ctx, tenantA, identity.RoleAssignmentListRequest{AfterSubject: assignmentPosition.Subject, AfterNamespaceID: assignmentPosition.NamespaceID, AfterRole: assignmentPosition.Role, AfterID: assignmentPosition.ID, Limit: 2})
	if err != nil || len(finalAssignmentPage) != 0 {
		t.Fatalf("final PostgreSQL role-assignment page = %#v, %v", finalAssignmentPage, err)
	}
	assignmentsB, err := identityService.ListRoleAssignments(identity.WithActor(ctx, sharedActor), identity.RoleAssignmentListRequest{Limit: 100})
	if err != nil || len(assignmentsB) != 1 || assignmentsB[0].TenantID != tenantB {
		t.Fatalf("tenant B role assignments = %#v, %v", assignmentsB, err)
	}
	identityClock.Advance(2 * time.Second)
	if _, err := identityService.RevokeRole(dedicatedAdminContext, serviceAssignment.ID, "remove the integration namespace grant", "identity-integration-revoke", serviceAssignment.Revision); err != nil {
		t.Fatal(err)
	}
	identityClock.Advance(-2 * time.Second)
	// Restoring a revoked namespace grant must also tolerate a clock backstep.
	// The retained namespace membership is reactivated, not replaced or backdated.
	var namespaceMembershipID string
	var namespaceCreatedAt time.Time
	if err := operatorPool.QueryRow(ctx, `SELECT id::text,created_at FROM namespace_memberships WHERE tenant_id=$1 AND membership_id=$2 AND namespace_id=$3`, tenantA, serviceMembership.ID, namespaceA.ID).Scan(&namespaceMembershipID, &namespaceCreatedAt); err != nil {
		t.Fatal(err)
	}
	if !identityClock.Now().Before(namespaceCreatedAt) {
		t.Fatal("regression requires the injected clock to precede the retained membership")
	}
	restoredAssignment, err := identityService.GrantRole(dedicatedAdminContext, identity.GrantRoleInput{Subject: "integration-service", ActorType: identity.ActorService, Role: identity.RoleContributor, NamespaceID: namespaceA.ID, Reason: "Restore namespace access across a clock correction"}, "identity-regrant-backstep")
	if err != nil {
		t.Fatalf("namespace grant restoration after clock backstep: %v", err)
	}
	var restoredNamespaceID, restoredNamespaceState string
	var restoredNamespaceAt time.Time
	if err := operatorPool.QueryRow(ctx, `SELECT id::text,state,created_at FROM namespace_memberships WHERE tenant_id=$1 AND membership_id=$2 AND namespace_id=$3`, tenantA, serviceMembership.ID, namespaceA.ID).Scan(&restoredNamespaceID, &restoredNamespaceState, &restoredNamespaceAt); err != nil {
		t.Fatal(err)
	}
	if restoredNamespaceID != namespaceMembershipID || restoredNamespaceState != "active" || restoredNamespaceAt.Before(namespaceCreatedAt) || restoredAssignment.ID == serviceAssignment.ID {
		t.Fatal("restored grant changed retained identity or backdated namespace membership")
	}
	if _, err := identityService.RevokeRole(dedicatedAdminContext, restoredAssignment.ID, "Finish clock-backstep regression", "identity-regrant-revoke", restoredAssignment.Revision); err != nil {
		t.Fatal(err)
	}
	offboardedMembership, err := identityService.OffboardMembership(dedicatedAdminContext, serviceMembership.ID, "retire the integration identity", "identity-membership-offboard", reactivatedMembership.Membership.Revision)
	if err != nil || offboardedMembership.Membership.State != identity.MembershipOffboarded || offboardedMembership.RevokedRoleAssignments != 0 || offboardedMembership.RevokedNamespaceMemberships != 0 {
		t.Fatalf("offboarded PostgreSQL membership = %#v, %v", offboardedMembership, err)
	}
	if !offboardedMembership.Membership.UpdatedAt.Equal(serviceMembership.UpdatedAt) {
		t.Fatal("membership offboarding did not retain its stored timestamp floor")
	}
	identityClock.Advance(2 * time.Second)
	serviceActor, err = identityStore.Resolve(ctx, identity.Principal{Issuer: identityIssuer, Subject: "integration-service", TenantID: tenantA})
	if identity.ErrorCodeOf(err) != identity.CodeUnauthenticated {
		t.Fatalf("offboarded namespace-scoped service identity = %#v, %v", serviceActor, err)
	}
	secondAdministrator, err := identityService.GrantRole(dedicatedAdminContext, identity.GrantRoleInput{
		Subject:   "second-admin",
		ActorType: identity.ActorHuman,
		Role:      identity.RoleTenantAdmin,
		Reason:    "exercise the final-administrator guard",
	}, "identity-integration-second-admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identityService.RevokeRole(dedicatedAdminContext, secondAdministrator.ID, "return to one administrator", "identity-integration-second-admin-revoke", secondAdministrator.Revision); err != nil {
		t.Fatal(err)
	}
	var originalAdministrator identity.RoleAssignment
	for _, assignment := range provisionedA.Assignments {
		if assignment.Role == identity.RoleTenantAdmin {
			originalAdministrator = assignment
		}
	}
	if originalAdministrator.ID == "" {
		t.Fatal("identity provisioning did not return the initial tenant administrator assignment")
	}
	if _, err := identityService.RevokeRole(dedicatedAdminContext, originalAdministrator.ID, "attempt to remove the final administrator", "identity-integration-final-admin", originalAdministrator.Revision); identity.ErrorCodeOf(err) != identity.CodeSeparationOfDuties {
		t.Fatalf("final tenant administrator revocation returned %v", err)
	}
	administratorMembership, err := identityService.GetMembership(dedicatedAdminContext, dedicatedActor.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identityService.SuspendMembership(dedicatedAdminContext, administratorMembership.ID, "attempt to disable the final administrator", "identity-integration-final-membership", administratorMembership.Revision); identity.ErrorCodeOf(err) != identity.CodeSeparationOfDuties {
		t.Fatalf("final tenant administrator membership suspension returned %v", err)
	}
	var identityAuditCount, identityOutboxCount int
	// Six successful grant/revoke transitions include the restored namespace
	// grant and its revocation; rejected final-administrator changes add none.
	if err := operatorPool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action IN ('identity.role_assignment.granted','identity.role_assignment.revoked')),
		  (SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type IN ('com.open.agentic.catalog.identity.role.assignment.granted.v1','com.open.agentic.catalog.identity.role.assignment.revoked.v1'))`, tenantA).Scan(&identityAuditCount, &identityOutboxCount); err != nil || identityAuditCount != 6 || identityOutboxCount != 6 {
		t.Fatalf("identity change evidence = audit %d outbox %d, %v", identityAuditCount, identityOutboxCount, err)
	}
	tenantBOnlyContent := []byte("worker reads must retain the requested tenant boundary")
	tenantBOnlySum := sha256.Sum256(tenantBOnlyContent)
	tenantBOnlyDocumentID := fmt.Sprintf("%x", tenantBOnlySum)
	if _, err := operatorPool.Exec(ctx, `INSERT INTO document_blobs(tenant_id,id,media_type,content_digest,size_bytes,content,created_at) VALUES ($1,$2,'text/plain',$3,$4,$5,$6)`, tenantB, tenantBOnlyDocumentID, "sha256:"+tenantBOnlyDocumentID, len(tenantBOnlyContent), tenantBOnlyContent, now); err != nil {
		t.Fatal(err)
	}
	if _, err := workerRepository.GetDocument(ctx, tenantA, tenantBOnlyDocumentID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant worker document read returned %v", err)
	}
	if _, err := workerRepository.GetDocument(ctx, tenantB, tenantBOnlyDocumentID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound || !errors.Is(err, catalog.ErrInactiveDocument) {
		t.Fatalf("unreferenced tenant-b worker document read returned %v", err)
	}
	items, err := apiRepository.ListNamespaces(ctx, tenantA, catalog.NamespaceListRequest{Limit: 100})
	if err != nil || len(items) != 1 || items[0].ID != namespaceA.ID {
		t.Fatalf("tenant A namespaces = %#v, %v", items, err)
	}
	terminalNamespaceID, _ := generator.New(now.Add(3 * time.Nanosecond))
	terminalAuditID, _ := generator.New(now.Add(4 * time.Nanosecond))
	terminalOutboxID, _ := generator.New(now.Add(5 * time.Nanosecond))
	terminalPublishedAt := now.Add(time.Second)
	terminalNamespace := catalog.Namespace{ID: terminalNamespaceID, TenantID: tenantA, Slug: "terminal-outbox", DisplayName: "Terminal outbox", Revision: 1, CreatedAt: now, UpdatedAt: now}
	terminalAudit := integrationAudit(t, terminalAuditID, tenantA, "test", "namespace.created", "namespace", terminalNamespaceID, "", now)
	terminalOutbox := integrationOutbox(t, terminalOutboxID, terminalAudit)
	terminalOutbox.PublishedAt = &terminalPublishedAt
	if _, err := apiRepository.CreateNamespace(ctx, terminalNamespace, terminalAudit, terminalOutbox); err != nil {
		t.Fatal(err)
	}
	var storedPublishedAt time.Time
	if err := workerPool.QueryRow(ctx, `SELECT published_at FROM outbox_events WHERE id=$1`, terminalOutboxID).Scan(&storedPublishedAt); err != nil || !storedPublishedAt.Equal(terminalPublishedAt) {
		t.Fatalf("terminal outbox published_at = %v, %v; want %v", storedPublishedAt, err, terminalPublishedAt)
	}
	var visibleWithoutContext int
	if err := apiPool.QueryRow(ctx, `SELECT count(*) FROM namespaces`).Scan(&visibleWithoutContext); err != nil || visibleWithoutContext != 0 {
		t.Fatalf("RLS without context exposed %d rows, err=%v", visibleWithoutContext, err)
	}
	if err := apiPool.QueryRow(ctx, `SELECT count(*) FROM tenants`).Scan(&visibleWithoutContext); err != nil || visibleWithoutContext != 0 {
		t.Fatalf("tenant registry without context exposed %d rows, err=%v", visibleWithoutContext, err)
	}
	for _, table := range []string{"tenant_identity_realms", "tenant_memberships", "namespace_memberships", "role_assignments"} {
		if err := apiPool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&visibleWithoutContext); err != nil || visibleWithoutContext != 0 {
			t.Fatalf("%s without context exposed %d rows, err=%v", table, visibleWithoutContext, err)
		}
	}
	var crossTenantIdentities int
	_, err = withTenantRead(ctx, apiPool, tenantA, func(tx pgx.Tx) (struct{}, error) {
		return struct{}{}, tx.QueryRow(ctx, `SELECT count(*) FROM tenant_memberships WHERE subject=$1`, provisionedB.Subject).Scan(&crossTenantIdentities)
	})
	if err != nil || crossTenantIdentities != 0 {
		t.Fatalf("tenant A identity context exposed %d tenant B memberships, err=%v", crossTenantIdentities, err)
	}
	crossTenantTx, err := apiPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crossTenantTx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantA); err != nil {
		_ = crossTenantTx.Rollback(ctx)
		t.Fatal(err)
	}
	crossResourceID, _ := generator.New(now.Add(20 * time.Nanosecond))
	_, crossTenantErr := crossTenantTx.Exec(ctx, `INSERT INTO resources(id, tenant_id, namespace_id, canonical_identifier, kind, created_by, revision, created_at, updated_at) VALUES ($1,$2,$3,$4,'agent','test',1,$5,$5)`, crossResourceID, tenantA, namespaceB.ID, "urn:air:catalog.example.com:alpha:cross-tenant", now)
	_ = crossTenantTx.Rollback(ctx)
	if crossTenantErr == nil {
		t.Fatal("database accepted a child row linked to another tenant")
	}
	namespaceA2 := createNamespace(tenantA, "gamma")
	firstNamespacePage, err := apiRepository.ListNamespaces(ctx, tenantA, catalog.NamespaceListRequest{Limit: 2})
	if err != nil || len(firstNamespacePage) != 2 || firstNamespacePage[0].Slug != "alpha" || firstNamespacePage[1].Slug != "gamma" {
		t.Fatalf("first PostgreSQL namespace page = %#v, %v", firstNamespacePage, err)
	}
	secondNamespacePage, err := apiRepository.ListNamespaces(ctx, tenantA, catalog.NamespaceListRequest{AfterSlug: firstNamespacePage[1].Slug, AfterID: firstNamespacePage[1].ID, Limit: 2})
	if err != nil || len(secondNamespacePage) != 1 || secondNamespacePage[0].Slug != "terminal-outbox" {
		t.Fatalf("second PostgreSQL namespace page = %#v, %v", secondNamespacePage, err)
	}
	finalNamespacePage, err := apiRepository.ListNamespaces(ctx, tenantA, catalog.NamespaceListRequest{AfterSlug: secondNamespacePage[0].Slug, AfterID: secondNamespacePage[0].ID, Limit: 2})
	if err != nil || len(finalNamespacePage) != 0 {
		t.Fatalf("final PostgreSQL namespace page = %#v, %v", finalNamespacePage, err)
	}
	if _, err := apiRepository.GetNamespace(ctx, tenantB, namespaceA.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant exact namespace read returned %v", err)
	}
	resourceID, _ := generator.New(now.Add(21 * time.Nanosecond))
	resourceAuditID, _ := generator.New(now.Add(22 * time.Nanosecond))
	resourceOutboxID, _ := generator.New(now.Add(23 * time.Nanosecond))
	resource := catalog.Resource{ID: resourceID, TenantID: tenantA, NamespaceID: namespaceA.ID, CanonicalIdentifier: "vendor.example/agents/namespace-bound", Kind: catalog.KindAgent, CreatedBy: "test", Revision: 1, CreatedAt: now, UpdatedAt: now}
	resourceAudit := integrationAudit(t, resourceAuditID, tenantA, "test", "resource.created", "resource", resource.ID, "", now)
	resourceOutbox := integrationOutbox(t, resourceOutboxID, resourceAudit)
	if _, err := apiRepository.CreateResource(ctx, catalog.CreateResourceParams{Resource: resource, Audit: resourceAudit, Outbox: resourceOutbox}); err != nil {
		t.Fatal(err)
	}
	cursorKey := sha256.Sum256([]byte(t.Name()))
	catalogClock := clock.NewFake(now)
	externalURL := "https://content.example.com/postgres-agent.json"
	externalOriginal := []byte(`{"name":"PostgreSQL External Agent","description":"Exercises exact external representation persistence.","version":"3.0.0","supportedInterfaces":[{"url":"https://agents.example.com/postgres-external","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"persist-external","name":"Persist external","description":"Exercises PostgreSQL external evidence.","tags":["postgres"]}]}`)
	externalFetcher := &postgresExternalFetcher{at: now, body: externalOriginal, policyDigest: "sha256:" + strings.Repeat("e", 64)}
	catalogService, err := catalog.NewService(catalog.Options{Repository: apiRepository, Clock: catalogClock, IDs: generator, Validator: adapter.NewRegistry(), ExternalFetcher: externalFetcher, Artwork: postgresArtworkService(t), CursorKey: cursorKey[:], Policy: "integration-policy"})
	if err != nil {
		t.Fatal(err)
	}
	authorContext := dedicatedAdminContext
	readerActor := dedicatedActor
	readerActor.Roles = []identity.Role{identity.RoleReader}
	readerContext := identity.WithActor(ctx, readerActor)
	otherReaderActor := sharedActor
	otherReaderActor.Roles = []identity.Role{identity.RoleReader}
	otherReaderContext := identity.WithActor(ctx, otherReaderActor)
	namespaceDisplayName := "Alpha Catalog"
	namespaceDescription := "Updated through the practical administration contract."
	updatedNamespace, err := catalogService.UpdateNamespace(authorContext, namespaceA.ID, catalog.PatchNamespaceInput{DisplayName: &namespaceDisplayName, Description: &namespaceDescription, Reason: "clarify the integration namespace"}, namespaceA.Revision)
	if err != nil || updatedNamespace.Revision != 2 || updatedNamespace.Slug != namespaceA.Slug || updatedNamespace.DisplayName != namespaceDisplayName || updatedNamespace.Description != namespaceDescription {
		t.Fatalf("PostgreSQL namespace update = %#v, %v", updatedNamespace, err)
	}
	if _, err := catalogService.UpdateNamespace(authorContext, namespaceA.ID, catalog.PatchNamespaceInput{DisplayName: &namespaceDisplayName, Reason: "reject the stale namespace revision"}, namespaceA.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("PostgreSQL stale namespace update returned %v", err)
	}
	namespaceA = updatedNamespace
	draftToDelete, err := catalogService.CreateResource(authorContext, catalog.CreateResourceInput{NamespaceID: namespaceA.ID, CanonicalIdentifier: "urn:air:catalog.example.com:alpha:discarded-draft", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	deletedDraftContent := `{"name":"Discarded PostgreSQL Agent","description":"Exercises compatibility-only tombstone reads.","version":"1.0.0","supportedInterfaces":[{"url":"https://agents.example.com/discarded","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"discard-draft","name":"Discard draft","description":"Exercises safe deletion.","tags":["postgres"]}]}`
	_, deletedDraftRevision, _, err := catalogService.CreateVersion(authorContext, draftToDelete.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Content: deletedDraftContent}}})
	if err != nil {
		t.Fatal(err)
	}
	deletedDraftDocumentID := deletedDraftRevision.Representations[0].ArtifactID
	otherTenantAdminContext := identity.WithActor(ctx, sharedActor)
	if err := catalogService.DeleteResource(otherTenantAdminContext, draftToDelete.ID, "cross-tenant deletion must remain hidden", draftToDelete.Revision); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL draft deletion returned %v", err)
	}
	if err := catalogService.DeleteResource(authorContext, draftToDelete.ID, "reject a stale draft revision", draftToDelete.Revision+1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale PostgreSQL draft deletion returned %v", err)
	}
	if err := catalogService.DeleteResource(authorContext, draftToDelete.ID, "discard the unused local draft", draftToDelete.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogService.GetResource(readerContext, draftToDelete.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("soft-deleted PostgreSQL draft remained visible: %v", err)
	}
	if _, _, err := catalogService.GetArtifact(readerContext, deletedDraftDocumentID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound || !errors.Is(err, catalog.ErrInactiveDocument) {
		t.Fatalf("soft-deleted PostgreSQL draft document remained visible: %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE representations SET document_blob_id=NULL WHERE tenant_id=$1 AND version_revision_id=$2`, tenantA, deletedDraftRevision.ID); err == nil {
		t.Fatal("retained representation accepted removal of its authoritative document")
	}
	if _, err := operatorPool.Exec(ctx, `DELETE FROM document_blobs WHERE tenant_id=$1 AND id=$2`, tenantA, deletedDraftDocumentID); err == nil {
		t.Fatal("retained document accepted an ordinary deletion")
	}
	if _, err := apiRepository.GetDocument(ctx, tenantA, deletedDraftDocumentID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound || !errors.Is(err, catalog.ErrInactiveDocument) {
		t.Fatalf("soft-deleted PostgreSQL draft document remained readable through the repository: %v", err)
	}
	var deletedBy, deleteReason string
	var deletedAt time.Time
	var deletedRevision int64
	if err := workerPool.QueryRow(ctx, `SELECT deleted_by,delete_reason,deleted_at,revision FROM resources WHERE tenant_id=$1 AND id=$2`, tenantA, draftToDelete.ID).Scan(&deletedBy, &deleteReason, &deletedAt, &deletedRevision); err != nil || deletedBy != dedicatedActor.Subject || deleteReason != "discard the unused local draft" || deletedAt.IsZero() || deletedRevision != 2 {
		t.Fatalf("retained PostgreSQL draft tombstone = actor %q reason %q at %v revision %d, %v", deletedBy, deleteReason, deletedAt, deletedRevision, err)
	}
	if _, err := catalogService.CreateResource(authorContext, catalog.CreateResourceInput{NamespaceID: namespaceA.ID, CanonicalIdentifier: draftToDelete.CanonicalIdentifier, Kind: draftToDelete.Kind}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("soft-deleted PostgreSQL canonical identifier was reusable: %v", err)
	}
	skillResource, err := catalogService.CreateResource(authorContext, catalog.CreateResourceInput{NamespaceID: namespaceA.ID, CanonicalIdentifier: "urn:air:catalog.example.com:alpha:postgres-skill", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	skillContent := "---\nname: postgres-skill\ndescription: Proves transactional PostgreSQL document storage.\n---\n\n# Instructions\nKeep these exact bytes.\n"
	skillVersion, skillRevision, _, err := catalogService.CreateVersion(authorContext, skillResource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillMarkdown, Content: skillContent}}})
	if err != nil {
		t.Fatal(err)
	}
	documentID := skillRevision.Representations[0].ArtifactID
	artifact, storedContent, err := catalogService.GetArtifact(readerContext, documentID)
	if err != nil || artifact.ID != documentID || string(storedContent) != skillContent || artifact.Digest != "sha256:"+documentID || artifact.Size != int64(len(skillContent)) {
		t.Fatalf("PostgreSQL catalog document = %#v, %q, %v", artifact, storedContent, err)
	}
	if _, _, err := catalogService.GetArtifact(otherReaderContext, documentID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL catalog document read returned %v", err)
	}
	var documentCount, referenceCount int
	if err := workerPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM document_blobs WHERE tenant_id=$1 AND id=$2),(SELECT count(*) FROM representations WHERE tenant_id=$1 AND document_blob_id=$2)`, tenantA, documentID).Scan(&documentCount, &referenceCount); err != nil || documentCount != 1 || referenceCount != 1 {
		t.Fatalf("PostgreSQL catalog document persistence = documents %d references %d, %v", documentCount, referenceCount, err)
	}
	externalResource, err := catalogService.CreateResource(authorContext, catalog.CreateResourceInput{NamespaceID: namespaceA.ID, CanonicalIdentifier: "urn:air:catalog.example.com:alpha:postgres-external", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	externalVersion, externalRevision, externalEvidence, err := catalogService.CreateVersion(authorContext, externalResource.ID, catalog.CreateVersionInput{Version: "3.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, URL: externalURL}}})
	if err != nil {
		t.Fatal(err)
	}
	externalRepresentation := externalRevision.Representations[0]
	if externalEvidence.State != catalog.EvidenceComplete || len(externalEvidence.ExternalRetrievals) != 1 || externalEvidence.ExpiresAt.IsZero() || externalRepresentation.URL != externalURL || externalRepresentation.ArtifactID == "" || externalRepresentation.ContentDigest != externalEvidence.ExternalRetrievals[0].ContentDigest {
		t.Fatalf("PostgreSQL external representation = %#v / %#v", externalRevision, externalEvidence)
	}
	loadedExternal, loadedExternalEvidence, err := apiRepository.GetVersionRevision(ctx, tenantA, externalRevision.ID)
	if err != nil || loadedExternal.Representations[0].ArtifactID != externalRepresentation.ArtifactID || loadedExternalEvidence.Digest != externalEvidence.Digest || len(loadedExternalEvidence.ExternalRetrievals) != 1 {
		t.Fatalf("PostgreSQL external representation reload = %#v / %#v, %v", loadedExternal, loadedExternalEvidence, err)
	}
	if _, _, err := apiRepository.GetVersionRevision(ctx, tenantB, externalRevision.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant external representation read returned %v", err)
	}
	if _, _, err := workerRepository.GetVersionRevision(ctx, tenantB, externalRevision.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("BYPASSRLS worker cross-tenant representation read returned %v", err)
	}
	externalDocument, err := apiRepository.GetDocument(ctx, tenantA, externalRepresentation.ArtifactID)
	if err != nil || !bytes.Equal(externalDocument.Content, externalOriginal) {
		t.Fatalf("PostgreSQL external validation bytes = %q, %v", externalDocument.Content, err)
	}
	var storedExternalURL, storedExternalDocumentID string
	var externalRetrievalCount int
	var externalRunExpiry time.Time
	if err := workerPool.QueryRow(ctx, `
		SELECT r.external_url,r.document_blob_id,jsonb_array_length(e.external_retrievals),run.expires_at
		FROM representations r
		JOIN resource_version_revisions revision ON revision.tenant_id=r.tenant_id AND revision.id=r.version_revision_id
		JOIN evidence_sets e ON e.tenant_id=revision.tenant_id AND e.id=revision.evidence_set_id
		JOIN version_validation_runs run ON run.tenant_id=revision.tenant_id AND run.version_revision_id=revision.id AND run.evidence_set_id=e.id
		WHERE r.tenant_id=$1 AND r.id=$2`, tenantA, externalRepresentation.ID).Scan(&storedExternalURL, &storedExternalDocumentID, &externalRetrievalCount, &externalRunExpiry); err != nil || storedExternalURL != externalURL || storedExternalDocumentID != externalRepresentation.ArtifactID || externalRetrievalCount != 1 || !externalRunExpiry.Equal(externalEvidence.ExpiresAt) {
		t.Fatalf("PostgreSQL external relational evidence = url %q document %q retrievals %d expiry %v, %v", storedExternalURL, storedExternalDocumentID, externalRetrievalCount, externalRunExpiry, err)
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE representations SET document_blob_id=NULL WHERE tenant_id=$1 AND id=$2`, tenantA, externalRepresentation.ID); err == nil || !strings.Contains(err.Error(), "retained history is immutable") {
		t.Fatalf("external representation history mutation returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE evidence_sets SET external_retrievals='{}'::jsonb WHERE tenant_id=$1 AND id=$2`, tenantA, externalEvidence.ID); err == nil || !strings.Contains(err.Error(), "retained history is immutable") {
		t.Fatalf("external validation evidence mutation returned %v", err)
	}
	var crossTenantExternalEvidence int
	_, err = withTenantRead(ctx, apiPool, tenantB, func(tx pgx.Tx) (struct{}, error) {
		return struct{}{}, tx.QueryRow(ctx, `SELECT count(*) FROM evidence_sets WHERE id=$1 AND jsonb_array_length(external_retrievals)=1`, externalEvidence.ID).Scan(&crossTenantExternalEvidence)
	})
	if err != nil || crossTenantExternalEvidence != 0 {
		t.Fatalf("cross-tenant external evidence visibility = %d, %v", crossTenantExternalEvidence, err)
	}
	catalogClock.Advance(time.Minute)
	externalFetcher.at = catalogClock.Now()
	_, renewedExternalEvidence, _, err := catalogService.RevalidateVersionRevision(authorContext, externalRevision.ID)
	if err != nil || renewedExternalEvidence.State != catalog.EvidenceComplete || renewedExternalEvidence.ID == externalEvidence.ID || len(renewedExternalEvidence.ExternalRetrievals) != 1 {
		t.Fatalf("PostgreSQL renewed external evidence = %#v, %v", renewedExternalEvidence, err)
	}
	externalChanged := []byte(`{"name":"PostgreSQL External Agent","description":"Exercises changed external representation bytes.","version":"3.0.0","supportedInterfaces":[{"url":"https://agents.example.com/postgres-external","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"persist-external","name":"Persist external","description":"Exercises PostgreSQL external evidence.","tags":["postgres"]}]}`)
	catalogClock.Advance(time.Minute)
	externalFetcher.at = catalogClock.Now()
	externalFetcher.body = externalChanged
	_, driftedExternalEvidence, _, err := catalogService.RevalidateVersionRevision(authorContext, externalRevision.ID)
	driftFinding := false
	for _, finding := range driftedExternalEvidence.Findings {
		if finding.Code == "EXTERNAL_CONTENT_CHANGED" {
			driftFinding = true
		}
	}
	if err != nil || driftedExternalEvidence.State != catalog.EvidenceBlocked || !driftFinding {
		t.Fatalf("PostgreSQL external drift evidence = %#v, %v", driftedExternalEvidence, err)
	}
	externalDocument, err = apiRepository.GetDocument(ctx, tenantA, externalRepresentation.ArtifactID)
	if err != nil || !bytes.Equal(externalDocument.Content, externalOriginal) {
		t.Fatalf("PostgreSQL external drift rewrote retained bytes = %q, %v", externalDocument.Content, err)
	}
	externalVersion, replacementExternalRevision, replacementExternalEvidence, err := catalogService.CreateVersionRevision(authorContext, externalVersion.ID, []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, URL: externalURL}}, externalVersion.Revision)
	if err != nil || externalVersion.Revision != 2 || replacementExternalRevision.RevisionNumber != 2 || replacementExternalEvidence.State != catalog.EvidenceComplete || replacementExternalRevision.ContentSetDigest == externalRevision.ContentSetDigest {
		t.Fatalf("PostgreSQL replacement external revision = %#v / %#v / %#v, %v", externalVersion, replacementExternalRevision, replacementExternalEvidence, err)
	}
	packageBytes := postgresSkillPackageZIP(t)
	packageHandoff := &catalog.SkillHandoff{
		Git: &catalog.GitHandoffReference{Repository: "https://git.example.com/team/skills.git", Commit: strings.Repeat("a", 40), Directory: "skills/postgres-skill"},
		OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/team/postgres-skill@sha256:" + strings.Repeat("b", 64)},
	}
	_, packageRevision, packageEvidence, err := catalogService.CreateVersion(authorContext, skillResource.ID, catalog.CreateVersionInput{Version: "2.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillZIP, PackageData: packageBytes, Handoff: packageHandoff}}})
	if err != nil {
		t.Fatal(err)
	}
	packageRepresentation := packageRevision.Representations[0]
	if packageEvidence.State != catalog.EvidenceComplete || packageRepresentation.Package == nil || packageRepresentation.Package.FileCount != 2 || packageRepresentation.Handoff == nil {
		t.Fatalf("PostgreSQL Skill package = %#v / %#v", packageRevision, packageEvidence)
	}
	loadedPackage, _, err := apiRepository.GetVersionRevision(ctx, tenantA, packageRevision.ID)
	if err != nil || loadedPackage.Representations[0].Package.FileCount != 2 || loadedPackage.Representations[0].Package.Assessments != catalog.NotAssessedSkillPackageAssessments() || loadedPackage.Representations[0].Handoff.Git.Commit != strings.Repeat("a", 40) || loadedPackage.Representations[0].Handoff.OCI.Reference != packageHandoff.OCI.Reference {
		t.Fatalf("PostgreSQL Skill package reload = %#v, %v", loadedPackage, err)
	}
	if _, _, err := apiRepository.GetVersionRevision(ctx, tenantB, packageRevision.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant Skill package revision read returned %v", err)
	}
	var packageRows, artifactReferenceRows, inventoryRows, packageDocumentRows int
	packageDocumentID := strings.TrimPrefix(packageRepresentation.ContentDigest, "sha256:")
	if err := workerPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM representations WHERE tenant_id=$1 AND id=$2 AND external_reference_only AND skill_package_structure_state='passed'),(SELECT count(*) FROM artifact_references WHERE tenant_id=$1 AND representation_id=$2),(SELECT count(*) FROM skill_file_inventory WHERE tenant_id=$1 AND representation_id=$2),(SELECT count(*) FROM document_blobs WHERE tenant_id=$1 AND id=$3)`, tenantA, packageRepresentation.ID, packageDocumentID).Scan(&packageRows, &artifactReferenceRows, &inventoryRows, &packageDocumentRows); err != nil || packageRows != 1 || artifactReferenceRows != 2 || inventoryRows != 2 || packageDocumentRows != 0 {
		t.Fatalf("Skill package relational persistence = package %d references %d files %d documents %d, %v", packageRows, artifactReferenceRows, inventoryRows, packageDocumentRows, err)
	}
	var crossTenantReferences, crossTenantFiles int
	_, err = withTenantRead(ctx, apiPool, tenantB, func(tx pgx.Tx) (struct{}, error) {
		return struct{}{}, tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM artifact_references WHERE representation_id=$1),(SELECT count(*) FROM skill_file_inventory WHERE representation_id=$1)`, packageRepresentation.ID).Scan(&crossTenantReferences, &crossTenantFiles)
	})
	if err != nil || crossTenantReferences != 0 || crossTenantFiles != 0 {
		t.Fatalf("cross-tenant Skill metadata visibility = references %d files %d, %v", crossTenantReferences, crossTenantFiles, err)
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE representations SET skill_package_malware_state=NULL WHERE tenant_id=$1 AND id=$2`, tenantA, packageRepresentation.ID); err == nil {
		t.Fatal("PostgreSQL accepted an incomplete Skill package assessment summary")
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE artifact_references SET oci_reference=$3 WHERE tenant_id=$1 AND representation_id=$2 AND reference_kind='oci'`, tenantA, packageRepresentation.ID, "team/postgres-skill@sha256:"+strings.Repeat("b", 64)); err == nil {
		t.Fatal("PostgreSQL accepted an OCI handoff without an explicit registry")
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE artifact_references SET oci_reference=$3 WHERE tenant_id=$1 AND representation_id=$2 AND reference_kind='oci'`, tenantA, packageRepresentation.ID, "registry.example.com:/team/postgres-skill@sha256:"+strings.Repeat("b", 64)); err == nil {
		t.Fatal("PostgreSQL accepted an OCI handoff with an empty registry port")
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE representations SET skill_package_expanded_size=size_bytes*100+1 WHERE tenant_id=$1 AND id=$2`, tenantA, packageRepresentation.ID); err == nil {
		t.Fatal("PostgreSQL accepted a Skill package summary above its expansion ratio")
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE skill_file_inventory SET content_digest=$3 WHERE tenant_id=$1 AND representation_id=$2 AND path LIKE '%/SKILL.md'`, tenantA, packageRepresentation.ID, "sha256:"+strings.Repeat("c", 64)); err == nil {
		t.Fatal("immutable Skill inventory accepted a digest rewrite")
	}
	if _, _, err := apiRepository.GetVersionRevision(ctx, tenantA, packageRevision.ID); err != nil {
		t.Fatalf("Skill inventory changed after a rejected rewrite: %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `INSERT INTO document_blobs(tenant_id,id,media_type,content_digest,size_bytes,content,created_at) VALUES ($1,$2,'text/plain',$3,1,'x',$4)`, tenantA, strings.Repeat("a", 64), "sha256:"+strings.Repeat("a", 64), now); err == nil {
		t.Fatal("PostgreSQL accepted catalog document bytes that do not match their digest")
	}
	invalidUTF8 := []byte{0xff}
	invalidUTF8Sum := sha256.Sum256(invalidUTF8)
	invalidUTF8ID := fmt.Sprintf("%x", invalidUTF8Sum)
	if _, err := operatorPool.Exec(ctx, `INSERT INTO document_blobs(tenant_id,id,media_type,content_digest,size_bytes,content,created_at) VALUES ($1,$2,'text/plain',$3,1,$4,$5)`, tenantA, invalidUTF8ID, "sha256:"+invalidUTF8ID, invalidUTF8, now); err == nil {
		t.Fatal("PostgreSQL accepted non-UTF-8 textual catalog document bytes")
	}
	nulContent := []byte{'x', 0, 'y'}
	nulSum := sha256.Sum256(nulContent)
	nulID := fmt.Sprintf("%x", nulSum)
	if _, err := operatorPool.Exec(ctx, `INSERT INTO document_blobs(tenant_id,id,media_type,content_digest,size_bytes,content,created_at) VALUES ($1,$2,'text/plain',$3,$4,$5,$6)`, tenantA, nulID, "sha256:"+nulID, len(nulContent), nulContent, now); err == nil {
		t.Fatal("PostgreSQL accepted a textual catalog document containing a NUL byte")
	}
	rollbackContent := []byte("transaction must roll this document back")
	rollbackSum := sha256.Sum256(rollbackContent)
	rollbackDocumentID := fmt.Sprintf("%x", rollbackSum)
	rollbackRevisionID, _ := generator.New(now.Add(30 * time.Nanosecond))
	rollbackEvidenceID, _ := generator.New(now.Add(31 * time.Nanosecond))
	rollbackVersion := skillVersion
	rollbackVersion.CandidateRevisionID = rollbackRevisionID
	rollbackVersion.Revision++
	rollbackVersion.UpdatedAt = now.Add(time.Minute)
	rollbackRevision := catalog.VersionRevision{ID: rollbackRevisionID, TenantID: tenantA, ResourceVersionID: skillVersion.ID, RevisionNumber: 2, ContentSetDigest: "sha256:" + strings.Repeat("e", 64), EvidenceSetID: rollbackEvidenceID, CreatedBy: "test", CreatedAt: now.Add(time.Minute), Representations: []catalog.Representation{{ID: skillRevision.Representations[0].ID, Role: catalog.RepresentationPrimary, MediaType: "text/plain", ProfileID: "rollback-test", ContentDigest: "sha256:" + rollbackDocumentID, Size: int64(len(rollbackContent)), ArtifactID: rollbackDocumentID, ValidationState: "valid"}}}
	rollbackEvidence := catalog.EvidenceSet{ID: rollbackEvidenceID, TenantID: tenantA, SubjectDigest: rollbackRevision.ContentSetDigest, Digest: "sha256:" + strings.Repeat("f", 64), State: catalog.EvidenceComplete, ValidationProfile: "catalog-validation-v7", PolicyVersion: "rollback-test", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, CreatedAt: now.Add(time.Minute)}
	rollbackDocument := catalog.DocumentBlob{ID: rollbackDocumentID, TenantID: tenantA, Digest: "sha256:" + rollbackDocumentID, MediaType: "text/plain", Size: int64(len(rollbackContent)), Content: rollbackContent, CreatedAt: now.Add(time.Minute)}
	if _, _, _, err := apiRepository.CreateVersionRevision(ctx, catalog.CreateVersionParams{Version: rollbackVersion, VersionRevision: rollbackRevision, Documents: []catalog.DocumentBlob{rollbackDocument}, Evidence: rollbackEvidence, ExpectedRevision: skillVersion.Revision}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("forced representation conflict returned %v", err)
	}
	if _, err := apiRepository.GetDocument(ctx, tenantA, rollbackDocumentID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("failed version transaction retained its catalog document: %v", err)
	}
	publisher, err := catalogService.CreatePublisher(authorContext, catalog.CreatePublisherInput{DisplayName: "Integration Publisher", Domains: []string{"catalog.example.com", "unused.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	updatedPublisherName := "Integration Publisher Updated"
	updatedPublisherDomains := []string{"catalog.example.com"}
	publisher, err = catalogService.UpdatePublisher(authorContext, publisher.ID, catalog.PatchPublisherInput{DisplayName: &updatedPublisherName, Domains: &updatedPublisherDomains, Reason: "remove an unused declaration"}, publisher.Revision)
	if err != nil || publisher.Revision != 2 || publisher.DisplayName != updatedPublisherName || len(publisher.Domains) != 1 || publisher.Domains[0] != "catalog.example.com" {
		t.Fatalf("PostgreSQL publisher update = %#v, %v", publisher, err)
	}
	if publisher.ApprovedPublicationDomains == nil {
		t.Fatal("PostgreSQL publisher update returned null rather than an empty approval array")
	}
	if _, err := catalogService.UpdatePublisher(otherTenantAdminContext, publisher.ID, catalog.PatchPublisherInput{DisplayName: &updatedPublisherName, Reason: "cross-tenant update must remain hidden"}, publisher.Revision); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL publisher update returned %v", err)
	}
	if _, err := catalogService.ApprovePublisherPublicationDomain(otherTenantAdminContext, publisher.ID, "catalog.example.com", "cross-tenant attempt", publisher.Revision); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL publication-domain approval returned %v", err)
	}
	publisher, err = catalogService.ApprovePublisherPublicationDomain(authorContext, publisher.ID, "catalog.example.com", "approved for integration publication", publisher.Revision)
	if err != nil || publisher.Revision != 3 || len(publisher.ApprovedPublicationDomains) != 1 || publisher.ApprovedPublicationDomains[0] != "catalog.example.com" {
		t.Fatalf("PostgreSQL publication-domain approval = %#v, %v", publisher, err)
	}
	if _, err := catalogService.UpdatePublisher(authorContext, publisher.ID, catalog.PatchPublisherInput{Domains: &[]string{"replacement.example.com"}, Reason: "approved domains cannot be silently removed"}, publisher.Revision); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("PostgreSQL approved-domain removal returned %v", err)
	}
	alternatePublisher, err := catalogService.CreatePublisher(authorContext, catalog.CreatePublisherInput{DisplayName: "A Publisher", Domains: []string{"alternate.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	alternatePublisher, err = catalogService.ApprovePublisherPublicationDomain(authorContext, alternatePublisher.ID, "alternate.example.com", "qualify revocation evidence", alternatePublisher.Revision)
	if err != nil {
		t.Fatal(err)
	}
	alternateRevocation, err := catalogService.RevokePublisherPublicationDomain(authorContext, alternatePublisher.ID, "alternate.example.com", "retire unused authority", alternatePublisher.Revision)
	if err != nil || alternateRevocation.Publisher.Revision != alternatePublisher.Revision+1 || len(alternateRevocation.AffectedReleaseIDs) != 0 {
		t.Fatalf("PostgreSQL publication-domain revocation = %#v, %v", alternateRevocation, err)
	}
	var affectedReleaseCount int
	var affectedReleaseDigest string
	var retainedAffectedIDs bool
	if err := operatorPool.QueryRow(ctx, `SELECT (details->'facts'->>'affectedReleaseCount')::integer,details->'facts'->>'affectedReleaseDigest',(details->'facts') ? 'affectedReleaseIds' FROM audit_events WHERE tenant_id=$1 AND action='publisher.publication_domain_revoked' AND target_id=$2 ORDER BY occurred_at DESC,id DESC LIMIT 1`, tenantA, alternatePublisher.ID).Scan(&affectedReleaseCount, &affectedReleaseDigest, &retainedAffectedIDs); err != nil || affectedReleaseCount != 0 || affectedReleaseDigest != audit.DigestBytes([]byte("[]")) || retainedAffectedIDs {
		t.Fatalf("bounded publication-domain revocation evidence = count %d digest %q ids=%t, %v", affectedReleaseCount, affectedReleaseDigest, retainedAffectedIDs, err)
	}
	firstPublisherPage, err := apiRepository.ListPublisherPage(ctx, tenantA, catalog.PublisherListRequest{Limit: 1})
	if err != nil || len(firstPublisherPage) != 1 || firstPublisherPage[0].ID != alternatePublisher.ID {
		t.Fatalf("first PostgreSQL publisher page = %#v, %v", firstPublisherPage, err)
	}
	secondPublisherPage, err := apiRepository.ListPublisherPage(ctx, tenantA, catalog.PublisherListRequest{AfterDisplayName: firstPublisherPage[0].DisplayName, AfterID: firstPublisherPage[0].ID, Limit: 1})
	if err != nil || len(secondPublisherPage) != 1 || secondPublisherPage[0].ID != publisher.ID {
		t.Fatalf("second PostgreSQL publisher page = %#v, %v", secondPublisherPage, err)
	}
	finalPublisherPage, err := apiRepository.ListPublisherPage(ctx, tenantA, catalog.PublisherListRequest{AfterDisplayName: secondPublisherPage[0].DisplayName, AfterID: secondPublisherPage[0].ID, Limit: 1})
	if err != nil || len(finalPublisherPage) != 0 {
		t.Fatalf("final PostgreSQL publisher page = %#v, %v", finalPublisherPage, err)
	}
	if _, err := apiRepository.GetPublisher(ctx, tenantB, publisher.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant exact publisher read returned %v", err)
	}
	representation := func(description string) []catalog.RepresentationInput {
		return []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: json.RawMessage(`{"name":"PostgreSQL Agent","description":"` + description + `","version":"1.0.0","supportedInterfaces":[{"url":"https://agents.example.com/postgres","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"persist","name":"Persist","description":"Exercises PostgreSQL persistence","tags":["postgres"]}]}`)}}
	}
	version, originalVersionRevision, _, err := catalogService.CreateVersion(authorContext, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: representation("Original PostgreSQL revision")})
	if err != nil {
		t.Fatal(err)
	}
	if exactVersion, getErr := apiRepository.GetVersionByLabel(ctx, tenantA, resource.ID, version.Version); getErr != nil || exactVersion.ID != version.ID {
		t.Fatalf("PostgreSQL exact version-label read = %#v, %v", exactVersion, getErr)
	}
	if _, getErr := apiRepository.GetVersionByLabel(ctx, tenantB, resource.ID, version.Version); catalog.ErrorCodeOf(getErr) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL version-label read returned %v", getErr)
	}
	if _, _, _, err := catalogService.CreateVersionRevision(authorContext, version.ID, representation("Rejected stale PostgreSQL revision"), version.Revision+1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("PostgreSQL future version precondition returned %v", err)
	}
	updatedVersion, versionRevision, versionEvidence, err := catalogService.CreateVersionRevision(authorContext, version.ID, representation("Revised PostgreSQL revision"), version.Revision)
	if err != nil || updatedVersion.Revision != 2 || versionRevision.RevisionNumber != 2 {
		t.Fatalf("PostgreSQL version revision = %#v, %#v, %v", updatedVersion, versionRevision, err)
	}
	if _, _, _, err := apiRepository.CreateVersionRevision(ctx, catalog.CreateVersionParams{Version: updatedVersion, VersionRevision: versionRevision, Evidence: versionEvidence, ExpectedRevision: version.Revision}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("PostgreSQL repository accepted stale version revision: %v", err)
	}
	if stored, _, err := apiRepository.GetVersionRevision(ctx, tenantA, originalVersionRevision.ID); err != nil || stored.ID != originalVersionRevision.ID {
		t.Fatalf("PostgreSQL original version revision = %#v, %v", stored, err)
	}
	if _, err := apiRepository.GetVersion(ctx, tenantB, version.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL version read returned %v", err)
	}
	initialEvidence := versionEvidence
	validatedRevision, renewedEvidence, _, err := catalogService.RevalidateVersionRevision(authorContext, versionRevision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renewedEvidence.ID == initialEvidence.ID || renewedEvidence.State != catalog.EvidenceComplete || renewedEvidence.ValidationProfile != "catalog-validation-v7" || !renewedEvidence.ExpiresAt.IsZero() || validatedRevision.ContentSetDigest != versionRevision.ContentSetDigest || validatedRevision.EvidenceSetID != renewedEvidence.ID {
		t.Fatalf("PostgreSQL validation run = revision %#v evidence %#v", validatedRevision, renewedEvidence)
	}
	historicalEvidence, err := apiRepository.GetValidationEvidence(ctx, tenantA, versionRevision.ID, initialEvidence.ID)
	if err != nil || historicalEvidence.Digest != initialEvidence.Digest {
		t.Fatalf("PostgreSQL historical validation evidence = %#v, %v", historicalEvidence, err)
	}
	if _, err := apiRepository.GetValidationEvidence(ctx, tenantB, versionRevision.ID, renewedEvidence.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant validation evidence read returned %v", err)
	}
	if _, _, _, err := catalogService.RevalidateVersionRevision(otherReaderContext, versionRevision.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant validation command returned %v", err)
	}
	var validationRunCount, nullValidationExpiryCount, validationAuditCount, validationOutboxCount int
	if err := operatorPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM version_validation_runs WHERE tenant_id=$1 AND version_revision_id=$2),(SELECT count(*) FROM version_validation_runs WHERE tenant_id=$1 AND version_revision_id=$2 AND expires_at IS NULL),(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='version_revision.validated' AND target_id=$2::text),(SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='com.open.agentic.catalog.version.revision.validated.v1' AND subject=$2::text)`, tenantA, versionRevision.ID).Scan(&validationRunCount, &nullValidationExpiryCount, &validationAuditCount, &validationOutboxCount); err != nil || validationRunCount != 2 || nullValidationExpiryCount != 2 || validationAuditCount != 1 || validationOutboxCount != 1 {
		t.Fatalf("PostgreSQL validation persistence = runs %d null expiry %d audit %d outbox %d, %v", validationRunCount, nullValidationExpiryCount, validationAuditCount, validationOutboxCount, err)
	}
	var existingValidationAuditID string
	if err := operatorPool.QueryRow(ctx, `SELECT id FROM audit_events WHERE tenant_id=$1 AND action='version_revision.validated' AND target_id=$2`, tenantA, versionRevision.ID).Scan(&existingValidationAuditID); err != nil {
		t.Fatal(err)
	}
	failedEvidenceID, _ := generator.New(now.Add(40 * time.Nanosecond))
	failedOutboxID, _ := generator.New(now.Add(41 * time.Nanosecond))
	failedEvidence := catalog.EvidenceSet{ID: failedEvidenceID, TenantID: tenantA, SubjectDigest: versionRevision.ContentSetDigest, Digest: "sha256:" + strings.Repeat("9", 64), State: catalog.EvidenceComplete, ValidationProfile: "catalog-validation-v7", PolicyVersion: "integration-policy", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, CreatedAt: now.Add(time.Minute)}
	failedAudit := integrationAudit(t, existingValidationAuditID, tenantA, "integration-admin", "version_revision.validated", "version_revision", versionRevision.ID, "", now.Add(time.Minute))
	failedOutbox := integrationOutbox(t, failedOutboxID, failedAudit)
	if _, err := apiRepository.CreateValidationRun(ctx, catalog.CreateValidationRunParams{TenantID: tenantA, VersionRevisionID: versionRevision.ID, Evidence: failedEvidence, Audit: failedAudit, Outbox: failedOutbox}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("forced validation evidence conflict returned %v", err)
	}
	var failedEvidenceCount int
	if err := operatorPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM version_validation_runs WHERE tenant_id=$1 AND version_revision_id=$2),(SELECT count(*) FROM evidence_sets WHERE tenant_id=$1 AND id=$3)`, tenantA, versionRevision.ID, failedEvidence.ID).Scan(&validationRunCount, &failedEvidenceCount); err != nil || validationRunCount != 2 || failedEvidenceCount != 0 {
		t.Fatalf("failed validation transaction = runs %d evidence %d, %v", validationRunCount, failedEvidenceCount, err)
	}
	if err := apiPool.QueryRow(ctx, `SELECT count(*) FROM version_validation_runs`).Scan(&visibleWithoutContext); err != nil || visibleWithoutContext != 0 {
		t.Fatalf("validation RLS without context exposed %d rows, err=%v", visibleWithoutContext, err)
	}
	_, err = withTenantRead(ctx, apiPool, tenantB, func(tx pgx.Tx) (struct{}, error) {
		var visible int
		if queryErr := tx.QueryRow(ctx, `SELECT count(*) FROM version_validation_runs WHERE version_revision_id=$1`, versionRevision.ID).Scan(&visible); queryErr != nil {
			return struct{}{}, queryErr
		}
		if visible != 0 {
			return struct{}{}, fmt.Errorf("cross-tenant validation run count = %d", visible)
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := catalogService.CreateListing(authorContext, catalog.CreateListingInput{NamespaceID: namespaceA.ID, ResourceID: resource.ID})
	if err != nil {
		t.Fatal(err)
	}
	listingInput := catalog.CreateListingRevisionInput{VersionRevisionID: versionRevision.ID, DisplayName: "PostgreSQL Agent", Description: "Exercises revision preconditions on PostgreSQL.", License: strings.Repeat("𐐀", 257), Tags: []string{"postgres", "integration"}, Categories: []string{"development"}, RepresentativeQueries: []string{"Find the PostgreSQL agent"}, PublisherID: publisher.ID, Visibility: catalog.VisibilityTenant}
	if _, _, err := catalogService.CreateListingRevision(authorContext, listing.ID, listingInput, listing.Revision+1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("PostgreSQL future listing precondition returned %v", err)
	}
	updatedListing, listingRevision, err := catalogService.CreateListingRevision(authorContext, listing.ID, listingInput, listing.Revision)
	if err != nil || updatedListing.Revision != 2 || listingRevision.RevisionNumber != 1 {
		t.Fatalf("PostgreSQL listing revision = %#v, %#v, %v", updatedListing, listingRevision, err)
	}
	if _, _, err := apiRepository.CreateListingRevision(ctx, catalog.CreateListingRevisionParams{Revision: listingRevision, ExpectedRevision: listing.Revision}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("PostgreSQL repository accepted stale listing revision: %v", err)
	}
	listingID, _ := generator.New(now.Add(24 * time.Nanosecond))
	listingAuditID, _ := generator.New(now.Add(25 * time.Nanosecond))
	listingOutboxID, _ := generator.New(now.Add(26 * time.Nanosecond))
	wrongNamespaceListing := catalog.CatalogListing{ID: listingID, TenantID: tenantA, NamespaceID: namespaceA2.ID, ResourceID: resource.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	listingAudit := integrationAudit(t, listingAuditID, tenantA, "test", "catalog_listing.created", "catalog_listing", listingID, "", now)
	listingOutbox := integrationOutbox(t, listingOutboxID, listingAudit)
	if _, err := apiRepository.CreateListing(ctx, catalog.CreateListingParams{Listing: wrongNamespaceListing, Audit: listingAudit, Outbox: listingOutbox}); err == nil {
		t.Fatal("database accepted a listing outside its resource namespace")
	}
	submission, err := catalogService.Submit(authorContext, updatedListing.ID)
	if err != nil || submission.EvidenceSetID != renewedEvidence.ID {
		t.Fatalf("PostgreSQL current-evidence submission = %#v, %v", submission, err)
	}
	_, evidenceAfterSubmission, _, err := catalogService.RevalidateVersionRevision(authorContext, versionRevision.ID)
	if err != nil || evidenceAfterSubmission.ID == renewedEvidence.ID {
		t.Fatalf("PostgreSQL concurrent validation replacement = %#v, %v", evidenceAfterSubmission, err)
	}
	if _, err := identityService.GrantRole(dedicatedAdminContext, identity.GrantRoleInput{Subject: "integration-reviewer", ActorType: identity.ActorHuman, Role: identity.RoleReviewer, Reason: "review integration submissions"}, "identity-integration-reviewer"); err != nil {
		t.Fatal(err)
	}
	reviewerActor, err := identityStore.Resolve(ctx, identity.Principal{Issuer: identityIssuer, Subject: "integration-reviewer", TenantID: tenantA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identityService.GrantRole(dedicatedAdminContext, identity.GrantRoleInput{Subject: "integration-publisher", ActorType: identity.ActorHuman, Role: identity.RolePublisher, Reason: "publish integration releases"}, "identity-integration-publisher"); err != nil {
		t.Fatal(err)
	}
	publisherActor, err := identityStore.Resolve(ctx, identity.Principal{Issuer: identityIssuer, Subject: "integration-publisher", TenantID: tenantA})
	if err != nil {
		t.Fatal(err)
	}
	reviewerContext := identity.WithActor(ctx, reviewerActor)
	publisherContext := identity.WithActor(ctx, publisherActor)
	definitions, err := identityService.ListRoleDefinitions(dedicatedAdminContext, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions.Items {
		if definition.Key == "publication_admin" {
			if _, err := identityService.GrantAuthorizationAssignment(dedicatedAdminContext, identity.AccessAssignmentInput{MembershipID: publisherActor.MembershipID, RoleDefinitionID: definition.ID, Reason: "Explicit host publication authority"}, "host-grant"); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, _, err := apiRepository.DecideSubmission(ctx, catalog.DecideSubmissionParams{ActorMembershipID: reviewerActor.MembershipID, ActorRealmID: reviewerActor.RealmID, TenantID: tenantA, SubmissionID: submission.ID, ExpectedRevision: submission.Revision, ExpectedValidationEvidenceID: renewedEvidence.ID, Actor: "integration-reviewer", Action: "approve", At: now.Add(time.Minute)}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("PostgreSQL approval accepted validation replaced before its transaction: %v", err)
	}
	if _, _, err := catalogService.DecideSubmission(reviewerContext, submission.ID, "request_changes", "validation evidence was replaced", submission.Revision); err != nil {
		t.Fatal(err)
	}
	currentSubmission, err := catalogService.Submit(authorContext, updatedListing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalogService.DecideSubmission(reviewerContext, currentSubmission.ID, "approve", "current validation evidence reviewed", currentSubmission.Revision); err != nil {
		t.Fatal(err)
	}
	publishedListing, localRelease, _, err := catalogService.Publish(publisherContext, updatedListing.ID, catalog.PublishInput{SubmissionID: currentSubmission.ID}, updatedListing.Revision)
	if err != nil || publishedListing.CurrentReleaseID != localRelease.ID {
		t.Fatalf("PostgreSQL local publication = %#v / %#v, %v", publishedListing, localRelease, err)
	}
	t.Run("publisher revision fencing and audit rollback", func(t *testing.T) {
		publisher = testPublisherRevisionAndAuditFailures(t, apiRepository, operatorPool, publisher)
	})
	t.Run("bounded authoring history with exact runtime roles", func(t *testing.T) {
		for _, repository := range []*PostgresRepository{apiRepository, workerRepository} {
			identityReader, err := NewPostgresFederationStore(repository)
			if err != nil {
				t.Fatal(err)
			}
			for _, tenant := range []string{tenantA, tenantB} {
				found, err := identityReader.HasPublishedIdentity(ctx, tenant, resource.CanonicalIdentifier, version.Version)
				if err != nil || found != (tenant == tenantA) {
					t.Fatalf("published identity tenant boundary: %t %v", found, err)
				}
				page := catalog.HistoryPageRequest{Limit: 1}
				versions, err := repository.ListVersions(ctx, tenant, resource.ID, page)
				if err != nil || len(versions) > 1 || (tenant == tenantA && len(versions) != 1) || (tenant == tenantB && len(versions) != 0) {
					t.Fatalf("versions: %d, %v", len(versions), err)
				}
				revisions, err := repository.ListVersionRevisions(ctx, tenant, version.ID, page)
				if err != nil || len(revisions) > 1 || (tenant == tenantA && len(revisions) != 1) || (tenant == tenantB && len(revisions) != 0) {
					t.Fatalf("version revisions: %d, %v", len(revisions), err)
				}
				listings, err := repository.ListListings(ctx, tenant, resource.ID, page)
				if err != nil || len(listings) > 1 || (tenant == tenantA && len(listings) != 1) || (tenant == tenantB && len(listings) != 0) {
					t.Fatalf("listings: %d, %v", len(listings), err)
				}
				listingRevisions, err := repository.ListListingRevisions(ctx, tenant, publishedListing.ID, page)
				if err != nil || len(listingRevisions) > 1 || (tenant == tenantA && len(listingRevisions) != 1) || (tenant == tenantB && len(listingRevisions) != 0) {
					t.Fatalf("listing revisions: %d, %v", len(listingRevisions), err)
				}
				submissions, err := repository.ListSubmissions(ctx, tenant, catalog.SubmissionListRequest{HistoryPageRequest: page, ListingID: publishedListing.ID, State: catalog.SubmissionApproved})
				if repository == workerRepository {
					if catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
						t.Fatalf("worker gained review-read authority: %v", err)
					}
				} else if err != nil || len(submissions) > 1 || (tenant == tenantA && len(submissions) != 1) || (tenant == tenantB && len(submissions) != 0) {
					t.Fatalf("submissions: %d, %v", len(submissions), err)
				}
				releases, err := repository.ListReleases(ctx, tenant, publishedListing.ID, page)
				if err != nil || len(releases) > 1 || (tenant == tenantA && len(releases) != 1) || (tenant == tenantB && len(releases) != 0) {
					t.Fatalf("releases: %d, %v", len(releases), err)
				}
				if tenant == tenantA {
					next, err := repository.ListVersions(ctx, tenant, resource.ID, catalog.HistoryPageRequest{Limit: 1, AfterID: versions[0].ID})
					if err != nil || len(next) > 0 && next[0].ID <= versions[0].ID {
						t.Fatalf("history continuation: %v", err)
					}
				}
			}
		}
		bound, err := catalogService.GetSubmissionEvidence(authorContext, submission.ID)
		if err != nil || bound.ID != submission.EvidenceSetID || bound.Digest != submission.EvidenceDigest {
			t.Fatalf("historical submission evidence: %#v, %v", bound, err)
		}
		if _, err := catalogService.GetSubmissionEvidence(otherReaderContext, submission.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("cross-tenant bound evidence: %v", err)
		}
	})
	t.Run("listing approval preserves an already sealed version", func(t *testing.T) {
		sealed, readErr := catalogService.GetVersion(authorContext, versionRevision.ResourceVersionID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		input := listingInput
		input.DisplayName = "PostgreSQL Agent revised listing"
		changed, _, changeErr := catalogService.CreateListingRevision(authorContext, publishedListing.ID, input, publishedListing.Revision)
		if changeErr != nil {
			t.Fatal(changeErr)
		}
		publishedListing = changed
		review, submitErr := catalogService.Submit(authorContext, changed.ID)
		if submitErr != nil {
			t.Fatal(submitErr)
		}
		decision, unchanged, approveErr := catalogService.DecideSubmission(reviewerContext, review.ID, "approve", "Listing-only metadata reviewed", review.Revision)
		if approveErr != nil || decision.State != catalog.SubmissionApproved || unchanged != sealed {
			t.Fatalf("review of sealed content: state=%s unchanged=%t err=%v", decision.State, unchanged == sealed, approveErr)
		}
		stored, readErr := catalogService.GetVersion(authorContext, sealed.ID)
		if readErr != nil || stored != sealed {
			t.Fatalf("sealed version changed on readback: %v", readErr)
		}
		t.Run("replacement rolls back when PostgreSQL rejects its audit", func(t *testing.T) {
			// Fail after both release writes, through the real API transaction.
			if _, err := operatorPool.Exec(ctx, `CREATE FUNCTION reject_publication_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action = 'listing_release.published' THEN RAISE EXCEPTION 'owned publication audit rejection'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_publication_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_publication_audit()`); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := operatorPool.Exec(ctx, `DROP TRIGGER reject_publication_audit ON audit_events; DROP FUNCTION reject_publication_audit()`); err != nil {
					t.Error(err)
				}
			}()
			counts := func() [3]int {
				var result [3]int
				if err := operatorPool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM listing_releases WHERE tenant_id=$1 AND listing_id=$2),
  (SELECT count(*) FROM audit_events WHERE tenant_id=$1),
  (SELECT count(*) FROM outbox_events WHERE tenant_id=$1)`, tenantA, changed.ID).Scan(&result[0], &result[1], &result[2]); err != nil {
					t.Fatal(err)
				}
				return result
			}
			before := counts()
			prior, err := apiRepository.GetRelease(ctx, tenantA, localRelease.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, err = catalogService.Publish(publisherContext, changed.ID, catalog.PublishInput{SubmissionID: review.ID, PriorDisposition: catalog.ReleaseSuperseded, PriorReason: "Owned replacement rollback"}, changed.Revision)
			var sqlError *pgconn.PgError
			if !errors.As(err, &sqlError) || sqlError.Code != "P0001" || sqlError.Message != "owned publication audit rejection" {
				t.Fatalf("did not reach injected PostgreSQL audit failure: %v", err)
			}
			if after := counts(); after != before {
				t.Fatalf("failed replacement retained release/audit/outbox rows: %v -> %v", before, after)
			}
			retainedListing, err := apiRepository.GetListing(ctx, tenantA, changed.ID)
			if err != nil || retainedListing != changed {
				t.Fatalf("failed replacement changed listing pointer: %#v, %v", retainedListing, err)
			}
			retainedRelease, err := apiRepository.GetRelease(ctx, tenantA, prior.ID)
			if err != nil || retainedRelease != prior {
				t.Fatalf("failed replacement changed prior release: %#v, %v", retainedRelease, err)
			}
		})
	})
	if err := catalogService.DeleteResource(authorContext, resource.ID, "published history must remain immutable", resource.Revision); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("PostgreSQL published-resource deletion returned %v", err)
	}
	externalListing, err := catalogService.CreateListing(authorContext, catalog.CreateListingInput{NamespaceID: namespaceA.ID, ResourceID: externalResource.ID})
	if err != nil {
		t.Fatal(err)
	}
	externalListing, _, err = catalogService.CreateListingRevision(authorContext, externalListing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: replacementExternalRevision.ID, DisplayName: "PostgreSQL External Agent", Description: "Exercises external PostgreSQL discovery.", Tags: []string{"postgres", "external"}, Categories: []string{"development"}, RepresentativeQueries: []string{"Find the external PostgreSQL agent", "Show PostgreSQL external resources"}, PublisherID: publisher.ID, Visibility: catalog.VisibilityPublic}, externalListing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	externalSubmission, err := catalogService.Submit(authorContext, externalListing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalogService.DecideSubmission(reviewerContext, externalSubmission.ID, "approve", "external evidence reviewed", externalSubmission.Revision); err != nil {
		t.Fatal(err)
	}
	externalListing, externalRelease, _, err := catalogService.Publish(publisherContext, externalListing.ID, catalog.PublishInput{SubmissionID: externalSubmission.ID}, externalListing.Revision)
	if err != nil || externalListing.CurrentReleaseID != externalRelease.ID {
		t.Fatalf("PostgreSQL external publication = %#v / %#v, %v", externalListing, externalRelease, err)
	}
	firstSearchPage, err := catalogService.Search(readerContext, catalog.SearchQuery{Text: "PostgreSQL", Limit: 1})
	if err != nil || firstSearchPage.Total != 2 || len(firstSearchPage.Items) != 1 || firstSearchPage.NextCursor == "" {
		t.Fatalf("first bounded PostgreSQL search page = %#v, %v", firstSearchPage, err)
	}
	if firstSearchPage.Items[0].Display == nil || firstSearchPage.Items[0].Display.PublisherName != publisher.DisplayName || firstSearchPage.Items[0].Display.NamespaceName != namespaceA.DisplayName {
		t.Fatal("selected-page directory labels missing")
	}
	renamedPublisher, renamedNamespace := "Renamed current publisher", "Renamed current namespace"
	publisher, err = catalogService.UpdatePublisher(authorContext, publisher.ID, catalog.PatchPublisherInput{DisplayName: &renamedPublisher, Reason: "qualify current directory presentation"}, publisher.Revision)
	if err != nil {
		t.Fatal(err)
	}
	namespaceA, err = catalogService.UpdateNamespace(authorContext, namespaceA.ID, catalog.PatchNamespaceInput{DisplayName: &renamedNamespace, Reason: "qualify current namespace presentation"}, namespaceA.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogService.Search(readerContext, catalog.SearchQuery{Text: "PostgreSQL", Limit: 1, Cursor: firstSearchPage.NextCursor}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("publisher rename continued an old search: %v", err)
	}
	firstSearchPage, err = catalogService.Search(readerContext, catalog.SearchQuery{Text: "PostgreSQL", Limit: 1})
	if err != nil || firstSearchPage.NextCursor == "" {
		t.Fatalf("restart after publisher rename: %+v %v", firstSearchPage, err)
	}
	secondSearchPage, err := catalogService.Search(readerContext, catalog.SearchQuery{Text: "PostgreSQL", Limit: 1, Cursor: firstSearchPage.NextCursor})
	if err != nil || secondSearchPage.Total != 2 || len(secondSearchPage.Items) != 1 || secondSearchPage.NextCursor != "" || secondSearchPage.Items[0].Release.ID == firstSearchPage.Items[0].Release.ID {
		t.Fatalf("second bounded PostgreSQL search page = %#v, %v", secondSearchPage, err)
	}
	exactSearch, err := catalogService.Search(readerContext, catalog.SearchQuery{Text: resource.CanonicalIdentifier, Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"postgres"}, Publisher: publisher.ID, Visibility: catalog.VisibilityTenant})
	if err != nil || exactSearch.Total != 1 || len(exactSearch.Items) != 1 || exactSearch.Items[0].Release.ID != localRelease.ID || exactSearch.Items[0].RelevanceScore != 100 {
		t.Fatalf("exact filtered PostgreSQL search = %#v, %v", exactSearch, err)
	}
	if secondSearchPage.Items[0].Display == nil || secondSearchPage.Items[0].Display.PublisherName != renamedPublisher || secondSearchPage.Items[0].Display.NamespaceName != renamedNamespace {
		t.Fatal("current names lost between continuation pages")
	}
	display := exactSearch.Items[0].Display
	if display == nil || !display.LicenseOmitted || display.License != "" {
		t.Fatalf("oversized retained license was loaded into collection: %+v", display)
	}
	filteredSearch, err := catalogService.Search(readerContext, catalog.SearchQuery{Text: "external PostgreSQL", Tags: []string{"external"}, Visibility: catalog.VisibilityPublic})
	if err != nil || filteredSearch.Total != 1 || len(filteredSearch.Items) != 1 || filteredSearch.Items[0].Release.ID != externalRelease.ID || filteredSearch.Items[0].RelevanceScore <= 0 {
		t.Fatalf("lexical filtered PostgreSQL search = %#v, %v", filteredSearch, err)
	}
	explored, err := catalogService.Explore(readerContext, catalog.ExploreRequest{Query: catalog.SearchQuery{Text: "PostgreSQL", Kinds: []catalog.Kind{catalog.KindAgent}}, Facets: []catalog.FacetRequest{{Field: "kind"}, {Field: "visibility"}, {Field: "publisher"}, {Field: "tag"}, {Field: "category"}, {Field: "namespace"}, {Field: "trustState"}, {Field: "source"}}})
	if err != nil || explored.Matched != 2 || len(explored.Facets) != 8 {
		t.Fatalf("bounded PostgreSQL facets = %#v, %v", explored, err)
	}
	limitedFacets, err := catalogService.Explore(readerContext, catalog.ExploreRequest{Query: catalog.SearchQuery{Text: "PostgreSQL"}, Facets: []catalog.FacetRequest{{Field: "tag", Limit: 1}}})
	if err != nil || len(limitedFacets.Facets["tag"].Buckets) != 1 || limitedFacets.Facets["tag"].Buckets[0].Value != "postgres" || limitedFacets.Facets["tag"].Buckets[0].Count != 2 || limitedFacets.Facets["tag"].OtherCount != 2 {
		t.Fatalf("bounded PostgreSQL facet overflow = %#v, %v", limitedFacets, err)
	}
	loadedCatalogRecord, err := catalogService.GetCatalogRecord(readerContext, localRelease.ID)
	if err != nil || loadedCatalogRecord.Release.ID != localRelease.ID || loadedCatalogRecord.Resource.ID != resource.ID {
		t.Fatalf("exact PostgreSQL Catalog record = %#v, %v", loadedCatalogRecord, err)
	}
	if loadedCatalogRecord.Display == nil || loadedCatalogRecord.Display.PublisherName != renamedPublisher || loadedCatalogRecord.Display.NamespaceName != renamedNamespace || !loadedCatalogRecord.Display.LicenseOmitted || loadedCatalogRecord.ListingRevision.License != strings.Repeat("𐐀", 257) {
		t.Fatal("full retained declaration or current labels lost")
	}
	if page, err := catalogService.Search(otherReaderContext, catalog.SearchQuery{Text: "PostgreSQL"}); err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("cross-tenant PostgreSQL search = %#v, %v", page, err)
	}
	if _, err := catalogService.GetCatalogRecord(otherReaderContext, localRelease.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant exact Catalog record returned %v", err)
	}
	boundedResource, err := catalogService.CreateResource(authorContext, catalog.CreateResourceInput{NamespaceID: namespaceA.ID, CanonicalIdentifier: "urn:air:catalog.example.com:alpha:bounded-selection", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	_, boundedRevision, _, err := catalogService.CreateVersion(authorContext, boundedResource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: representation("Exercises bounded publication selection")})
	if err != nil {
		t.Fatal(err)
	}
	boundedListing, err := catalogService.CreateListing(authorContext, catalog.CreateListingInput{NamespaceID: namespaceA.ID, ResourceID: boundedResource.ID})
	if err != nil {
		t.Fatal(err)
	}
	boundedListing, _, err = catalogService.CreateListingRevision(authorContext, boundedListing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: boundedRevision.ID, Extensions: postgresArtworkInput(), DisplayName: "Bounded Selection Agent", Description: "Exercises limit plus one publication selection.", PublisherID: publisher.ID, Visibility: catalog.VisibilityTenant}, boundedListing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	boundedSubmission, err := catalogService.Submit(authorContext, boundedListing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalogService.DecideSubmission(reviewerContext, boundedSubmission.ID, "approve", "bounded publication selection reviewed", boundedSubmission.Revision); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := catalogService.Publish(publisherContext, boundedListing.ID, catalog.PublishInput{SubmissionID: boundedSubmission.ID}, boundedListing.Revision); err != nil {
		t.Fatal(err)
	}
	artworkPage, artErr := catalogService.Search(readerContext, catalog.SearchQuery{Text: "Bounded Selection Agent"})
	if artErr != nil || len(artworkPage.Items) != 1 || artworkPage.Items[0].Artwork == nil || artworkPage.Items[0].Artwork.Descriptor.Icon.Alt != "Fixture artwork" {
		t.Fatalf("bounded local artwork projection: %#v %v", artworkPage, artErr)
	}
	publicPublication, err := apiRepository.PublicationRecords(ctx, tenantA, catalog.PublicationRecordRequest{Limit: 1})
	if err != nil || publicPublication.Overflow || len(publicPublication.Records) != 1 || publicPublication.Records[0].Release.ID != externalRelease.ID {
		t.Fatalf("bounded public publication projection = %#v, %v", publicPublication, err)
	}
	tenantPublicationOverflow, err := apiRepository.PublicationRecords(ctx, tenantA, catalog.PublicationRecordRequest{IncludeTenant: true, Limit: 1})
	if err != nil || !tenantPublicationOverflow.Overflow || len(tenantPublicationOverflow.Records) != 0 {
		t.Fatalf("publication limit+1 projection = %#v, %v", tenantPublicationOverflow, err)
	}
	tenantPublication, err := apiRepository.PublicationRecords(ctx, tenantA, catalog.PublicationRecordRequest{IncludeTenant: true, Limit: 2})
	if err != nil || tenantPublication.Overflow || len(tenantPublication.Records) != 2 || tenantPublication.Records[0].Resource.CanonicalIdentifier > tenantPublication.Records[1].Resource.CanonicalIdentifier {
		t.Fatalf("complete deterministic publication projection = %#v, %v", tenantPublication, err)
	}
	for _, record := range tenantPublication.Records {
		if record.Resource.ID == boundedResource.ID {
			if catalog.ListingArtwork(record.ListingRevision.Extensions) == nil || len(record.ListingRevision.Extensions) != 1 {
				t.Fatalf("publication sealed artwork projection: %#v", record.ListingRevision.Extensions)
			}
		}
	}
	if other, err := apiRepository.PublicationRecords(ctx, tenantB, catalog.PublicationRecordRequest{IncludeTenant: true, Limit: 2}); err != nil || other.Overflow || len(other.Records) != 0 {
		t.Fatalf("cross-tenant publication projection = %#v, %v", other, err)
	}
	releaseIDs, releaseOverflow, err := apiRepository.PublishedReleaseIDsByVersionRevision(ctx, tenantA, versionRevision.ID, 1)
	if err != nil || releaseOverflow || len(releaseIDs) != 1 || releaseIDs[0] != localRelease.ID {
		t.Fatalf("exact release safety lookup = %#v, overflow=%t, %v", releaseIDs, releaseOverflow, err)
	}
	if releaseIDs, releaseOverflow, err := apiRepository.PublishedReleaseIDsByVersionRevision(ctx, tenantB, versionRevision.ID, 1); err != nil || releaseOverflow || len(releaseIDs) != 0 {
		t.Fatalf("cross-tenant release safety lookup = %#v, overflow=%t, %v", releaseIDs, releaseOverflow, err)
	}
	staleCursor := firstSearchPage.NextCursor
	if _, err := catalogService.CreateResource(authorContext, catalog.CreateResourceInput{NamespaceID: namespaceA.ID, CanonicalIdentifier: "urn:air:catalog.example.com:alpha:search-generation", Kind: catalog.KindAgent}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogService.Search(readerContext, catalog.SearchQuery{Text: "PostgreSQL", Limit: 1, Cursor: staleCursor}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("PostgreSQL search accepted a stale generation cursor: %v", err)
	}

	publicationStore, err := NewPostgresPublicationStore(apiRepository, "catalog.example.com")
	if err != nil {
		t.Fatal(err)
	}
	snapshotID, _ := generator.New(now.Add(3 * time.Nanosecond))
	snapshot := publication.Snapshot{ID: snapshotID, TenantID: tenantA, Host: "catalog.example.com", Audience: publication.AudienceAnonymous, Generation: 1, ARDProfile: "ard-test", AICatalogProfile: "ai-test", State: publication.SnapshotCandidate, ARD: publication.ARDManifest{Entries: []publication.Entry{}}, AICatalog: publication.AICatalog{SpecVersion: "1.0", Entries: []publication.AICatalogEntry{}}, ETag: `"one"`, AICatalogETag: `"two"`, CreatedAt: now}
	if _, err := publicationStore.SaveCandidate(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	firstActivationAt := now.Add(999 * time.Nanosecond)
	if _, err := publicationStore.Activate(ctx, tenantA, snapshot.ID, firstActivationAt); err != nil {
		t.Fatal(err)
	}
	active, err := publicationStore.Active(ctx, tenantA)
	if err != nil || active.ID != snapshot.ID || active.State != publication.SnapshotActive || active.ActivatedAt == nil || !active.ActivatedAt.Equal(firstActivationAt.Truncate(time.Microsecond)) {
		t.Fatalf("active publication = %#v, %v", active, err)
	}
	if _, err := TransitionIdentityRealm(ctx, operatorPool, IdentityRealmTransitionInput{TenantID: tenantA, RealmID: provisionedA.RealmID, ExpectedRevision: 3, TargetState: identity.RealmSuspended, Operator: "integration-operator", Reason: "active discovery must block total identity shutdown", TenantDiscoveryDisabled: true, AcknowledgeOutbox: true}); err == nil || !strings.Contains(err.Error(), "active discovery snapshots") {
		t.Fatalf("final identity realm suspension with active discovery returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE publication_snapshots SET document=jsonb_set(document,'{host}',to_jsonb($2::text),false) WHERE tenant_id=$1 AND id=$3`, tenantA, "tampered.example.com", snapshot.ID); err == nil {
		t.Fatal("immutable publication snapshot accepted a document rewrite")
	}
	if restored, err := publicationStore.Active(ctx, tenantA); err != nil || restored.ID != snapshot.ID {
		t.Fatalf("restored immutable publication row = %#v, %v", restored, err)
	}
	if _, err := operatorPool.Exec(ctx, `
		UPDATE publication_snapshots
		   SET state='retired',
		       document=jsonb_set(jsonb_set(document,'{state}','"retired"'::jsonb),'{activatedAt}','"2099-01-01T00:00:00Z"'::jsonb)
		 WHERE tenant_id=$1 AND id=$2`, tenantA, snapshot.ID); err == nil || !strings.Contains(err.Error(), "cannot rewrite its activation time") {
		t.Fatalf("publication retirement activation-time rewrite returned %v", err)
	}
	secondSnapshotID, _ := generator.New(now.Add(4 * time.Nanosecond))
	secondSnapshot := snapshot
	secondSnapshot.ID = secondSnapshotID
	secondSnapshot.Generation = 2
	secondSnapshot.ETag = `"three"`
	secondSnapshot.AICatalogETag = `"four"`
	secondSnapshot.State = publication.SnapshotCandidate
	secondSnapshot.ActivatedAt = nil
	if _, err := publicationStore.SaveCandidate(ctx, secondSnapshot); err != nil {
		t.Fatal(err)
	}
	t.Run("publication clock backstep preserves the active snapshot", func(t *testing.T) {
		_, err := publicationStore.Activate(ctx, tenantA, secondSnapshot.ID, now.Add(-2*time.Second))
		var postgresError *pgconn.PgError
		if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !errors.As(err, &postgresError) || postgresError.Code != "P0001" || postgresError.Message != "active publication snapshot activation time is inconsistent" {
			t.Fatalf("backdated activation returned %v", err)
		}
		current, err := publicationStore.Active(ctx, tenantA)
		if err != nil || current.ID != snapshot.ID || current.State != publication.SnapshotActive {
			t.Fatalf("backdated activation changed the active snapshot: %#v, %v", current, err)
		}
		candidate, err := publicationStore.Get(ctx, tenantA, secondSnapshot.ID)
		if err != nil || candidate.State != publication.SnapshotCandidate || candidate.ActivatedAt != nil {
			t.Fatalf("backdated activation changed its candidate: %#v, %v", candidate, err)
		}
	})
	// A forward timestamp succeeds after the rejected transition.
	if _, err := publicationStore.Activate(ctx, tenantA, secondSnapshot.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	history, err := publicationStore.ListSummaries(ctx, tenantA, publication.SnapshotListRequest{Limit: 10})
	if err != nil || len(history) != 2 || history[0].ID != secondSnapshot.ID || history[0].State != publication.SnapshotActive || history[1].ID != snapshot.ID || history[1].State != publication.SnapshotRetired {
		t.Fatalf("replacement publication history = %#v, %v", history, err)
	}
	rollbackAt := now.Add(2*time.Minute + 999*time.Nanosecond)
	if rolledBack, err := publicationStore.Rollback(ctx, tenantA, snapshot.ID, rollbackAt); err != nil || rolledBack.ActivatedAt == nil || !rolledBack.ActivatedAt.Equal(rollbackAt.Truncate(time.Microsecond)) {
		t.Fatalf("rollback activation timestamp = %#v, %v", rolledBack.ActivatedAt, err)
	}
	history, err = publicationStore.ListSummaries(ctx, tenantA, publication.SnapshotListRequest{Limit: 10})
	if err != nil || len(history) != 2 || history[0].State != publication.SnapshotRetired || history[1].State != publication.SnapshotActive {
		t.Fatalf("rollback publication history = %#v, %v", history, err)
	}
	firstPublicationPage, err := publicationStore.ListSummaries(ctx, tenantA, publication.SnapshotListRequest{Limit: 1})
	if err != nil || len(firstPublicationPage) != 1 || firstPublicationPage[0].ID != secondSnapshot.ID || firstPublicationPage[0].Generation != 2 {
		t.Fatalf("first PostgreSQL publication-snapshot page = %#v, %v", firstPublicationPage, err)
	}
	// Rollback leaves the active generation behind a newer retired generation.
	// Exact API-role reads must apply state before LIMIT, with host and tenant fences.
	for state, want := range map[publication.SnapshotState]string{publication.SnapshotActive: snapshot.ID, publication.SnapshotRetired: secondSnapshot.ID, publication.SnapshotCandidate: ""} {
		filtered, filterErr := publicationStore.ListSummaries(ctx, tenantA, publication.SnapshotListRequest{State: state, Limit: 1})
		if filterErr != nil || want == "" && len(filtered) != 0 || want != "" && (len(filtered) != 1 || filtered[0].ID != want) {
			t.Fatalf("state %s before PostgreSQL limit: %+v, %v", state, filtered, filterErr)
		}
	}
	filteredTail, filterErr := publicationStore.ListSummaries(ctx, tenantA, publication.SnapshotListRequest{State: publication.SnapshotActive, Limit: 1, AfterGeneration: 1, AfterID: snapshot.ID})
	if filterErr != nil || len(filteredTail) != 0 {
		t.Fatalf("filtered PostgreSQL continuation: %+v, %v", filteredTail, filterErr)
	}
	otherHostStore, hostErr := NewPostgresPublicationStore(apiRepository, "other-publication.example.com")
	if hostErr != nil {
		t.Fatal(hostErr)
	}
	if other, hostErr := otherHostStore.ListSummaries(ctx, tenantA, publication.SnapshotListRequest{State: publication.SnapshotActive, Limit: 1}); hostErr != nil || len(other) != 0 {
		t.Fatalf("cross-host active lookup: %+v, %v", other, hostErr)
	}
	if other, tenantErr := publicationStore.ListSummaries(ctx, tenantB, publication.SnapshotListRequest{State: publication.SnapshotActive, Limit: 1}); tenantErr != nil || len(other) != 0 {
		t.Fatalf("cross-tenant active lookup: %+v, %v", other, tenantErr)
	}
	secondPublicationPage, err := publicationStore.ListSummaries(ctx, tenantA, publication.SnapshotListRequest{AfterGeneration: firstPublicationPage[0].Generation, AfterID: firstPublicationPage[0].ID, Limit: 1})
	if err != nil || len(secondPublicationPage) != 1 || secondPublicationPage[0].ID != snapshot.ID || secondPublicationPage[0].Generation != 1 {
		t.Fatalf("second PostgreSQL publication-snapshot page = %#v, %v", secondPublicationPage, err)
	}
	finalPublicationPage, err := publicationStore.ListSummaries(ctx, tenantA, publication.SnapshotListRequest{AfterGeneration: secondPublicationPage[0].Generation, AfterID: secondPublicationPage[0].ID, Limit: 1})
	if err != nil || len(finalPublicationPage) != 0 {
		t.Fatalf("final PostgreSQL publication-snapshot page = %#v, %v", finalPublicationPage, err)
	}
	if otherTenantPublicationPage, err := publicationStore.ListSummaries(ctx, tenantB, publication.SnapshotListRequest{Limit: 1}); err != nil || len(otherTenantPublicationPage) != 0 {
		t.Fatalf("cross-tenant PostgreSQL publication-snapshot page = %#v, %v", otherTenantPublicationPage, err)
	}
	timeGuardSnapshotID, _ := generator.New(now.Add(5 * time.Nanosecond))
	timeGuardSnapshot := snapshot
	timeGuardSnapshot.ID = timeGuardSnapshotID
	timeGuardSnapshot.Generation = 3
	timeGuardSnapshot.ETag = `"five"`
	timeGuardSnapshot.AICatalogETag = `"six"`
	timeGuardSnapshot.CreatedAt = now.Add(3*time.Minute + 999*time.Nanosecond)
	timeGuardSnapshot, err = publicationStore.SaveCandidate(ctx, timeGuardSnapshot)
	if err != nil || !timeGuardSnapshot.CreatedAt.Equal(now.Add(3*time.Minute).UTC()) {
		t.Fatalf("normalized PostgreSQL publication candidate = %#v, %v", timeGuardSnapshot, err)
	}
	if storedTimeGuardSnapshot, getErr := publicationStore.Get(ctx, tenantA, timeGuardSnapshot.ID); getErr != nil || storedTimeGuardSnapshot.ID != timeGuardSnapshot.ID {
		t.Fatalf("stored PostgreSQL publication candidate = %#v, %v", storedTimeGuardSnapshot, getErr)
	}
	t.Run("publication manager tolerates a backward clock", func(t *testing.T) {
		serviceClock := clock.NewFake(now.Add(4 * time.Minute))
		manager, err := publication.NewManager(publication.Options{
			Records: catalogService, Store: publicationStore, Clock: serviceClock, IDs: generator,
			BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com",
			ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: []byte("01234567890123456789012345678901"), Evidence: apiRepository,
		})
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := manager.BuildCandidate(publisherContext, tenantA)
		if err != nil {
			t.Fatal(err)
		}
		serviceClock.Advance(-2 * time.Second)
		activated, err := manager.Activate(publisherContext, tenantA, candidate.ID, "Exercise backward-clock activation")
		if err != nil || activated.ActivatedAt == nil || !activated.ActivatedAt.Equal(candidate.CreatedAt) {
			t.Fatalf("backward-clock activation = %#v, %v", activated, err)
		}
		replacement, err := manager.BuildCandidate(publisherContext, tenantA)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Activate(publisherContext, tenantA, replacement.ID, "Exercise replacement activation"); err != nil {
			t.Fatal(err)
		}
		rolledBack, err := manager.Rollback(publisherContext, tenantA, candidate.ID, "Exercise backward-clock rollback")
		want := candidate.CreatedAt.Add(time.Microsecond)
		if err != nil || rolledBack.ActivatedAt == nil || !rolledBack.ActivatedAt.Equal(want) {
			t.Fatalf("backward-clock rollback = %#v, %v", rolledBack, err)
		}
		retained, err := publicationStore.Active(ctx, tenantA)
		if err != nil || retained.ActivatedAt == nil || !retained.ActivatedAt.Equal(want) {
			t.Fatalf("retained rollback metadata = %#v, %v", retained, err)
		}
	})

	federationStore, err := NewPostgresFederationStore(apiRepository)
	if err != nil {
		t.Fatal(err)
	}
	workerFederationStore, err := NewPostgresFederationStore(workerRepository)
	if err != nil {
		t.Fatal(err)
	}
	inspectionID, _ := generator.New(now.Add(4 * time.Nanosecond))
	inspection := federation.Inspection{ID: inspectionID, TenantID: tenantA, RequestedURL: "https://remote.example", ResolvedURL: "https://remote.example/.well-known/ard.json", SourceType: federation.SourceARDManifest, ProfileID: "ard-test", MediaType: "application/ard+json", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if _, err := federationStore.SaveInspection(ctx, inspection); err != nil {
		t.Fatal(err)
	}
	if stored, err := federationStore.GetInspection(ctx, tenantA, inspection.ID); err != nil || stored.ResolvedURL != inspection.ResolvedURL {
		t.Fatalf("inspection = %#v, %v", stored, err)
	}
	targetSourceID, _ := generator.New(now.Add(5 * time.Nanosecond))
	policyDigest := "sha256:" + strings.Repeat("a", 64)
	assignment := []federation.DiscoveryAssignment{{Host: "catalog.example.com", Audience: federation.AudienceAnonymous}}
	targetSource := federation.Source{ID: targetSourceID, TenantID: tenantA, DisplayName: "Referral target", Type: federation.SourceRegistry, Endpoint: "https://target.example/search", RegistrySearchURL: "https://target.example/search", Status: federation.SourceActive, ProfileID: "ard-test", RetrievalMode: federation.RetrievalLive, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: federation.UpdateAutomatic, Live: federation.LivePolicy{Enabled: true, ReferralOnly: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true, FederationMode: "none"}, DiscoveryAssignments: assignment, PolicyID: "integration", PolicyRevision: 1, PolicyDigest: policyDigest, Revision: 1, CreatedBy: "source-admin", CreatedAt: now, UpdatedAt: now}
	if _, err := federationStore.CreateSource(ctx, targetSource); err != nil {
		t.Fatal(err)
	}
	sourceID, _ := generator.New(now.Add(6 * time.Nanosecond))
	// #nosec G101 -- credential revision is a non-secret synthetic evidence identifier.
	source := federation.Source{ID: sourceID, TenantID: tenantA, DisplayName: "Remote", Type: federation.SourceARDManifest, Endpoint: inspection.ResolvedURL, RegistrySearchURL: "https://remote.example/search", Status: federation.SourceActive, ProfileID: "ard-test", RetrievalMode: federation.RetrievalHybrid, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: federation.UpdateAutomatic, Live: federation.LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true, FederationMode: "referrals"}, Referrals: federation.ReferralPolicy{Enabled: true, MaxHops: 1, ApprovedSourceIDs: []string{targetSourceID}, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: assignment, PolicyID: "integration", PolicyRevision: 1, PolicyDigest: policyDigest, CredentialRevision: "credential-1", Revision: 1, CreatedBy: "source-admin", CreatedAt: now, UpdatedAt: now}
	if _, err := federationStore.CreateSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	t.Run("source clock backstep preserves the approved revision", func(t *testing.T) {
		proposed := source
		proposed.Revision++
		proposed.DisplayName = "Backdated source update"
		proposed.UpdatedAt = now.Add(-2 * time.Second)
		_, err := federationStore.UpdateSource(ctx, proposed, source.Revision)
		var postgresError *pgconn.PgError
		if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !errors.As(err, &postgresError) || postgresError.Code != "P0001" || postgresError.Message != "federation source revision transition is invalid" {
			t.Fatalf("backdated source update returned %v", err)
		}
		retained, err := federationStore.GetSource(ctx, tenantA, source.ID)
		if err != nil || retained.Revision != source.Revision || retained.DisplayName != source.DisplayName || !retained.UpdatedAt.Equal(source.UpdatedAt) {
			t.Fatalf("backdated source update changed approved state: %#v, %v", retained, err)
		}
	})
	// The same next revision remains available once time moves forward.
	source.DisplayName = "Remote updated"
	source.Revision = 2
	source.UpdatedAt = now.Add(time.Second)
	if _, err := federationStore.UpdateSource(ctx, source, 1); err != nil {
		t.Fatal(err)
	}
	listedSources, err := federationStore.ListSourcePage(ctx, tenantA, federation.SourceListRequest{Limit: 10})
	if err != nil || len(listedSources) != 2 || listedSources[0].ID != targetSource.ID || listedSources[1].ID != source.ID {
		t.Fatalf("PostgreSQL source list = %#v, %v", listedSources, err)
	}
	if otherTenantSources, listErr := federationStore.ListSourcePage(ctx, tenantB, federation.SourceListRequest{Limit: 10}); listErr != nil || len(otherTenantSources) != 0 {
		t.Fatalf("cross-tenant PostgreSQL source list = %#v, %v", otherTenantSources, listErr)
	}
	if connected, err := federationStore.SourceEndpointConnected(ctx, tenantA, "", source.RegistrySearchURL); err != nil || !connected {
		t.Fatalf("exact source endpoint lookup = %t, %v", connected, err)
	}
	if connected, err := federationStore.SourceEndpointConnected(ctx, tenantA, source.ID, source.RegistrySearchURL); err != nil || connected {
		t.Fatalf("self-excluding source endpoint lookup = %t, %v", connected, err)
	}
	if connected, err := federationStore.SourceEndpointConnected(ctx, tenantB, "", source.RegistrySearchURL); err != nil || connected {
		t.Fatalf("cross-tenant source endpoint lookup = %t, %v", connected, err)
	}
	discoveryLive, discoveryLiveOverflow, err := federationStore.ListDiscoveryLiveSources(ctx, tenantA, "catalog.example.com", federation.AudienceAnonymous, policyDigest, 1)
	if err != nil || discoveryLiveOverflow || len(discoveryLive) != 1 || discoveryLive[0].ID != source.ID {
		t.Fatalf("bounded discovery live source selection = %#v, overflow=%t, %v", discoveryLive, discoveryLiveOverflow, err)
	}
	if excluded, overflow, err := federationStore.ListDiscoveryLiveSources(ctx, tenantA, "catalog.example.com", federation.AudienceAuthenticated, policyDigest, 1); err != nil || overflow || len(excluded) != 0 {
		t.Fatalf("audience-excluded live source selection = %#v, overflow=%t, %v", excluded, overflow, err)
	}
	catalogLive, catalogLiveOverflow, err := federationStore.ListCatalogLiveSources(ctx, tenantA, policyDigest, 1)
	if err != nil || catalogLiveOverflow || len(catalogLive) != 1 || catalogLive[0].ID != source.ID {
		t.Fatalf("bounded Catalog live source selection = %#v, overflow=%t, %v", catalogLive, catalogLiveOverflow, err)
	}
	previewID, _ := generator.New(now.Add(61 * time.Nanosecond))
	preview := federation.ChangePreview{
		ID:                   previewID,
		TenantID:             tenantA,
		SourceID:             source.ID,
		SourceRevision:       source.Revision,
		CurrentMatched:       2,
		ProposedMatched:      1,
		ExcludedObservations: 1,
		SendsQueriesUpstream: true,
		ConfigurationDigest:  "sha256:" + strings.Repeat("e", 64),
		Digest:               "sha256:" + strings.Repeat("f", 64),
		CreatedAt:            now.Add(time.Second),
		ExpiresAt:            now.Add(time.Hour),
	}
	if storedPreview, saveErr := federationStore.SaveChangePreview(ctx, preview); saveErr != nil || storedPreview.Digest != preview.Digest {
		t.Fatalf("saved PostgreSQL source change preview = %#v, %v", storedPreview, saveErr)
	}
	if storedPreview, getErr := federationStore.GetChangePreview(ctx, tenantA, preview.Digest); getErr != nil || storedPreview.ID != preview.ID || storedPreview.ExcludedObservations != preview.ExcludedObservations {
		t.Fatalf("read PostgreSQL source change preview = %#v, %v", storedPreview, getErr)
	}
	if _, getErr := federationStore.GetChangePreview(ctx, tenantB, preview.Digest); catalog.ErrorCodeOf(getErr) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL source change preview read returned %v", getErr)
	}
	if _, mutationErr := operatorPool.Exec(ctx, `UPDATE source_change_previews SET document='{}'::jsonb WHERE tenant_id=$1 AND id=$2`, tenantA, preview.ID); mutationErr == nil || !strings.Contains(mutationErr.Error(), "source change preview remains authoritative or immutable") {
		t.Fatalf("source change preview history mutation returned %v", mutationErr)
	}
	firstSourcePage, err := federationStore.ListSourcePage(ctx, tenantA, federation.SourceListRequest{Limit: 1})
	if err != nil || len(firstSourcePage) != 1 || firstSourcePage[0].ID != targetSource.ID {
		t.Fatalf("first PostgreSQL source page = %#v, %v", firstSourcePage, err)
	}
	secondSourcePage, err := federationStore.ListSourcePage(ctx, tenantA, federation.SourceListRequest{AfterDisplayName: firstSourcePage[0].DisplayName, AfterID: firstSourcePage[0].ID, Limit: 1})
	if err != nil || len(secondSourcePage) != 1 || secondSourcePage[0].ID != source.ID {
		t.Fatalf("second PostgreSQL source page = %#v, %v", secondSourcePage, err)
	}
	finalSourcePage, err := federationStore.ListSourcePage(ctx, tenantA, federation.SourceListRequest{AfterDisplayName: secondSourcePage[0].DisplayName, AfterID: secondSourcePage[0].ID, Limit: 1})
	if err != nil || len(finalSourcePage) != 0 {
		t.Fatalf("final PostgreSQL source page = %#v, %v", finalSourcePage, err)
	}
	if otherTenantSourcePage, err := federationStore.ListSourcePage(ctx, tenantB, federation.SourceListRequest{Limit: 1}); err != nil || len(otherTenantSourcePage) != 0 {
		t.Fatalf("cross-tenant PostgreSQL source page = %#v, %v", otherTenantSourcePage, err)
	}
	indexedReferralID, _ := generator.New(now.Add(60 * time.Nanosecond))
	indexedReferral := federation.Source{ID: indexedReferralID, TenantID: tenantA, DisplayName: "Indexed referral", Type: federation.SourceRegistry, Endpoint: "https://indexed-referral.example/ard.json", RegistrySearchURL: "https://indexed-referral.example/search", Status: federation.SourceActive, ProfileID: "ard-test", RetrievalMode: federation.RetrievalIndexed, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: federation.UpdateManual, Referrals: federation.ReferralPolicy{Enabled: true, MaxHops: 1, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: assignment, PolicyID: "integration", PolicyRevision: 1, PolicyDigest: policyDigest, Revision: 1, CreatedBy: "source-admin", CreatedAt: now, UpdatedAt: now}
	if _, err := federationStore.CreateSource(ctx, indexedReferral); err != nil {
		t.Fatal(err)
	}
	discoveryReferrals, discoveryReferralOverflow, err := federationStore.ListDiscoveryReferralSources(ctx, tenantA, "catalog.example.com", federation.AudienceAnonymous, policyDigest, 2)
	if err != nil || discoveryReferralOverflow || len(discoveryReferrals) != 2 || !slices.ContainsFunc(discoveryReferrals, func(candidate federation.Source) bool { return candidate.ID == source.ID }) || !slices.ContainsFunc(discoveryReferrals, func(candidate federation.Source) bool { return candidate.ID == indexedReferral.ID }) {
		t.Fatalf("bounded discovery referral source selection = %#v, overflow=%t, %v", discoveryReferrals, discoveryReferralOverflow, err)
	}
	tracingSourceID, _ := generator.New(now.Add(62 * time.Nanosecond))
	tracingSource := federation.Source{
		ID: tracingSourceID, TenantID: tenantA, DisplayName: "Audit race source", Type: federation.SourceARDManifest,
		Endpoint: "https://audit-race.example/ard.json", Status: federation.SourceActive, ProfileID: "ard-0.91-aa3e598",
		RetrievalMode: federation.RetrievalIndexed, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: federation.UpdateManual,
		Live: federation.LivePolicy{FederationMode: "none"}, PolicyID: "integration-policy", PolicyRevision: 1, PolicyDigest: policyDigest,
		Revision: 1, CreatedBy: dedicatedActor.MembershipID, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := federationStore.CreateSource(ctx, tracingSource); err != nil {
		t.Fatal(err)
	}
	sourceActor := dedicatedActor
	sourceActor.Roles = append(slices.Clone(sourceActor.Roles), identity.RoleSourceAdmin)
	concurrentFetcher := &postgresFederationFetcher{
		result:       platformfetcher.Result{Status: 200, FinalURL: tracingSource.Endpoint, MediaType: "application/ard+json", Body: []byte(`{"entries":[]}`)},
		policyDigest: policyDigest,
	}
	concurrentFetcher.beforeReturn = func() error {
		winner := tracingSource
		winner.DisplayName = "Concurrent winner"
		winner.Revision = 2
		winner.UpdatedAt = now.Add(91 * time.Second)
		_, updateErr := federationStore.UpdateSource(identity.WithActor(context.Background(), sourceActor), winner, tracingSource.Revision)
		return updateErr
	}
	auditClock := clock.NewFake(now.Add(90 * time.Second))
	auditManager, err := federation.NewManager(federationStore, concurrentFetcher, auditClock, generator, federation.WithEvidenceSink(apiRepository, false))
	if err != nil {
		t.Fatal(err)
	}
	commandConnection, err := apiPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command := &commandTransaction{pool: apiPool, conn: commandConnection, tenantID: tenantA}
	commandComplete := false
	defer func() {
		if !commandComplete {
			command.abort(context.Background(), catalog.E(catalog.CodeInternal, "source-test integration command did not complete"))
		}
		commandConnection.Release()
	}()
	commandContext := identity.WithActor(ctx, sourceActor)
	commandContext = audit.WithExecution(commandContext, audit.Execution{RequestID: "source-test-race", AuthorizationDigest: sourceActor.AuthorizationDigest()})
	t.Run("foreign artwork immutable overlays and RLS", func(t *testing.T) {
		qualifyArtworkOverlayPostgres(t, auditManager, federationStore, workerFederationStore, operatorPool, apiPool, workerPool, commandContext, source, tenantB)
	})
	commandContext = withCommandTransaction(commandContext, command)
	if result, testErr := auditManager.TestSourceAtRevision(commandContext, tracingSource.ID, tracingSource.Revision); result.SourceID != "" || catalog.ErrorCodeOf(testErr) != catalog.CodePreconditionFailed || !catalog.ErrorPreservesEvidence(testErr) {
		t.Fatalf("concurrent PostgreSQL source test = %#v, %v", result, testErr)
	}
	if command.failed != nil || command.tx == nil {
		t.Fatalf("concurrent source test poisoned its evidence transaction: failed=%v tx=%v", command.failed, command.tx)
	}
	if err := command.complete(ctx, 200, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	commandComplete = true
	storedTracingSource, err := federationStore.GetSource(identity.WithActor(ctx, sourceActor), tenantA, tracingSource.ID)
	if err != nil || storedTracingSource.Revision != 2 || storedTracingSource.DisplayName != "Concurrent winner" {
		t.Fatalf("concurrent source winner = %#v, %v", storedTracingSource, err)
	}
	testAuditEvents, err := apiRepository.AuditEvents(ctx, tenantA, catalog.AuditEventListRequest{Filters: audit.Query{Action: audit.ActionSourceTested, TargetType: "federation_source", TargetID: tracingSource.ID}, Limit: 10})
	if err != nil || len(testAuditEvents) != 1 || testAuditEvents[0].RequestID != "source-test-race" || testAuditEvents[0].BeforeDigest != testAuditEvents[0].AfterDigest {
		t.Fatalf("concurrent source-test audit = %#v, %v", testAuditEvents, err)
	}
	testFacts, ok := testAuditEvents[0].Details["facts"].(map[string]any)
	if !ok || testFacts["outcome"] != "negative_result" || testFacts["stateChanged"] != false || fmt.Sprint(testFacts["beforeRevision"]) != "1" || fmt.Sprint(testFacts["afterRevision"]) != "1" {
		t.Fatalf("concurrent source-test facts = %#v", testFacts)
	}
	var sourceTestOutboxCount int
	if err := workerPool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND subject=$2 AND event_type=$3`, tenantA, tracingSource.ID, catalogevents.CanonicalType("source.revision.activated")).Scan(&sourceTestOutboxCount); err != nil || sourceTestOutboxCount != 0 {
		t.Fatalf("concurrent source test emitted %d public notifications: %v", sourceTestOutboxCount, err)
	}
	t.Run("source manager tolerates a backward clock", func(t *testing.T) {
		concurrentFetcher.beforeReturn = nil
		// A new manager reads the persisted timestamp; this must not depend on
		// an in-process monotonic clock surviving a restart.
		backwardClock := clock.NewFake(now.Add(-2 * time.Second))
		manager, err := federation.NewManager(federationStore, concurrentFetcher, backwardClock, generator, federation.WithEvidenceSink(apiRepository, false))
		if err != nil {
			t.Fatal(err)
		}
		testContext := identity.WithActor(ctx, sourceActor)
		result, err := manager.TestSourceAtRevision(testContext, tracingSource.ID, storedTracingSource.Revision)
		if err != nil || result.Status != "succeeded" || result.SourceRevision != 3 || !result.TestedAt.Equal(storedTracingSource.UpdatedAt) {
			t.Fatalf("backward-clock source test = %#v, %v", result, err)
		}
		stored, err := federationStore.GetSource(testContext, tenantA, tracingSource.ID)
		if err != nil || stored.Revision != 3 || !stored.UpdatedAt.Equal(result.TestedAt) {
			t.Fatalf("retained source timestamp = %#v, %v", stored, err)
		}
		records, err := apiRepository.AuditEvents(ctx, tenantA, catalog.AuditEventListRequest{Filters: audit.Query{Action: audit.ActionSourceTested, TargetType: "federation_source", TargetID: tracingSource.ID}, Limit: 10})
		if err != nil || len(records) != 2 || !slices.ContainsFunc(records, func(record audit.Record) bool {
			return record.AfterDigest != record.BeforeDigest && record.At.Equal(result.TestedAt)
		}) {
			t.Fatalf("source transition audit timestamps = %#v, %v", records, err)
		}
		var outboxAt time.Time
		if err := workerPool.QueryRow(ctx, `SELECT created_at FROM outbox_events WHERE tenant_id=$1 AND subject=$2 AND event_type=$3`, tenantA, tracingSource.ID, catalogevents.CanonicalType("source.revision.activated")).Scan(&outboxAt); err != nil || !outboxAt.Equal(result.TestedAt) {
			t.Fatalf("source transition notification timestamp = %v, %v", outboxAt, err)
		}
	})
	var revisionCount, audienceCount, referralCount int
	if err := workerPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM federation_source_revisions WHERE tenant_id=$1 AND source_id=$2),(SELECT count(*) FROM federation_source_audiences WHERE tenant_id=$1 AND source_id=$2),(SELECT count(*) FROM federation_source_referrals WHERE tenant_id=$1 AND source_id=$2 AND target_source_id=$3)`, tenantA, source.ID, targetSource.ID).Scan(&revisionCount, &audienceCount, &referralCount); err != nil || revisionCount != 2 || audienceCount != 2 || referralCount != 2 {
		t.Fatalf("immutable source topology history = revisions %d audiences %d referrals %d, %v", revisionCount, audienceCount, referralCount, err)
	}
	var invalidConfigurationDigests int
	if err := operatorPool.QueryRow(ctx, `SELECT count(*) FROM federation_source_revisions WHERE tenant_id=$1 AND source_id=$2 AND configuration_digest <> 'sha256:' || encode(digest(configuration::text,'sha256'),'hex')`, tenantA, source.ID).Scan(&invalidConfigurationDigests); err != nil || invalidConfigurationDigests != 0 {
		t.Fatalf("non-recomputable source configuration digests = %d, %v", invalidConfigurationDigests, err)
	}
	for name, statement := range map[string]string{
		"revision update": `UPDATE federation_source_revisions SET recorded_at=recorded_at WHERE tenant_id=$1 AND source_id=$2`,
		"audience delete": `DELETE FROM federation_source_audiences WHERE tenant_id=$1 AND source_id=$2`,
		"referral delete": `DELETE FROM federation_source_referrals WHERE tenant_id=$1 AND source_id=$2`,
	} {
		t.Run("immutable source history/"+name, func(t *testing.T) {
			_, mutationErr := operatorPool.Exec(ctx, statement, tenantA, source.ID)
			if mutationErr == nil || !strings.Contains(mutationErr.Error(), "source history is immutable") {
				t.Fatalf("immutable source history mutation returned %v", mutationErr)
			}
		})
	}
	_, err = withTenantRead(ctx, apiPool, tenantB, func(tx pgx.Tx) (struct{}, error) {
		var visible int
		queryErr := tx.QueryRow(ctx, `SELECT count(*) FROM federation_source_revisions WHERE source_id=$1`, source.ID).Scan(&visible)
		if queryErr != nil {
			return struct{}{}, queryErr
		}
		if visible != 0 {
			return struct{}{}, fmt.Errorf("cross-tenant source revision count = %d", visible)
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceSnapshotID, _ := generator.New(now.Add(7 * time.Nanosecond))
	retrievalID, _ := generator.New(now.Add(71 * time.Nanosecond))
	observationID, _ := generator.New(now.Add(8 * time.Nanosecond))
	completed := now.Add(time.Minute)
	retrievalBody := []byte(`{"entries":[]}`)
	retrievalDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(retrievalBody))
	retrievalRequestKey := "sha256:" + strings.Repeat("b", 64)
	retrievalURLDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(source.Endpoint)))
	retrievalVaryDigest := "sha256:" + strings.Repeat("c", 64)
	sourceSnapshot := federation.Snapshot{ID: sourceSnapshotID, TenantID: tenantA, SourceID: source.ID, SourceRevision: source.Revision, State: federation.SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 2, PolicyID: source.PolicyID, PolicyRevision: source.PolicyRevision, PolicyDigest: source.PolicyDigest, CredentialRevision: source.CredentialRevision, StartedAt: now, CompletedAt: &completed}
	sourceSnapshot.Retrievals = []federation.RetrievalEvidence{{ID: retrievalID, RequestKey: retrievalRequestKey, RequestedURLDigest: retrievalURLDigest, FinalURLDigest: retrievalURLDigest, ProfileID: source.ProfileID, Status: 200, ReceivedBody: true, RepresentationDigest: retrievalDigest, ETagDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`"source-v1"`))), FetchedAt: now.Add(30 * time.Second)}}
	sourceSnapshot.RetrievalStates = []federation.RetrievalState{{TenantID: tenantA, SourceID: source.ID, SourceRevision: source.Revision, RequestKey: retrievalRequestKey, RequestedURL: source.Endpoint, FinalURL: source.Endpoint, RequestIdentityDigest: retrievalRequestKey, ProfileID: source.ProfileID, PolicyDigest: source.PolicyDigest, CredentialRevision: source.CredentialRevision, RepresentationDigest: retrievalDigest, MediaType: "application/ard+json", Body: retrievalBody, ETag: `"source-v1"`, VarySelectionDigest: retrievalVaryDigest, UpdatedAt: now.Add(30 * time.Second)}}
	rawObservation := json.RawMessage(`{"identifier":"urn:air:remote.example:default:agent","data":{"iconUrl":"https://images.example/native.png"}}`)
	exactDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(rawObservation))
	normalizedObservation := json.RawMessage(`{"profile":"catalog-observation-v1","identifier":"urn:air:remote.example:default:agent","versionPresent":true,"version":"1","kind":"agent","mediaType":"application/a2a-agent-card+json","displayName":"Remote/PostgreSQL Agent","description":"Search an indexed PostgreSQL observation.","url":"","data":{"iconUrl":"https://images.example/native.png"},"tags":["postgres","remote"],"representativeQueries":["Find the remote PostgreSQL agent"],"metadata":null}`)
	normalizedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(normalizedObservation))
	normalizationInputDigest := "sha256:" + strings.Repeat("d", 64)
	identityDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("catalog-observation-identity-v1\x00"+exactDigest+"\x00"+normalizationInputDigest)))
	observation := federation.Observation{ID: observationID, TenantID: tenantA, SourceID: source.ID, SnapshotID: sourceSnapshot.ID, SourceKey: "urn:air:remote.example:default:agent@1", Identifier: "urn:air:remote.example:default:agent", Version: "1", VersionPresent: true, Kind: catalog.KindAgent, MediaType: "application/a2a-agent-card+json", Completeness: federation.CompleteEntry, Compatibility: federation.Compatibility{Structural: "valid", ProfileID: "ard-test", Support: "supported"}, RawEntry: rawObservation, NormalizedEntry: normalizedObservation, RawEntryAvailable: true, ExactEntryDigest: exactDigest, IdentityDigest: identityDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: "catalog-observation-v1", SearchDisplayName: "Remote/PostgreSQL Agent", SearchDescription: "Search an indexed PostgreSQL observation.", SearchTags: []string{"postgres", "remote"}, SearchQueries: []string{"Find the remote PostgreSQL agent"}, SearchPublisher: "remote.example", Freshness: federation.FreshCurrent, FirstSeenAt: now, LastSeenAt: now}
	observation.Occurrence = federation.ObservationOccurrence{ObservationID: observation.ID, SourceID: source.ID, SnapshotID: sourceSnapshot.ID, SourceRevision: source.Revision, RetrievalDigest: exactDigest, EntryLocation: "/entries/0", ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationInputDigest: normalizationInputDigest, EvidenceComplete: true, ObservedAt: now}
	emptyObservationID, _ := generator.New(now.Add(81 * time.Nanosecond))
	emptyRaw := json.RawMessage(`{}`)
	emptyDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(emptyRaw))
	emptyObservation := federation.Observation{ID: emptyObservationID, TenantID: tenantA, SourceID: source.ID, SnapshotID: sourceSnapshot.ID, SourceKey: "unresolved-entry", Completeness: federation.Unresolved, Compatibility: federation.Compatibility{Structural: "invalid", ProfileID: "ard-test", Support: "unsupported"}, RawEntry: emptyRaw, RawEntryAvailable: true, ExactEntryDigest: emptyDigest, IdentityDigest: emptyDigest, Freshness: federation.FreshCurrent, FirstSeenAt: now, LastSeenAt: now}
	emptyObservation.Occurrence = federation.ObservationOccurrence{ObservationID: emptyObservation.ID, SourceID: source.ID, SnapshotID: sourceSnapshot.ID, SourceRevision: source.Revision, RetrievalDigest: emptyDigest, EntryLocation: "/entries/1", ExactEntryDigest: emptyDigest, EvidenceComplete: true, ObservedAt: now}
	source.LastCompleteSnapshotID = sourceSnapshot.ID
	source.LastSyncAt = &completed
	source.UpdatedAt = completed
	syncSource := source
	syncSource.DisplayName = "untrusted synchronization copy"
	_, committedSyncSource, err := workerFederationStore.CommitSync(ctx, sourceSnapshot, []federation.Observation{observation, emptyObservation}, syncSource, source.Revision)
	if err != nil || committedSyncSource.DisplayName != source.DisplayName {
		t.Fatalf("synchronization source projection = %#v, %v", committedSyncSource, err)
	}
	if retainedSource, readErr := federationStore.GetSource(ctx, tenantA, source.ID); readErr != nil || retainedSource.DisplayName != source.DisplayName {
		t.Fatalf("synchronization changed immutable source configuration: %#v, %v", retainedSource, readErr)
	}
	repeatSnapshotID, _ := generator.New(now.Add(82 * time.Nanosecond))
	repeatCompleted := completed.Add(time.Minute)
	repeatSnapshot := sourceSnapshot
	repeatSnapshot.ID = repeatSnapshotID
	repeatSnapshot.State = federation.SnapshotIncomplete
	repeatSnapshot.EnumerationComplete = false
	repeatSnapshot.WithdrawalAuthority = false
	repeatSnapshot.MemberCount = 1
	repeatSnapshot.Retrievals = nil
	repeatSnapshot.RetrievalStates = nil
	repeatSnapshot.StartedAt = completed.Add(30 * time.Second)
	repeatSnapshot.CompletedAt = &repeatCompleted
	repeatObservation := observation
	repeatObservation.ArtworkMetadata = federation.ExtractArtworkCandidates(nil, json.RawMessage(`{"iconUrl":"https://images.example/native.png"}`))
	repeatObservation.SnapshotID = repeatSnapshot.ID
	repeatObservation.Freshness = federation.FreshStale
	repeatObservation.LastSeenAt = repeatCompleted
	repeatObservation.Occurrence.SnapshotID = repeatSnapshot.ID
	repeatObservation.Occurrence.ObservedAt = repeatCompleted
	source.LastSyncAt = &repeatCompleted
	source.UpdatedAt = repeatCompleted
	if _, _, err := workerFederationStore.CommitSync(ctx, repeatSnapshot, []federation.Observation{repeatObservation}, source, source.Revision); err != nil {
		t.Fatal(err)
	}
	var retainedRaw, retainedNormalized, retainedMetadata []byte
	var retainedExact, retainedIdentity string
	if err := workerPool.QueryRow(ctx, `SELECT raw_entry_bytes,normalized_entry_bytes,exact_entry_digest,identity_digest,artwork_metadata FROM observations WHERE tenant_id=$1 AND id=$2`, tenantA, observation.ID).Scan(&retainedRaw, &retainedNormalized, &retainedExact, &retainedIdentity, &retainedMetadata); err != nil || !bytes.Equal(retainedRaw, rawObservation) || !bytes.Equal(retainedNormalized, normalizedObservation) || retainedExact != exactDigest || retainedIdentity != identityDigest || len(retainedMetadata) == 0 {
		t.Fatalf("derived metadata refresh changed retained evidence: %s %v", retainedMetadata, err)
	}
	targetForProjection := federation.ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: observation.Identifier, Version: observation.Version, VersionPresent: true, Kind: observation.Kind}
	projected, projectionErr := federationStore.ArtworkReadBatch(ctx, tenantA, []federation.ArtworkReadRequest{{Target: targetForProjection, EntryDigest: normalizedDigest}})
	if projectionErr != nil || !projected[targetForProjection].Eligible || projected[targetForProjection].Candidates.NativeIcon == nil || projected[targetForProjection].Candidates.NativeIcon.URL != "https://images.example/native.png" {
		t.Fatalf("refreshed indexed artwork candidate: %#v %v", projected, projectionErr)
	}
	if storedSnapshot, getErr := federationStore.GetSnapshot(ctx, tenantA, sourceSnapshot.ID); getErr != nil || storedSnapshot.ID != sourceSnapshot.ID || storedSnapshot.SourceID != source.ID {
		t.Fatalf("exact PostgreSQL source-snapshot read = %#v, %v", storedSnapshot, getErr)
	}
	if _, getErr := federationStore.GetSnapshot(ctx, tenantB, sourceSnapshot.ID); catalog.ErrorCodeOf(getErr) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL source-snapshot read returned %v", getErr)
	}
	listedSnapshots, err := federationStore.ListSnapshots(ctx, tenantA, source.ID, 1)
	if err != nil || len(listedSnapshots) != 1 || listedSnapshots[0].ID != repeatSnapshot.ID {
		t.Fatalf("bounded PostgreSQL source-snapshot list = %#v, %v", listedSnapshots, err)
	}
	if otherTenantSnapshots, listErr := federationStore.ListSnapshots(ctx, tenantB, source.ID, 10); listErr != nil || len(otherTenantSnapshots) != 0 {
		t.Fatalf("cross-tenant PostgreSQL source-snapshot list = %#v, %v", otherTenantSnapshots, listErr)
	}
	firstSnapshotPage, err := federationStore.ListSnapshotPage(ctx, tenantA, federation.SnapshotListRequest{SourceID: source.ID, Limit: 1})
	if err != nil || len(firstSnapshotPage) != 1 || firstSnapshotPage[0].ID != repeatSnapshot.ID {
		t.Fatalf("first PostgreSQL source-snapshot page = %#v, %v", firstSnapshotPage, err)
	}
	secondSnapshotPage, err := federationStore.ListSnapshotPage(ctx, tenantA, federation.SnapshotListRequest{SourceID: source.ID, AfterStartedAt: &firstSnapshotPage[0].StartedAt, AfterID: firstSnapshotPage[0].ID, Limit: 1})
	if err != nil || len(secondSnapshotPage) != 1 || secondSnapshotPage[0].ID != sourceSnapshot.ID {
		t.Fatalf("second PostgreSQL source-snapshot page = %#v, %v", secondSnapshotPage, err)
	}
	finalSnapshotPage, err := federationStore.ListSnapshotPage(ctx, tenantA, federation.SnapshotListRequest{SourceID: source.ID, AfterStartedAt: &secondSnapshotPage[0].StartedAt, AfterID: secondSnapshotPage[0].ID, Limit: 1})
	if err != nil || len(finalSnapshotPage) != 0 {
		t.Fatalf("final PostgreSQL source-snapshot page = %#v, %v", finalSnapshotPage, err)
	}
	if otherTenantSnapshotPage, err := federationStore.ListSnapshotPage(ctx, tenantB, federation.SnapshotListRequest{SourceID: source.ID, Limit: 1}); err != nil || len(otherTenantSnapshotPage) != 0 {
		t.Fatalf("cross-tenant PostgreSQL source-snapshot page = %#v, %v", otherTenantSnapshotPage, err)
	}
	retrievalState, found, err := workerFederationStore.GetRetrievalState(ctx, tenantA, source.ID, source.Revision, retrievalRequestKey)
	if err != nil || !found || retrievalState.RequestedURL != source.Endpoint || retrievalState.ETag != `"source-v1"` || retrievalState.MediaType != "application/ard+json" || !bytes.Equal(retrievalState.Body, retrievalBody) {
		t.Fatalf("retained conditional retrieval state = %#v, found %t, %v", retrievalState, found, err)
	}
	if _, found, err := workerFederationStore.GetRetrievalState(ctx, tenantB, source.ID, source.Revision, retrievalRequestKey); err != nil || found {
		t.Fatalf("cross-tenant conditional retrieval state found=%t, %v", found, err)
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE source_retrieval_states SET updated_at=updated_at-interval '1 second' WHERE tenant_id=$1 AND source_id=$2 AND source_revision=$3 AND request_key=$4`, tenantA, source.ID, source.Revision, retrievalRequestKey); err == nil || !strings.Contains(err.Error(), "source retrieval state transition is invalid") {
		t.Fatalf("backwards retrieval-state time returned %v", err)
	}
	if _, err := workerPool.Exec(ctx, `UPDATE source_retrieval_states SET vary_names=ARRAY['authorization']::text[] WHERE tenant_id=$1 AND source_id=$2 AND source_revision=$3 AND request_key=$4`, tenantA, source.ID, source.Revision, retrievalRequestKey); err == nil {
		t.Fatal("unsupported persisted Vary dimension was accepted")
	}
	invalidRetrievalID, _ := generator.New(now.Add(72 * time.Nanosecond))
	if _, err := workerPool.Exec(ctx, `
      INSERT INTO source_retrieval_attempts(
        tenant_id,id,source_id,source_revision,snapshot_id,request_key,requested_url_digest,final_url_digest,profile_id,
        status,received_body,reused,representation_digest,fetched_at
      ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,200,true,false,NULL,$10)`,
		tenantA, invalidRetrievalID, source.ID, source.Revision, sourceSnapshot.ID, retrievalRequestKey, retrievalURLDigest, retrievalURLDigest, source.ProfileID, now.Add(30*time.Second)); err == nil {
		t.Fatal("received-body retrieval evidence without a representation digest was accepted")
	}
	for name, statement := range map[string]string{
		"document update": `UPDATE source_retrieval_documents SET created_at=created_at WHERE tenant_id=$1 AND source_id=$2`,
		"document delete": `DELETE FROM source_retrieval_documents WHERE tenant_id=$1 AND source_id=$2`,
		"attempt update":  `UPDATE source_retrieval_attempts SET fetched_at=fetched_at WHERE tenant_id=$1 AND source_id=$2`,
		"attempt delete":  `DELETE FROM source_retrieval_attempts WHERE tenant_id=$1 AND source_id=$2`,
	} {
		t.Run("immutable retrieval evidence/"+name, func(t *testing.T) {
			if _, err := operatorPool.Exec(ctx, statement, tenantA, source.ID); err == nil || !strings.Contains(err.Error(), "source history is immutable") {
				t.Fatalf("immutable retrieval evidence mutation returned %v", err)
			}
		})
	}
	for _, table := range []string{"source_retrieval_documents", "source_retrieval_states", "source_retrieval_attempts"} {
		var visible int
		err := apiPool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&visible)
		requireInsufficientPrivilege(t, err)
	}
	var snapshotDetails string
	if _, err := withTenantRead(ctx, apiPool, tenantA, func(tx pgx.Tx) (struct{}, error) {
		return struct{}{}, tx.QueryRow(ctx, `SELECT details::text FROM source_snapshots WHERE id=$1`, sourceSnapshot.ID).Scan(&snapshotDetails)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshotDetails, retrievalURLDigest) || !strings.Contains(snapshotDetails, retrievalDigest) || strings.Contains(snapshotDetails, source.Endpoint) || strings.Contains(snapshotDetails, `"source-v1"`) {
		t.Fatalf("snapshot exposed private conditional state or omitted safe evidence: %s", snapshotDetails)
	}
	if bounded, overflow, err := federationStore.ListCurrentObservations(ctx, tenantA, source.ID, 1); err != nil || !overflow || len(bounded) != 0 {
		t.Fatalf("current-observation limit+1 result = %#v, overflow=%t, %v", bounded, overflow, err)
	}
	if bounded, overflow, err := federationStore.ListCurrentObservations(ctx, tenantB, source.ID, 1); err != nil || overflow || len(bounded) != 0 {
		t.Fatalf("cross-tenant current-observation result = %#v, overflow=%t, %v", bounded, overflow, err)
	}
	if bounded, overflow, err := federationStore.CurrentObservationSet(ctx, tenantA, source.ID, 1); err != nil || !overflow || bounded != (federation.ObservationSet{}) {
		t.Fatalf("current observation-set limit+1 result = %#v, overflow=%t, %v", bounded, overflow, err)
	}
	if bounded, overflow, err := federationStore.CurrentObservationSet(ctx, tenantA, source.ID, 2); err != nil || overflow || bounded.Count != 2 || bounded.Digest == "" {
		t.Fatalf("exact current observation set = %#v, overflow=%t, %v", bounded, overflow, err)
	}
	if bounded, overflow, err := federationStore.CurrentObservationSet(ctx, tenantB, source.ID, 1); err != nil || overflow || bounded.Count != 0 || bounded.Digest == "" {
		t.Fatalf("cross-tenant current observation set = %#v, overflow=%t, %v", bounded, overflow, err)
	}
	observations, _, err := federationStore.ListCurrentObservations(ctx, tenantA, source.ID, 100)
	if err != nil || len(observations) != 2 || !slices.ContainsFunc(observations, func(item federation.Observation) bool { return item.ID == observation.ID }) || !slices.ContainsFunc(observations, func(item federation.Observation) bool { return item.ID == emptyObservation.ID }) {
		t.Fatalf("observations = %#v, %v", observations, err)
	}
	firstObservationPage, err := federationStore.ListObservationPage(ctx, tenantA, federation.ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 1})
	if err != nil || len(firstObservationPage) != 1 || firstObservationPage[0].ID != emptyObservation.ID || firstObservationPage[0].Identifier != "" {
		t.Fatalf("first PostgreSQL observation page = %#v, %v", firstObservationPage, err)
	}
	secondObservationPage, err := federationStore.ListObservationPage(ctx, tenantA, federation.ObservationListRequest{SourceID: source.ID, CurrentOnly: true, AfterIdentifier: firstObservationPage[0].Identifier, AfterVersionPresent: firstObservationPage[0].VersionPresent, AfterVersion: firstObservationPage[0].Version, AfterID: firstObservationPage[0].ID, Limit: 1})
	if err != nil || len(secondObservationPage) != 1 || secondObservationPage[0].ID != observation.ID {
		t.Fatalf("second PostgreSQL observation page = %#v, %v", secondObservationPage, err)
	}
	finalObservationPage, err := federationStore.ListObservationPage(ctx, tenantA, federation.ObservationListRequest{SourceID: source.ID, CurrentOnly: true, AfterIdentifier: secondObservationPage[0].Identifier, AfterVersionPresent: secondObservationPage[0].VersionPresent, AfterVersion: secondObservationPage[0].Version, AfterID: secondObservationPage[0].ID, Limit: 1})
	if err != nil || len(finalObservationPage) != 0 {
		t.Fatalf("final PostgreSQL observation page = %#v, %v", finalObservationPage, err)
	}
	if otherTenantObservationPage, err := federationStore.ListObservationPage(ctx, tenantB, federation.ObservationListRequest{Limit: 1}); err != nil || len(otherTenantObservationPage) != 0 {
		t.Fatalf("cross-tenant PostgreSQL observation page = %#v, %v", otherTenantObservationPage, err)
	}
	if stored, err := federationStore.GetObservation(ctx, tenantA, observation.ID); err != nil || !stored.RawEntryAvailable || stored.ExactEntryDigest != exactDigest || stored.IdentityDigest != identityDigest || stored.NormalizedEntryDigest != normalizedDigest || !bytes.Equal(stored.RawEntry, rawObservation) || !bytes.Equal(stored.NormalizedEntry, normalizedObservation) {
		t.Fatalf("exact observation lookup = %#v, %v", stored, err)
	}
	if _, err := federationStore.GetObservation(ctx, tenantB, observation.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant exact observation lookup returned %v", err)
	}
	groupMembers, err := federationStore.ListObservationGroupMembers(ctx, tenantA, observation.Identifier, observation.Version, true, 10)
	if err != nil || len(groupMembers) != 1 || groupMembers[0].ID != observation.ID {
		t.Fatalf("exact observation identity lookup = %#v, %v", groupMembers, err)
	}
	occurrences, occurrenceTotal, err := federationStore.ListObservationOccurrences(ctx, tenantA, observation.ID, 10)
	if err != nil || occurrenceTotal != 2 || len(occurrences) != 2 || occurrences[0].SnapshotID != repeatSnapshot.ID || occurrences[1].SnapshotID != sourceSnapshot.ID || occurrences[0].SourceRevision != source.Revision || occurrences[0].EntryLocation != "/entries/0" {
		t.Fatalf("observation occurrences = %#v total %d, %v", occurrences, occurrenceTotal, err)
	}
	firstOccurrencePage, err := federationStore.ListObservationOccurrencePage(ctx, tenantA, federation.OccurrenceListRequest{ObservationID: observation.ID, Limit: 1})
	if err != nil || len(firstOccurrencePage) != 1 || firstOccurrencePage[0].SnapshotID != repeatSnapshot.ID {
		t.Fatalf("first PostgreSQL occurrence page = %#v, %v", firstOccurrencePage, err)
	}
	secondOccurrencePage, err := federationStore.ListObservationOccurrencePage(ctx, tenantA, federation.OccurrenceListRequest{ObservationID: observation.ID, AfterObservedAt: &firstOccurrencePage[0].ObservedAt, AfterSnapshotID: firstOccurrencePage[0].SnapshotID, AfterRetrievalDigest: firstOccurrencePage[0].RetrievalDigest, AfterEntryLocation: firstOccurrencePage[0].EntryLocation, Limit: 1})
	if err != nil || len(secondOccurrencePage) != 1 || secondOccurrencePage[0].SnapshotID != sourceSnapshot.ID {
		t.Fatalf("second PostgreSQL occurrence page = %#v, %v", secondOccurrencePage, err)
	}
	finalOccurrencePage, err := federationStore.ListObservationOccurrencePage(ctx, tenantA, federation.OccurrenceListRequest{ObservationID: observation.ID, AfterObservedAt: &secondOccurrencePage[0].ObservedAt, AfterSnapshotID: secondOccurrencePage[0].SnapshotID, AfterRetrievalDigest: secondOccurrencePage[0].RetrievalDigest, AfterEntryLocation: secondOccurrencePage[0].EntryLocation, Limit: 1})
	if err != nil || len(finalOccurrencePage) != 0 {
		t.Fatalf("final PostgreSQL occurrence page = %#v, %v", finalOccurrencePage, err)
	}
	observationSearchPage, err := federationStore.SearchObservationPage(ctx, tenantA, federation.ObservationSearchRequest{Query: federation.SearchQuery{Text: "PostgreSQL", Tags: []string{"postgres"}}, Limit: 1})
	if err != nil || len(observationSearchPage) != 1 || observationSearchPage[0].Observation.ID != observation.ID {
		t.Fatalf("first PostgreSQL observation-search page = %#v, %v", observationSearchPage, err)
	}
	finalObservationSearchPage, err := federationStore.SearchObservationPage(ctx, tenantA, federation.ObservationSearchRequest{Query: federation.SearchQuery{Text: "PostgreSQL", Tags: []string{"postgres"}}, AfterScore: observationSearchPage[0].Score, AfterIdentifier: observationSearchPage[0].Observation.Identifier, AfterID: observationSearchPage[0].Observation.ID, Limit: 1})
	if err != nil || len(finalObservationSearchPage) != 0 {
		t.Fatalf("final PostgreSQL observation-search page = %#v, %v", finalObservationSearchPage, err)
	}
	indexedSearch, err := federationStore.SearchIndexedCatalog(ctx, tenantA, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: observation.Identifier, Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"postgres"}, Publisher: "remote.example", Visibility: catalog.VisibilityTenant}, Limit: 1})
	if err != nil || indexedSearch.IndexedTotal != 1 || len(indexedSearch.Indexed) != 1 || indexedSearch.Indexed[0].ID != catalog.IndexedSearchResultID(observation.Identifier, observation.Version, true) || indexedSearch.Indexed[0].Score != 100 || indexedSearch.Indexed[0].Indexed == nil || indexedSearch.Indexed[0].Indexed.SourceCount != 1 || indexedSearch.Indexed[0].Indexed.SourceIDs[0] != source.ID || indexedSearch.Generation == "" {
		t.Fatalf("bounded indexed PostgreSQL search = %#v, %v", indexedSearch, err)
	}
	punctuationQuery := "Remote PostgreSQL"
	exactIndexedRequest := catalog.FederatedCatalogSearchRequest{Limit: 1, ExactIdentity: &catalog.IndexedIdentity{Identifier: observation.Identifier, Version: observation.Version, VersionPresent: true}}
	for _, selectedTenant := range []string{tenantA, tenantB} {
		found, err := federationStore.SearchIndexedCatalog(ctx, selectedTenant, exactIndexedRequest)
		want := 0
		if selectedTenant == tenantA {
			want = 1
		}
		if err != nil || len(found.Indexed) != want || found.IndexedTotal != want {
			t.Fatalf("exact indexed PostgreSQL lookup for tenant %s: %#v %v", selectedTenant, found, err)
		}
	}
	exactIndexedRequest.ExactIdentity.VersionPresent = false
	exactIndexedRequest.ExactIdentity.Version = ""
	if found, err := federationStore.SearchIndexedCatalog(ctx, tenantA, exactIndexedRequest); err != nil || len(found.Indexed) != 0 {
		t.Fatalf("exact PostgreSQL lookup confused absent and present versions: %#v %v", found, err)
	}
	punctuationSearch, err := federationStore.SearchIndexedCatalog(ctx, tenantA, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: punctuationQuery}, Limit: 10})
	wantPunctuationScore := catalog.CoreLexicalScore(catalog.SearchDocument{Identifier: observation.Identifier, DisplayName: observation.SearchDisplayName, Description: observation.SearchDescription, Tags: observation.SearchTags, RepresentativeQueries: observation.SearchQueries}, punctuationQuery)
	if err != nil || punctuationSearch.IndexedTotal != 1 || len(punctuationSearch.Indexed) != 1 || punctuationSearch.Indexed[0].Score != wantPunctuationScore {
		t.Fatalf("PostgreSQL and Go lexical punctuation parity = %#v, want score %d, %v", punctuationSearch, wantPunctuationScore, err)
	}
	if crossTenantIndexed, err := federationStore.SearchIndexedCatalog(ctx, tenantB, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "PostgreSQL"}, Limit: 10}); err != nil || crossTenantIndexed.IndexedTotal != 0 || len(crossTenantIndexed.Indexed) != 0 {
		t.Fatalf("cross-tenant indexed PostgreSQL search = %#v, %v", crossTenantIndexed, err)
	}
	if publicIndexed, err := federationStore.SearchIndexedCatalog(ctx, tenantA, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "PostgreSQL", Visibility: catalog.VisibilityPublic}, Limit: 10}); err != nil || publicIndexed.IndexedTotal != 0 || len(publicIndexed.Indexed) != 0 {
		t.Fatalf("public visibility admitted an external indexed projection = %#v, %v", publicIndexed, err)
	}
	liveSources, liveSourcesTruncated, err := federationStore.ListCatalogLiveSources(ctx, tenantA, policyDigest, 1)
	if err != nil || liveSourcesTruncated || len(liveSources) != 1 || liveSources[0].ID != source.ID {
		t.Fatalf("bounded direct live source selection = %#v truncated=%t, %v", liveSources, liveSourcesTruncated, err)
	}
	unifiedSearch, err := catalog.NewSearchService(catalogService, postgresIndexedCatalogSearcher{store: federationStore})
	if err != nil {
		t.Fatal(err)
	}
	unifiedPage, err := unifiedSearch.Search(readerContext, catalog.SearchQuery{Text: "PostgreSQL", Limit: 10})
	if err != nil || unifiedPage.DurableTotal != 3 || len(unifiedPage.Items) != 3 || unifiedPage.LiveCount != 0 || unifiedPage.Partial || unifiedPage.Profile != catalog.CoreSearchProfile || unifiedPage.Tier != "core" || unifiedPage.Generation == "" {
		t.Fatalf("unified PostgreSQL Catalog search = %#v, %v", unifiedPage, err)
	}
	resultTypes := map[catalog.SearchResultType]int{}
	if current, err := unifiedSearch.GetIndexed(readerContext, catalog.IndexedIdentity{Identifier: observation.Identifier, Version: observation.Version, VersionPresent: true}); err != nil || current.Identifier != observation.Identifier {
		t.Fatalf("current indexed reader lookup: %#v %v", current, err)
	}
	if current, err := unifiedSearch.GetIndexed(readerContext, catalog.IndexedIdentity{Identifier: externalResource.CanonicalIdentifier, Version: externalVersion.Version, VersionPresent: true}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound || current.ID != "" {
		t.Fatalf("indexed lookup ignored local publication precedence: %#v %v", current, err)
	}
	for _, item := range unifiedPage.Items {
		resultTypes[item.Type]++
	}
	if resultTypes[catalog.SearchResultLocal] != 2 || resultTypes[catalog.SearchResultIndexed] != 1 || resultTypes[catalog.SearchResultLive] != 0 {
		t.Fatalf("unified PostgreSQL result types = %#v", resultTypes)
	}
	if _, err := RepairCatalogSearchProjections(ctx, apiPool, tenantA, 1, "restore derived search", "change-42"); err == nil || !strings.Contains(err.Error(), "schema owner") {
		t.Fatalf("API role search projection repair returned %v", err)
	}
	repairPool, err := Open(ctx, testDSN, testPoolConfig(2))
	if err != nil {
		t.Fatal(err)
	}
	defer repairPool.Close()
	planRows, err := repairPool.Query(ctx, `
EXPLAIN (COSTS OFF)
SELECT EXISTS (
  SELECT 1 FROM observations
   WHERE tenant_id=$1 AND normalized_entry_digest IS NOT NULL
     AND (search_display_name IS NULL OR search_description IS NULL OR search_tags IS NULL OR search_representative_queries IS NULL OR search_publisher IS NULL OR search_vector IS NULL OR search_vector_version<>2 OR search_filter_version<>1)
   ORDER BY id LIMIT 1)`, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	var repairPlan strings.Builder
	for planRows.Next() {
		var line string
		if err := planRows.Scan(&line); err != nil {
			planRows.Close()
			t.Fatal(err)
		}
		repairPlan.WriteString(line)
	}
	if err := planRows.Err(); err != nil {
		planRows.Close()
		t.Fatal(err)
	}
	planRows.Close()
	if !strings.Contains(repairPlan.String(), "observations_catalog_search_projection_missing") {
		t.Fatalf("search projection completion probe did not use its sparse index: %s", repairPlan.String())
	}
	if _, err := repairPool.Exec(ctx, `DROP INDEX observations_catalog_search_projection_missing`); err != nil {
		t.Fatal(err)
	}
	if _, err := RepairCatalogSearchProjections(ctx, repairPool, tenantA, 1, "restore derived search", "change-42"); err == nil || !strings.Contains(err.Error(), "requires migration 000039") {
		t.Fatalf("search projection repair without its sparse index returned %v", err)
	}
	if _, err := repairPool.Exec(ctx, `
CREATE INDEX observations_catalog_search_projection_missing
  ON observations (tenant_id,id)
  WHERE normalized_entry_digest IS NOT NULL
    AND (search_display_name IS NULL OR search_description IS NULL OR search_tags IS NULL
      OR search_representative_queries IS NULL OR search_publisher IS NULL OR search_vector IS NULL OR search_vector_version<>2 OR search_filter_version<>1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repairPool.Exec(ctx, `
ALTER TABLE observations
  DROP CONSTRAINT observations_search_display_pair_check,
  DROP CONSTRAINT observations_search_description_pair_check,
  DROP CONSTRAINT observations_search_tags_pair_check,
  DROP CONSTRAINT observations_search_queries_pair_check,
  DROP CONSTRAINT observations_search_publisher_pair_check,
  DROP CONSTRAINT observations_search_vector_pair_check;
ALTER TABLE observations DISABLE TRIGGER observations_catalog_search_projection`); err != nil {
		t.Fatal(err)
	}
	if _, err := repairPool.Exec(ctx, `UPDATE observations SET search_display_name=NULL,search_description=NULL,search_tags=NULL,search_representative_queries=NULL,search_publisher=NULL,search_vector=NULL WHERE tenant_id=$1 AND id=$2`, tenantA, observation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repairPool.Exec(ctx, `
ALTER TABLE observations ENABLE TRIGGER observations_catalog_search_projection;
ALTER TABLE observations
  ADD CONSTRAINT observations_search_display_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_display_name IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_description_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_description IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_tags_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_tags IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_queries_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_representative_queries IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_publisher_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_publisher IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_vector_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_vector IS NULL)) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := federationStore.SearchIndexedCatalog(ctx, tenantA, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "PostgreSQL"}, Limit: 10}); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("unrepaired derived projection did not block incomplete search: %v", err)
	}
	cancelledRepairContext, cancelRepair := context.WithCancel(ctx)
	cancelRepair()
	if _, err := RepairCatalogSearchProjections(cancelledRepairContext, repairPool, tenantA, 1, "restore derived search", "change-42"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search projection repair returned %v", err)
	}
	repairResult, err := RepairCatalogSearchProjections(ctx, repairPool, tenantA, 1, "restore derived search", "change-42")
	if err != nil || repairResult.Processed != 1 || repairResult.HasMore || !repairResult.Complete || repairResult.AuditEventID == "" || repairResult.AfterGeneration <= repairResult.BeforeGeneration {
		t.Fatalf("bounded search projection repair = %#v, %v", repairResult, err)
	}
	var repairAction, repairReason, repairActor, repairDetails string
	if err := repairPool.QueryRow(ctx, `SELECT action,reason,actor,details::text FROM audit_events WHERE tenant_id=$1 AND id=$2`, tenantA, repairResult.AuditEventID).Scan(&repairAction, &repairReason, &repairActor, &repairDetails); err != nil || repairAction != "repair.search_projection.rebuilt" || repairReason != "restore derived search" || repairActor == "" || !strings.Contains(repairDetails, `"operatorLabel": "change-42"`) || !strings.Contains(repairDetails, observation.ID) {
		t.Fatalf("search projection repair evidence = action %q reason %q actor %q details %q, %v", repairAction, repairReason, repairActor, repairDetails, err)
	}
	var repairedDisplayName, repairedPublisher, repairedVector string
	var repairedTags []string
	if err := repairPool.QueryRow(ctx, `SELECT search_display_name,search_publisher,search_tags,search_vector::text FROM observations WHERE tenant_id=$1 AND id=$2`, tenantA, observation.ID).Scan(&repairedDisplayName, &repairedPublisher, &repairedTags, &repairedVector); err != nil || repairedDisplayName != observation.SearchDisplayName || repairedPublisher != observation.SearchPublisher || !slices.Equal(repairedTags, observation.SearchTags) || !strings.Contains(repairedVector, "postgresql") {
		t.Fatalf("rebuilt search projection = display %q publisher %q tags %#v vector %q, %v", repairedDisplayName, repairedPublisher, repairedTags, repairedVector, err)
	}
	emptyRepair, err := RepairCatalogSearchProjections(ctx, repairPool, tenantA, 1, "confirm derived search repair", "change-42")
	if err != nil || emptyRepair.Processed != 0 || emptyRepair.HasMore || !emptyRepair.Complete || emptyRepair.AuditEventID != "" || emptyRepair.AfterGeneration != emptyRepair.BeforeGeneration {
		t.Fatalf("empty search projection repair = %#v, %v", emptyRepair, err)
	}
	var repairedEligible int
	if err := repairPool.QueryRow(ctx, `
SELECT count(*)
  FROM observations observation
  JOIN federation_sources source ON source.tenant_id=observation.tenant_id AND source.id=observation.source_id AND source.status IN ('active','error')
  JOIN federation_source_revisions source_revision ON source_revision.tenant_id=source.tenant_id AND source_revision.source_id=source.id AND source_revision.revision=source.revision AND source_revision.retrieval_mode IN ('indexed','hybrid')
 WHERE observation.tenant_id=$1 AND observation.id=$2 AND observation.freshness='current'
   AND observation.normalized_entry_digest IS NOT NULL AND observation.search_vector IS NOT NULL
   AND observation.completeness='complete' AND observation.compatibility->>'structural'='valid'
   AND observation.compatibility->>'support'='supported' AND observation.kind IN ('agent','mcp_server','skill')
   AND observation.search_vector @@ plainto_tsquery('simple','PostgreSQL')`, tenantA, observation.ID).Scan(&repairedEligible); err != nil || repairedEligible != 1 {
		t.Fatalf("repaired projection eligibility = %d, %v", repairedEligible, err)
	}
	var repairedLocalDuplicate int
	if err := repairPool.QueryRow(ctx, `
SELECT count(*)
  FROM resources local_resource
  JOIN catalog_listings local_listing ON local_listing.tenant_id=local_resource.tenant_id AND local_listing.resource_id=local_resource.id
  JOIN listing_releases local_release ON local_release.tenant_id=local_listing.tenant_id AND local_release.id=local_listing.current_release_id AND local_release.state IN ('published','deprecated')
  JOIN resource_version_revisions local_revision ON local_revision.tenant_id=local_release.tenant_id AND local_revision.id=local_release.version_revision_id
  JOIN resource_versions local_version ON local_version.tenant_id=local_revision.tenant_id AND local_version.id=local_revision.resource_version_id
 WHERE local_resource.tenant_id=$1 AND local_resource.canonical_identifier=$2 AND local_version.version=$3`, tenantA, observation.Identifier, observation.Version).Scan(&repairedLocalDuplicate); err != nil || repairedLocalDuplicate != 0 {
		t.Fatalf("repaired projection local duplicate count = %d, %v", repairedLocalDuplicate, err)
	}
	_, err = withTenantRead(ctx, apiPool, tenantA, func(tx pgx.Tx) (struct{}, error) {
		var visible int
		queryErr := tx.QueryRow(ctx, `
SELECT count(*)
  FROM observations observation
  JOIN federation_sources source ON source.tenant_id=observation.tenant_id AND source.id=observation.source_id AND source.status IN ('active','error')
  JOIN federation_source_revisions source_revision ON source_revision.tenant_id=source.tenant_id AND source_revision.source_id=source.id AND source_revision.revision=source.revision AND source_revision.retrieval_mode IN ('indexed','hybrid')
 WHERE observation.tenant_id=$1 AND observation.id=$2 AND observation.freshness='current'
   AND observation.normalized_entry_digest IS NOT NULL AND observation.search_vector IS NOT NULL
   AND observation.completeness='complete' AND observation.compatibility->>'structural'='valid'
   AND observation.compatibility->>'support'='supported' AND observation.kind IN ('agent','mcp_server','skill')
   AND observation.search_vector @@ plainto_tsquery('simple','PostgreSQL')`, tenantA, observation.ID).Scan(&visible)
		if queryErr != nil {
			return struct{}{}, queryErr
		}
		if visible != 1 {
			return struct{}{}, fmt.Errorf("repaired application-role projection count = %d", visible)
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if repaired, err := federationStore.SearchIndexedCatalog(ctx, tenantA, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "PostgreSQL"}, Limit: 10}); err != nil || repaired.IndexedTotal != 1 || len(repaired.Indexed) != 1 {
		t.Fatalf("repaired indexed PostgreSQL search = %#v, %v", repaired, err)
	}
	originalScope := source.Scope
	excludedSource := source
	excludedSource.Scope = federation.Scope{Tags: []string{"not-present"}}
	excludedSource.Revision++
	excludedSource.UpdatedAt = source.UpdatedAt.Add(time.Second)
	if _, err := federationStore.UpdateSource(ctx, excludedSource, source.Revision); err != nil {
		t.Fatal(err)
	}
	if excluded, err := federationStore.SearchIndexedCatalog(ctx, tenantA, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "PostgreSQL"}, Limit: 10}); err != nil || excluded.IndexedTotal != 0 || len(excluded.Indexed) != 0 {
		t.Fatalf("narrowed current source scope admitted an older observation = %#v, %v", excluded, err)
	}
	restoredSource := excludedSource
	restoredSource.Scope = originalScope
	restoredSource.Revision++
	restoredSource.UpdatedAt = excludedSource.UpdatedAt.Add(time.Second)
	restoredSource.LastCompleteSnapshotID = ""
	restoredSource.LastSyncAt = nil
	restoredSource.LastError = "must not enter immutable configuration"
	source, err = federationStore.UpdateSource(ctx, restoredSource, excludedSource.Revision)
	if err != nil || source.LastCompleteSnapshotID != excludedSource.LastCompleteSnapshotID || source.LastSyncAt == nil || excludedSource.LastSyncAt == nil || !source.LastSyncAt.Equal(*excludedSource.LastSyncAt) || source.LastError != excludedSource.LastError {
		t.Fatalf("configuration update preserved sync state = %#v, %v", source, err)
	}
	var retainedMutableSyncState bool
	if err := operatorPool.QueryRow(ctx, `
SELECT configuration ?| ARRAY['lastCompleteSnapshotId','lastSyncAt','lastError']
  FROM federation_source_revisions
 WHERE tenant_id=$1 AND source_id=$2 AND revision=$3`, tenantA, source.ID, source.Revision).Scan(&retainedMutableSyncState); err != nil || retainedMutableSyncState {
		t.Fatalf("immutable source configuration retained mutable sync state=%t, %v", retainedMutableSyncState, err)
	}
	if stored, err := federationStore.GetSource(ctx, tenantA, source.ID); err != nil || stored.LastCompleteSnapshotID != source.LastCompleteSnapshotID || stored.LastSyncAt == nil || source.LastSyncAt == nil || !stored.LastSyncAt.Equal(*source.LastSyncAt) {
		t.Fatalf("separate mutable source sync state = %#v, %v", stored, err)
	}
	if restored, err := federationStore.SearchIndexedCatalog(ctx, tenantA, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "PostgreSQL"}, Limit: 10}); err != nil || restored.IndexedTotal != 1 || len(restored.Indexed) != 1 {
		t.Fatalf("restored current source scope did not admit the observation = %#v, %v", restored, err)
	}
	observation.Occurrence.SourceRevision = source.Revision
	partialSnapshotID, _ := generator.New(now.Add(8 * time.Nanosecond))
	partialObservationID, _ := generator.New(now.Add(9 * time.Nanosecond))
	partialCompleted := repeatCompleted.Add(time.Minute)
	partialSnapshot := federation.Snapshot{ID: partialSnapshotID, TenantID: tenantA, SourceID: source.ID, SourceRevision: source.Revision, State: federation.SnapshotIncomplete, EnumerationComplete: false, WithdrawalAuthority: false, MemberCount: 1, StartedAt: repeatCompleted, CompletedAt: &partialCompleted}
	partialObservation := observation
	partialObservation.ID = partialObservationID
	partialObservation.SnapshotID = partialSnapshot.ID
	partialObservation.Freshness = federation.FreshStale
	partialObservation.LastSeenAt = partialCompleted
	partialObservation.Occurrence.ObservationID = partialObservation.ID
	partialObservation.Occurrence.SnapshotID = partialSnapshot.ID
	partialObservation.Occurrence.ObservedAt = partialCompleted
	partialSource := source
	partialSource.Status = federation.SourceError
	partialSource.LastError = "Source sync was incomplete. The last complete membership remains active."
	partialSource.LastSyncAt = &partialCompleted
	partialSource.UpdatedAt = partialCompleted
	if _, _, err := workerFederationStore.CommitSync(ctx, partialSnapshot, []federation.Observation{partialObservation}, partialSource, source.Revision); err != nil {
		t.Fatal(err)
	}
	if stored, err := federationStore.GetSource(ctx, tenantA, source.ID); err != nil || stored.Status != federation.SourceError || stored.LastError != partialSource.LastError || stored.LastCompleteSnapshotID != source.LastCompleteSnapshotID {
		t.Fatalf("mutable source failure state = %#v, %v", stored, err)
	}
	observations, _, err = federationStore.ListCurrentObservations(ctx, tenantA, source.ID, 100)
	if err != nil || len(observations) != 2 || !slices.ContainsFunc(observations, func(item federation.Observation) bool {
		return item.ID == observation.ID && item.SnapshotID == sourceSnapshot.ID && item.Freshness == federation.FreshCurrent && item.LastSeenAt.Equal(partialCompleted)
	}) || !slices.ContainsFunc(observations, func(item federation.Observation) bool {
		return item.ID == emptyObservation.ID && item.Freshness == federation.FreshCurrent
	}) {
		t.Fatalf("same-digest partial sync changed active membership = %#v, %v", observations, err)
	}
	changedSnapshotID, _ := generator.New(now.Add(10 * time.Nanosecond))
	changedObservationID, _ := generator.New(now.Add(11 * time.Nanosecond))
	changedCompleted := partialCompleted.Add(time.Minute)
	changedSnapshot := partialSnapshot
	changedSnapshot.ID = changedSnapshotID
	changedSnapshot.StartedAt = partialCompleted
	changedSnapshot.CompletedAt = &changedCompleted
	changedObservation := partialObservation
	changedObservation.ID = changedObservationID
	changedObservation.SnapshotID = changedSnapshot.ID
	changedObservation.RawEntry = json.RawMessage(`{"identifier":"urn:air:remote.example:default:agent","displayName":"Changed"}`)
	changedObservation.ExactEntryDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(changedObservation.RawEntry))
	changedObservation.IdentityDigest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("catalog-observation-identity-v1\x00"+changedObservation.ExactEntryDigest+"\x00"+normalizationInputDigest)))
	changedObservation.LastSeenAt = changedCompleted
	changedObservation.Occurrence.ObservationID = changedObservation.ID
	changedObservation.Occurrence.SnapshotID = changedSnapshot.ID
	changedObservation.Occurrence.ExactEntryDigest = changedObservation.ExactEntryDigest
	changedObservation.Occurrence.RetrievalDigest = changedObservation.ExactEntryDigest
	changedObservation.Occurrence.ObservedAt = changedCompleted
	partialSource.LastSyncAt = &changedCompleted
	partialSource.UpdatedAt = changedCompleted
	if _, _, err := workerFederationStore.CommitSync(ctx, changedSnapshot, []federation.Observation{changedObservation}, partialSource, source.Revision); err != nil {
		t.Fatal(err)
	}
	if stored, err := federationStore.GetSource(ctx, tenantA, source.ID); err != nil || stored.LastCompleteSnapshotID != source.LastCompleteSnapshotID {
		t.Fatalf("changed partial sync replaced last complete snapshot = %#v, %v", stored, err)
	}
	observations, _, err = federationStore.ListCurrentObservations(ctx, tenantA, source.ID, 100)
	if err != nil || len(observations) != 2 || !slices.ContainsFunc(observations, func(item federation.Observation) bool {
		return item.ID == observation.ID && item.Freshness == federation.FreshCurrent
	}) || !slices.ContainsFunc(observations, func(item federation.Observation) bool {
		return item.ID == emptyObservation.ID && item.Freshness == federation.FreshCurrent
	}) {
		t.Fatalf("changed partial sync replaced active membership = %#v, %v", observations, err)
	}
	allObservations, err := federationStore.ListObservationPage(ctx, tenantA, federation.ObservationListRequest{SourceID: source.ID, Limit: 100})
	if err != nil || len(allObservations) != 3 {
		t.Fatalf("partial observation history = %#v, %v", allObservations, err)
	}
	occurrences, occurrenceTotal, err = federationStore.ListObservationOccurrences(ctx, tenantA, observation.ID, 10)
	if err != nil || occurrenceTotal != 3 || len(occurrences) != 3 || !slices.ContainsFunc(occurrences, func(item federation.ObservationOccurrence) bool {
		return item.SnapshotID == partialSnapshot.ID
	}) || !slices.ContainsFunc(occurrences, func(item federation.ObservationOccurrence) bool {
		return item.SnapshotID == repeatSnapshot.ID
	}) || !slices.ContainsFunc(occurrences, func(item federation.ObservationOccurrence) bool {
		return item.SnapshotID == sourceSnapshot.ID
	}) {
		t.Fatalf("repeated observation occurrence history = %#v total %d, %v", occurrences, occurrenceTotal, err)
	}
	summaries, err := federationStore.ObservationOccurrenceSummaries(ctx, tenantA, []string{observation.ID})
	latestOccurrence, summarizedOccurrenceTotal := summaries[observation.ID].Latest, summaries[observation.ID].Total
	if err != nil || summarizedOccurrenceTotal != occurrenceTotal || latestOccurrence.SnapshotID != partialSnapshot.ID {
		t.Fatalf("bounded observation occurrence summary = %#v total %d, %v", latestOccurrence, summarizedOccurrenceTotal, err)
	}
	if _, err := federationStore.ObservationOccurrenceSummaries(ctx, tenantB, []string{observation.ID}); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("cross-tenant observation occurrence summary returned %v", err)
	}
	if exact, err := federationStore.GetObservationOccurrence(ctx, tenantA, observation.ID, partialSnapshot.ID, exactDigest, "/entries/0"); err != nil || exact.ObservationID != observation.ID || exact.EvidenceComplete != true {
		t.Fatalf("exact snapshot occurrence lookup = %#v, %v", exact, err)
	}
	withdrawnSnapshotID, _ := generator.New(now.Add(80 * time.Nanosecond))
	withdrawnCompleted := changedCompleted.Add(time.Minute)
	withdrawnSnapshot := federation.Snapshot{ID: withdrawnSnapshotID, TenantID: tenantA, SourceID: source.ID, SourceRevision: source.Revision, State: federation.SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, StartedAt: changedCompleted, CompletedAt: &withdrawnCompleted}
	withdrawnSource := partialSource
	withdrawnSource.Status = federation.SourceActive
	withdrawnSource.LastError = ""
	withdrawnSource.LastCompleteSnapshotID = withdrawnSnapshot.ID
	withdrawnSource.LastSyncAt = &withdrawnCompleted
	withdrawnSource.UpdatedAt = withdrawnCompleted
	withdrawnSnapshot, _, err = workerFederationStore.CommitSync(ctx, withdrawnSnapshot, nil, withdrawnSource, source.Revision)
	if err != nil || withdrawnSnapshot.Withdrawn != 2 {
		t.Fatalf("authoritative withdrawal = %#v, %v", withdrawnSnapshot, err)
	}
	if current, _, err := federationStore.ListCurrentObservations(ctx, tenantA, source.ID, 100); err != nil || len(current) != 0 {
		t.Fatalf("withdrawn observation remained current = %#v, %v", current, err)
	}
	reappearedSnapshotID, _ := generator.New(now.Add(81 * time.Nanosecond))
	reappearedCompleted := withdrawnCompleted.Add(time.Minute)
	reappearedSnapshot := federation.Snapshot{ID: reappearedSnapshotID, TenantID: tenantA, SourceID: source.ID, SourceRevision: source.Revision, State: federation.SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 1, StartedAt: withdrawnCompleted, CompletedAt: &reappearedCompleted}
	reappeared := observation
	reappeared.ID, _ = generator.New(now.Add(82 * time.Nanosecond))
	reappeared.SnapshotID = reappearedSnapshot.ID
	reappeared.Freshness = federation.FreshCurrent
	reappeared.LastSeenAt = reappearedCompleted
	reappeared.Occurrence.ObservationID = reappeared.ID
	reappeared.Occurrence.SnapshotID = reappearedSnapshot.ID
	reappeared.Occurrence.ObservedAt = reappearedCompleted
	withdrawnSource.LastCompleteSnapshotID = reappearedSnapshot.ID
	withdrawnSource.LastSyncAt = &reappearedCompleted
	withdrawnSource.UpdatedAt = reappearedCompleted
	if _, _, err := workerFederationStore.CommitSync(ctx, reappearedSnapshot, []federation.Observation{reappeared}, withdrawnSource, source.Revision); err != nil {
		t.Fatal(err)
	}
	if current, _, err := federationStore.ListCurrentObservations(ctx, tenantA, source.ID, 100); err != nil || len(current) != 1 || current[0].ID != observation.ID || current[0].Freshness != federation.FreshCurrent {
		t.Fatalf("exact observation reappearance = %#v, %v", current, err)
	}
	_, occurrenceTotal, err = federationStore.ListObservationOccurrences(ctx, tenantA, observation.ID, 10)
	if err != nil || occurrenceTotal != 4 {
		t.Fatalf("reappearance occurrence count = %d, %v", occurrenceTotal, err)
	}
	if err := apiPool.QueryRow(ctx, `SELECT count(*) FROM observation_occurrences`).Scan(&visibleWithoutContext); err != nil || visibleWithoutContext != 0 {
		t.Fatalf("observation occurrence RLS without context exposed %d rows, err=%v", visibleWithoutContext, err)
	}
	_, immutableOccurrenceErr := operatorPool.Exec(ctx, `UPDATE observation_occurrences SET observed_at=observed_at WHERE observation_id=$1`, observation.ID)
	if immutableOccurrenceErr == nil || !strings.Contains(immutableOccurrenceErr.Error(), "source history is immutable") {
		t.Fatalf("immutable occurrence mutation returned %v", immutableOccurrenceErr)
	}
	adoptionInput := catalog.ExternalObservationAdoptionInput{
		SourceAdmissionRevision: source.Revision,
		NamespaceID:             namespaceA.ID, CanonicalIdentifier: observation.Identifier, Kind: catalog.KindAgent, Version: "1",
		Representation: catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: json.RawMessage(`{"name":"Adopted PostgreSQL Agent"}`)},
		DisplayName:    "Adopted PostgreSQL Agent", Description: "A local draft retaining exact source occurrence provenance.", PublisherID: publisher.ID, Visibility: catalog.VisibilityPrivate,
		Provenance: catalog.AdoptionProvenance{ObservationID: observation.ID, SourceID: source.ID, SourceRevision: source.Revision, SnapshotID: reappearedSnapshot.ID, RetrievalDigest: exactDigest, EntryLocation: "/entries/0", ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: "catalog-observation-v1"},
	}
	var beforeAdmission [3]int
	const admissionCounts = `SELECT (SELECT count(*) FROM resources WHERE tenant_id=$1), (SELECT count(*) FROM audit_events WHERE tenant_id=$1), (SELECT count(*) FROM outbox_events WHERE tenant_id=$1)`
	if err := operatorPool.QueryRow(ctx, admissionCounts, tenantA).Scan(&beforeAdmission[0], &beforeAdmission[1], &beforeAdmission[2]); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{0, source.Revision + 1} {
		rejectedInput := adoptionInput
		rejectedInput.SourceAdmissionRevision = revision
		if result, err := catalogService.AdoptExternalObservation(authorContext, rejectedInput); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || result.Resource.ID != "" {
			t.Fatalf("mismatched source admission revision %d: %+v %v", revision, result, err)
		}
		var afterAdmission [3]int
		if err := operatorPool.QueryRow(ctx, admissionCounts, tenantA).Scan(&afterAdmission[0], &afterAdmission[1], &afterAdmission[2]); err != nil || afterAdmission != beforeAdmission {
			t.Fatalf("rejected source admission wrote resource/audit/outbox state: %v -> %v, %v", beforeAdmission, afterAdmission, err)
		}
	}
	adopted, err := catalogService.AdoptExternalObservation(authorContext, adoptionInput)
	if err != nil || adopted.VersionRevision.Adoption == nil || adopted.VersionRevision.Adoption.ObservationID != observation.ID || adopted.Listing.CandidateRevisionID != adopted.ListingRevision.ID {
		t.Fatalf("PostgreSQL observation adoption = %#v, %v", adopted, err)
	}
	// An import indicator follows exact retained peer version presence, not the
	// operator-assigned local version. No second adoption or install is inferred.
	signalContext, _, err := identity.WithCollectionContext(authorContext)
	if err != nil {
		t.Fatal(err)
	}
	importedTarget := ratings.Target{Origin: "foreign", SourceID: source.ID, PresentationGeneration: federation.EffectivePresentationGeneration(source), Identifier: observation.Identifier, VersionPresent: observation.VersionPresent, Version: observation.Version, Kind: observation.Kind}
	// Exercise real foreign-rating writes through the API role. The frozen
	// authority below models a source change after admission but before Save.
	foreignRatingStore, err := NewPostgresRatingsStore(apiRepository)
	if err != nil {
		t.Fatal(err)
	}
	foreignRatings, err := ratings.New(foreignRatingStore, postgresRatingAuthority(auditManager.AdmitRating), catalogClock, generator)
	if err != nil {
		t.Fatal(err)
	}
	foreignAdmission, err := auditManager.AdmitRating(authorContext, ratings.Admission{Target: importedTarget})
	if err != nil {
		t.Fatal(err)
	}
	foreignVote, err := foreignRatings.Submit(authorContext, foreignAdmission, 5, nil, 0)
	if err != nil || foreignVote.EntryDigest != normalizedDigest || foreignVote.Consent {
		t.Fatalf("foreign PostgreSQL vote = %+v, %v", foreignVote, err)
	}
	for name, stale := range map[string]ratings.Admission{
		"source revision": {Target: importedTarget, SourceRevision: source.Revision + 1, EntryDigest: normalizedDigest},
		"entry digest":    {Target: importedTarget, SourceRevision: source.Revision, EntryDigest: "sha256:" + strings.Repeat("b", 64)},
	} {
		t.Run("foreign rating stale "+name, func(t *testing.T) {
			staleService, err := ratings.New(foreignRatingStore, postgresRatingAuthority(func(context.Context, ratings.Admission) (ratings.Admission, error) { return stale, nil }), catalogClock, generator)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := staleService.Submit(authorContext, stale, 1, nil, foreignVote.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
				t.Fatalf("stale foreign write = %v", err)
			}
		})
	}
	// A verified live admission uses the same source/version identity and vote.
	liveAdmission := foreignAdmission
	liveAdmission.Live = true
	foreignVote, err = foreignRatings.Submit(authorContext, liveAdmission, 4, nil, foreignVote.Revision)
	if err != nil || foreignVote.Revision != 2 {
		t.Fatalf("live foreign PostgreSQL vote = %+v, %v", foreignVote, err)
	}
	if view, err := foreignRatings.Read(authorContext, foreignAdmission); err != nil || view.Count != 1 || view.Average == nil || *view.Average != 4 {
		t.Fatalf("foreign PostgreSQL aggregate = %+v, %v", view, err)
	}
	if _, err := foreignRatingStore.Get(otherTenantAdminContext, tenantB, foreignVote.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("foreign rating cross-tenant read = %v", err)
	}
	signalRequests := []federation.RatingSignalTarget{{Target: importedTarget, EntryDigest: normalizedDigest}}
	signals, err := federationStore.RatingSignals(signalContext, tenantA, signalRequests)
	if err != nil || signals[ratings.Key(importedTarget)].ImportedResourceID != adopted.Resource.ID {
		t.Fatalf("exact authorized import match %+v %v", signals, err)
	}
	signalRequests[0].EntryDigest = "sha256:" + strings.Repeat("b", 64)
	signals, err = federationStore.RatingSignals(signalContext, tenantA, signalRequests)
	if err != nil || !signals[ratings.Key(importedTarget)].ChangedEntry {
		t.Fatal("changed import context hidden", err)
	}
	mismatched := importedTarget
	mismatched.VersionPresent = true
	mismatched.Version = "unrelated-version"
	signals, err = federationStore.RatingSignals(signalContext, tenantA, []federation.RatingSignalTarget{{Target: mismatched}})
	if err != nil || signals[ratings.Key(mismatched)].ImportedResourceID != "" {
		t.Fatal("unrelated advertised version inherited import", err)
	}
	otherSignalContext, _, err := identity.WithCollectionContext(otherTenantAdminContext)
	if err != nil {
		t.Fatal(err)
	}
	signals, err = federationStore.RatingSignals(otherSignalContext, tenantB, signalRequests)
	if err != nil || len(signals) != 0 {
		t.Fatal("cross-tenant import disclosed", err)
	}
	if err := catalogService.DeleteResource(authorContext, adopted.Resource.ID, "adoption provenance must be retained", adopted.Resource.Revision); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("PostgreSQL adopted-resource deletion returned %v", err)
	}
	storedAdoptionRevision, _, err := apiRepository.GetVersionRevision(ctx, tenantA, adopted.VersionRevision.ID)
	if err != nil || storedAdoptionRevision.Adoption == nil || storedAdoptionRevision.Adoption.SnapshotID != reappearedSnapshot.ID || storedAdoptionRevision.Adoption.AdoptedBy == "" {
		t.Fatalf("stored PostgreSQL adoption provenance = %#v, %v", storedAdoptionRevision.Adoption, err)
	}
	if _, _, err := apiRepository.GetVersionRevision(ctx, tenantB, adopted.VersionRevision.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant adopted revision read returned %v", err)
	}
	if _, err := catalogService.AdoptExternalObservation(authorContext, catalog.ExternalObservationAdoptionInput{
		SourceAdmissionRevision: source.Revision,
		NamespaceID:             namespaceA.ID, CanonicalIdentifier: observation.Identifier, Kind: catalog.KindAgent, Version: "1",
		Representation: catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: json.RawMessage(`{"name":"Duplicate"}`)},
		DisplayName:    "Duplicate", Description: "Must roll back atomically.", PublisherID: publisher.ID, Visibility: catalog.VisibilityPrivate,
		Provenance: catalog.AdoptionProvenance{ObservationID: observation.ID, SourceID: source.ID, SourceRevision: source.Revision, SnapshotID: reappearedSnapshot.ID, RetrievalDigest: exactDigest, EntryLocation: "/entries/0", ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: "catalog-observation-v1"},
	}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("duplicate PostgreSQL observation adoption returned %v", err)
	}
	var adoptedResourceCount, adoptedListingCount, adoptionAuditCount int
	if err := operatorPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM resources WHERE tenant_id=$1 AND canonical_identifier=$2),(SELECT count(*) FROM catalog_listings WHERE tenant_id=$1 AND resource_id=$3),(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='observation.adopted' AND target_id=$3::text)`, tenantA, observation.Identifier, adopted.Resource.ID).Scan(&adoptedResourceCount, &adoptedListingCount, &adoptionAuditCount); err != nil || adoptedResourceCount != 1 || adoptedListingCount != 1 || adoptionAuditCount != 1 {
		t.Fatalf("atomic PostgreSQL adoption persistence = resources %d listings %d audit %d, %v", adoptedResourceCount, adoptedListingCount, adoptionAuditCount, err)
	}
	postAdoptionSnapshotID, _ := generator.New(now.Add(83 * time.Nanosecond))
	postAdoptionCompleted := reappearedCompleted.Add(time.Minute)
	postAdoptionSnapshot := federation.Snapshot{ID: postAdoptionSnapshotID, TenantID: tenantA, SourceID: source.ID, SourceRevision: source.Revision, State: federation.SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, StartedAt: reappearedCompleted, CompletedAt: &postAdoptionCompleted}
	postAdoptionSource := withdrawnSource
	postAdoptionSource.LastCompleteSnapshotID = postAdoptionSnapshot.ID
	postAdoptionSource.LastSyncAt = &postAdoptionCompleted
	postAdoptionSource.UpdatedAt = postAdoptionCompleted
	if committed, _, err := workerFederationStore.CommitSync(ctx, postAdoptionSnapshot, nil, postAdoptionSource, source.Revision); err != nil || committed.Withdrawn != 1 {
		t.Fatalf("post-adoption withdrawal = %#v, %v", committed, err)
	}
	staleForeignRatings, err := ratings.New(foreignRatingStore, postgresRatingAuthority(func(context.Context, ratings.Admission) (ratings.Admission, error) { return foreignAdmission, nil }), catalogClock, generator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staleForeignRatings.Submit(authorContext, foreignAdmission, 1, nil, foreignVote.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("withdrawn observation accepted stale rating admission: %v", err)
	}
	if stored, err := foreignRatingStore.Get(authorContext, tenantA, foreignVote.ID); err != nil || stored.Revision != foreignVote.Revision || stored.Score != 4 {
		t.Fatalf("failed foreign rating changed retained opinion = %+v, %v", stored, err)
	}
	if withdrawn, err := foreignRatings.Change(authorContext, foreignVote.ID, foreignVote.Revision, "withdraw", ""); err != nil || withdrawn.State != "withdrawn" {
		t.Fatalf("owner cannot withdraw after source withdrawal = %+v, %v", withdrawn, err)
	}
	if _, err := catalogService.AdoptExternalObservation(authorContext, catalog.ExternalObservationAdoptionInput{
		SourceAdmissionRevision: source.Revision,
		NamespaceID:             namespaceA.ID, CanonicalIdentifier: "urn:air:remote.example:default:withdrawn-agent", Kind: catalog.KindAgent, Version: "1",
		Representation: catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: json.RawMessage(`{"name":"Withdrawn PostgreSQL Agent"}`)},
		DisplayName:    "Withdrawn PostgreSQL Agent", Description: "A stale selection must fail before any local draft is written.", PublisherID: publisher.ID, Visibility: catalog.VisibilityPrivate,
		Provenance: catalog.AdoptionProvenance{ObservationID: observation.ID, SourceID: source.ID, SourceRevision: source.Revision, SnapshotID: reappearedSnapshot.ID, RetrievalDigest: exactDigest, EntryLocation: "/entries/0", ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: "catalog-observation-v1"},
	}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("withdrawn PostgreSQL observation adoption returned %v", err)
	}
	if stored, _, err := apiRepository.GetVersionRevision(ctx, tenantA, adopted.VersionRevision.ID); err != nil || stored.Adoption == nil || stored.Adoption.ObservationID != observation.ID {
		t.Fatalf("source withdrawal changed adopted local draft = %#v, %v", stored, err)
	}

	conflictSnapshotID, _ := generator.New(now.Add(90 * time.Nanosecond))
	conflictObservationID, _ := generator.New(now.Add(91 * time.Nanosecond))
	conflictingObservationID, _ := generator.New(now.Add(92 * time.Nanosecond))
	conflictCompleted := postAdoptionCompleted.Add(time.Minute)
	conflictSnapshotBody := []byte("complete same-source conflict snapshot")
	conflictRetrievalDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(conflictSnapshotBody))
	conflictSnapshot := federation.Snapshot{ID: conflictSnapshotID, TenantID: tenantA, SourceID: source.ID, SourceRevision: source.Revision, State: federation.SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 2, Added: 2, BodyDigest: conflictRetrievalDigest, StartedAt: postAdoptionCompleted, CompletedAt: &conflictCompleted}
	conflictIdentifier := "urn:air:remote.example:default:conflict-agent"
	conflictObservation := func(idValue, displayName, description, entryLocation string) federation.Observation {
		raw := json.RawMessage(fmt.Sprintf(`{"identifier":%q,"version":"1","displayName":%q}`, conflictIdentifier, displayName))
		rawDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		normalized := json.RawMessage(fmt.Sprintf(`{"profile":"catalog-observation-v1","identifier":%q,"versionPresent":true,"version":"1","kind":"agent","mediaType":"application/a2a-agent-card+json","displayName":%q,"description":%q,"url":"","data":null,"tags":["conflict"],"representativeQueries":[],"metadata":null}`, conflictIdentifier, displayName, description))
		normalizedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(normalized))
		item := federation.Observation{ID: idValue, TenantID: tenantA, SourceID: source.ID, SnapshotID: conflictSnapshot.ID, SourceKey: "conflict-agent-version-1", Identifier: conflictIdentifier, Version: "1", VersionPresent: true, Kind: catalog.KindAgent, MediaType: adapter.MediaA2AAgentCard, Completeness: federation.CompleteEntry, Compatibility: federation.Compatibility{Structural: "valid", ProfileID: "ard-test", Support: "supported"}, RawEntry: raw, NormalizedEntry: normalized, RawEntryAvailable: true, ExactEntryDigest: rawDigest, IdentityDigest: rawDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: "catalog-observation-v1", SearchDisplayName: displayName, SearchDescription: description, SearchTags: []string{"conflict"}, SearchPublisher: "remote.example", Freshness: federation.FreshCurrent, FirstSeenAt: conflictCompleted, LastSeenAt: conflictCompleted}
		item.Occurrence = federation.ObservationOccurrence{ObservationID: item.ID, SourceID: source.ID, SnapshotID: conflictSnapshot.ID, SourceRevision: source.Revision, RetrievalDigest: conflictRetrievalDigest, EntryLocation: entryLocation, ExactEntryDigest: rawDigest, NormalizedEntryDigest: normalizedDigest, EvidenceComplete: true, ObservedAt: conflictCompleted}
		return item
	}
	selectedConflictObservation := conflictObservation(conflictObservationID, "Needle Agent", "Matches only the selected normalized member.", "/entries/0")
	otherConflictObservation := conflictObservation(conflictingObservationID, "Haystack Agent", "Does not match the narrow lexical query.", "/entries/1")
	conflictSource := postAdoptionSource
	conflictSource.Status = federation.SourceActive
	conflictSource.LastCompleteSnapshotID = conflictSnapshot.ID
	conflictSource.LastSyncAt = &conflictCompleted
	conflictSource.UpdatedAt = conflictCompleted
	if _, _, err := workerFederationStore.CommitSync(ctx, conflictSnapshot, []federation.Observation{selectedConflictObservation, otherConflictObservation}, conflictSource, source.Revision); err != nil {
		t.Fatal(err)
	}
	boundedConflictGroup, err := federationStore.ListObservationGroupMembers(ctx, tenantA, conflictIdentifier, "1", true, 1)
	if err != nil || len(boundedConflictGroup) != 1 || boundedConflictGroup[0].TenantID != tenantA {
		t.Fatalf("bounded PostgreSQL observation group = %#v, %v", boundedConflictGroup, err)
	}
	conflict, sourceConflicts, err := federationStore.ObservationGroupConflicts(ctx, tenantA, conflictIdentifier, "1", true, []string{source.ID})
	if err != nil || !conflict || !sourceConflicts[source.ID] {
		t.Fatalf("complete PostgreSQL observation conflict = conflict %t sources %#v, %v", conflict, sourceConflicts, err)
	}
	conflictWithoutSources, sourceConflicts, err := federationStore.ObservationGroupConflicts(ctx, tenantA, conflictIdentifier, "1", true, nil)
	if err != nil || !conflictWithoutSources || len(sourceConflicts) != 0 {
		t.Fatalf("aggregate PostgreSQL observation conflict = conflict %t sources %#v, %v", conflictWithoutSources, sourceConflicts, err)
	}
	isolatedGroup, err := federationStore.ListObservationGroupMembers(ctx, tenantB, conflictIdentifier, "1", true, 100)
	if err != nil || len(isolatedGroup) != 0 {
		t.Fatalf("cross-tenant PostgreSQL observation group = %#v, %v", isolatedGroup, err)
	}
	conflictSearch, err := federationStore.SearchIndexedCatalog(ctx, tenantA, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "Needle"}, Limit: 10})
	if err != nil || conflictSearch.IndexedTotal != 1 || len(conflictSearch.Indexed) != 1 || conflictSearch.Indexed[0].Indexed == nil || !conflictSearch.Indexed[0].Indexed.Conflict || !conflictSearch.Indexed[0].Indexed.SameSourceConflict {
		t.Fatalf("query-independent PostgreSQL conflict evidence = %#v, %v", conflictSearch, err)
	}
	if _, err := catalogService.AdoptExternalObservation(authorContext, catalog.ExternalObservationAdoptionInput{
		SourceAdmissionRevision: source.Revision,
		NamespaceID:             namespaceA.ID, CanonicalIdentifier: conflictIdentifier, Kind: catalog.KindAgent, Version: "1",
		Representation: catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: json.RawMessage(`{"name":"Conflicting PostgreSQL Agent"}`)},
		DisplayName:    "Conflicting PostgreSQL Agent", Description: "A same-source conflict must fail inside the adoption transaction.", PublisherID: publisher.ID, Visibility: catalog.VisibilityPrivate,
		Provenance: catalog.AdoptionProvenance{ObservationID: selectedConflictObservation.ID, SourceID: source.ID, SourceRevision: source.Revision, SnapshotID: conflictSnapshot.ID, RetrievalDigest: conflictRetrievalDigest, EntryLocation: "/entries/0", ExactEntryDigest: selectedConflictObservation.ExactEntryDigest, NormalizedEntryDigest: selectedConflictObservation.NormalizedEntryDigest, NormalizationProfile: "catalog-observation-v1"},
	}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("transactional same-source conflict adoption returned %v", err)
	}
	var conflictingAdoptionCount int
	if err := workerPool.QueryRow(ctx, `SELECT count(*) FROM resources WHERE tenant_id=$1 AND canonical_identifier=$2`, tenantA, conflictIdentifier).Scan(&conflictingAdoptionCount); err != nil || conflictingAdoptionCount != 0 {
		t.Fatalf("failed conflicting adoption retained %d resources, %v", conflictingAdoptionCount, err)
	}
	if _, err := repairPool.Exec(ctx, `
ALTER TABLE observations
  DROP CONSTRAINT observations_search_display_pair_check,
  DROP CONSTRAINT observations_search_description_pair_check,
  DROP CONSTRAINT observations_search_tags_pair_check,
  DROP CONSTRAINT observations_search_queries_pair_check,
  DROP CONSTRAINT observations_search_publisher_pair_check,
  DROP CONSTRAINT observations_search_vector_pair_check;
ALTER TABLE observations DISABLE TRIGGER observations_catalog_search_projection`); err != nil {
		t.Fatal(err)
	}
	if _, err := repairPool.Exec(ctx, `
UPDATE observations
   SET search_display_name=NULL,search_description=NULL,search_tags=NULL,
       search_representative_queries=NULL,search_publisher=NULL,search_vector=NULL
 WHERE tenant_id=$1 AND id IN ($2,$3)`, tenantA, selectedConflictObservation.ID, otherConflictObservation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repairPool.Exec(ctx, `
ALTER TABLE observations ENABLE TRIGGER observations_catalog_search_projection;
ALTER TABLE observations
  ADD CONSTRAINT observations_search_display_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_display_name IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_description_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_description IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_tags_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_tags IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_queries_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_representative_queries IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_publisher_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_publisher IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_vector_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_vector IS NULL)) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	firstMultiRowRepair, err := RepairCatalogSearchProjections(ctx, repairPool, tenantA, 1, "restore multi-row derived search", "change-43")
	if err != nil || firstMultiRowRepair.Processed != 1 || !firstMultiRowRepair.HasMore || firstMultiRowRepair.Complete || firstMultiRowRepair.AuditEventID == "" || firstMultiRowRepair.AfterGeneration-firstMultiRowRepair.BeforeGeneration != firstMultiRowRepair.Processed {
		t.Fatalf("continued multi-row search projection repair = %#v, %v", firstMultiRowRepair, err)
	}
	finalMultiRowRepair, err := RepairCatalogSearchProjections(ctx, repairPool, tenantA, 1, "restore multi-row derived search", "change-43")
	if err != nil || finalMultiRowRepair.Processed != 1 || finalMultiRowRepair.HasMore || !finalMultiRowRepair.Complete || finalMultiRowRepair.AuditEventID == "" || finalMultiRowRepair.AfterGeneration-finalMultiRowRepair.BeforeGeneration != finalMultiRowRepair.Processed {
		t.Fatalf("final multi-row search projection repair = %#v, %v", finalMultiRowRepair, err)
	}
	var multiRowRepairCount int
	if err := repairPool.QueryRow(ctx, `
SELECT count(*)
  FROM observations
 WHERE tenant_id=$1 AND id IN ($2,$3)
   AND search_display_name IS NOT NULL AND search_description IS NOT NULL
   AND search_tags IS NOT NULL AND search_representative_queries IS NOT NULL
   AND search_publisher IS NOT NULL AND search_vector IS NOT NULL`, tenantA, selectedConflictObservation.ID, otherConflictObservation.ID).Scan(&multiRowRepairCount); err != nil || multiRowRepairCount != 2 {
		t.Fatalf("multi-row repaired projection count = %d, %v", multiRowRepairCount, err)
	}

	idempotencyStore, err := NewPostgresIdempotencyStore(apiRepository)
	if err != nil {
		t.Fatal(err)
	}

	scope := idempotency.Scope{TenantID: tenantA, Actor: "contributor", Route: "POST /api/v1/resources", Key: "key-one", AuthorizationDigest: digestText("authorization")}
	idempotencyExpiry := time.Now().UTC().Add(time.Hour)
	execution, err := idempotencyStore.Execute(ctx, scope, "digest", idempotencyExpiry, func(context.Context) idempotency.Response {
		return idempotency.Response{Digest: "digest", Status: 201, Headers: map[string][]string{"Location": {"/one"}}, Body: []byte(`{"id":"one"}`)}
	})
	if err != nil || execution.Replayed || execution.Response.Status != 201 {
		t.Fatalf("idempotency execution = %#v, %v", execution, err)
	}
	execution, err = idempotencyStore.Execute(ctx, scope, "digest", idempotencyExpiry, func(context.Context) idempotency.Response {
		t.Fatal("replayed command executed again")
		return idempotency.Response{}
	})
	if err != nil || !execution.Replayed || execution.Response.Status != 201 {
		t.Fatalf("idempotency replay = %#v, %v", execution, err)
	}

	webhookStore, err := NewPostgresWebhookStore(apiRepository)
	if err != nil {
		t.Fatal(err)
	}
	workerWebhookStore, err := NewPostgresWebhookStore(workerRepository)
	if err != nil {
		t.Fatal(err)
	}
	webhookID, _ := generator.New(now.Add(8 * time.Nanosecond))
	endpoint := webhook.Endpoint{ID: webhookID, TenantID: tenantA, Name: "Catalog events", URL: "https://hooks.example.com/catalog", EventTypes: []string{catalog.CanonicalEventType("resource.created")}, Status: webhook.StatusActive, Revision: 1, SecretVersion: 1, SecretCiphertext: []byte("encrypted"), CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	endpoint, err = webhookStore.CreateEndpoint(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := webhookStore.GetEndpoint(ctx, tenantB, endpoint.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant webhook read returned %v", err)
	}
	endpoint.Name = "Updated catalog events"
	endpoint.Revision = 2
	endpoint.UpdatedAt = now.Add(time.Minute)
	if _, err := webhookStore.UpdateEndpoint(ctx, endpoint, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := webhookStore.UpdateEndpoint(ctx, endpoint, 1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale webhook update returned %v", err)
	}
	alternateWebhookID, _ := generator.New(now.Add(83 * time.Nanosecond))
	alternateWebhook := webhook.Endpoint{ID: alternateWebhookID, TenantID: tenantA, Name: "Alpha events", URL: "https://hooks.example.com/alpha", EventTypes: []string{"*"}, Status: webhook.StatusActive, Revision: 1, SecretVersion: 1, SecretCiphertext: []byte("encrypted-alpha"), CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	if _, err := webhookStore.CreateEndpoint(ctx, alternateWebhook); err != nil {
		t.Fatal(err)
	}
	listedEndpoints, err := webhookStore.ListEndpointPage(ctx, tenantA, webhook.EndpointListRequest{Limit: 10})
	if err != nil || len(listedEndpoints) != 2 || listedEndpoints[0].ID != alternateWebhook.ID || listedEndpoints[1].ID != endpoint.ID {
		t.Fatalf("PostgreSQL webhook endpoint list = %#v, %v", listedEndpoints, err)
	}
	if otherTenantEndpoints, listErr := webhookStore.ListEndpointPage(ctx, tenantB, webhook.EndpointListRequest{Limit: 10}); listErr != nil || len(otherTenantEndpoints) != 0 {
		t.Fatalf("cross-tenant PostgreSQL webhook endpoint list = %#v, %v", otherTenantEndpoints, listErr)
	}
	firstWebhookPage, err := webhookStore.ListEndpointPage(ctx, tenantA, webhook.EndpointListRequest{Limit: 1})
	if err != nil || len(firstWebhookPage) != 1 || firstWebhookPage[0].ID != alternateWebhook.ID {
		t.Fatalf("first PostgreSQL webhook page = %#v, %v", firstWebhookPage, err)
	}
	secondWebhookPage, err := webhookStore.ListEndpointPage(ctx, tenantA, webhook.EndpointListRequest{AfterName: firstWebhookPage[0].Name, AfterID: firstWebhookPage[0].ID, Limit: 1})
	if err != nil || len(secondWebhookPage) != 1 || secondWebhookPage[0].ID != endpoint.ID {
		t.Fatalf("second PostgreSQL webhook page = %#v, %v", secondWebhookPage, err)
	}
	finalWebhookPage, err := webhookStore.ListEndpointPage(ctx, tenantA, webhook.EndpointListRequest{AfterName: secondWebhookPage[0].Name, AfterID: secondWebhookPage[0].ID, Limit: 1})
	if err != nil || len(finalWebhookPage) != 0 {
		t.Fatalf("final PostgreSQL webhook page = %#v, %v", finalWebhookPage, err)
	}
	if otherTenantWebhookPage, err := webhookStore.ListEndpointPage(ctx, tenantB, webhook.EndpointListRequest{Limit: 1}); err != nil || len(otherTenantWebhookPage) != 0 {
		t.Fatalf("cross-tenant PostgreSQL webhook page = %#v, %v", otherTenantWebhookPage, err)
	}
	deliveryID, _ := generator.New(now.Add(9 * time.Nanosecond))
	eventID, _ := generator.New(now.Add(10 * time.Nanosecond))
	delivery := webhook.Delivery{ID: deliveryID, TenantID: tenantA, EndpointID: endpoint.ID, EventID: eventID, EventType: catalog.CanonicalEventType("resource.created"), State: webhook.DeliverySucceeded, Attempt: 1, ResponseStatus: 204, CreatedAt: now, UpdatedAt: now}
	if _, err := webhookStore.SaveDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if storedDelivery, getErr := webhookStore.GetDelivery(ctx, tenantA, endpoint.ID, eventID); getErr != nil || storedDelivery.ID != delivery.ID || storedDelivery.ResponseStatus != delivery.ResponseStatus {
		t.Fatalf("exact PostgreSQL webhook delivery read = %#v, %v", storedDelivery, getErr)
	}
	if _, getErr := webhookStore.GetDelivery(ctx, tenantB, endpoint.ID, eventID); catalog.ErrorCodeOf(getErr) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL webhook delivery read returned %v", getErr)
	}
	secondDeliveryID, _ := generator.New(now.Add(84 * time.Nanosecond))
	secondEventID, _ := generator.New(now.Add(85 * time.Nanosecond))
	secondDelivery := delivery
	secondDelivery.ID = secondDeliveryID
	secondDelivery.EventID = secondEventID
	secondDelivery.UpdatedAt = now.Add(time.Minute)
	if _, err := workerWebhookStore.SaveDelivery(ctx, secondDelivery); err != nil {
		t.Fatal(err)
	}
	deliveries, err := webhookStore.ListDeliveries(ctx, tenantA, webhook.DeliveryListRequest{EndpointID: endpoint.ID, Limit: 10})
	if err != nil || len(deliveries) != 2 || deliveries[0].EventID != secondEventID || deliveries[1].EventID != eventID {
		t.Fatalf("webhook deliveries = %#v, %v", deliveries, err)
	}
	firstDeliveryPage, err := webhookStore.ListDeliveries(ctx, tenantA, webhook.DeliveryListRequest{EndpointID: endpoint.ID, Limit: 1})
	if err != nil || len(firstDeliveryPage) != 1 || firstDeliveryPage[0].ID != secondDelivery.ID {
		t.Fatalf("first PostgreSQL webhook-delivery page = %#v, %v", firstDeliveryPage, err)
	}
	secondDeliveryPage, err := webhookStore.ListDeliveries(ctx, tenantA, webhook.DeliveryListRequest{EndpointID: endpoint.ID, AfterUpdatedAt: &firstDeliveryPage[0].UpdatedAt, AfterID: firstDeliveryPage[0].ID, Limit: 1})
	if err != nil || len(secondDeliveryPage) != 1 || secondDeliveryPage[0].ID != delivery.ID {
		t.Fatalf("second PostgreSQL webhook-delivery page = %#v, %v", secondDeliveryPage, err)
	}
	finalDeliveryPage, err := webhookStore.ListDeliveries(ctx, tenantA, webhook.DeliveryListRequest{EndpointID: endpoint.ID, AfterUpdatedAt: &secondDeliveryPage[0].UpdatedAt, AfterID: secondDeliveryPage[0].ID, Limit: 1})
	if err != nil || len(finalDeliveryPage) != 0 {
		t.Fatalf("final PostgreSQL webhook-delivery page = %#v, %v", finalDeliveryPage, err)
	}
	if otherTenantDeliveryPage, err := webhookStore.ListDeliveries(ctx, tenantB, webhook.DeliveryListRequest{EndpointID: endpoint.ID, Limit: 1}); err != nil || len(otherTenantDeliveryPage) != 0 {
		t.Fatalf("cross-tenant PostgreSQL webhook-delivery page = %#v, %v", otherTenantDeliveryPage, err)
	}

	maintenanceSourceID, _ := generator.New(now.Add(101 * time.Nanosecond))
	maintenanceSource := federation.Source{ID: maintenanceSourceID, TenantID: tenantA, DisplayName: "Automatic maintenance", Type: federation.SourceARDManifest, Endpoint: "https://maintenance.example/.well-known/ard.json", Status: federation.SourceActive, ProfileID: "ard-test", RetrievalMode: federation.RetrievalIndexed, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: federation.UpdateAutomatic, PolicyID: "integration", PolicyRevision: 1, PolicyDigest: policyDigest, Revision: 1, CreatedBy: "source-admin", CreatedAt: now, UpdatedAt: now}
	if _, err := federationStore.CreateSource(ctx, maintenanceSource); err != nil {
		t.Fatal(err)
	}
	staleMaintenanceSource := maintenanceSource
	staleMaintenanceSource.ID, _ = generator.New(now.Add(102 * time.Nanosecond))
	staleMaintenanceSource.DisplayName = "Stale policy maintenance"
	staleMaintenanceSource.Endpoint = "https://stale-maintenance.example/.well-known/ard.json"
	staleMaintenanceSource.PolicyDigest = "sha256:" + strings.Repeat("f", 64)
	if _, err := federationStore.CreateSource(ctx, staleMaintenanceSource); err != nil {
		t.Fatal(err)
	}
	workerMaintenance, err := NewPostgresSourceMaintenanceStore(workerRepository)
	if err != nil {
		t.Fatal(err)
	}
	apiMaintenance, err := NewPostgresSourceMaintenanceStore(apiRepository)
	if err != nil {
		t.Fatal(err)
	}
	if due, err := apiMaintenance.ListDueAutomaticSources(ctx, time.Now(), time.Minute, policyDigest, 32); catalog.ErrorCodeOf(err) != catalog.CodeForbidden || len(due) != 0 {
		t.Fatalf("API maintenance access = %#v, %v", due, err)
	}
	due, err := workerMaintenance.ListDueAutomaticSources(ctx, time.Now(), time.Minute, policyDigest, 32)
	if err != nil {
		t.Fatal(err)
	}
	var maintenanceCandidate federation.MaintenanceCandidate
	for _, candidate := range due {
		if candidate.SourceID == maintenanceSource.ID {
			maintenanceCandidate = candidate
		}
		if candidate.SourceID == targetSource.ID || candidate.SourceID == staleMaintenanceSource.ID {
			t.Fatalf("ineligible source was selected for maintenance: %#v", candidate)
		}
	}
	if maintenanceCandidate.SourceID == "" || maintenanceCandidate.TenantID != tenantA || maintenanceCandidate.SourceRevision != 1 {
		t.Fatalf("automatic maintenance candidate = %#v from %#v", maintenanceCandidate, due)
	}
	type admissionResult struct {
		job      jobs.Job
		existing bool
		err      error
	}
	startAdmissions := make(chan struct{})
	results := make(chan admissionResult, 2)
	var admissions sync.WaitGroup
	for index := 0; index < 2; index++ {
		jobID, _ := generator.New(now.Add(time.Duration(103+index) * time.Nanosecond))
		automaticJob := jobs.Job{ID: jobID, TenantID: tenantA, Kind: "source.sync", Actor: "catalog-source-maintainer", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + maintenanceSource.ID + `","sourceRevision":1,"trigger":"automatic"}`), State: jobs.StateQueued, NotBefore: now, AttemptLimit: 7, DeduplicationKey: "source.sync:" + maintenanceSource.ID + ":1", CreatedAt: now, UpdatedAt: now}
		admissions.Add(1)
		go func(job jobs.Job) {
			defer admissions.Done()
			<-startAdmissions
			admitted, existing, admissionErr := workerMaintenance.AdmitAutomaticSync(ctx, maintenanceCandidate, job, time.Minute, policyDigest, 64)
			results <- admissionResult{job: admitted, existing: existing, err: admissionErr}
		}(automaticJob)
	}
	close(startAdmissions)
	admissions.Wait()
	close(results)
	succeeded := 0
	preconditioned := 0
	var admittedJob jobs.Job
	for result := range results {
		switch {
		case result.err == nil:
			succeeded++
			admittedJob = result.job
			if result.existing {
				t.Fatalf("first automatic admission unexpectedly returned an existing job: %#v", result.job)
			}
		case catalog.ErrorCodeOf(result.err) == catalog.CodePreconditionFailed:
			preconditioned++
		default:
			t.Fatalf("concurrent automatic admission returned %v", result.err)
		}
	}
	if succeeded != 1 || preconditioned != 1 || admittedJob.PayloadVersion != 2 || admittedJob.AttemptLimit != 7 || admittedJob.Actor != "catalog-source-maintainer" {
		t.Fatalf("concurrent automatic admissions succeeded=%d preconditioned=%d job=%#v", succeeded, preconditioned, admittedJob)
	}
	var automaticJobCount, maintenanceMarkerCount int
	if err := workerPool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE tenant_id=$1 AND kind='source.sync' AND deduplication_key=$2`, tenantA, "source.sync:"+maintenanceSource.ID+":1").Scan(&automaticJobCount); err != nil || automaticJobCount != 1 {
		t.Fatalf("automatic maintenance job count = %d, %v", automaticJobCount, err)
	}
	if err := workerPool.QueryRow(ctx, `SELECT count(*) FROM source_maintenance_state WHERE tenant_id=$1 AND source_id=$2 AND source_revision=1`, tenantA, maintenanceSource.ID).Scan(&maintenanceMarkerCount); err != nil || maintenanceMarkerCount != 1 {
		t.Fatalf("automatic maintenance marker count = %d, %v", maintenanceMarkerCount, err)
	}
	err = apiPool.QueryRow(ctx, `SELECT count(*) FROM source_maintenance_state`).Scan(&maintenanceMarkerCount)
	requireInsufficientPrivilege(t, err)
	foreignJob := admittedJob
	foreignJob.ID, _ = generator.New(now.Add(105 * time.Nanosecond))
	foreignJob.TenantID = tenantB
	foreignJob.DeduplicationKey = "source.sync:cross-tenant:1"
	if _, _, err := workerMaintenance.AdmitAutomaticSync(ctx, federation.MaintenanceCandidate{TenantID: tenantB, SourceID: maintenanceSource.ID, SourceRevision: 1}, foreignJob, time.Minute, policyDigest, 64); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant automatic admission returned %v", err)
	}
	findMaintenanceCandidate := func(tenantID, sourceID string) federation.MaintenanceCandidate {
		t.Helper()
		candidates, candidateErr := workerMaintenance.ListDueAutomaticSources(ctx, time.Now(), time.Minute, policyDigest, 32)
		if candidateErr != nil {
			t.Fatal(candidateErr)
		}
		for _, candidate := range candidates {
			if candidate.TenantID == tenantID && candidate.SourceID == sourceID {
				return candidate
			}
		}
		t.Fatalf("automatic source %s/%s was not due in %#v", tenantID, sourceID, candidates)
		return federation.MaintenanceCandidate{}
	}
	capacitySource := maintenanceSource
	capacitySource.ID, _ = generator.New(now.Add(106 * time.Nanosecond))
	capacitySource.TenantID = tenantB
	capacitySource.DisplayName = "Global-capacity maintenance"
	capacitySource.Endpoint = "https://capacity-maintenance.example/.well-known/ard.json"
	if _, err := federationStore.CreateSource(ctx, capacitySource); err != nil {
		t.Fatal(err)
	}
	capacityCandidate := findMaintenanceCandidate(capacitySource.TenantID, capacitySource.ID)
	capacityJobID, _ := generator.New(now.Add(107 * time.Nanosecond))
	capacityJob := jobs.Job{ID: capacityJobID, TenantID: capacitySource.TenantID, Kind: "source.sync", Actor: "catalog-source-maintainer", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + capacitySource.ID + `","sourceRevision":1,"trigger":"automatic"}`), State: jobs.StateQueued, NotBefore: now, AttemptLimit: 5, DeduplicationKey: "source.sync:" + capacitySource.ID + ":1", CreatedAt: now, UpdatedAt: now}
	if _, _, err := workerMaintenance.AdmitAutomaticSync(ctx, capacityCandidate, capacityJob, time.Minute, policyDigest, 1); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("global automatic-job ceiling returned %v", err)
	}
	if err := workerPool.QueryRow(ctx, `SELECT count(*) FROM source_maintenance_state WHERE tenant_id=$1 AND source_id=$2`, capacitySource.TenantID, capacitySource.ID).Scan(&maintenanceMarkerCount); err != nil || maintenanceMarkerCount != 0 {
		t.Fatalf("capacity-rejected maintenance marker count = %d, %v", maintenanceMarkerCount, err)
	}
	deduplicatedSource := capacitySource
	deduplicatedSource.ID, _ = generator.New(now.Add(108 * time.Nanosecond))
	deduplicatedSource.DisplayName = "Deduplicated maintenance"
	deduplicatedSource.Endpoint = "https://deduplicated-maintenance.example/.well-known/ard.json"
	if _, err := federationStore.CreateSource(ctx, deduplicatedSource); err != nil {
		t.Fatal(err)
	}
	deduplicatedCandidate := findMaintenanceCandidate(deduplicatedSource.TenantID, deduplicatedSource.ID)
	maintenanceJobs, err := NewPostgresJobStore(workerRepository)
	if err != nil {
		t.Fatal(err)
	}
	existingJobID, _ := generator.New(now.Add(109 * time.Nanosecond))
	deduplicationKey := "source.sync:" + deduplicatedSource.ID + ":1"
	existingAutomaticJob := jobs.Job{ID: existingJobID, TenantID: deduplicatedSource.TenantID, Kind: "source.sync", Actor: "catalog-source-maintainer", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + deduplicatedSource.ID + `","sourceRevision":1,"trigger":"automatic"}`), State: jobs.StateQueued, NotBefore: now, AttemptLimit: 5, DeduplicationKey: deduplicationKey, CreatedAt: now, UpdatedAt: now}
	if _, existing, err := maintenanceJobs.Enqueue(ctx, existingAutomaticJob); err != nil || existing {
		t.Fatalf("seed automatic job existing=%t, err=%v", existing, err)
	}
	duplicateAutomaticJob := existingAutomaticJob
	duplicateAutomaticJob.AttemptLimit = 9
	duplicateAutomaticJob.ID, _ = generator.New(now.Add(110 * time.Nanosecond))
	admittedDuplicate, existing, err := workerMaintenance.AdmitAutomaticSync(ctx, deduplicatedCandidate, duplicateAutomaticJob, time.Minute, policyDigest, 64)
	if err != nil || !existing || admittedDuplicate.ID != existingAutomaticJob.ID || admittedDuplicate.AttemptLimit != 5 {
		t.Fatalf("automatic admission deduplication = %#v existing=%t, %v", admittedDuplicate, existing, err)
	}
	if err := workerPool.QueryRow(ctx, `SELECT count(*) FROM source_maintenance_state WHERE tenant_id=$1 AND source_id=$2`, deduplicatedSource.TenantID, deduplicatedSource.ID).Scan(&maintenanceMarkerCount); err != nil || maintenanceMarkerCount != 1 {
		t.Fatalf("deduplicated maintenance marker count = %d, %v", maintenanceMarkerCount, err)
	}

	apiJobs, err := NewPostgresJobStore(apiRepository)
	if err != nil {
		t.Fatal(err)
	}
	manualSyncJobs, err := jobs.NewService(apiJobs, clock.NewFake(now.Add(time.Second)), generator, jobs.WithAuditEvidence(), jobs.WithMaxAttempts(9))
	if err != nil {
		t.Fatal(err)
	}
	manualSyncContext := identity.WithActor(ctx, sharedActor)
	manualSyncContext = audit.WithExecution(manualSyncContext, audit.Execution{RequestID: "manual-sync-reuse", AuthorizationDigest: sharedActor.AuthorizationDigest()})
	manualSyncInput := jobs.EnqueueInput{
		Kind:             "source.sync",
		PayloadVersion:   2,
		Payload:          json.RawMessage(`{"sourceId":"` + deduplicatedSource.ID + `","sourceRevision":1,"trigger":"manual"}`),
		DeduplicationKey: deduplicationKey,
		SourceSyncAudit:  &jobs.SourceSyncRequestAudit{SourceID: deduplicatedSource.ID, SourceRevision: 1, Trigger: "manual"},
	}
	reusedAutomaticJob, existing, err := manualSyncJobs.Enqueue(manualSyncContext, manualSyncInput)
	if err != nil || !existing || reusedAutomaticJob.ID != existingAutomaticJob.ID || reusedAutomaticJob.AttemptLimit != 5 {
		t.Fatalf("manual PostgreSQL synchronization reuse = %#v existing=%t, %v", reusedAutomaticJob, existing, err)
	}
	reuseAudit, err := apiRepository.AuditEvents(ctx, deduplicatedSource.TenantID, catalog.AuditEventListRequest{Filters: audit.Query{Action: audit.ActionSourceSyncRequested, TargetType: "federation_source", TargetID: deduplicatedSource.ID}, Limit: 2})
	if err != nil || len(reuseAudit) != 1 || reuseAudit[0].Actor != sharedActor.MembershipID || reuseAudit[0].RequestID != "manual-sync-reuse" {
		t.Fatalf("manual PostgreSQL synchronization reuse evidence = %#v, %v", reuseAudit, err)
	}
	reuseFacts, _ := reuseAudit[0].Details["facts"].(map[string]any)
	if reuseFacts["jobId"] != existingAutomaticJob.ID || reuseFacts["trigger"] != "manual" || reuseFacts["deduplicated"] != true {
		t.Fatalf("manual PostgreSQL synchronization reuse facts = %#v", reuseFacts)
	}

	misattributedSourceID, _ := generator.New(now.Add(112 * time.Nanosecond))
	misattributedJobID, _ := generator.New(now.Add(113 * time.Nanosecond))
	misattributedKey := "source.sync:" + misattributedSourceID + ":1"
	misattributedJob := jobs.Job{ID: misattributedJobID, TenantID: sharedActor.TenantID, Kind: "source.sync", Actor: sharedActor.MembershipID, PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + misattributedSourceID + `","sourceRevision":1,"trigger":"automatic"}`), State: jobs.StateQueued, NotBefore: now, AttemptLimit: 5, DeduplicationKey: misattributedKey, CreatedAt: now, UpdatedAt: now}
	if _, existing, err := apiJobs.Enqueue(ctx, misattributedJob); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || existing {
		t.Fatalf("reject misattributed PostgreSQL synchronization Job existing=%t, %v", existing, err)
	}
	misattributedInput := manualSyncInput
	misattributedInput.Payload = json.RawMessage(`{"sourceId":"` + misattributedSourceID + `","sourceRevision":1,"trigger":"manual"}`)
	misattributedInput.DeduplicationKey = misattributedKey
	misattributedInput.SourceSyncAudit = &jobs.SourceSyncRequestAudit{SourceID: misattributedSourceID, SourceRevision: 1, Trigger: "manual"}
	if _, _, err := manualSyncJobs.Enqueue(manualSyncContext, misattributedInput); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("misattributed manual PostgreSQL synchronization request returned %v", err)
	}
	misattributedAudit, err := apiRepository.AuditEvents(ctx, sharedActor.TenantID, catalog.AuditEventListRequest{Filters: audit.Query{Action: audit.ActionSourceSyncRequested, TargetType: "federation_source", TargetID: misattributedSourceID}, Limit: 1})
	if err != nil || len(misattributedAudit) != 0 {
		t.Fatalf("rejected PostgreSQL synchronization reuse evidence = %#v, %v", misattributedAudit, err)
	}
	// A new deployment default must not replace an explicit retry parent's budget.
	budgetClock := clock.NewFake(now)
	budgetService, err := jobs.NewService(apiJobs, budgetClock, generator, jobs.WithMaxAttempts(7))
	if err != nil {
		t.Fatal(err)
	}
	budgetContext := dedicatedAdminContext
	budgetParent, _, err := budgetService.Enqueue(budgetContext, jobs.EnqueueInput{Kind: "publication.generate", Payload: json.RawMessage(`{}`), Priority: 100, DeduplicationKey: "configured-retry-parent"})
	if err != nil || budgetParent.AttemptLimit != 7 {
		t.Fatalf("configured PostgreSQL budget: %#v %v", budgetParent, err)
	}
	budgetClaim, err := maintenanceJobs.Claim(ctx, "budget-worker", now, time.Minute)
	if err != nil || budgetClaim.ID != budgetParent.ID {
		t.Fatalf("budget parent claim: %#v %v", budgetClaim, err)
	}
	if _, err := maintenanceJobs.Fail(ctx, tenantA, budgetParent.ID, "budget-worker", now, jobs.FailurePermanentUpstream, "Permanent failure.", time.Time{}); err != nil {
		t.Fatal(err)
	}
	replacementService, err := jobs.NewService(apiJobs, budgetClock, generator, jobs.WithMaxAttempts(1))
	if err != nil {
		t.Fatal(err)
	}
	budgetChild, err := replacementService.Retry(budgetContext, budgetParent.ID, "Retry after configuration change")
	if err != nil || budgetChild.ParentJobID != budgetParent.ID || budgetChild.AttemptLimit != 7 {
		t.Fatalf("retained PostgreSQL parent budget: %#v %v", budgetChild, err)
	}
	retainedChild, err := apiJobs.Get(ctx, tenantA, budgetChild.ID)
	if err != nil || retainedChild.AttemptLimit != 7 {
		t.Fatalf("read back retry budget: %#v %v", retainedChild, err)
	}
	if _, err := apiJobs.RequestCancellation(ctx, tenantA, budgetChild.ID, now); err != nil {
		t.Fatal(err)
	}
	jobID, _ := generator.New(now.Add(11 * time.Nanosecond))
	job := jobs.Job{ID: jobID, TenantID: tenantA, Kind: "source.sync", Actor: "source-admin", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + maintenanceSource.ID + `","sourceRevision":1,"trigger":"manual"}`), State: jobs.StateQueued, Priority: 100, NotBefore: now, AttemptLimit: 3, DeduplicationKey: "job-one", CreatedAt: now, UpdatedAt: now}
	if _, _, err := apiJobs.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	duplicateJob := job
	duplicateJob.ID, _ = generator.New(now.Add(12 * time.Nanosecond))
	if existing, duplicate, err := apiJobs.Enqueue(ctx, duplicateJob); err != nil || !duplicate || existing.ID != job.ID {
		t.Fatalf("PostgreSQL live job deduplication = %#v, existing %v, %v", existing, duplicate, err)
	}
	cancelledJob := job
	cancelledJob.ID, _ = generator.New(now.Add(111 * time.Nanosecond))
	cancelledJob.Priority = 0
	cancelledJob.DeduplicationKey = "job-cancel"
	if _, _, err := apiJobs.Enqueue(ctx, cancelledJob); err != nil {
		t.Fatal(err)
	}
	cancelAt := now.Add(time.Minute)
	cancelledJob, err = apiJobs.RequestCancellation(ctx, tenantA, cancelledJob.ID, cancelAt)
	if err != nil || cancelledJob.State != jobs.StateCancelled || cancelledJob.CancellationRequested || !cancelledJob.UpdatedAt.Equal(cancelAt) {
		t.Fatalf("cancelled queued PostgreSQL Job = %#v, %v", cancelledJob, err)
	}
	if repeatedCancellation, cancelErr := apiJobs.RequestCancellation(ctx, tenantA, cancelledJob.ID, cancelAt.Add(time.Minute)); cancelErr != nil || repeatedCancellation.ID != cancelledJob.ID || repeatedCancellation.State != jobs.StateCancelled || !repeatedCancellation.UpdatedAt.Equal(cancelAt) {
		t.Fatalf("repeated PostgreSQL Job cancellation = %#v, %v", repeatedCancellation, cancelErr)
	}
	if _, cancelErr := apiJobs.RequestCancellation(ctx, tenantB, cancelledJob.ID, cancelAt); catalog.ErrorCodeOf(cancelErr) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL Job cancellation returned %v", cancelErr)
	}
	if _, err := apiJobs.Claim(ctx, "api-should-not-claim", now, time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("API worker-job claim access returned %v", err)
	}
	seenJobs := make(map[string]struct{})
	var jobAfterCreatedAt *time.Time
	jobAfterID := ""
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		page, listErr := apiJobs.List(ctx, tenantA, jobs.ListRequest{AfterCreatedAt: jobAfterCreatedAt, AfterID: jobAfterID, Limit: 2})
		if listErr != nil || len(page) > 2 {
			t.Fatalf("PostgreSQL Job page %d = %#v, %v", pageNumber, page, listErr)
		}
		if len(page) == 0 {
			break
		}
		for _, item := range page {
			if item.TenantID != tenantA {
				t.Fatalf("PostgreSQL Job page crossed tenant boundary: %#v", item)
			}
			if _, duplicate := seenJobs[item.ID]; duplicate {
				t.Fatalf("PostgreSQL Job page repeated %s", item.ID)
			}
			seenJobs[item.ID] = struct{}{}
		}
		position := page[len(page)-1].CreatedAt
		jobAfterCreatedAt, jobAfterID = &position, page[len(page)-1].ID
		if pageNumber == 99 {
			t.Fatal("PostgreSQL Job pagination did not terminate")
		}
	}
	if len(seenJobs) < 2 {
		t.Fatalf("PostgreSQL Job traversal saw only %#v", seenJobs)
	}
	if _, err := apiJobs.Get(ctx, tenantB, job.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant exact PostgreSQL Job read returned %v", err)
	}
	seenResources := make(map[string]struct{})
	var resourceAfterUpdatedAt *time.Time
	resourceAfterID := ""
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		page, listErr := apiRepository.ListResources(ctx, tenantA, catalog.ResourceListRequest{AfterUpdatedAt: resourceAfterUpdatedAt, AfterID: resourceAfterID, Limit: 3})
		if listErr != nil || len(page) > 3 {
			t.Fatalf("PostgreSQL Resource page %d = %#v, %v", pageNumber, page, listErr)
		}
		if len(page) == 0 {
			break
		}
		for _, item := range page {
			if item.TenantID != tenantA {
				t.Fatalf("PostgreSQL Resource page crossed tenant boundary: %#v", item)
			}
			if _, duplicate := seenResources[item.ID]; duplicate {
				t.Fatalf("PostgreSQL Resource page repeated %s", item.ID)
			}
			seenResources[item.ID] = struct{}{}
		}
		position := page[len(page)-1].UpdatedAt
		resourceAfterUpdatedAt, resourceAfterID = &position, page[len(page)-1].ID
		if pageNumber == 99 {
			t.Fatal("PostgreSQL Resource pagination did not terminate")
		}
	}
	if len(seenResources) < 3 {
		t.Fatalf("PostgreSQL Resource traversal saw only %#v", seenResources)
	}
	seenAuditEvents := make(map[string]struct{})
	var auditAfterOccurredAt *time.Time
	auditAfterID := ""
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		page, listErr := apiRepository.AuditEvents(ctx, tenantA, catalog.AuditEventListRequest{AfterOccurredAt: auditAfterOccurredAt, AfterID: auditAfterID, Limit: 7})
		if listErr != nil || len(page) > 7 {
			t.Fatalf("PostgreSQL audit-event page %d = %#v, %v", pageNumber, page, listErr)
		}
		if len(page) == 0 {
			break
		}
		for _, item := range page {
			if item.TenantID != tenantA {
				t.Fatalf("PostgreSQL audit-event page crossed tenant boundary: %#v", item)
			}
			if _, duplicate := seenAuditEvents[item.ID]; duplicate {
				t.Fatalf("PostgreSQL audit-event page repeated %s", item.ID)
			}
			seenAuditEvents[item.ID] = struct{}{}
		}
		position := page[len(page)-1].At
		auditAfterOccurredAt, auditAfterID = &position, page[len(page)-1].ID
		if pageNumber == 99 {
			t.Fatal("PostgreSQL audit-event pagination did not terminate")
		}
	}
	if len(seenAuditEvents) < 7 {
		t.Fatalf("PostgreSQL audit-event traversal saw only %d rows", len(seenAuditEvents))
	}
	auditFrom, auditBefore := now.Add(-time.Hour), now.Add(2*time.Hour)
	auditFilters := audit.Query{Action: "resource.created", TargetType: "resource", TargetID: resource.ID, OccurredFrom: &auditFrom, OccurredBefore: &auditBefore}
	filteredAuditEvents, err := apiRepository.AuditEvents(ctx, tenantA, catalog.AuditEventListRequest{Filters: auditFilters, Limit: 1})
	if err != nil || len(filteredAuditEvents) != 1 || filteredAuditEvents[0].Action != auditFilters.Action || filteredAuditEvents[0].TargetID != resource.ID {
		t.Fatalf("filtered PostgreSQL audit page = %#v, %v", filteredAuditEvents, err)
	}
	otherTenantAuditEvents, err := apiRepository.AuditEvents(ctx, tenantB, catalog.AuditEventListRequest{Filters: auditFilters, Limit: 1})
	if err != nil || len(otherTenantAuditEvents) != 0 {
		t.Fatalf("cross-tenant filtered audit page = %#v, %v", otherTenantAuditEvents, err)
	}
	if _, err := operatorPool.Exec(ctx, `ANALYZE audit_events`); err != nil {
		t.Fatal(err)
	}
	requireAuditInvestigationPlans(t, ctx, apiPool, tenantA, resource.ID, auditFrom, auditBefore)
	var auditCountBeforeFailedExport int
	if err := operatorPool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1`, tenantA).Scan(&auditCountBeforeFailedExport); err != nil {
		t.Fatal(err)
	}
	invalidAccess := catalog.AuditEvent{ID: "00000000-0000-7000-8000-000000000198", TenantID: tenantA, Actor: "legacy-writer", Action: "audit.exported", TargetType: "audit_range", TargetID: tenantA, Reason: "invalid legacy access", At: auditBefore}
	if _, err := apiRepository.ExportAuditEvents(ctx, tenantA, catalog.AuditExportRequest{Filters: auditFilters, Cutoff: auditBefore, GeneratedAt: auditBefore, Access: invalidAccess}); err == nil {
		t.Fatal("PostgreSQL audit export accepted a legacy access row")
	}
	var auditCountAfterFailedExport int
	if err := operatorPool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1`, tenantA).Scan(&auditCountAfterFailedExport); err != nil || auditCountAfterFailedExport != auditCountBeforeFailedExport {
		t.Fatalf("failed PostgreSQL export changed audit rows from %d to %d: %v", auditCountBeforeFailedExport, auditCountAfterFailedExport, err)
	}
	exportAccessID, _ := generator.New(auditBefore)
	exportAccess := integrationAudit(t, exportAccessID, tenantA, "audit-export", "audit.exported", "audit_range", tenantA, "qualify restore comparison", auditBefore)
	auditExport, err := apiRepository.ExportAuditEvents(ctx, tenantA, catalog.AuditExportRequest{Filters: auditFilters, Cutoff: auditBefore, GeneratedAt: auditBefore, Access: exportAccess})
	if err != nil || auditExport.Manifest.RowCount != 1 || auditExport.Manifest.First == nil || auditExport.Manifest.Last == nil || auditExport.Manifest.First.ID != filteredAuditEvents[0].ID || auditExport.Manifest.Last.ID != filteredAuditEvents[0].ID {
		t.Fatalf("PostgreSQL audit export = %#v, %v", auditExport, err)
	}
	exportRaw, err := json.Marshal(auditExport)
	if err != nil {
		t.Fatal(err)
	}
	verifiedExport, fileReport, err := audit.VerifyExport(exportRaw)
	if err != nil || !fileReport.Valid || !bytes.Equal(verifiedExport.RawEvents, auditExport.RawEvents) {
		t.Fatalf("PostgreSQL export verification = %#v, %v", fileReport, err)
	}
	trustedManifest, err := audit.BindTrustedManifest(auditExport, "integration-recovery-point")
	if err != nil {
		t.Fatal(err)
	}
	var auditCountBeforeComparison int
	if err := operatorPool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1`, tenantA).Scan(&auditCountBeforeComparison); err != nil {
		t.Fatal(err)
	}
	t.Run("audit comparison cancellation during a read", func(t *testing.T) {
		testInterruptedRestoreRead(t, operatorPool, "audit_events", func(readCtx context.Context) (any, error) {
			return CompareRestoredAudit(readCtx, operatorPool, trustedManifest, "integration-recovery-point")
		})
	})
	restoreReport, err := CompareRestoredAudit(ctx, operatorPool, trustedManifest, "integration-recovery-point")
	if err != nil || !restoreReport.Valid || restoreReport.RowCount != auditExport.Manifest.RowCount {
		t.Fatalf("restored audit comparison = %#v, %v", restoreReport, err)
	}
	var auditCountAfterComparison int
	if err := operatorPool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1`, tenantA).Scan(&auditCountAfterComparison); err != nil || auditCountAfterComparison != auditCountBeforeComparison {
		t.Fatalf("read-only restored comparison changed audit count from %d to %d: %v", auditCountBeforeComparison, auditCountAfterComparison, err)
	}
	divergentAuditID, _ := generator.New(now.Add(time.Minute))
	divergentAudit := integrationAudit(t, divergentAuditID, tenantA, "restore-divergence", "resource.created", "resource", resource.ID, "", now.Add(time.Minute))
	if err := apiRepository.AppendAuditEvent(ctx, divergentAudit); err != nil {
		t.Fatal(err)
	}
	if _, err := CompareRestoredAudit(ctx, operatorPool, trustedManifest, "integration-recovery-point"); err == nil {
		t.Fatal("restored audit comparison accepted a changed selected range")
	}
	workerJobs, err := NewPostgresJobStore(workerRepository)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := workerJobs.Claim(ctx, "worker", now, time.Minute)
	if err != nil || claimed.ID != job.ID || claimed.State != jobs.StateRunning {
		t.Fatalf("worker claim = %#v, %v", claimed, err)
	}
	if _, err := workerJobs.Heartbeat(ctx, tenantB, claimed.ID, "worker", now, time.Minute, claimed.Progress); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant worker heartbeat returned %v", err)
	}
	if _, err := workerJobs.Complete(ctx, tenantB, claimed.ID, "worker", now, json.RawMessage(`{}`)); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant worker completion returned %v", err)
	}
	if _, err := workerJobs.Fail(ctx, tenantB, claimed.ID, "worker", now, jobs.FailureInternal, "failed", time.Time{}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant worker failure returned %v", err)
	}
	retryAt := now.Add(2 * time.Minute)
	waitingJob, err := workerJobs.Fail(ctx, tenantA, claimed.ID, "worker", now, jobs.FailureTransientUpstream, "retry later", retryAt)
	if err != nil || waitingJob.State != jobs.StateWaiting || waitingJob.SafeError == "" || waitingJob.FailureClass == "" {
		t.Fatalf("waiting PostgreSQL Job = %#v, %v", waitingJob, err)
	}
	// Read back PostgreSQL's timestamp precision before checking the due boundary.
	waitingJob, err = apiJobs.Get(ctx, tenantA, job.ID)
	if err != nil || !waitingJob.NotBefore.Equal(retryAt) || waitingJob.AttemptLimit != 3 {
		t.Fatalf("retained retry schedule: %#v %v", waitingJob, err)
	}
	early, earlyErr := workerJobs.Claim(ctx, "worker-early", waitingJob.NotBefore.Add(-time.Microsecond), time.Minute)
	if earlyErr != nil && catalog.ErrorCodeOf(earlyErr) != catalog.CodeNotFound {
		t.Fatal(earlyErr)
	}
	if earlyErr == nil && early.ID == job.ID {
		t.Fatal("PostgreSQL claimed Job before its retained due time")
	}
	claimed, err = workerJobs.Claim(ctx, "worker-retry", waitingJob.NotBefore, time.Minute)
	if err != nil || claimed.ID != job.ID || claimed.State != jobs.StateRunning || claimed.SafeError != "" || claimed.FailureClass != "" || claimed.Attempt != 2 {
		t.Fatalf("retried PostgreSQL Job = %#v, %v", claimed, err)
	}
	// Repeated running cancellation must return the timestamp actually retained
	// by PostgreSQL, including when the cancellation flag was already set.
	for _, at := range []time.Time{retryAt.Add(time.Second), retryAt.Add(2 * time.Second)} {
		cancelled, cancelErr := apiJobs.RequestCancellation(ctx, tenantA, claimed.ID, at)
		if cancelErr != nil || cancelled.State != jobs.StateRunning || !cancelled.CancellationRequested || !cancelled.UpdatedAt.Equal(at) {
			t.Fatalf("running cancellation = %#v, %v", cancelled, cancelErr)
		}
		retained, readErr := apiJobs.Get(ctx, tenantA, claimed.ID)
		if readErr != nil || retained.State != cancelled.State || !retained.CancellationRequested || !retained.UpdatedAt.Equal(cancelled.UpdatedAt) || retained.LeaseOwner != claimed.LeaseOwner || retained.LeaseExpiresAt == nil || !retained.LeaseExpiresAt.Equal(*claimed.LeaseExpiresAt) {
			t.Fatalf("running cancellation readback = %#v, %v", retained, readErr)
		}
	}
	if events, err := apiRepository.ClaimOutbox(ctx, "api", now, time.Minute, 10); catalog.ErrorCodeOf(err) != catalog.CodeForbidden || len(events) != 0 {
		t.Fatalf("API outbox claim access = %#v, %v", events, err)
	}
	events, err := workerRepository.ClaimOutbox(ctx, "outbox-worker-one", now, time.Minute, 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("worker outbox claim = %#v, %v", events, err)
	}
	if err := workerRepository.MarkOutboxPublished(ctx, events[0].TenantID, events[0].ID, "outbox-worker-two", now.Add(time.Second)); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("non-owner outbox acknowledgement returned %v", err)
	}
	if err := workerRepository.RenewOutbox(ctx, events[0].TenantID, events[0].ID, "outbox-worker-two", now.Add(time.Second), time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("non-owner outbox renewal returned %v", err)
	}
	otherTenantID := tenantB
	if events[0].TenantID == tenantB {
		otherTenantID = tenantA
	}
	if err := workerRepository.RenewOutbox(ctx, otherTenantID, events[0].ID, "outbox-worker-one", now.Add(time.Second), time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant outbox renewal returned %v", err)
	}
	if err := workerRepository.MarkOutboxPublished(ctx, otherTenantID, events[0].ID, "outbox-worker-one", now.Add(time.Second)); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant outbox acknowledgement returned %v", err)
	}
	if err := workerRepository.ReleaseOutbox(ctx, otherTenantID, events[0].ID, "outbox-worker-one"); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cross-tenant outbox release returned %v", err)
	}
	if err := workerRepository.RenewOutbox(ctx, events[0].TenantID, events[0].ID, "outbox-worker-one", now.Add(time.Second), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	second, err := workerRepository.ClaimOutbox(ctx, "outbox-worker-two", now, time.Minute, 10)
	if err != nil || len(second) == 0 {
		t.Fatalf("second worker claim = %#v, %v", second, err)
	}
	for _, event := range second {
		if event.ID == events[0].ID {
			t.Fatalf("leased event was claimed by another worker: %#v", event)
		}
	}
	if err := workerRepository.MarkOutboxPublished(ctx, events[0].TenantID, events[0].ID, "outbox-worker-one", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := operatorPool.Exec(ctx, `UPDATE webhook_endpoints SET status='disabled',revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`, tenantA, alternateWebhook.ID); err != nil {
		t.Fatalf("disable webhook endpoint for terminal-state test: %v", err)
	}
	workerEvidenceAt := now.Add(2 * time.Second).UTC().Truncate(time.Microsecond)
	workerAuditID := "00000000-0000-7000-8000-000000000097"
	workerSnapshotID := "00000000-0000-7000-8000-000000000098"
	workerJobID := "00000000-0000-7000-8000-000000000096"
	workerOutboxID := "00000000-0000-7000-8000-000000000095"
	workerFacts, err := audit.Facts(map[string]any{
		"sourceId": targetSource.ID, "sourceRevision": int64(1), "state": "complete",
		"policyDigest": "sha256:" + strings.Repeat("a", 64), "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0,
		"jobId": workerJobID, "jobKind": "source.sync", "jobAttempt": 1, "initiator": audit.InitiatorSourceMaintainer, "trigger": "automatic",
	})
	if err != nil {
		t.Fatal(err)
	}
	workerAudit, err := audit.NewRecord(workerAuditID, tenantA, audit.Principal{ID: audit.PrincipalIDCatalogWorker, Type: audit.PrincipalServiceExecutor}, audit.ActionSourceSyncCompleted, "source_snapshot", workerSnapshotID, "", audit.Execution{}, workerFacts, workerEvidenceAt)
	if err != nil {
		t.Fatal(err)
	}
	workerAudit.AfterDigest = audit.DigestBytes([]byte("worker-source-snapshot"))
	workerOutboxData, err := catalogevents.MarshalReference("source.sync.completed", "source_snapshot", workerSnapshotID, workerAudit.AfterDigest)
	if err != nil {
		t.Fatal(err)
	}
	workerOutbox := catalog.OutboxEvent{ID: workerOutboxID, TenantID: tenantA, Type: catalogevents.CanonicalType("source.sync.completed"), Subject: workerSnapshotID, Data: workerOutboxData, CreatedAt: workerEvidenceAt, PublishedAt: &workerEvidenceAt}
	if err := workerRepository.AppendEvidence(ctx, workerAudit, &workerOutbox); err != nil {
		t.Fatalf("worker exact evidence insert returned %v", err)
	}
	var workerAuditCount, workerOutboxCount int
	if err := operatorPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND id=$2),(SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND id=$3)`, tenantA, workerAuditID, workerOutboxID).Scan(&workerAuditCount, &workerOutboxCount); err != nil || workerAuditCount != 1 || workerOutboxCount != 1 {
		t.Fatalf("worker exact evidence = audit %d outbox %d, %v", workerAuditCount, workerOutboxCount, err)
	}

	for _, testCase := range []struct {
		name      string
		operation func() error
	}{
		{name: "API cannot update worker source state", operation: func() error {
			_, mutationErr := apiPool.Exec(ctx, `UPDATE federation_sources SET last_error='forbidden'`)
			return mutationErr
		}},
		{name: "worker cannot update source configuration", operation: func() error {
			_, mutationErr := workerPool.Exec(ctx, `UPDATE federation_sources SET configuration=configuration`)
			return mutationErr
		}},
		{name: "API cannot update worker job progress", operation: func() error {
			_, mutationErr := apiPool.Exec(ctx, `UPDATE jobs SET progress=progress`)
			return mutationErr
		}},
		{name: "worker cannot read audit evidence", operation: func() error {
			var count int
			return workerPool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1`, tenantA).Scan(&count)
		}},
		{name: "worker cannot update audit evidence", operation: func() error {
			_, mutationErr := workerPool.Exec(ctx, `UPDATE audit_events SET reason=reason WHERE tenant_id=$1 AND id=$2`, tenantA, workerAuditID)
			return mutationErr
		}},
		{name: "worker cannot delete audit evidence", operation: func() error {
			_, mutationErr := workerPool.Exec(ctx, `DELETE FROM audit_events WHERE tenant_id=$1 AND id=$2`, tenantA, workerAuditID)
			return mutationErr
		}},
		{name: "worker cannot update publication state", operation: func() error {
			_, mutationErr := workerPool.Exec(ctx, `UPDATE publication_snapshots SET state=state WHERE tenant_id=$1 AND id=$2`, tenantA, timeGuardSnapshot.ID)
			return mutationErr
		}},
		{name: "API cannot delete retained rows", operation: func() error {
			_, mutationErr := apiPool.Exec(ctx, `DELETE FROM namespaces`)
			return mutationErr
		}},
	} {
		t.Run("least privilege/"+testCase.name, func(t *testing.T) {
			requireInsufficientPrivilege(t, testCase.operation())
		})
	}

	advancedSkillVersion, _, _, err := catalogService.CreateVersionRevision(authorContext, skillVersion.ID, []catalog.RepresentationInput{{
		Role:      catalog.RepresentationPrimary,
		MediaType: adapter.MediaSkillMarkdown,
		Content:   "---\nname: postgres-skill\ndescription: Proves monotonic PostgreSQL candidate pointers.\n---\n\n# Instructions\nKeep candidate revisions moving forward.\n",
	}}, skillVersion.Revision)
	if err != nil || advancedSkillVersion.CandidateRevisionID == skillRevision.ID {
		t.Fatalf("advanced PostgreSQL Skill candidate = %#v, %v", advancedSkillVersion, err)
	}
	advancedListingInput := listingInput
	advancedListingInput.Description = "Exercises monotonic ListingRevision candidate pointers on PostgreSQL."
	advancedListing, advancedListingRevision, err := catalogService.CreateListingRevision(authorContext, publishedListing.ID, advancedListingInput, publishedListing.Revision)
	if err != nil || advancedListing.CandidateRevisionID != advancedListingRevision.ID || advancedListingRevision.RevisionNumber <= listingRevision.RevisionNumber {
		t.Fatalf("advanced PostgreSQL listing candidate = %#v / %#v, %v", advancedListing, advancedListingRevision, err)
	}

	invalidPointerMutations := []struct {
		name       string
		constraint string
		statement  string
		arguments  []any
	}{
		{
			name:       "version candidate belongs to its version",
			constraint: "resource_versions_candidate_parent_fk",
			statement:  `UPDATE resource_versions SET candidate_revision_id=$3,revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments:  []any{tenantA, skillVersion.ID, packageRevision.ID},
		},
		{
			name:       "listing candidate belongs to its listing",
			constraint: "catalog_listings_candidate_parent_fk",
			statement:  `UPDATE catalog_listings SET candidate_revision_id=$3,revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments:  []any{tenantA, publishedListing.ID, externalListing.CandidateRevisionID},
		},
		{
			name:       "current release belongs to its listing",
			constraint: "catalog_listings_release_parent_fk",
			statement:  `UPDATE catalog_listings SET current_release_id=$3,revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments:  []any{tenantA, publishedListing.ID, externalRelease.ID},
		},
		{
			name:       "source snapshot belongs to its source",
			constraint: "federation_sources_snapshot_parent_fk",
			statement:  `UPDATE federation_sources SET last_complete_snapshot_id=$3,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments:  []any{tenantA, targetSource.ID, sourceSnapshot.ID},
		},
	}
	for _, testCase := range invalidPointerMutations {
		t.Run("parent-bound pointers/"+testCase.name, func(t *testing.T) {
			_, mutationErr := operatorPool.Exec(ctx, testCase.statement, testCase.arguments...)
			if mutationErr == nil || !strings.Contains(mutationErr.Error(), testCase.constraint) {
				t.Fatalf("invalid pointer mutation returned %v", mutationErr)
			}
		})
	}
	func() {
		tx, transactionErr := operatorPool.Begin(ctx)
		if transactionErr != nil {
			t.Fatal(transactionErr)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, transactionErr = tx.Exec(ctx, `
          UPDATE listing_releases
             SET state='superseded',replacement_release_id=$3,disposition_reason='governed identity replacement',revision=revision+1,updated_at=updated_at+interval '1 second'
           WHERE tenant_id=$1 AND id=$2`, tenantA, localRelease.ID, externalRelease.ID); transactionErr != nil {
			t.Fatalf("set cross-listing replacement: %v", transactionErr)
		}
		if _, transactionErr = tx.Exec(ctx, `
          UPDATE catalog_listings
             SET current_release_id=NULL,revision=revision+1,updated_at=updated_at+interval '1 second'
           WHERE tenant_id=$1 AND id=$2 AND current_release_id=$3`, tenantA, publishedListing.ID, localRelease.ID); transactionErr != nil {
			t.Fatalf("clear superseded listing pointer: %v", transactionErr)
		}
		if _, transactionErr = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); transactionErr != nil {
			t.Fatalf("valid cross-listing replacement was rejected: %v", transactionErr)
		}
	}()
	duplicateCurrentReleaseID, _ := generator.New(now.Add(129 * time.Nanosecond))
	duplicateCurrentTx, err := operatorPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = duplicateCurrentTx.Exec(ctx, `
      INSERT INTO listing_releases(
        id,tenant_id,listing_id,submission_id,version_revision_id,listing_revision_id,state,
        disposition_reason,activated_by,revision,activated_at,updated_at
      ) VALUES ($1,$2,$3,$4,$5,$6,'published','','integrity-test',1,$7,$7)`,
		duplicateCurrentReleaseID, tenantA, publishedListing.ID, currentSubmission.ID,
		currentSubmission.VersionRevisionID, currentSubmission.ListingRevisionID, now); err != nil {
		_ = duplicateCurrentTx.Rollback(ctx)
		t.Fatalf("stage duplicate current release: %v", err)
	}
	if _, err = duplicateCurrentTx.Exec(ctx, `
      UPDATE catalog_listings
         SET current_release_id=$3,revision=revision+1,updated_at=updated_at+interval '1 second'
       WHERE tenant_id=$1 AND id=$2`, tenantA, publishedListing.ID, duplicateCurrentReleaseID); err != nil {
		_ = duplicateCurrentTx.Rollback(ctx)
		t.Fatalf("stage invalid current release pointer swap: %v", err)
	}
	if err = duplicateCurrentTx.Commit(ctx); err == nil || !strings.Contains(err.Error(), "published or deprecated listing release must be the current release") {
		t.Fatalf("pointer swap leaving two active releases returned %v", err)
	}
	invalidFailedSnapshotID, _ := generator.New(now.Add(126 * time.Nanosecond))
	if _, err := operatorPool.Exec(ctx, `
      INSERT INTO source_snapshots(
        id,tenant_id,source_id,source_revision,state,enumeration_complete,withdrawal_authority,
        member_count,body_digest,details,started_at,completed_at
      ) VALUES ($1,$2,$3,$4,'failed',false,false,1,'','{}',$5,$5)`,
		invalidFailedSnapshotID, tenantA, source.ID, source.Revision, now); err == nil || !strings.Contains(err.Error(), "source_snapshots_authority_check") {
		t.Fatalf("failed source snapshot with members returned %v", err)
	}
	if _, err := operatorPool.Exec(ctx, `
		UPDATE listing_releases
		   SET state='deprecated',disposition_reason='invalid past sunset',sunset_at=updated_at,
		       revision=revision+1,updated_at=updated_at+interval '1 second'
		 WHERE tenant_id=$1 AND id=$2`, tenantA, localRelease.ID); err == nil || !strings.Contains(err.Error(), "listing_releases_guidance_check") {
		t.Fatalf("past release sunset returned %v", err)
	}

	mismatchedSubmissionID, _ := generator.New(now.Add(120 * time.Nanosecond))
	if _, err := operatorPool.Exec(ctx, `
      INSERT INTO review_submissions(
        id,tenant_id,listing_id,version_revision_id,listing_revision_id,evidence_set_id,evidence_digest,
        policy_version,state,submitted_by,decisions,revision,submitted_at
      ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'submitted','integrity-test','[]',1,$9)`,
		mismatchedSubmissionID, tenantA, publishedListing.ID, externalSubmission.VersionRevisionID,
		externalSubmission.ListingRevisionID, externalSubmission.EvidenceSetID, externalSubmission.EvidenceDigest,
		externalSubmission.PolicyVersion, now); err == nil || !strings.Contains(err.Error(), "submissions_listing_revision_identity_fk") {
		t.Fatalf("cross-listing review submission returned %v", err)
	}
	mismatchedEvidenceSubmissionID, _ := generator.New(now.Add(121 * time.Nanosecond))
	if _, err := operatorPool.Exec(ctx, `
      INSERT INTO review_submissions(
        id,tenant_id,listing_id,version_revision_id,listing_revision_id,evidence_set_id,evidence_digest,
        policy_version,state,submitted_by,decisions,revision,submitted_at
      ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'submitted','integrity-test','[]',1,$9)`,
		mismatchedEvidenceSubmissionID, tenantA, currentSubmission.ListingID, currentSubmission.VersionRevisionID,
		currentSubmission.ListingRevisionID, externalSubmission.EvidenceSetID, externalSubmission.EvidenceDigest,
		externalSubmission.PolicyVersion, now); err == nil || !strings.Contains(err.Error(), "submissions_validation_evidence_fk") {
		t.Fatalf("cross-version review evidence returned %v", err)
	}
	mismatchedPolicySubmissionID, _ := generator.New(now.Add(124 * time.Nanosecond))
	if _, err := operatorPool.Exec(ctx, `
      INSERT INTO review_submissions(
        id,tenant_id,listing_id,version_revision_id,listing_revision_id,evidence_set_id,evidence_digest,
        policy_version,state,submitted_by,decisions,revision,submitted_at
      ) VALUES ($1,$2,$3,$4,$5,$6,$7,'wrong-policy','submitted','integrity-test','[]',1,$8)`,
		mismatchedPolicySubmissionID, tenantA, currentSubmission.ListingID, currentSubmission.VersionRevisionID,
		currentSubmission.ListingRevisionID, currentSubmission.EvidenceSetID, currentSubmission.EvidenceDigest,
		now); err == nil || !strings.Contains(err.Error(), "submissions_evidence_identity_fk") {
		t.Fatalf("mismatched review policy evidence returned %v", err)
	}
	mismatchedReleaseID, _ := generator.New(now.Add(122 * time.Nanosecond))
	if _, err := operatorPool.Exec(ctx, `
      INSERT INTO listing_releases(
        id,tenant_id,listing_id,submission_id,version_revision_id,listing_revision_id,state,
        disposition_reason,activated_by,revision,activated_at,updated_at
      ) VALUES ($1,$2,$3,$4,$5,$6,'published','','integrity-test',1,$7,$7)`,
		mismatchedReleaseID, tenantA, publishedListing.ID, externalSubmission.ID,
		externalSubmission.VersionRevisionID, externalSubmission.ListingRevisionID, now); err == nil || !strings.Contains(err.Error(), "releases_submission_identity_fk") {
		t.Fatalf("cross-listing release submission returned %v", err)
	}
	unapprovedReleaseID, _ := generator.New(now.Add(125 * time.Nanosecond))
	if _, err := operatorPool.Exec(ctx, `
      INSERT INTO listing_releases(
        id,tenant_id,listing_id,submission_id,version_revision_id,listing_revision_id,state,
        disposition_reason,activated_by,revision,activated_at,updated_at
      ) VALUES ($1,$2,$3,$4,$5,$6,'published','','integrity-test',1,$7,$7)`,
		unapprovedReleaseID, tenantA, submission.ListingID, submission.ID,
		submission.VersionRevisionID, submission.ListingRevisionID, now); err == nil || !strings.Contains(err.Error(), "new listing release requires an approved review submission") {
		t.Fatalf("unapproved release submission returned %v", err)
	}
	unselectedReleaseID, _ := generator.New(now.Add(123 * time.Nanosecond))
	if _, err := operatorPool.Exec(ctx, `
      INSERT INTO listing_releases(
        id,tenant_id,listing_id,submission_id,version_revision_id,listing_revision_id,state,
        disposition_reason,activated_by,revision,activated_at,updated_at
      ) VALUES ($1,$2,$3,$4,$5,$6,'published','','integrity-test',1,$7,$7)`,
		unselectedReleaseID, tenantA, publishedListing.ID, currentSubmission.ID,
		currentSubmission.VersionRevisionID, currentSubmission.ListingRevisionID, now); err == nil || !strings.Contains(err.Error(), "published or deprecated listing release must be the current release") {
		t.Fatalf("unselected published release returned %v", err)
	}
	invalidDeliveryID, _ := generator.New(now.Add(127 * time.Nanosecond))
	invalidDeliveryEventID, _ := generator.New(now.Add(128 * time.Nanosecond))
	if _, err := operatorPool.Exec(ctx, `
      INSERT INTO webhook_deliveries(
        id,tenant_id,endpoint_id,event_id,event_type,state,attempt,created_at,updated_at
      ) VALUES ($1,$2,$3,$4,$5,'pending',1,$6,$6)`,
		invalidDeliveryID, tenantA, endpoint.ID, invalidDeliveryEventID,
		catalog.CanonicalEventType("resource.created"), now); err == nil || !strings.Contains(err.Error(), "webhook_deliveries_state_shape_check") {
		t.Fatalf("invalid pending webhook delivery returned %v", err)
	}
	invalidPublicationMetadataID, _ := generator.New(now.Add(130 * time.Nanosecond))
	if _, err := operatorPool.Exec(ctx, `
      INSERT INTO publication_snapshots(
        id,tenant_id,host,document_family,profile_id,generation,state,etag,document,created_at,activated_at
      )
      SELECT $1,tenant_id,host,document_family,profile_id,generation+1,state,etag,document,created_at,activated_at
        FROM publication_snapshots
       WHERE tenant_id=$2 AND id=$3`, invalidPublicationMetadataID, tenantA, timeGuardSnapshot.ID); err == nil || !strings.Contains(err.Error(), "publication snapshot row and immutable document metadata are inconsistent") {
		t.Fatalf("inconsistent publication snapshot metadata returned %v", err)
	}

	for _, testCase := range []struct {
		name      string
		message   string
		statement string
		arguments []any
	}{
		{
			name:      "namespace identity",
			message:   "namespace lifecycle transition is invalid",
			statement: `UPDATE namespaces SET slug='tampered',revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, namespaceA.ID},
		},
		{
			name:      "publisher revision",
			message:   "publisher lifecycle transition is invalid",
			statement: `UPDATE publishers SET display_name='tampered' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, publisher.ID},
		},
		{
			name:      "version candidate rollback",
			message:   "resource version candidate revision must move forward",
			statement: `UPDATE resource_versions SET candidate_revision_id=$3,revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, advancedSkillVersion.ID, skillRevision.ID},
		},
		{
			name:      "version sealing candidate switch",
			message:   "resource version pointer transition is invalid",
			statement: `UPDATE resource_versions SET candidate_revision_id=$3,sealed_revision_id=$3,revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, advancedSkillVersion.ID, skillRevision.ID},
		},
		{
			name:      "listing candidate rollback",
			message:   "Catalog listing candidate revision must move forward",
			statement: `UPDATE catalog_listings SET candidate_revision_id=$3,revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, advancedListing.ID, listingRevision.ID},
		},
		{
			name:      "listing candidate removal",
			message:   "must change exactly one current pointer",
			statement: `UPDATE catalog_listings SET candidate_revision_id=NULL,revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, publishedListing.ID},
		},
		{
			name:      "terminal review",
			message:   "review submission lifecycle transition is invalid",
			statement: `UPDATE review_submissions SET revision=revision+1 WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, currentSubmission.ID},
		},
		{
			name:      "terminal release left current",
			message:   "Catalog listing current release must remain published or deprecated",
			statement: `UPDATE listing_releases SET state='retired',disposition_reason='invalid retained pointer',revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, localRelease.ID},
		},
		{
			name:      "release identity",
			message:   "listing release lifecycle transition is invalid",
			statement: `UPDATE listing_releases SET activated_by='tampered',state='retired',disposition_reason='tampered',revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, localRelease.ID},
		},
		{
			name:      "source configuration without revision",
			message:   "configuration requires a new revision",
			statement: `UPDATE federation_sources SET configuration=configuration||'{"tampered":true}'::jsonb,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, source.ID},
		},
		{
			name:      "source error without synchronization",
			message:   "error state requires synchronization evidence",
			statement: `UPDATE federation_sources SET status='error',revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, targetSource.ID},
		},
		{
			name:      "incomplete source snapshot pointer",
			message:   "federation source last complete snapshot must be complete",
			statement: `UPDATE federation_sources SET last_complete_snapshot_id=$3,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, source.ID, partialSnapshot.ID},
		},
		{
			name:      "complete source snapshot rollback",
			message:   "federation source complete snapshot cannot move backwards",
			statement: `UPDATE federation_sources SET last_complete_snapshot_id=$3,last_sync_at=last_sync_at+interval '1 second',updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, source.ID, sourceSnapshot.ID},
		},
		{
			name:      "identity realm issuer",
			message:   "identity realm lifecycle transition is invalid",
			statement: `UPDATE tenant_identity_realms SET issuer='https://tampered.example',state='suspended',revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, provisionedA.RealmID},
		},
		{
			name:      "offboarded membership",
			message:   "tenant membership lifecycle transition is invalid",
			statement: `UPDATE tenant_memberships SET state='active',revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, offboardedMembership.Membership.ID},
		},
		{
			name:      "revoked assignment",
			message:   "role assignment lifecycle transition is invalid",
			statement: `UPDATE role_assignments SET state='active',revision=revision+1 WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, serviceAssignment.ID},
		},
		{
			name:      "observation evidence",
			message:   "original evidence are immutable",
			statement: `UPDATE observations SET raw_entry_bytes=convert_to('{}','UTF8') WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, observation.ID},
		},
		{
			name:      "maintenance time",
			message:   "source maintenance state transition is invalid",
			statement: `UPDATE source_maintenance_state SET last_admitted_at=last_admitted_at-interval '1 second' WHERE tenant_id=$1 AND source_id=$2 AND source_revision=1`,
			arguments: []any{tenantA, maintenanceSource.ID},
		},
		{
			name:      "publication activation before creation",
			message:   "active publication snapshot activation time is inconsistent",
			statement: `UPDATE publication_snapshots SET state='active',activated_at=created_at-interval '1 second',document=jsonb_set(jsonb_set(document,'{state}','"active"'::jsonb,false),'{activatedAt}',to_jsonb(created_at-interval '1 second'),false) WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, timeGuardSnapshot.ID},
		},
		{
			name:      "publication activation time",
			message:   "publication snapshot activation time cannot move backwards",
			statement: `UPDATE publication_snapshots SET state='active',activated_at=activated_at-interval '1 second',document=jsonb_set(jsonb_set(document,'{state}','"active"'::jsonb,false),'{activatedAt}',to_jsonb(activated_at-interval '1 second'),false) WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, secondSnapshot.ID},
		},
		{
			name:      "webhook endpoint revision",
			message:   "webhook endpoint lifecycle transition is invalid",
			statement: `UPDATE webhook_endpoints SET name='tampered' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, endpoint.ID},
		},
		{
			name:      "disabled webhook endpoint",
			message:   "disabled webhook endpoint is terminal",
			statement: `UPDATE webhook_endpoints SET status='active',revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, alternateWebhook.ID},
		},
		{
			name:      "terminal webhook delivery",
			message:   "webhook delivery lifecycle transition is invalid",
			statement: `UPDATE webhook_deliveries SET state='waiting',attempt=attempt+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, delivery.ID},
		},
		{
			name:      "job input",
			message:   "job identity and input are immutable",
			statement: `UPDATE jobs SET payload='{"tampered":true}'::jsonb WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, claimed.ID},
		},
		{
			name:      "job result before completion",
			message:   "job result transition is invalid",
			statement: `UPDATE jobs SET result='{}'::jsonb WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, claimed.ID},
		},
		{
			name:      "job reclaim before lease expiry",
			message:   "job lease cannot be reclaimed before expiry",
			statement: `UPDATE jobs SET attempt=attempt+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, claimed.ID},
		},
		{
			name:      "job lease expiry regression",
			message:   "job lease expiry cannot move backwards",
			statement: `UPDATE jobs SET lease_expires_at=lease_expires_at-interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, claimed.ID},
		},
		{
			name:      "published outbox payload",
			message:   "outbox event payload is immutable",
			statement: `UPDATE outbox_events SET data='{"tampered":true}'::jsonb WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{events[0].TenantID, events[0].ID},
		},
		{
			name:      "partial outbox lease",
			message:   "outbox lease owner and expiry must change together",
			statement: `UPDATE outbox_events SET lease_expires_at=NULL WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{second[0].TenantID, second[0].ID},
		},
		{
			name:      "outbox lease owner switch",
			message:   "outbox event delivery transition is invalid",
			statement: `UPDATE outbox_events SET lease_owner='different-worker' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{second[0].TenantID, second[0].ID},
		},
		{
			name:      "outbox lease expiry regression",
			message:   "outbox event delivery transition is invalid",
			statement: `UPDATE outbox_events SET lease_expires_at=lease_expires_at-interval '1 second' WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{second[0].TenantID, second[0].ID},
		},
		{
			name:      "completed idempotency response",
			message:   "idempotency key lifecycle transition is invalid",
			statement: `UPDATE idempotency_keys SET atomic_response_body='tampered' WHERE tenant_id=$1 AND actor=$2 AND route=$3 AND idempotency_key=$4`,
			arguments: []any{scope.TenantID, scope.Actor, scope.Route, scope.Key},
		},
		{
			name:      "retained namespace deletion",
			message:   "catalog retained history is immutable",
			statement: `DELETE FROM namespaces WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, namespaceA.ID},
		},
		{
			name:      "audit history deletion",
			message:   "catalog retained history is immutable",
			statement: `DELETE FROM audit_events WHERE tenant_id=$1 AND id=$2`,
			arguments: []any{tenantA, resourceAudit.ID},
		},
	} {
		t.Run("history guards/"+testCase.name, func(t *testing.T) {
			_, mutationErr := operatorPool.Exec(ctx, testCase.statement, testCase.arguments...)
			if mutationErr == nil || !strings.Contains(mutationErr.Error(), testCase.message) {
				t.Fatalf("guarded history mutation returned %v", mutationErr)
			}
		})
	}

	loadedRelease, err := catalogService.GetRelease(readerContext, localRelease.ID)
	if err != nil || loadedRelease.ID != localRelease.ID || loadedRelease.State != catalog.ReleasePublished {
		t.Fatalf("exact PostgreSQL listing-release read = %#v, %v", loadedRelease, err)
	}
	if _, err := catalogService.GetRelease(otherReaderContext, localRelease.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant PostgreSQL listing-release read returned %v", err)
	}
	sunsetAt := catalogClock.Now().Add(time.Hour + 999*time.Nanosecond)
	deprecatedRelease, err := catalogService.TransitionRelease(publisherContext, localRelease.ID, catalog.ReleaseDeprecated, "scheduled PostgreSQL retirement", externalRelease.ID, &sunsetAt, localRelease.Revision)
	if err != nil || deprecatedRelease.State != catalog.ReleaseDeprecated || deprecatedRelease.Revision != localRelease.Revision+1 || deprecatedRelease.ReplacementReleaseID != externalRelease.ID || deprecatedRelease.SunsetAt == nil || !deprecatedRelease.SunsetAt.Equal(sunsetAt.Truncate(time.Microsecond)) {
		t.Fatalf("deprecated PostgreSQL listing release = %#v, %v", deprecatedRelease, err)
	}
	if _, err := catalogService.TransitionRelease(publisherContext, localRelease.ID, catalog.ReleaseRetired, "stale transition", "", nil, localRelease.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale PostgreSQL listing-release transition returned %v", err)
	}
	catalogClock.Advance(time.Minute)
	retiredRelease, err := catalogService.TransitionRelease(publisherContext, localRelease.ID, catalog.ReleaseRetired, "completed PostgreSQL retirement", "", nil, deprecatedRelease.Revision)
	if err != nil || retiredRelease.State != catalog.ReleaseRetired || retiredRelease.Revision != deprecatedRelease.Revision+1 || retiredRelease.ReplacementReleaseID != "" || retiredRelease.SunsetAt != nil {
		t.Fatalf("retired PostgreSQL listing release = %#v, %v", retiredRelease, err)
	}
	if _, err := catalogService.TransitionRelease(publisherContext, localRelease.ID, catalog.ReleaseDeprecated, "terminal transition", "", nil, retiredRelease.Revision); catalog.ErrorCodeOf(err) != catalog.CodeInvalidTransition {
		t.Fatalf("terminal PostgreSQL listing-release transition returned %v", err)
	}
	retiredListing, err := apiRepository.GetListing(ctx, tenantA, publishedListing.ID)
	if err != nil || retiredListing.CurrentReleaseID != "" {
		t.Fatalf("retired PostgreSQL listing retained its current release = %#v, %v", retiredListing, err)
	}
	var releaseAuditCount, releaseOutboxCount int
	if err := operatorPool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND target_id=$2::text AND action=ANY($3::text[])),
       (SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND subject=$2::text AND event_type=ANY($4::text[]))`,
		tenantA, localRelease.ID,
		[]string{"listing_release.deprecated", "listing_release.retired"},
		[]string{catalog.CanonicalEventType("listing_release.deprecated"), catalog.CanonicalEventType("listing_release.retired")},
	).Scan(&releaseAuditCount, &releaseOutboxCount); err != nil || releaseAuditCount != 2 || releaseOutboxCount != 2 {
		t.Fatalf("PostgreSQL listing-release transition evidence = audit %d outbox %d, %v", releaseAuditCount, releaseOutboxCount, err)
	}

	operationalAt := catalogClock.Now().Add(time.Hour)
	operational, err := workerRepository.OperationalSnapshot(ctx, operationalAt)
	if err != nil {
		t.Fatal(err)
	}
	if !operational.CollectedAt.Equal(operationalAt.UTC()) || operational.Jobs.Queued+operational.Jobs.Running+operational.Jobs.Waiting+operational.Jobs.Succeeded+operational.Jobs.Failed+operational.Jobs.Cancelled+operational.Jobs.DeadLetter == 0 || operational.Sources.Active+operational.Sources.Paused+operational.Sources.Error+operational.Sources.Removed == 0 || operational.Sources.Complete+operational.Sources.Incomplete+operational.Sources.Failed == 0 || operational.Publications.Candidate+operational.Publications.Active+operational.Publications.Retired+operational.Publications.Failed == 0 || operational.Outbox.Pending+operational.Outbox.Acknowledged == 0 {
		t.Fatalf("PostgreSQL operational snapshot omitted retained state: %#v", operational)
	}
	for name, age := range map[string]int64{
		"job":         operational.Jobs.OldestAgeSec,
		"source":      operational.Sources.LastAgeSec,
		"publication": operational.Publications.OldestAgeSec,
		"outbox":      operational.Outbox.OldestAgeSec,
	} {
		if age < 0 {
			t.Errorf("%s operational age = %d", name, age)
		}
	}
	collectRestoreFindings := func(t *testing.T, recoveryPoint string) (map[string]int, map[string]bool) {
		t.Helper()
		options := RestoreVerificationOptions{TenantID: tenantA, RecoveryPoint: recoveryPoint, APIRole: apiRole, WorkerRole: workerRole, Limit: 100}
		codes := map[string]int{}
		records := map[string]bool{}
		for page := 1; ; page++ {
			report, verifyErr := VerifyRestoredCatalog(ctx, operatorPool, options)
			if verifyErr != nil {
				t.Fatalf("verify restored Catalog page %d: %v", page, verifyErr)
			}
			for _, finding := range report.Findings {
				codes[finding.Code]++
				if finding.RecordID != "" {
					records[finding.Code+"\x00"+finding.RecordID] = true
				}
			}
			if report.Traversal == restoreTraversalDone {
				return codes, records
			}
			options.Cursor = report.Continuation
			if page > 64 {
				t.Fatal("restore verification did not complete")
			}
		}
	}
	richRestoreFindingCodes, _ := collectRestoreFindings(t, "integration-rich-current")
	for _, expected := range []string{"RESTORE_SOURCE_INSPECTION_INVALID", "RESTORE_SOURCE_PREVIEW_INVALID", "RESTORE_PUBLICATION_SNAPSHOT_INVALID"} {
		if richRestoreFindingCodes[expected] == 0 {
			t.Fatalf("rich restore verification findings = %#v; want %s from deliberately malformed repository fixture", richRestoreFindingCodes, expected)
		}
	}
	for _, unexpected := range []string{"RESTORE_LISTING_REVISION_INVALID", "RESTORE_LISTING_REVISION_UNREADABLE", "RESTORE_VALIDATION_EVIDENCE_INVALID", "RESTORE_OBSERVATION_OCCURRENCE_INVALID"} {
		if richRestoreFindingCodes[unexpected] != 0 {
			t.Fatalf("rich restore verification unexpectedly reported %s: %#v", unexpected, richRestoreFindingCodes)
		}
	}

	t.Run("restore verifier classifies pending command reservations", func(t *testing.T) {
		startedAt := now.Add(93 * time.Hour)
		pendingReservationID, _ := generator.New(startedAt)
		pendingExecutionToken, _ := generator.New(startedAt.Add(time.Nanosecond))
		invalidReservationID, _ := generator.New(startedAt.Add(2 * time.Nanosecond))
		invalidExecutionToken, _ := generator.New(startedAt.Add(3 * time.Nanosecond))
		t.Cleanup(func() {
			if _, err := operatorPool.Exec(ctx, `ALTER TABLE idempotency_keys DISABLE TRIGGER idempotency_keys_delete_lifecycle`); err != nil {
				t.Errorf("disable idempotency cleanup guard: %v", err)
				return
			}
			_, deleteErr := operatorPool.Exec(ctx, `DELETE FROM idempotency_keys WHERE tenant_id=$1 AND reservation_id IN ($2,$3)`, tenantA, pendingReservationID, invalidReservationID)
			_, enableErr := operatorPool.Exec(ctx, `ALTER TABLE idempotency_keys ENABLE TRIGGER idempotency_keys_delete_lifecycle`)
			if deleteErr != nil {
				t.Errorf("remove protocol-v2 restore fixtures: %v", deleteErr)
			}
			if enableErr != nil {
				t.Errorf("reenable idempotency cleanup guard: %v", enableErr)
			}
		})
		for _, fixture := range []struct {
			key            string
			reservationID  string
			executionToken string
			expiresAt      time.Time
		}{
			{key: "restore-pending-v2", reservationID: pendingReservationID, executionToken: pendingExecutionToken, expiresAt: startedAt.Add(time.Hour)},
			{key: "restore-invalid-v2-time", reservationID: invalidReservationID, executionToken: invalidExecutionToken, expiresAt: startedAt.Add(-time.Second)},
		} {
			if _, err := operatorPool.Exec(ctx, `
INSERT INTO idempotency_keys(
  tenant_id,actor,route,idempotency_key,request_digest,expires_at,created_at,
  protocol_version,authorization_digest,reservation_id,execution_token,started_at
) VALUES ($1,'restore-test','POST /restore-test/v2',$2,$3,$4,$5,2,$6,$7,$8,$5)`,
				tenantA, fixture.key, "sha256:"+strings.Repeat("2", 64), fixture.expiresAt, startedAt,
				"sha256:"+strings.Repeat("3", 64), fixture.reservationID, fixture.executionToken); err != nil {
				t.Fatalf("create %s fixture: %v", fixture.key, err)
			}
		}
		_, records := collectRestoreFindings(t, "integration-pending-idempotency")
		if !records["RESTORE_PENDING_IDEMPOTENCY_V2\x00"+pendingReservationID] {
			t.Fatalf("restore verification omitted pending protocol-v2 reservation %s", pendingReservationID)
		}
		if !records["RESTORE_IDEMPOTENCY_TIME_INVALID\x00"+invalidReservationID] {
			t.Fatalf("restore verification omitted invalid protocol-v2 timing %s", invalidReservationID)
		}
	})

	t.Run("restore verifier detects offboarded membership grants", func(t *testing.T) {
		membershipID, _ := generator.New(now.Add(90 * time.Hour))
		assignmentID, _ := generator.New(now.Add(90*time.Hour + time.Nanosecond))
		if _, err := operatorPool.Exec(ctx, `INSERT INTO tenant_memberships(id,tenant_id,realm_id,subject,actor_type,state,revision,created_at,updated_at) VALUES ($1,$2,$3,'restore-invalid-member','human','active',1,$4,$4)`, membershipID, tenantA, provisionedA.RealmID, now); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE role_assignments DISABLE TRIGGER role_assignments_retained_row`)
			_, _ = operatorPool.Exec(ctx, `DELETE FROM role_assignments WHERE tenant_id=$1 AND id=$2`, tenantA, assignmentID)
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE role_assignments ENABLE TRIGGER role_assignments_retained_row`)
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE tenant_memberships DISABLE TRIGGER tenant_memberships_retained_row`)
			_, _ = operatorPool.Exec(ctx, `DELETE FROM tenant_memberships WHERE tenant_id=$1 AND id=$2`, tenantA, membershipID)
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE tenant_memberships ENABLE TRIGGER tenant_memberships_retained_row`)
		}()
		if _, err := operatorPool.Exec(ctx, `INSERT INTO role_assignments(id,tenant_id,membership_id,role,state,revision,granted_by,grant_reason,granted_at) VALUES ($1,$2,$3,'reader','active',1,'restore-test','detect an invalid active grant',$4)`, assignmentID, tenantA, membershipID, now); err != nil {
			t.Fatal(err)
		}
		// Construct an incomplete owner-side offboarding after a legal grant.
		// New grants to already offboarded members are now rejected by SQL.
		if _, err := operatorPool.Exec(ctx, `UPDATE tenant_memberships SET state='offboarded',revision=revision+1,updated_at=$3 WHERE tenant_id=$1 AND id=$2`, tenantA, membershipID, now); err != nil {
			t.Fatal(err)
		}

		_, records := collectRestoreFindings(t, "integration-invalid-membership")
		if !records["RESTORE_OFFBOARDED_MEMBERSHIP_HAS_ACTIVE_GRANTS\x00"+membershipID] {
			t.Fatalf("restore verification omitted invalid membership %s", membershipID)
		}
	})

	t.Run("restore verifier detects validation evidence tampering", func(t *testing.T) {
		var original []byte
		if err := operatorPool.QueryRow(ctx, `SELECT findings FROM evidence_sets WHERE tenant_id=$1 AND id=$2`, tenantA, replacementExternalEvidence.ID).Scan(&original); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `ALTER TABLE evidence_sets DISABLE TRIGGER evidence_sets_immutable_history`); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `UPDATE evidence_sets SET findings=jsonb_build_array(jsonb_build_object('code',repeat('x',2097153))) WHERE tenant_id=$1 AND id=$2`, tenantA, replacementExternalEvidence.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `ALTER TABLE evidence_sets ENABLE TRIGGER evidence_sets_immutable_history`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE evidence_sets DISABLE TRIGGER evidence_sets_immutable_history`)
			_, _ = operatorPool.Exec(ctx, `UPDATE evidence_sets SET findings=$1::jsonb WHERE tenant_id=$2 AND id=$3`, original, tenantA, replacementExternalEvidence.ID)
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE evidence_sets ENABLE TRIGGER evidence_sets_immutable_history`)
		}()
		_, records := collectRestoreFindings(t, "integration-invalid-evidence")
		if !records["RESTORE_VALIDATION_EVIDENCE_INVALID\x00"+replacementExternalEvidence.ID] {
			t.Fatalf("restore verification omitted invalid evidence %s", replacementExternalEvidence.ID)
		}
		if !records["RESTORE_VERSION_REVISION_BOUNDS_INVALID\x00"+replacementExternalRevision.ID] {
			t.Fatalf("restore verification decoded oversized revision evidence for %s", replacementExternalRevision.ID)
		}
	})

	t.Run("restore verifier detects malformed actionable work", func(t *testing.T) {
		jobID, _ := generator.New(now.Add(91 * time.Hour))
		if _, err := operatorPool.Exec(ctx, `INSERT INTO jobs(id,tenant_id,kind,actor,payload_version,payload,state,priority,progress,not_before,attempt,attempt_limit,cancellation_requested,deduplication_key,created_at,updated_at) VALUES ($1,$2,'restore.unsupported','restore-test',1,'{}','queued',0,0,$3,0,5,false,$4,$3,$3)`, jobID, tenantA, now, "restore-invalid:"+jobID); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE jobs DISABLE TRIGGER jobs_retained_row`)
			_, _ = operatorPool.Exec(ctx, `DELETE FROM jobs WHERE tenant_id=$1 AND id=$2`, tenantA, jobID)
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE jobs ENABLE TRIGGER jobs_retained_row`)
		}()
		_, records := collectRestoreFindings(t, "integration-invalid-job")
		if !records["RESTORE_ACTIONABLE_JOB_PAYLOAD_INVALID\x00"+jobID] {
			t.Fatalf("restore verification omitted invalid actionable Job %s", jobID)
		}
	})

	t.Run("restore verifier detects occurrence provenance tampering", func(t *testing.T) {
		var observationID, snapshotID, retrievalDigest, entryLocation string
		var originalObservedAt time.Time
		if err := operatorPool.QueryRow(ctx, `SELECT observation_id::text,snapshot_id::text,retrieval_digest,entry_location,observed_at FROM observation_occurrences WHERE tenant_id=$1 AND evidence_complete AND source_revision IS NOT NULL ORDER BY observed_at,snapshot_id,retrieval_digest,entry_location LIMIT 1`, tenantA).Scan(&observationID, &snapshotID, &retrievalDigest, &entryLocation, &originalObservedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `ALTER TABLE observation_occurrences DISABLE TRIGGER observation_occurrences_immutable`); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `UPDATE observation_occurrences occurrence SET observed_at=(SELECT started_at - interval '1 microsecond' FROM source_snapshots WHERE tenant_id=occurrence.tenant_id AND id=occurrence.snapshot_id) WHERE occurrence.tenant_id=$1 AND occurrence.snapshot_id=$2 AND occurrence.retrieval_digest=$3 AND occurrence.entry_location=$4`, tenantA, snapshotID, retrievalDigest, entryLocation); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `ALTER TABLE observation_occurrences ENABLE TRIGGER observation_occurrences_immutable`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE observation_occurrences DISABLE TRIGGER observation_occurrences_immutable`)
			_, _ = operatorPool.Exec(ctx, `UPDATE observation_occurrences SET observed_at=$1 WHERE tenant_id=$2 AND snapshot_id=$3 AND retrieval_digest=$4 AND entry_location=$5`, originalObservedAt, tenantA, snapshotID, retrievalDigest, entryLocation)
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE observation_occurrences ENABLE TRIGGER observation_occurrences_immutable`)
		}()
		_, records := collectRestoreFindings(t, "integration-invalid-occurrence")
		if !records["RESTORE_OBSERVATION_OCCURRENCE_INVALID\x00"+observationID] {
			t.Fatalf("restore verification omitted invalid occurrence for observation %s", observationID)
		}
	})

	t.Run("restore verifier detects retrieval attempt tampering", func(t *testing.T) {
		var attemptID, snapshotID, originalProfile string
		if err := operatorPool.QueryRow(ctx, `SELECT id::text,snapshot_id::text,profile_id FROM source_retrieval_attempts WHERE tenant_id=$1 ORDER BY fetched_at,id LIMIT 1`, tenantA).Scan(&attemptID, &snapshotID, &originalProfile); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `ALTER TABLE source_retrieval_attempts DISABLE TRIGGER source_retrieval_attempts_immutable`); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `UPDATE source_retrieval_attempts SET profile_id='restore-tampered' WHERE tenant_id=$1 AND id=$2`, tenantA, attemptID); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `ALTER TABLE source_retrieval_attempts ENABLE TRIGGER source_retrieval_attempts_immutable`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE source_retrieval_attempts DISABLE TRIGGER source_retrieval_attempts_immutable`)
			_, _ = operatorPool.Exec(ctx, `UPDATE source_retrieval_attempts SET profile_id=$1 WHERE tenant_id=$2 AND id=$3`, originalProfile, tenantA, attemptID)
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE source_retrieval_attempts ENABLE TRIGGER source_retrieval_attempts_immutable`)
		}()
		_, records := collectRestoreFindings(t, "integration-invalid-retrieval-attempt")
		if !records["RESTORE_SOURCE_SNAPSHOT_INVALID\x00"+snapshotID] {
			t.Fatalf("restore verification omitted invalid retrieval attempt %s for snapshot %s", attemptID, snapshotID)
		}
	})

	t.Run("restore verifier detects conditional retrieval state tampering", func(t *testing.T) {
		var sourceID, requestKey, originalPolicy string
		var sourceRevision int64
		if err := operatorPool.QueryRow(ctx, `SELECT source_id::text,source_revision,request_key,policy_digest FROM source_retrieval_states WHERE tenant_id=$1 ORDER BY source_id,source_revision,request_key LIMIT 1`, tenantA).Scan(&sourceID, &sourceRevision, &requestKey, &originalPolicy); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `ALTER TABLE source_retrieval_states DISABLE TRIGGER source_retrieval_states_lifecycle`); err != nil {
			t.Fatal(err)
		}
		tamperedPolicy := "sha256:" + strings.Repeat("f", 64)
		if tamperedPolicy == originalPolicy {
			tamperedPolicy = "sha256:" + strings.Repeat("e", 64)
		}
		if _, err := operatorPool.Exec(ctx, `UPDATE source_retrieval_states SET policy_digest=$1 WHERE tenant_id=$2 AND source_id=$3 AND source_revision=$4 AND request_key=$5`, tamperedPolicy, tenantA, sourceID, sourceRevision, requestKey); err != nil {
			t.Fatal(err)
		}
		if _, err := operatorPool.Exec(ctx, `ALTER TABLE source_retrieval_states ENABLE TRIGGER source_retrieval_states_lifecycle`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE source_retrieval_states DISABLE TRIGGER source_retrieval_states_lifecycle`)
			_, _ = operatorPool.Exec(ctx, `UPDATE source_retrieval_states SET policy_digest=$1 WHERE tenant_id=$2 AND source_id=$3 AND source_revision=$4 AND request_key=$5`, originalPolicy, tenantA, sourceID, sourceRevision, requestKey)
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE source_retrieval_states ENABLE TRIGGER source_retrieval_states_lifecycle`)
		}()
		_, records := collectRestoreFindings(t, "integration-invalid-retrieval-state")
		if !records["RESTORE_SOURCE_RETRIEVAL_STATE_INVALID\x00"+sourceID] {
			t.Fatalf("restore verification omitted invalid conditional state for source %s", sourceID)
		}
	})

	t.Run("restore verifier detects delivery binding tampering", func(t *testing.T) {
		deliveryID, _ := generator.New(now.Add(92 * time.Hour))
		missingEventID, _ := generator.New(now.Add(92*time.Hour + time.Nanosecond))
		if _, err := operatorPool.Exec(ctx, `INSERT INTO webhook_deliveries(id,tenant_id,endpoint_id,event_id,event_type,state,attempt,safe_error,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,'pending',0,'',$6,$6)`, deliveryID, tenantA, endpoint.ID, missingEventID, catalogevents.CanonicalType("resource.created"), now); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE webhook_deliveries DISABLE TRIGGER webhook_deliveries_delete_lifecycle`)
			_, _ = operatorPool.Exec(ctx, `DELETE FROM webhook_deliveries WHERE tenant_id=$1 AND id=$2`, tenantA, deliveryID)
			_, _ = operatorPool.Exec(ctx, `ALTER TABLE webhook_deliveries ENABLE TRIGGER webhook_deliveries_delete_lifecycle`)
		}()
		_, records := collectRestoreFindings(t, "integration-invalid-delivery")
		if !records["RESTORE_WEBHOOK_DELIVERY_BINDING_INVALID\x00"+deliveryID] {
			t.Fatalf("restore verification omitted invalid delivery %s", deliveryID)
		}
	})

	cancelledContext, cancelOperational := context.WithCancel(ctx)
	cancelOperational()
	if _, err := workerRepository.OperationalSnapshot(cancelledContext, operationalAt); err == nil {
		t.Fatal("operational snapshot ignored cancellation")
	}
}

func postgresSkillPackageZIP(tb testing.TB) []byte {
	tb.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	files := []struct {
		name    string
		content string
	}{
		{name: "postgres-skill/SKILL.md", content: "---\nname: postgres-skill\ndescription: Proves transactional Skill package inventory.\n---\n# Instructions\n"},
		{name: "postgres-skill/scripts/run.sh", content: "#!/bin/sh\nexit 99\n"},
	}
	for _, entry := range files {
		file, err := writer.Create(entry.name)
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := file.Write([]byte(entry.content)); err != nil {
			tb.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		tb.Fatal(err)
	}
	return buffer.Bytes()
}

func withSearchPath(tb testing.TB, dsn, searchPath string) string {
	tb.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		tb.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", searchPath)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func rolePool(tb testing.TB, dsn, role, password string) *pgxpool.Pool {
	tb.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		tb.Fatal(err)
	}
	parsed.User = url.UserPassword(role, password)
	pool, err := Open(context.Background(), parsed.String(), testPoolConfig(4))
	if err != nil {
		tb.Fatal(err)
	}
	return pool
}

func assumedRolePool(t *testing.T, dsn, role string) *pgxpool.Pool {
	t.Helper()
	configuration, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	configuration.MaxConns = 4
	configuration.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		_, err := connection.Exec(ctx, `SET ROLE `+pgx.Identifier{role}.Sanitize())
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool
}
