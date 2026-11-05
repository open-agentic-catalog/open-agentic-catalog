//go:build minikube

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const gatewayNamespace = "gateway-qualification"
const gatewayOrigin = "https://gateway." + gatewayNamespace + ".svc.cluster.local"

type gatewayReport struct {
	Inputs        map[string]any    `json:"inputs"`
	Build         map[string]string `json:"build"`
	Source        map[string]string `json:"source"`
	Workloads     map[string]string `json:"runningImages"`
	CatalogValues string            `json:"redactedCatalogValuesSha256"`
	Qualification map[string]any    `json:"qualification,omitempty"`
	Lifecycle     map[string]any    `json:"lifecycle,omitempty"`
	Persistence   map[string]any    `json:"persistence,omitempty"`
}
type gatewayState struct {
	report  gatewayReport
	images  map[string]agntcyImage
	aliases map[string]agntcyImage
	secrets []string
	tls     map[string]string
}

func (h *harness) managedBuild() map[string]string {
	if h.ans != nil {
		return h.ans.report.Build
	}
	if h.agntcy != nil {
		return h.agntcy.report.Build
	}
	if h.gateway != nil {
		return h.gateway.report.Build
	}
	return nil
}
func (h *harness) managedWorkloads() map[string]string {
	if h.ans != nil {
		return h.ans.report.Workloads
	}
	if h.agntcy != nil {
		return h.agntcy.report.Workloads
	}
	if h.gateway != nil {
		return h.gateway.report.Workloads
	}
	return nil
}
func (h *harness) setManagedCatalogValues(value string) {
	if h.ans != nil {
		h.ans.report.CatalogValues = value
	}
	if h.agntcy != nil {
		h.agntcy.report.CatalogValues = value
	}
	if h.gateway != nil {
		h.gateway.report.CatalogValues = value
	}
}
func (h *harness) gatewayPython() string { return environment("CATALOG_GATEWAY_PYTHON", "python3") }

func (h *harness) gatewayPreflight() error {
	raw, err := h.run("verify installed Gateway inputs", h.gatewayPython(), "-B", "test/interoperability/gateway_tools.py", "check")
	if err != nil {
		return err
	}
	state := &gatewayState{images: make(map[string]agntcyImage), aliases: make(map[string]agntcyImage)}
	if err := json.Unmarshal(raw, &state.report.Inputs); err != nil {
		return err
	}
	var installed struct {
		Images map[string]agntcyImage `json:"images"`
	}
	if err := json.Unmarshal(raw, &installed); err != nil || len(installed.Images) != 3 {
		return errors.New("incomplete Gateway installed image inventory")
	}
	state.images = installed.Images
	for name, image := range installed.Images {
		state.aliases[name] = agntcyImage{ID: image.ID, Reference: "open-agentic-catalog:minikube-gateway-" + name + "-" + h.suffix}
	}
	raw, err = h.run("resolve managed Catalog build identity", h.gatewayPython(), "-B", "test/packaging/versioning.py", "metadata")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &state.report.Build); err != nil {
		return err
	}
	state.report.Source, err = h.gatewayFingerprints()
	if err != nil {
		return err
	}
	state.tls, err = serviceTLSMaterial(time.Now(), "owned-gateway-ca", gatewayNamespace, []string{"gateway", "catalog-origin"}, net.ParseIP("127.0.0.1"))
	if err != nil {
		return err
	}
	for range 6 {
		value, err := randomHex(24)
		if err != nil {
			return err
		}
		state.secrets = append(state.secrets, value)
	}
	state.secrets = append(state.secrets, state.tls["gateway.key"], state.tls["catalog-origin.key"])
	state.report.Workloads = make(map[string]string)
	h.gateway, h.report.Gateway = state, &state.report
	return h.interopProgress("inputs-verified", nil, nil)
}

