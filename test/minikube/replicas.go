//go:build minikube

// Replica workloads check shared-state integrity and bounded mixed traffic across API/UI Pods.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const replicaRequests = 600
const replicaConcurrency = 6

type replicaBatchReport struct {
	Phase       string  `json:"phase"`
	Requests    int     `json:"requests"`
	Writes      int     `json:"writes"`
	Concurrency int     `json:"concurrency"`
	Seconds     float64 `json:"seconds"`
	PerSecond   float64 `json:"requestsPerSecond"`
	P50Millis   float64 `json:"p50Millis"`
	P95Millis   float64 `json:"p95Millis"`
	P99Millis   float64 `json:"p99Millis"`
}

type replicaResourceReport struct {
	Phase        string `json:"phase"`
	Pod          string `json:"pod"`
	Role         string `json:"role"`
	WorkingSet   uint64 `json:"workingSetBytes"`
	CPUCoreNanos uint64 `json:"cpuCoreNanoSeconds"`
	Restarts     int    `json:"restarts"`
}

// Replica requests deliberately bypass random load-balancer selection. UI
// requests still traverse the real backend Service from inside the cluster.
type replicaRequest func(replica int, method, path, key, match string, body any) (httpResult, error)

type replicaEvidence struct {
	Key  string
	Body map[string]string
	Want httpResult
}

// replicaHTTPClient builds a proxy-free, non-redirecting client with connection and deadline limits sized for the fixed replica workload.
func replicaHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       10 * time.Second,
		Transport:     &http.Transport{Proxy: nil, MaxConnsPerHost: replicaConcurrency, MaxIdleConns: 24, MaxIdleConnsPerHost: replicaConcurrency, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: 8 * time.Second, IdleConnTimeout: 15 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// replicaClients binds indexed direct-API and authenticated UI-proxy request functions to the supplied Pod tunnels.
func (h *harness) replicaClients(apis, uis []string, cookie string, client *http.Client) (replicaRequest, replicaRequest) {
	api := func(replica int, method, path, key, match string, body any) (httpResult, error) {
		return h.request(method, apis[replica]+path, h.tokens["admin"], key, match, body, client)
	}
	ui := func(replica int, method, path, key, match string, body any) (httpResult, error) {
		var raw []byte
		if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				return httpResult{}, err
			}
		}
		return h.uiCommand(client, uis[replica], method, "/browser/_catalog"+path, string(raw), cookie, key, match)
	}
	return api, ui
}

// replicaAddresses requires three ready Pods with the expected image and service account, then opens a separate tunnel to each.
func (h *harness) replicaAddresses(role string) ([]string, error) {
	pods, err := h.pods(role)
	if err != nil || len(pods) != 3 {
		return nil, fmt.Errorf("expected three %s replicas", role)
	}
	addresses := make([]string, 0, 3)
	for _, pod := range pods {
		imageID := h.report.Images["application"]
		if role == "ui" {
			imageID = h.report.Images["ui"]
		}
		if !pod.Ready || pod.ImageID != imageID || pod.ServiceAccount != "catalog-"+role {
			return nil, fmt.Errorf("unexpected %s replica identity", role)
		}
		address, err := h.startPortForward("pod/"+pod.Name, 8080)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, "http://"+address)
	}
	h.recordPlacement("replicas-"+role, pods)
	return addresses, nil
}

