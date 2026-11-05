//go:build minikube

// Topology checks exercise network restrictions, leases, disruption budgets and replica loss.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// runTopology runs the owned two-worker topology's network, cross-replica command, lease recovery and disruption checks.
func (h *harness) runTopology() error {
	if err := h.applyNetworkPolicies(); err != nil {
		return err
	}
	before := make(map[string][]podIdentity)
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return err
		}
		before[role] = pods
	}
	if err := h.verifyUI(); err != nil {
		return err
	}
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return err
		}
		if !sameUIDs(before[role], pods) {
			return fmt.Errorf("UI-only policy changes rolled %s Pods", role)
		}
	}
	h.report.Checks = append(h.report.Checks, "ui-policy-preserves-backend-pods")
	if err := h.verifyServiceRouteCalibration(); err != nil {
		return err
	}
	address, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	baseURL := "http://" + address
	fixtureAddress, err := h.startPortForward("service/federation-fixture", 8443)
	if err != nil {
		return err
	}
	caPath := filepath.Join(h.temporary, "fixture", "trust", "functional-ca.pem")
	sourceID, err := h.runFederationWorkflow(baseURL, "https://"+fixtureAddress, caPath)
	if err != nil {
		return err
	}
	if err := h.verifyDisruptionBudgets(); err != nil {
		return err
	}
	// Reopen the Service tunnel after the selected API disappears during drain.
	address, err = h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	baseURL = "http://" + address
	if err := h.verifyDeniedDecoy(baseURL, caPath); err != nil {
		return err
	}
	if err := h.verifyCrossReplicaIdempotency(); err != nil {
		return err
	}
	if err := h.verifyWorkerLeaseRecovery(baseURL, "https://"+fixtureAddress, caPath, sourceID); err != nil {
		return err
	}
	if err := h.verifyDirectPodLoss(); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "multi-replica-topology-disruption-and-isolation")
	return nil
}

// applyNetworkPolicies restricts the test API and worker Pods to DNS and their fixed dependency ports; the decoy remains outside the permitted peers.
func (h *harness) applyNetworkPolicies() error {
	for role, rules := range runtimeEgressRules(h.options.scenario == "pgbouncer" && !h.pgbouncerDirect) {
		policy := kubernetesObject{
			"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
			"metadata": map[string]any{"name": "catalog-" + role + "-egress", "namespace": namespace, "labels": testLabels("network-policy")},
			"spec": map[string]any{
				"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/instance": releaseName, "app.kubernetes.io/component": role}},
				"policyTypes": []string{"Egress"}, "egress": rules,
			},
		}
		if err := h.applyObject("apply "+role+" default-deny egress policy", policy); err != nil {
			return err
		}
	}
	time.Sleep(2 * time.Second)
	if _, err := h.kubectl("verify NetworkPolicies select application Pods", "-n", namespace, "get", "networkpolicy", "catalog-api-egress", "catalog-worker-egress"); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "calico-role-egress-policies")
	return nil
}

