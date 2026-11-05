//go:build minikube

package main

import (
	"context"
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

const ansNamespace = "ans-qualification"
const ansOrigin = "https://finder-edge." + ansNamespace + ".svc.cluster.local:8443"
const ansTLOrigin = "https://tl." + ansNamespace + ".svc.cluster.local"

type ansReport struct {
	Inputs              map[string]any            `json:"inputs"`
	Build               map[string]string         `json:"build"`
	Source              map[string]string         `json:"source"`
	Workloads           map[string]string         `json:"runningImages"`
	CatalogValues       string                    `json:"redactedCatalogValuesSha256"`
	Qualification       map[string]any            `json:"qualification,omitempty"`
	Capture             map[string]any            `json:"capture,omitempty"`
	Trust               map[string]any            `json:"trust,omitempty"`
	TrustConfigurations map[string]string         `json:"trustConfigurations,omitempty"`
	Lifecycle           map[string]any            `json:"lifecycle,omitempty"`
	Persistence         map[string]any            `json:"persistence,omitempty"`
	Versions            map[string]map[string]any `json:"versions"`
	Namespace           string                    `json:"namespace"`
	Storage             []string                  `json:"storage"`
}
type ansState struct {
	report      ansReport
	alias       agntcyImage
	router      string
	tls         map[string]string
	secrets     []string
	trustConfig map[string]any
	publicKeys  map[string]string
}

func (h *harness) ansPython() string { return environment("CATALOG_ANS_PYTHON", "python3") }
func (h *harness) ansTool(action string) ([]byte, error) {
	return h.run("verify ANS "+action, h.ansPython(), "-B", "test/interoperability/ans_tools.py", action)
}
func (h *harness) ansFingerprints() (map[string]string, error) {
	raw, err := h.ansTool("fingerprint")
	if err != nil {
		return nil, err
	}
	var result map[string]string
	if json.Unmarshal(raw, &result) != nil || len(result) != 3 {
		return nil, errors.New("incomplete ANS source fingerprints")
	}
	return result, nil
}
func (h *harness) ansPreflight() error {
	if _, err := h.run("verify mandatory ARD schema tooling", h.ansPython(), "-B", "test/packaging/ard_conformance.py", "check"); err != nil {
		return err
	}
	raw, err := h.ansTool("check")
	if err != nil {
		return err
	}
	state := &ansState{router: "open-agentic-catalog:minikube-ans-router-" + h.suffix}
	var installed struct {
		Images map[string]agntcyImage `json:"images"`
	}
	if json.Unmarshal(raw, &state.report.Inputs) != nil || json.Unmarshal(raw, &installed) != nil || len(installed.Images) != 1 {
		return errors.New("incomplete ANS image inventory")
	}
	state.alias = agntcyImage{ID: installed.Images["native"].ID, Reference: "open-agentic-catalog:minikube-ans-native-" + h.suffix}
	raw, err = h.run("resolve managed Catalog build", h.ansPython(), "-B", "test/packaging/versioning.py", "metadata")
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &state.report.Build); err != nil {
		return err
	}
	state.report.Source, err = h.ansFingerprints()
	if err != nil {
		return err
	}
	state.report.Workloads = make(map[string]string)
	state.report.Namespace = ansNamespace
	state.report.Storage = []string{"ra-state", "tl-state", "finder-state"}
	state.tls, err = serviceTLSMaterial(time.Now(), "owned-ans-ca", ansNamespace, []string{"finder-edge"}, net.ParseIP("127.0.0.1"))
	if err != nil {
		return err
	}
	for range 2 {
		value, err := randomHex(24)
		if err != nil {
			return err
		}
		state.secrets = append(state.secrets, value)
	}
	state.secrets = append(state.secrets, state.tls["finder-edge.key"])
	h.ans, h.report.ANS = state, &state.report
	return h.interopProgress("inputs-verified", nil, nil)
}
func (h *harness) buildANSRouter() error {
	if _, err := h.run("build offline ANS HTTPS edge", h.options.docker, "build", "--pull=false", "--network=none", "--tag", h.ans.router, "--file", "test/interoperability/agntcy/router/Containerfile", "."); err != nil {
		return err
	}
	raw, err := h.run("record ANS edge image", h.options.docker, "image", "inspect", "--format", "{{.Id}}", h.ans.router)
	if err != nil {
		return err
	}
	h.report.Images["ans-router"] = strings.TrimSpace(string(raw))
	return nil
}
func (h *harness) loadANSImages() error {
	var installed struct {
		Images map[string]agntcyImage `json:"images"`
	}
	raw, err := json.Marshal(h.ans.report.Inputs)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &installed); err != nil {
		return err
	}
	if _, err := h.run("alias exact ANS image", h.options.docker, "image", "tag", installed.Images["native"].Reference, h.ans.alias.Reference); err != nil {
		return err
	}
	for _, image := range []string{h.ans.alias.Reference, h.ans.router} {
		if _, err := h.minikube("load exact ANS tool", "image", "load", "--daemon=true", "--overwrite=true", image); err != nil {
			return err
		}
	}
	return nil
}