func (h *harness) gatewayFingerprints() (map[string]string, error) {
	raw, err := h.run("fingerprint current Catalog and Gateway fixture", h.gatewayPython(), "-B", "test/interoperability/gateway_tools.py", "fingerprint")
	if err != nil {
		return nil, err
	}
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil || len(result) != 3 {
		return nil, errors.New("invalid Gateway fingerprint inventory")
	}
	for _, name := range []string{"catalog", "chart", "harness"} {
		if len(result[name]) != 64 {
			return nil, errors.New("invalid Gateway fingerprint")
		}
	}
	return result, nil
}

func (h *harness) loadGatewayImages() error {
	for name, image := range h.gateway.images {
		alias := h.gateway.aliases[name].Reference
		if _, err := h.run("alias exact Gateway "+name, h.options.docker, "image", "tag", image.Reference, alias); err != nil {
			return err
		}
		raw, err := h.run("verify Gateway alias identity", h.options.docker, "image", "inspect", "--format", "{{.Id}}", alias)
		if err != nil || strings.TrimSpace(string(raw)) != image.ID {
			return errors.New("gateway alias identity differs")
		}
		if _, err := h.minikube("load exact Gateway "+name, "image", "load", "--daemon=true", "--overwrite=true", alias); err != nil {
			return err
		}
	}
	return nil
}

func gatewayEnvironment(secrets []string) (map[string]string, error) {
	type key struct {
		Key    string   `json:"key"`
		Groups []string `json:"groups"`
	}
	keys, err := json.Marshal(map[string]key{"reader": {secrets[1], []string{"qualified-reader"}}, "private": {secrets[2], []string{"qualified-private"}}})
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"DEPLOYMENT_MODE": "registry-only", "REGISTRY_MODE": "full", "STORAGE_BACKEND": "mongodb-ce",
		"DOCUMENTDB_HOST": "mongodb", "DOCUMENTDB_DATABASE": "mcp_registry", "DOCUMENTDB_NAMESPACE": "qualification", "DOCUMENTDB_USE_TLS": "false",
		"REGISTRY_NAME": "Owned Gateway qualification", "REGISTRY_ORGANIZATION_NAME": "Local interoperability tests", "REGISTRY_URL": gatewayOrigin,
		"ARD_PUBLISHER_DOMAIN": "gateway." + gatewayNamespace + ".svc.cluster.local", "ARD_CATALOG_ENABLED": "true", "ARD_REGISTRY_ENABLED": "true",
		"SECRET_KEY": secrets[3], "AUTH_SERVER_NGINX_MARKER_SECRET": secrets[4], "REGISTRY_API_TOKEN": secrets[0], "REGISTRY_API_KEYS": string(keys), "REGISTRY_STATIC_TOKEN_AUTH_ENABLED": "true",
		"AUTH_SERVER_URL": "http://auth-server:8888", "AUTH_SERVER_EXTERNAL_URL": "http://auth-server:8888", "AUTH_PROVIDER": "cognito", "KEYCLOAK_ENABLED": "false", "COGNITO_ENABLED": "false", "GITHUB_ENABLED": "false", "GOOGLE_ENABLED": "false",
		"AWS_EC2_METADATA_DISABLED": "true", "EGRESS_AUTH_ENABLED": "false", "EGRESS_FORWARD_PROXY_ENABLED": "false", "EGRESS_REFRESH_WORKER_INTERVAL_SECONDS": "0",
		"MCP_TELEMETRY_DISABLED": "1", "MCP_TELEMETRY_OPT_OUT": "1", "MCP_TELEMETRY_IMDS_PROBE_DISABLED": "1", "TELEMETRY_ENABLED": "false", "TELEMETRY_OPT_OUT": "true", "TELEMETRY_IMDS_PROBE_DISABLED": "true",
		"DISABLE_AI_REGISTRY_TOOLS_SERVER": "true", "SECURITY_SCAN_ENABLED": "false", "SECURITY_SCAN_ON_REGISTRATION": "false", "AGENT_SECURITY_SCAN_ENABLED": "false", "SKILL_SECURITY_SCAN_ENABLED": "false", "UPDATE_CHECK_ENABLED": "false", "ANS_INTEGRATION_ENABLED": "false", "OTEL_SDK_DISABLED": "true", "METRICS_LEGACY_HTTP_POST": "false",
		"HEALTH_CHECK_INTERVAL_SECONDS": "3600", "HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "HF_HUB_DISABLE_TELEMETRY": "1",
		"EMBEDDINGS_PROVIDER": "sentence-transformers", "EMBEDDINGS_MODEL_NAME": "qualification-no-model", "EMBEDDINGS_MODEL_DIR": "/not-installed/qualification-no-model", "TOKENIZERS_PARALLELISM": "false",
		"PYTHONDONTWRITEBYTECODE": "1", "PYTHONPATH": "/app", "BIND_HOST": "127.0.0.1",
	}, nil
}

