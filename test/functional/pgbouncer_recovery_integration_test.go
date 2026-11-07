//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
)

func preparePooledCommandAuthor(ctx context.Context, t *testing.T, f processFixture, address string) string {
	t.Helper()
	var member struct {
		MembershipID string `json:"membershipId"`
	}
	decodeProcessJSON(t, f.request(ctx, address, "GET", "/api/v1/me", "local-reader", "", 200), &member)
	var role, grant struct {
		ID string `json:"id"`
	}
	decodeProcessJSON(t, f.request(ctx, address, "POST", "/api/v1/access/roles", "local-admin", `{"name":"Pooled recovery namespace author","permissions":["namespace.create"],"reason":"Owned recovery command authority"}`, 201, map[string]string{"Idempotency-Key": "pooled-recovery-role"}), &role)
	decodeProcessJSON(t, f.request(ctx, address, "POST", "/api/v1/access/assignments", "local-admin", `{"roleDefinitionId":"`+role.ID+`","membershipId":"`+member.MembershipID+`","reason":"Owned recovery author grant"}`, 201, map[string]string{"Idempotency-Key": "pooled-recovery-grant"}), &grant)
	return grant.ID
}

func pooledRepairPlan(ctx context.Context, t *testing.T, f processFixture, dsn string, cfg config.Config, reservation string) database.IdempotencyRepairPlan {
	t.Helper()
	var plan database.IdempotencyRepairPlan
	decodeProcessJSON(t, f.run(ctx, f.command(ctx, "catalogctl", dsn, "repair", "idempotency", "plan", "--config", f.configuration("recovery-owner", cfg), "--tenant", f.tenantID, "--reservation", reservation, "--reason", "Original execution ended after fenced writer loss"), 0), &plan)
	if plan.TenantID != f.tenantID || plan.ReservationID != reservation || plan.PlanDigest == "" {
		t.Fatal("repair plan changed its exact scope")
	}
	return plan
}

func pooledRepairApply(ctx context.Context, t *testing.T, f processFixture, dsn string, cfg config.Config, plan database.IdempotencyRepairPlan, want int) database.IdempotencyRepairResult {
	t.Helper()
	raw := f.run(ctx, f.command(ctx, "catalogctl", dsn, "repair", "idempotency", "apply", "--config", f.configuration("recovery-owner", cfg), "--tenant", plan.TenantID, "--reservation", plan.ReservationID, "--reason", plan.Reason, "--plan-digest", plan.PlanDigest), want)
	var result database.IdempotencyRepairResult
	if want == 0 {
		decodeProcessJSON(t, raw, &result)
		if !result.Applied || result.Plan.ReservationID != plan.ReservationID {
			t.Fatal("repair did not release the exact reservation")
		}
	}
	return result
}

