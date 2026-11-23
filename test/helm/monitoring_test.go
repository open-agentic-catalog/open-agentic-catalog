//go:build helm

package helm_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func monitoringPackage(t *testing.T) string {
	t.Helper()
	root := filepath.Clean(filepath.Join(chartPath(t), "..", "..", ".."))
	command := exec.CommandContext(t.Context(), "python3", filepath.Join(root, "test", "monitoring", "helm.py"), "package", "--output", t.TempDir()) // #nosec G204 -- repository-owned packager and test-owned output directory.
	raw, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("package monitoring: %v\n%s", err, raw)
	}
	return strings.TrimSpace(string(raw))
}

func monitoringArguments(archive string, extra ...string) []string {
	return append([]string{"template", "9-monitor", archive, "--namespace", "monitoring", "--kube-version", kubeVersion(),
		"--set", "enabled=true",
		"--set", "target.release=catalog", "--set", "target.deploymentLabel=cluster/application/catalog",
		"--set", "grafana.adminSecret.name=monitor-admin"}, extra...)
}

func monitoringObjects(t *testing.T, archive string, extra ...string) []object {
	t.Helper()
	raw, err := helm(t, monitoringArguments(archive, extra...)...)
	if err != nil {
		t.Fatalf("monitoring render: %v\n%s", err, raw)
	}
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	var result []object
	for {
		var resource object
		if err := decoder.Decode(&resource); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(resource) > 0 {
			result = append(result, resource)
		}
	}
	return result
}

// Read the file selected by Prometheus through the Deployment's ConfigMap mount.
func prometheusConfigFile(t *testing.T, objects []object) string {
	t.Helper()
	pod := mapAt(t, oneObject(t, objects, "Deployment", "prometheus"), "spec", "template", "spec")
	container := namedMap(t, sliceAt(t, pod, "containers"), "prometheus")
	mount := namedMap(t, sliceAt(t, container, "volumeMounts"), "config")
	volume := namedMap(t, sliceAt(t, pod, "volumes"), stringAt(t, mount, "name"))
	source := mapAt(t, volume, "configMap")
	files := renderedConfigMap(t, objects, stringAt(t, source, "name"))
	args := stringSlice(t, container["args"])
	for _, item := range sliceAt(t, source, "items") {
		filename := path.Join(stringAt(t, mount, "mountPath"), stringAt(t, item, "path"))
		if slices.Contains(args, "--config.file="+filename) {
			key := stringAt(t, item, "key")
			if files[key] == "" {
				t.Fatalf("Prometheus config file %q references missing or empty ConfigMap key %q", filename, key)
			}
			return files[key]
		}
	}
	t.Fatalf("Prometheus --config.file does not select a projected file from ConfigMap %q", source["name"])
	return ""
}

func prometheusScrapeConfig(t *testing.T, objects []object) (global, job map[string]any) {
	t.Helper()
	var config object
	if err := yaml.Unmarshal([]byte(prometheusConfigFile(t, objects)), &config); err != nil {
		t.Fatal(err)
	}
	jobs := sliceAt(t, config, "scrape_configs")
	if len(jobs) != 1 || stringAt(t, jobs[0], "job_name") != "catalog-pods" {
		t.Fatalf("expected one catalog-pods scrape job, got %v", jobs)
	}
	return mapAt(t, config, "global"), asMap(t, jobs[0])
}

