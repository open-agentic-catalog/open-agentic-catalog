//go:build helm

package helm_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUINetworkPolicyBoundaries(t *testing.T) {
	args := []string{"--values", filepath.Join(chartPath(t), "ci/ui-values.yaml")}
	objects := renderChart(t, args...)
	policy := oneObject(t, objects, "NetworkPolicy", "ui")
	spec := mapAt(t, policy, "spec")
	deployment := oneObject(t, objects, "Deployment", "ui")
	if !reflect.DeepEqual(mapAt(t, spec, "podSelector"), mapAt(t, deployment, "spec", "selector")) {
		t.Fatal("network policy does not select exactly the UI release")
	}
	assertPolicyJSON(t, spec["policyTypes"], `["Ingress","Egress"]`)
	assertPolicyJSON(t, spec["ingress"], `[]`)
	assertPolicyJSON(t, spec["egress"], `[
  {"to":[{"podSelector":{"matchLabels":{"app.kubernetes.io/name":"open-agentic-catalog","app.kubernetes.io/instance":"open-agentic-catalog","app.kubernetes.io/component":"api"}}}],"ports":[{"protocol":"TCP","port":8080}]},
  {"to":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}},"podSelector":{"matchLabels":{"k8s-app":"kube-dns"}}}],"ports":[{"protocol":"UDP","port":53},{"protocol":"TCP","port":53}]}
 ]`)
	// A different release must not gain access to the first release's API.
	other := oneObject(t, renderChartRelease(t, "other", args...), "NetworkPolicy", "ui")
	first := asMap(t, sliceAt(t, other, "spec", "egress")[0])
	peer := asMap(t, sliceAt(t, first, "to")[0])
	if stringAt(t, peer, "podSelector", "matchLabels", "app.kubernetes.io/instance") != "other" || peer["namespaceSelector"] != nil {
		t.Fatal("API egress escaped its release/namespace")
	}
	ingress := `[{"from":[{"namespaceSelector":{"matchLabels":{"purpose":"ingress"}},"podSelector":{"matchLabels":{"app":"gateway"}}}],"ports":[{"protocol":"TCP","port":8080}]}]`
	extra := `[{"to":[{"ipBlock":{"cidr":"192.0.2.42/32"}}],"ports":[{"protocol":"TCP","port":443}]}]`
	configured := oneObject(t, renderChart(t, append(args, "--set-json", "ui.networkPolicy.ingress="+ingress, "--set-json", "ui.networkPolicy.extraEgress="+extra)...), "NetworkPolicy", "ui")
	assertPolicyJSON(t, mapAt(t, configured, "spec")["ingress"], ingress)
	egress := sliceAt(t, configured, "spec", "egress")
	if len(egress) != 3 || !reflect.DeepEqual(egress[:2], sliceAt(t, spec, "egress")) {
		t.Fatal("extra egress changed default rules")
	}
	assertPolicyJSON(t, egress[2:], extra)
	for _, disabled := range []string{"ui.enabled=false", "ui.networkPolicy.enabled=false"} {
		rendered := renderChart(t, append(args, "--set", disabled)...)
		if len(objectsByKind(rendered, "NetworkPolicy")) != 0 {
			t.Fatal("disabled policy still rendered")
		}
		if disabled == "ui.networkPolicy.enabled=false" {
			_ = oneObject(t, rendered, "Deployment", "ui")
		}
	}
}

func assertPolicyJSON(t *testing.T, actual any, expected string) {
	t.Helper()
	raw, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("policy boundary mismatch: got %s, want %s", raw, expected)
	}
}
