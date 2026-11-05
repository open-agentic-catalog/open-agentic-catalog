//go:build minikube

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const pressureJobs = 12
const pressureReads = 96
const pressureClients = 16

type replicaPressureReport struct {
	Jobs             []string           `json:"jobs"`
	Running          int                `json:"observedRunning"`
	Queued           int                `json:"observedQueued"`
	QueueAgeSeconds  float64            `json:"observedQueueAgeSeconds"`
	Connections      int                `json:"runtimeConnections"`
	OrdinaryCapacity int                `json:"ordinaryCapacity"`
	Reads            int                `json:"reads"`
	Clients          int                `json:"clients"`
	Seconds          float64            `json:"seconds"`
	CancelSeconds    float64            `json:"cancelUnwindSeconds"`
	PoolMetrics      map[string]float64 `json:"selectedApiPoolMetrics"`
}

type pressureSource struct {
	ID       string
	Revision int64
	Token    string
	JobID    string
	Key      string
	Accepted httpResult
}

type pressureQueue struct {
	Total            int     `json:"total"`
	Running          int     `json:"running"`
	Queued           int     `json:"queued"`
	Owners           int     `json:"owners"`
	Age              float64 `json:"age"`
	Connections      int     `json:"connections"`
	OrdinaryCapacity int     `json:"ordinaryCapacity"`
}

func validPressureQueue(q pressureQueue) bool {
	return q.Total == pressureJobs && q.Running == 3 && q.Queued == 9 && q.Owners == 3 && q.Age >= 0 && q.Age < 120 && q.Connections > 0 && q.Connections <= 48 && q.OrdinaryCapacity >= 50
}

func validPressureSelf(r httpResult) bool {
	var me struct {
		TenantID  string `json:"tenantId"`
		Subject   string `json:"subject"`
		ActorType string `json:"actorType"`
	}
	return r.Status == 200 && json.Unmarshal(r.Body, &me) == nil && me.TenantID == primaryTenantID && me.Subject == "oidc-admin" && me.ActorType == "human"
}

