//go:build minikube

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type serviceCommandResult struct {
	Outcome    string `json:"outcome"`
	InstanceID string `json:"instanceId,omitempty"`
	Status     int    `json:"status,omitempty"`
	Digest     string `json:"digest,omitempty"`
	ETag       string `json:"etag,omitempty"`
	Replayed   bool   `json:"replayed,omitempty"`
	Code       string `json:"code,omitempty"`
}
type serviceDisruptionReport struct {
	SelectedUID            string                  `json:"selectedPodUid"`
	SurvivorUID            string                  `json:"survivorPodUid"`
	SelectedProcess        string                  `json:"selectedProcess"`
	SurvivorProcess        string                  `json:"survivorProcess"`
	ReceiptDigest          string                  `json:"retainedReceiptSha256"`
	ReceiptOutcome         string                  `json:"receiptOutcome"`
	SelectionAttempts      int                     `json:"selectionAttempts"`
	ReadsBefore            int                     `json:"readsBefore"`
	ReadsDegraded          int                     `json:"readsDegraded"`
	ReadsRecovered         int                     `json:"readsRecovered"`
	DegradedOutcomes       []serviceCommandResult  `json:"degradedOutcomes"`
	EndpointTransitions    []serviceEndpointReport `json:"endpointSlices"`
	CurrentAuthorityDenied bool                    `json:"currentAuthorityDenied"`
	TenantDenied           bool                    `json:"tenantDenied"`
	ReplayVerified         bool                    `json:"replayVerified"`
	SingularEvidence       bool                    `json:"singularEvidence"`
	RecoveredBackends      map[string]string       `json:"recoveredBackendPodUidsByProcessId"`
	SelectedReadStatus     int                     `json:"selectedReadStatus"`
}

type serviceEndpointReport struct {
	Phase           string `json:"phase"`
	Digest          string `json:"digest"`
	SelectedPresent bool   `json:"selectedPresent"`
	SelectedReady   bool   `json:"selectedReady"`
	SurvivorReady   bool   `json:"survivorReady"`
	ReadyBackends   int    `json:"readyBackends"`
}

type serviceDisruption struct {
	report      serviceDisruptionReport
	jobID       string
	job         httpResult
	namespaceID string
	namespace   httpResult
	body        map[string]string
	key         string
	assignment  identity.RoleAssignment
	grantETag   string
	survivorURL string
}

func serviceDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func (h *harness) serviceCommand(ctx context.Context, token, operation, path, key, process string, namespaceBody map[string]string) (serviceCommandResult, error) {
	input := map[string]any{"address": h.report.ServiceRoute.Address, "token": token, "operation": operation, "path": path, "key": key, "process": process, "namespace": namespaceBody}
	if namespaceBody == nil {
		delete(input, "namespace")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return serviceCommandResult{}, err
	}
	raw, err = h.topologyCommand(ctx, "exercise in-cluster Service command", raw, "-n", namespace, "exec", "-i", serviceClientName, "--", "/federation-fixture", "service-command")
	if err != nil {
		return serviceCommandResult{}, errors.New("in-cluster Service command process failed")
	}
	var result serviceCommandResult
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return serviceCommandResult{}, errors.New("invalid Service command result")
	}
	return result, nil
}

func serviceReadMatches(result serviceCommandResult, want httpResult, process string) bool {
	return result.Outcome == "ok" && result.Status == 200 && result.Digest == serviceDigest(want.Body) && result.ETag == want.Header.Get("ETag") && !result.Replayed && result.InstanceID != "" && (process == "" || result.InstanceID == process)
}