// verifyReplicaWorkload tests state integrity before timing, then uses a fixed
// small seed and bounded mixed batches. Timings include loopback port-forward
// overhead and are not a production capacity or linear-scaling claim.
func (h *harness) verifyReplicaWorkload(uis []string, cookie string) error {
	apis, err := h.replicaAddresses("api")
	if err != nil {
		return err
	}
	client := replicaHTTPClient()
	defer client.CloseIdleConnections()
	api, ui := h.replicaClients(apis, uis, cookie, client)
	for index := range uis {
		unauthenticated, err := h.uiCommand(client, uis[index], "GET", "/browser/_catalog/api/v1/me", "", "", "", "")
		if err != nil || unauthenticated.Status != http.StatusUnauthorized {
			return errors.New("UI replica accepted an unauthenticated API request")
		}
	}
	var updates []httpResult
	for index, request := range []replicaRequest{api, ui} {
		updated, err := h.verifyReplicaCommands(request, fmt.Sprintf("replica-race-%d-%s", index, h.suffix))
		if err != nil {
			return fmt.Errorf("replica command transport %d: %w", index, err)
		}
		updates = append(updates, updated)
	}
	if err := h.verifyReplicaIsolation(apis, updates, client); err != nil {
		return err
	}
	var value catalog.Namespace
	if err := json.Unmarshal(updates[0].Body, &value); err != nil {
		return err
	}
	reader := func(replica int, method, path, key, match string, body any) (httpResult, error) {
		return h.request(method, apis[replica]+path, h.tokens["reader"], key, match, body, client)
	}
	if err := h.verifyReplicaAuthority(api, reader, value.ID, "replica-authority-"+h.suffix); err != nil {
		return err
	}
	if err := h.replicaResources("before-load"); err != nil {
		return err
	}
	var evidence []replicaEvidence
	for index, request := range []replicaRequest{api, ui} {
		batch, err := h.replicaLoad(request, fmt.Sprintf("initial-%d", index))
		if err != nil {
			return err
		}
		evidence = append(evidence, batch...)
	}
	if err := h.replicaResources("after-load"); err != nil {
		return err
	}
	// Prove session and durable command survival after every process is replaced,
	// not just after a new client connects to unchanged Pods.
	for _, role := range []string{"ui", "api", "worker"} {
		before, err := h.pods(role)
		if err != nil {
			return err
		}
		h.recordPlacement("before-replacement-"+role, before)
		if _, err := h.kubectl("roll three "+role+" replicas", "-n", namespace, "rollout", "restart", "deployment/catalog-"+role); err != nil {
			return err
		}
		if _, err := h.kubectl("wait for three "+role+" replacements", "-n", namespace, "rollout", "status", "deployment/catalog-"+role, "--timeout=3m"); err != nil {
			return err
		}
		after, err := h.pods(role)
		if err != nil || !allReplicaPodsReplaced(before, after) {
			return fmt.Errorf("%s rollout did not replace all three ready Pods", role)
		}
		h.recordPlacement("after-replacement-"+role, after)
	}
	apis, err = h.replicaAddresses("api")
	if err != nil {
		return err
	}
	uis, err = h.replicaAddresses("ui")
	if err != nil {
		return err
	}
	api, ui = h.replicaClients(apis, uis, cookie, client)
	for _, updated := range updates {
		for _, request := range []replicaRequest{api, ui} {
			if err := h.verifyReplicaUpdate(request, updated); err != nil {
				return err
			}
		}
	}
	if err := h.verifyReplicaIsolation(apis, updates, client); err != nil {
		return err
	}
	for _, entry := range evidence {
		if err := h.verifyReplicaEvidence(ui, entry); err != nil {
			return err
		}
	}
	for index, request := range []replicaRequest{api, ui} {
		if _, err := h.replicaLoad(request, fmt.Sprintf("recovered-%d", index)); err != nil {
			return err
		}
	}
	if err := h.replicaResources("after-recovery-load"); err != nil {
		return err
	}
	// New indexed sources must not change the preceding fixed three-record workload.
	if err := h.verifyReplicaPressure(apis, uis, cookie); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "three-ui-api-worker-replicas", "cross-replica-command-and-audit-integrity", "completed-key-body-and-precondition-rejection", "direct-api-current-authority-replay", "optimistic-concurrency-and-tenant-isolation", "shared-ui-session-and-durable-replay-after-all-process-replacement", "bounded-mixed-load-before-and-after-recovery")
	return nil
}

