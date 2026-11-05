//go:build minikube

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServiceClusterAddressRequiresOwnedNonAffinityClusterIP(t *testing.T) {
	const source = `{"metadata":{"name":"catalog","namespace":"NAMESPACE"},"spec":{"type":"ClusterIP","clusterIP":"10.96.1.1","sessionAffinity":"None","selector":{"app.kubernetes.io/instance":"catalog","app.kubernetes.io/component":"api"},"ports":[{"protocol":"TCP","port":8080}]}}`
	good := strings.ReplaceAll(source, "NAMESPACE", namespace)
	address, err := serviceClusterAddress([]byte(good))
	if err != nil || address != "10.96.1.1:8080" {
		t.Fatal(address, err)
	}
	for _, raw := range []string{`{}`, `{`, strings.Replace(good, "10.96.1.1", "None", 1), strings.Replace(good, "10.96.1.1", "127.0.0.1", 1), strings.Replace(good, namespace, "foreign", 1), strings.Replace(good, "None", "ClientIP", 1), strings.Replace(good, "ClusterIP", "ExternalName", 1), strings.Replace(good, `"api"`, `"worker"`, 1), strings.Replace(good, "8080", "9090", 1), strings.Replace(good, "TCP", "UDP", 1)} {
		if _, err := serviceClusterAddress([]byte(raw)); err == nil {
			t.Fatal("invalid Service accepted")
		}
	}
}

func TestServiceCoverageRequiresEveryRequestAndBothKnownProcesses(t *testing.T) {
	backends := map[string]string{"one": "pod-a", "two": "pod-b"}
	good := make([]serviceRouteOutcome, 48)
	for i := range good {
		good[i] = serviceRouteOutcome{Outcome: "ok", InstanceID: "one"}
	}
	good[47].InstanceID = "two"
	counts, err := serviceRouteCoverage(good, backends)
	if err != nil || counts["one"] != 47 || counts["two"] != 1 {
		t.Fatal(counts, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]serviceRouteOutcome) []serviceRouteOutcome
	}{
		{"short", func(r []serviceRouteOutcome) []serviceRouteOutcome { return r[:47] }},
		{"unknown", func(r []serviceRouteOutcome) []serviceRouteOutcome { r[0].InstanceID = "other"; return r }},
		{"failure", func(r []serviceRouteOutcome) []serviceRouteOutcome { r[0].Outcome = "timeout"; return r }},
		{"one-backend", func(r []serviceRouteOutcome) []serviceRouteOutcome { r[47].InstanceID = "one"; return r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := serviceRouteCoverage(tc.mutate(append([]serviceRouteOutcome(nil), good...)), backends); err == nil {
				t.Fatal("incomplete coverage accepted")
			}
		})
	}
}

func TestServiceClientPolicyHasOnlyAPIEndpointAccess(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		var policy struct {
			Spec struct {
				Egress      []any    `json:"egress"`
				Ingress     []any    `json:"ingress"`
				PolicyTypes []string `json:"policyTypes"`
			} `json:"spec"`
		}
		decodePolicyObject(t, serviceClientPolicy(allowed), &policy)
		if len(policy.Spec.Ingress) != 0 || len(policy.Spec.PolicyTypes) != 2 {
			t.Fatal("client inbound isolation missing")
		}
		for _, tc := range []struct {
			ns, component, protocol string
			port                    int
			want                    bool
		}{
			{namespace, "api", "TCP", 8080, allowed}, {namespace, "api", "TCP", 9090, false}, {namespace, "worker", "TCP", 8080, false}, {"other", "api", "TCP", 8080, false}, {namespace, "api", "UDP", 8080, false},
		} {
			if egressAllows(t, policy.Spec.Egress, tc.ns, map[string]string{"app.kubernetes.io/instance": releaseName, "app.kubernetes.io/component": tc.component}, tc.protocol, tc.port) != tc.want {
				t.Fatal("wrong client egress admission")
			}
		}
	}
	h := &harness{fixture: "fixture-exact-image"}
	raw, err := json.Marshal(h.serviceClientPod())
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`"automountServiceAccountToken":false`, `"readOnlyRootFilesystem":true`, `"image":"fixture-exact-image"`, `"network-probe","hold"`} {
		if !strings.Contains(string(raw), text) {
			t.Fatal("client confinement missing", text)
		}
	}
}
