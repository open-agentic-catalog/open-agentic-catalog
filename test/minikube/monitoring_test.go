//go:build minikube

// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMonitoringDashboardInventory catches fixture drift before starting a cluster.
func TestMonitoringDashboardInventory(t *testing.T) {
	raw, err := os.ReadFile("../../deployments/monitoring/dashboards/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		UID    string `json:"uid"`
		Panels []struct {
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.UID != "catalog-overview" || len(dashboard.Panels) != monitoringDashboardPanels {
		t.Fatal("update the Minikube inventory to match the shared dashboard")
	}
	for _, panel := range dashboard.Panels {
		if len(panel.Targets) != 1 || panel.Targets[0].Expr == "" {
			t.Fatal("expected one nonempty query per shared dashboard panel")
		}
	}
}

func TestMonitoringValuesKeepClaimsAndCredentialsExternal(t *testing.T) {
	h := &harness{temporary: t.TempDir(), suffix: "owned"}
	for _, persistent := range []bool{false, true, false} {
		path, err := h.monitoringValues(persistent)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- generated values in this test's temporary directory.
		if err != nil {
			t.Fatal(err)
		}
		var values struct {
			Grafana struct {
				AdminSecret map[string]string `json:"adminSecret"`
				Storage     map[string]string `json:"storage"`
			} `json:"grafana"`
			Prometheus struct {
				Storage map[string]string `json:"storage"`
			} `json:"prometheus"`
			Security map[string]int `json:"podSecurityContext"`
		}
		if err := json.Unmarshal(raw, &values); err != nil {
			t.Fatal(err)
		}
		if len(values.Grafana.AdminSecret) != 1 || values.Grafana.AdminSecret["name"] != "monitoring.admin" || (persistent && values.Security["runAsUser"] != 1000730000) || (!persistent && len(values.Security) != 0) {
			t.Fatal("credential reference or arbitrary identity boundary changed")
		}
		for _, claim := range []struct{ got, want string }{
			{values.Prometheus.Storage["existingClaim"], "monitoring.prometheus-history"},
			{values.Grafana.Storage["existingClaim"], "monitoring.grafana-history"},
		} {
			want := ""
			if persistent {
				want = claim.want
			}
			if claim.got != want {
				t.Fatal("persistent selection changed the exact external claim reference")
			}
		}
		if persistent && values.Prometheus.Storage["existingClaim"] == values.Grafana.Storage["existingClaim"] {
			t.Fatal("components share a writable claim")
		}
	}
}

func TestMonitoringCrossNamespaceAuthority(t *testing.T) {
	const role = `{"metadata":{"namespace":"open-agentic-catalog-e2e"},"rules":[{"apiGroups":[""],"resources":["pods"],"verbs":["get","list","watch"]}]}`
	const binding = `{"metadata":{"namespace":"open-agentic-catalog-e2e"},"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":"collector-discovery"},"subjects":[{"kind":"ServiceAccount","name":"collector-prometheus","namespace":"catalog-monitoring"}]}`
	for _, tc := range []struct {
		name, kind, raw string
		bad             bool
	}{
		{"Pod reader", "role", role, false},
		{"cross namespace subject", "rolebinding", binding, false},
		{"extra resource", "role", strings.Replace(role, `["pods"]`, `["pods","secrets"]`, 1), true},
		{"extra verb", "role", strings.Replace(role, `"watch"`, `"watch","create"`, 1), true},
		{"wrong namespace", "role", strings.Replace(role, `"open-agentic-catalog-e2e"`, `"catalog-monitoring"`, 1), true},
		{"wrong subject namespace", "rolebinding", strings.Replace(binding, `"catalog-monitoring"`, `"open-agentic-catalog-e2e"`, 1), true},
		{"cluster role", "rolebinding", strings.Replace(binding, `"Role"`, `"ClusterRole"`, 1), true},
		{"wrong role", "rolebinding", strings.Replace(binding, `collector-discovery`, `other`, 1), true},
		{"wrong identity", "rolebinding", strings.Replace(binding, `collector-prometheus`, `installer`, 1), true},
		{"unknown kind", "unknown", binding, true},
		{"malformed", "role", `{`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := monitoringAuthorityDocument([]byte(tc.raw), tc.kind, "collector"); (err != nil) != tc.bad {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMonitoringCrossNamespacePrivateInputs(t *testing.T) {
	const prom = `{"items":[{"spec":{"serviceAccountName":"collector-prometheus","automountServiceAccountToken":true,"containers":[{}],"volumes":[{"projected":{"sources":[{"serviceAccountToken":{"path":"token"}}]}}]}}]}`
	const graf = `{"items":[{"spec":{"serviceAccountName":"collector-grafana","automountServiceAccountToken":false,"containers":[{}],"volumes":[{"secret":{"secretName":"monitoring.admin"}}]}}]}`
	for _, tc := range []struct {
		name, component, raw string
		bad                  bool
	}{
		{"collector token", "prometheus", prom, false},
		{"separate admin Secret", "grafana", graf, false},
		{"Grafana token", "grafana", strings.Replace(graf, `false`, `true`, 1), true},
		{"implicit token", "grafana", strings.Replace(graf, `"automountServiceAccountToken":false,`, ``, 1), true},
		{"database Secret", "grafana", strings.Replace(graf, `monitoring.admin`, `database`, 1), true},
		{"prometheus Secret", "prometheus", strings.Replace(prom, `"projected":{"sources":[{"serviceAccountToken":{"path":"token"}}]}`, `"secret":{"secretName":"database"}`, 1), true},
		{"projected Secret", "prometheus", strings.Replace(prom, `"serviceAccountToken":{"path":"token"}`, `"secret":{"name":"database"}`, 1), true},
		{"environment credential", "grafana", strings.Replace(graf, `"containers":[{}]`, `"containers":[{"env":[{"valueFrom":{"secretKeyRef":{"name":"database"}}}]}]`, 1), true},
		{"environment import", "grafana", strings.Replace(graf, `"containers":[{}]`, `"containers":[{"envFrom":[{"secretRef":{"name":"database"}}]}]`, 1), true},
		{"wrong account", "grafana", strings.Replace(graf, `collector-grafana`, `installer`, 1), true},
		{"missing Pod", "grafana", `{"items":[]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := monitoringPrivatePodInputs([]byte(tc.raw), tc.component, "collector"); (err != nil) != tc.bad {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMonitoringTargetsRejectDecoysAndStaleIdentities(t *testing.T) {
	h := &harness{suffix: "owned"}
	for _, tc := range []string{"valid", "missing", "wrong namespace", "wrong release", "wrong role", "wrong deployment", "duplicate instance", "duplicate UID", "old UID", "unhealthy"} {
		t.Run(tc, func(t *testing.T) {
			var targets []monitoringTarget
			for i, role := range []string{"api", "api", "worker", "worker"} {
				targets = append(targets, monitoringTarget{Health: "up", Labels: map[string]string{"instance": fmt.Sprintf("pod-%d:8080", i), "catalog_deployment": "mk-owned", "catalog_role": role}, Discovered: map[string]string{"__meta_kubernetes_namespace": namespace, "__meta_kubernetes_pod_label_app_kubernetes_io_instance": releaseName, "__meta_kubernetes_pod_uid": fmt.Sprintf("uid-%d", i)}})
			}
			switch tc {
			case "missing":
				targets = targets[:3]
			case "wrong namespace":
				targets[0].Discovered["__meta_kubernetes_namespace"] = monitoringNamespace
			case "wrong release":
				targets[0].Discovered["__meta_kubernetes_pod_label_app_kubernetes_io_instance"] = "other"
			case "wrong role":
				targets[0].Labels["catalog_role"] = "migration"
			case "wrong deployment":
				targets[0].Labels["catalog_deployment"] = "other"
			case "duplicate instance":
				targets[0].Labels["instance"] = targets[1].Labels["instance"]
			case "duplicate UID":
				targets[0].Discovered["__meta_kubernetes_pod_uid"] = "uid-1"
			case "old UID":
				targets[0].Discovered["__meta_kubernetes_pod_uid"] = "old"
			case "unhealthy":
				targets[0].Health = "down"
			}
			if err := h.validateMonitoringTargets(targets, "uid-0", "uid-1", "uid-2", "uid-3"); (err == nil) != (tc == "valid") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMonitoringValuesSelectExactCrossNamespaceTarget(t *testing.T) {
	h := &harness{temporary: t.TempDir(), suffix: "owned"}
	path, err := h.monitoringValuesIn(false, namespace)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- generated values in this test's temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Target struct {
			Namespace       string `json:"namespace"`
			Release         string `json:"release"`
			DeploymentLabel string `json:"deploymentLabel"`
		} `json:"target"`
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	if values.Target.Namespace != namespace || values.Target.Release != releaseName || values.Target.DeploymentLabel != "mk-owned" {
		t.Fatal("cross-namespace target identity changed")
	}
}

func TestMonitoringDiscoveryFailureRequiresExactContext(t *testing.T) {
	collector := "system:serviceaccount:catalog-monitoring:collector-prometheus"
	good := `pods is forbidden: User "` + collector + `" cannot list resource "pods" in namespace "` + namespace + `"`
	for _, tc := range []struct {
		name, message string
		want          bool
	}{
		{"exact denial", good, true},
		{"different error", "connection refused: " + good[len("pods is forbidden: "):], false},
		{"different identity", strings.ReplaceAll(good, collector, "other-collector"), false},
		{"identity prefix", strings.ReplaceAll(good, collector, collector+"-other"), false},
		{"escaped exact denial", strings.ReplaceAll(good, `"`, `\"`), true},
		{"different namespace", strings.ReplaceAll(good, namespace, "other-namespace"), false},
		{"namespace prefix", strings.ReplaceAll(good, namespace, namespace+"-other"), false},
		{"different Pod operation", strings.ReplaceAll(good, "cannot list", "cannot delete"), false},
		{"different resource", strings.ReplaceAll(good, "pods", "secrets"), false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := monitoringDiscoveryForbidden(tc.message, collector); got != tc.want {
				t.Fatalf("denial = %t", got)
			}
		})
	}
}

func TestMonitoringBindingRestorationPreservesScopeAndOwnership(t *testing.T) {
	const binding = `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"RoleBinding","metadata":{"name":"collector-discovery","namespace":"open-agentic-catalog-e2e","uid":"old-uid","resourceVersion":"12","creationTimestamp":"2026-11-01T00:00:00Z","managedFields":[{}],"generation":1,"selfLink":"old","labels":{"app.kubernetes.io/instance":"monitoring"},"annotations":{"meta.helm.sh/release-name":"monitoring","meta.helm.sh/release-namespace":"catalog-monitoring"}},"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":"collector-discovery"},"subjects":[{"kind":"ServiceAccount","name":"collector-prometheus","namespace":"catalog-monitoring"}]}`
	object, err := monitoringRestorableBinding([]byte(binding), "collector")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	var restored struct {
		Metadata struct {
			Name        string            `json:"name"`
			Namespace   string            `json:"namespace"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Metadata.Name != "collector-discovery" || restored.Metadata.Namespace != namespace || restored.Metadata.Labels["app.kubernetes.io/instance"] != "monitoring" || restored.Metadata.Annotations["meta.helm.sh/release-name"] != "monitoring" || restored.Metadata.Annotations["meta.helm.sh/release-namespace"] != monitoringNamespace {
		t.Fatal("restoration changed Helm ownership or target identity")
	}
	if err := monitoringAuthorityDocument(raw, "rolebinding", "collector"); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"uid", "resourceVersion", "creationTimestamp", "managedFields", "generation", "selfLink"} {
		if bytes.Contains(raw, []byte(`"`+field+`":`)) {
			t.Fatalf("server field remains: %s", field)
		}
	}
	for _, invalid := range []string{strings.Replace(binding, `"name":"collector-discovery"`, `"name":"other-discovery"`, 1), strings.Replace(binding, `"kind":"Role"`, `"kind":"ClusterRole"`, 1), strings.Replace(binding, `"namespace":"catalog-monitoring"`, `"namespace":"other"`, 1)} {
		if _, err := monitoringRestorableBinding([]byte(invalid), "collector"); err == nil {
			t.Fatal("restoration accepted a changed binding")
		}
	}
}

func TestMonitoringRemovalRequiresExplicitEmptyInventory(t *testing.T) {
	for _, tc := range []struct {
		raw string
		bad bool
	}{{`{"items":[]}`, false}, {`{"items":[{"kind":"RoleBinding"}]}`, true}, {`{}`, true}, {`{"items":null}`, true}, {`not-json`, true}} {
		if err := monitoringEmptyInventory([]byte(tc.raw)); (err != nil) != tc.bad {
			t.Fatalf("inventory %s: %v", tc.raw, err)
		}
	}
}