// verifyReplicaCommands races namespace creation and revision updates across three replicas, requiring one writer, exact replay and one winning update.
func (h *harness) verifyReplicaCommands(request replicaRequest, key string) (httpResult, error) {
	body := map[string]string{"slug": key, "displayName": "Concurrent namespace"}
	results := make([]httpResult, 3)
	functions := make([]func() error, 3)
	for index := range functions {
		functions[index] = func() error {
			var err error
			results[index], err = request(index, "POST", "/api/v1/namespaces", key, "", body)
			return err
		}
	}
	if err := runConcurrent(functions...); err != nil {
		return httpResult{}, err
	}
	var winner httpResult
	winnerIndex := -1
	writers := 0
	for index, result := range results {
		writer, err := replicaCreationWriter(result)
		if err != nil {
			return httpResult{}, err
		}
		if writer {
			winner = result
			winnerIndex = index
			writers++
		}
	}
	if writers != 1 {
		return httpResult{}, fmt.Errorf("concurrent creation has %d writers, want one", writers)
	}
	for _, result := range results {
		if result.Status == http.StatusCreated && (!bytes.Equal(result.Body, winner.Body) || result.Header.Get("ETag") != winner.Header.Get("ETag")) {
			return httpResult{}, errors.New("concurrent completed responses disagree")
		}
	}
	if err := verifyReplicaCommandBinding(request, winnerIndex, "POST", "/api/v1/namespaces", key, "", body, winner); err != nil {
		return httpResult{}, err
	}
	if err := h.verifyReplicaEvidence(request, replicaEvidence{Key: key, Body: body, Want: winner}); err != nil {
		return httpResult{}, err
	}
	var created catalog.Namespace
	if json.Unmarshal(winner.Body, &created) != nil || created.ID == "" {
		return httpResult{}, errors.New("invalid namespace race result")
	}
	path := "/api/v1/namespaces/" + created.ID
	patchBodies := make([]map[string]string, 3)
	for index := range functions {
		patchBodies[index] = map[string]string{"displayName": fmt.Sprintf("Winner %d", index), "reason": "Concurrent update qualification"}
		functions[index] = func() error {
			var err error
			results[index], err = request(index, "PATCH", path, fmt.Sprintf("%s-patch-%d", key, index), winner.Header.Get("ETag"), patchBodies[index])
			return err
		}
	}
	if err := runConcurrent(functions...); err != nil {
		return httpResult{}, err
	}
	updates := 0
	updateIndex := -1
	var updated httpResult
	for index, result := range results {
		if result.Status == http.StatusOK {
			var value catalog.Namespace
			if json.Unmarshal(result.Body, &value) != nil || value.ID != created.ID || value.TenantID != created.TenantID || value.Slug != created.Slug || value.Description != created.Description || value.DisplayName != fmt.Sprintf("Winner %d", index) || value.Revision != 2 {
				return httpResult{}, errors.New("winning update did not retain the requested change")
			}
			updates++
			updated = result
			updateIndex = index
		} else if !replicaProblem(result, http.StatusPreconditionFailed, catalog.CodePreconditionFailed) {
			return httpResult{}, fmt.Errorf("concurrent update returned %d instead of 200/412", result.Status)
		}
	}
	if updates != 1 || updated.Header.Get("ETag") != `"rev-2"` {
		return httpResult{}, errors.New("optimistic concurrency did not retain exactly one update")
	}
	if err := verifyReplicaCommandBinding(request, updateIndex, "PATCH", path, fmt.Sprintf("%s-patch-%d", key, updateIndex), `"rev-1"`, patchBodies[updateIndex], updated); err != nil {
		return httpResult{}, err
	}
	return updated, h.verifyReplicaUpdate(request, updated)
}

// verifyReplicaCommandBinding rejects changed completed requests away from the
// writer, then replays the original on every replica, including its old If-Match.
func verifyReplicaCommandBinding(request replicaRequest, writer int, method, path, key, match string, body map[string]string, want httpResult) error {
	changed := maps.Clone(body)
	changed["displayName"] += " changed"
	rejected, err := request((writer+1)%3, method, path, key, match, changed)
	if err != nil || !replicaProblem(rejected, http.StatusConflict, catalog.CodeIdempotencyKeyReused) {
		return errors.New("changed completed command body was not rejected")
	}
	if method == http.MethodPatch {
		rejected, err = request((writer+1)%3, method, path, key, `"rev-999"`, body)
		if err != nil || !replicaProblem(rejected, http.StatusConflict, catalog.CodeIdempotencyKeyReused) {
			return errors.New("changed completed command precondition was not rejected")
		}
	}
	for offset := 1; offset <= 3; offset++ {
		replay, err := request((writer+offset)%3, method, path, key, match, body)
		if err != nil || replay.Status != want.Status || replay.Header.Get("Idempotency-Replayed") != "true" || replay.Header.Get("ETag") != want.Header.Get("ETag") || !bytes.Equal(replay.Body, want.Body) {
			return errors.New("original completed command did not replay across replicas")
		}
	}
	return nil
}

// replicaProblem excludes cached successes and requires the specific rejection.
func replicaProblem(result httpResult, status int, code catalog.ErrorCode) bool {
	var problem struct {
		Status int    `json:"status"`
		Code   string `json:"code"`
	}
	return result.Status == status && result.Header.Get("Idempotency-Replayed") == "" && json.Unmarshal(result.Body, &problem) == nil && problem.Status == status && problem.Code == string(code)
}

