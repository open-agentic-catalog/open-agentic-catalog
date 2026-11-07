//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// This selection proves held work, fenced promotion and retained recovery
// through the original processes and exact session-pool route.
func qualifyAbruptPooledWriterLoss(ctx context.Context, t *testing.T, f processFixture, owner *pgx.Conn, cfg, peerCfg config.Config, roleDSN func(string) string, peerURL, root string) {
	t.Helper()
	if os.Getenv("CATALOG_TEST_PGBOUNCER_DSN") == "" {
		t.Fatal("abrupt qualification requires the owned pooler")
	}

	grantID := preparePooledCommandAuthor(ctx, t, f, cfg.HTTP.Address)
	var namespace catalog.Namespace
	decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/namespaces", "local-admin", `{"slug":"pooled-abrupt-events","displayName":"Abrupt event baseline"}`, 201, map[string]string{"Idempotency-Key": "pooled-abrupt-namespace"}), &namespace)
	var resource catalog.Resource
	decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/resources", "local-admin", `{"namespaceId":"`+namespace.ID+`","canonicalIdentifier":"urn:air:catalog.example.com:agent:abrupt-event","kind":"agent"}`, 201, map[string]string{"Idempotency-Key": "pooled-abrupt-resource"}), &resource)

	// Only the schema owner holds these test barriers. Runtime roles and budgets
	// remain unchanged. A row lock preserves one pending outbox record without
	// blocking the independent command and Job outbox inserts.
	blocker, err := pgx.Connect(ctx, f.ownerDSN)
	if err != nil {
		t.Fatal("connect owned barrier")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = blocker.Close(cleanup)
	}()
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal("begin owned barrier")
	}
	workers := make([]config.Config, 2)
	workerStops := make([]func(), 0, len(workers))
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		_ = tx.Rollback(cleanup)
		cancel()
		// Release test locks before graceful worker shutdown on a failed setup.
		for i := len(workerStops) - 1; i >= 0; i-- {
			workerStops[i]()
		}
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL application_name='pooled-abrupt-barrier'`); err != nil {
		t.Fatal("hold owned write barriers")
	}
	var outboxID, outboxDocument string
	if err := tx.QueryRow(ctx, `SELECT id, (to_jsonb(e)-ARRAY['published_at','lease_owner','lease_expires_at','cleanup_after','dispatch_after'])::text FROM outbox_events e WHERE tenant_id=$1 AND published_at IS NULL AND lease_owner IS NULL AND data->>'targetId'=$2 ORDER BY created_at,id LIMIT 1 FOR UPDATE`, f.tenantID, resource.ID).Scan(&outboxID, &outboxDocument); err != nil {
		t.Fatalf("hold an existing pending outbox event: %v", err)
	}

	processes := make([]json.RawMessage, 0, 4)
	identity := func(address, path string) {
		raw := f.request(ctx, address, "GET", path, "local-admin", "", 200)
		var value struct {
			InstanceID string `json:"instanceId"`
			StartedAt  string `json:"startedAt"`
		}
		decodeProcessJSON(t, raw, &value)
		if value.InstanceID == "" || value.StartedAt == "" {
			t.Fatal("missing original execution identity")
		}
		bounded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		processes = append(processes, bounded)
	}
	identity(cfg.HTTP.Address, "/api/v1/service-information")
	identity(peerCfg.HTTP.Address, "/api/v1/service-information")
	var firstWorker *os.Process
	for i := range workers {
		workers[i] = cfg
		workers[i].Worker.OperationsAddress = processAddress(ctx, t)
		if i == 1 {
			continue
		}
		stop, process := f.startProcess(ctx, "worker", roleDSN("worker"), f.configuration("abrupt-worker-"+string(rune('1'+i)), workers[i]), workers[i].Worker.OperationsAddress)
		firstWorker = process
		workerStops = append(workerStops, stop)
		identity(workers[i].Worker.OperationsAddress, "/status")
	}

	f.run(ctx, f.command(ctx, "catalogctl", "", "smoke", "--base-url", "http://"+cfg.HTTP.Address, "--run-id", "pooled-abrupt-public", "--timeout", "2m"), 0)
	public := pooledPublicRead(ctx, t, cfg)

	// Earlier process checks leave work for their own sources. Drain eligible
	// work before taking table barriers, so it cannot occupy both selected
	// workers ahead of this test's exact Job.
	drainDeadline := time.Now().Add(45 * time.Second)
	for {
		var active int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE state='running' OR state IN ('queued','waiting') AND not_before<=clock_timestamp()`).Scan(&active); err != nil {
			t.Fatal("observe fixture Job drain")
		}
		if active == 0 {
			break
		}
		if time.Now().After(drainDeadline) {
			t.Fatal("earlier eligible fixture Jobs did not drain")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE namespaces,source_snapshots IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal("hold owned write barriers")
	}
	var inspected federation.Inspection
	decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/federation/source-inspections", "local-admin", `{"url":"`+peerURL+`/abrupt-writer","networkProfile":"process-settings"}`, 201, map[string]string{"Idempotency-Key": "pooled-abrupt-inspect"}), &inspected)
	var source federation.Source
	decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/federation/sources", "local-admin", `{"inspectionId":"`+inspected.ID+`","displayName":"Abrupt pooled writer","retrievalMode":"indexed","updateMode":"manual_review","scope":{"kinds":["agent"]},"networkProfile":"process-settings","reason":"Owned abrupt writer qualification"}`, 201, map[string]string{"Idempotency-Key": "pooled-abrupt-source"}), &source)

	var accepted struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/federation/sources/"+source.ID+":sync", "local-admin", `{}`, 202, map[string]string{"If-Match": `"rev-1"`, "Idempotency-Key": "pooled-abrupt-sync"}), &accepted)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan settingsExchange, 1)
	requestFinished := false
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "http://"+cfg.HTTP.Address+"/api/v1/namespaces", bytes.NewBufferString(`{"slug":"pooled-abrupt-pending","displayName":"Interrupted pooled command"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer local-reader")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "pooled-abrupt-pending")
	go func() {
		response, err := client.Do(request) // #nosec G704 -- exact owned loopback API; redirects refused.
		if err != nil {
			done <- settingsExchange{err: err}
			return
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(response.Body, 8193))
		done <- settingsExchange{status: response.StatusCode, body: body, err: err}
	}()
	// Join the original request even when a barrier or promotion assertion fails.
	defer func() {
		cancel()
		if requestFinished {
			return
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("original request did not unwind")
		}
	}()
	var reservation, initialOwner string
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked int
		err := owner.QueryRow(ctx, `SELECT count(DISTINCT a.usename) FROM pg_stat_activity a WHERE a.usename IN ('catalog_api','catalog_worker') AND EXISTS (SELECT 1 FROM pg_stat_activity b WHERE b.application_name='pooled-abrupt-barrier' AND b.pid=ANY(pg_blocking_pids(a.pid)))`).Scan(&blocked)
		if err != nil {
			t.Fatal("observe held runtime work")
		}
		var leased bool
		var jobState string
		var attempt int
		if err := owner.QueryRow(ctx, `SELECT state,attempt,coalesce(lease_owner,''),state='running' AND attempt=1 AND lease_owner IS NOT NULL AND lease_expires_at>clock_timestamp() FROM jobs WHERE tenant_id=$1 AND id=$2`, f.tenantID, accepted.Job.ID).Scan(&jobState, &attempt, &initialOwner, &leased); err != nil {
			t.Fatal("observe selected Job lease")
		}
		if blocked == 2 && leased {
			if err := owner.QueryRow(ctx, `SELECT reservation_id::text FROM idempotency_keys WHERE tenant_id=$1 AND idempotency_key='pooled-abrupt-pending' AND atomic_response_status IS NULL`, f.tenantID).Scan(&reservation); err != nil {
				t.Fatal("observe incomplete reservation")
			}
			break
		}
		select {
		case <-done:
			t.Fatal("request completed before writer loss")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime work did not reach owned barriers: roles=%d job_state=%s attempt=%d leased=%t", blocked, jobState, attempt, leased)
		}
		time.Sleep(10 * time.Millisecond)
	}

	stopSecond := f.start(ctx, "worker", roleDSN("worker"), f.configuration("abrupt-worker-two", workers[1]), workers[1].Worker.OperationsAddress)
	workerStops = append(workerStops, stopSecond)
	identity(workers[1].Worker.OperationsAddress, "/status")
	active := pooledRepairPlan(ctx, t, f, f.ownerDSN, cfg, reservation)
	if !active.Active || active.Eligible || active.State != "active" {
		t.Fatal("active repair plan did not refuse held command")
	}
	pooledRepairApply(ctx, t, f, f.ownerDSN, cfg, active, 1)
	listeners := []string{"http://" + cfg.HTTP.Address, "http://" + peerCfg.HTTP.Address, "http://" + workers[0].Worker.OperationsAddress, "http://" + workers[1].Worker.OperationsAddress}
	payload, err := json.Marshal(map[string]any{"tenant": f.tenantID, "reservation": reservation, "job": accepted.Job.ID, "outbox": outboxID, "outboxDocument": json.RawMessage(outboxDocument), "processes": processes, "listeners": listeners})
	if err != nil {
		t.Fatal(err)
	}
	promotion := exec.CommandContext(ctx, os.Getenv("CATALOG_TEST_PGBOUNCER_PYTHON"), "-B", filepath.Join(root, "test", "pgbouncer", "writer_recovery.py")) // #nosec G204,G702 -- exact invocation-owned qualification helper.
	promotion.Env = append(os.Environ(), "CATALOG_TEST_PGBOUNCER_API=http://"+cfg.HTTP.Address)
	promotion.Stdin = bytes.NewReader(payload)

	releaseRetryBarrier := promoteWithRetryBarrier(ctx, t, promotion, firstWorker, f.tenantID, accepted.Job.ID, initialOwner)
	defer releaseRetryBarrier()
	awaitFreshPooledRoles(ctx, t, roleDSN)
	select {
	case response := <-done:
		requestFinished = true
		var problem struct {
			Code string `json:"code"`
		}
		decodeProcessJSON(t, response.body, &problem)
		if response.err != nil || response.status != 503 || problem.Code != "DEPENDENCY_UNAVAILABLE" {
			t.Fatal("original interrupted request did not unwind with dependency refusal")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("original execution did not unwind before repair")
	}
	qualifyPooledRecoveredWork(ctx, t, f, releaseRetryBarrier, cfg, peerCfg, workers, roleDSN, pooledRecoveryBindings{
		reservationID:  reservation,
		jobID:          accepted.Job.ID,
		outboxID:       outboxID,
		outboxDocument: outboxDocument,
		resourceID:     resource.ID,
		grantID:        grantID,
	}, processes, public)
}
