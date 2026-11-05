//go:build minikube

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const monitoringNamespace = "catalog-monitoring"

// verifyCrossNamespaceMonitoring reuses the scenario's Catalog and image/storage
// qualification, but gives the collector its own namespace and ephemeral state.
func (h *harness) verifyCrossNamespaceMonitoring(archive, password string, client *http.Client) error {
	if err := h.applyObject("create owned monitoring namespace", kubernetesObject{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": monitoringNamespace, "labels": testLabels("monitoring")}}); err != nil {
		return err
	}
	if err := h.applyObject("create separate monitoring credential", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]string{"name": "monitoring.admin", "namespace": monitoringNamespace}, "type": "Opaque", "data": map[string]string{"admin-user": base64.StdEncoding.EncodeToString([]byte("catalog")), "admin-password": base64.StdEncoding.EncodeToString([]byte(password))}}); err != nil {
		return err
	}
	// Only the decoy uses the existing test server material, never the collectors.
	material, err := h.kubectl("read owned decoy TLS fixture material", "-n", namespace, "get", "secret/local-fixture-material", "-o", "json")
	if err != nil {
		return err
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(material, &secret); err != nil {
		return err
	}
	if err := h.applyObject("create separate decoy TLS fixture material", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]string{"name": "local-fixture-material", "namespace": monitoringNamespace}, "data": secret.Data}); err != nil {
		return err
	}
	if err := h.applyObject("create tokenless decoy identity", kubernetesObject{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]string{"name": "fixture", "namespace": monitoringNamespace}, "automountServiceAccountToken": false}); err != nil {
		return err
	}
	// This running Pod has all target labels and the named port, but lives outside
	// target.namespace. Existing target-namespace decoys cover wrong role/release.
	fixture := h.fixtureDeployment("monitoring-namespace-decoy", "decoy", nil)
	deployment, ok := fixture["spec"].(map[string]any)
	if !ok {
		return errors.New("fixture deployment spec is missing")
	}
	template, ok := deployment["template"].(map[string]any)
	if !ok {
		return errors.New("fixture template is missing")
	}
	spec, ok := template["spec"].(map[string]any)
	if !ok {
		return errors.New("fixture Pod spec is missing")
	}
	containers, ok := spec["containers"].([]any)
	if !ok || len(containers) != 1 {
		return errors.New("fixture container is missing")
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		return errors.New("fixture container spec is missing")
	}
	container["ports"] = []any{map[string]any{"name": "http", "containerPort": 8081}}
	if err := h.applyObject("create matching-label collector namespace decoy", kubernetesObject{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "monitoring-namespace-decoy", "namespace": monitoringNamespace, "labels": map[string]string{"app.kubernetes.io/instance": releaseName, "app.kubernetes.io/component": "api"}}, "spec": spec}); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for collector namespace decoy", "-n", monitoringNamespace, "wait", "pod/monitoring-namespace-decoy", "--for=condition=Ready", "--timeout=1m"); err != nil {
		return err
	}
	values, err := h.monitoringValuesIn(false, namespace)
	if err != nil {
		return err
	}
	if _, err := h.helm("install independent cross-namespace collector", "install", monitoringRelease, archive, "-n", monitoringNamespace, "-f", values, "--wait", "--timeout=4m"); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(monitoringNamespace + "/" + monitoringRelease))
	name := fmt.Sprintf("cm-%s-%x", monitoringRelease, hash[:4])
	if err := h.monitoringCrossNamespaceAuthority(name); err != nil {
		return err
	}
	address, err := h.startPortForwardIn(monitoringNamespace, "service/"+name+"-prometheus", 9090)
	if err != nil {
		return err
	}
	prometheus := monitoringClient{client: client, base: "http://" + address}
	if err := h.monitoringCurrentSamples(prometheus); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "monitoring-cross-namespace-exact-rbac-and-private-inputs", "monitoring-cross-namespace-four-current-targets-and-fresh-samples")
	// An unsatisfied custom readiness gate keeps the processes running. Recreate
	// prevents transient surge Pods from obscuring the exact four-target assertion.
	if _, err := h.kubectl("make owned API Pods running but unready", "-n", namespace, "patch", "deployment/catalog-api", "--type=merge", "-p", `{"spec":{"strategy":{"type":"Recreate","rollingUpdate":null},"template":{"spec":{"readinessGates":[{"conditionType":"catalog.test/diagnostic-ready"}]}}}}`); err != nil {
		return err
	}
	if err := h.monitoringEventually("running unready API Pods", func() error {
		pods, err := h.pods("api")
		if err != nil {
			return err
		}
		if len(pods) != 2 {
			return errors.New("expected two API Pods")
		}
		for _, pod := range pods {
			if pod.Ready {
				return errors.New("API Pod is not running and unready")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := h.monitoringCurrentSamples(prometheus); err != nil {
		return err
	}
	if _, err := h.kubectl("restore owned API readiness", "-n", namespace, "patch", "deployment/catalog-api", "--type=merge", "-p", `{"spec":{"template":{"spec":{"readinessGates":null}}}}`); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for restored API readiness", "-n", namespace, "rollout", "status", "deployment/catalog-api", "--timeout=2m"); err != nil {
		return err
	}
	if err := h.monitoringCurrentSamples(prometheus); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "monitoring-cross-namespace-running-unready-targets")
	return h.verifyMonitoringDiscoveryRecovery(archive, name, client)
}

// monitoringCurrentSamples requires current Pod identities and source timestamps.
// Prometheus instant-vector evaluation times alone cannot establish freshness.
func (h *harness) monitoringCurrentSamples(client monitoringClient) error {
	return h.monitoringCurrentSamplesSince(client, time.Time{})
}

func (h *harness) monitoringCurrentSamplesSince(client monitoringClient, since time.Time) error {
	var uids []string
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return err
		}
		if len(pods) != 2 {
			return fmt.Errorf("expected two current %s Pods", role)
		}
		for _, pod := range pods {
			uids = append(uids, pod.UID)
		}
	}
	return h.monitoringEventually("four current cross-namespace samples", func() error {
		targets, err := h.monitoringTargetInventory(client)
		if err != nil {
			return err
		}
		if err := h.validateMonitoringTargets(targets, uids...); err != nil {
			return err
		}
		for _, target := range targets {
			metric := `open_agentic_catalog_go_heap_bytes{catalog_deployment=` + strconv.Quote("mk-"+h.suffix) + `,catalog_role=` + strconv.Quote(target.Labels["catalog_role"]) + `,instance=` + strconv.Quote(target.Labels["instance"]) + `}`
			// timestamp() returns the source sample time. Check each current target,
			// so a fresh but replaced instance cannot substitute for a missing one.
			expression := metric + ` and (time() - timestamp(` + metric + `) >= 0) and (time() - timestamp(` + metric + `) < 20)`
			if !since.IsZero() {
				expression += ` and (timestamp(` + metric + `) > ` + monitoringTimestamp(since) + `)`
			}
			if err := h.monitoringQuery(client, expression, "", 1); err != nil {
				return err
			}
		}
		return nil
	})
}

