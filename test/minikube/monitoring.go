//go:build minikube

// Monitoring checks deploy the optional companion chart and verify its scoped collectors.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const monitoringRelease = "monitoring"

// The shared dashboard has one query per panel, including optional hybrid metrics.
// TestMonitoringDashboardInventory checks this against the authoritative JSON.
const monitoringDashboardPanels = 32

type monitoringClient struct {
	client   *http.Client
	base     string
	user     string
	password string
}

// monitoringJSON calls an owned monitoring endpoint, requiring HTTP 200 and a response within 1 MiB before optional JSON decoding.
func (h *harness) monitoringJSON(client monitoringClient, method, path string, body, result any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(h.ctx, method, client.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if client.user != "" {
		request.SetBasicAuth(client.user, client.password)
	}
	response, err := client.client.Do(request)
	if err != nil {
		return errors.New("owned monitoring HTTP request failed")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || response.StatusCode != http.StatusOK {
		return fmt.Errorf("owned monitoring response failed: status %d", response.StatusCode)
	}
	if result != nil {
		return json.Unmarshal(raw, result)
	}
	return nil
}

// monitoringEventually retries an asynchronous monitoring assertion within the harness context and a fixed test deadline.
func (h *harness) monitoringEventually(label string, check func() error) error {
	deadline := time.Now().Add(90 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if last = check(); last == nil {
			return nil
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-h.ctx.Done():
			timer.Stop()
			return h.ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("%s: %w", label, last)
}

// monitoringValues writes private invocation-owned companion values, optionally referencing the test's independently retained claims.
func (h *harness) monitoringValues(persistent bool) (string, error) {
	return h.monitoringValuesIn(persistent, "")
}

func (h *harness) monitoringValuesIn(persistent bool, targetNamespace string) (string, error) {
	prometheus, grafana := "", ""
	if persistent {
		prometheus, grafana = "monitoring.prometheus-history", "monitoring.grafana-history"
	}
	values := map[string]any{
		"enabled":    true,
		"target":     map[string]string{"namespace": targetNamespace, "release": releaseName, "deploymentLabel": "mk-" + h.suffix},
		"prometheus": map[string]any{"scrapeIntervalSeconds": 5, "scrapeTimeoutSeconds": 3, "storage": map[string]string{"existingClaim": prometheus}},
		"grafana":    map[string]any{"adminSecret": map[string]string{"name": "monitoring.admin"}, "storage": map[string]string{"existingClaim": grafana}},
	}
	if persistent {
		values["podSecurityContext"] = map[string]int{"runAsUser": 1000730000, "runAsGroup": 1000730000, "fsGroup": 1000730000}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	path := filepath.Join(h.temporary, "monitoring-values.json")
	return path, os.WriteFile(path, raw, 0o600)
}

// monitoringClients opens loopback tunnels to the owned Prometheus and Grafana Services and binds the supplied client and fixture credentials.
func (h *harness) monitoringClients(name, password string, client *http.Client) (monitoringClient, monitoringClient, error) {
	prometheus, err := h.startPortForward("service/"+name+"-prometheus", 9090)
	if err != nil {
		return monitoringClient{}, monitoringClient{}, err
	}
	grafana, err := h.startPortForward("service/"+name+"-grafana", 3000)
	if err != nil {
		return monitoringClient{}, monitoringClient{}, err
	}
	fmt.Printf("[monitoring] Grafana loopback: http://%s\n", grafana)
	return monitoringClient{client: client, base: "http://" + prometheus}, monitoringClient{client: client, base: "http://" + grafana, user: "catalog", password: password}, nil
}

// monitoringTargets requires exactly two healthy API and two healthy worker scrape targets with this test's labels and optional current Pod UIDs.
func (h *harness) monitoringTargets(client monitoringClient, expectedUIDs ...string) error {
	targets, err := h.monitoringTargetInventory(client)
	if err != nil {
		return err
	}
	return h.validateMonitoringTargets(targets, expectedUIDs...)
}

type monitoringTarget struct {
	Health     string            `json:"health"`
	Labels     map[string]string `json:"labels"`
	Discovered map[string]string `json:"discoveredLabels"`
}

func (h *harness) monitoringTargetInventory(client monitoringClient) ([]monitoringTarget, error) {
	var response struct {
		Status string `json:"status"`
		Data   struct {
			Targets []monitoringTarget `json:"activeTargets"`
		} `json:"data"`
	}
	if err := h.monitoringJSON(client, http.MethodGet, "/api/v1/targets", nil, &response); err != nil {
		return nil, err
	}
	if response.Status != "success" {
		return nil, errors.New("monitoring target query failed")
	}
	return response.Data.Targets, nil
}

func (h *harness) validateMonitoringTargets(targets []monitoringTarget, expectedUIDs ...string) error {
	if len(targets) != 4 {
		return fmt.Errorf("expected four Catalog targets, got %d", len(targets))
	}
	instances, roles := map[string]bool{}, map[string]int{}
	uids := map[string]bool{}
	for _, target := range targets {
		instance := target.Labels["instance"]
		role := target.Labels["catalog_role"]
		if target.Health != "up" || instance == "" || instances[instance] || target.Labels["catalog_deployment"] != "mk-"+h.suffix ||
			target.Discovered["__meta_kubernetes_namespace"] != namespace || target.Discovered["__meta_kubernetes_pod_label_app_kubernetes_io_instance"] != releaseName {
			return errors.New("monitoring target unhealthy, duplicated or outside selected deployment")
		}
		uid := target.Discovered["__meta_kubernetes_pod_uid"]
		if uid == "" || uids[uid] || (len(expectedUIDs) != 0 && !slices.Contains(expectedUIDs, uid)) {
			return errors.New("discovery still includes a replaced Pod")
		}
		uids[uid] = true
		instances[instance] = true
		roles[role]++
	}
	if roles["api"] != 2 || roles["worker"] != 2 {
		return errors.New("monitoring role targeting is incomplete")
	}
	return nil
}

// monitoringQuery checks Prometheus query success; a nonnegative expected count also requires that many finite positive samples.
func (h *harness) monitoringQuery(client monitoringClient, expression, at string, expected int) error {
	parameters := url.Values{"query": []string{expression}}
	if at != "" {
		parameters.Set("time", at)
	}
	var response struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
		Data     struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := h.monitoringJSON(client, http.MethodGet, "/api/v1/query?"+parameters.Encode(), nil, &response); err != nil {
		return err
	}
	if response.Status != "success" || len(response.Warnings) != 0 || (expected >= 0 && len(response.Data.Result) != expected) {
		return errors.New("monitoring query failed or stored sample count differs")
	}
	if expected >= 0 {
		for _, sample := range response.Data.Result {
			if len(sample.Value) != 2 {
				return errors.New("monitoring query has no scalar sample")
			}
			var value string
			if err := json.Unmarshal(sample.Value[1], &value); err != nil {
				return err
			}
			parsed, err := strconv.ParseFloat(value, 64)
			// Negating > also rejects NaN, which a <= comparison would admit.
			if err != nil || !(parsed > 0) || math.IsInf(parsed, 0) {
				return errors.New("monitoring query has no positive observation")
			}
		}
	}
	return nil
}

// monitoringAbsent requires that the disabled companion release leave no matching namespaced workloads, configuration, authority or claims.
func (h *harness) monitoringAbsent() error {
	const label = "app.kubernetes.io/name=open-agentic-catalog-monitoring"
	absent, err := h.kubectl("verify disabled monitoring resource absence", "-n", namespace, "get", "pods,deployments,services,configmaps,serviceaccounts,roles,rolebindings,persistentvolumeclaims", "-l", label, "-o", "json")
	if err != nil {
		return err
	}
	var inventory struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(absent, &inventory) != nil || len(inventory.Items) != 0 {
		return errors.New("disabled monitoring still has resources")
	}
	return nil
}

// verifyMonitoring qualifies the optional companion in the owned cluster, including target selection, RBAC, dashboard queries and retained state across removal.
func (h *harness) verifyMonitoring(baseURL string) error {
	if err := h.monitoringAbsent(); err != nil {
		return err
	}
	archiveRaw, err := h.run("package monitoring with authoritative assets", "python3", "test/monitoring/helm.py", "package", "--output", filepath.Join(h.temporary, "monitoring-package"))
	if err != nil {
		return err
	}
	archive := strings.TrimSpace(string(archiveRaw))
	if _, err := h.helm("install default-disabled monitoring without target or credentials", "install", monitoringRelease, archive, "-n", namespace, "--wait", "--timeout=1m"); err != nil {
		return err
	}
	if err := h.monitoringAbsent(); err != nil {
		return err
	}
	for _, role := range []string{"api", "worker"} {
		if _, err := h.kubectl("scale Catalog replicas for monitoring", "-n", namespace, "scale", "deployment/catalog-"+role, "--replicas=2"); err != nil {
			return err
		}
		if _, err := h.kubectl("wait for Catalog replicas", "-n", namespace, "rollout", "status", "deployment/catalog-"+role, "--timeout=2m"); err != nil {
			return err
		}
	}
	// Running decoys have a matching named port but the wrong release or role.
	// Reuse the owned fixture image rather than pull another test dependency.
	for _, decoy := range []struct{ name, release, role string }{{"monitoring-other-release", "other", "api"}, {"monitoring-migration", releaseName, "migration"}} {
		fixture := h.fixtureDeployment(decoy.name, "decoy", nil)
		deployment, ok := fixture["spec"].(map[string]any)
		if !ok {
			return errors.New("fixture deployment spec is missing")
		}
		template, ok := deployment["template"].(map[string]any)
		if !ok {
			return errors.New("fixture Pod template is missing")
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
		pod := kubernetesObject{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": decoy.name, "namespace": namespace, "labels": map[string]string{"app.kubernetes.io/instance": decoy.release, "app.kubernetes.io/component": decoy.role}}, "spec": spec}
		if err := h.applyObject("create excluded monitoring discovery fixture", pod); err != nil {
			return err
		}
		if _, err := h.kubectl("wait for excluded discovery fixture", "-n", namespace, "wait", "pod/"+decoy.name, "--for=condition=Ready", "--timeout=1m"); err != nil {
			return err
		}
	}
	pinsRaw, err := os.ReadFile(filepath.Join(h.options.repositoryRoot, "deployments", "monitoring", "images.json"))
	if err != nil {
		return err
	}
	var pins map[string]string
	if err := json.Unmarshal(pinsRaw, &pins); err != nil {
		return err
	}
	for _, component := range []string{"prometheus", "grafana"} {
		if err := validateDigestReference(pins[component]); err != nil {
			return err
		}
		if _, err := h.minikube("pull pinned "+component+" into owned cluster", "image", "pull", pins[component]); err != nil {
			return err
		}
		h.report.Images[component+"-pin"] = pins[component]
	}
	password, err := randomHex(24)
	if err != nil {
		return err
	}
	h.secrets.monitoringPassword = password
	if err := h.createSecret("create private monitoring admin Secret", "monitoring.admin", map[string]string{"admin-user": "catalog", "admin-password": password}); err != nil {
		return err
	}
	values, err := h.monitoringValues(false)
	if err != nil {
		return err
	}
	if _, err := h.helm("enable ephemeral monitoring", "upgrade", monitoringRelease, archive, "-n", namespace, "-f", values, "--wait", "--timeout=4m"); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(namespace + "/" + monitoringRelease))
	name := fmt.Sprintf("cm-%s-%x", monitoringRelease, hash[:4])
	for _, check := range []struct{ resource, selectedNamespace, want string }{{"pods", namespace, "yes"}, {"secrets", namespace, "no"}, {"pods", "default", "no"}, {"nodes", namespace, "no"}} {
		raw, commandErr := h.kubectl("verify namespaced collector authorization", "auth", "can-i", "list", check.resource, "-n", check.selectedNamespace, "--as=system:serviceaccount:"+namespace+":"+name+"-prometheus")
		if strings.TrimSpace(string(raw)) != check.want {
			// kubectl exits nonzero for denial; the wrapper does not return stdout on errors.
			if check.want != "no" || commandErr == nil || !strings.HasSuffix(strings.TrimSpace(commandErr.Error()), "\nno") {
				return fmt.Errorf("collector RBAC differs: %s", check.resource)
			}
		}
	}
	if _, err := h.kubectl("validate actual Prometheus configuration", "-n", namespace, "exec", "deployment/"+name+"-prometheus", "--", "/bin/promtool", "check", "config", "/etc/catalog-monitoring/prometheus.yaml"); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, MaxConnsPerHost: 2, MaxIdleConns: 4, ResponseHeaderTimeout: 3 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	prometheus, grafana, err := h.monitoringClients(name, password, client)
	if err != nil {
		return err
	}
	if err := h.monitoringEventually("four namespaced targets", func() error { return h.monitoringTargets(prometheus) }); err != nil {
		return err
	}
	if err := h.verifyMonitoringDashboard(prometheus, grafana); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "monitoring-default-off-install-rbac-arbitrary-uid-and-dashboard")
	// A missing Secret fails only this separately owned release, without deleting state.
	if _, err := h.helm("prove missing monitoring Secret failure", "install", "monitoring-failure", archive, "-n", namespace, "-f", values, "--set", "grafana.adminSecret.name=missing-monitoring.admin", "--wait", "--timeout=20s"); err == nil {
		return errors.New("monitoring accepted missing Secret")
	}
	if _, err := h.helm("remove only failed monitoring release", "uninstall", "monitoring-failure", "-n", namespace, "--wait", "--timeout=1m"); err != nil {
		return err
	}
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return err
	}
	// Retained claims are external to both releases and use independent directories.
	for _, claim := range []string{"monitoring.prometheus-history", "monitoring.grafana-history"} {
		if err := h.applyObject("create owned retained monitoring claim", kubernetesObject{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": claim, "namespace": namespace}, "spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": "1Gi"}}}}); err != nil {
			return err
		}
	}
	values, err = h.monitoringValues(true)
	if err != nil {
		return err
	}
	if _, err := h.helm("upgrade to externally retained monitoring storage", "upgrade", monitoringRelease, archive, "-n", namespace, "-f", values, "--wait", "--timeout=3m"); err != nil {
		return err
	}
	prometheus, grafana, err = h.monitoringClients(name, password, client)
	if err != nil {
		return err
	}
	if err := h.monitoringEventually("persistent collection", func() error { return h.monitoringTargets(prometheus) }); err != nil {
		return err
	}
	if err := h.verifyMonitoringDashboard(prometheus, grafana); err != nil {
		return err
	}
	if err := h.verifyInstrumentation("monitoring-workload"); err != nil {
		return err
	}
	traffic := `sum(increase(open_agentic_catalog_http_responses_total{catalog_deployment="mk-` + h.suffix + `",route="/api/v1/catalog/search",status_class="2xx"}[2m]))`
	if err := h.monitoringEventually("stored workload observations", func() error { return h.monitoringQuery(prometheus, traffic, "", 1) }); err != nil {
		return err
	}
	if err := h.monitoringJSON(grafana, http.MethodPut, "/api/user/preferences", map[string]string{"theme": "light"}, nil); err != nil {
		return err
	}
	retainedAt := strconv.FormatInt(time.Now().Unix(), 10)
	heap := `open_agentic_catalog_go_heap_bytes{catalog_deployment="mk-` + h.suffix + `"}`
	if err := h.monitoringQuery(prometheus, heap, retainedAt, 4); err != nil {
		return err
	}
	// Replacement discovers new instance addresses without changing scrape configuration.
	if err := h.recreateRuntimePods(); err != nil {
		return err
	}
	var currentUIDs []string
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return err
		}
		for _, pod := range pods {
			currentUIDs = append(currentUIDs, pod.UID)
		}
	}
	if err := h.monitoringEventually("replacement Pod discovery", func() error { return h.monitoringTargets(prometheus, currentUIDs...) }); err != nil {
		return err
	}
	// Both the boolean switch and release removal leave external state alone.
	for _, removeRelease := range []bool{false, true} {
		if removeRelease {
			if _, err := h.helm("remove monitoring without deleting external claims", "uninstall", monitoringRelease, "-n", namespace, "--wait", "--timeout=2m"); err != nil {
				return err
			}
		} else if _, err := h.helm("disable monitoring with its Helm flag", "upgrade", monitoringRelease, archive, "-n", namespace, "--reuse-values", "--set", "enabled=false", "--wait", "--timeout=2m"); err != nil {
			return err
		}
		if err := h.monitoringEventually("disabled monitoring resource absence", h.monitoringAbsent); err != nil {
			return err
		}
		if _, err := h.kubectl("verify retained monitoring claims", "-n", namespace, "get", "pvc", "monitoring.prometheus-history", "monitoring.grafana-history"); err != nil {
			return err
		}
		if err := h.verifyInstrumentation("monitoring-removed"); err != nil {
			return err
		}
		if _, err := h.helm("re-enable monitoring using retained history", "upgrade", "--install", monitoringRelease, archive, "-n", namespace, "-f", values, "--set", "enabled=true", "--wait", "--timeout=3m"); err != nil {
			return err
		}
		prometheus, grafana, err = h.monitoringClients(name, password, client)
		if err != nil {
			return err
		}
		if err := h.monitoringEventually("re-enabled collection", func() error { return h.monitoringTargets(prometheus) }); err != nil {
			return err
		}
		if err := h.monitoringQuery(prometheus, heap, retainedAt, 4); err != nil {
			return fmt.Errorf("prometheus history not retained: %w", err)
		}
		var preferences struct {
			Theme string `json:"theme"`
		}
		if err := h.monitoringJSON(grafana, http.MethodGet, "/api/user/preferences", nil, &preferences); err != nil {
			return err
		}
		if preferences.Theme != "light" {
			return errors.New("grafana persistent state was not retained")
		}
		if err := h.verifyMonitoringDashboard(prometheus, grafana); err != nil {
			return err
		}
	}
	h.report.Checks = append(h.report.Checks, "monitoring-workload-and-pod-replacement", "monitoring-missing-secret-failure-isolation", "monitoring-flag-disable-and-reenable-with-retained-history", "monitoring-removal-independence-and-retained-history-reinstallation")
	return h.verifyCrossNamespaceMonitoring(archive, password, client)
}

// verifyMonitoringDashboard checks the fixed Grafana dashboard and datasource, then evaluates all panel expressions against the test's Prometheus.
func (h *harness) verifyMonitoringDashboard(prometheus, grafana monitoringClient) error {
	var response struct {
		Dashboard struct {
			UID    string `json:"uid"`
			Panels []struct {
				Targets []struct {
					Expr string `json:"expr"`
				} `json:"targets"`
			} `json:"panels"`
		} `json:"dashboard"`
	}
	if err := h.monitoringJSON(grafana, http.MethodGet, "/api/dashboards/uid/catalog-overview", nil, &response); err != nil {
		return err
	}
	if response.Dashboard.UID != "catalog-overview" || len(response.Dashboard.Panels) != monitoringDashboardPanels {
		return errors.New("dashboard not provisioned")
	}
	var datasource struct {
		URL  string `json:"url"`
		Type string `json:"type"`
	}
	if err := h.monitoringJSON(grafana, http.MethodGet, "/api/datasources/uid/catalog-prometheus", nil, &datasource); err != nil {
		return err
	}
	if datasource.Type != "prometheus" || !strings.HasSuffix(datasource.URL, "-prometheus:9090") {
		return errors.New("dashboard data source incorrectly provisioned")
	}
	if err := h.monitoringJSON(grafana, http.MethodGet, "/api/datasources/uid/catalog-prometheus/health", nil, nil); err != nil {
		return err
	}
	replacer := strings.NewReplacer("${deployment:regex}", "mk-"+h.suffix, "${role:regex}", "api|worker", "$__rate_interval", "1m")
	queries := 0
	for _, panel := range response.Dashboard.Panels {
		for _, target := range panel.Targets {
			expression := replacer.Replace(target.Expr)
			if strings.Contains(expression, "$") {
				return errors.New("unknown dashboard query variable")
			}
			if err := h.monitoringQuery(prometheus, expression, "", -1); err != nil {
				return err
			}
			queries++
		}
	}
	if queries != monitoringDashboardPanels {
		return errors.New("dashboard query inventory changed")
	}
	return nil
}