type gatewayWorkloadDefinition struct {
	object                   kubernetesObject
	pod, container, template map[string]any
}

func gatewayWorkload(name, image string, env map[string]string) gatewayWorkloadDefinition {
	labels := map[string]string{"component": name}
	container := map[string]any{"name": name, "image": image, "imagePullPolicy": "Never",
		"resources":       map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "128Mi"}, "limits": map[string]string{"cpu": "2", "memory": "2Gi"}},
		"securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}}
	if env != nil {
		container["envFrom"] = []any{map[string]any{"secretRef": map[string]string{"name": "gateway-environment"}}}
	}
	spec := map[string]any{"automountServiceAccountToken": false, "containers": []any{container}, "terminationGracePeriodSeconds": 10}
	template := map[string]any{"metadata": map[string]any{"labels": labels}, "spec": spec}
	object := kubernetesObject{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": name, "namespace": gatewayNamespace}, "spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": labels}, "template": template}}
	return gatewayWorkloadDefinition{object: object, pod: spec, container: container, template: template}
}

func (h *harness) deployGateway() error {
	if err := h.applyObject("create owned Gateway namespace", kubernetesObject{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": gatewayNamespace, "labels": map[string]string{"pod-security.kubernetes.io/enforce": "baseline"}}}); err != nil {
		return err
	}
	if err := h.gatewayPolicies(); err != nil {
		return err
	}
	env, err := gatewayEnvironment(h.gateway.secrets)
	if err != nil {
		return err
	}
	if err := h.applyObject("create private native Gateway settings", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "gateway-environment", "namespace": gatewayNamespace}, "stringData": env}); err != nil {
		return err
	}
	tls := map[string]string{"ca.pem": h.gateway.tls["ca.pem"], "fullchain.pem": h.gateway.tls["gateway.crt"], "privkey.pem": h.gateway.tls["gateway.key"]}
	if err := h.applyObject("create private Gateway TLS", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "gateway-tls", "namespace": gatewayNamespace}, "stringData": tls}); err != nil {
		return err
	}
	for _, item := range []struct {
		name, image  string
		port, target int
	}{{"mongodb", "mongodb", 27017, 27017}, {"auth-server", "auth", 8888, 8888}, {"gateway", "registry", 443, 8443}, {"control", "registry", 0, 0}} {
		workload := gatewayWorkload(item.name, h.gateway.aliases[item.image].Reference, env)
		spec, container := workload.pod, workload.container
		switch item.name {
		case "mongodb":
			spec["securityContext"] = map[string]any{"runAsUser": 999, "runAsGroup": 999, "runAsNonRoot": true, "fsGroup": 999}
			delete(container, "envFrom")
			container["args"] = []string{"mongod", "--replSet", "rs0", "--bind_ip_all"}
			container["volumeMounts"] = []any{map[string]any{"name": "data", "mountPath": "/data/db"}}
			spec["volumes"] = []any{map[string]any{"name": "data", "emptyDir": map[string]any{"sizeLimit": "1Gi"}}}
			container["readinessProbe"] = map[string]any{"tcpSocket": map[string]any{"port": 27017}, "periodSeconds": 2}
		case "auth-server":
			container["env"] = []any{map[string]string{"name": "BIND_HOST", "value": "0.0.0.0"}}
			container["readinessProbe"] = map[string]any{"tcpSocket": map[string]any{"port": 8888}, "periodSeconds": 2}
		case "gateway":
			container["volumeMounts"] = []any{map[string]any{"name": "tls", "mountPath": "/etc/ssl/certs/fullchain.pem", "subPath": "fullchain.pem", "readOnly": true}, map[string]any{"name": "tls", "mountPath": "/etc/ssl/private", "readOnly": true}}
			spec["volumes"] = []any{map[string]any{"name": "tls", "secret": map[string]any{"secretName": "gateway-tls"}}}
			container["readinessProbe"] = map[string]any{"httpGet": map[string]any{"path": "/.well-known/ai-catalog.json", "port": 8443, "scheme": "HTTPS"}, "periodSeconds": 2, "failureThreshold": 150}
		case "control":
			delete(container, "envFrom")
			container["command"] = []string{"/app/.venv/bin/python", "-B", "-c", "import time; time.sleep(2400)"}
			container["volumeMounts"] = []any{map[string]any{"name": "trust", "mountPath": "/trust", "readOnly": true}, map[string]any{"name": "credentials", "mountPath": "/credentials", "readOnly": true}, map[string]any{"name": "qualification", "mountPath": "/qualification", "readOnly": true}}
			spec["volumes"] = []any{map[string]any{"name": "trust", "secret": map[string]any{"secretName": "gateway-tls", "items": []any{map[string]string{"key": "ca.pem", "path": "ca.pem"}}}}, map[string]any{"name": "credentials", "secret": map[string]any{"secretName": "gateway-control"}}, map[string]any{"name": "qualification", "configMap": map[string]any{"name": "gateway-qualification", "items": gatewayAssetItems()}}}
		}
		if item.name == "control" {
			if err := h.gatewayAssets(); err != nil {
				return err
			}
		}
		if err := h.applyObject("deploy native Gateway "+item.name, workload.object); err != nil {
			return err
		}
		if item.port != 0 {
			if err := h.applyObject("create private Gateway "+item.name+" Service", kubernetesObject{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": item.name, "namespace": gatewayNamespace}, "spec": map[string]any{"type": "ClusterIP", "selector": map[string]string{"component": item.name}, "ports": []any{map[string]any{"port": item.port, "targetPort": item.target}}}}); err != nil {
				return err
			}
		}
		if item.name == "mongodb" {
			if _, err := h.kubectl("wait for owned MongoDB", "-n", gatewayNamespace, "rollout", "status", "deployment/mongodb", "--timeout=2m"); err != nil {
				return err
			}
			// Native bootstrap initializes rs0 and seeds the original admin scopes; no database data is forged.
			definition := gatewayWorkload("bootstrap", h.gateway.aliases["registry"].Reference, env)
			job, template := definition.object, definition.template
			job["kind"] = "Job"
			job["apiVersion"] = "batch/v1"
			job["spec"] = map[string]any{"backoffLimit": 0, "activeDeadlineSeconds": 120, "template": template}
			podSpec := definition.pod
			podSpec["restartPolicy"] = "Never"
			definition.container["command"] = []string{"/app/.venv/bin/python", "-B", "/app/scripts/init-mongodb-ce.py"}
			if err := h.applyObject("initialize native MongoDB replica set and scopes", job); err != nil {
				return err
			}
			if _, err := h.kubectl("wait for native MongoDB bootstrap", "-n", gatewayNamespace, "wait", "--for=condition=complete", "job/bootstrap", "--timeout=2m"); err != nil {
				return err
			}
			raw, err := h.kubectl("verify actual native bootstrap image", "-n", gatewayNamespace, "get", "pods", "-l", "component=bootstrap", "-o", "json")
			if err != nil {
				return err
			}
			id, err := gatewayPodImage(raw, h.gateway.aliases["registry"], true)
			if err != nil {
				return err
			}
			h.gateway.report.Workloads["gateway-bootstrap"] = id
		}
	}
	for _, name := range []string{"auth-server", "gateway", "control"} {
		if _, err := h.kubectl("wait for native "+name, "-n", gatewayNamespace, "rollout", "status", "deployment/"+name, "--timeout=5m"); err != nil {
			return err
		}
	}
	// Confirm a real primary rather than accepting a standalone server's successful ping.
	raw, err := h.kubectl("verify native MongoDB replica set", "-n", gatewayNamespace, "exec", "deployment/mongodb", "--", "mongosh", "--quiet", "--eval", "JSON.stringify({set:rs.status().set,members:rs.status().members.map(m=>({state:m.state,health:m.health}))})")
	if err != nil || !strings.Contains(string(raw), `"set":"rs0"`) || !strings.Contains(string(raw), `"state":1`) || !strings.Contains(string(raw), `"health":1`) {
		return errors.New("native MongoDB is not the owned healthy replica-set primary")
	}
	h.report.Checks = append(h.report.Checks, "gateway-native-replica-set-and-bootstrap", "gateway-native-nginx-auth-server-ready")
	return h.gatewayImageInventory()
}

