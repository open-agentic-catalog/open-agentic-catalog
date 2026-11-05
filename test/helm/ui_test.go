//go:build helm

package helm_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOptionalUIRender(t *testing.T) {
	objects := renderChart(t, "--values", filepath.Join(chartPath(t), "ci/ui-values.yaml"))
	deployment := oneObject(t, objects, "Deployment", "ui")
	service := oneObject(t, objects, "Service", "ui")
	assertSelectorMatches(t, service, deployment)
	pod := mapAt(t, deployment, "spec", "template", "spec")
	if boolAt(t, pod, "automountServiceAccountToken") {
		t.Fatal("UI mounted a service account token")
	}
	containers := sliceAt(t, pod, "containers")
	container := asMap(t, containers[0])
	if len(containers) != 1 || container["envFrom"] != nil || container["env"] != nil {
		t.Fatal("UI inherited backend configuration")
	}
	for probe, path := range map[string]string{"startupProbe": "/livez", "livenessProbe": "/livez", "readinessProbe": "/readyz"} {
		if stringAt(t, container, probe, "httpGet", "path") != path {
			t.Fatalf("wrong UI %s", probe)
		}
	}
	if !boolAt(t, container, "securityContext", "readOnlyRootFilesystem") || boolAt(t, container, "securityContext", "allowPrivilegeEscalation") {
		t.Fatal("UI lacks restricted container security")
	}
	volumes := sliceAt(t, pod, "volumes")
	if len(volumes) != 4 {
		t.Fatal("UI must mount only config, scoped session inputs and the two selected trust bundles")
	}
	for _, value := range volumes {
		volume := asMap(t, value)
		if secret, ok := volume["secret"].(map[string]any); ok {
			if secret["secretName"] != "catalog-ui-session" {
				t.Fatal("UI mounted an unrelated Secret")
			}
		}
	}
	configMap := oneObject(t, objects, "ConfigMap", "ui")
	var config map[string]any
	if err := json.Unmarshal([]byte(stringAt(t, configMap, "data", "config.json")), &config); err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{"upstream": "http://open-agentic-catalog-open-agentic-catalog:8080", "basePath": "/catalog", "keysFile": "/secrets/keys.json", "clientSecretFile": "/secrets/client-secret", "apiCAFile": "/trust/api/ca.pem", "issuerCAFile": "/trust/issuer/ca.pem"} {
		if config[key] != expected {
			t.Errorf("UI config %s = %v", key, config[key])
		}
	}
	if config["developmentUsersFile"] != nil {
		t.Fatal("OIDC UI gained test identities")
	}
	for _, component := range []string{"api", "worker"} {
		baseline := oneObject(t, renderChart(t), "Deployment", component)
		if !reflect.DeepEqual(baseline, oneObject(t, objects, "Deployment", component)) {
			t.Fatalf("enabling UI changed %s", component)
		}
	}
	_ = oneObject(t, objects, "Ingress", "ui")
	_ = oneObject(t, objects, "NetworkPolicy", "ui")
}

func TestUIDocumentationIngressAndOverride(t *testing.T) {
	for _, base := range []string{"", "/catalog"} {
		for _, override := range []string{"", "https://docs.example.test/manual/"} {
			objects := renderChart(t, "--values", filepath.Join(chartPath(t), "ci/ui-values.yaml"), "--set-string", "ui.config.basePath="+base, "--set-string", "ui.config.documentationUrl="+override)
			ingress := oneObject(t, objects, "Ingress", "ui")
			rule := asMap(t, sliceAt(t, ingress, "spec", "rules")[0])
			paths := sliceAt(t, rule, "http", "paths")
			wantPaths := []string{"/"}
			if base != "" {
				wantPaths = []string{base, "/docs"}
			}
			if len(paths) != len(wantPaths) {
				t.Fatal("wrong documentation ingress inventory")
			}
			for i, value := range paths {
				p := asMap(t, value)
				if stringAt(t, p, "path") != wantPaths[i] || stringAt(t, p, "pathType") != "Prefix" || stringAt(t, p, "backend", "service", "name") != stringAt(t, oneObject(t, objects, "Service", "ui"), "metadata", "name") || stringAt(t, p, "backend", "service", "port", "name") != "http" {
					t.Fatal("documentation path lost fixed UI service routing")
				}
			}
			var config map[string]any
			if err := json.Unmarshal([]byte(stringAt(t, oneObject(t, objects, "ConfigMap", "ui"), "data", "config.json")), &config); err != nil {
				t.Fatal(err)
			}
			if config["documentationUrl"] != override {
				t.Fatal("documentation override lost")
			}
		}
	}
}

