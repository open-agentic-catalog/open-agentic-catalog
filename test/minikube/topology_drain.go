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
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// topologyCommand keeps the fault-phase deadline independent of cluster cleanup.
func (h *harness) topologyCommand(ctx context.Context, label string, input []byte, args ...string) ([]byte, error) {
	return h.runWithInput(ctx, label, input, h.options.kubectl, append([]string{"--kubeconfig", h.kubeconfig, "--context", h.profile}, args...)...)
}

// waitTopology rejects observations that finish outside the phase deadline.
func waitTopology(ctx context.Context, observe func() (bool, error)) error {
	for ctx.Err() == nil {
		ok, err := observe()
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	return ctx.Err()
}

func (h *harness) drainSQL(ctx context.Context, query string) ([]byte, error) {
	return h.topologyCommand(ctx, "observe controlled database barrier", nil, "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", query)
}

// verifyAPIDrainAndPDB proves the selected read is blocked before eviction.
func (h *harness) verifyAPIDrainAndPDB() (resultErr error) {
	pods, err := h.pods("api")
	if err != nil || len(pods) != 2 || !pods[0].Ready || !pods[1].Ready {
		return fmt.Errorf("API drain requires two ready Pods: %w", err)
	}
	address, err := h.startPortForward("pod/"+pods[0].Name, 8080)
	if err != nil {
		return err
	}
	baseURL := "http://" + address
	if err := h.verifyDisruptionBusiness(baseURL, "before-drain"); err != nil {
		return err
	}
	serviceState, err := h.prepareServiceDisruption(baseURL, pods)
	if err != nil {
		return err
	}
	var retained catalog.Namespace
	if err := json.Unmarshal(h.disruptionEvidence.Want.Body, &retained); err != nil {
		return err
	}
	rawIP, err := h.kubectl("identify selected API address", "-n", namespace, "get", "pod", pods[0].Name, "-o", "jsonpath={.status.podIP}")
	ip := net.ParseIP(strings.TrimSpace(string(rawIP)))
	if err != nil || ip == nil {
		return errors.New("selected API has no valid Pod IP")
	}
	// Includes lock observation, blocked-request proof, eviction and all drain checks.
	ctx, cancel := context.WithTimeout(h.ctx, 25*time.Second)
	defer cancel()
	if err := h.waitDrainEndpoints(ctx, pods, false); err != nil {
		return err
	}
	if err := h.serviceEndpointEvidence(ctx, pods, false, "before", serviceState); err != nil {
		return err
	}
	lockName := "catalog-drain-" + h.suffix
	lockSQL := "BEGIN; SET LOCAL application_name='" + lockName + "'; SET LOCAL statement_timeout='30s'; LOCK TABLE namespaces IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(30); ROLLBACK"
	lockCtx, cancelLock := context.WithCancel(ctx)
	defer cancelLock()
	lock := exec.CommandContext(lockCtx, h.options.kubectl, "--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-c", lockSQL) // #nosec G204 -- Preflight-validated tool and owned fixture inputs.
	lock.Env, lock.Dir = h.environment(), h.options.repositoryRoot
	lock.WaitDelay = 2 * time.Second
	lockOutput := &boundedBuffer{limit: 32 << 10}
	lock.Stdout, lock.Stderr = lockOutput, lockOutput
	if err := lock.Start(); err != nil {
		return err
	}
	lockDone := make(chan struct{})
	go func() {
		_ = lock.Wait()
		close(lockDone)
	}()
	// Independent cleanup also runs when observation, eviction or response checks fail.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, cleanupErr := h.drainSQL(cleanupCtx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='"+lockName+"' AND usename='catalog_owner'")
		cancelLock()
		<-lockDone
		resultErr = errors.Join(resultErr, cleanupErr)
	}()
	lockPID := 0
	if err := waitTopology(ctx, func() (bool, error) {
		raw, err := h.drainSQL(ctx, "SELECT a.pid FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE a.application_name='"+lockName+"' AND l.relation='namespaces'::regclass AND l.mode='AccessExclusiveLock' AND l.granted")
		if err != nil {
			return false, err
		}
		lockPID, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		return lockPID > 0, nil
	}); err != nil {
		return err
	}
	type outcome struct {
		response httpResult
		err      error
	}
	completed := make(chan outcome, 1)
	requestCtx, cancelRequest := context.WithCancel(ctx)
	requestDone := make(chan struct{})
	defer func() { cancelRequest(); <-requestDone }()
	go func() {
		defer close(requestDone)
		response, requestErr := h.requestContext(requestCtx, "GET", baseURL+"/api/v1/namespaces/"+retained.ID, h.tokens["admin"], "", "", nil, nil)
		completed <- outcome{response, requestErr}
	}()
	// Exact query, selected Pod address, runtime role and blocker bind the observation.
	blockedSQL := fmt.Sprintf("SELECT count(*) FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE a.usename='catalog_api' AND a.client_addr='%s'::inet AND a.state='active' AND a.wait_event_type='Lock' AND a.query='SELECT id,tenant_id,slug,display_name,description,revision,created_at,updated_at FROM namespaces WHERE tenant_id=$1 AND id=$2' AND l.relation='namespaces'::regclass AND NOT l.granted AND %d=ANY(pg_blocking_pids(a.pid))", ip.String(), lockPID)
	if err := waitTopology(ctx, func() (bool, error) {
		select {
		case <-completed:
			return false, errors.New("selected request finished before eviction")
		default:
		}
		raw, err := h.drainSQL(ctx, blockedSQL)
		return strings.TrimSpace(string(raw)) == "1", err
	}); err != nil {
		return err
	}
	select {
	case <-completed:
		return errors.New("selected request finished before eviction")
	default:
	}
	// Cordon only the owned selected worker to hold its hard-spread replacement
	// Pending. This preserves a measurable degraded interval without changing PDBs.
	if _, err := h.topologyCommand(ctx, "cordon owned API replacement node", nil, "cordon", pods[0].Node); err != nil {
		return err
	}
	uncordoned := false
	defer func() {
		if !uncordoned {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanupCancel()
			_, cleanupErr := h.topologyCommand(cleanupCtx, "restore owned API scheduling", nil, "uncordon", pods[0].Node)
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()
	if err := h.evictContext(ctx, pods[0].Name, true); err != nil {
		return err
	}
	if err := h.waitDrainEndpoints(ctx, pods, true); err != nil {
		return err
	}
	if err := waitTopology(ctx, func() (bool, error) {
		raw, err := h.topologyCommand(ctx, "inspect API disruption allowance", nil, "-n", namespace, "get", "pdb/catalog-api", "-o", "jsonpath={.status.disruptionsAllowed}")
		return strings.TrimSpace(string(raw)) == "0", err
	}); err != nil {
		return err
	}
	if err := h.evictContext(ctx, pods[1].Name, false); err != nil {
		return err
	}
	if err := h.serviceEndpointEvidence(ctx, pods, true, "degraded", serviceState); err != nil {
		return err
	}
	if err := h.verifyDegradedService(ctx, pods, serviceState); err != nil {
		return err
	}
	if _, err := h.drainSQL(ctx, fmt.Sprintf("SELECT pg_terminate_backend(%d)", lockPID)); err != nil {
		return err
	}
	select {
	case value := <-completed:
		if value.err != nil {
			return fmt.Errorf("selected drain request transport failed: %w", value.err)
		}
		serviceState.report.SelectedReadStatus = value.response.Status
		if err := validateDrainResponse(value.response, h.disruptionEvidence.Want, "/api/v1/namespaces/"+retained.ID); err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	h.report.Checks = append(h.report.Checks, "api-drain-selected-lock-request-endpoint-and-response")
	recoveryCtx, recoveryCancel := context.WithTimeout(h.ctx, 120*time.Second)
	defer recoveryCancel()
	if _, err := h.topologyCommand(recoveryCtx, "restore owned API scheduling", nil, "uncordon", pods[0].Node); err != nil {
		return err
	}
	uncordoned = true
	if _, err := h.topologyCommand(recoveryCtx, "wait for API recovery after eviction", nil, "-n", namespace, "rollout", "status", "deployment/catalog-api", "--timeout=2m"); err != nil {
		return err
	}
	if err := h.waitPodReplacementContext(recoveryCtx, "api", pods); err != nil {
		return err
	}
	if err := h.serviceEndpointEvidence(recoveryCtx, []podIdentity{pods[0], pods[1]}, true, "recovered", serviceState); err != nil {
		return err
	}
	return h.finishServiceDisruption(recoveryCtx, serviceState)
}

func validateDrainResponse(result, want httpResult, path string) error {
	if result.Status == http.StatusOK && sameNamespaceState(result, want) {
		return nil
	}
	var problem map[string]any
	if result.Status != 503 || result.Header.Get("Content-Type") != "application/problem+json" || json.Unmarshal(result.Body, &problem) != nil || len(problem) != 7 ||
		problem["type"] != "https://catalog.example.com/problems/request-deadline-exceeded" || problem["title"] != "Request deadline exceeded" || problem["status"] != float64(503) || problem["code"] != "REQUEST_DEADLINE_EXCEEDED" || problem["detail"] != "the request exceeded the service work deadline" || problem["instance"] != path || result.Header.Get("X-Request-ID") == "" || problem["requestId"] != result.Header.Get("X-Request-ID") {
		return errors.New("drain did not return retained namespace or safe correlated work-deadline problem")
	}
	return nil
}

// drainEndpoints rejects empty/wrong inventories and requires the original survivor.
func drainEndpoints(raw []byte, selected, survivor podIdentity, removed bool) bool {
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Endpoints []struct {
				Conditions struct {
					Ready       *bool `json:"ready"`
					Terminating bool  `json:"terminating"`
				} `json:"conditions"`
				TargetRef struct {
					Kind      string `json:"kind"`
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
					UID       string `json:"uid"`
				} `json:"targetRef"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil || len(list.Items) == 0 || selected.UID == "" || survivor.UID == "" || selected.UID == survivor.UID {
		return false
	}
	selectedReady, survivorReady := false, false
	for _, item := range list.Items {
		if item.Metadata.Namespace != namespace || item.Metadata.Labels["kubernetes.io/service-name"] != "catalog" {
			return false
		}
		for _, ep := range item.Endpoints {
			if ep.TargetRef.Kind != "Pod" || ep.TargetRef.Namespace != namespace {
				continue
			}
			ready := ep.Conditions.Ready != nil && *ep.Conditions.Ready && !ep.Conditions.Terminating
			if ep.TargetRef.Name == selected.Name && ep.TargetRef.UID == selected.UID {
				// Unknown readiness is not evidence of removal or non-readiness.
				// Explicit termination remains sufficient even without Ready.
				if ep.Conditions.Ready == nil && !ep.Conditions.Terminating {
					return false
				}
				selectedReady = selectedReady || ready
			}
			if ep.TargetRef.Name == survivor.Name && ep.TargetRef.UID == survivor.UID && ready {
				survivorReady = true
			}
		}
	}
	return survivorReady && selectedReady != removed
}

func (h *harness) waitDrainEndpoints(ctx context.Context, pods []podIdentity, removed bool) error {
	return waitTopology(ctx, func() (bool, error) {
		raw, err := h.topologyCommand(ctx, "inspect selected API EndpointSlice", nil, "-n", namespace, "get", "endpointslice", "-l", "kubernetes.io/service-name=catalog", "-o", "json")
		return drainEndpoints(raw, pods[0], pods[1], removed), err
	})
}

func replacementReady(before, after []podIdentity) bool {
	if len(before) != 2 || len(after) != 2 || before[0].UID == "" || before[1].UID == "" || before[0].UID == before[1].UID {
		return false
	}
	survivor, replacement := false, false
	for _, pod := range after {
		if !pod.Ready || pod.UID == "" || pod.UID == before[0].UID || pod.Name == before[0].Name {
			return false
		}
		if pod.UID == before[1].UID && pod.Name == before[1].Name {
			survivor = true
		} else {
			replacement = true
		}
	}
	return survivor && replacement
}

func (h *harness) waitPodReplacement(role string, before []podIdentity) error {
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()
	return h.waitPodReplacementContext(ctx, role, before)
}

func (h *harness) waitPodReplacementContext(ctx context.Context, role string, before []podIdentity) error {
	return waitTopology(ctx, func() (bool, error) {
		// pods() omits terminating Pods; explicitly require deletion of the old object.
		raw, err := h.topologyCommand(ctx, "verify old Pod absent", nil, "-n", namespace, "get", "pod", before[0].Name, "--ignore-not-found", "-o", "name")
		if err != nil || len(bytes.TrimSpace(raw)) != 0 {
			return false, err
		}
		after, err := h.podsContext(ctx, role)
		return replacementReady(before, after), err
	})
}