// The writer stays private until this owner transaction blocks snapshot writes.
// No runtime lease is changed and no replacement API/worker is started.
func promoteWithRetryBarrier(ctx context.Context, t *testing.T, command *exec.Cmd, firstWorker *os.Process, tenant, jobID, oldOwner string) func() {
	t.Helper()
	var diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &diagnostics, &diagnostics
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		t.Fatal("start owned promotion")
	}
	finished := make(chan struct{})
	var runErr error
	go func() { runErr = command.Wait(); close(finished) }()
	t.Cleanup(func() {
		select {
		case <-finished:
			return
		default:
		}
		_ = command.Process.Kill()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("promotion helper did not stop")
		}
	})
	marker := os.Getenv("CATALOG_TEST_PGBOUNCER_WRITER_FILE")
	deadline := time.Now().Add(60 * time.Second)
	for {
		raw, err := os.ReadFile(marker + ".promoted") // #nosec G304,G703 -- private checkpoint path supplied by the owned runner.
		if err == nil && string(raw) == "promoted" {
			break
		}
		select {
		case <-finished:
			t.Fatalf("promotion stopped before retry barrier: %v %s", runErr, &diagnostics)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("promotion checkpoint timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	owner, err := pgx.Connect(ctx, os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"))
	if err != nil {
		t.Fatal("connect direct promoted owner")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = owner.Close(cleanup)
	})

	// A Unix-socket owner session survives replacement of the standby's Docker
	// network endpoint. Its stdin stays open until the stale-owner probe ends.
	barrier := exec.CommandContext(ctx, "docker", "compose", "--env-file", "/dev/null", "-f", os.Getenv("CATALOG_TEST_PGBOUNCER_COMPOSE"), "-p", os.Getenv("CATALOG_TEST_PGBOUNCER_PROJECT"), "exec", "-T", "standby", "psql", "-XqAt", "-v", "ON_ERROR_STOP=1", "-U", "catalog", "-d", "catalog_test") // #nosec G204,G702 -- exact invocation-owned Compose project and fixed standby command.
	input, err := barrier.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var barrierDiagnostics processEvidenceWriter
	barrier.Stdout, barrier.Stderr = &barrierDiagnostics, &barrierDiagnostics
	barrier.WaitDelay = time.Second
	if err := barrier.Start(); err != nil {
		t.Fatal("start owned retry barrier")
	}
	barrierDone := make(chan struct{})
	var barrierErr error
	go func() { barrierErr = barrier.Wait(); close(barrierDone) }()
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, _ = io.WriteString(input, "ROLLBACK;\n\\q\n")
			_ = input.Close()
			select {
			case <-barrierDone:
				if barrierErr != nil {
					t.Error("owned retry barrier failed")
				}
			case <-time.After(10 * time.Second):
				_ = barrier.Process.Kill()
				t.Error("retry barrier did not stop")
				select {
				case <-barrierDone:
				case <-time.After(3 * time.Second):
					t.Error("retry barrier process did not join")
				}
			}
		})
	}
	t.Cleanup(release)
	if _, err := io.WriteString(input, "BEGIN; SET LOCAL application_name='pooled-retry-barrier'; LOCK TABLE source_snapshots IN SHARE ROW EXCLUSIVE MODE;\n"); err != nil {
		t.Fatal("write owned retry barrier")
	}
	held := false
	for until := time.Now().Add(10 * time.Second); time.Now().Before(until); {
		if err := owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE a.application_name='pooled-retry-barrier' AND l.granted AND l.mode='ShareRowExclusiveLock' AND l.relation='source_snapshots'::regclass)`).Scan(&held); err != nil {
			t.Fatal("observe held retry barrier")
		}
		if held {
			break
		}
		select {
		case <-barrierDone:
			t.Fatal("retry barrier exited before holding its lock")
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !held {
		t.Fatal("promoted retry barrier did not acquire its lock")
	}
	if err := owner.Close(ctx); err != nil {
		t.Fatal("close pre-route observer")
	}

	paused := false
	defer func() {
		if paused {
			_ = firstWorker.Signal(syscall.SIGCONT)
		}
	}()
	if err := firstWorker.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal("pause original lease owner")
	}
	paused = true
	pauseStarted := time.Now()
	if err := os.WriteFile(marker+".route-ready", []byte("ready"), 0600); err != nil { // #nosec G703 -- private checkpoint path supplied by the owned runner.
		t.Fatal(err)
	}
	routedDeadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(marker + ".routed") // #nosec G304,G703 -- invocation-owned private route checkpoint.
		if err == nil && string(raw) == "routed" {
			break
		}
		select {
		case <-finished:
			t.Fatal("promotion exited before routing")
		default:
		}
		if time.Now().After(routedDeadline) {
			t.Fatal("writer routing checkpoint timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	observer, err := pgx.Connect(ctx, os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"))
	if err != nil {
		t.Fatal("observe second-worker takeover")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = observer.Close(cleanup)
	}()
	takenOver := false
	for until := time.Now().Add(75 * time.Second); time.Now().Before(until); {
		if err := observer.QueryRow(ctx, `SELECT state='running' AND attempt>=2 AND lease_owner<>$3 AND lease_expires_at>clock_timestamp() FROM jobs WHERE tenant_id=$1 AND id=$2`, tenant, jobID, oldOwner).Scan(&takenOver); err != nil {
			t.Fatal("read exact second-worker lease")
		}
		if takenOver {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !takenOver {
		t.Fatal("second original worker did not take over the Job")
	}
	if err := firstWorker.Signal(syscall.SIGCONT); err != nil {
		t.Fatal("resume original lease owner")
	}
	paused = false
	if err := os.WriteFile(marker+".resumed", []byte("resumed"), 0600); err != nil { // #nosec G703 -- invocation-owned private resume checkpoint.
		t.Fatal(err)
	}
	t.Logf("controlled worker pause before takeover: %s", time.Since(pauseStarted))

	select {
	case <-finished:
		if runErr != nil {
			t.Fatalf("owned promotion failed: %s", &diagnostics)
		}
	case <-ctx.Done():
		t.Fatal("promotion deadline exceeded")
	}
	return release
}

func pooledExchange(ctx context.Context, t *testing.T, address, method, path, token, body, key, host string, status int) settingsExchange {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", key)
	}
	if host != "" {
		request.Host = host
	}
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request) // #nosec G704 -- fixed owned loopback fixture address; redirects refused.
	if err != nil {
		t.Fatal("owned recovery HTTP exchange failed")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 || response.StatusCode != status {
		t.Fatalf("owned recovery %s %s: status=%d want=%d read=%v", method, path, response.StatusCode, status, err)
	}
	return settingsExchange{status: response.StatusCode, headers: response.Header, body: raw}
}

func pooledPublicRead(ctx context.Context, t *testing.T, cfg config.Config) settingsExchange {
	t.Helper()
	result := pooledExchange(ctx, t, cfg.HTTP.Address, http.MethodGet, "/.well-known/ard.json", "", "", "", cfg.Discovery.Hosts[0].Host, 200)
	digest := sha256.Sum256(result.body)
	if result.headers.Get("ETag") != `"`+hex.EncodeToString(digest[:])+`"` {
		t.Fatal("public snapshot ETag does not bind exact bytes")
	}
	return result
}

type pooledRecoveryBindings struct {
	reservationID  string
	jobID          string
	outboxID       string
	outboxDocument string
	resourceID     string
	grantID        string
}

func qualifyPooledRecoveredWork(ctx context.Context, t *testing.T, f processFixture, releaseRetryBarrier func(), cfg, peer config.Config, workers []config.Config, roleDSN func(string) string, bindings pooledRecoveryBindings, processes []json.RawMessage, public settingsExchange) {
	t.Helper()
	// A second owner session remains free while the transaction holds its lock.
	owner, err := pgx.Connect(ctx, os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"))
	if err != nil {
		t.Fatal("connect recovered observer")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = owner.Close(cleanup)
	}()
	evidencePath := os.Getenv("CATALOG_TEST_PGBOUNCER_EVIDENCE_FILE")
	raw, err := os.ReadFile(evidencePath) // #nosec G304,G703 -- private evidence path supplied by the owned runner.
	if err != nil {
		t.Fatal(err)
	}
	var evidence map[string]any
	decodeProcessJSON(t, raw, &evidence)
	oldOwner, ok := evidence["jobOwner"].(string)
	if !ok || oldOwner == "" {
		t.Fatal("missing original Job owner")
	}
	const body = `{"slug":"pooled-abrupt-pending","displayName":"Interrupted pooled command"}`
	// The original request already returned. Refuse blind replay before repair.
	response := pooledExchange(ctx, t, cfg.HTTP.Address, http.MethodPost, "/api/v1/namespaces", "local-reader", body, "pooled-abrupt-pending", "", 503)
	var problem struct {
		Code string `json:"code"`
	}
	decodeProcessJSON(t, response.body, &problem)
	if problem.Code != "DEPENDENCY_UNAVAILABLE" {
		t.Fatal("pending reservation refusal changed")
	}
	plan := pooledRepairPlan(ctx, t, f, os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"), cfg, bindings.reservationID)
	if plan.Active || !plan.Eligible || plan.State != "interrupted" {
		t.Fatal("interrupted command is not safely repairable")
	}
	repaired := pooledRepairApply(ctx, t, f, os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"), cfg, plan, 0)
	if repaired.AuditEventID == "" {
		t.Fatal("repair has no retained audit receipt")
	}
	var receiptMatches bool
	if err := owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events WHERE tenant_id=$1 AND id=$2 AND target_id=$3 AND action='idempotency.reservation_released')`, f.tenantID, repaired.AuditEventID, bindings.reservationID).Scan(&receiptMatches); err != nil || !receiptMatches {
		t.Fatal("repair receipt does not identify its retained audit event")
	}
	// A stale reviewed plan cannot apply again.
	pooledRepairApply(ctx, t, f, os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"), cfg, plan, 1)
	created := pooledExchange(ctx, t, cfg.HTTP.Address, http.MethodPost, "/api/v1/namespaces", "local-reader", body, "pooled-abrupt-pending", "", 201)
	replay := pooledExchange(ctx, t, peer.HTTP.Address, http.MethodPost, "/api/v1/namespaces", "local-reader", body, "pooled-abrupt-pending", "", 201)
	if !bytes.Equal(created.body, replay.body) || replay.headers.Get("Idempotency-Replayed") != "true" || created.headers.Get("ETag") != `"rev-1"` || replay.headers.Get("ETag") != created.headers.Get("ETag") {
		t.Fatal("recovered command replay changed bytes or ETag")
	}
	var namespace, current catalog.Namespace
	decodeProcessJSON(t, created.body, &namespace)

	// pgx encodes PostgreSQL timestamps at microsecond precision. Exact
	// replay bytes remain independently asserted above.
	expected := namespace
	expected.CreatedAt = expected.CreatedAt.Truncate(time.Microsecond)
	expected.UpdatedAt = expected.UpdatedAt.Truncate(time.Microsecond)
	for _, address := range []string{cfg.HTTP.Address, peer.HTTP.Address} {
		decodeProcessJSON(t, f.request(ctx, address, "GET", "/api/v1/namespaces/"+namespace.ID, "local-reader", "", 200), &current)
		if !reflect.DeepEqual(expected, current) || namespace.Slug != "pooled-abrupt-pending" {
			t.Fatal("recovered command state differs from its receipt")
		}
	}

	f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/assignments/"+bindings.grantID+":revoke", "local-admin", `{"reason":"Current authority must govern recovered replay"}`, 200, map[string]string{"If-Match": `"rev-1"`, "Idempotency-Key": "pooled-recovery-revoke"})
	for _, address := range []string{cfg.HTTP.Address, peer.HTTP.Address} {
		pooledExchange(ctx, t, address, http.MethodPost, "/api/v1/namespaces", "local-reader", body, "pooled-abrupt-pending", "", 403)
	}

	var attempt int
	var newOwner, state string
	deadline := time.Now().Add(75 * time.Second)
	for {
		if err := owner.QueryRow(ctx, `SELECT state,attempt,coalesce(lease_owner,'') FROM jobs WHERE tenant_id=$1 AND id=$2`, f.tenantID, bindings.jobID).Scan(&state, &attempt, &newOwner); err != nil {
			t.Fatal("read recovering Job")
		}
		if state == "running" && attempt >= 2 && newOwner != "" && newOwner != oldOwner {
			break
		}
		if state == "failed" || state == "cancelled" || state == "dead_letter" || state == "succeeded" {
			t.Fatalf("Job missed retry barrier: state=%s attempt=%d", state, attempt)
		}
		if time.Now().After(deadline) {
			t.Fatalf("original Job did not retry: state=%s attempt=%d", state, attempt)
		}
		time.Sleep(25 * time.Millisecond)
	}
	pool, err := database.OpenRuntimeObserved(ctx, roleDSN("worker"), database.PoolConfig{MaxConnections: 1, MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute, HealthCheckPeriod: time.Second}, nil)
	if err != nil {
		t.Fatal("connect exact pooled worker for stale completion")
	}
	defer pool.Close()
	if _, err := database.VerifyRuntimeRole(ctx, pool, "worker"); err != nil {
		t.Fatal("stale completion probe lost exact worker role")
	}
	repository, err := database.NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := database.NewPostgresJobStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(ctx, f.tenantID, bindings.jobID, oldOwner, time.Now().UTC(), json.RawMessage(`{"stale":true}`)); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("stale Job owner completion was not refused: code=%s", catalog.ErrorCodeOf(err))
	}
	var unchanged bool
	if err := owner.QueryRow(ctx, `SELECT state='running' AND attempt=$3 AND lease_owner=$4 AND lease_expires_at>clock_timestamp() AND result IS NULL FROM jobs WHERE tenant_id=$1 AND id=$2`, f.tenantID, bindings.jobID, attempt, newOwner).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("stale completion changed the current lease or result")
	}
	releaseRetryBarrier()
	deadline = time.Now().Add(45 * time.Second)
	for {
		var acknowledged bool
		var retained string
		if err := owner.QueryRow(ctx, `SELECT (SELECT state='succeeded' AND attempt=$3 FROM jobs WHERE tenant_id=$1 AND id=$2),published_at IS NOT NULL AND lease_owner IS NULL AND lease_expires_at IS NULL,(to_jsonb(e)-ARRAY['published_at','lease_owner','lease_expires_at','cleanup_after','dispatch_after'])::text FROM outbox_events e WHERE tenant_id=$1 AND id=$4`, f.tenantID, bindings.jobID, attempt, bindings.outboxID).Scan(&unchanged, &acknowledged, &retained); err != nil {
			t.Fatal("observe Job completion and exact outbox acknowledgement")
		}
		if retained != bindings.outboxDocument {
			t.Fatal("pre-barrier event identity or content changed")
		}
		if unchanged && acknowledged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Job or exact outbox event did not complete")
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, check := range []struct {
		action, target string
		outbox         int
	}{
		{"namespace.created", namespace.ID, 1},
		{"idempotency.reservation_released", bindings.reservationID, 0},
		{"resource.created", bindings.resourceID, 1},
	} {
		var audits, events int
		if err := owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2 AND target_id=$3),(SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND data->>'action'=$2 AND data->>'targetId'=$3)`, f.tenantID, check.action, check.target).Scan(&audits, &events); err != nil || audits != 1 || events != check.outbox {
			t.Fatalf("logical evidence %s: audit=%d outbox=%d", check.action, audits, events)
		}
	}
	var snapshots int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM source_snapshots WHERE tenant_id=$1 AND id=(SELECT result->>'id' FROM jobs WHERE tenant_id=$1 AND id=$2)::uuid AND state='complete'`, f.tenantID, bindings.jobID).Scan(&snapshots); err != nil || snapshots != 1 {
		t.Fatalf("source retry retained %d complete snapshots", snapshots)
	}
	var completedAudits, completedEvents int
	if err := owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='source.sync.completed' AND details->'facts'->>'jobId'=$2),(SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND data->>'action'='source.sync.completed' AND subject=(SELECT result->>'id' FROM jobs WHERE tenant_id=$1 AND id=$2::uuid))`, f.tenantID, bindings.jobID).Scan(&completedAudits, &completedEvents); err != nil || completedAudits != 1 || completedEvents != 1 {
		t.Fatalf("source Job logical transitions: audits=%d events=%d", completedAudits, completedEvents)
	}
	for i, address := range []string{cfg.HTTP.Address, peer.HTTP.Address, workers[0].Worker.OperationsAddress, workers[1].Worker.OperationsAddress} {
		path := "/api/v1/service-information"
		if i >= 2 {
			path = "/status"
		}
		identity := f.request(ctx, address, "GET", path, "local-admin", "", 200)
		var got, want struct {
			InstanceID string `json:"instanceId"`
			StartedAt  string `json:"startedAt"`
		}
		decodeProcessJSON(t, identity, &got)
		decodeProcessJSON(t, processes[i], &want)
		if got != want {
			t.Fatal("writer loss restarted a selected process")
		}
		f.request(ctx, address, "GET", "/livez", "", "", 200)
		f.request(ctx, address, "GET", "/readyz", "", "", 200)
	}
	after := pooledPublicRead(ctx, t, cfg)
	if !bytes.Equal(public.body, after.body) || public.headers.Get("ETag") != after.headers.Get("ETag") {
		t.Fatal("writer recovery changed the active public snapshot")
	}
	outcomes := map[string]any{"activeRepairRefused": true, "originalRequestUnwound": true, "interruptedRepair": true, "staleRepairRefused": true, "sameKeyReplay": true, "currentAuthorityDenied": true, "logicalEvidence": true, "jobRecovered": true, "staleOwnerRefused": true, "exactOutboxAcknowledged": true, "originalProcesses": true, "publicSnapshotRetained": true, "jobAttempt": attempt, "jobOwner": newOwner}
	evidence["outcomes"] = outcomes
	raw, err = json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, raw, 0600); err != nil { // #nosec G703 -- private evidence path supplied by the owned runner.
		t.Fatal(err)
	}
	t.Logf("abrupt pooled recovery: exact command repaired/replayed with current denial; Job attempt=%d; stale owner refused; exact event acknowledged; four original processes and public bytes/ETag retained", attempt)
}