func TestUIRejectsIncompleteAndUnsafeSettings(t *testing.T) {
	for name, settings := range map[string][]string{
		"no inputs":              {"--set", "ui.enabled=true"},
		"missing session Secret": {"--set", "ui.existingSecret="},
		"HTTP production":        {"--set", "ui.config.origin=http://browse.example.com"},
		"development production": {"--set", "ui.config.authMode=development"},
		"origin path":            {"--set", "ui.config.origin=https://browse.example.com/path"},
		"missing issuer":         {"--set", "ui.config.issuer="},
		"missing client":         {"--set", "ui.config.clientId="},
		"inline secret":          {"--set", "ui.config.clientSecret=unsafe"},
		"zero replicas":          {"--set", "ui.replicas=0"},
		"ingress mismatch":       {"--set", "ui.ingress.host=wrong.example.com"},
		"no ingress TLS":         {"--set", "ui.ingress.tlsSecretName="},
		"offline scope":          {"--set-json", `ui.config.scopes=["offline_access"]`},
		"reserved docs path":     {"--set", "ui.config.basePath=/docs"},
		"reserved docs subtree":  {"--set", "ui.config.basePath=/docs/manual"},
	} {
		t.Run(name, func(t *testing.T) {
			args := []string{"template", testRelease, chartPath(t)}
			if name != "no inputs" {
				args = append(args, "--values", filepath.Join(chartPath(t), "ci/ui-values.yaml"))
			}
			if _, err := helm(t, append(args, settings...)...); err == nil {
				t.Fatal("unsafe UI values rendered successfully")
			}
		})
	}
}

func TestUIDevelopmentAndDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	objects := renderChart(t, "--set", "ui.enabled=true,ui.existingSecret=ui-local,ui.config.environment=development,ui.config.authMode=development,ui.config.origin=http://127.0.0.1:14180,ui.image.digest="+digest)
	container := asMap(t, sliceAt(t, oneObject(t, objects, "Deployment", "ui"), "spec", "template", "spec", "containers")[0])
	if stringAt(t, container, "image") != "open-agentic-catalog-ui@"+digest {
		t.Fatal("UI digest did not take precedence")
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(stringAt(t, oneObject(t, objects, "ConfigMap", "ui"), "data", "config.json")), &config); err != nil {
		t.Fatal(err)
	}
	if config["developmentUsersFile"] != "/secrets/users.json" || config["clientSecretFile"] != nil {
		t.Fatal("local UI input selection is incorrect")
	}
}

// Backend identities must not alias the chart-owned UI account. In particular,
// migration hook replacement/cleanup must never delete a live UI identity.
func TestUIServiceAccountIsolation(t *testing.T) {
	for _, role := range []string{"api", "worker", "migration"} {
		for _, create := range []string{"true", "false"} {
			t.Run(role+"/create="+create, func(t *testing.T) {
				args := []string{"template", testRelease, chartPath(t), "--values", filepath.Join(chartPath(t), "ci/ui-values.yaml"),
					"--set-string", "fullnameOverride=catalog-custom",
					"--set-string", "serviceAccounts." + role + ".name=catalog-custom-ui",
					"--set", "serviceAccounts." + role + ".create=" + create}
				output, err := helm(t, args...)
				if err == nil || !strings.Contains(output, "UI ServiceAccount must be distinct") {
					t.Fatalf("UI/backend identity collision was not rejected: err=%v output=%s", err, output)
				}
				// A disabled UI owns no account and must not reserve its future name.
				if output, err := helm(t, append(args, "--set", "ui.enabled=false")...); err != nil {
					t.Fatalf("disabled UI reserved an account: %v: %s", err, output)
				}
			})
		}
	}
}

