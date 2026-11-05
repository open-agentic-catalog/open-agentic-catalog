//go:build helm

package helm_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// These supplied contents qualify only the fixture, never a real external ConfigMap.
const testExternalPolicy = `{"schemaVersion":1,"policyId":"external-fixture","revision":1,"public":{"mode":"allowlist","origins":[]}}`

type renderedConfigFixtures struct {
	externalPolicies map[string]map[string]string
	files            map[string]string // Exact container paths mapped to local test files.
}

func validateRenderedConfigurationWithFixtures(t *testing.T, objects []object, fixtures renderedConfigFixtures) {
	t.Helper()
	for _, role := range []config.Role{config.RoleAPI, config.RoleWorker, config.RoleMigration} {
		if _, err := loadRenderedConfiguration(t, objects, role, fixtures); err != nil {
			t.Fatalf("rendered %s configuration: %v", role, err)
		}
	}
}

func renderedConfigMap(t *testing.T, objects []object, name string) map[string]string {
	t.Helper()
	for _, resource := range objectsByKind(objects, "ConfigMap") {
		if stringAt(t, resource, "metadata", "name") == name {
			data := make(map[string]string)
			for key, value := range mapAt(t, resource, "data") {
				data[key] = stringValue(t, value)
			}
			return data
		}
	}
	return nil
}

func loadRenderedConfiguration(t *testing.T, objects []object, role config.Role, fixtures renderedConfigFixtures) (config.Config, error) {
	t.Helper()
	kind := "Deployment"
	if role == config.RoleMigration {
		kind = "Job"
	}
	pod := mapAt(t, oneObject(t, objects, kind, string(role)), "spec", "template", "spec")
	container := asMap(t, sliceAt(t, pod, "containers")[0])
	sources := sliceAt(t, container, "envFrom")
	if len(sources) != 1 {
		return config.Config{}, fmt.Errorf("expected one role ConfigMap, got %d", len(sources))
	}
	values := renderedConfigMap(t, objects, stringAt(t, sources[0], "configMapRef", "name"))
	if values == nil {
		return config.Config{}, fmt.Errorf("role ConfigMap is missing")
	}
	for _, raw := range sliceAt(t, container, "env") {
		entry := asMap(t, raw)
		name := stringAt(t, entry, "name")
		if value, present := entry["value"]; present {
			values[name] = stringValue(t, value)
			continue
		}
		// Actual Secret contents are unknown. Only these explicit synthetic values
		// supply runtime admission; separate tests assert the exact Secret references.
		ref := mapAt(t, entry, "valueFrom", "secretKeyRef")
		if stringAt(t, ref, "name") == "" || stringAt(t, ref, "key") == "" {
			return config.Config{}, fmt.Errorf("Secret reference for %s is incomplete", name)
		}
		switch name {
		case "CATALOG_DATABASE_DSN":
			values[name] = "postgres://catalog:catalog@127.0.0.1:5432/catalog?sslmode=disable"
		case "CATALOG_CURSOR_SIGNING_KEY", "CATALOG_WEBHOOK_ENCRYPTION_KEY":
			values[name] = base64.StdEncoding.EncodeToString(make([]byte, 32))
		default:
			return config.Config{}, fmt.Errorf("no explicit Secret fixture for %s", name)
		}
	}
	if role != config.RoleMigration {
		volume := namedMap(t, sliceAt(t, pod, "volumes"), "federation-policy")
		mount := namedMap(t, sliceAt(t, container, "volumeMounts"), "federation-policy")
		items := sliceAt(t, volume, "configMap", "items")
		if len(items) != 1 || values["CATALOG_FEDERATION_POLICY_FILE"] != stringAt(t, mount, "mountPath")+"/"+stringAt(t, items[0], "path") {
			return config.Config{}, fmt.Errorf("federation policy path does not match its projection")
		}
		name := stringAt(t, volume, "configMap", "name")
		data := renderedConfigMap(t, objects, name)
		if data == nil {
			data = fixtures.externalPolicies[name]
		}
		policy, found := data[stringAt(t, items[0], "key")]
		if !found {
			return config.Config{}, fmt.Errorf("no explicit policy fixture for ConfigMap %s", name)
		}
		policyPath := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(policyPath, []byte(replaceFixturePaths(policy, fixtures.files)), 0o600); err != nil {
			t.Fatal(err)
		}
		values["CATALOG_FEDERATION_POLICY_FILE"] = policyPath
	}
	for name, value := range values {
		if replacement, found := fixtures.files[value]; found {
			values[name] = replacement
		} else {
			values[name] = replaceFixturePaths(value, fixtures.files)
		}
	}
	var loaded config.Config
	var err error
	withEnvironment(t, values, func() {
		loaded, err = config.LoadFor("", role)
		if err == nil {
			err = config.ValidateRuntimeSecrets(loaded, role)
		}
	})
	return loaded, err
}