func TestMonitoringPackagedRender(t *testing.T) {
	archive := monitoringPackage(t)
	if output, err := helm(t, "lint", "--strict", archive, "--set", "enabled=true", "--set", "target.release=catalog", "--set", "target.deploymentLabel=test", "--set", "grafana.adminSecret.name=admin"); err != nil {
		t.Fatalf("lint: %v\n%s", err, output)
	}
	objects := monitoringObjects(t, archive, "--set", "target.namespace=application")
	if len(objects) != 10 {
		t.Fatalf("monitoring inventory = %d, want 10", len(objects))
	}
	for _, resource := range objects {
		kind := stringAt(t, resource, "kind")
		if !slices.Contains([]string{"ConfigMap", "Deployment", "Service", "ServiceAccount", "Role", "RoleBinding"}, kind) {
			t.Fatalf("unexpected monitoring resource %s", kind)
		}
		if len(stringAt(t, resource, "metadata", "name")) > 63 {
			t.Fatal("generated resource name too long")
		}
		wantNamespace := "monitoring"
		if kind == "Role" || kind == "RoleBinding" {
			wantNamespace = "application"
		}
		if stringAt(t, resource, "metadata", "namespace") != wantNamespace {
			t.Fatal("resource escaped selected namespace")
		}
	}
	for _, component := range []string{"prometheus", "grafana"} {
		deployment := oneObject(t, objects, "Deployment", component)
		pod := mapAt(t, deployment, "spec", "template", "spec")
		if boolAt(t, pod, "automountServiceAccountToken") != (component == "prometheus") {
			t.Fatal("collector identity token boundary")
		}
		security := mapAt(t, pod, "securityContext")
		if !boolAt(t, security, "runAsNonRoot") || security["runAsUser"] != 65534 || security["runAsGroup"] != 65534 || security["fsGroup"] != 65534 || pod["initContainers"] != nil {
			t.Fatal("monitoring lost numeric non-root defaults or added an init container")
		}
		containers := sliceAt(t, pod, "containers")
		if len(containers) != 1 {
			t.Fatal("unexpected helper container")
		}
		container := asMap(t, containers[0])
		if !strings.Contains(stringAt(t, container, "image"), "@sha256:") || !boolAt(t, container, "securityContext", "readOnlyRootFilesystem") || boolAt(t, container, "securityContext", "allowPrivilegeEscalation") {
			t.Fatal("image/security boundary")
		}
		if stringAt(t, deployment, "spec", "strategy", "type") != "Recreate" {
			t.Fatal("local data directory allows overlapping writers")
		}
		if component == "prometheus" {
			arguments := stringSlice(t, container["args"])
			for _, bound := range []string{"--query.timeout=10s", "--query.max-concurrency=4", "--query.max-samples=1000000", "--web.max-connections=32"} {
				if !slices.Contains(arguments, bound) {
					t.Errorf("missing query bound %s", bound)
				}
			}
		}
		service := oneObject(t, objects, "Service", component)
		assertSelectorMatches(t, service, deployment)
		if stringAt(t, service, "spec", "type") != "ClusterIP" {
			t.Fatal("monitoring Service is public")
		}
	}
	scrape := prometheusConfigFile(t, objects)
	for _, want := range []string{"catalog_deployment", "cluster/application/catalog", "api;http|worker;operations", "sample_limit: 10000"} {
		if !strings.Contains(scrape, want) {
			t.Errorf("scrape configuration missing %s", want)
		}
	}
	if strings.Contains(scrape, "__meta_kubernetes_pod_ready") {
		t.Fatal("unready running Pods hidden from collection")
	}
	root := filepath.Clean(filepath.Join(chartPath(t), "..", "..", ".."))
	wantDashboard, err := os.ReadFile(filepath.Join(root, "deployments", "monitoring", "dashboards", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range objectsByKind(objects, "ConfigMap") {
		if value, ok := mapAt(t, item, "data")["catalog.json"].(string); ok && !bytes.Equal(bytes.TrimSpace([]byte(value)), bytes.TrimSpace(wantDashboard)) {
			t.Fatal("packaged dashboard differs from authoritative asset")
		}
	}
}

func TestMonitoringPodDiscoveryRBAC(t *testing.T) {
	archive := monitoringPackage(t)
	const releaseNamespace = "observability"
	for _, tc := range []struct {
		name, targetNamespace string
		values                []string
	}{
		{"release namespace by default", releaseNamespace, nil},
		{"separate Catalog namespace", "catalog-team", []string{"--set", "target.namespace=catalog-team"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--namespace", releaseNamespace}, tc.values...)
			objects := monitoringObjects(t, archive, args...)
			_, job := prometheusScrapeConfig(t, objects)
			discovery := sliceAt(t, job, "kubernetes_sd_configs")
			if len(discovery) != 1 || stringAt(t, discovery[0], "role") != "pod" {
				t.Fatalf("unexpected Catalog Pod discovery configuration: %v", discovery)
			}
			namespaces := stringSlice(t, valueAt(t, discovery[0], "namespaces", "names"))
			if !slices.Equal(namespaces, []string{tc.targetNamespace}) {
				t.Fatalf("Prometheus discovers namespaces %v, want only %q", namespaces, tc.targetNamespace)
			}
			roles, bindings := objectsByKind(objects, "Role"), objectsByKind(objects, "RoleBinding")
			if len(roles) != 1 || len(bindings) != 1 {
				t.Fatalf("got %d Roles and %d RoleBindings, want one of each", len(roles), len(bindings))
			}
			if len(objectsByKind(objects, "ClusterRole")) != 0 || len(objectsByKind(objects, "ClusterRoleBinding")) != 0 {
				t.Fatal("Pod discovery rendered cluster-wide RBAC")
			}
			role, binding := roles[0], bindings[0]
			if stringAt(t, role, "metadata", "namespace") != tc.targetNamespace || stringAt(t, binding, "metadata", "namespace") != tc.targetNamespace {
				t.Fatalf("discovery Role and RoleBinding must both be in %s", tc.targetNamespace)
			}
			rules := sliceAt(t, role, "rules")
			if len(rules) != 1 {
				t.Fatalf("discovery rules = %v, want a single Pod read rule", rules)
			}
			rule := asMap(t, rules[0])
			verbs := stringSlice(t, rule["verbs"])
			slices.Sort(verbs)
			if !slices.Equal(verbs, []string{"get", "list", "watch"}) || !slices.Equal(stringSlice(t, rule["apiGroups"]), []string{""}) ||
				!slices.Equal(stringSlice(t, rule["resources"]), []string{"pods"}) ||
				len(optionalSlice(t, rule["resourceNames"])) != 0 || len(optionalSlice(t, rule["nonResourceURLs"])) != 0 {
				t.Fatalf("unexpected Pod discovery permissions: %v", rule)
			}
			ref := mapAt(t, binding, "roleRef")
			if ref["apiGroup"] != "rbac.authorization.k8s.io" || ref["kind"] != "Role" || ref["name"] != stringAt(t, role, "metadata", "name") {
				t.Fatalf("binding does not reference the rendered discovery Role: %v", ref)
			}
			subjects := sliceAt(t, binding, "subjects")
			if len(subjects) != 1 {
				t.Fatalf("discovery subjects = %v, want only Prometheus", subjects)
			}
			subject := asMap(t, subjects[0])
			if subject["kind"] != "ServiceAccount" || subject["namespace"] != releaseNamespace ||
				(subject["apiGroup"] != nil && subject["apiGroup"] != "") {
				t.Fatalf("unexpected discovery subject: %v", subject)
			}
			accounts := make(map[string]bool)
			for _, sa := range objectsByKind(objects, "ServiceAccount") {
				if stringAt(t, sa, "metadata", "namespace") == releaseNamespace {
					accounts[stringAt(t, sa, "metadata", "name")] = boolAt(t, sa, "automountServiceAccountToken")
				}
			}
			for _, component := range []string{"prometheus", "grafana"} {
				deployment := oneObject(t, objects, "Deployment", component)
				name := stringAt(t, deployment, "spec", "template", "spec", "serviceAccountName")
				token, exists := accounts[name]
				if stringAt(t, deployment, "metadata", "namespace") != releaseNamespace || !exists {
					t.Fatalf("%s must use a rendered ServiceAccount in %s, got %q", component, releaseNamespace, name)
				}
				collector := component == "prometheus"
				if token != collector {
					t.Errorf("%s ServiceAccount %q automounts a token = %t, want %t", component, name, token, collector)
				}
				if bound := subject["name"] == name; bound != collector {
					t.Errorf("%s ServiceAccount %q bound to discovery Role = %t, want %t", component, name, bound, collector)
				}
			}
		})
	}
}

func TestMonitoringValuesRejectUnsafeAndUnknownInputs(t *testing.T) {
	archive := monitoringPackage(t)
	if _, err := helm(t, "template", "monitor", archive, "--set-string", "enabled=on"); err == nil {
		t.Fatal("non-boolean enablement accepted")
	}
	if _, err := helm(t, monitoringArguments(archive, "--set", "prometheus.image=prom/prometheus:latest@sha256:"+strings.Repeat("a", 64))...); err == nil {
		t.Fatal("latest tag accepted even with a digest")
	}
	for _, input := range []string{"grafana.adminSecret.name=", "grafana.adminPassword=secret", "podSecurityContext.runAsUser=0", "podSecurityContext.privileged=true", "prometheus.image=prom/prometheus:latest", "prometheus.storage.emptyDirSizeLimit=0Gi", "target.namespace=other/namespace", "target.deploymentLabel=bad$label"} {
		t.Run(input, func(t *testing.T) {
			if _, err := helm(t, monitoringArguments(archive, "--set", input)...); err == nil {
				t.Fatal("invalid monitoring value accepted")
			}
		})
	}
	for _, tc := range []struct{ values, diagnostic string }{
		{"target.namespace=catalog-team,target.release=", "target.release"},
		{"target.namespace=catalog-team,target.deploymentLabel=", "target.deploymentLabel"},
		{"prometheus.scrapeIntervalSeconds=16,prometheus.scrapeTimeoutSeconds=17", "prometheus.scrapeTimeoutSeconds must not exceed scrapeIntervalSeconds"},
		{"prometheus.storage.existingClaim=same,grafana.storage.existingClaim=same", "Prometheus and Grafana must use separate claims"},
	} {
		t.Run(tc.values, func(t *testing.T) {
			raw, err := helm(t, monitoringArguments(archive, "--set", tc.values)...)
			// Helm 3 reports dotted schema paths and Helm 4 reports JSON pointers.
			if err == nil || !strings.Contains(strings.ReplaceAll(raw, "/", "."), tc.diagnostic) {
				t.Fatalf("expected Helm to reject %q with %q, got %v\n%s", tc.values, tc.diagnostic, err, raw)
			}
		})
	}
}

func TestMonitoringScrapeTiming(t *testing.T) {
	archive := monitoringPackage(t)
	for _, tc := range []struct{ values, timeout string }{
		{"prometheus.scrapeIntervalSeconds=16,prometheus.scrapeTimeoutSeconds=7", "7s"},
		{"prometheus.scrapeIntervalSeconds=16,prometheus.scrapeTimeoutSeconds=16", "16s"},
	} {
		t.Run(tc.values, func(t *testing.T) {
			objects := monitoringObjects(t, archive, "--set", tc.values)
			global, job := prometheusScrapeConfig(t, objects)
			for setting, want := range map[string]string{"scrape_interval": "16s", "scrape_timeout": tc.timeout} {
				got, overridden := job[setting]
				if !overridden {
					got = global[setting]
				}
				if got != want {
					t.Errorf("catalog-pods %s = %v, want %s", setting, got, want)
				}
			}
		})
	}
}

func TestMonitoringExternalResourceNames(t *testing.T) {
	archive := monitoringPackage(t)
	longName := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	for _, reference := range []struct {
		setting, component, volume, kind, field string
	}{
		{"grafana.adminSecret.name", "grafana", "admin", "secret", "secretName"},
		{"imagePullSecrets[0].name", "", "", "", ""},
		{"prometheus.storage.existingClaim", "prometheus", "data", "persistentVolumeClaim", "claimName"},
		{"grafana.storage.existingClaim", "grafana", "data", "persistentVolumeClaim", "claimName"},
	} {
		t.Run(reference.setting, func(t *testing.T) {
			for _, name := range []string{"team.catalog-input", longName} {
				objects := monitoringObjects(t, archive, "--set-string", reference.setting+"="+name)
				for _, deployment := range objectsByKind(objects, "Deployment") {
					if reference.component != "" && stringAt(t, deployment, "metadata", "labels", "app.kubernetes.io/component") != reference.component {
						continue
					}
					pod := mapAt(t, deployment, "spec", "template", "spec")
					if reference.component == "" {
						pullSecrets := sliceAt(t, pod, "imagePullSecrets")
						if len(pullSecrets) != 1 || stringAt(t, pullSecrets[0], "name") != name {
							t.Fatal("registry Secret name changed during rendering")
						}
					} else {
						volume := namedMap(t, sliceAt(t, pod, "volumes"), reference.volume)
						if stringAt(t, volume, reference.kind, reference.field) != name {
							t.Fatal("external resource name changed during rendering")
						}
					}
				}
			}
			for _, name := range []string{"UPPER", "bad/name", ".start", "end.", "two..labels", "label.-start", "end-.label", longName + "x"} {
				if _, err := helm(t, monitoringArguments(archive, "--set-string", reference.setting+"="+name)...); err == nil {
					t.Fatalf("accepted invalid external resource name %q", name)
				}
			}
		})
	}
	for _, name := range []string{"team.catalog", strings.Repeat("n", 64)} {
		if _, err := helm(t, monitoringArguments(archive, "--set-string", "target.namespace="+name)...); err == nil {
			t.Fatalf("accepted invalid namespace %q", name)
		}
	}
}

func TestMonitoringDisabledNeedsNoInputsOrResources(t *testing.T) {
	archive := monitoringPackage(t)
	if output, err := helm(t, "lint", "--strict", archive); err != nil {
		t.Fatalf("default lint: %v\n%s", err, output)
	}
	if output, err := helm(t, "template", "monitor", archive); err != nil || strings.TrimSpace(output) != "" {
		t.Fatalf("disabled chart rendered resources: %v\n%s", err, output)
	}
	if objects := monitoringObjects(t, archive, "--is-upgrade", "--set", "enabled=false"); len(objects) != 0 {
		t.Fatal("disabled upgrade retains monitoring resources")
	}
	if _, err := helm(t, "template", "monitor", archive, "--set", "enabled=true"); err == nil {
		t.Fatal("enabled monitoring accepted missing target and credentials")
	}
}

func TestMonitoringExistingClaimsAndRollout(t *testing.T) {
	archive := monitoringPackage(t)
	for _, resource := range objectsByKind(monitoringObjects(t, archive, "--set-json", `podSecurityContext={"runAsUser":null,"runAsGroup":null,"fsGroup":null}`), "Deployment") {
		security := mapAt(t, resource, "spec", "template", "spec", "securityContext")
		if !boolAt(t, security, "runAsNonRoot") || security["runAsUser"] != nil || security["runAsGroup"] != nil || security["fsGroup"] != nil {
			t.Fatal("admission-assigned identity override lost")
		}
	}
	for _, resource := range monitoringObjects(t, archive, "--namespace", "true", "--set-string", "target.namespace=false,target.release=true") {
		wantNamespace := "true"
		if kind := stringAt(t, resource, "kind"); kind == "Role" || kind == "RoleBinding" {
			wantNamespace = "false"
		}
		if stringAt(t, resource, "metadata", "namespace") != wantNamespace {
			t.Fatal("YAML-looking namespace lost its string identity")
		}
	}
	before := monitoringObjects(t, archive)
	after := monitoringObjects(t, archive, "--is-upgrade", "--set", "prometheus.storage.existingClaim=prom-history,grafana.storage.existingClaim=graf-history", "--set", "prometheus.scrapeIntervalSeconds=15,podSecurityContext.runAsUser=1000730000,podSecurityContext.fsGroup=1000730000")
	if len(objectsByKind(after, "PersistentVolumeClaim")) != 0 {
		t.Fatal("chart owns retained claims")
	}
	for _, component := range []string{"prometheus", "grafana"} {
		old := oneObject(t, before, "Deployment", component)
		updated := oneObject(t, after, "Deployment", component)
		if stringAt(t, old, "spec", "template", "metadata", "annotations", "checksum/config") == stringAt(t, updated, "spec", "template", "metadata", "annotations", "checksum/config") {
			t.Fatal("configuration upgrade does not roll collectors")
		}
		data := namedMap(t, sliceAt(t, updated, "spec", "template", "spec", "volumes"), "data")
		if data["persistentVolumeClaim"] == nil || data["emptyDir"] != nil {
			t.Fatal("existing claim not selected")
		}
	}
	// Also ensure the default application chart remains entirely collector-free.
	raw, err := json.Marshal(renderChart(t))
	if err != nil || bytes.Contains(raw, []byte("open-agentic-catalog-monitoring")) {
		t.Fatal("monitoring leaked into ordinary Catalog installation")
	}
}