var gatewayAssetPaths = map[string]string{
	"common.py":                "test/interoperability/common.py",
	"mcp-card.schema.json":     "schemas/upstream/mcp/mcp-server-card-v1-experimental-526201b/schema.json",
	"mcp-registry.schema.json": "schemas/upstream/mcp/mcp-registry-server-1.8.1-f52dc85/server.schema.json",
	"native.py":                "test/interoperability/gateway/native.py",
	"catalog_origin.py":        "test/interoperability/gateway/catalog_origin.py",
	"conformance-test":         "schemas/upstream/ard/ard-0.91-aa3e598/conformance/bin/conformance-test",
	"ard-entry.schema.json":    "schemas/upstream/ard/ard-0.91-aa3e598/spec/schemas/ard-entry.schema.json",
	"ard.openapi.json":         "schemas/upstream/ard/ard-0.91-aa3e598/spec/schemas/ard.openapi.yaml",
}

func gatewayAssetItems() []any {
	return []any{map[string]string{"key": "catalog_origin.py", "path": "catalog_origin.py"}, map[string]string{"key": "common.py", "path": "common.py"}, map[string]string{"key": "mcp-card.schema.json", "path": "spec/schemas/mcp-card.schema.json"}, map[string]string{"key": "mcp-registry.schema.json", "path": "spec/schemas/mcp-registry.schema.json"}, map[string]string{"key": "native.py", "path": "native.py"}, map[string]string{"key": "conformance-test", "path": "conformance/bin/conformance-test"}, map[string]string{"key": "ard-entry.schema.json", "path": "spec/schemas/ard-entry.schema.json"}, map[string]string{"key": "ard.openapi.json", "path": "spec/schemas/ard.openapi.json"}}
}
func (h *harness) gatewayAssetData() (map[string]string, error) {
	data := make(map[string]string)
	for name, path := range gatewayAssetPaths {
		raw, err := os.ReadFile(filepath.Join(h.options.repositoryRoot, path)) // #nosec G304 -- Paths come only from the fixed gatewayAssetPaths inventory.
		if err != nil {
			return nil, err
		}
		if name == "ard.openapi.json" {
			var document any
			if err := yaml.Unmarshal(raw, &document); err != nil {
				return nil, err
			}
			raw, err = json.Marshal(document)
			if err != nil {
				return nil, err
			}
		}
		data[name] = string(raw)
	}
	return data, nil
}