func replaceFixturePaths(value string, paths map[string]string) string {
	for container, local := range paths {
		// Replace complete JSON string values, never partial paths or URL substrings.
		value = strings.ReplaceAll(value, strconv.Quote(container), strconv.Quote(local))
	}
	return value
}

func TestRenderedConfigurationUsesManagedPolicyAndExplicitEnvironment(t *testing.T) {
	const policy = `{"schemaVersion":1,"policyId":"actual-managed-policy","revision":7,"public":{"mode":"allowlist","origins":["https://catalog.example"]}}`
	objects := renderChart(t, "--set-string", "federation.policy.key=selected.json", "--set-literal", "federation.policy.document="+policy)
	loaded, err := loadRenderedConfiguration(t, objects, config.RoleAPI, renderedConfigFixtures{})
	if err != nil || loaded.Federation.Policy.PolicyID != "actual-managed-policy" || loaded.Federation.Policy.Revision != 7 || !reflect.DeepEqual(loaded.Federation.Policy.Public.Origins, []string{"https://catalog.example:443"}) {
		t.Fatalf("managed policy was substituted: %+v, %v", loaded.Federation.Policy, err)
	}
	api := oneObject(t, objects, "Deployment", "api")
	container := asMap(t, sliceAt(t, api, "spec", "template", "spec", "containers")[0])
	container["env"] = append(sliceAt(t, container, "env"), map[string]any{"name": "CATALOG_AUTH_MODE", "value": "invalid"})
	if _, err := loadRenderedConfiguration(t, objects, config.RoleAPI, renderedConfigFixtures{}); err == nil || !strings.Contains(err.Error(), "auth.mode") {
		t.Fatalf("explicit environment override was ignored: %v", err)
	}
	objects = renderChart(t, "--set-literal", "federation.policy.document=not-json")
	if _, err := loadRenderedConfiguration(t, objects, config.RoleAPI, renderedConfigFixtures{}); err == nil || !strings.Contains(err.Error(), "federation policy") {
		t.Fatalf("invalid managed policy was substituted: %v", err)
	}
}

func TestExternalPolicyNeedsAnExplicitNamedFixture(t *testing.T) {
	objects := renderChart(t, "--set-string", "federation.policy.existingConfigMap=external-policy")
	if _, err := loadRenderedConfiguration(t, objects, config.RoleAPI, renderedConfigFixtures{}); err == nil || !strings.Contains(err.Error(), "no explicit policy fixture") {
		t.Fatalf("missing external policy was silently supplied: %v", err)
	}
	fixtures := renderedConfigFixtures{externalPolicies: map[string]map[string]string{"external-policy": {"policy.json": testExternalPolicy}}}
	loaded, err := loadRenderedConfiguration(t, objects, config.RoleAPI, fixtures)
	if err != nil || loaded.Federation.Policy.PolicyID != "external-fixture" {
		t.Fatalf("named policy fixture not loaded: %+v, %v", loaded.Federation.Policy, err)
	}
}