// Classify only the documented completed/replayed or transient command response.
// The caller must still prove one writer and exact replay on every replica.
func replicaCreationWriter(result httpResult) (bool, error) {
	replayed := result.Header.Get("Idempotency-Replayed")
	if result.Status == http.StatusCreated && (replayed == "" || replayed == "true") {
		return replayed == "", nil
	}
	if result.Status == http.StatusServiceUnavailable && result.Header.Get("Retry-After") == "1" && replayed == "" {
		var problem struct {
			Code   string `json:"code"`
			Status int    `json:"status"`
		}
		if json.Unmarshal(result.Body, &problem) == nil && problem.Code == string(catalog.CodeDependencyUnavailable) && problem.Status == http.StatusServiceUnavailable {
			return false, nil
		}
	}
	return false, fmt.Errorf("invalid concurrent creation response contract: status %d", result.Status)
}

// verifyReplicaUpdate requires all three replicas to read the winning revision and exactly one matching update audit record.
func (h *harness) verifyReplicaUpdate(request replicaRequest, updated httpResult) error {
	var value catalog.Namespace
	if json.Unmarshal(updated.Body, &value) != nil || value.ID == "" || value.TenantID != primaryTenantID || value.Revision != 2 || updated.Header.Get("ETag") != `"rev-2"` {
		return errors.New("invalid winning update evidence")
	}
	for index := range 3 {
		actual, err := request(index, "GET", "/api/v1/namespaces/"+value.ID, "", "", nil)
		if err != nil || !sameNamespaceState(actual, updated) {
			return errors.New("replicas disagree on winning update")
		}
	}
	return h.replicaAuditOnce(request, value.ID, "namespace", "namespace.updated")
}

// verifyReplicaIsolation requires another tenant's reads of each created namespace to return 404 on every API tunnel.
func (h *harness) verifyReplicaIsolation(apis []string, updates []httpResult, client *http.Client) error {
	for _, updated := range updates {
		var value catalog.Namespace
		if json.Unmarshal(updated.Body, &value) != nil || value.ID == "" {
			return errors.New("invalid tenant isolation evidence")
		}
		for _, address := range apis {
			denied, err := h.request("GET", address+"/api/v1/namespaces/"+value.ID, h.tokens["other"], "", "", nil, client)
			if err != nil || denied.Status != http.StatusNotFound {
				return errors.New("cross-tenant namespace read was not concealed on every API replica")
			}
		}
	}
	return nil
}

// A completed rollout is not sufficient evidence if even one old process stays.
func allReplicaPodsReplaced(before, after []podIdentity) bool {
	if len(before) != 3 || len(after) != 3 {
		return false
	}
	seen := make(map[string]bool, 6)
	for _, group := range [][]podIdentity{before, after} {
		for _, pod := range group {
			if pod.UID == "" || !pod.Ready || seen[pod.UID] {
				return false
			}
			seen[pod.UID] = true
		}
	}
	return true
}

// Ignore timestamp precision on readback; replay below is separately byte-exact.
func sameNamespaceState(actual, want httpResult) bool {
	var left, right catalog.Namespace
	if actual.Status != http.StatusOK || json.Unmarshal(actual.Body, &left) != nil || json.Unmarshal(want.Body, &right) != nil || right.ID == "" {
		return false
	}
	return left.ID == right.ID && left.TenantID == primaryTenantID && left.TenantID == right.TenantID && left.Slug == right.Slug && left.DisplayName == right.DisplayName && left.Description == right.Description && left.Revision == right.Revision && actual.Header.Get("ETag") == want.Header.Get("ETag")
}

// verifyReplicaEvidence checks committed namespace state, byte-exact command replay and one creation audit record across three replicas.
func (h *harness) verifyReplicaEvidence(request replicaRequest, entry replicaEvidence) error {
	var created catalog.Namespace
	if entry.Want.Status != http.StatusCreated || entry.Want.Header.Get("ETag") != `"rev-1"` || json.Unmarshal(entry.Want.Body, &created) != nil || created.ID == "" || created.TenantID != primaryTenantID || created.Slug != entry.Body["slug"] || created.DisplayName != entry.Body["displayName"] || created.Revision != 1 {
		return errors.New("write did not retain requested namespace state")
	}
	for index := range 3 {
		replay, err := request(index, "POST", "/api/v1/namespaces", entry.Key, "", entry.Body)
		if err != nil || replay.Status != http.StatusCreated || replay.Header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(replay.Body, entry.Want.Body) || replay.Header.Get("ETag") != entry.Want.Header.Get("ETag") {
			return errors.New("replica did not replay exact committed response")
		}
		read, err := request(index, "GET", "/api/v1/namespaces/"+created.ID, "", "", nil)
		if err != nil || !sameNamespaceState(read, entry.Want) {
			return errors.New("replica readback lost or changed committed namespace")
		}
	}
	return h.replicaAuditOnce(request, created.ID, "namespace", "namespace.created")
}