func (h *harness) prepareServiceDisruption(baseURL string, pods []podIdentity) (*serviceDisruption, error) {
	if h.report.ServiceRoute == nil || len(pods) != 2 {
		return nil, errors.New("service calibration must precede disruption")
	}
	state := &serviceDisruption{key: "service-loss-" + h.suffix}
	state.body = map[string]string{"slug": state.key, "displayName": "Service recovery"}
	state.report.SelectedUID, state.report.SurvivorUID = pods[0].UID, pods[1].UID
	for process, uid := range h.report.ServiceRoute.Backends {
		if uid == pods[0].UID {
			state.report.SelectedProcess = process
		}
		if uid == pods[1].UID {
			state.report.SurvivorProcess = process
		}
	}
	if state.report.SelectedProcess == "" || state.report.SurvivorProcess == "" {
		return nil, errors.New("calibrated API identities changed before disruption")
	}
	// A completed source synchronization Job provides retained state outside the namespaces barrier.
	page, err := h.request("GET", baseURL+"/api/v1/jobs?kind=source.sync&state=succeeded&limit=1", h.tokens["admin"], "", "", nil, nil)
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err != nil || page.Status != 200 || json.Unmarshal(page.Body, &list) != nil || len(list.Items) != 1 || !identity.ValidTenantID(list.Items[0].ID) {
		return nil, errors.New("no retained source synchronization Job for Service reads")
	}
	state.jobID = list.Items[0].ID
	state.job, err = h.request("GET", baseURL+"/api/v1/jobs/"+state.jobID, h.tokens["admin"], "", "", nil, nil)
	if err != nil || state.job.Status != 200 {
		return nil, errors.New("retained source synchronization Job unavailable")
	}
	grant, err := h.request("POST", baseURL+"/api/v1/role-assignments", h.tokens["admin"], state.key+"-grant", "", map[string]string{"subject": "oidc-reader", "actorType": "human", "role": "tenant_admin", "reason": "Owned Service replay qualification"}, nil)
	if err != nil || grant.Status != 201 || json.Unmarshal(grant.Body, &state.assignment) != nil || state.assignment.ID == "" || state.assignment.State != identity.AssignmentActive || grant.Header.Get("ETag") != `"rev-1"` {
		return nil, errors.New("temporary Service command grant failed")
	}
	state.grantETag = grant.Header.Get("ETag")
	survivorAddress, err := h.startPortForward("pod/"+pods[1].Name, 8080)
	if err != nil {
		return nil, err
	}
	state.survivorURL = "http://" + survivorAddress
	ctx, cancel := context.WithTimeout(h.ctx, 120*time.Second)
	defer cancel()
	for attempt := 1; attempt <= 48; attempt++ {
		result, err := h.serviceCommand(ctx, h.tokens["reader"], "discard", "", state.key, state.report.SelectedProcess, state.body)
		if err != nil {
			return nil, err
		}
		state.report.SelectionAttempts = attempt
		if result.Outcome == "not_selected" && result.InstanceID == state.report.SurvivorProcess {
			continue
		}
		if result.Outcome != "response_discarded" || result.InstanceID != state.report.SelectedProcess || result.Status != 0 || result.Digest != "" || result.ETag != "" || result.Replayed {
			return nil, errors.New("command receipt was not deliberately withheld on the selected API")
		}
		state.report.ReceiptOutcome = result.Outcome
		break
	}
	if state.report.ReceiptOutcome == "" {
		return nil, errors.New("service did not select the original API within the frozen connection bound")
	}
	// Independent state read, never the discarded command receipt.
	raw, err := h.drainSQL(ctx, "SELECT id FROM namespaces WHERE tenant_id='"+primaryTenantID+"' AND slug='"+state.key+"'")
	state.namespaceID = strings.TrimSpace(string(raw))
	if err != nil || !identity.ValidTenantID(state.namespaceID) {
		return nil, errors.New("lost-receipt namespace is not retained")
	}
	// Read only the retained receipt hash, status and ETag through the owner.
	// Creation replies can retain finer timestamp precision than a fresh SQL read.
	if !identity.ValidTenantID(state.assignment.MembershipID) {
		return nil, errors.New("invalid reader membership binding")
	}
	receiptSQL := fmt.Sprintf("SELECT encode(digest(k.atomic_response_body,'sha256'),'hex'),coalesce(k.atomic_response_headers->'Etag'->>0,k.atomic_response_headers->'ETag'->>0),k.atomic_response_status FROM idempotency_keys k JOIN tenant_memberships m ON m.tenant_id=k.tenant_id AND k.actor='membership:'||m.realm_id::text||':'||m.id::text WHERE k.tenant_id='%s' AND m.id='%s' AND k.route='POST /api/v1/namespaces' AND k.idempotency_key='%s' AND k.completed_at IS NOT NULL", primaryTenantID, state.assignment.MembershipID, state.key)
	receipt, err := h.drainSQL(ctx, receiptSQL)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSpace(string(receipt)), "|")
	if len(fields) != 3 || len(fields[0]) != 64 || fields[1] != `"rev-1"` || fields[2] != "201" {
		return nil, errors.New("completed command receipt is unavailable")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return nil, errors.New("invalid retained receipt hash")
	}
	state.report.ReceiptDigest = fields[0]
	state.namespace, err = h.request("GET", baseURL+"/api/v1/namespaces/"+state.namespaceID, h.tokens["admin"], "", "", nil, nil)
	var retained catalog.Namespace
	if err != nil || state.namespace.Status != 200 || state.namespace.Header.Get("ETag") != `"rev-1"` || json.Unmarshal(state.namespace.Body, &retained) != nil || retained.ID != state.namespaceID || retained.TenantID != primaryTenantID || retained.Slug != state.key || retained.DisplayName != state.body["displayName"] || retained.Revision != 1 {
		return nil, errors.New("lost-receipt command failed independent state verification")
	}
	for range 2 {
		result, err := h.serviceCommand(ctx, h.tokens["admin"], "read", "/api/v1/jobs/"+state.jobID, "", "", nil)
		if err != nil || !serviceReadMatches(result, state.job, "") {
			return nil, errors.New("service Job read failed before disruption")
		}
		state.report.ReadsBefore++
	}
	return state, nil
}