func TestEnabledFeatureConfigurationsLoadFromRenderedValues(t *testing.T) {
	const allowed = `{"schemaVersion":1,"policyId":"feature-fixture","revision":1,"public":{"mode":"allowlist","origins":["https://log.example","https://media.example"]}}`
	ra, err := filepath.Abs("../../internal/standards/anstrust/testdata/native/ra.pem")
	if err != nil {
		t.Fatal(err)
	}
	log, err := filepath.Abs("../../internal/standards/anstrust/testdata/native/tl.pem")
	if err != nil {
		t.Fatal(err)
	}
	fixtures := renderedConfigFixtures{files: map[string]string{"/etc/open-agentic-catalog/trust-verification/ra.pem": ra, "/etc/open-agentic-catalog/trust-verification/log.pem": log}}
	for _, test := range []struct {
		name string
		args []string
	}{
		{"service activity", []string{"--set-string", "configEnv.CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED=true"}},
		{"semantic", []string{"--values", chartPath(t, "ci", "semantic-values.yaml")}},
		{"tracing", []string{"--set-literal", `configEnv.CATALOG_TRACING={"enabled":true,"endpoint":"https://collector.example/v1/traces","installation":"test"}`}},
		{"artwork", []string{"--set-literal", `configEnv.CATALOG_LISTING_MEDIA={"allowedOrigins":[{"origin":"https://media.example"}]}`}},
		{"ANS", []string{"--set-string", "trustVerification.publicKeysConfigMap=verification-keys", "--set-literal", `configEnv.CATALOG_TRUST_VERIFICATION={"enabled":true,"policies":[{"name":"owned-ans","profile":"ans-registration-v1","registrationAuthority":"ans-ra-local","logOrigin":"owned-ans","evidenceOrigin":"https://log.example","publisherDomains":["trust-v1.ans.test"],"registrationKeys":[{"id":"ans-ra-signer","file":"/etc/open-agentic-catalog/trust-verification/ra.pem"}],"logKeyFiles":["/etc/open-agentic-catalog/trust-verification/log.pem"]}]}`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--set-literal", "federation.policy.document=" + allowed}, test.args...)
			objects := renderChart(t, args...)
			validateRenderedConfigurationWithFixtures(t, objects, fixtures)
		})
	}
}

func TestPublisherDNSPolicyDeliveryAndIsolation(t *testing.T) {
	baseline := renderChart(t, "--values", chartPath(t, "ci", "ui-values.yaml"))
	for _, mode := range []string{"disabled", "optional", "required_for_approval"} {
		value, err := json.Marshal(map[string]string{"mode": mode, "challengeTTL": "12h", "proofMaxAge": "48h", "lookupTimeout": "2s"})
		if err != nil {
			t.Fatal(err)
		}
		objects := renderChart(t, "--values", chartPath(t, "ci", "ui-values.yaml"), "--is-upgrade", "--set-literal", "configEnv.CATALOG_PUBLISHER_DOMAIN_VERIFICATION="+string(value))
		if got := stringAt(t, oneObject(t, objects, "ConfigMap", "configuration"), "data", "CATALOG_PUBLISHER_DOMAIN_VERIFICATION"); got != string(value) {
			t.Fatal("Publisher DNS JSON changed")
		}
		for _, role := range []config.Role{config.RoleAPI, config.RoleWorker} {
			loaded, err := loadRenderedConfiguration(t, objects, role, renderedConfigFixtures{})
			if err != nil || loaded.PublisherDomainVerification.Mode != mode || loaded.PublisherDomainVerification.LookupTimeout.String() != "2s" {
				t.Fatalf("Publisher DNS policy not loaded: %+v, %v", loaded.PublisherDomainVerification, err)
			}
			before := mapAt(t, oneObject(t, baseline, "Deployment", string(role)), "spec", "template", "metadata", "annotations")
			after := mapAt(t, oneObject(t, objects, "Deployment", string(role)), "spec", "template", "metadata", "annotations")
			if reflect.DeepEqual(before, after) {
				t.Fatal("Publisher DNS policy change did not roll runtime", role)
			}
		}
		for _, kind := range []string{"Job", "ConfigMap"} {
			if !reflect.DeepEqual(oneObject(t, baseline, kind, "migration"), oneObject(t, objects, kind, "migration")) {
				t.Fatal("Publisher DNS changed migration", kind)
			}
		}
		for _, kind := range []string{"Deployment", "ConfigMap"} {
			if !reflect.DeepEqual(oneObject(t, baseline, kind, "ui"), oneObject(t, objects, kind, "ui")) {
				t.Fatal("Publisher DNS changed UI", kind)
			}
		}
	}
	objects := renderChart(t, "--set-literal", `configEnv.CATALOG_PUBLISHER_DOMAIN_VERIFICATION={"mode":"required"}`)
	if _, err := loadRenderedConfiguration(t, objects, config.RoleAPI, renderedConfigFixtures{}); err == nil {
		t.Fatal("invalid DNS mode was accepted")
	}
}
