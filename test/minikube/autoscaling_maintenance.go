//go:build minikube

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

const neighborHPA = "catalog-test-neighbor-hpa"
const inactiveWorkerHPA = "catalog-test-worker-hpa"

type hpaMaintenanceReport struct {
	Refusals       []string            `json:"refusals"`
	NeighborHPAUID string              `json:"neighborHpaUID"`
	NeighborPodUID string              `json:"neighborPodUID"`
	OriginalHPAUID string              `json:"originalHpaUID"`
	RestoredHPAUID string              `json:"restoredHpaUID"`
	Before         map[string][]string `json:"beforePodUIDs"`
	After          map[string][]string `json:"afterPodUIDs"`
	TargetReplicas int                 `json:"targetReplicas"`
}

type hpaControl struct {
	Metadata struct {
		Name string `json:"name"`
		UID  string `json:"uid"`
	} `json:"metadata"`
	Spec   json.RawMessage `json:"spec"`
	Status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

// Use fixed bounds outside the timed scaling experiment. A missing custom
// metric makes the worker HPA genuinely inactive without stopping its target.
func auxiliaryHPA(name, target string, replicas int, missingMetric bool) kubernetesObject {
	metric := map[string]any{"type": "Resource", "resource": map[string]any{"name": "cpu", "target": map[string]any{"type": "Utilization", "averageUtilization": 50}}}
	if missingMetric {
		metric = map[string]any{"type": "Pods", "pods": map[string]any{"metric": map[string]string{"name": "catalog_test_missing_metric"}, "target": map[string]any{"type": "AverageValue", "averageValue": "1"}}}
	}
	return kubernetesObject{"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler", "metadata": map[string]any{"name": name, "namespace": namespace, "labels": testLabels("autoscaling")}, "spec": map[string]any{"scaleTargetRef": map[string]string{"apiVersion": "apps/v1", "kind": "Deployment", "name": target}, "minReplicas": replicas, "maxReplicas": replicas, "metrics": []any{metric}}}
}

func decodeHPAControl(raw []byte, name, active string) (hpaControl, bool) {
	var state hpaControl
	if json.Unmarshal(raw, &state) != nil || state.Metadata.Name != name || state.Metadata.UID == "" || len(state.Spec) == 0 {
		return state, false
	}
	for _, c := range state.Status.Conditions {
		if c.Type == "ScalingActive" {
			return state, c.Status == active
		}
	}
	return state, false
}

func (h *harness) waitHPAControl(name, active string) (hpaControl, error) {
	ctx, cancel := context.WithTimeout(h.ctx, 90*time.Second)
	defer cancel()
	var state hpaControl
	err := waitTopology(ctx, func() (bool, error) {
		raw, e := h.topologyCommand(ctx, "read external HPA control", nil, "-n", namespace, "get", "hpa/"+name, "-o", "json", "--request-timeout=3s")
		if e != nil {
			return false, e
		}
		var ready bool
		state, ready = decodeHPAControl(raw, name, active)
		return ready, nil
	})
	return state, err
}

func (h *harness) removeTestHPA(name string) error {
	_, err := h.kubectl("remove selected external HPA", "-n", namespace, "delete", "hpa/"+name, "--wait=true", "--timeout=30s")
	return err
}

func (h *harness) requireNoRuntimeHPA() error {
	raw, err := h.kubectl("require no runtime HPA before maintenance", "-n", namespace, "get", "hpa", "-o", "json")
	var inventory struct {
		Items []struct {
			Spec struct {
				Target struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"scaleTargetRef"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &inventory) != nil || inventory.Items == nil {
		return errors.New("HPA inventory unavailable before maintenance")
	}
	for _, v := range inventory.Items {
		if v.Spec.Target.Kind == "Deployment" && (v.Spec.Target.Name == "catalog-api" || v.Spec.Target.Name == "catalog-worker") {
			return errors.New("targeting HPA still present before maintenance")
		}
	}
	return nil
}

func (h *harness) expectHPAPreflightRefusal(values, stage string) error {
	before, err := h.waitMaintenanceRuntimes()
	if err != nil {
		return err
	}
	err = h.deployCatalog(values, "1m")
	if err == nil || !strings.Contains(err.Error(), "suspend and remove targeting HPA before coordinated deployment") {
		return fmt.Errorf("%s did not stop at targeting-HPA admission", stage)
	}
	for _, role := range []string{"api", "worker"} {
		after, e := h.pods(role)
		if e != nil {
			return e
		}
		if !sameUIDs(before[role], after) {
			return errors.New("HPA refusal changed runtime Pods")
		}
		for _, p := range after {
			if !p.Ready || p.Restarts != 0 {
				return errors.New("HPA refusal disturbed runtime readiness")
			}
		}
	}
	h.report.Autoscaling.Maintenance.Refusals = append(h.report.Autoscaling.Maintenance.Refusals, stage)
	return nil
}

// HPA conditions can precede Deployment convergence. Wait for the actual
// selected processes before using their identities as a refusal control.
func (h *harness) waitMaintenanceRuntimes() (map[string][]podIdentity, error) {
	ctx, cancel := context.WithTimeout(h.ctx, 90*time.Second)
	defer cancel()
	var selected map[string][]podIdentity
	err := waitTopology(ctx, func() (bool, error) {
		current := make(map[string][]podIdentity)
		for _, role := range []string{"api", "worker"} {
			pods, err := h.podsContext(ctx, role)
			if err != nil {
				return false, err
			}
			if !maintenanceRuntimeReady(pods, role, h.report.Images["application"]) {
				return false, nil
			}
			current[role] = pods
		}
		selected = current
		return true, nil
	})
	return selected, err
}

func maintenanceRuntimeReady(pods []podIdentity, role, image string) bool {
	if requireUpgradeReplicas(pods, false) != nil || pods[0].UID == pods[1].UID || image == "" {
		return false
	}
	for _, pod := range pods {
		if pod.UID == "" || pod.Restarts != 0 || pod.ImageID != image || pod.ServiceAccount != "catalog-"+role {
			return false
		}
	}
	return true
}

func (h *harness) requireHPANeighbor(want hpaControl, pod podIdentity, address string, client *http.Client) error {
	current, err := h.waitHPAControl(neighborHPA, "True")
	if err != nil {
		return err
	}
	if current.Metadata.UID != want.Metadata.UID || !bytes.Equal(current.Spec, want.Spec) {
		return errors.New("maintenance changed the unrelated HPA")
	}
	after, err := h.pods("decoy")
	if err != nil || len(after) != 1 || after[0].UID != pod.UID || !after[0].Ready || after[0].Restarts != 0 || after[0].ImageID != pod.ImageID {
		return errors.New("maintenance replaced or restarted the unrelated Pod")
	}
	response, err := h.request("GET", address+"/healthz", "", "", "", nil, client)
	if err != nil || response.Status != 200 {
		return errors.New("unrelated fixture health read failed")
	}
	return nil
}

func (h *harness) verifyHPAMaintenance(runID string, initial []byte, evidence replicaEvidence) error {
	report := &hpaMaintenanceReport{Before: map[string][]string{}, After: map[string][]string{}, TargetReplicas: 2}
	h.report.Autoscaling.Maintenance = report
	original, err := h.waitHPAControl("catalog-test-hpa", "True")
	if err != nil {
		return err
	}
	report.OriginalHPAUID = original.Metadata.UID
	// Keep the refusal control stable while Helm renders and performs its dry-run.
	// The completed 2-to-3-to-2 experiment above retains its original bounds.
	if _, err := h.kubectl("freeze API HPA for refusal readback", "-n", namespace, "patch", "hpa/catalog-test-hpa", "--type=merge", "-p", `{"spec":{"minReplicas":2,"maxReplicas":2}}`); err != nil {
		return err
	}
	if _, err := h.waitHPAControl("catalog-test-hpa", "True"); err != nil {
		return err
	}
	if err := h.applyObject("create unrelated HPA", auxiliaryHPA(neighborHPA, "decoy-fixture", 1, false)); err != nil {
		return err
	}
	neighbor, err := h.waitHPAControl(neighborHPA, "True")
	if err != nil {
		return err
	}
	report.NeighborHPAUID = neighbor.Metadata.UID
	decoys, err := h.pods("decoy")
	if err != nil || len(decoys) != 1 || !decoys[0].Ready || decoys[0].Restarts != 0 {
		return errors.New("unrelated Pod baseline unavailable")
	}
	report.NeighborPodUID = decoys[0].UID
	forwarded, err := h.startPortForward("pod/"+decoys[0].Name, 8081)
	if err != nil {
		return err
	}
	client := replicaHTTPClient()
	defer client.CloseIdleConnections()
	neighborAddress := "http://" + forwarded
	if err := h.requireHPANeighbor(neighbor, decoys[0], neighborAddress, client); err != nil {
		return err
	}
	good, err := h.upgradeValues("hpa-maintenance", "catalog_api", 2, false)
	if err != nil {
		return err
	}
	if err := h.expectHPAPreflightRefusal(good, "active-api"); err != nil {
		return err
	}
	if err := h.removeTestHPA("catalog-test-hpa"); err != nil {
		return err
	}
	if err := h.applyObject("create inactive targeting worker HPA", auxiliaryHPA(inactiveWorkerHPA, "catalog-worker", 2, true)); err != nil {
		return err
	}
	if _, err := h.waitHPAControl(inactiveWorkerHPA, "False"); err != nil {
		return err
	}
	if err := h.expectHPAPreflightRefusal(good, "inactive-worker"); err != nil {
		return err
	}
	if err := h.removeTestHPA(inactiveWorkerHPA); err != nil {
		return err
	}
	if err := h.requireNoRuntimeHPA(); err != nil {
		return err
	}
	if err := h.requireHPANeighbor(neighbor, decoys[0], neighborAddress, client); err != nil {
		return err
	}
	for _, role := range []string{"api", "worker"} {
		pods, e := h.pods(role)
		if e != nil {
			return e
		}
		for _, p := range pods {
			report.Before[role] = append(report.Before[role], p.UID)
		}
	}
	// This fixture has no HPA recreator. Creation stays stopped until all release
	// and failure-recovery checks finish. External GitOps suspension is operator-owned.
	if err := h.verifyCoordinatedUpgrade(runID, initial); err != nil {
		return err
	}
	if err := h.requireNoRuntimeHPA(); err != nil {
		return err
	}
	targets, err := h.waitMaintenanceRuntimes()
	if err != nil {
		return err
	}
	for _, role := range []string{"api", "worker"} {
		pods := targets[role]
		for _, p := range pods {
			if slices.Contains(report.Before[role], p.UID) || p.Restarts != 0 {
				return errors.New("maintenance retained an old runtime Pod or restarted a target")
			}
			report.After[role] = append(report.After[role], p.UID)
		}
	}
	if err := h.requireHPANeighbor(neighbor, decoys[0], neighborAddress, client); err != nil {
		return err
	}
	if err := h.applyObject("reinstate HPA after target readiness", hpaObject()); err != nil {
		return err
	}
	restored, err := h.waitHPAControl("catalog-test-hpa", "True")
	if err != nil {
		return err
	}
	if restored.Metadata.UID == original.Metadata.UID || !bytes.Equal(restored.Spec, original.Spec) {
		return errors.New("HPA handoff did not restore the reviewed configuration")
	}
	report.RestoredHPAUID = restored.Metadata.UID
	if _, err := h.waitHPAScale("maintenance-hpa-restored", 2, "True"); err != nil {
		return err
	}
	workers, err := h.pods("worker")
	if err != nil {
		return err
	}
	addresses, err := h.hpaAddresses(2)
	if err != nil {
		return err
	}
	if err := h.hpaIntegrity("after-maintenance", addresses, evidence, workers, client); err != nil {
		return err
	}
	if err := h.requireHPANeighbor(neighbor, decoys[0], neighborAddress, client); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "targeting-active-and-inactive-hpa-refusal-preserves-runtimes", "unrelated-hpa-and-serving-pod-survive-maintenance", "target-values-restored-before-hpa-reinstatement", "retained-authority-replay-and-worker-progress-after-maintenance")
	return nil
}