func (h *harness) monitoringCrossNamespaceAuthority(name string) error {
	for _, kind := range []string{"role", "rolebinding"} {
		raw, err := h.kubectl("read target namespace discovery authority", "-n", namespace, "get", kind, name+"-discovery", "-o", "json")
		if err != nil {
			return err
		}
		if err := monitoringAuthorityDocument(raw, kind, name); err != nil {
			return err
		}
	}
	for _, verb := range []string{"get", "list", "watch", "create", "delete", "patch", "update"} {
		for _, selected := range []struct{ resource, namespace string }{{"pods", namespace}, {"secrets", namespace}, {"pods", monitoringNamespace}, {"secrets", monitoringNamespace}, {"nodes", namespace}} {
			want := "no"
			if selected.resource == "pods" && selected.namespace == namespace && (verb == "get" || verb == "list" || verb == "watch") {
				want = "yes"
			}
			raw, err := h.kubectl("check live collector authority", "auth", "can-i", verb, selected.resource, "-n", selected.namespace, "--as=system:serviceaccount:"+monitoringNamespace+":"+name+"-prometheus")
			if strings.TrimSpace(string(raw)) != want && (want != "no" || err == nil || !strings.HasSuffix(strings.TrimSpace(err.Error()), "\nno")) {
				return fmt.Errorf("cross-namespace collector authority differs: %s %s in %s", verb, selected.resource, selected.namespace)
			}
			if want == "yes" && err != nil {
				return err
			}
		}
	}
	for _, component := range []string{"prometheus", "grafana"} {
		raw, err := h.kubectl("read live collector Pod inputs", "-n", monitoringNamespace, "get", "pods", "-l", "app.kubernetes.io/instance="+monitoringRelease+",app.kubernetes.io/component="+component, "-o", "json")
		if err != nil {
			return err
		}
		if err := monitoringPrivatePodInputs(raw, component, name); err != nil {
			return err
		}
	}
	return nil
}

