//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
)

// retainProcessAuditRange selects one known immutable event before the product
// listeners stop. The later operator checks compare this range, not a claimed
// backup provenance or a complete audit history.
func retainProcessAuditRange(ctx context.Context, t *testing.T, f processFixture, owner *pgx.Conn, address, tenant, namespace string) string {
	t.Helper()
	var eventID string
	var at time.Time
	if err := owner.QueryRow(ctx, `SELECT id::text,occurred_at FROM audit_events WHERE tenant_id=$1 AND action='namespace.created' AND target_type='namespace' AND target_id=$2`, tenant, namespace).Scan(&eventID, &at); err != nil {
		t.Fatal("read known namespace audit range")
	}
	input, err := json.Marshal(map[string]string{
		"occurredFrom": at.Format(time.RFC3339Nano), "occurredBefore": at.Add(time.Microsecond).Format(time.RFC3339Nano),
		"action": "namespace.created", "targetType": "namespace", "targetId": namespace, "reason": "Retain the owned operator verification range",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := f.request(ctx, address, "POST", "/api/v1/audit-exports", "local-admin", string(input), 200)
	export, _, err := audit.VerifyExport(raw)
	if err != nil || export.Manifest.TenantID != tenant || export.Manifest.RowCount != 1 {
		t.Fatal("owned audit export did not retain the exact namespace range", err)
	}
	var events []audit.Record
	decodeProcessJSON(t, export.RawEvents, &events)
	if len(events) != 1 || events[0].ID != eventID || events[0].Action != "namespace.created" || events[0].TargetID != namespace {
		t.Fatal("owned audit export changed its selected immutable event")
	}
	path := filepath.Join(f.directory, "operator-audit-export.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// qualifyProcessOperators reuses the stopped process fixture and its direct
// schema-owner connection. After provisioning an empty control tenant, every
// command is read-only or must have no work; completed reservations stay sealed.
func qualifyProcessOperators(ctx context.Context, t *testing.T, f processFixture, dsn, configuration, tenant, otherTenant, exportPath string) {
	t.Helper()
	const emptyTenant = "00000000-0000-7000-8000-000000009691"
	if tenant == otherTenant || otherTenant == "" || tenant == emptyTenant || otherTenant == emptyTenant {
		t.Fatal("operator controls require distinct provisioned and new tenants")
	}
	// Both pooled writer-loss modes have promoted the standby before this hook.
	// Its schema owner has a distinct direct listener; the original DSN still
	// names the fenced predecessor and must not be reused or sent to PgBouncer.
	if os.Getenv("CATALOG_TEST_PGBOUNCER_DSN") != "" {
		dsn = os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN")
		address, err := url.Parse(dsn)
		if err != nil || address.Scheme != "postgres" || address.Hostname() != "127.0.0.1" || address.Path != "/catalog_test" || os.Getenv("CATALOG_TEST_PGBOUNCER_PROJECT") == "" {
			t.Fatal("offline pooled operators require the owned direct promoted-owner endpoint")
		}
	}
	owner, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect to the owned operator database")
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = owner.Close(closeCtx)
	}()
	var ownsSchema, recovery, readOnly bool
	if err := owner.QueryRow(ctx, `SELECT current_user=pg_get_userbyid(class.relowner),pg_is_in_recovery(),current_setting('transaction_read_only')::boolean
 FROM pg_catalog.pg_class class WHERE class.oid=to_regclass('observations')`).Scan(&ownsSchema, &recovery, &readOnly); err != nil || !ownsSchema || recovery || readOnly {
		t.Fatal("operator control did not reach the direct schema owner on the writable server")
	}
	run := func(output any, args ...string) []byte {
		t.Helper()
		raw := f.run(ctx, f.command(ctx, "catalogctl", dsn, append(args, "--config", configuration)...), 0)
		decodeProcessJSON(t, raw, output)
		return raw
	}
	reject := func(stage string, args ...string) {
		t.Helper()
		f.run(ctx, f.command(ctx, "catalogctl", dsn, append(args, "--config", configuration)...), 1, "catalogctl: "+stage+": INTERNAL:")
	}
	// The existing peer tenant has source-maintenance history. Provision the
	// no-discovery control through the owning command, before taking the
	// unchanged-state snapshot; never fake or clear retained database records.
	var exists bool
	if err := owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1)`, emptyTenant).Scan(&exists); err != nil || exists {
		t.Fatal("empty operator tenant is not invocation-owned new state")
	}
	var provisioned database.IdentityProvisioningResult
	run(&provisioned, "identity", "provision", "--tenant", emptyTenant, "--tenant-name", "Empty operator tenant", "--issuer", "https://operator-process.example", "--subject", "empty-operator-admin", "--roles", "tenant_admin", "--operator", "native-process-test", "--reason", "Provision an owned empty repair control")
	if provisioned.TenantID != emptyTenant || provisioned.Subject != "empty-operator-admin" {
		t.Fatal("native provisioning did not create the exact empty operator control")
	}
	// With all product writers stopped, these counts also detect unexpected
	// evidence, coordination or generation writes by a supposedly read-only path.
	type operatorState struct {
		counts      [7]int64
		generations string
	}
	state := func() operatorState {
		t.Helper()
		var value operatorState
		if err := owner.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM audit_events),(SELECT count(*) FROM outbox_events),
 (SELECT count(*) FROM idempotency_keys),(SELECT count(*) FROM jobs),
 (SELECT count(*) FROM listing_releases),(SELECT count(*) FROM publication_snapshots),
 (SELECT count(*) FROM catalog_search_generations),
 (SELECT coalesce(jsonb_agg(jsonb_build_array(tenant_id,generation) ORDER BY tenant_id),'[]')::text FROM catalog_search_generations)`).Scan(&value.counts[0], &value.counts[1], &value.counts[2], &value.counts[3], &value.counts[4], &value.counts[5], &value.counts[6], &value.generations); err != nil {
			t.Fatal("read quiesced operator state")
		}
		return value
	}
	before := state()

	const recoveryPoint = "owned-process-operator-checkpoint"
	const pageLimit = 10
	var schemaVersion int64
	if err := owner.QueryRow(ctx, `SELECT max(version) FROM catalog_schema_migrations`).Scan(&schemaVersion); err != nil {
		t.Fatal("read operator schema version")
	}
	cursor := ""
	for page := 0; ; page++ {
		if page == 200 {
			t.Fatal("bounded native restore verification did not terminate")
		}
		args := []string{"restore", "verify", "--tenant", tenant, "--recovery-point", recoveryPoint, "--limit", "10"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		var report database.RestoreVerificationReport
		raw := run(&report, args...)
		if len(raw) > database.MaxRestoreVerificationOutputBytes || report.FormatVersion != database.RestoreVerificationFormatVersion || report.CheckSetVersion != database.RestoreVerificationCheckSetVersion || report.TenantID != tenant || report.RecoveryPoint != recoveryPoint || report.SchemaVersion != schemaVersion || report.PrivilegeManifestVersion < 1 || report.Integrity != "valid" || report.AssuranceBoundary == "" || report.Processed < 0 || report.Processed > pageLimit {
			// Report comparisons and verifier codes only, never retained rows,
			// private identifiers, cursor contents or command diagnostics.
			codes := make([]string, 0, len(report.Findings))
			for _, finding := range report.Findings {
				code := finding.Code
				if !strings.HasPrefix(code, "RESTORE_") || len(code) > 100 || strings.IndexFunc(code, func(r rune) bool { return r != '_' && (r < 'A' || r > 'Z') && (r < '0' || r > '9') }) >= 0 {
					code = "UNRECOGNIZED_FINDING"
				}
				codes = append(codes, code)
			}
			t.Fatalf("native restore verification lost its binding, bounds or integrity conclusion: page=%d bytes=%d format=%d checkSet=%d tenantMatches=%t recoveryPointMatches=%t schemaMatches=%t manifest=%d integrityValid=%t assurancePresent=%t processed=%d codes=%v", page, len(raw), report.FormatVersion, report.CheckSetVersion, report.TenantID == tenant, report.RecoveryPoint == recoveryPoint, report.SchemaVersion == schemaVersion, report.PrivilegeManifestVersion, report.Integrity == "valid", report.AssuranceBoundary != "", report.Processed, codes)
		}
		for _, finding := range report.Findings {
			if finding.Class == "integrity" {
				t.Fatal("valid process fixture produced an integrity finding", finding.Code)
			}
		}
		if report.Traversal == "complete" {
			if page == 0 || report.Continuation != "" {
				t.Fatal("native restore verification did not preserve gap-free page completion")
			}
			break
		}
		if report.Traversal != "continued" || report.Continuation == "" || report.Continuation == cursor {
			t.Fatal("native restore verification did not advance its bounded continuation")
		}
		if page == 0 {
			// The original cursor must work below; changing only its recovery
			// label must fail without changing any retained state.
			reject("restore", "restore", "verify", "--tenant", tenant, "--recovery-point", "another-owned-point", "--limit", "10", "--cursor", report.Continuation)
		}
		cursor = report.Continuation
	}

	// The compiled binder is offline; the comparer must read the named owner
	// database and reject a structurally valid but unequal trusted digest.
	manifestRaw := f.run(ctx, f.command(ctx, "catalogctl", "", "audit", "bind-manifest", "--file", exportPath, "--recovery-point", recoveryPoint), 0)
	var trusted audit.TrustedManifest
	decodeProcessJSON(t, manifestRaw, &trusted)
	if trusted.RecoveryPointID != recoveryPoint || trusted.Manifest.TenantID != tenant || trusted.Manifest.RowCount != 1 {
		t.Fatal("native manifest binding changed its selected range")
	}
	manifestPath := filepath.Join(f.directory, "operator-audit-manifest.json")
	if err := os.WriteFile(manifestPath, manifestRaw, 0600); err != nil {
		t.Fatal(err)
	}
	compare := func() {
		t.Helper()
		var report audit.VerificationReport
		run(&report, "audit", "compare-restore", "--file", manifestPath, "--recovery-point", recoveryPoint)
		if !report.Valid || report.Mode != "restore" || report.RowCount != 1 || report.EventsDigest != trusted.Manifest.EventsDigest || report.RecoveryPointID != recoveryPoint || report.AssuranceBoundary == "" {
			t.Fatal("native audit comparison did not preserve the trusted exact-range result")
		}
	}
	compare()
	wrong := trusted
	wrong.Manifest.EventsDigest = "sha256:" + strings.Repeat("0", 64)
	wrongRaw, err := json.Marshal(wrong)
	if err != nil {
		t.Fatal(err)
	}
	wrongPath := filepath.Join(f.directory, "operator-wrong-manifest.json")
	if err := os.WriteFile(wrongPath, wrongRaw, 0600); err != nil {
		t.Fatal(err)
	}
	reject("audit", "audit", "compare-restore", "--file", wrongPath, "--recovery-point", recoveryPoint)
	compare()

	// A completed command is a valid plan target but never a valid release
	// target. Capture the exact retained row so a refusal cannot hide a write.
	var reservation, actor, route, requestDigest, key, retained string
	if err := owner.QueryRow(ctx, `SELECT reservation_id::text,actor,route,request_digest,idempotency_key,to_jsonb(keys)::text
 FROM idempotency_keys keys WHERE tenant_id=$1 AND route='POST /api/v1/namespaces'
 AND protocol_version=2 AND atomic_response_status=201 ORDER BY reservation_id LIMIT 1`, tenant).Scan(&reservation, &actor, &route, &requestDigest, &key, &retained); err != nil {
		t.Fatal("select known completed command")
	}
	var pendingCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE tenant_id=$1 AND protocol_version=2 AND atomic_response_status IS NULL AND released_at IS NULL`, tenant).Scan(&pendingCount); err != nil || pendingCount > 100 {
		t.Fatal("pending native command inventory exceeded its fixture bound")
	}
	t.Logf("native repair inventory pending count: %d", pendingCount)
	var inventory database.PendingIdempotencyReport
	run(&inventory, "repair", "idempotency", "list", "--tenant", tenant, "--limit", "100")
	if inventory.TenantID != tenant || inventory.Count != pendingCount || len(inventory.Items) != pendingCount {
		t.Fatal("native repair inventory lost its tenant or finite count")
	}
	for _, item := range inventory.Items {
		if item.TenantID != tenant || item.ProtocolVersion != 2 || item.ReservationID == reservation {
			t.Fatal("native pending inventory included a foreign or completed command")
		}
	}
	const reason = "Preserve the completed native command"
	args := []string{"repair", "idempotency", "plan", "--tenant", tenant, "--reservation", reservation, "--reason", reason}
	var plan database.IdempotencyRepairPlan
	planRaw := run(&plan, args...)
	keyHash := sha256.Sum256([]byte(key))
	planHash, hashErr := hex.DecodeString(strings.TrimPrefix(plan.PlanDigest, "sha256:"))
	if plan.Version != "catalog.idempotency-repair/v2" || plan.Action != "release_interrupted_reservation" || plan.TenantID != tenant || plan.ReservationID != reservation || plan.Actor != actor || plan.Route != route || plan.RequestDigest != requestDigest || plan.ProtocolVersion != 2 || plan.State != "completed" || plan.Active || plan.Eligible || plan.Refusal == "" || plan.Reason != reason || plan.IdempotencyKeyDigest != "sha256:"+hex.EncodeToString(keyHash[:]) || hashErr != nil || len(planHash) != sha256.Size || plan.PlanDigest != "sha256:"+hex.EncodeToString(planHash) || bytes.Contains(planRaw, []byte(key)) {
		t.Fatal("native repair plan did not preserve completed state or key privacy")
	}
	reject("repair", "repair", "idempotency", "plan", "--tenant", otherTenant, "--reservation", reservation, "--reason", reason)
	reject("repair", "repair", "idempotency", "apply", "--tenant", tenant, "--reservation", reservation, "--reason", reason, "--plan-digest", plan.PlanDigest)
	var afterPlan database.IdempotencyRepairPlan
	run(&afterPlan, args...)
	var afterRow string
	if err := owner.QueryRow(ctx, `SELECT to_jsonb(keys)::text FROM idempotency_keys keys WHERE tenant_id=$1 AND reservation_id=$2`, tenant, reservation).Scan(&afterRow); err != nil || afterRow != retained || !reflect.DeepEqual(plan, afterPlan) {
		t.Fatal("refused native repair changed a completed command or its plan")
	}

	// The search-repair runbook promises that an empty tenant creates neither
	// a generation row nor audit evidence. Check the actual SQL state too.
	var observations, generationRows int
	if err := owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM observations WHERE tenant_id=$1),(SELECT count(*) FROM catalog_search_generations WHERE tenant_id=$1)`, emptyTenant).Scan(&observations, &generationRows); err != nil || observations != 0 || generationRows != 0 {
		t.Fatal("search repair no-work control is not an empty tenant")
	}
	var repaired database.CatalogSearchProjectionRepairResult
	run(&repaired, "repair", "search-projections", "--tenant", emptyTenant, "--reason", "Verify an owned empty repair", "--operator-label", "native-process-test", "--limit", "1")
	if repaired.TenantID != emptyTenant || repaired.Processed != 0 || repaired.HasMore || !repaired.Complete || repaired.BeforeGeneration != 0 || repaired.AfterGeneration != 0 || repaired.AuditEventID != "" {
		t.Fatal("native empty search repair manufactured work or evidence")
	}
	if after := state(); after != before {
		t.Fatal("read-only native operator checks changed retained coordination or publication state")
	}
	t.Log("native restore pages, exact audit comparison, completed-command repair refusal and empty search repair passed without retained-state changes")
}
