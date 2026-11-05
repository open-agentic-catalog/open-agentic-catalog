//go:build minikube

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const metricsServerImage = "registry.k8s.io/metrics-server/metrics-server:v0.9.1@sha256:9b0ba1926e1d3089673f45461c3c1416109dea33b3ba3434048defd380df329b"
const metricsServerAMD64 = "registry.k8s.io/metrics-server/metrics-server@sha256:4dbfba731a021437b4cab5ba2092569edbd898030c27d10c6ec85e03b63789f6"

//go:embed fixtures/metrics-server/components-v0.9.1.json
var metricsServerManifest []byte

type autoscalingReport struct {
	Maintenance                *hpaMaintenanceReport `json:"maintenance,omitempty"`
	MetricsImage               string                `json:"metricsImage"`
	Observations               []hpaObservation      `json:"observations"`
	Load                       hpaLoadReport         `json:"load"`
	RuntimeConnectionLimit     int                   `json:"runtimeConnectionLimit"`
	MissingPrerequisiteRefused bool                  `json:"missingPrerequisiteRefused"`
	SourceID                   string                `json:"sourceId"`
	Jobs                       []string              `json:"jobs"`
}

type hpaObservation struct {
	Stage         string   `json:"stage"`
	Replicas      int      `json:"replicas"`
	Ready         int      `json:"ready"`
	Desired       int      `json:"desired"`
	ScalingActive string   `json:"scalingActive"`
	Reason        string   `json:"reason,omitempty"`
	CPU           *int     `json:"cpuUtilizationPercent,omitempty"`
	Pods          []string `json:"podUIDs"`
}

func hpaObject() kubernetesObject {
	return kubernetesObject{"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler",
		"metadata": map[string]any{"name": "catalog-test-hpa", "namespace": namespace, "labels": testLabels("autoscaling")},
		"spec": map[string]any{"scaleTargetRef": map[string]string{"apiVersion": "apps/v1", "kind": "Deployment", "name": "catalog-api"}, "minReplicas": 2, "maxReplicas": 3,
			"metrics": []any{map[string]any{"type": "Resource", "resource": map[string]any{"name": "cpu", "target": map[string]any{"type": "Utilization", "averageUtilization": 50}}}},
			"behavior": map[string]any{"scaleUp": map[string]any{"stabilizationWindowSeconds": 0, "policies": []any{map[string]any{"type": "Pods", "value": 1, "periodSeconds": 15}}},
				"scaleDown": map[string]any{"stabilizationWindowSeconds": 60, "policies": []any{map[string]any{"type": "Pods", "value": 1, "periodSeconds": 60}}}}}}
}

// The upstream resources and their RBAC stay external to the application chart.
// TLS exceptions below apply only to this disposable Minikube dependency.
func metricsServerObjects(image string) ([]kubernetesObject, error) {
	if fmt.Sprintf("%x", sha256.Sum256(metricsServerManifest)) != "32d55bd09f7d61eeee20e543364730403cb77b291e67a59a4a12ae0dfe72ea4e" {
		return nil, errors.New("metrics server fixture checksum mismatch")
	}
	var rows []kubernetesObject
	if json.Unmarshal(metricsServerManifest, &rows) != nil || len(rows) != 9 {
		return nil, errors.New("invalid Metrics Server fixture inventory")
	}
	for _, row := range rows {
		if row["kind"] != "Deployment" {
			continue
		}
		spec, ok := row["spec"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid metrics deployment spec")
		}
		template, ok := spec["template"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid metrics Pod template")
		}
		pod, ok := template["spec"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid metrics Pod spec")
		}
		containers, ok := pod["containers"].([]any)
		if !ok || len(containers) != 1 {
			return nil, errors.New("invalid metrics container inventory")
		}
		container, ok := containers[0].(map[string]any)
		if !ok {
			return nil, errors.New("invalid metrics container")
		}
		args, ok := container["args"].([]any)
		if !ok {
			return nil, errors.New("invalid metrics arguments")
		}
		resources, ok := container["resources"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid metrics resource budget")
		}
		spec["replicas"] = 1
		container["image"] = image
		container["imagePullPolicy"] = "Never"
		container["args"] = append(args, "--kubelet-insecure-tls")
		resources["limits"] = map[string]string{"cpu": "500m", "memory": "384Mi"}

	}
	return rows, nil
}

