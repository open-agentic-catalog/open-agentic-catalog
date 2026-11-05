//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const serviceClientName = "catalog-service-client"

// Fixed budgets precede every request. Batch 2 uses the existing 25-second drain
// and two-minute replacement bounds; calibration adds no production SLA.
type serviceRouteReport struct {
	Address                   string            `json:"address"`
	ClientPod                 string            `json:"clientPod"`
	ClientUID                 string            `json:"clientUid"`
	ClientImageID             string            `json:"clientImageId"`
	Requests                  int               `json:"requests"`
	Concurrency               int               `json:"concurrency"`
	RequestTimeoutSeconds     int               `json:"requestTimeoutSeconds"`
	CalibrationCeilingSeconds int               `json:"calibrationCeilingSeconds"`
	FaultCeilingSeconds       int               `json:"faultCeilingSeconds"`
	RecoveryCeilingSeconds    int               `json:"recoveryCeilingSeconds"`
	DeniedRequests            int               `json:"deniedRequests"`
	Backends                  map[string]string `json:"backendPodUidsByProcessId"`
	Observed                  map[string]int    `json:"requestsByProcessId"`
}

type serviceRouteOutcome struct {
	Outcome    string `json:"outcome"`
	InstanceID string `json:"instanceId,omitempty"`
}

func serviceClientPolicy(allowed bool) kubernetesObject {
	rules := []any{}
	if allowed {
		rules = append(rules, map[string]any{
			"to":    []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/instance": releaseName, "app.kubernetes.io/component": "api"}}}},
			"ports": []any{map[string]any{"protocol": "TCP", "port": 8080}},
		})
	}
	return kubernetesObject{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": serviceClientName, "namespace": namespace, "labels": testLabels("service-client")},
		"spec":     map[string]any{"podSelector": map[string]any{"matchLabels": testLabels("service-client")}, "policyTypes": []string{"Ingress", "Egress"}, "ingress": []any{}, "egress": rules}}
}

func (h *harness) serviceClientPod() kubernetesObject {
	return kubernetesObject{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": serviceClientName, "namespace": namespace, "labels": testLabels("service-client")},
		"spec": map[string]any{"serviceAccountName": "fixture", "automountServiceAccountToken": false, "enableServiceLinks": false, "restartPolicy": "Never",
			"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{"name": "client", "image": h.fixture, "imagePullPolicy": "Never", "args": []string{"network-probe", "hold"},
				"resources":       map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "16Mi"}, "limits": map[string]string{"cpu": "200m", "memory": "64Mi"}},
				"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}}}}}}
}