func monitoringAuthorityDocument(raw []byte, kind, name string) error {
	var document struct {
		Metadata struct {
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Rules []struct {
			APIGroups       []string `json:"apiGroups"`
			Resources       []string `json:"resources"`
			Verbs           []string `json:"verbs"`
			ResourceNames   []string `json:"resourceNames"`
			NonResourceURLs []string `json:"nonResourceURLs"`
		} `json:"rules"`
		RoleRef struct {
			APIGroup string `json:"apiGroup"`
			Kind     string `json:"kind"`
			Name     string `json:"name"`
		} `json:"roleRef"`
		Subjects []struct {
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"subjects"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return err
	}
	if document.Metadata.Namespace != namespace {
		return errors.New("discovery authority is outside target namespace")
	}
	switch kind {
	case "role":
		if len(document.Rules) != 1 {
			return errors.New("collector must have exactly one discovery rule")
		}
		rule := document.Rules[0]
		if len(rule.APIGroups) != 1 || rule.APIGroups[0] != "" || len(rule.Resources) != 1 || rule.Resources[0] != "pods" || strings.Join(rule.Verbs, ",") != "get,list,watch" || len(rule.ResourceNames) != 0 || len(rule.NonResourceURLs) != 0 {
			return errors.New("collector rule exceeds Pod read authority")
		}
	case "rolebinding":
		if document.RoleRef.APIGroup != "rbac.authorization.k8s.io" || document.RoleRef.Kind != "Role" || document.RoleRef.Name != name+"-discovery" || len(document.Subjects) != 1 {
			return errors.New("incorrect discovery RoleBinding")
		}
		subject := document.Subjects[0]
		if subject.Kind != "ServiceAccount" || subject.Name != name+"-prometheus" || subject.Namespace != monitoringNamespace {
			return errors.New("discovery subject is not the separate collector")
		}
	default:
		return errors.New("unknown authority document")
	}
	return nil
}

func monitoringPrivatePodInputs(raw []byte, component, name string) error {
	var inventory struct {
		Items []struct {
			Spec struct {
				ServiceAccountName string `json:"serviceAccountName"`
				Automount          *bool  `json:"automountServiceAccountToken"`
				Containers         []struct {
					EnvFrom []json.RawMessage `json:"envFrom"`
					Env     []struct {
						ValueFrom json.RawMessage `json:"valueFrom"`
					} `json:"env"`
				} `json:"containers"`
				Volumes []struct {
					Secret *struct {
						Name string `json:"secretName"`
					} `json:"secret"`
					Projected *struct {
						Sources []struct {
							Token  json.RawMessage `json:"serviceAccountToken"`
							Secret json.RawMessage `json:"secret"`
						} `json:"sources"`
					} `json:"projected"`
				} `json:"volumes"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		return err
	}
	if len(inventory.Items) != 1 {
		return errors.New("expected one separate collector Pod")
	}
	spec := inventory.Items[0].Spec
	if spec.ServiceAccountName != name+"-"+component || spec.Automount == nil || *spec.Automount != (component == "prometheus") || len(spec.Containers) != 1 {
		return errors.New("collector Pod identity or token mount differs")
	}
	for _, container := range spec.Containers {
		if len(container.EnvFrom) != 0 {
			return errors.New("collector has unexpected environment source")
		}
		for _, env := range container.Env {
			if len(env.ValueFrom) != 0 {
				return errors.New("collector has unexpected environment credential")
			}
		}
	}
	admin := 0
	for _, volume := range spec.Volumes {
		if volume.Secret != nil {
			if component != "grafana" || volume.Secret.Name != "monitoring.admin" {
				return errors.New("collector mounts an unexpected Secret")
			}
			admin++
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if len(source.Secret) != 0 || (component == "grafana" && len(source.Token) != 0) {
					return errors.New("collector has unexpected projected credentials")
				}
			}
		}
	}
	if (component == "grafana" && admin != 1) || (component == "prometheus" && admin != 0) {
		return errors.New("collector admin credential isolation differs")
	}
	return nil
}