func (h *harness) requireHPAMetrics(ctx context.Context) error {
	return waitTopology(ctx, func() (bool, error) {
		raw, err := h.topologyCommand(ctx, "read owned Pod CPU metrics", nil, "get", "--raw", "/apis/metrics.k8s.io/v1beta1/namespaces/"+namespace+"/pods", "--request-timeout=3s")
		return err == nil && usableHPAMetrics(raw), nil
	})
}

func usableHPAMetrics(raw []byte) bool {
	var v struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Timestamp  time.Time `json:"timestamp"`
			Containers []struct {
				Usage map[string]string `json:"usage"`
			} `json:"containers"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	seen := make(map[string]bool)
	n := 0
	for _, row := range v.Items {
		if strings.HasPrefix(row.Metadata.Name, "catalog-api-") && !row.Timestamp.IsZero() && time.Since(row.Timestamp) >= 0 && time.Since(row.Timestamp) < time.Minute && len(row.Containers) == 1 && usableHPACPU(row.Containers[0].Usage["cpu"]) {
			if seen[row.Metadata.Name] {
				return false
			}
			seen[row.Metadata.Name] = true
			n++
		}
	}
	return n >= 2
}

func usableHPACPU(value string) bool {
	for _, suffix := range []string{"n", "u", "m"} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSuffix(value, suffix)
			break
		}
	}
	v, err := strconv.ParseFloat(value, 64)
	return err == nil && v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func (h *harness) installHPAMetrics() error {
	missing, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	err := h.requireHPAMetrics(missing)
	cancel()
	if err == nil {
		return errors.New("fresh owned cluster unexpectedly supplies Pod metrics")
	}
	if h.ctx.Err() != nil {
		return h.ctx.Err()
	}
	h.report.Autoscaling.MissingPrerequisiteRefused = true
	rows, err := metricsServerObjects(h.metricsImage)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := h.applyObject("install owned Metrics Server", row); err != nil {
			return err
		}
	}
	if _, err := h.kubectl("wait for Metrics Server", "-n", "kube-system", "rollout", "status", "deployment/metrics-server", "--timeout=2m"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 90*time.Second)
	defer cancel()
	if err := h.requireHPAMetrics(ctx); err != nil {
		return fmt.Errorf("owned Metrics Server did not supply CPU samples: %w", err)
	}
	raw, err := h.kubectl("verify Metrics Server identity", "-n", "kube-system", "get", "pods", "-l", "k8s-app=metrics-server", "-o", "json")
	var image struct {
		Items []struct {
			Status struct {
				ContainerStatuses []struct {
					ImageID string `json:"imageID"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &image) != nil || len(image.Items) != 1 || len(image.Items[0].Status.ContainerStatuses) != 1 {
		return errors.New("metrics server Pod identity unavailable")
	}
	id := image.Items[0].Status.ContainerStatuses[0].ImageID
	index := "registry.k8s.io/metrics-server/metrics-server@sha256:9b0ba1926e1d3089673f45461c3c1416109dea33b3ba3434048defd380df329b"
	if id != metricsServerAMD64 && id != index && id != h.report.Images["metrics-server-config"] {
		return errors.New("metrics server image identity differs from reviewed pin")
	}
	h.report.Images["metrics-server"] = id
	for _, role := range []string{"api", "worker"} {
		_, err := h.kubectl("deny runtime access to metrics API", "auth", "can-i", "list", "pods.metrics.k8s.io", "-n", namespace, "--as=system:serviceaccount:"+namespace+":catalog-"+role)
		if !hpaMetricsDenied(err) {
			return errors.New("catalog runtime unexpectedly has metrics API access")
		}
	}
	return nil
}

// The command wrapper keeps denied stdout in its error, not in the byte result.
func hpaMetricsDenied(err error) bool {
	return err != nil && strings.HasSuffix(strings.TrimSpace(err.Error()), "\nno")
}