// verifyReplicaPressure adds only the missing packaged queue and cancellation
// boundaries. The existing integrity/load/replacement assertions remain intact.
func (h *harness) verifyReplicaPressure(apis, uis []string, cookie string) (resultErr error) {
	ctx, cancel := context.WithTimeout(h.ctx, 120*time.Second)
	defer cancel()
	started := time.Now()
	before := map[string][]podIdentity{}
	for _, role := range []string{"api", "worker", "ui"} {
		pods, err := h.pods(role)
		if err != nil || len(pods) != 3 {
			return fmt.Errorf("pressure requires three %s Pods: %w", role, err)
		}
		before[role] = pods
	}
	// The steady-state pool bound excludes surge and terminating processes.
	// Observe the complete selected inventory, not only ready/non-terminating Pods.
	if err := waitTopology(ctx, func() (bool, error) {
		raw, e := h.topologyCommand(ctx, "require drained replica inventory before pressure", nil, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/instance=catalog,app.kubernetes.io/component in (api,worker,ui)", "-o", "json")
		var inventory struct {
			Items []struct {
				Metadata struct {
					DeletionTimestamp string `json:"deletionTimestamp"`
				} `json:"metadata"`
			} `json:"items"`
		}
		if e != nil {
			return false, e
		}
		if json.Unmarshal(raw, &inventory) != nil {
			return false, errors.New("invalid complete pressure Pod inventory")
		}
		if len(inventory.Items) != 9 {
			return false, nil
		}
		for _, pod := range inventory.Items {
			if pod.Metadata.DeletionTimestamp != "" {
				return false, nil
			}
		}
		return true, nil
	}); err != nil {
		return err
	}
	if err := h.replicaResources("before-pressure"); err != nil {
		return err
	}
	ca, err := os.ReadFile(filepath.Join(h.temporary, "fixture", "trust", "functional-ca.pem")) // #nosec G304 -- Invocation-owned fixed fixture path.
	if err != nil {
		return err
	}
	fixtureClient, err := fixtureHTTPClient(ca)
	if err != nil {
		return err
	}
	defer fixtureClient.CloseIdleConnections()
	address, err := h.startPortForward("service/federation-fixture", 8443)
	if err != nil {
		return err
	}
	fixtureURL := "https://" + address
	capacityRaw, err := h.drainSQL(ctx, "SELECT current_setting('max_connections')::int-current_setting('superuser_reserved_connections')::int-current_setting('reserved_connections')::int")
	capacity, parseErr := strconv.Atoi(strings.TrimSpace(string(capacityRaw)))
	if err != nil || parseErr != nil || capacity < 50 {
		return errors.New("pressure budget requires 48 runtime slots and two direct SQL observer slots")
	}
	sources, err := h.preparePressureSources(ctx, apis)
	if err != nil {
		return err
	}
	client := replicaHTTPClient()
	defer client.CloseIdleConnections()
	// The slot limit belongs to the worker configuration, not a shared queue cap.
	armed, err := h.requestContext(ctx, "POST", fixtureURL+"/pressure/arm", "", "", "", nil, fixtureClient)
	if err != nil || armed.Status != 204 {
		return errors.New("pressure response barrier did not arm")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		released, e := h.requestContext(cleanup, "POST", fixtureURL+"/pressure/release", "", "", "", nil, fixtureClient)
		if e != nil || released.Status != 204 {
			resultErr = errors.Join(resultErr, errors.New("pressure barrier cleanup failed"))
		}
	}()
	for _, indices := range [][]int{{0, 1, 2, 3, 4, 5, 6, 7, 8}, {9, 10, 11}} {
		calls := make([]func() error, len(indices))
		for i, index := range indices {
			calls[i] = func() error {
				s := &sources[index]
				s.Key = fmt.Sprintf("pressure-sync-%s-%d", h.suffix, index)
				accepted, e := h.requestContext(ctx, "POST", apis[index%3]+"/api/v1/federation/sources/"+s.ID+":sync", s.Token, s.Key, fmt.Sprintf(`"rev-%d"`, s.Revision), map[string]any{}, client)
				var envelope struct {
					Job struct {
						ID string `json:"id"`
					} `json:"job"`
				}
				if e != nil || accepted.Status != 202 || json.Unmarshal(accepted.Body, &envelope) != nil || !identity.ValidTenantID(envelope.Job.ID) {
					return errors.New("pressure sync was not accepted with a Job identity")
				}
				s.JobID, s.Accepted = envelope.Job.ID, accepted
				return nil
			}
		}
		if err := runConcurrent(calls...); err != nil {
			return err
		}
	}
	ids := make([]string, pressureJobs)
	seen := map[string]bool{}
	for i, s := range sources {
		if seen[s.JobID] {
			return errors.New("pressure admission reused a Job across distinct sources")
		}
		seen[s.JobID] = true
		ids[i] = "'" + s.JobID + "'"
	}
	selected := "tenant_id IN ('" + primaryTenantID + "','" + otherTenantID + "') AND id IN (" + strings.Join(ids, ",") + ")"
	report := replicaPressureReport{Reads: pressureReads, Clients: pressureClients, PoolMetrics: map[string]float64{}}
	observeSQL := "SELECT json_build_object('total',count(*),'running',count(*) FILTER(WHERE state='running'),'queued',count(*) FILTER(WHERE state='queued'),'owners',count(DISTINCT lease_owner) FILTER(WHERE state='running'),'age',COALESCE(max(EXTRACT(epoch FROM clock_timestamp()-created_at)) FILTER(WHERE state='queued'),0),'ordinaryCapacity',current_setting('max_connections')::int-current_setting('superuser_reserved_connections')::int-current_setting('reserved_connections')::int,'connections',(SELECT count(*) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker'))) FROM jobs WHERE " + selected
	if err := waitTopology(ctx, func() (bool, error) {
		raw, e := h.drainSQL(ctx, observeSQL)
		if e != nil {
			return false, e
		}
		var q pressureQueue
		if json.Unmarshal(raw, &q) != nil {
			return false, errors.New("invalid pressure queue observation")
		}
		if validPressureQueue(q) {
			report.Running, report.Queued, report.QueueAgeSeconds, report.Connections, report.OrdinaryCapacity = q.Running, q.Queued, q.Age, q.Connections, q.OrdinaryCapacity
			return true, nil
		}
		if q.Total != 12 || q.Running > 3 || q.Running+q.Queued != 12 {
			return false, fmt.Errorf("pressure jobs left the constrained interval: %+v", q)
		}
		return false, nil
	}); err != nil {
		return err
	}
	// Read both tenants through the product API while the same twelve Jobs remain held.
	observed := map[string]string{}
	for _, token := range []string{h.tokens["admin"], h.tokens["other"]} {
		response, e := h.requestContext(ctx, "GET", apis[0]+"/api/v1/jobs?kind=source.sync&limit=100", token, "", "", nil, client)
		var page struct {
			Items []struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"items"`
		}
		if e != nil || response.Status != 200 || json.Unmarshal(response.Body, &page) != nil {
			return errors.New("pressure Job page failed")
		}
		for _, job := range page.Items {
			if seen[job.ID] {
				if observed[job.ID] != "" {
					return errors.New("pressure Job crossed tenants")
				}
				observed[job.ID] = job.State
			}
		}
	}
	running, queued := 0, 0
	for _, state := range observed {
		switch state {
		case "running":
			running++
		case "queued":
			queued++
		}
	}
	if running != 3 || queued != 9 {
		return fmt.Errorf("product Job pages did not expose three running/nine queued Jobs: %d/%d", running, queued)
	}
	state, e := h.requestContext(ctx, "GET", fixtureURL+"/pressure/state", "", "", "", nil, fixtureClient)
	var barrier struct {
		Active  int  `json:"active"`
		Peak    int  `json:"peak"`
		Held    bool `json:"held"`
		Expired bool `json:"expired"`
	}
	if e != nil || state.Status != 200 || json.Unmarshal(state.Body, &barrier) != nil || barrier.Active != 3 || barrier.Peak != 3 || !barrier.Held || barrier.Expired {
		return errors.New("source response barrier did not positively hold all three worker slots")
	}
	calls := make([]func() error, pressureClients)
	for lane := range pressureClients {
		calls[lane] = func() error {
			for n := lane; n < pressureReads; n += pressureClients {
				var response httpResult
				var e error
				if n%2 == 0 {
					response, e = h.requestContext(ctx, "GET", apis[n%3]+"/api/v1/me", h.tokens["admin"], "", "", nil, client)
				} else {
					response, e = h.uiCommandContext(ctx, client, uis[n%3], "GET", "/browser/_catalog/api/v1/me", "", cookie, "", "")
				}
				if e != nil || !validPressureSelf(response) {
					return errors.New("bounded pressure read failed or changed identity")
				}
			}
			return nil
		}
	}
	if err := runConcurrent(calls...); err != nil {
		return err
	}
	// Close accepted command connections while work is positively held. Retained
	// identities must finish through the worker, not depend on browser lifetime.
	client.CloseIdleConnections()
	released, e := h.requestContext(ctx, "POST", fixtureURL+"/pressure/release", "", "", "", nil, fixtureClient)
	if e != nil || released.Status != 204 {
		return errors.New("source response barrier release failed")
	}
	state, e = h.requestContext(ctx, "GET", fixtureURL+"/pressure/state", "", "", "", nil, fixtureClient)
	if e != nil || json.Unmarshal(state.Body, &barrier) != nil || barrier.Expired || barrier.Held {
		return errors.New("pressure barrier expired instead of explicit release")
	}
	if err := waitTopology(ctx, func() (bool, error) {
		raw, e := h.drainSQL(ctx, "SELECT count(*) FROM jobs WHERE "+selected+" AND state='succeeded' AND attempt=1 AND lease_owner IS NULL AND lease_expires_at IS NULL AND NOT cancellation_requested")
		return strings.TrimSpace(string(raw)) == "12", e
	}); err != nil {
		return err
	}
	snapshots := map[string]string{}
	for i, s := range sources {
		report.Jobs = append(report.Jobs, s.JobID)
		read, e := h.requestContext(ctx, "GET", apis[i%3]+"/api/v1/jobs/"+s.JobID, s.Token, "", "", nil, client)
		var job struct {
			ID      string `json:"id"`
			State   string `json:"state"`
			Attempt int    `json:"attempt"`
			Result  struct {
				SnapshotID string `json:"snapshotId"`
			} `json:"result"`
		}
		if e != nil || read.Status != 200 || json.Unmarshal(read.Body, &job) != nil || job.ID != s.JobID || job.State != "succeeded" || job.Attempt != 1 || !identity.ValidTenantID(job.Result.SnapshotID) {
			return errors.New("accepted pressure Job did not finish on its first attempt")
		}
		snapshots[s.JobID] = job.Result.SnapshotID
		denied, e := h.requestContext(ctx, "GET", apis[i%3]+"/api/v1/jobs/"+s.JobID, map[bool]string{true: h.tokens["other"], false: h.tokens["admin"]}[i < 9], "", "", nil, client)
		if e != nil || !replicaProblem(denied, 404, catalog.CodeNotFound) {
			return errors.New("pressure Job crossed the tenant boundary")
		}
		replay, e := h.requestContext(ctx, "POST", apis[(i+1)%3]+"/api/v1/federation/sources/"+s.ID+":sync", s.Token, s.Key, fmt.Sprintf(`"rev-%d"`, s.Revision), map[string]any{}, client)
		if e != nil || replay.Status != 202 || replay.Header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(replay.Body, s.Accepted.Body) {
			return errors.New("completed pressure command did not replay its exact accepted Job")
		}
	}
	if err := h.verifyPressureAudits(ctx, apis[0], sources, snapshots, client); err != nil {
		return err
	}
	report.CancelSeconds, err = h.verifyReplicaClientCancellation(ctx, apis[0], before["api"][0])
	if err != nil {
		return err
	}
	metrics, e := h.requestContext(ctx, "GET", apis[0]+"/metrics", "", "", "", nil, client)
	if e != nil || metrics.Status != 200 {
		return errors.New("pressure pool metrics unavailable")
	}
	for _, metric := range []string{"open_agentic_catalog_postgresql_pool_acquires_total", "open_agentic_catalog_postgresql_pool_acquire_seconds_total", "open_agentic_catalog_postgresql_pool_acquire_canceled_total", "open_agentic_catalog_postgresql_pool_empty_acquires_total"} {
		value, e := fixedMetric(metrics.Body, metric)
		if e != nil {
			return e
		}
		report.PoolMetrics[metric] = value
	}
	report.Seconds = time.Since(started).Seconds()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := h.replicaResources("after-pressure"); err != nil {
		return err
	}
	for role, pods := range before {
		after, e := h.pods(role)
		if e != nil || !sameUIDs(pods, after) {
			return fmt.Errorf("pressure replaced %s processes", role)
		}
	}
	report.Seconds = time.Since(started).Seconds()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	h.report.ReplicaPressure = &report
	h.report.Checks = append(h.report.Checks, "three-worker-slots-and-observed-shared-queue", "two-tenant-pressure-progress-and-exact-job-replay", "accepted-job-survives-command-disconnection", "packaged-request-cancellation-releases-observed-sql-wait", "pressure-retains-original-processes")
	return nil
}

func (h *harness) preparePressureSources(ctx context.Context, apis []string) ([]pressureSource, error) {
	sources := make([]pressureSource, pressureJobs)
	for i := range sources {
		token := h.tokens["admin"]
		if i >= 9 {
			token = h.tokens["other"]
		}
		prefix := fmt.Sprintf("pressure-%s-%d", h.suffix, i)
		inspection, e := h.requestContext(ctx, "POST", apis[i%3]+"/api/v1/federation/source-inspections", token, prefix+"-inspect", "", map[string]any{"url": fmt.Sprintf("https://federation-fixture:8443/pressure/%d/.well-known/ard.json", i), "networkProfile": "functional-fixture"}, nil)
		var inspected struct {
			ID string `json:"id"`
		}
		if e != nil {
			return nil, fmt.Errorf("pressure source %d inspection transport: %w", i, e)
		}
		if inspection.Status != 201 || json.Unmarshal(inspection.Body, &inspected) != nil || !identity.ValidTenantID(inspected.ID) {
			return nil, fmt.Errorf("pressure source %d inspection returned HTTP %d or invalid evidence", i, inspection.Status)
		}
		created, e := h.requestContext(ctx, "POST", apis[i%3]+"/api/v1/federation/sources", token, prefix+"-connect", "", map[string]any{"inspectionId": inspected.ID, "displayName": prefix, "retrievalMode": federation.RetrievalIndexed, "scope": map[string]any{"kinds": []catalog.Kind{catalog.KindAgent}}, "updateMode": federation.UpdateManual, "networkProfile": "functional-fixture", "reason": "Qualify bounded packaged queue progress"}, nil)
		var source struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		if e != nil {
			return nil, fmt.Errorf("pressure source %d connection transport: %w", i, e)
		}
		if created.Status != 201 || json.Unmarshal(created.Body, &source) != nil || !identity.ValidTenantID(source.ID) || source.Revision < 1 {
			return nil, fmt.Errorf("pressure source %d connection returned HTTP %d or invalid evidence", i, created.Status)
		}
		sources[i] = pressureSource{ID: source.ID, Revision: source.Revision, Token: token}
	}
	return sources, nil
}

func (h *harness) verifyPressureAudits(ctx context.Context, base string, sources []pressureSource, snapshots map[string]string, client *http.Client) error {
	for _, token := range []string{h.tokens["admin"], h.tokens["other"]} {
		response, e := h.requestContext(ctx, "GET", base+"/api/v1/audit-events?action=source.sync.completed&limit=100", token, "", "", nil, client)
		var page struct {
			Items []struct {
				TargetID string `json:"targetId"`
				Details  struct {
					Facts struct {
						JobID   string `json:"jobId"`
						Attempt int    `json:"jobAttempt"`
					} `json:"facts"`
				} `json:"details"`
			} `json:"items"`
		}
		if e != nil || response.Status != 200 || json.Unmarshal(response.Body, &page) != nil {
			return errors.New("pressure completion audit read failed")
		}
		for _, source := range sources {
			if source.Token != token {
				continue
			}
			count := 0
			for _, event := range page.Items {
				if event.Details.Facts.JobID == source.JobID && event.Details.Facts.Attempt == 1 && event.TargetID == snapshots[source.JobID] {
					count++
				}
			}
			if count != 1 {
				return errors.New("pressure Job lacks singular completion audit")
			}
		}
	}
	return nil
}

// The lock stays held until cancellation has removed the positively observed
// waiter. Success after releasing the lock would not establish cancellation.
func (h *harness) verifyReplicaClientCancellation(parent context.Context, base string, pod podIdentity) (elapsed float64, resultErr error) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	created, e := h.requestContext(ctx, "POST", base+"/api/v1/namespaces", h.tokens["admin"], "pressure-cancel-"+h.suffix, "", map[string]string{"slug": "pressure-cancel-" + h.suffix, "displayName": "Pressure cancellation"}, nil)
	var namespaceValue catalog.Namespace
	if e != nil || created.Status != 201 || json.Unmarshal(created.Body, &namespaceValue) != nil || !identity.ValidTenantID(namespaceValue.ID) {
		return 0, errors.New("cancellation control creation failed")
	}
	ipRaw, e := h.topologyCommand(ctx, "identify pressure API", nil, "-n", namespace, "get", "pod", pod.Name, "-o", "jsonpath={.status.podIP}")
	ip := net.ParseIP(strings.TrimSpace(string(ipRaw)))
	if e != nil || ip == nil {
		return 0, errors.New("pressure API lacks a valid address")
	}
	lockName := "catalog-pressure-" + h.suffix
	done := make(chan error, 1)
	lockCtx, stopLock := context.WithCancel(ctx)
	go func() {
		_, e := h.topologyCommand(lockCtx, "hold pressure read barrier", nil, "-n", namespace, "exec", "postgres-0", "--", "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-c", "BEGIN; SET LOCAL application_name='"+lockName+"'; SET LOCAL statement_timeout='15s'; LOCK TABLE namespaces IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(15); ROLLBACK")
		done <- e
	}()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
		defer cancel()
		_, e := h.drainSQL(cleanup, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='"+lockName+"' AND usename='catalog_owner'")
		stopLock()
		lockErr := <-done // The barrier owner is intentionally terminated.
		if cleanup.Err() != nil {
			resultErr = errors.Join(resultErr, lockErr, cleanup.Err())
		}
		resultErr = errors.Join(resultErr, e)
	}()
	pid := 0
	if e := waitTopology(ctx, func() (bool, error) {
		raw, e := h.drainSQL(ctx, "SELECT a.pid FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE a.application_name='"+lockName+"' AND l.relation='namespaces'::regclass AND l.mode='AccessExclusiveLock' AND l.granted")
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		return pid > 0, e
	}); e != nil {
		return 0, e
	}
	requestCtx, stopRequest := context.WithCancel(ctx)
	defer stopRequest()
	result := make(chan error, 1)
	var wait sync.WaitGroup
	wait.Add(1)
	defer wait.Wait()
	defer stopRequest()
	go func() {
		defer wait.Done()
		_, e := h.requestContext(requestCtx, "GET", base+"/api/v1/namespaces/"+namespaceValue.ID, h.tokens["admin"], "", "", nil, nil)
		result <- e
	}()
	blocked := fmt.Sprintf("SELECT count(*) FROM pg_stat_activity WHERE usename='catalog_api' AND client_addr='%s'::inet AND state='active' AND wait_event_type='Lock' AND query='SELECT id,tenant_id,slug,display_name,description,revision,created_at,updated_at FROM namespaces WHERE tenant_id=$1 AND id=$2' AND %d=ANY(pg_blocking_pids(pid))", ip.String(), pid)
	if e := waitTopology(ctx, func() (bool, error) {
		select {
		case requestErr := <-result:
			if requestErr != nil {
				return false, fmt.Errorf("request failed before its SQL wait: %w", requestErr)
			}
			return false, errors.New("request completed before its SQL wait was observed")
		default:
		}
		raw, e := h.drainSQL(ctx, blocked)
		return strings.TrimSpace(string(raw)) == "1", e
	}); e != nil {
		return 0, e
	}
	unwind, stopUnwind := context.WithTimeout(ctx, 3*time.Second)
	defer stopUnwind()
	started := time.Now()
	if err := cancelPressureRequest(unwind, stopRequest, result); err != nil {
		return 0, err
	}

	if e := waitTopology(unwind, func() (bool, error) {
		raw, e := h.drainSQL(unwind, blocked)
		return strings.TrimSpace(string(raw)) == "0", e
	}); e != nil {
		return 0, e
	}
	if e := waitTopology(unwind, func() (bool, error) {
		metrics, e := h.requestContext(unwind, "GET", base+"/metrics", "", "", "", nil, nil)
		if e != nil {
			return false, e
		}
		if metrics.Status != http.StatusOK {
			return false, errors.New("pool-release metrics unavailable")
		}
		acquired, e := fixedMetric(metrics.Body, `open_agentic_catalog_postgresql_pool_connections{state="acquired"}`)
		return acquired == 0, e
	}); e != nil {
		return 0, e
	}
	raw, e := h.drainSQL(unwind, fmt.Sprintf("SELECT count(*) FROM pg_stat_activity WHERE pid=%d AND application_name='%s'", pid, lockName))
	if e != nil || strings.TrimSpace(string(raw)) != "1" {
		return 0, errors.New("database barrier ended before cancellation unwind")
	}
	if err := pressureBudgetError(unwind); err != nil {
		return 0, err
	}
	elapsed = time.Since(started).Seconds()
	if _, e := h.drainSQL(ctx, fmt.Sprintf("SELECT pg_terminate_backend(%d)", pid)); e != nil {
		return 0, e
	}
	read, e := h.requestContext(ctx, "GET", base+"/api/v1/namespaces/"+namespaceValue.ID, h.tokens["admin"], "", "", nil, nil)
	if e != nil || !sameNamespaceState(read, created) {
		return 0, errors.New("original API pool did not recover after client cancellation")
	}
	return elapsed, nil
}

// cancelPressureRequest uses the already-started unwind budget. A slow client
// must not receive an extra deadline before SQL and pool-release observations.
func cancelPressureRequest(ctx context.Context, cancelRequest context.CancelFunc, result <-chan error) error {
	cancelRequest()
	select {
	case err := <-result:
		if err := pressureBudgetError(ctx); err != nil {
			return err
		}
		if !errors.Is(err, context.Canceled) {
			return errors.New("packaged client did not report explicit cancellation")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Context timer delivery can follow a completed observation at the exact
// deadline. Reject that result even when Err has not yet been published.
func pressureBudgetError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}