func serviceEndpointObservation(raw []byte, pods []podIdentity, removed bool, phase string) (serviceEndpointReport, error) {
	if len(pods) != 2 || !drainEndpoints(raw, pods[0], pods[1], removed) {
		return serviceEndpointReport{}, errors.New("service EndpointSlice transition did not preserve the original survivor")
	}
	var list struct {
		Items []struct {
			Endpoints []struct {
				Conditions struct {
					Ready       *bool `json:"ready"`
					Terminating bool  `json:"terminating"`
				} `json:"conditions"`
				Target struct {
					UID string `json:"uid"`
				} `json:"targetRef"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return serviceEndpointReport{}, err
	}
	observation := serviceEndpointReport{Phase: phase, Digest: serviceDigest(raw)}
	ready := map[string]bool{}
	for _, item := range list.Items {
		for _, ep := range item.Endpoints {
			available := ep.Conditions.Ready != nil && *ep.Conditions.Ready && !ep.Conditions.Terminating
			if available && ep.Target.UID != "" {
				ready[ep.Target.UID] = true
			}
			if ep.Target.UID == pods[0].UID {
				observation.SelectedPresent = true
				observation.SelectedReady = observation.SelectedReady || available
			}
			if ep.Target.UID == pods[1].UID {
				observation.SurvivorReady = observation.SurvivorReady || available
			}
		}
	}
	observation.ReadyBackends = len(ready)
	return observation, nil
}
func (h *harness) serviceEndpointEvidence(ctx context.Context, pods []podIdentity, removed bool, phase string, state *serviceDisruption) error {
	return waitTopology(ctx, func() (bool, error) {
		raw, err := h.topologyCommand(ctx, "record API Service EndpointSlice transition", nil, "-n", namespace, "get", "endpointslice", "-l", "kubernetes.io/service-name=catalog", "-o", "json")
		if err != nil {
			return false, err
		}
		if !drainEndpoints(raw, pods[0], pods[1], removed) {
			return false, nil
		}
		observation, err := serviceEndpointObservation(raw, pods, removed, phase)
		if err != nil {
			return false, err
		}
		expected := 2
		if phase == "degraded" {
			expected = 1
		}
		if observation.ReadyBackends != expected {
			return false, nil
		}
		state.report.EndpointTransitions = append(state.report.EndpointTransitions, observation)
		return true, nil
	})
}

func (h *harness) requireDegradedService(ctx context.Context, pods []podIdentity) error {
	return waitTopology(ctx, func() (bool, error) {
		raw, err := h.topologyCommand(ctx, "observe degraded API Deployment", nil, "-n", namespace, "get", "deployment/catalog-api", "-o", "json")
		var deployment struct {
			Spec struct {
				Replicas int `json:"replicas"`
			} `json:"spec"`
			Status struct {
				Ready int `json:"readyReplicas"`
			} `json:"status"`
		}
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(raw, &deployment); err != nil {
			return false, err
		}
		if deployment.Spec.Replicas != 2 || deployment.Status.Ready != 1 {
			return false, nil
		}
		current, err := h.podsContext(ctx, "api")
		if err != nil {
			return false, err
		}
		ready := 0
		for _, pod := range current {
			if pod.Ready {
				if pod.UID != pods[1].UID {
					return false, errors.New("service used a replacement instead of the original survivor")
				}
				ready++
			}
		}
		if ready != 1 {
			return false, errors.New("original API survivor is not independently ready")
		}
		return true, nil
	})
}

func (h *harness) verifyDegradedService(ctx context.Context, pods []podIdentity, state *serviceDisruption) error {
	if err := h.requireDegradedService(ctx, pods); err != nil {
		return err
	}
	for range 4 {
		result, err := h.serviceCommand(ctx, h.tokens["admin"], "read", "/api/v1/jobs/"+state.jobID, "", "", nil)
		state.report.DegradedOutcomes = append(state.report.DegradedOutcomes, result)
		if err != nil || !serviceReadMatches(result, state.job, state.report.SurvivorProcess) {
			return errors.New("fresh Service read did not return retained Job state through the original survivor")
		}
		state.report.ReadsDegraded++
	}
	denied, err := h.serviceCommand(ctx, h.tokens["other"], "read", "/api/v1/jobs/"+state.jobID, "", "", nil)
	if err != nil || denied.Outcome != "ok" || denied.Status != 404 || denied.Code != "NOT_FOUND" || denied.InstanceID != state.report.SurvivorProcess {
		return errors.New("degraded Service disclosed another tenant's Job")
	}
	state.report.TenantDenied = true
	replay, err := h.serviceCommand(ctx, h.tokens["reader"], "create", "", state.key, "", state.body)
	if err != nil || replay.Outcome != "ok" || replay.InstanceID != state.report.SurvivorProcess || replay.Status != 201 || !replay.Replayed || replay.ETag != state.namespace.Header.Get("ETag") || replay.Digest != state.report.ReceiptDigest {
		return errors.New("lost-receipt command did not replay exactly through the surviving Service backend")
	}
	state.report.ReplayVerified = true
	return h.requireDegradedService(ctx, pods)
}

func (h *harness) finishServiceDisruption(ctx context.Context, state *serviceDisruption) error {
	current, err := h.podsContext(ctx, "api")
	if err != nil || len(current) != 2 {
		return errors.New("two API Pods did not recover")
	}
	state.report.RecoveredBackends = map[string]string{}
	for _, pod := range current {
		if !pod.Ready || pod.ImageID != h.report.Images["application"] {
			return errors.New("recovered API image or readiness mismatch")
		}
		address, err := h.startPortForward("pod/"+pod.Name, 8080)
		if err != nil {
			return err
		}
		response, err := h.requestContext(ctx, "GET", "http://"+address+"/api/v1/service-information", h.tokens["admin"], "", "", nil, nil)
		var info struct {
			InstanceID string `json:"instanceId"`
		}
		if err != nil || response.Status != 200 || json.Unmarshal(response.Body, &info) != nil || info.InstanceID == "" || state.report.RecoveredBackends[info.InstanceID] != "" {
			return errors.New("recovered API process identity unavailable")
		}
		state.report.RecoveredBackends[info.InstanceID] = pod.UID
	}
	if state.report.RecoveredBackends[state.report.SurvivorProcess] != state.report.SurvivorUID || state.report.RecoveredBackends[state.report.SelectedProcess] != "" {
		return errors.New("API recovery lost the original survivor or retained the evicted process")
	}
	h.recordPlacement("service-recovered-api", current)
	for range 2 {
		result, err := h.serviceCommand(ctx, h.tokens["admin"], "read", "/api/v1/namespaces/"+state.namespaceID, "", "", nil)
		if err != nil || (!serviceReadMatches(result, state.namespace, "") || state.report.RecoveredBackends[result.InstanceID] == "") {
			return errors.New("service lost the committed namespace after recovery")
		}
		state.report.ReadsRecovered++
	}
	revoked, err := h.requestContext(ctx, "POST", state.survivorURL+"/api/v1/role-assignments/"+state.assignment.ID+":revoke", h.tokens["admin"], state.key+"-revoke", state.grantETag, map[string]string{"reason": "Finish owned Service replay qualification"}, nil)
	var assignment identity.RoleAssignment
	if err != nil || revoked.Status != 200 || revoked.Header.Get("ETag") != `"rev-2"` || json.Unmarshal(revoked.Body, &assignment) != nil || assignment.ID != state.assignment.ID || assignment.State != identity.AssignmentRevoked {
		return errors.New("temporary Service command grant was not revoked")
	}
	denied, err := h.serviceCommand(ctx, h.tokens["reader"], "create", "", state.key, "", state.body)
	if err != nil || denied.Outcome != "ok" || denied.Status != 403 || denied.Code != "FORBIDDEN" || denied.Replayed {
		return errors.New("revoked permission disclosed a completed command receipt")
	}
	state.report.CurrentAuthorityDenied = true
	query := fmt.Sprintf("SELECT (SELECT count(*) FROM namespaces WHERE tenant_id='%s' AND id='%s' AND slug='%s' AND revision=1),(SELECT count(*) FROM audit_events WHERE tenant_id='%s' AND action='namespace.created' AND target_type='namespace' AND target_id='%s'),(SELECT count(*) FROM outbox_events WHERE tenant_id='%s' AND subject='%s' AND data->>'action'='namespace.created')", primaryTenantID, state.namespaceID, state.key, primaryTenantID, state.namespaceID, primaryTenantID, state.namespaceID)
	raw, err := h.drainSQL(ctx, query)
	if err != nil || strings.TrimSpace(string(raw)) != "1|1|1" {
		return errors.New("service recovery did not retain one namespace, audit and outbox event")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	state.report.SingularEvidence = true
	h.report.ServiceDisruption = &state.report
	h.report.Checks = append(h.report.Checks, "clusterip-useful-reads-on-original-survivor-while-degraded", "clusterip-lost-receipt-exact-replay-and-current-authority", "clusterip-recovery-singular-namespace-audit-and-outbox")
	return nil
}