func (h *harness) gatewayAssets() error {
	data, err := h.gatewayAssetData()
	if err != nil {
		return err
	}
	if err := h.applyObject("mount unchanged conformance checker and pinned schemas", kubernetesObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "gateway-qualification", "namespace": gatewayNamespace}, "data": data}); err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]string{"admin": h.gateway.secrets[0], "reader": h.gateway.secrets[1], "private": h.gateway.secrets[2], "invalid": h.gateway.secrets[5]})
	if err != nil {
		return err
	}
	return h.applyObject("mount private native test credentials", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "gateway-control", "namespace": gatewayNamespace}, "data": map[string]string{"keys.json": base64.StdEncoding.EncodeToString(raw)}})
}

func (h *harness) gatewayImageInventory() error {
	components := map[string]string{"mongodb": "mongodb", "auth-server": "auth", "gateway": "registry", "control": "registry"}
	if h.gateway.report.Lifecycle != nil {
		components["catalog-origin"] = "registry"
	}
	for name, image := range components {
		raw, err := h.kubectl("verify native running "+name+" identity", "-n", gatewayNamespace, "get", "pods", "-l", "component="+name, "-o", "json")
		if err != nil {
			return err
		}
		id, err := gatewayRunningImage(raw, h.gateway.aliases[image])
		if err != nil {
			return err
		}
		h.gateway.report.Workloads[name] = id
	}
	return nil
}