// serviceClusterAddress refuses headless, external, wrong-selector or affinity
// Services. Requests use the API-reported numeric ClusterIP, never a Pod tunnel.
func serviceClusterAddress(raw []byte) (string, error) {
	var service struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			Type            string            `json:"type"`
			ClusterIP       string            `json:"clusterIP"`
			SessionAffinity string            `json:"sessionAffinity"`
			Selector        map[string]string `json:"selector"`
			Ports           []struct {
				Port     int    `json:"port"`
				Protocol string `json:"protocol"`
			} `json:"ports"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &service) != nil {
		return "", errors.New("invalid owned API Service")
	}
	ip, err := netip.ParseAddr(service.Spec.ClusterIP)
	if err != nil || !ip.IsPrivate() || service.Metadata.Name != "catalog" || service.Metadata.Namespace != namespace || service.Spec.Type != "ClusterIP" || service.Spec.SessionAffinity != "None" || service.Spec.Selector["app.kubernetes.io/instance"] != releaseName || service.Spec.Selector["app.kubernetes.io/component"] != "api" || len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port != 8080 || service.Spec.Ports[0].Protocol != "TCP" {
		return "", errors.New("unexpected owned API Service route")
	}
	return netip.AddrPortFrom(ip, 8080).String(), nil
}

func (h *harness) serviceRouteRequests(ctx context.Context, address string, count int) ([]serviceRouteOutcome, error) {
	input, err := json.Marshal(map[string]any{"address": address, "token": h.tokens["admin"], "requests": count})
	if err != nil {
		return nil, err
	}
	raw, err := h.topologyCommand(ctx, "probe owned API ClusterIP", input, "-n", namespace, "exec", "-i", serviceClientName, "--", "/federation-fixture", "service-probe")
	if err != nil {
		return nil, errors.New("in-cluster Service client failed")
	}
	var results []serviceRouteOutcome
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&results) != nil || len(results) != count {
		return nil, errors.New("invalid in-cluster client result inventory")
	}
	return results, nil
}

func serviceRouteCoverage(results []serviceRouteOutcome, backends map[string]string) (map[string]int, error) {
	if len(results) != 48 || len(backends) != 2 {
		return nil, errors.New("incomplete service coverage inventory")
	}
	counts := map[string]int{}
	for _, result := range results {
		if result.Outcome != "ok" || result.InstanceID == "" || backends[result.InstanceID] == "" {
			return nil, errors.New("service request failed or reached an unexpected backend")
		}
		counts[result.InstanceID]++
	}
	if len(counts) != 2 {
		return nil, errors.New("service requests did not observe both API processes")
	}
	return counts, nil
}

func (h *harness) verifyServiceRouteCalibration() error {
	pods, err := h.pods("api")
	if err != nil || len(pods) != 2 || !pods[0].Ready || !pods[1].Ready {
		return errors.New("service calibration requires two ready API Pods")
	}
	if err := h.waitDrainEndpoints(h.ctx, pods, false); err != nil {
		return err
	}
	raw, err := h.kubectl("inspect owned API Service", "-n", namespace, "get", "service/catalog", "-o", "json")
	if err != nil {
		return err
	}
	address, err := serviceClusterAddress(raw)
	if err != nil {
		return err
	}
	report := &serviceRouteReport{Address: address, ClientPod: serviceClientName, Requests: 48, Concurrency: 1, RequestTimeoutSeconds: 2, CalibrationCeilingSeconds: 120, FaultCeilingSeconds: 25, RecoveryCeilingSeconds: 120, DeniedRequests: 1, Backends: map[string]string{}}
	for _, pod := range pods {
		forwarded, err := h.startPortForward("pod/"+pod.Name, 8080)
		if err != nil {
			return err
		}
		response, err := h.request("GET", "http://"+forwarded+"/api/v1/service-information", h.tokens["admin"], "", "", nil, nil)
		var info struct {
			InstanceID string `json:"instanceId"`
		}
		if err != nil || response.Status != 200 || json.Unmarshal(response.Body, &info) != nil || info.InstanceID == "" || report.Backends[info.InstanceID] != "" {
			return errors.New("could not bind separate API process identities")
		}
		report.Backends[info.InstanceID] = pod.UID
	}
	if err := h.applyObject("deny Service client traffic", serviceClientPolicy(false)); err != nil {
		return err
	}
	if err := h.applyObject("create restricted Service client", h.serviceClientPod()); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for Service client", "-n", namespace, "wait", "--for=condition=Ready", "pod/"+serviceClientName, "--timeout=2m"); err != nil {
		return err
	}
	raw, err = h.kubectl("record Service client identity", "-n", namespace, "get", "pod/"+serviceClientName, "-o", "json")
	if err != nil {
		return err
	}
	var client struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
		Status struct {
			Containers []struct {
				ImageID string `json:"imageID"`
			} `json:"containerStatuses"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &client) != nil || client.Metadata.UID == "" || len(client.Status.Containers) != 1 || client.Status.Containers[0].ImageID != h.report.Images["fixture"] {
		return errors.New("service client image or identity mismatch")
	}
	report.ClientUID, report.ClientImageID = client.Metadata.UID, client.Status.Containers[0].ImageID
	// A denied request precedes the allow rule. This is the same client, address,
	// executable and credentials used for the positive control.
	ctx, cancel := context.WithTimeout(h.ctx, 120*time.Second)
	defer cancel()
	denied, err := h.serviceRouteRequests(ctx, address, 1)
	if err != nil || denied[0].Outcome != "timeout" || denied[0].InstanceID != "" {
		return errors.New("denied Service route did not time out")
	}
	if err := h.applyObject("allow only API Service endpoints", serviceClientPolicy(true)); err != nil {
		return err
	}
	// Calico applies policy asynchronously. A bounded TCP check establishes the
	// allow transition before the fixed, unretried HTTP sample begins.
	if err := waitTopology(ctx, func() (bool, error) {
		raw, err := h.topologyCommand(ctx, "observe allowed Service route", nil, "-n", namespace, "exec", serviceClientName, "--", "/federation-fixture", "network-probe", "tcp", address)
		if err != nil {
			return false, err
		}
		return strings.TrimSpace(string(raw)) == "connected", nil
	}); err != nil {
		return err
	}
	outcomes, err := h.serviceRouteRequests(ctx, address, 48)
	if err != nil {
		return err
	}
	report.Observed, err = serviceRouteCoverage(outcomes, report.Backends)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	h.report.ServiceRoute = report
	h.recordPlacement("service-route-calibration-api", pods)
	h.report.Checks = append(h.report.Checks, "in-cluster-clusterip-route-denied-control", "in-cluster-clusterip-two-api-process-coverage")
	fmt.Printf("[topology] Service route: 48 fresh connections reached both API processes; denied control timed out\n")
	return nil
}
