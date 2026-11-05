//go:build minikube

// Directory qualification reuses the owned cluster and current Catalog chart, without a retained lab.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const agntcyNamespace = "agntcy-qualification"
const agntcyOrigin = "https://directory-gateway." + agntcyNamespace + ".svc.cluster.local:8443"

type agntcyImage struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`
}

type agntcyInputs struct {
	Release      string                 `json:"release"`
	Revision     string                 `json:"revision"`
	CLI          string                 `json:"cli_sha256"`
	Chart        string                 `json:"chart_sha256"`
	License      string                 `json:"license_sha256"`
	Dependencies string                 `json:"cli_dependencies_sha256"`
	Images       map[string]agntcyImage `json:"images"`
}

type agntcyReport struct {
	Batch         string            `json:"batch"`
	Inputs        agntcyInputs      `json:"inputs"`
	Build         map[string]string `json:"build"`
	Source        map[string]string `json:"source"`
	Namespace     string            `json:"namespace"`
	Origin        string            `json:"origin"`
	Values        string            `json:"redactedValuesSha256"`
	CatalogValues string            `json:"redactedCatalogValuesSha256"`
	Workloads     map[string]string `json:"runningImages"`
	Aliases       map[string]string `json:"localImageAliases"`
	Native        map[string]int    `json:"nativeHTTP"`
	Qualification map[string]any    `json:"qualification,omitempty"`
	Persistence   map[string]any    `json:"persistence,omitempty"`
}

type agntcyState struct {
	report           agntcyReport
	router           string
	secrets          []string
	tls              map[string]string
	runtimeImages    agntcyInputs
	registryPassword string
}

// agntcyPreflight validates installed inputs and resolves one managed build stamp before mutation.
func (h *harness) agntcyPreflight() error {
	if _, err := h.run("verify mandatory ARD schema tooling", h.agntcyPython(), "-B", "test/packaging/ard_conformance.py", "check"); err != nil {
		return err
	}
	state := &agntcyState{router: "open-agentic-catalog:minikube-agntcy-router-" + h.suffix}
	raw, err := h.agntcyTool("check")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &state.report.Inputs); err != nil {
		return err
	}
	raw, err = h.run("resolve managed Catalog build identity", h.agntcyPython(), "-B", "test/packaging/versioning.py", "metadata")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &state.report.Build); err != nil {
		return err
	}
	state.report.Source, err = h.agntcyFingerprints()
	if err != nil {
		return err
	}
	state.tls, err = serviceTLSMaterial(time.Now(), "owned-directory-ca", agntcyNamespace, []string{"directory-gateway"}, net.ParseIP("127.0.0.1"))
	if err != nil {
		return err
	}
	for range 3 {
		secret, err := randomHex(24)
		if err != nil {
			return err
		}
		state.secrets = append(state.secrets, secret)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	raw, err = h.runWithInput(ctx, "generate private Zot password hash", []byte(state.secrets[1]+"\n"), "htpasswd", "-niB", "-C", "4", "admin")
	cancel()
	if err != nil {
		return err
	}
	state.registryPassword = strings.TrimSpace(string(raw))
	if !strings.HasPrefix(state.registryPassword, "admin:$2y$04$") || len(state.registryPassword) != 66 {
		return errors.New("invalid Zot password hash")
	}
	state.secrets = append(state.secrets, state.registryPassword, state.tls["directory-gateway.key"])
	state.report.Batch, state.report.Namespace, state.report.Origin = "qualification", agntcyNamespace, agntcyOrigin
	state.report.Workloads, state.report.Native = make(map[string]string), make(map[string]int)
	state.runtimeImages = agntcyRuntimeInputs(state.report.Inputs, h.suffix)
	state.report.Aliases = make(map[string]string)
	for name, image := range state.runtimeImages.Images {
		state.report.Aliases[name] = image.Reference
	}
	h.agntcy = state
	h.report.Directory = &state.report
	return nil
}

func (h *harness) agntcyPython() string {
	return environment("CATALOG_AGNTCY_PYTHON", "python3")
}

func (h *harness) agntcyTool(action string) ([]byte, error) {
	return h.run("verify Directory "+action, h.agntcyPython(), "-B", "test/interoperability/agntcy_tools.py", action)
}

func (h *harness) agntcyFingerprints() (map[string]string, error) {
	raw, err := h.agntcyTool("fingerprint")
	if err != nil {
		return nil, err
	}
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil || len(result) != 3 || len(result["catalog"]) != 64 || len(result["harness"]) != 64 || len(result["chart"]) != 64 {
		return nil, errors.New("invalid Catalog/Directory source fingerprint report")
	}
	return result, nil
}

func agntcyBuildArguments(image string, stamp map[string]string) []string {
	return []string{"build", "--pull=false", "--target", "runtime", "--build-arg", "VERSION=" + stamp["version"], "--build-arg", "COMMIT=" + stamp["commit"], "--build-arg", "SOURCE_STATE=" + stamp["sourceState"], "--tag", image, "--file", "Containerfile", "."}
}

func (h *harness) buildAGNTCYRouter() error {
	if _, err := h.run("build current offline Directory router", h.options.docker, "build", "--pull=false", "--network=none", "--tag", h.agntcy.router, "--file", "test/interoperability/agntcy/router/Containerfile", "."); err != nil {
		return err
	}
	raw, err := h.run("record built Directory router", h.options.docker, "image", "inspect", "--format", "{{.Id}}", h.agntcy.router)
	if err != nil {
		return err
	}
	h.report.Images["directory-router"] = strings.TrimSpace(string(raw))
	return nil
}

// agntcyRuntimeInputs names imported images without changing upstream pins or host tags.
func agntcyRuntimeInputs(pinned agntcyInputs, suffix string) agntcyInputs {
	result := pinned
	result.Images = make(map[string]agntcyImage)
	for name, image := range pinned.Images {
		image.Reference = "open-agentic-catalog:minikube-directory-" + name + "-" + suffix
		result.Images[name] = image
	}
	return result
}

func (h *harness) loadAGNTCYImages() error {
	for name, image := range h.agntcy.report.Inputs.Images {
		alias := h.agntcy.runtimeImages.Images[name].Reference
		if _, err := h.run("alias exact Directory "+name, h.options.docker, "image", "tag", image.Reference, alias); err != nil {
			return err
		}
		raw, err := h.run("verify Directory alias identity", h.options.docker, "image", "inspect", "--format", "{{.Id}}", alias)
		if err != nil || strings.TrimSpace(string(raw)) != image.ID {
			return errors.New("directory alias differs from the pinned image")
		}
		ctx, cancel := context.WithTimeout(h.ctx, 3*time.Minute)
		_, err = h.runWithInput(ctx, "load named Directory "+name, nil, h.options.minikube, "--profile", h.profile, "image", "load", "--daemon=true", "--overwrite=true", alias)
		cancel()
		if err != nil {
			return err
		}
	}
	_, err := h.minikube("load current Directory router", "image", "load", "--daemon=true", "--overwrite=true", h.agntcy.router)
	return err
}

// agntcyValues overrides public defaults without changing the upstream chart or containers.
func agntcyValues(inputs agntcyInputs, secrets []string, registryPassword string) map[string]any {
	// Tags include the immutable digest. Split before @ rather than at the digest's colon.
	forName := func(name string) map[string]any {
		reference := inputs.Images[name].Reference
		base, digest, pinned := strings.Cut(reference, "@")
		index := strings.LastIndex(base, ":")
		tag := base[index+1:]
		if pinned {
			tag += "@" + digest
		}
		return map[string]any{"repository": base[:index], "tag": tag, "pullPolicy": "Never"}
	}
	return map[string]any{"global": map[string]any{"security": map[string]any{"allowInsecureImages": true}}, "apiserver": map[string]any{
		"image":   map[string]any{"repository": forName("apiserver")["repository"], "tag": forName("apiserver")["tag"], "pullPolicy": "Never", "pullSecrets": []any{}},
		"service": map[string]any{"type": "ClusterIP"}, "routingService": map[string]any{"type": "ClusterIP", "externalTrafficPolicy": ""},
		"httpGateway": map[string]any{"enabled": true, "publicURL": agntcyOrigin, "ingress": map[string]any{"enabled": false}},
		"spire":       map[string]any{"enabled": false}, "metrics": map[string]any{"enabled": false, "serviceMonitor": map[string]any{"enabled": false}},
		"config": map[string]any{"authn": map[string]any{"enabled": false}, "authz": map[string]any{"enabled": false},
			"routing": map[string]any{"bootstrap_peers": []any{}, "autosync": map[string]any{"enabled": false}},
			"policy":  map[string]any{"operations": []any{map[string]any{"provider": "oasf", "op": []any{"push", "autosync", "index"}, "name": "schema", "schema_url": "http://schema-router." + agntcyNamespace + ".svc.cluster.local:8080"}}, "evaluations": []any{}}},
		"reconciler": map[string]any{"image": forName("reconciler"), "config": map[string]any{
			"indexer": map[string]any{"enabled": true, "interval": "5s"},
			"regsync": map[string]any{"enabled": false}, "identity": map[string]any{"enabled": false}, "signature": map[string]any{"enabled": false}, "scan": map[string]any{"enabled": false}, "metrics": map[string]any{"enabled": false},
			"prune": map[string]any{"enabled": false}, "sync": map[string]any{"enabled": false}, "publish": map[string]any{"enabled": false}, "policy_evaluation": map[string]any{"enabled": false}}},
		"oasf":     map[string]any{"enabled": true, "localDeploy": false, "ingress": map[string]any{"enabled": false}, "image": map[string]any{"repository": forName("oasf")["repository"], "pullPolicy": "Never", "versions": []any{map[string]any{"server": forName("oasf")["tag"], "schema": "1.0.0", "default": true}}}},
		"oasf-sdk": map[string]any{"enabled": false}, "zot": map[string]any{"service": map[string]any{"type": "ClusterIP"}, "image": forName("zot"), "mountConfig": true, "configFiles": map[string]any{"config.json": agntcyZotConfig}, "secretFiles": map[string]any{"htpasswd": registryPassword}},
		"postgresql": map[string]any{"image": agntcyPostgresImage(inputs.Images["postgres"].Reference), "auth": map[string]any{"username": "dir", "password": secrets[0], "postgresPassword": secrets[0], "database": "dir"}, "primary": map[string]any{"persistence": map[string]any{"size": "1Gi"}, "networkPolicy": map[string]any{"enabled": false}}},
		"secrets":    map[string]any{"postgresAuth": map[string]any{"username": "dir", "password": secrets[0]}, "ociAuth": map[string]any{"username": "admin", "password": secrets[1]}, "syncAuth": map[string]any{"username": "sync", "password": secrets[2]}},
	}}
}

// Keep OCI storage and credentials, but do not start Zot's optional public CVE database updater.
const agntcyZotConfig = `{
  "distSpecVersion": "1.1.1",
  "storage": {"rootDirectory": "/var/lib/registry"},
  "http": {"address": "0.0.0.0", "port": "5000",
    "auth": {"htpasswd": {"path": "/secret/htpasswd"}},
    "accessControl": {"adminPolicy": {"users": ["admin"], "actions": ["read", "create", "update", "delete"]},
      "repositories": {"**": {"anonymousPolicy": [], "defaultPolicy": ["read"]}}}},
  "log": {"level": "info"},
  "extensions": {"search": {"enable": false}, "trust": {"enable": false}}
}`

// verifyAGNTCY qualifies native inventory and the supported Catalog overlap against the owned deployment.
func (h *harness) verifyAGNTCY(baseURL string) error {
	if err := h.deployAGNTCY(); err != nil {
		return err
	}
	workers, err := h.pods("worker")
	if err != nil || len(workers) != 1 {
		return errors.New("directory qualification requires one ready worker")
	}
	address, err := h.startPortForward("pod/"+workers[0].Name, 9090)
	if err != nil {
		return err
	}
	version, err := h.request(http.MethodGet, "http://"+address+"/version", "", "", "", nil, nil)
	var actual map[string]any
	if err != nil || version.Status != 200 || json.Unmarshal(version.Body, &actual) != nil || !matchesManagedFields(actual, h.agntcy.report.Build) || actual["role"] != "worker" {
		return errors.New("worker build identity differs from the managed Catalog stamp")
	}
	if err := h.verifyAGNTCYHTTP(); err != nil {
		return err
	}
	if err := h.interopProgress("active-checks", nil, nil); err != nil {
		return err
	}
	if err := h.verifyAGNTCYIsolation(); err != nil {
		return err
	}
	if err := h.qualifyAGNTCY(baseURL); err != nil {
		return err
	}
	raw, err := h.kubectl("verify Directory workloads after qualification", "-n", agntcyNamespace, "get", "pods", "-o", "json")
	if err != nil {
		return err
	}
	images, err := agntcyRunningImages(raw, h.agntcy.runtimeImages, h.agntcy.router, h.report.Images["directory-router"])
	if err != nil {
		return err
	}
	for name, id := range images {
		if h.agntcy.report.Workloads[name] != id {
			return errors.New("directory workload identity changed during qualification")
		}
	}
	h.report.Checks = append(h.report.Checks, "directory-final-workloads-ready")
	after, err := h.agntcyFingerprints()
	if err != nil || after["catalog"] != h.agntcy.report.Source["catalog"] || after["harness"] != h.agntcy.report.Source["harness"] || after["chart"] != h.agntcy.report.Source["chart"] {
		return errors.New("catalog or Directory harness source changed during qualification")
	}
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "directory-native-and-catalog-qualification", "api-worker-managed-build-identity", "directory-source-stable")
	return nil
}

func (h *harness) deployAGNTCY() error {
	if err := h.applyObject("create owned Directory namespace", kubernetesObject{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": agntcyNamespace, "labels": map[string]string{"pod-security.kubernetes.io/enforce": "baseline"}}}); err != nil {
		return err
	}
	if err := h.applyObject("bound owned Directory workload resources", kubernetesObject{"apiVersion": "v1", "kind": "LimitRange", "metadata": map[string]any{"name": "directory-bounds", "namespace": agntcyNamespace}, "spec": map[string]any{"limits": []any{map[string]any{"type": "Container", "default": map[string]string{"cpu": "2", "memory": "1Gi"}, "defaultRequest": map[string]string{"cpu": "50m", "memory": "64Mi"}}}}}); err != nil {
		return err
	}
	data := make(map[string]string)
	for name, value := range h.agntcy.tls {
		data[name] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	if err := h.applyObject("create private Directory gateway TLS Secret", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "directory-tls", "namespace": agntcyNamespace}, "data": data}); err != nil {
		return err
	}
	values := agntcyValues(h.agntcy.runtimeImages, h.agntcy.secrets, h.agntcy.registryPassword)
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	path := filepath.Join(h.temporary, "directory-values.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	h.agntcy.report.Values = fmt.Sprintf("%x", sha256.Sum256([]byte(h.redactDiagnostic(string(raw)))))
	chart := filepath.Join(h.options.repositoryRoot, ".tools/agntcy", h.agntcy.report.Inputs.Release, "dir-"+h.agntcy.report.Inputs.Release+".tgz")
	rendered, err := h.helm("render pinned Directory without hooks", "template", "directory", chart, "--namespace", agntcyNamespace, "--no-hooks", "--values", path)
	if err != nil {
		return err
	}
	if err := validateAGNTCYRender(rendered, h.agntcy.runtimeImages); err != nil {
		return err
	}
	if err := h.agntcyPolicies(); err != nil {
		return err
	}
	for _, schema := range []bool{true, false} {
		for _, object := range h.agntcyRouterObjects(schema) {
			if err := h.applyObject("apply owned Directory router", object); err != nil {
				return err
			}
		}
	}
	if _, err := h.helm("install pinned Directory chart without hooks", "upgrade", "--install", "directory", chart, "--namespace", agntcyNamespace, "--no-hooks", "--values", path, "--wait", "--timeout=6m"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for all Directory Pods", "-n", agntcyNamespace, "wait", "pods", "--all", "--for=condition=Ready", "--timeout=3m"); err != nil {
		return err
	}
	raw, err = h.kubectl("verify pinned Directory workloads", "-n", agntcyNamespace, "get", "pods", "-o", "json")
	if err != nil {
		return err
	}
	images, err := agntcyRunningImages(raw, h.agntcy.runtimeImages, h.agntcy.router, h.report.Images["directory-router"])
	if err != nil {
		return err
	}
	for name, id := range images {
		h.agntcy.report.Workloads[name] = id
	}
	if err := h.interopProgress("directory-ready", nil, nil); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "directory-pinned-chart-and-seven-workloads", "directory-hooks-excluded", "directory-validation-enabled-local-schema")
	return nil
}

func validateAGNTCYRender(raw []byte, inputs agntcyInputs) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	images := make(map[string]bool)
	for {
		var object struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Type     string `yaml:"type"`
				External string `yaml:"externalTrafficPolicy"`
				Template struct {
					Spec struct {
						Containers []struct {
							Image  string `yaml:"image"`
							Policy string `yaml:"imagePullPolicy"`
						} `yaml:"containers"`
						Init []any `yaml:"initContainers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		if slices.Contains([]string{"ClusterRole", "ClusterRoleBinding", "CustomResourceDefinition", "IngressClass", "Ingress", "NetworkPolicy"}, object.Kind) || object.Kind == "Service" && (object.Spec.Type != "ClusterIP" || object.Spec.External != "") || len(object.Spec.Template.Spec.Init) != 0 {
			return errors.New("directory chart renders unselected cluster authority, ingress, init container or public Service")
		}
		for _, container := range object.Spec.Template.Spec.Containers {
			found := false
			for name, image := range inputs.Images {
				if agntcyImageMatches(container.Image, image.Reference) && container.Policy == "Never" {
					images[name], found = true, true
				}
			}
			if !found {
				return errors.New("directory chart renders an unpinned or downloadable workload image")
			}
		}
	}
	if len(images) != 5 {
		return errors.New("directory chart does not render the five pinned workloads")
	}
	return nil
}

