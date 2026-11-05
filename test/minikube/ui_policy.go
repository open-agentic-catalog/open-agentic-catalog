//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"
)

func uiNetworkPolicyValues(extra bool) map[string]any {
	values := map[string]any{"enabled": true, "ingress": []any{map[string]any{
		"from":  []any{map[string]any{"podSelector": map[string]any{"matchLabels": testLabels("identity")}}},
		"ports": []any{map[string]any{"protocol": "TCP", "port": 8080}},
	}}}
	values["extraEgress"] = []any{}
	if extra {
		values["extraEgress"] = []any{map[string]any{
			"to":    []any{map[string]any{"podSelector": map[string]any{"matchLabels": testLabels("decoy")}}},
			"ports": []any{map[string]any{"protocol": "TCP", "port": 8443}},
		}}
	}
	return values
}

// verifyUINetworkPolicy uses the shipped chart policy and a fixture-only
// ephemeral container in a real UI Pod, never a port-forward for ingress proof.
func (h *harness) verifyUINetworkPolicy(uiPod, chart, values, overlay string, uiConfig map[string]any) error {
	if err := h.checkUIPolicyInventory(); err != nil {
		return err
	}
	if err := h.attachUIProbe(uiPod); err != nil {
		return err
	}
	identity, err := h.policyPeer("identity")
	if err != nil {
		return err
	}
	decoy, err := h.policyPeer("decoy")
	if err != nil {
		return err
	}
	worker, err := h.policyPeer("worker")
	if err != nil {
		return err
	}
	api, err := h.policyPeer("api")
	if err != nil {
		return err
	}
	peers := []struct{ name, pod, port string }{
		{"postgres", "postgres-0", "5432"}, {"worker-operations", worker, "9090"}, {"unrelated-peer", decoy, "8443"},
	}
	uiAddress, err := h.policyAddress(uiPod, "8080")
	if err != nil {
		return err
	}
	if err := h.awaitPolicyTCP("ui-selected-ingress", identity, "", uiAddress, true, identity); err != nil {
		return err
	}
	if err := h.awaitPolicyTCP("ui-unselected-ingress-denied", decoy, "", uiAddress, false, identity); err != nil {
		return err
	}
	apiAddress, err := h.policyAddress(api, "8080")
	if err != nil {
		return err
	}
	if err := h.awaitPolicyTCP("ui-release-api-egress", uiPod, "network-probe", apiAddress, true, identity); err != nil {
		return err
	}
	for _, mode := range []string{"dns-udp", "dns-tcp"} {
		raw, err := h.kubectl("UI "+mode+" resolution", "-n", namespace, "exec", uiPod, "-c", "network-probe", "--", "/federation-fixture", "network-probe", mode, "catalog."+namespace+".svc.cluster.local")
		connected, resultErr := policyProbeResult(raw, err)
		if resultErr != nil || !connected {
			return fmt.Errorf("UI %s probe did not resolve its destination", mode)
		}
		h.report.Checks = append(h.report.Checks, "ui-"+mode+"-resolution")
	}
	for _, peer := range peers {
		address, err := h.policyAddress(peer.pod, peer.port)
		if err != nil {
			return err
		}
		if err := h.awaitPolicyTCP("ui-"+peer.name+"-egress-denied", uiPod, "network-probe", address, false, identity); err != nil {
			return err
		}
	}
	// Same Pod/IP/port is denied, then explicitly permitted, then denied again.
	address, err := h.policyAddress(decoy, "8443")
	if err != nil {
		return err
	}
	for _, enabled := range []bool{true, false} {
		uiConfig["networkPolicy"] = uiNetworkPolicyValues(enabled)
		raw, err := json.Marshal(map[string]any{"ui": uiConfig})
		if err != nil {
			return err
		}
		if err := os.WriteFile(overlay, raw, 0o600); err != nil {
			return err
		}
		if _, err := h.helm("update explicit UI peer exception", "upgrade", releaseName, chart, "-n", namespace, "--values", values, "--values", overlay, "--wait", "--wait-for-jobs", "--timeout=4m"); err != nil {
			return err
		}
		if err := h.checkUIPolicyInventory(); err != nil {
			return err
		}
		name := "ui-extra-egress-enabled"
		if !enabled {
			name = "ui-extra-egress-removed"
		}
		if err := h.awaitPolicyTCP(name, uiPod, "network-probe", address, enabled, identity); err != nil {
			return err
		}
	}
	pods, err := h.pods("ui")
	if err != nil || len(pods) != 2 {
		return errors.New("UI policy checks changed replica membership")
	}
	for _, pod := range pods {
		if !pod.Ready {
			return errors.New("UI policy checks left an unhealthy replica")
		}
	}
	h.report.Checks = append(h.report.Checks, "shipped-ui-policy-real-pod-network-namespace")
	return nil
}

func (h *harness) policyPeer(role string) (string, error) {
	pods, err := h.pods(role)
	if err != nil {
		return "", err
	}
	if len(pods) == 0 || !pods[0].Ready {
		return "", fmt.Errorf("missing ready policy peer: %s", role)
	}
	return pods[0].Name, nil
}