func TestUICustomServiceAccounts(t *testing.T) {
	for _, create := range []string{"true", "false"} {
		t.Run("create="+create, func(t *testing.T) {
			settings := make([]string, 0, 16)
			settings = append(settings, "--values", filepath.Join(chartPath(t), "ci/ui-values.yaml"),
				"--set-string", "fullnameOverride="+strings.Repeat("x", 63))
			for _, role := range []string{"api", "worker", "migration"} {
				settings = append(settings, "--set-string", "serviceAccounts."+role+".name=custom-"+role,
					"--set", "serviceAccounts."+role+".create="+create)
			}
			objects := renderChart(t, settings...)
			uiAccount := stringAt(t, oneObject(t, objects, "ServiceAccount", "ui"), "metadata", "name")
			if uiAccount != strings.Repeat("x", 60)+"-ui" {
				t.Fatalf("UI account did not preserve its suffix within the name limit: %q", uiAccount)
			}
			for _, role := range []string{"api", "worker", "migration", "ui"} {
				kind, want := "Deployment", "custom-"+role
				if role == "migration" {
					kind = "Job"
				} else if role == "ui" {
					want = uiAccount
				}
				workload := oneObject(t, objects, kind, role)
				if got := stringAt(t, workload, "spec", "template", "spec", "serviceAccountName"); got != want {
					t.Errorf("%s account = %q, want %q", role, got, want)
				}
			}
			wantAccounts := 1 // The UI always owns its account, even with external backend accounts.
			if create == "true" {
				wantAccounts = 4
			}
			if got := len(objectsByKind(objects, "ServiceAccount")); got != wantAccounts {
				t.Fatalf("rendered %d accounts, want %d", got, wantAccounts)
			}
			args := append([]string{"template", testRelease, chartPath(t)}, settings...)
			output, err := helm(t, append(args, "--set-string", "serviceAccounts.api.name="+uiAccount)...)
			if err == nil || !strings.Contains(output, "UI ServiceAccount must be distinct") {
				t.Fatalf("truncated UI account collision was not rejected: %v: %s", err, output)
			}
		})
	}
}

// Existing independent transport controls must reach the rendered processes;
// compact collections do not remove the need to configure large exact reads.
func TestCatalogPayloadConfigurationDelivery(t *testing.T) {
	objects := renderChart(t, "--values", filepath.Join(chartPath(t), "ci/ui-values.yaml"),
		"--values", filepath.Join(chartPath(t), "ci/payload-values.yaml"))
	validateRenderedConfiguration(t, objects)
	for setting, want := range map[string]string{
		"CATALOG_HTTP_MAX_BODY_BYTES":             "33554432",
		"CATALOG_VALIDATION_MAX_FINDINGS":         "256",
		"CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES": "131072",
	} {
		if stringAt(t, oneObject(t, objects, "ConfigMap", "configuration"), "data", setting) != want {
			t.Fatalf("backend payload setting %s lost", setting)
		}
	}
	var config struct {
		RequestBytes  int64 `json:"requestBytes"`
		ResponseBytes int64 `json:"responseBytes"`
	}
	if err := json.Unmarshal([]byte(stringAt(t, oneObject(t, objects, "ConfigMap", "ui"), "data", "config.json")), &config); err != nil {
		t.Fatal(err)
	}
	if config.RequestBytes != 32<<20 || config.ResponseBytes != 64<<20 {
		t.Fatal("UI transport limits lost")
	}
}

func TestUIIngressAnnotationLifecycle(t *testing.T) {
	settings := []string{"--values", filepath.Join(chartPath(t), "ci/ui-values.yaml"),
		"--set", "ui.ingress.enabled=true", "--set-string", "ui.ingress.annotations.kubernetes\\.io/ingress\\.class=nginx"}
	objects := renderChart(t, settings...)
	if got := stringAt(t, oneObject(t, objects, "Ingress", "ui"), "metadata", "annotations", "kubernetes.io/ingress.class"); got != "nginx" {
		t.Fatalf("controller annotation = %q", got)
	}
	for key, value := range map[string]string{"hook": "pre-install", "hook-weight": "-1", "hook-delete-policy": "hook-succeeded", "resource-policy": "keep"} {
		t.Run(key, func(t *testing.T) {
			args := append([]string{"template", testRelease, chartPath(t)}, settings...)
			args = append(args, "--set-string", "ui.ingress.annotations.helm\\.sh/"+key+"="+value)
			output, err := helm(t, args...)
			// Helm validators format schema paths differently. Require the exact
			// rejected key instead of depending on a validator's reported path.
			rejectedKey := false
			for _, word := range strings.Fields(output) {
				if strings.Trim(word, "'\":,()") == "helm.sh/"+key {
					rejectedKey = true
				}
			}
			if err == nil || !strings.Contains(output, "specifications of the schema(s)") || !rejectedKey {
				t.Fatalf("reserved Ingress annotation admitted: %v: %s", err, output)
			}
		})
	}
}