func gatewayRunningImage(raw []byte, expected agntcyImage) (string, error) {
	return gatewayPodImage(raw, expected, false)
}

func gatewayPodImage(raw []byte, expected agntcyImage, completed bool) (string, error) {
	var object struct {
		Items []struct {
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase      string `json:"phase"`
				Containers []struct {
					ImageID string `json:"imageID"`
					Ready   bool   `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", err
	}
	if len(object.Items) != 1 {
		return "", errors.New("ambiguous Gateway Pod inventory")
	}
	pod := object.Items[0]
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != expected.Reference || len(pod.Status.Containers) != 1 || (!completed && !pod.Status.Containers[0].Ready) || (completed && pod.Status.Phase != "Succeeded") {
		return "", errors.New("wrong or unready Gateway workload")
	}
	id := strings.TrimPrefix(pod.Status.Containers[0].ImageID, "docker://")
	if i := strings.LastIndex(id, "@"); i >= 0 {
		id = id[i+1:]
	}
	if id != expected.ID {
		return "", errors.New("gateway running image differs from installed identity")
	}
	return id, nil
}

func (h *harness) verifyGateway(baseURL string) error {
	if err := h.deployGateway(); err != nil {
		return err
	}
	workers, err := h.pods("worker")
	if err != nil || len(workers) != 1 {
		return errors.New("gateway fixture requires one ready Catalog worker")
	}
	address, err := h.startPortForward("pod/"+workers[0].Name, 9090)
	if err != nil {
		return err
	}
	version, err := h.request(http.MethodGet, "http://"+address+"/version", "", "", "", nil, nil)
	var actual map[string]any
	if err != nil || version.Status != 200 || json.Unmarshal(version.Body, &actual) != nil || !matchesManagedFields(actual, h.gateway.report.Build) || actual["role"] != "worker" {
		return errors.New("gateway fixture worker build identity differs")
	}
	if err := h.interopProgress("active-checks", nil, nil); err != nil {
		return err
	}
	if err := h.verifyGatewayIsolation(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Minute)
	raw, runErr := h.runWithInput(ctx, "qualify native authenticated Gateway controls", nil, h.options.kubectl, "--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", gatewayNamespace, "exec", "deployment/control", "--", "/app/.venv/bin/python", "-B", "-c", "import runpy,sys; sys.path.insert(0,'/qualification'); runpy.run_path('/qualification/native.py')['main']()")
	cancel()
	// The transport returns the complete JSON on assertion failure; the supervisor owns its verdict.
	if runErr != nil {
		return runErr
	}
	if err := json.Unmarshal(raw, &h.gateway.report.Qualification); err != nil {
		return err
	}
	output := filepath.Join(h.options.repositoryRoot, "output/interoperability/minikube-gateway-"+h.suffix, "qualification.json")
	if err := os.WriteFile(output, []byte(h.redactDiagnostic(string(raw))), 0600); err != nil {
		return err
	}
	if err := validateGatewayQualification(raw); err != nil {
		return err
	}
	if err := h.qualifyGatewayCatalog(baseURL); err != nil {
		return err
	}
	if err := h.gatewayImageInventory(); err != nil {
		return err
	}
	after, err := h.gatewayFingerprints()
	if err != nil || !reflect.DeepEqual(after, h.gateway.report.Source) {
		return errors.New("Catalog/chart/Gateway fixture source changed during qualification")
	}
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "gateway-native-auth-visibility-pagination-filters", "gateway-model-free-nonempty-lexical-search", "gateway-original-schema-and-official-checkers", "api-worker-managed-build-identity", "gateway-source-stable")
	return h.interopProgress("qualified", nil, nil)
}

// A successful transport or an empty success object cannot qualify the native fixture.
func validateGatewayQualification(raw []byte) error {
	var report struct {
		Status   string                     `json:"status"`
		Error    string                     `json:"error"`
		Evidence map[string]json.RawMessage `json:"evidence"`
		Captures []struct {
			Path       string `json:"path"`
			Credential string `json:"credential"`
			Status     int    `json:"status"`
		} `json:"captures"`
	}
	if json.Unmarshal(raw, &report) != nil || report.Status != "pass" || report.Error != "" {
		return errors.New("native Gateway qualification did not pass")
	}
	for _, key := range []string{"publisher", "descriptors", "tls", "current_discovery", "groups", "characterization", "explore", "no_match", "official", "list", "list_reader", "list_private", "list_types", "list_media", "list_tag", "list_after_denial", "search", "search_reader", "search_private", "search_types", "search_media", "search_tag", "search_after_denial"} {
		if value := report.Evidence[key]; len(value) == 0 || string(value) == "null" || string(value) == "{}" {
			return fmt.Errorf("missing native Gateway evidence: %s", key)
		}
	}
	var official map[string]struct {
		Exit   *int   `json:"exit"`
		Output string `json:"output"`
	}
	if json.Unmarshal(report.Evidence["official"], &official) != nil || len(official) != 3 {
		return errors.New("incomplete official Gateway checker inventory")
	}
	for _, name := range []string{"manifest", "publisher", "registry"} {
		entry, ok := official[name]
		if !ok || entry.Exit == nil || (*entry.Exit != 0 && *entry.Exit != 1) || !strings.Contains(entry.Output, "CONFORMANCE STATUS:") {
			return errors.New("incomplete official Gateway checker result")
		}
	}
	for _, control := range []struct {
		path, credential string
		status           int
	}{
		{"/.well-known/ard.json", "missing", 404}, {"/.well-known/ai-catalog.json", "missing", 200},
		{"/api/ard/agents", "missing", 401}, {"/api/ard/agents", "invalid", 401},
		{"/api/ard/search", "missing", 401}, {"/api/ard/search", "invalid", 401},
		{"/api/servers/toggle", "reader", 403}, {"/api/ard/explore", "admin", 405},
	} {
		found := false
		for _, capture := range report.Captures {
			if capture.Path == control.path && capture.Credential == control.credential && capture.Status == control.status {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("missing native Gateway control: %s %s", control.credential, control.path)
		}
	}
	return nil
}

func (h *harness) gatewayDiagnostics() {
	if h.gateway == nil || !h.clusterReady {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, name := range []string{"mongodb", "bootstrap", "auth-server", "gateway"} {
		raw, _ := h.runWithInput(ctx, "capture owned Gateway "+name+" diagnostics", nil, h.options.kubectl, "--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", gatewayNamespace, "logs", "-l", "component="+name, "--all-containers=true", "--tail=50", "--prefix")
		fmt.Fprintln(os.Stderr, h.redactDiagnostic(string(raw)))
	}
}
