//go:build minikube

package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"testing"
)

// Test the generated policy's allowed destinations, including its denial controls.
func TestRuntimeEgressAllowsFederationAndKeepsIsolation(t *testing.T) {
	h := &harness{fixture: "owned-fixture:test"}
	primary := fixturePolicyLabels(t, h.fixtureDeployment("identity-fixture", "identity", nil))
	decoy := fixturePolicyLabels(t, h.fixtureDeployment("decoy-fixture", "decoy", nil))
	foreign := maps.Clone(primary)
	foreign["app.kubernetes.io/name"] = "unrelated-application"
	peers := map[string]map[string]string{
		"primary": primary, "decoy": decoy, "foreign": foreign,
		"postgres":  {"app.kubernetes.io/name": "open-agentic-catalog-e2e", "app.kubernetes.io/component": "postgres"},
		"pgbouncer": {"app.kubernetes.io/instance": "catalog", "app.kubernetes.io/component": "pgbouncer"},
		"dns":       {"k8s-app": "kube-dns"},
	}
	// The Service's component label is not its endpoint's component label.
	// Read the actual dependency objects before checking network admission.
	var service struct {
		Spec struct {
			Selector map[string]string `json:"selector"`
			Ports    []struct {
				Port       int    `json:"port"`
				TargetPort string `json:"targetPort"`
			} `json:"ports"`
		} `json:"spec"`
	}
	found := false
	for _, object := range h.dependencyObjects() {
		metadata, ok := object["metadata"].(map[string]any)
		if !ok || metadata["name"] != "federation-fixture" || object["kind"] != "Service" {
			continue
		}
		decodePolicyObject(t, object, &service)
		found = true
	}
	if !found || !policyLabelsMatch(service.Spec.Selector, primary) || policyLabelsMatch(service.Spec.Selector, decoy) || len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port != 8443 || service.Spec.Ports[0].TargetPort != "federation" {
		t.Fatal("federation Service must select the primary fixture's named HTTPS port, excluding the decoy")
	}
	for _, pooler := range []bool{false, true} {
		for _, role := range []string{"api", "worker"} {
			t.Run(fmt.Sprintf("pooler=%v/%s", pooler, role), func(t *testing.T) {
				rules := runtimeEgressRules(pooler)[role]
				for _, test := range []struct {
					name, targetNamespace, peer, protocol string
					port                                  int
					allowed                               bool
				}{
					{"federation HTTPS", namespace, "primary", "TCP", 8443, true},
					{"federation wrong port", namespace, "primary", "TCP", 8444, false},
					{"federation wrong protocol", namespace, "primary", "UDP", 8443, false},
					{"foreign federation", "another-tenant", "primary", "TCP", 8443, false},
					{"unrelated application", namespace, "foreign", "TCP", 8443, false},
					{"decoy HTTPS", namespace, "decoy", "TCP", 8443, false},
					{"identity HTTP", namespace, "primary", "TCP", 8081, role == "api"},
					{"direct database", namespace, "postgres", "TCP", 5432, !pooler},
					{"session pooler", namespace, "pgbouncer", "TCP", 6432, pooler},
					{"DNS UDP", "kube-system", "dns", "UDP", 53, true},
					{"DNS TCP", "kube-system", "dns", "TCP", 53, true},
					{"unselected DNS", namespace, "dns", "UDP", 53, false},
				} {
					t.Run(test.name, func(t *testing.T) {
						if got := egressAllows(t, rules, test.targetNamespace, peers[test.peer], test.protocol, test.port); got != test.allowed {
							t.Fatalf("destination admission = %v, want %v", got, test.allowed)
						}
					})
				}
			})
		}
	}
}

type testPolicySelector struct {
	MatchLabels map[string]string `json:"matchLabels"`
}

// Evaluate the fixture's label selectors and ports against independently selected peers.
func egressAllows(t *testing.T, rules []any, targetNamespace string, labels map[string]string, protocol string, port int) bool {
	t.Helper()
	var parsed []struct {
		To []struct {
			NamespaceSelector *testPolicySelector `json:"namespaceSelector"`
			PodSelector       *testPolicySelector `json:"podSelector"`
		} `json:"to"`
		Ports []struct {
			Protocol string `json:"protocol"`
			Port     int    `json:"port"`
		} `json:"ports"`
	}
	decodePolicyObject(t, rules, &parsed)
	matches := func(selector *testPolicySelector, actual map[string]string) bool {
		return selector == nil || policyLabelsMatch(selector.MatchLabels, actual)
	}
	for _, rule := range parsed {
		for _, peer := range rule.To {
			sameNamespace := peer.NamespaceSelector == nil && targetNamespace == namespace
			namedNamespace := peer.NamespaceSelector != nil && matches(peer.NamespaceSelector, map[string]string{"kubernetes.io/metadata.name": targetNamespace})
			if !(sameNamespace || namedNamespace) || !matches(peer.PodSelector, labels) {
				continue
			}
			for _, allowed := range rule.Ports {
				if allowed.Protocol == protocol && allowed.Port == port {
					return true
				}
			}
		}
	}
	return false
}

func policyLabelsMatch(selector, actual map[string]string) bool {
	for name, value := range selector {
		if actual[name] != value {
			return false
		}
	}
	return true
}

func fixturePolicyLabels(t *testing.T, object kubernetesObject) map[string]string {
	t.Helper()
	var deployment struct {
		Spec struct {
			Template struct {
				Metadata struct {
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
				Spec struct {
					Containers []struct {
						Ports []struct {
							Name          string `json:"name"`
							ContainerPort int    `json:"containerPort"`
						} `json:"ports"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	decodePolicyObject(t, object, &deployment)
	if deployment.Spec.Template.Metadata.Labels["open-agentic-catalog.github.io/fixture"] == "primary" {
		found := false
		for _, container := range deployment.Spec.Template.Spec.Containers {
			for _, port := range container.Ports {
				found = found || port.Name == "federation" && port.ContainerPort == 8443
			}
		}
		if !found {
			t.Fatal("primary fixture has no named federation HTTPS port")
		}
	}
	return deployment.Spec.Template.Metadata.Labels
}

func decodePolicyObject(t *testing.T, object, target any) {
	t.Helper()
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}