func (h *harness) observeHPA(ctx context.Context, stage string) (hpaObservation, error) {
	v := hpaObservation{Stage: stage}
	raw, err := h.topologyCommand(ctx, "observe actual API scale", nil, "-n", namespace, "get", "deployment/catalog-api", "-o", "json", "--request-timeout=3s")
	var d struct {
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			Ready int `json:"readyReplicas"`
		} `json:"status"`
	}
	if err != nil || json.Unmarshal(raw, &d) != nil {
		return v, errors.New("API scale readback failed")
	}
	v.Replicas, v.Ready = d.Spec.Replicas, d.Status.Ready
	if v.Replicas < 2 || v.Replicas > 3 {
		return v, errors.New("HPA exceeded the selected replica bounds")
	}
	raw, err = h.topologyCommand(ctx, "observe owned HPA conditions", nil, "-n", namespace, "get", "hpa/catalog-test-hpa", "-o", "json", "--request-timeout=3s")
	var hp struct {
		Spec struct {
			Min    int `json:"minReplicas"`
			Max    int `json:"maxReplicas"`
			Target struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"scaleTargetRef"`
		} `json:"spec"`
		Status struct {
			Desired    int `json:"desiredReplicas"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"conditions"`
			Metrics []struct {
				Resource struct {
					Current struct {
						CPU *int `json:"averageUtilization"`
					} `json:"current"`
				} `json:"resource"`
			} `json:"currentMetrics"`
		} `json:"status"`
	}
	if err != nil || json.Unmarshal(raw, &hp) != nil || hp.Spec.Min != 2 || hp.Spec.Max != 3 || hp.Spec.Target.Kind != "Deployment" || hp.Spec.Target.Name != "catalog-api" {
		return v, errors.New("HPA target or bounds changed")
	}
	v.Desired = hp.Status.Desired
	for _, c := range hp.Status.Conditions {
		if c.Type == "ScalingActive" {
			v.ScalingActive, v.Reason = c.Status, c.Reason
		}
	}
	if len(hp.Status.Metrics) == 1 {
		v.CPU = hp.Status.Metrics[0].Resource.Current.CPU
	}
	pods, err := h.podsContext(ctx, "api")
	if err != nil {
		return v, err
	}
	for _, p := range pods {
		if p.Restarts != 0 || p.ImageID != h.report.Images["application"] || p.ServiceAccount != "catalog-api" {
			return v, errors.New("API identity changed or restarted during autoscaling")
		}
		if p.Ready {
			v.Pods = append(v.Pods, p.UID)
		}
	}
	slices.Sort(v.Pods)
	return v, nil
}

func (h *harness) waitHPAScale(stage string, replicas int, active string) (hpaObservation, error) {
	fmt.Printf("[autoscaling] require %s: %d ready API replicas, ScalingActive=%s\n", stage, replicas, active)
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Minute)
	defer cancel()
	var last hpaObservation
	err := waitTopology(ctx, func() (bool, error) {
		var err error
		last, err = h.observeHPA(ctx, stage)
		if err != nil {
			return false, err
		}
		if last.ScalingActive == active && (active != "True" || last.CPU != nil) && last.Replicas == replicas && last.Ready == replicas && len(last.Pods) == replicas {
			return true, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
		return false, nil
	})
	if err != nil {
		cpu := "unavailable"
		if last.CPU != nil {
			cpu = strconv.Itoa(*last.CPU)
		}
		return last, fmt.Errorf("%s did not reach actual ready replicas %d (last replicas=%d ready=%d desired=%d active=%s reason=%s CPU=%v): %w", stage, replicas, last.Replicas, last.Ready, last.Desired, last.ScalingActive, last.Reason, cpu, err)
	}
	h.report.Autoscaling.Observations = append(h.report.Autoscaling.Observations, last)
	return last, nil
}

func (h *harness) hpaAddresses(replicas int) ([]string, error) {
	pods, err := h.pods("api")
	if err != nil || len(pods) != replicas {
		return nil, errors.New("unexpected API replica inventory")
	}
	addresses := make([]string, 0, replicas)
	for _, p := range pods {
		if !p.Ready || p.Restarts != 0 {
			return nil, errors.New("API Pod is not ready without restarts")
		}
		addr, err := h.startPortForward("pod/"+p.Name, 8080)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, "http://"+addr)
	}
	return addresses, nil
}

func (h *harness) verifyAutoscaling(runID string, initial []byte) error {
	report := &autoscalingReport{MetricsImage: metricsServerImage, RuntimeConnectionLimit: 40}
	h.report.Autoscaling = report
	if err := h.installHPAMetrics(); err != nil {
		return err
	}
	if err := h.applyObject("create external owned HPA", hpaObject()); err != nil {
		return err
	}
	_, initialErr := h.waitHPAScale("initial", 2, "True")
	if initialErr != nil {
		return initialErr
	}
	workers, err := h.pods("worker")
	if err != nil || len(workers) != 2 {
		return errors.New("autoscaling requires two retained workers")
	}
	addresses, err := h.hpaAddresses(2)
	if err != nil {
		return err
	}
	client := replicaHTTPClient()
	defer client.CloseIdleConnections()
	key := "hpa-" + h.suffix
	body := map[string]string{"slug": key, "displayName": "Autoscaling integrity fixture"}
	created, err := h.request("POST", addresses[0]+"/api/v1/namespaces", h.tokens["admin"], key, "", body, client)
	if err != nil || created.Status != 201 || created.Header.Get("ETag") != `"rev-1"` {
		return errors.New("autoscaling replay control did not commit")
	}
	evidence := replicaEvidence{Key: key, Body: body, Want: created}
	if err := h.hpaIntegrity("initial", addresses, evidence, workers, client); err != nil {
		return err
	}
	// Integrity commands can also use CPU. Require two ready APIs immediately
	// before the unchanged Search phase, then select those exact processes.
	before, err := h.waitHPAScale("before-load", 2, "True")
	if err != nil {
		return err
	}
	addresses, err = h.hpaAddresses(2)
	if err != nil {
		return err
	}
	load, err := h.startHPALoad(addresses)
	if err != nil {
		return err
	}
	defer func() {
		load.stop()
		<-load.done
		report.Load = load.report()
		// Failed scenarios do not print a successful final report. Retain the
		// finite workload evidence independently, including early termination.
		raw, err := json.Marshal(report.Load)
		if err != nil {
			fmt.Println("[autoscaling] Search evidence unavailable: encoding failed")
		} else {
			fmt.Printf("[autoscaling] Search evidence: %s\n", raw)
		}
	}()
	up, err := h.waitHPAScale("scaled-up", 3, "True")
	if err != nil {
		return err
	}
	for _, uid := range before.Pods {
		if !slices.Contains(up.Pods, uid) {
			return errors.New("scale-up unexpectedly replaced an original API Pod")
		}
	}
	addresses, err = h.hpaAddresses(3)
	if err != nil {
		return err
	}
	load.setPhase("scaled-up-integrity")
	if err := h.hpaIntegrity("scaled-up", addresses, evidence, workers, client); err != nil {
		return err
	}
	load.setPhase("metrics-removal")
	if _, err := h.kubectl("remove owned runtime metrics", "-n", "kube-system", "scale", "deployment/metrics-server", "--replicas=0"); err != nil {
		return err
	}
	if err := h.requireHPAMetricsAbsent(); err != nil {
		return err
	}
	missing, err := h.waitHPAScale("metrics-unavailable", 3, "False")
	if err != nil {
		return err
	}
	if !slices.Equal(up.Pods, missing.Pods) {
		return errors.New("metrics outage changed API processes")
	}
	load.setPhase("metrics-unavailable-integrity")
	if err := h.hpaIntegrity("metrics-unavailable", addresses, evidence, workers, client); err != nil {
		return err
	}
	load.setPhase("metrics-unavailable-hold")
	// Observe multiple independent HPA reconciliations without metrics.
	ctx, cancel := context.WithTimeout(h.ctx, 45*time.Second)
	for ctx.Err() == nil {
		state, err := h.observeHPA(ctx, "metrics-unavailable-hold")
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			cancel()
			return err
		}
		if state.Replicas != 3 || state.Ready != 3 || state.ScalingActive != "False" || !slices.Equal(state.Pods, up.Pods) {
			cancel()
			return errors.New("unavailable metrics did not retain the observed API scale")
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	cancel()
	if h.ctx.Err() != nil {
		return h.ctx.Err()
	}
	load.setPhase("metrics-restoration")
	if _, err := h.kubectl("restore owned runtime metrics", "-n", "kube-system", "scale", "deployment/metrics-server", "--replicas=1"); err != nil {
		return err
	}
	recovery, cancel := context.WithTimeout(h.ctx, 90*time.Second)
	err = h.requireHPAMetrics(recovery)
	cancel()
	if err != nil {
		return err
	}
	<-load.done
	report.Load = load.report()
	if report.Load.Failed != 0 || report.Load.Succeeded == 0 || !report.Load.Complete {
		return fmt.Errorf("bounded Search workload failed: succeeded=%d failed=%d complete=%t seconds=%.3f first=%s", report.Load.Succeeded, report.Load.Failed, report.Load.Complete, report.Load.Seconds, report.Load.FirstFailure)
	}
	down, err := h.waitHPAScale("scaled-down", 2, "True")
	if err != nil {
		return err
	}
	for _, uid := range down.Pods {
		if !slices.Contains(up.Pods, uid) {
			return errors.New("scale-down unexpectedly created a new API process")
		}
	}
	addresses, err = h.hpaAddresses(2)
	if err != nil {
		return err
	}
	if err := h.hpaIntegrity("recovered", addresses, evidence, workers, client); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "actual-hpa-2-to-3-to-2", "missing-metrics-preflight-refusal", "runtime-metrics-outage-and-recovery", "current-authority-and-tenant-refusal-through-scaling", "bounded-Search-traffic-and-worker-progress")
	return h.verifyHPAMaintenance(runID, initial, evidence)
}

func (h *harness) requireHPAMetricsAbsent() error {
	ctx, cancel := context.WithTimeout(h.ctx, time.Minute)
	defer cancel()
	return waitTopology(ctx, func() (bool, error) {
		raw, err := h.topologyCommand(ctx, "observe metrics API unavailability", nil, "get", "apiservice/v1beta1.metrics.k8s.io", "-o", "json", "--request-timeout=3s")
		var state struct {
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		}
		if err != nil || json.Unmarshal(raw, &state) != nil {
			return false, errors.New("metrics APIService state unavailable")
		}
		for _, condition := range state.Status.Conditions {
			if condition.Type == "Available" && condition.Status == "False" {
				_, failed := h.topologyCommand(ctx, "require metrics endpoint failure", nil, "get", "--raw", "/apis/metrics.k8s.io/v1beta1/namespaces/"+namespace+"/pods", "--request-timeout=3s")
				return failed != nil, nil
			}
		}
		return false, nil
	})
}

func (h *harness) hpaIntegrity(stage string, addresses []string, evidence replicaEvidence, workers []podIdentity, client *http.Client) error {
	admin := func(index int, method, path, key, match string, body any) (httpResult, error) {
		return h.request(method, addresses[index%len(addresses)]+path, h.tokens["admin"], key, match, body, client)
	}
	reader := func(index int, method, path, key, match string, body any) (httpResult, error) {
		return h.request(method, addresses[index%len(addresses)]+path, h.tokens["reader"], key, match, body, client)
	}
	if err := h.verifyReplicaEvidence(admin, evidence); err != nil {
		return err
	}
	if err := h.verifyReplicaIsolation(addresses, []httpResult{evidence.Want}, client); err != nil {
		return err
	}
	var ns catalog.Namespace
	if json.Unmarshal(evidence.Want.Body, &ns) != nil || ns.ID == "" {
		return errors.New("invalid autoscaling Namespace control")
	}
	// Grant transitions increment tenant policy even when the load actor is
	// unchanged. Keep those assertions outside stable-policy Search traffic.
	if hpaChangesAuthority(stage) {
		if err := h.verifyReplicaAuthority(admin, reader, ns.ID, "hpa-"+stage+"-"+h.suffix); err != nil {
			return err
		}
	}
	for i := range addresses {
		key := "hpa-" + stage + "-denied-" + h.suffix
		body := map[string]string{"namespaceId": ns.ID, "kind": "skill", "canonicalIdentifier": "urn:air:catalog.example.com:hpa:" + key}
		denied, err := reader(i, "POST", "/api/v1/resources", key, "", body)
		if err != nil || !replicaProblem(denied, http.StatusForbidden, catalog.CodeForbidden) {
			return errors.New("autoscaling admitted an ungranted Resource write")
		}
	}
	after, err := h.pods("worker")
	if err != nil || len(after) != len(workers) {
		return errors.New("autoscaling changed worker count")
	}
	for _, worker := range workers {
		if !slices.ContainsFunc(after, func(p podIdentity) bool { return p.UID == worker.UID && p.Ready && p.Restarts == 0 }) {
			return errors.New("autoscaling replaced or restarted a worker")
		}
	}
	raw, err := h.drainSQL(h.ctx, `SELECT json_build_object('runtime',(SELECT count(*) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker')),'capacity',current_setting('max_connections')::int-current_setting('superuser_reserved_connections')::int-current_setting('reserved_connections')::int)`)
	var budget struct {
		Runtime  int `json:"runtime"`
		Capacity int `json:"capacity"`
	}
	if err != nil || json.Unmarshal(bytes.TrimSpace(raw), &budget) != nil || budget.Runtime > 40 || budget.Capacity < 54 {
		return errors.New("autoscaling exceeds the frozen database connection budget")
	}
	return h.hpaWorkerProgress(addresses[0], stage, client)
}

// Full grant/revoke/restore checks bracket the load. All stages retain denials.
func hpaChangesAuthority(stage string) bool {
	return stage == "initial" || stage == "recovered"
}