// replicaAuditOnce uses a target-filtered, limit-two audit read to reject missing, duplicated or mismatched mutation evidence.
func (h *harness) replicaAuditOnce(request replicaRequest, id, targetType, action string) error {
	now := time.Now().UTC()
	query := url.Values{"action": {action}, "targetType": {targetType}, "targetId": {id}, "limit": {"2"}, "occurredFrom": {now.Add(-time.Hour).Format(time.RFC3339Nano)}, "occurredBefore": {now.Add(time.Minute).Format(time.RFC3339Nano)}}
	result, err := request(0, "GET", "/api/v1/audit-events?"+query.Encode(), "", "", nil)
	var page struct {
		Items []struct {
			TargetID string `json:"targetId"`
			Action   string `json:"action"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err != nil || result.Status != http.StatusOK || json.Unmarshal(result.Body, &page) != nil || len(page.Items) != 1 || page.NextCursor != "" || page.Items[0].TargetID != id || page.Items[0].Action != action {
		return errors.New("mutation audit missing, duplicated or mismatched")
	}
	return nil
}

// summarizeReplicaBatch reports nearest-rank latencies and observed throughput for the fixed one-write-per-ten-request mix, including tunnel overhead.
func summarizeReplicaBatch(phase string, durations []time.Duration, elapsed time.Duration) replicaBatchReport {
	ordered := slices.Clone(durations)
	slices.Sort(ordered)
	percentile := func(p float64) float64 {
		if len(ordered) == 0 {
			return 0
		}
		return float64(ordered[int(math.Ceil(p*float64(len(ordered))))-1]) / float64(time.Millisecond)
	}
	rate := float64(0)
	if elapsed > 0 {
		rate = float64(len(ordered)) / elapsed.Seconds()
	}
	return replicaBatchReport{Phase: phase, Requests: len(ordered), Writes: len(ordered) / 10, Concurrency: replicaConcurrency, Seconds: elapsed.Seconds(), PerSecond: rate, P50Millis: percentile(.50), P95Millis: percentile(.95), P99Millis: percentile(.99)}
}

// replicaLoad runs the bounded six-client search/write mix, validates unchanged search results and checks durable evidence for every write before reporting timing.
func (h *harness) replicaLoad(request replicaRequest, phase string) ([]replicaEvidence, error) {
	fmt.Printf("[replicas] mixed batch %s: %d requests, concurrency %d\n", phase, replicaRequests, replicaConcurrency)
	baseline, err := request(0, "POST", "/api/v1/catalog/search", "", "", map[string]int{"limit": 20})
	if err != nil || !validInstrumentationSearch(baseline) {
		return nil, errors.New("replica load requires the complete three-kind governed seed")
	}
	durations := make([]time.Duration, replicaRequests)
	outcomes := make([]error, replicaRequests)
	writes := make([]replicaEvidence, replicaRequests/10)
	started := time.Now()
	var wait sync.WaitGroup
	for worker := range replicaConcurrency {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := worker; index < replicaRequests; index += replicaConcurrency {
				if time.Since(started) > time.Minute || h.ctx.Err() != nil {
					outcomes[index] = errors.New("replica batch exceeded its bound")
					return
				}
				begin := time.Now()
				if index%10 == 0 {
					key := fmt.Sprintf("%s-%s-%d", h.suffix, phase, index)
					body := map[string]string{"slug": key, "displayName": "Replica load " + key}
					result, err := request(index%3, "POST", "/api/v1/namespaces", key, "", body)
					if err != nil || result.Status != http.StatusCreated {
						outcomes[index] = fmt.Errorf("mixed write failed: status %d", result.Status)
					} else {
						writes[index/10] = replicaEvidence{Key: key, Body: body, Want: result}
					}
				} else {
					result, err := request(index%3, "POST", "/api/v1/catalog/search", "", "", map[string]int{"limit": 20})
					if err != nil || result.Status != http.StatusOK || !bytes.Equal(result.Body, baseline.Body) {
						outcomes[index] = fmt.Errorf("mixed search failed or changed: status %d", result.Status)
					}
				}
				durations[index] = time.Since(begin)
			}
		}()
	}
	wait.Wait()
	elapsed := time.Since(started)
	if err := errors.Join(outcomes...); err != nil {
		return nil, err
	}
	for _, entry := range writes {
		if err := h.verifyReplicaEvidence(request, entry); err != nil {
			return nil, err
		}
	}
	h.report.ReplicaBatches = append(h.report.ReplicaBatches, summarizeReplicaBatch(phase, durations, elapsed))
	return writes, nil
}

// Read kubelet summaries directly: no metrics-server or monitoring dependency.
// Only fixed workload resource counters survive in the report, never Pod env.
func (h *harness) replicaResources(phase string) error {
	roles := make(map[string]string)
	for _, role := range []string{"api", "worker", "ui"} {
		pods, err := h.pods(role)
		if err != nil || len(pods) != 3 {
			return errors.New("resource check requires three ready Pods per role")
		}
		for _, pod := range pods {
			if !pod.Ready {
				return errors.New("resource check found an unready Pod")
			}
			roles[pod.Name] = role
		}
	}
	// Kubelet stats are refreshed asynchronously, separately from readiness.
	// Collect container counters rather than the possibly empty Pod aggregate.
	deadline := time.Now().Add(30 * time.Second)
	for {
		var samples []replicaResourceReport
		for _, node := range h.report.Nodes {
			raw, err := h.kubectl("sample replica container resources", "get", "--raw", "/api/v1/nodes/"+node.Name+"/proxy/stats/summary")
			if err != nil {
				return err
			}
			parsed, err := replicaResourceSamples(raw, roles, phase)
			if err != nil {
				return err
			}
			samples = append(samples, parsed...)
		}
		if len(samples) == 9 {
			h.report.ReplicaResources = append(h.report.ReplicaResources, samples...)
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("kubelet reported %d of nine complete container resource samples", len(samples))
		}
		select {
		case <-h.ctx.Done():
			return h.ctx.Err()
		case <-time.After(time.Second):
		}
	}
	raw, err := h.kubectl("check unexpected replica restarts", "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/instance="+releaseName, "-o", "json")
	if err != nil {
		return err
	}
	var inventory struct {
		Items []struct {
			Status struct {
				Containers []struct {
					Restarts int `json:"restartCount"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &inventory) != nil {
		return errors.New("invalid Pod restart inventory")
	}
	for _, pod := range inventory.Items {
		for _, container := range pod.Status.Containers {
			if container.Restarts != 0 {
				return errors.New("replica suffered an unexpected container restart")
			}
		}
	}
	return nil
}

// replicaResourceSamples projects nonzero single-container counters only for the requested test Pods, rejecting duplicate or multi-container inventory.
func replicaResourceSamples(raw []byte, roles map[string]string, phase string) ([]replicaResourceReport, error) {
	var summary struct {
		Pods []struct {
			PodRef struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"podRef"`
			Containers []struct {
				CPU struct {
					Total uint64 `json:"usageCoreNanoSeconds"`
				} `json:"cpu"`
				Memory struct {
					WorkingSet uint64 `json:"workingSetBytes"`
				} `json:"memory"`
			} `json:"containers"`
		} `json:"pods"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return nil, errors.New("invalid kubelet resource summary")
	}
	seen := make(map[string]bool)
	samples := make([]replicaResourceReport, 0, len(roles))
	for _, pod := range summary.Pods {
		role, ok := roles[pod.PodRef.Name]
		if !ok || pod.PodRef.Namespace != namespace {
			continue
		}
		if seen[pod.PodRef.Name] || len(pod.Containers) > 1 {
			return nil, errors.New("unexpected container resource inventory")
		}
		seen[pod.PodRef.Name] = true
		if len(pod.Containers) == 0 || pod.Containers[0].Memory.WorkingSet == 0 || pod.Containers[0].CPU.Total == 0 {
			continue
		}
		container := pod.Containers[0]
		samples = append(samples, replicaResourceReport{Phase: phase, Pod: pod.PodRef.Name, Role: role, WorkingSet: container.Memory.WorkingSet, CPUCoreNanos: container.CPU.Total})
	}
	return samples, nil
}