func (h *harness) policyAddress(pod, port string) (string, error) {
	raw, err := h.kubectl("resolve exact policy peer IP", "-n", namespace, "get", "pod", pod, "-o", "jsonpath={.status.podIP}")
	if err != nil {
		return "", err
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(raw)))
	if err != nil || !ip.IsValid() {
		return "", errors.New("policy peer has no numeric IP")
	}
	return net.JoinHostPort(ip.String(), port), nil
}

func (h *harness) attachUIProbe(pod string) error {
	return h.attachNetworkProbe(namespace, pod)
}

// attachNetworkProbe runs the maintained probe in the selected owned Pod's actual network namespace.
func (h *harness) attachNetworkProbe(selectedNamespace, pod string) error {
	raw, err := h.kubectl("read owned Pod for test-only diagnostic container", "-n", selectedNamespace, "get", "pod", pod, "-o", "json")
	if err != nil {
		return err
	}
	var object struct {
		APIVersion string         `json:"apiVersion"`
		Kind       string         `json:"kind"`
		Metadata   map[string]any `json:"metadata"`
		Spec       map[string]any `json:"spec"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	if existing, ok := object.Spec["ephemeralContainers"].([]any); ok && len(existing) != 0 {
		return errors.New("UI Pod already has diagnostic containers")
	}
	object.Spec["ephemeralContainers"] = []any{map[string]any{
		"name": "network-probe", "image": h.fixture, "imagePullPolicy": "Never",
		"command": []string{"/federation-fixture", "network-probe", "hold"},
		"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
			"capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
	}}
	raw, err = json.Marshal(object)
	if err != nil {
		return err
	}
	if _, err := h.kubectlInput("attach bounded test-only network probe", raw, "replace", "--raw", "/api/v1/namespaces/"+selectedNamespace+"/pods/"+pod+"/ephemeralcontainers", "-f", "-"); err != nil {
		return err
	}
	_, err = h.kubectl("wait for owned diagnostic container", "-n", selectedNamespace, "wait", "pod/"+pod, "--for=jsonpath={.status.ephemeralContainerStatuses[0].state.running}", "--timeout=60s")
	return err
}

// Fail closed on extra policies: their allows would be additive. The owned
// Calico cluster also must have no separately installed native policy objects.
func (h *harness) checkUIPolicyInventory() error {
	for _, resource := range []string{"networkpolicies", "networkpolicies.crd.projectcalico.org", "globalnetworkpolicies.crd.projectcalico.org"} {
		raw, err := h.kubectl("inspect effective UI policy inventory", "-n", namespace, "get", resource, "-o", "json")
		if err != nil {
			return err
		}
		expected := []string{}
		if resource == "networkpolicies" {
			expected = []string{"catalog-api-egress", "catalog-ui", "catalog-worker-egress"}
		}
		if err := policyInventory(raw, expected); err != nil {
			return err
		}
	}
	return nil
}

func policyInventory(raw []byte, expected []string) error {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	if list.Items == nil {
		return errors.New("missing policy inventory")
	}
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.Metadata.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, expected) {
		return errors.New("unexpected effective network policy inventory")
	}
	return nil
}

func policyProbeResult(raw []byte, err error) (bool, error) {
	if err != nil {
		return false, errors.New("network probe process failed")
	}
	switch string(raw) {
	case "connected\n":
		return true, nil
	case "timeout\n":
		return false, nil
	default:
		return false, errors.New("network probe returned invalid evidence")
	}
}

// Fresh TCP connections, a live permitted control before AND after each sample,
// and three consecutive matching observations avoid convergence false passes.
func (h *harness) awaitPolicyTCP(name, pod, container, address string, allowed bool, control string) error {
	return h.awaitPolicyTCPFrom(name, pod, container, address, allowed, control, "")
}

func (h *harness) awaitPolicyTCPFrom(name, pod, container, address string, allowed bool, control, controlContainer string) error {
	ctx, cancel := context.WithTimeout(h.ctx, 45*time.Second)
	defer cancel()
	probe := func(peer, selectedContainer string) (bool, error) {
		args := []string{"-n", namespace, "exec", peer}
		if selectedContainer != "" {
			args = append(args, "-c", selectedContainer)
		}
		args = append(args, "--", "/federation-fixture", "network-probe", "tcp", address)
		raw, err := h.topologyCommand(ctx, "observe "+name, nil, args...)
		return policyProbeResult(raw, err)
	}
	consecutive := 0
	err := waitTopology(ctx, func() (bool, error) {
		for _, before := range []bool{true, false} {
			live, err := probe(control, controlContainer)
			if err != nil {
				return false, err
			}
			if !live {
				return false, errors.New("policy destination lost its permitted live control")
			}
			if before {
				observed, err := probe(pod, container)
				if err != nil {
					return false, err
				}
				if observed == allowed {
					consecutive++
				} else {
					consecutive = 0
				}
			}
		}
		return consecutive >= 3, nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	h.report.Checks = append(h.report.Checks, name)
	return nil
}