// ansConfiguration selects development identity checks explicitly; no public service is consulted.
func ansConfiguration(component string, secrets []string, publicKey string) map[string]any {
	cfg := map[string]any{"server": map[string]any{"host": "0.0.0.0", "port": 8080}, "store": map[string]any{"type": "sqlite", "sqlite": map[string]string{"path": "/ans-state/ans.db"}}, "log": map[string]string{"level": "info", "format": "text"}}
	switch component {
	case "ra":
		cfg["auth"] = map[string]any{"type": "static", "static": map[string]string{"api-key": secrets[0]}}
		cfg["ca"] = map[string]any{"type": "self", "self": map[string]any{"org": "Owned ANS development CA", "validity-days": 365, "data-dir": "/ans-state/ca"}, "server": map[string]any{"type": "self", "org": "Owned ANS server CA", "validity-days": 365, "data-dir": "/ans-state/server-ca"}}
		cfg["dns"] = map[string]string{"type": "noop"}
		cfg["identity"] = map[string]any{"resolver": map[string]string{"type": "noop"}, "challenge-ttl": "1h"}
		cfg["vlei"] = map[string]string{"type": "noop"}
		cfg["keys"] = map[string]any{"type": "file", "file": map[string]string{"path": "/ans-state/keys"}}
		cfg["tl-client"] = map[string]any{"base-url": "http://tl:8080", "public-base-url": ansTLOrigin, "api-key": secrets[1], "timeout": "5s", "poll-interval": "1s", "max-backoff": "2s"}
		cfg["signer"] = map[string]string{"keyId": "ans-ra-signer", "raId": "ans-ra-local"}
	case "tl":
		cfg["auth"] = map[string]any{"type": "static", "static": map[string]string{"api-key": secrets[1]}, "public-read": true}
		cfg["keys"] = map[string]any{"type": "file", "file": map[string]string{"path": "/ans-state/keys"}}
		cfg["merkle"] = map[string]any{"origin": "owned-ans", "tile-storage": map[string]any{"type": "filesystem", "filesystem": map[string]string{"path": "/ans-state/tiles"}}, "checkpoint-interval": "1s"}
		cfg["attestation"] = map[string]string{"keyId": "ans-tl-attestation"}
		cfg["producerKeys"] = []any{map[string]string{"raId": "ans-ra-local", "keyId": "ans-ra-signer", "algorithm": "ES256", "publicKeyPem": publicKey}}
	case "finder":
		cfg["feed"] = map[string]any{"base-url": "http://ra:8080", "allow-http": true, "poll-interval": "1s", "page-size": 100, "timeout": "5s", "stale-bound": "5s"}
		cfg["tl"] = map[string]string{"public-base-url": ansTLOrigin}
		cfg["search"] = map[string]any{"source-url": ansOrigin, "rate": 50, "burst": 100, "max-page-size": 1, "default-page-size": 1}
		cfg["referrals"] = []any{}
	}
	return cfg
}
func (h *harness) ansDeploy(component string, publicKey string) error {
	if err := h.applyObject("create owned ANS private state", kubernetesObject{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": component + "-state", "namespace": ansNamespace}, "spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": "1Gi"}}}}); err != nil {
		return err
	}

	cfg := ansConfiguration(component, h.ans.secrets, publicKey)
	if h.options.ansTrust {
		if component == "ra" {
			client, ok := cfg["tl-client"].(map[string]any)
			if !ok {
				return errors.New("invalid native TL configuration")
			}
			client["public-base-url"] = ansOrigin
		}
		if component == "finder" {
			client, ok := cfg["tl"].(map[string]string)
			if !ok {
				return errors.New("invalid Finder TL configuration")
			}
			client["public-base-url"] = ansOrigin
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := h.applyObject("configure private ANS "+component, kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": component + "-config", "namespace": ansNamespace}, "stringData": map[string]string{"config.yaml": string(raw)}}); err != nil {
		return err
	}
	definition := gatewayWorkload(component, h.ans.alias.Reference, nil)
	definition.object["metadata"] = map[string]any{"name": component, "namespace": ansNamespace}
	workloadSpec, ok := definition.object["spec"].(map[string]any)
	if !ok {
		return errors.New("invalid native ANS workload template")
	}
	workloadSpec["strategy"] = map[string]string{"type": "Recreate"}
	pod, container := definition.pod, definition.container
	pod["securityContext"] = map[string]any{"runAsUser": 65532, "runAsGroup": 65532, "runAsNonRoot": true, "fsGroup": 65532}
	pod["volumes"] = []any{map[string]any{"name": "config", "secret": map[string]string{"secretName": component + "-config"}}, map[string]any{"name": "state", "persistentVolumeClaim": map[string]string{"claimName": component + "-state"}}}
	container["command"] = []string{"/ans-" + component}
	container["args"] = []string{"--config=/config/config.yaml"}
	container["securityContext"] = map[string]any{"readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}
	container["env"] = []any{map[string]string{"name": "TMPDIR", "value": "/ans-state"}}
	container["volumeMounts"] = []any{map[string]any{"name": "config", "mountPath": "/config", "readOnly": true}, map[string]any{"name": "state", "mountPath": "/ans-state"}}
	path := "/v2/admin/ready"
	if component == "finder" {
		path = "/v1/admin/ready"
	}
	container["readinessProbe"] = map[string]any{"httpGet": map[string]any{"path": path, "port": 8080}, "periodSeconds": 2, "failureThreshold": 60}
	// A separate owned utility can export only the generated public key and run bounded network controls.
	if component == "ra" || component == "tl" && (h.options.ansCapture || h.options.ansTrust) {
		probe := map[string]any{"name": "public-key", "image": h.fixture, "imagePullPolicy": "Never", "command": []string{"/federation-fixture", "network-probe", "hold"}, "volumeMounts": []any{map[string]any{"name": "state", "mountPath": "/ans-state", "readOnly": true}}, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}}}}
		pod["containers"] = []any{container, probe}
	}
	if err := h.applyObject("deploy native ANS "+component, definition.object); err != nil {
		return err
	}
	if err := h.applyObject("create owned ANS Service", kubernetesObject{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": component, "namespace": ansNamespace}, "spec": map[string]any{"selector": map[string]string{"component": component}, "ports": []any{map[string]any{"port": 8080, "targetPort": 8080}}}}); err != nil {
		return err
	}
	_, err = h.kubectl("wait for native ANS "+component, "-n", ansNamespace, "rollout", "status", "deployment/"+component, "--timeout=2m")
	return err
}
func (h *harness) deployANS() error {
	if err := h.applyObject("create owned ANS namespace", kubernetesObject{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ansNamespace, "labels": map[string]string{"pod-security.kubernetes.io/enforce": "baseline"}}}); err != nil {
		return err
	}
	if err := h.interopPolicies("ans", ansNamespace, "finder-edge", []int{8080, 8443}); err != nil {
		return err
	}
	if err := h.ansDeploy("ra", ""); err != nil {
		return err
	}
	raw, err := h.kubectl("obtain native RA producer public key", "-n", ansNamespace, "exec", "deployment/ra", "-c", "public-key", "--", "/federation-fixture", "ans-public-key")
	if err != nil {
		return err
	}
	if err = h.ansDeploy("tl", string(raw)); err != nil {
		return err
	}
	if err = h.ansDeploy("finder", ""); err != nil {
		return err
	}
	tls := map[string]string{"finder.crt": h.ans.tls["finder-edge.crt"], "finder.key": h.ans.tls["finder-edge.key"]}
	if err = h.applyObject("create owned ANS edge TLS", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "ans-tls", "namespace": ansNamespace}, "stringData": tls}); err != nil {
		return err
	}
	definition := gatewayWorkload("finder-edge", h.ans.router, nil)
	definition.object["metadata"] = map[string]any{"name": "finder-edge", "namespace": ansNamespace}
	definition.container["args"] = []string{"--listen=:8443", "--upstream=http://finder:8080", "--cert=/tls/finder.crt", "--key=/tls/finder.key", "--record-requests"}
	if h.options.ansTrust {
		args, ok := definition.container["args"].([]string)
		if !ok {
			return errors.New("invalid edge arguments")
		}
		definition.container["args"] = append(args, "--evidence-upstream=http://tl:8080")
	}
	definition.container["volumeMounts"] = []any{map[string]any{"name": "tls", "mountPath": "/tls", "readOnly": true}}
	definition.container["readinessProbe"] = map[string]any{"httpGet": map[string]any{"path": "/healthz", "port": 8443, "scheme": "HTTPS"}, "periodSeconds": 2}
	definition.pod["volumes"] = []any{map[string]any{"name": "tls", "secret": map[string]string{"secretName": "ans-tls"}}}
	if err = h.applyObject("deploy byte-preserving ANS HTTPS edge", definition.object); err != nil {
		return err
	}
	if err = h.applyObject("create ANS HTTPS Service", kubernetesObject{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "finder-edge", "namespace": ansNamespace}, "spec": map[string]any{"selector": map[string]string{"component": "finder-edge"}, "ports": []any{map[string]any{"port": 8443, "targetPort": 8443}}}}); err != nil {
		return err
	}
	_, err = h.kubectl("wait for ANS HTTPS edge", "-n", ansNamespace, "rollout", "status", "deployment/finder-edge", "--timeout=2m")
	return err
}
func (h *harness) ansImageInventory() error {
	for _, name := range []string{"ra", "tl", "finder", "finder-edge"} {
		raw, err := h.kubectl("verify actual ANS "+name+" image", "-n", ansNamespace, "get", "pods", "-l", "component="+name, "-o", "json")
		if err != nil {
			return err
		}
		expected := h.ans.alias
		if name == "finder-edge" {
			expected = agntcyImage{Reference: h.ans.router, ID: h.report.Images["ans-router"]}
		}
		if name == "ra" || name == "tl" && (h.options.ansCapture || h.options.ansTrust) { // Verify native and owned public-key utility separately.
			var pods struct {
				Items []struct {
					Status struct {
						Containers []struct {
							Name  string `json:"name"`
							ID    string `json:"imageID"`
							Ready bool   `json:"ready"`
						} `json:"containerStatuses"`
					} `json:"status"`
				} `json:"items"`
			}
			if json.Unmarshal(raw, &pods) != nil || len(pods.Items) != 1 {
				return errors.New("invalid ANS public-key Pod inventory")
			}
			seen := 0
			for _, c := range pods.Items[0].Status.Containers {
				want := expected.ID
				if c.Name == "public-key" {
					want = h.report.Images["fixture"]
				} else if c.Name != name {
					return errors.New("unexpected ANS public-key Pod container")
				}
				if !c.Ready || c.ID != want {
					return errors.New("ANS public-key Pod container identity differs")
				}
				h.ans.report.Workloads[c.Name] = c.ID
				seen++
			}
			if seen != 2 {
				return errors.New("incomplete ANS public-key Pod container inventory")
			}
		} else {
			id, err := gatewayRunningImage(raw, expected)
			if err != nil {
				return err
			}
			h.ans.report.Workloads[name] = id
		}
	}
	return nil
}
func (h *harness) verifyANS(baseURL string) error {
	if err := h.deployANS(); err != nil {
		return err
	}
	if err := h.ansImageInventory(); err != nil {
		return err
	}
	workers, err := h.pods("worker")
	if err != nil || len(workers) != 1 {
		return errors.New("ANS requires one ready Catalog worker")
	}
	address, err := h.startPortForward("pod/"+workers[0].Name, 9090)
	if err != nil {
		return err
	}
	version, err := h.request(http.MethodGet, "http://"+address+"/version", "", "", "", nil, nil)
	var identity map[string]any
	if err != nil || version.Status != 200 || json.Unmarshal(version.Body, &identity) != nil || !matchesManagedFields(identity, h.ans.report.Build) || identity["role"] != "worker" {
		return errors.New("ANS Catalog worker build identity differs")
	}
	h.ans.report.Versions = map[string]map[string]any{"worker": identity}
	apiVersion, err := h.request(http.MethodGet, baseURL+"/version", "", "", "", nil, nil)
	var apiIdentity map[string]any
	if err != nil || apiVersion.Status != 200 || json.Unmarshal(apiVersion.Body, &apiIdentity) != nil || !matchesManagedFields(apiIdentity, h.ans.report.Build) {
		return errors.New("ANS Catalog API version differs")
	}
	h.ans.report.Versions["api"] = apiIdentity
	if err := h.interopProgress("active-checks", nil, nil); err != nil {
		return err
	}
	if err := h.verifyInteropIsolation("ans", ansNamespace, "ra", "finder-edge"); err != nil {
		return err
	}
	settings := map[string]any{"origin": ansOrigin, "tl_origin": ansTLOrigin, "key": h.ans.secrets[0], "output": filepath.Join(h.options.repositoryRoot, "output/interoperability/minikube-ans-"+h.suffix)}
	if h.options.ansTrust {
		settings["tl_origin"] = ansOrigin
	}
	for _, name := range []string{"ra", "tl", "finder", "finder-edge"} {
		port := 8080
		if name == "finder-edge" {
			port = 8443
		}
		value, err := h.startPortForwardIn(ansNamespace, "service/"+name, port)
		if err != nil {
			return err
		}
		scheme := "http://"
		if port == 8443 {
			scheme = "https://"
		}
		settings[name] = scheme + value
	}
	ca := filepath.Join(h.temporary, "ans-ca.pem")
	if err = os.WriteFile(ca, []byte(h.ans.tls["ca.pem"]), 0600); err != nil {
		return err
	}
	settings["ca"] = ca
	schemaRaw, err := os.ReadFile(filepath.Join(h.options.repositoryRoot, "schemas/upstream/ard/ard-0.91-aa3e598/spec/schemas/ard.openapi.yaml"))
	if err != nil {
		return err
	}
	var schema any
	if err = yaml.Unmarshal(schemaRaw, &schema); err != nil {
		return err
	}
	schemaRaw, err = json.Marshal(schema)
	if err != nil {
		return err
	}
	schemaPath := filepath.Join(h.temporary, "ans-api.schema.json")
	if err = os.WriteFile(schemaPath, schemaRaw, 0600); err != nil {
		return err
	}
	settings["schema"] = schemaPath

	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(h.temporary, "ans-control.json")
	if err = os.WriteFile(settingsPath, raw, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Minute)
	defer cancel()
	raw, err = h.runWithInput(ctx, "qualify real ANS registration and discovery", nil, h.ansPython(), "-B", "test/interoperability/ans/native.py", "--settings", settingsPath)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, &h.ans.report.Qualification) != nil {
		return errors.New("invalid ANS qualification report")
	}
	if err = validateANSQualification(raw); err != nil {
		return err
	}
	if err = h.qualifyANSCatalog(baseURL, settings); err != nil {
		return err
	}
	if h.options.ansTrust {
		if err = h.qualifyANSTrust(settings); err != nil {
			return err
		}
		var ok bool
		baseURL, ok = settings["catalog"].(string)
		if !ok {
			return errors.New("missing restarted Catalog address")
		}
	}
	if err = h.ansImageInventory(); err != nil {
		return err
	}
	after, err := h.ansFingerprints()
	if err != nil || !reflect.DeepEqual(after, h.ans.report.Source) {
		return errors.New("ANS/Catalog/chart source changed during qualification")
	}
	if err = h.waitHTTP(baseURL + "/readyz"); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "ans-native-registration-feed-paging-filters-explore", "ans-original-schema-and-official-checkers", "api-worker-managed-build-identity", "ans-source-stable")
	return h.interopProgress("qualified", nil, nil)
}
func validateANSQualification(raw []byte) error {
	var result struct {
		Status string                     `json:"status"`
		Phases map[string]json.RawMessage `json:"phases"`
		Counts map[string]int             `json:"counts"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Status != "pass" || result.Counts["registered"] != 4 || result.Counts["discoverable"] != 3 {
		return errors.New("incomplete native ANS qualification")
	}
	for _, name := range []string{"registrations", "feed", "search", "filters", "explore", "negative", "schemas", "official", "tls", "shared_identity"} {
		if value := result.Phases[name]; len(value) == 0 || string(value) == "null" || string(value) == "{}" {
			return fmt.Errorf("missing ANS phase: %s", name)
		}
	}
	return nil
}
func (h *harness) ansDiagnostics() {
	if h.ans == nil || !h.clusterReady {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, name := range []string{"ra", "tl", "finder"} {
		raw, _ := h.runWithInput(ctx, "capture owned ANS diagnostics", nil, h.options.kubectl, "--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", ansNamespace, "logs", "deployment/"+name, "-c", name, "--tail=50")
		fmt.Fprintln(os.Stderr, h.redactDiagnostic(string(raw)))
	}
}