// runtimeEgressRules selects exact fixture destinations without granting access to the decoy.
func runtimeEgressRules(usePooler bool) map[string][]any {
	dnsRule := map[string]any{
		"to": []any{map[string]any{
			"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
			"podSelector":       map[string]any{"matchLabels": map[string]string{"k8s-app": "kube-dns"}},
		}},
		"ports": []any{map[string]any{"protocol": "UDP", "port": 53}, map[string]any{"protocol": "TCP", "port": 53}},
	}
	peerRule := func(component string, port int) map[string]any {
		return map[string]any{
			"to":    []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": "open-agentic-catalog-e2e", "app.kubernetes.io/component": component}}}},
			"ports": []any{map[string]any{"protocol": "TCP", "port": port}},
		}
	}
	runtimeRules := map[string][]any{
		"api":    {dnsRule, peerRule("postgres", 5432), peerRule("identity", 8081), peerRule("identity", 8443)},
		"worker": {dnsRule, peerRule("postgres", 5432), peerRule("identity", 8443)},
	}
	if usePooler {
		poolerRule := map[string]any{"to": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/instance": releaseName, "app.kubernetes.io/component": "pgbouncer"}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 6432}}}
		for role, rules := range runtimeRules {
			rules[1] = poolerRule
			runtimeRules[role] = rules
		}
	}
	return runtimeRules
}

// runFederationWorkflow builds the local response validator and runs the controlled federation workflow, returning its retained source ID.
func (h *harness) runFederationWorkflow(baseURL, fixtureURL, caPath string) (string, error) {
	validator := filepath.Join(h.temporary, "functional-contracts")
	buildContext, cancelBuild := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancelBuild()
	if _, err := h.runWithInput(buildContext, "build owned response validator", nil, "go", "build", "-mod=readonly", "-trimpath", "-o", validator, "./test/functional/contracts"); err != nil {
		return "", err
	}
	fmt.Printf("[%s] exercise allowed Service and DNS federation path\n", h.options.scenario)
	command := h.federationCommand(baseURL, fixtureURL, caPath, validator)
	buffer := &boundedBuffer{limit: maximumCommandOutput}
	command.Stdout, command.Stderr = buffer, buffer
	if err := command.Run(); err != nil {
		h.captureFederationCheckpoint()
		h.diagnoseFederationRoute()
		location, _ := os.ReadFile(filepath.Join(h.temporary, "scenario-failure.json")) // #nosec G304 -- Fixed diagnostic name inside the invocation-owned directory.
		return "", fmt.Errorf("federation workflow%s: %w\n%s", federationFailureLocation(location), err, h.redactDiagnostic(buffer.String()))
	}
	var report struct {
		SourceID string `json:"sourceId"`
	}
	if err := json.Unmarshal(buffer.bytes, &report); err != nil || report.SourceID == "" {
		return "", fmt.Errorf("decode federation report: %w", err)
	}
	h.report.Checks = append(h.report.Checks, "allowed-in-cluster-federation-and-job-deduplication")
	return report.SourceID, nil
}

// federationCommand prepares the Python fixture workflow with an environment-injected token, local validator and invocation-owned failure-state directory.
func (h *harness) federationCommand(baseURL, fixtureURL, caPath, validator string) *exec.Cmd {
	command := exec.CommandContext(h.ctx, "python3", "test/functional/federation.py", "--base-url", baseURL, "--fixture-url", fixtureURL, "--ca-file", caPath, "--run-id", "minikube-"+h.suffix) // #nosec G204 -- all arguments belong to this invocation.
	command.Dir = h.options.repositoryRoot
	command.Env = append(h.environment(), "CATALOG_FUNCTIONAL_ADMIN_TOKEN="+h.tokens["admin"], "CATALOG_FUNCTIONAL_CONTRACT_DRIVER="+validator, "CATALOG_FUNCTIONAL_STATE_DIRECTORY="+h.temporary, "PYTHONOPTIMIZE=0")
	return command
}

// Retain only reviewed source line numbers, never Python exception messages or
// private request/response values. Invalid diagnostic files add no disclosure.
func federationFailureLocation(raw []byte) string {
	if len(raw) > 1024 {
		return ""
	}
	var location struct {
		Scenario string `json:"scenario"`
		Lines    []int  `json:"lines"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&location) != nil || location.Scenario != "federation" || len(location.Lines) < 1 || len(location.Lines) > 3 {
		return ""
	}
	for _, line := range location.Lines {
		if line < 1 || line > 10000 {
			return ""
		}
	}
	return fmt.Sprintf(" (federation.py lines %v)", location.Lines)
}

// verifyDeniedDecoy requires the network-blocked but application-allowlisted origin to fail within the test bound without receiving a manifest request.
func (h *harness) verifyDeniedDecoy(baseURL, caPath string) error {
	address, err := h.startPortForward("service/decoy-fixture", 8443)
	if err != nil {
		return err
	}
	ca, err := os.ReadFile(caPath) // #nosec G304 -- this is the invocation-owned fixture CA path.
	if err != nil {
		return err
	}
	client, err := fixtureHTTPClient(ca)
	if err != nil {
		return err
	}
	stateURL := "https://" + address + "/state"
	before, err := h.request(http.MethodGet, stateURL, "", "", "", nil, client)
	if err != nil || before.Status != http.StatusOK {
		return fmt.Errorf("read decoy state before denial: %w", err)
	}
	started := time.Now()
	result, requestErr := h.request(http.MethodPost, baseURL+"/api/v1/federation/source-inspections", h.tokens["admin"], "minikube-decoy-"+h.suffix, "", map[string]any{
		"url": "https://decoy-fixture:8443/.well-known/ard.json", "networkProfile": "functional-fixture",
	}, nil)
	duration := time.Since(started)
	if requestErr != nil {
		return fmt.Errorf("decoy inspection did not return a bounded HTTP failure: %w", requestErr)
	}
	if result.Status != http.StatusServiceUnavailable || duration > 12*time.Second {
		return fmt.Errorf("decoy inspection was not denied at the network boundary: status=%d duration=%s", result.Status, duration)
	}
	after, err := h.request(http.MethodGet, stateURL, "", "", "", nil, client)
	if err != nil || after.Status != http.StatusOK {
		return fmt.Errorf("read decoy state after denial: %w", err)
	}
	var beforeState, afterState struct {
		ManifestRequests int `json:"manifestRequests"`
	}
	if json.Unmarshal(before.Body, &beforeState) != nil || json.Unmarshal(after.Body, &afterState) != nil || beforeState.ManifestRequests != afterState.ManifestRequests {
		return fmt.Errorf("network-denied decoy received an application request: before=%s after=%s", before.Body, after.Body)
	}
	h.report.Checks = append(h.report.Checks, "networkpolicy-denied-allowed-origin-decoy")
	return nil
}

// verifyCrossReplicaIdempotency races one namespace command through two API Pod tunnels, then requires one retained effect, one replay and one audit entry.
func (h *harness) verifyCrossReplicaIdempotency() error {
	pods, err := h.pods("api")
	if err != nil {
		return err
	}
	if len(pods) != 2 {
		return fmt.Errorf("cross-replica check requires two API Pods, got %d", len(pods))
	}
	addresses := make([]string, 2)
	for index := range pods {
		addresses[index], err = h.startPortForward("pod/"+pods[index].Name, 8080)
		if err != nil {
			return err
		}
	}
	results := make([]httpResult, 2)
	functions := make([]func() error, 2)
	for index := range functions {
		functions[index] = func() error {
			response, requestErr := h.request(http.MethodPost, "http://"+addresses[index]+"/api/v1/namespaces", h.tokens["admin"], "minikube-cross-api-"+h.suffix, "", map[string]any{"slug": "cross-api-" + h.suffix, "displayName": "Cross API " + h.suffix}, nil)
			results[index] = response
			return requestErr
		}
	}
	if err := runConcurrent(functions...); err != nil {
		return err
	}
	pending := -1
	created := make([]httpResult, 0, 2)
	for index, result := range results {
		switch result.Status {
		case http.StatusCreated:
			created = append(created, result)
		case http.StatusServiceUnavailable:
			if pending >= 0 || result.Header.Get("Retry-After") != "1" || result.Header.Get("Idempotency-Replayed") != "" {
				return fmt.Errorf("cross-replica pending response is not retry-safe: status=%d retry-after=%q replay=%q body=%s", result.Status, result.Header.Get("Retry-After"), result.Header.Get("Idempotency-Replayed"), result.Body)
			}
			pending = index
		default:
			return fmt.Errorf("cross-replica command returned %d: %s", result.Status, result.Body)
		}
	}
	if pending >= 0 {
		// The documented active-reservation contract is a retryable 503, not a
		// blocking wait. Once the winning request has returned, the same request
		// against the other replica must replay the atomically retained response.
		retry, err := h.request(http.MethodPost, "http://"+addresses[pending]+"/api/v1/namespaces", h.tokens["admin"], "minikube-cross-api-"+h.suffix, "", map[string]any{"slug": "cross-api-" + h.suffix, "displayName": "Cross API " + h.suffix}, nil)
		if err != nil || retry.Status != http.StatusCreated || retry.Header.Get("Idempotency-Replayed") != "true" {
			return fmt.Errorf("cross-replica retry did not replay the committed response: status=%d replay=%q err=%w body=%s", retry.Status, retry.Header.Get("Idempotency-Replayed"), err, retry.Body)
		}
		created = append(created, retry)
	}
	if len(created) != 2 {
		return fmt.Errorf("cross-replica command produced %d completed responses, want 2", len(created))
	}
	var identifiers []string
	replayed := 0
	for _, result := range created {
		var namespace struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(result.Body, &namespace); err != nil || namespace.ID == "" {
			return fmt.Errorf("decode cross-replica namespace: %w", err)
		}
		identifiers = append(identifiers, namespace.ID)
		if result.Header.Get("Idempotency-Replayed") == "true" {
			replayed++
		}
	}
	if identifiers[0] != identifiers[1] || replayed != 1 {
		return fmt.Errorf("cross-replica command did not produce one effect and one replay: ids=%v replayed=%d", identifiers, replayed)
	}
	audit, err := h.request(http.MethodGet, "http://"+addresses[0]+"/api/v1/audit-events?action=namespace.created&limit=100", h.tokens["admin"], "", "", nil, nil)
	if err != nil || audit.Status != http.StatusOK {
		return fmt.Errorf("read cross-replica audit: status %d: %w", audit.Status, err)
	}
	var page struct {
		Items []struct {
			TargetID string `json:"targetId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(audit.Body, &page); err != nil {
		return err
	}
	count := 0
	for _, item := range page.Items {
		if item.TargetID == identifiers[0] {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("cross-replica command has %d audit records, want 1", count)
	}
	h.report.Checks = append(h.report.Checks, "database-coordinated-cross-api-replay")
	return nil
}

// verifyWorkerLeaseRecovery blocks fixture retrieval, replaces the sole owning worker and requires attempt two to complete after releasing the controlled database lock.
func (h *harness) verifyWorkerLeaseRecovery(baseURL, fixtureURL, caPath, sourceID string) error {
	if _, err := h.kubectl("scale to one owning worker", "-n", namespace, "scale", "deployment/catalog-worker", "--replicas=1"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for one owning worker", "-n", namespace, "rollout", "status", "deployment/catalog-worker", "--timeout=2m"); err != nil {
		return err
	}
	ca, err := os.ReadFile(caPath) // #nosec G304 -- this is the invocation-owned fixture CA path.
	if err != nil {
		return err
	}
	client, err := fixtureHTTPClient(ca)
	if err != nil {
		return err
	}
	control, err := h.request(http.MethodPost, fixtureURL+"/control", "", "", "", map[string]any{"mode": "replacement", "delayMillis": 0}, client)
	if err != nil || control.Status != http.StatusOK {
		return fmt.Errorf("reset lease-recovery fixture: status %d: %w", control.Status, err)
	}
	source, err := h.request(http.MethodGet, baseURL+"/api/v1/federation/sources/"+sourceID, h.tokens["admin"], "", "", nil, nil)
	if err != nil || source.Status != http.StatusOK {
		return fmt.Errorf("read source for lease recovery: status %d: %w", source.Status, err)
	}
	var sourceDocument struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(source.Body, &sourceDocument); err != nil || sourceDocument.Revision < 1 {
		return fmt.Errorf("decode source revision: %w", err)
	}
	// Block the first conditional-retrieval read without blocking the API's
	// source read and atomic Job enqueue. This gives the owning worker a stable
	// point at which to be terminated; its replacement can reclaim the expired
	// lease, heartbeat while blocked, and finish after the bounded lock ends.
	lockContext, cancelLock := context.WithCancel(h.ctx)
	lock := exec.CommandContext(lockContext, h.options.kubectl, "--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-U", "catalog_owner", "-d", "catalog", "-c", "BEGIN; LOCK TABLE source_retrieval_states IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(60); COMMIT") // #nosec G204 -- the executable and context are preflight-validated invocation-owned values.
	lock.Env, lock.Dir = h.environment(), h.options.repositoryRoot
	lockOutput := &boundedBuffer{limit: 32 << 10}
	lock.Stdout, lock.Stderr = lockOutput, lockOutput
	if err := lock.Start(); err != nil {
		cancelLock()
		return err
	}
	lockResult := make(chan struct{})
	var lockErr error
	go func() {
		lockErr = lock.Wait()
		close(lockResult)
	}()
	lockComplete := false
	defer func() {
		cancelLock()
		if !lockComplete {
			<-lockResult
		}
	}()
	lockPID, err := h.waitRetrievalLock(10 * time.Second)
	if err != nil {
		return err
	}
	syncResponse, err := h.request(http.MethodPost, baseURL+"/api/v1/federation/sources/"+sourceID+":sync", h.tokens["admin"], "minikube-lease-"+h.suffix, fmt.Sprintf(`"rev-%d"`, sourceDocument.Revision), map[string]any{}, nil)
	if err != nil || syncResponse.Status != http.StatusAccepted {
		return fmt.Errorf("queue lease recovery Job: status %d: %w: %s", syncResponse.Status, err, syncResponse.Body)
	}
	var envelope struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if err := json.Unmarshal(syncResponse.Body, &envelope); err != nil || envelope.Job.ID == "" {
		return fmt.Errorf("decode lease recovery Job: %w", err)
	}
	firstAttempt, err := h.waitJobState(baseURL, envelope.Job.ID, "running", 20*time.Second)
	if err != nil {
		return err
	}
	if attempt, _ := firstAttempt["attempt"].(float64); attempt != 1 {
		return fmt.Errorf("lease-recovery Job reached running with attempt %.0f, want 1", attempt)
	}
	workers, err := h.pods("worker")
	if err != nil {
		return fmt.Errorf("identify owning worker: %w", err)
	}
	if len(workers) != 1 {
		return fmt.Errorf("identify owning worker: found %d active Pods, want 1", len(workers))
	}
	if _, err := h.kubectl("stop the owning worker deployment", "-n", namespace, "scale", "deployment/catalog-worker", "--replicas=0"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for owning worker termination", "-n", namespace, "wait", "--for=delete", "pod/"+workers[0].Name, "--timeout=2m"); err != nil {
		return err
	}
	if workers, err = h.pods("worker"); err != nil || len(workers) != 0 {
		return fmt.Errorf("owning worker did not terminate before replacement: %#v, %w", workers, err)
	}
	if _, err := h.kubectl("start one replacement worker", "-n", namespace, "scale", "deployment/catalog-worker", "--replicas=1"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for replacement worker", "-n", namespace, "rollout", "status", "deployment/catalog-worker", "--timeout=2m"); err != nil {
		return err
	}
	// Keep the retrieval table locked until the new process has claimed the
	// expired lease. With the previous owner fully gone, attempt two is the only
	// handler that can resume after the barrier is released.
	if _, err := h.waitJobAttempt(baseURL, envelope.Job.ID, 2, "running", 30*time.Second); err != nil {
		return err
	}
	terminated, err := h.kubectl("release controlled retrieval lock", "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", fmt.Sprintf("SELECT pg_terminate_backend(%d)", lockPID))
	if err != nil || strings.TrimSpace(string(terminated)) != "t" {
		return fmt.Errorf("terminate controlled retrieval backend %d: %w: %s", lockPID, err, terminated)
	}
	select {
	case <-lockResult:
	case <-time.After(10 * time.Second):
		cancelLock()
		<-lockResult
		return fmt.Errorf("controlled retrieval lock process did not exit after backend termination: %w", lockErr)
	}
	lockComplete = true
	if lockErr == nil {
		return errors.New("controlled retrieval lock ended before it was released")
	}
	if _, err := h.kubectl("verify controlled retrieval lock release", "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", "SET lock_timeout='2s'; SELECT count(*) FROM source_retrieval_states"); err != nil {
		return err
	}
	job, err := h.waitJobState(baseURL, envelope.Job.ID, "succeeded", 60*time.Second)
	if err != nil {
		return err
	}
	attempt, _ := job["attempt"].(float64)
	result, _ := job["result"].(map[string]any)
	if attempt != 2 || result["type"] != "source_snapshot" || result["sourceId"] != sourceID || result["state"] != "complete" {
		return fmt.Errorf("recovered Job did not produce one successful takeover: attempt=%.0f result=%v", attempt, result)
	}
	replicas := h.options.runtimeReplicas()
	if _, err := h.kubectl("restore worker topology", "-n", namespace, "scale", "deployment/catalog-worker", "--replicas="+strconv.Itoa(replicas)); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for restored worker topology", "-n", namespace, "rollout", "status", "deployment/catalog-worker", "--timeout=2m"); err != nil {
		return err
	}
	workers, err = h.pods("worker")
	if err != nil || len(workers) != replicas || (h.options.scenario == "topology" && workers[0].Node == workers[1].Node) {
		return fmt.Errorf("worker topology did not recover spread: %#v, %w", workers, err)
	}
	h.report.Checks = append(h.report.Checks, "worker-lease-recovery-after-owner-loss")
	return nil
}

// waitRetrievalLock polls the isolated fixture database for the granted retrieval-table lock and returns its backend PID for controlled release.
func (h *harness) waitRetrievalLock(timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := h.kubectl("identify controlled retrieval lock", "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", "SELECT pid FROM pg_locks WHERE relation='source_retrieval_states'::regclass AND mode='AccessExclusiveLock' AND granted AND pid<>pg_backend_pid() ORDER BY pid LIMIT 1")
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr == nil && pid > 0 {
				return pid, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, errors.New("controlled source retrieval lock was not acquired")
}

// waitJobState polls authenticated Job reads until the requested state or test timeout, retaining the last document for failure evidence.
func (h *harness) waitJobState(baseURL, jobID, state string, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	var last map[string]any
	for time.Now().Before(deadline) {
		result, err := h.request(http.MethodGet, baseURL+"/api/v1/jobs/"+jobID, h.tokens["admin"], "", "", nil, nil)
		if err == nil && result.Status == http.StatusOK && json.Unmarshal(result.Body, &last) == nil && last["state"] == state {
			return last, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out waiting for Job %s state %s; last=%v", jobID, state, last)
}

// waitJobAttempt waits for an exact attempt and state, failing early if an attempt is skipped or an earlier attempt terminates.
func (h *harness) waitJobAttempt(baseURL, jobID string, attempt int, state string, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	var last map[string]any
	for time.Now().Before(deadline) {
		result, err := h.request(http.MethodGet, baseURL+"/api/v1/jobs/"+jobID, h.tokens["admin"], "", "", nil, nil)
		if err == nil && result.Status == http.StatusOK && json.Unmarshal(result.Body, &last) == nil {
			observedAttempt, _ := last["attempt"].(float64)
			if observedAttempt > float64(attempt) {
				return nil, fmt.Errorf("job %s advanced to attempt %.0f before expected attempt %d could be observed", jobID, observedAttempt, attempt)
			}
			if observedAttempt == float64(attempt) && last["state"] == state {
				return last, nil
			}
			if observedState, _ := last["state"].(string); observedAttempt < float64(attempt) && (observedState == "succeeded" || observedState == "failed" || observedState == "cancelled" || observedState == "dead_letter") {
				return nil, fmt.Errorf("job %s became %s at attempt %.0f before expected attempt %d", jobID, observedState, observedAttempt, attempt)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out waiting for Job %s attempt %d state %s; last=%v", jobID, attempt, state, last)
}

// verifyDisruptionBudgets checks API drain behavior and one-at-a-time voluntary eviction for the fixture's two-replica API and worker Deployments.
func (h *harness) verifyDisruptionBudgets() error {
	if err := h.verifyAPIDrainAndPDB(); err != nil {
		return err
	}
	if err := h.verifySimplePDB("worker"); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "api-and-worker-pdb-eviction")
	return nil
}

// verifySimplePDB evicts one of two fixture replicas, requires the second eviction to be budget-blocked and waits for rollout recovery.
func (h *harness) verifySimplePDB(role string) error {
	pods, err := h.pods(role)
	if err != nil || len(pods) != 2 {
		return fmt.Errorf("%s PDB requires two Pods: %w", role, err)
	}
	if err := h.evict(pods[0].Name, true); err != nil {
		return err
	}
	if err := h.waitPDBBlocked(role, 8*time.Second); err != nil {
		return err
	}
	if err := h.evict(pods[1].Name, false); err != nil {
		return err
	}
	_, err = h.kubectl("wait for "+role+" recovery after eviction", "-n", namespace, "rollout", "status", "deployment/catalog-"+role, "--timeout=2m")
	return err
}

// evict submits a namespaced Pod eviction and requires either success or a disruption-budget-specific denial, as requested.
func (h *harness) evict(pod string, shouldSucceed bool) error {
	return h.evictContext(h.ctx, pod, shouldSucceed)
}

func (h *harness) evictContext(ctx context.Context, pod string, shouldSucceed bool) error {
	eviction := kubernetesObject{"apiVersion": "policy/v1", "kind": "Eviction", "metadata": map[string]any{"name": pod, "namespace": namespace}}
	raw, err := json.Marshal(eviction)
	if err != nil {
		return err
	}
	endpoint := "/api/v1/namespaces/" + namespace + "/pods/" + pod + "/eviction"
	_, err = h.topologyCommand(ctx, "evict Pod "+pod, raw, "create", "--raw", endpoint, "-f", "-")
	if shouldSucceed && err != nil {
		return err
	}
	if !shouldSucceed && err == nil {
		return errors.New("second eviction was not denied by its PodDisruptionBudget")
	}
	if !shouldSucceed && !strings.Contains(err.Error(), "disruption budget") {
		return fmt.Errorf("second eviction was not denied by its PodDisruptionBudget: %w", err)
	}
	return nil
}

// waitPDBBlocked polls the role's fixture disruption budget until it allows zero further voluntary disruptions.
func (h *harness) waitPDBBlocked(role string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := h.kubectl("inspect "+role+" disruption allowance", "-n", namespace, "get", "pdb/catalog-"+role, "-o", `jsonpath={.status.disruptionsAllowed}`)
		if err == nil && strings.TrimSpace(string(raw)) == "0" {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s PDB did not report a blocked second disruption", role)
}

// verifyDirectPodLoss force-deletes one owned API and worker Pod outside eviction-budget enforcement and waits for each Deployment to recover.
func (h *harness) verifyDirectPodLoss() error {
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil || len(pods) != 2 || !pods[0].Ready || !pods[1].Ready {
			return fmt.Errorf("direct loss requires two ready %s Pods: %w", role, err)
		}
		if _, err := h.kubectl("directly delete one "+role+" Pod", "-n", namespace, "delete", "pod", pods[0].Name, "--grace-period=0", "--force", "--wait=false"); err != nil {
			return err
		}
		if _, err := h.kubectl("wait for "+role+" after direct loss", "-n", namespace, "rollout", "status", "deployment/catalog-"+role, "--timeout=2m"); err != nil {
			return err
		}
		if err := h.waitPodReplacement(role, pods); err != nil {
			return err
		}
	}
	address, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	if err := h.verifyDisruptionBusiness("http://"+address, "pod-loss"); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "direct-api-and-worker-pod-loss")
	return nil
}

// verifyWorkerNodeLoss stops one owned worker node and checks survivor liveness and hard-spread degradation; the node stays stopped until cluster cleanup.
func (h *harness) verifyWorkerNodeLoss() error {
	var stopped, survivor string
	for _, node := range h.report.Nodes {
		if !node.ControlPlane && stopped == "" {
			stopped = node.Name
		} else if !node.ControlPlane {
			survivor = node.Name
		}
	}
	if stopped == "" || survivor == "" {
		return errors.New("two worker nodes are required for node-loss testing")
	}
	var survivorAPI string
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil || len(pods) != 2 || pods[0].Node == pods[1].Node {
			return fmt.Errorf("%s was not spread before node loss: %#v, %w", role, pods, err)
		}
		if role == "api" {
			for _, pod := range pods {
				if pod.Node == survivor {
					survivorAPI = pod.Name
				}
			}
		}
	}
	if survivorAPI == "" {
		return errors.New("could not identify the API Pod on the surviving worker")
	}
	address, err := h.startPortForward("pod/"+survivorAPI, 8080)
	if err != nil {
		return err
	}
	baseURL := "http://" + address
	if err := h.verifyDisruptionBusiness(baseURL, "before-node-loss"); err != nil {
		return err
	}
	probeContext, cancel := context.WithCancel(h.ctx)
	defer cancel()
	var successes, failures atomic.Int64
	var wait sync.WaitGroup
	wait.Add(1)
	defer func() { cancel(); wait.Wait() }()
	go func() {
		defer wait.Done()
		for {
			select {
			case <-probeContext.Done():
				return
			default:
			}
			probe, err := h.request(http.MethodGet, baseURL+"/livez", "", "", "", nil, &http.Client{Timeout: time.Second})
			if err == nil && probe.Status == http.StatusOK {
				successes.Add(1)
			} else {
				failures.Add(1)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	if _, err := h.minikube("stop one worker node", "node", "stop", stopped); err != nil {
		return err
	}
	if err := h.waitNodeUnavailable(stopped, 2*time.Minute); err != nil {
		return err
	}
	if _, err := h.kubectl("remove Pods stranded on stopped worker", "-n", namespace, "delete", "pod", "--field-selector=spec.nodeName="+stopped, "--grace-period=0", "--force", "--wait=false"); err != nil {
		return err
	}
	for _, role := range []string{"api", "worker"} {
		pods, err := h.waitHardSpreadDegraded(role, survivor, 45*time.Second)
		if err != nil {
			return err
		}
		h.recordPlacement("node-loss-"+role, pods)
	}
	serviceAddress, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	if err := h.waitHTTP("http://" + serviceAddress + "/readyz"); err != nil {
		return fmt.Errorf("catalog Service did not route to the recovered topology: %w", err)
	}
	if err := h.verifyDisruptionBusiness("http://"+serviceAddress, "node-loss"); err != nil {
		return err
	}
	time.Sleep(time.Second)
	cancel()
	wait.Wait()
	if successes.Load() < 10 || failures.Load() != 0 {
		return fmt.Errorf("service continuity during worker-node loss: successes=%d failures=%d", successes.Load(), failures.Load())
	}
	for index := range h.report.Nodes {
		if h.report.Nodes[index].Name == stopped {
			h.report.Nodes[index].Ready = false
		}
	}
	h.report.Checks = append(h.report.Checks, "worker-node-loss-hard-spread-and-survivor-continuity")
	return nil
}

// waitHardSpreadDegraded requires the test's two-replica Deployment to retain one ready survivor and exactly one unschedulable replacement.
func (h *harness) waitHardSpreadDegraded(role, survivor string, timeout time.Duration) ([]podIdentity, error) {
	type podStatus struct {
		Metadata struct {
			DeletionTimestamp string `json:"deletionTimestamp"`
		} `json:"metadata"`
		Status struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"conditions"`
		} `json:"status"`
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		active, activeErr := h.pods(role)
		deploymentRaw, deploymentErr := h.kubectl("inspect "+role+" degraded availability", "-n", namespace, "get", "deployment/catalog-"+role, "-o", "json")
		podsRaw, podsErr := h.kubectl("inspect "+role+" hard-spread replacement", "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/instance="+releaseName+",app.kubernetes.io/component="+role, "-o", "json")
		var deployment struct {
			Spec struct {
				Replicas int `json:"replicas"`
			} `json:"spec"`
			Status struct {
				AvailableReplicas int `json:"availableReplicas"`
				ReadyReplicas     int `json:"readyReplicas"`
			} `json:"status"`
		}
		var podList struct {
			Items []podStatus `json:"items"`
		}
		unschedulable := 0
		if activeErr == nil && deploymentErr == nil && podsErr == nil && json.Unmarshal(deploymentRaw, &deployment) == nil && json.Unmarshal(podsRaw, &podList) == nil {
			for _, pod := range podList.Items {
				if pod.Metadata.DeletionTimestamp != "" || pod.Status.Phase != "Pending" {
					continue
				}
				for _, condition := range pod.Status.Conditions {
					if condition.Type == "PodScheduled" && condition.Status == "False" && condition.Reason == "Unschedulable" {
						unschedulable++
					}
				}
			}
			if deployment.Spec.Replicas == 2 && deployment.Status.AvailableReplicas == 1 && deployment.Status.ReadyReplicas == 1 && len(active) == 1 && active[0].Ready && active[0].Node == survivor && unschedulable == 1 {
				return active, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("%s did not retain one ready survivor and one hard-spread Pending replica", role)
}

// waitNodeUnavailable polls the stopped fixture node until its Ready condition is no longer true.
func (h *harness) waitNodeUnavailable(node string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := h.kubectl("inspect stopped worker node", "get", "node/"+node, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
		if err == nil && strings.TrimSpace(string(raw)) != "True" {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("worker node %s remained Ready after it was stopped", node)
}