func agntcyImageMatches(actual, expected string) bool {
	if strings.HasPrefix(expected, "open-agentic-catalog:minikube-directory-") {
		return strings.TrimPrefix(actual, "docker.io/library/") == expected
	}
	normalize := func(value string) string {
		base, digest, ok := strings.Cut(value, "@")
		if !ok {
			return ""
		}
		if at := strings.LastIndex(base, ":"); at > strings.LastIndex(base, "/") {
			base = base[:at]
		}
		return base + "@" + digest
	}
	return normalize(actual) != "" && normalize(actual) == normalize(expected)
}

func agntcyRunningImages(raw []byte, inputs agntcyInputs, router, routerID string) (map[string]string, error) {
	var document struct {
		Items []struct {
			Metadata struct {
				Name    string  `json:"name"`
				Deleted *string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Containers []struct {
					Name    string `json:"name"`
					Ready   bool   `json:"ready"`
					ImageID string `json:"imageID"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	result := make(map[string]string)
	found := make(map[string]int)
	for _, pod := range document.Items {
		if pod.Metadata.Deleted != nil || len(pod.Spec.Containers) != 1 || len(pod.Status.Containers) != 1 {
			return nil, errors.New("ambiguous Directory workload")
		}
		container, status := pod.Spec.Containers[0], pod.Status.Containers[0]
		label, expectedID := "", ""
		if container.Image == router {
			label, expectedID = "router", routerID
		} else {
			for name, image := range inputs.Images {
				if agntcyImageMatches(container.Image, image.Reference) {
					label, expectedID = name, image.ID
				}
			}
		}
		id := strings.TrimPrefix(status.ImageID[strings.LastIndex(status.ImageID, "@")+1:], "docker://")
		if label == "" || !status.Ready || status.Name != container.Name || id != expectedID {
			return nil, errors.New("directory workload is unready or differs from its pinned image")
		}
		found[label]++
		result[pod.Metadata.Name] = status.ImageID
	}
	if len(result) != 7 || found["router"] != 2 || len(found) != 6 {
		return nil, errors.New("incomplete Directory workload inventory")
	}
	for name := range inputs.Images {
		if found[name] != 1 {
			return nil, errors.New("duplicate Directory workload")
		}
	}
	return result, nil
}

func (h *harness) agntcyRouterObjects(schema bool) []kubernetesObject {
	name, port := "schema-router", 8080
	arguments := []string{"--listen=:8080", "--schema", "--upstream=http://directory-oasf-1-0-0." + agntcyNamespace + ".svc.cluster.local:8080"}
	if !schema {
		name, port = "directory-gateway", 8443
		arguments = []string{"--listen=:8443", "--upstream=http://directory-apiserver." + agntcyNamespace + ".svc.cluster.local:8889", "--cert=/tls/directory-gateway.crt", "--key=/tls/directory-gateway.key"}
		if h.options.interopTrust {
			arguments = append(arguments, "--record-requests")
		}
	}
	container := map[string]any{"name": "router", "image": h.agntcy.router, "imagePullPolicy": "Never", "args": arguments,
		"resources":       map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "16Mi"}, "limits": map[string]string{"cpu": "250m", "memory": "128Mi"}},
		"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
		"readinessProbe":  map[string]any{"httpGet": map[string]any{"path": "/healthz", "port": port, "scheme": map[bool]string{true: "HTTP", false: "HTTPS"}[schema]}}}
	spec := map[string]any{"automountServiceAccountToken": false, "containers": []any{container}}
	if !schema {
		spec["volumes"] = []any{map[string]any{"name": "tls", "secret": map[string]any{"secretName": "directory-tls"}}}
		container["volumeMounts"] = []any{map[string]any{"name": "tls", "mountPath": "/tls", "readOnly": true}}
	}
	labels := map[string]string{"app": "directory-router", "component": name}
	return []kubernetesObject{
		{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": name, "namespace": agntcyNamespace}, "spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": labels}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": spec}}},
		{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": name, "namespace": agntcyNamespace}, "spec": map[string]any{"type": "ClusterIP", "selector": labels, "ports": []any{map[string]any{"port": port, "targetPort": port}}}},
	}
}

// interopProgress retains ownership and redacted results even when setup is interrupted.
func (h *harness) interopProgress(phase string, runErr, cleanupErr error) error {
	if h.options.scenario != "agntcy" && h.options.scenario != "gateway" && h.options.scenario != "ans" {
		return nil
	}
	directory := filepath.Join(h.options.repositoryRoot, "output", "interoperability", "minikube-"+h.options.scenario+"-"+h.suffix)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	record := map[string]any{"phase": phase, "pid": os.Getpid(), "profile": h.profile, "network": h.network, "temporary": h.temporary, "application": h.application, "fixture": h.fixture, "postgres": h.postgres, "report": h.report}
	if h.ans != nil {
		record["ansAlias"], record["router"] = h.ans.alias, h.ans.router
	}
	if h.gateway != nil {
		record["gatewayAliases"] = h.gateway.aliases
	}
	if h.agntcy != nil {
		record["router"] = h.agntcy.router
	}
	if phase == "finished" {
		record["passed"] = runErr == nil
		record["cleanupPassed"] = cleanupErr == nil
	}
	for name, err := range map[string]error{"error": runErr, "cleanupError": cleanupErr} {
		if err != nil {
			value := h.redactDiagnostic(err.Error())
			if len(value) > 32768 {
				value = value[:32768]
			}
			record[name] = value
		}
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	// Rename one complete private file so observers never read a half-written phase.
	staged := filepath.Join(directory, "result.json.new")
	if err = os.WriteFile(staged, append(raw, '\n'), 0600); err != nil {
		return err
	}
	if err = os.Rename(staged, filepath.Join(directory, "result.json")); err != nil {
		return err
	}
	fmt.Printf("[%s] %s: %s\n", h.options.scenario, phase, filepath.Join(directory, "result.json"))
	return nil
}

// agntcyDiagnostics retains namespace-specific state with an independent failure-time bound.
func (h *harness) agntcyDiagnostics() {
	if h.agntcy == nil || !h.clusterReady {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, tail := range [][]string{{"get", "pods,services,statefulsets,deployments", "-o", "wide"}, {"get", "events", "--sort-by=.metadata.creationTimestamp"}, {"logs", "--all-containers=true", "--tail=80", "--prefix", "-l", "app.kubernetes.io/instance=directory"}} {
		args := []string{"--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", agntcyNamespace}
		output, err := h.runWithInput(ctx, "capture owned Directory failure diagnostics", nil, h.options.kubectl, append(args, tail...)...)
		if len(output) > 0 {
			fmt.Fprintln(os.Stderr, h.redactDiagnostic(string(output)))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, h.redactDiagnostic(err.Error()))
		}
	}
}

// installManagedCatalog observes the short-lived owner migration before Helm removes it.
func (h *harness) installManagedCatalog(arguments []string) error {
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	type observation struct {
		id  string
		err error
	}
	observed := make(chan observation, 1)
	go func() {
		timer := time.NewTicker(200 * time.Millisecond)
		defer timer.Stop()
		for {
			raw, err := h.runWithInput(ctx, "observe managed migration image", nil, h.options.kubectl, "--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/component=migration", "-o", "json")
			if err != nil {
				observed <- observation{err: err}
				return
			}
			id, err := managedMigrationImage(raw, h.application, h.report.Images["application"])
			if err != nil || id != "" {
				observed <- observation{id, err}
				return
			}
			select {
			case <-ctx.Done():
				observed <- observation{err: ctx.Err()}
				return
			case <-timer.C:
			}
		}
	}()
	_, installErr := h.helm("install managed Catalog through Helm", arguments...)
	cancel()
	result := <-observed
	if installErr != nil {
		return installErr
	}
	if result.err != nil {
		return result.err
	}
	if result.id == "" {
		return errors.New("managed migration image was not observed")
	}
	h.managedWorkloads()["catalog-migration"] = result.id
	h.report.Checks = append(h.report.Checks, "migration-managed-image-identity")
	return nil
}

func managedMigrationImage(raw []byte, image, expectedID string) (string, error) {
	var document struct {
		Items []struct {
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Containers []struct {
					ImageID string `json:"imageID"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", err
	}
	if len(document.Items) == 0 {
		return "", nil
	}
	if len(document.Items) != 1 {
		return "", errors.New("ambiguous migration Pod")
	}
	pod := document.Items[0]
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != image {
		return "", errors.New("migration uses a different build")
	}
	if len(pod.Status.Containers) == 0 {
		return "", nil
	}
	if len(pod.Status.Containers) != 1 {
		return "", errors.New("ambiguous migration container")
	}
	id := strings.TrimPrefix(pod.Status.Containers[0].ImageID, "docker://")
	if id == "" {
		return "", nil
	}
	if id != expectedID {
		return "", errors.New("migration running image differs from the build")
	}
	return id, nil
}

func agntcyPostgresImage(reference string) map[string]any {
	if base, digest, pinned := strings.Cut(reference, "@"); pinned {
		return map[string]any{"registry": "docker.io", "repository": strings.TrimPrefix(base, "docker.io/"), "tag": "18", "digest": digest, "pullPolicy": "Never"}
	}
	base, tag, _ := strings.Cut(reference, ":")
	return map[string]any{"registry": "docker.io", "repository": "library/" + base, "tag": tag, "digest": "", "pullPolicy": "Never"}
}
