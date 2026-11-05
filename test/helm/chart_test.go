//go:build helm

package helm_test

import (
	"errors"
	"go.yaml.in/yaml/v3"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

const (
	testNamespace = "open-agentic-catalog"
	testRelease   = "open-agentic-catalog"
)

type object map[string]any

func TestConfiguredPoliciesUseSharedConfigMapAndRollout(t *testing.T) {
	defaults := renderChart(t)
	for _, policy := range []map[string]string{
		{"CATALOG_VALIDATION_MAX_FINDINGS": "32", "CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES": "4096"},
		{"CATALOG_JOBS_MAX_ATTEMPTS": "7", "CATALOG_JOBS_RETRY_CEILING": "5.5s"},
		{"CATALOG_WORKER_DEPENDENCY_RETRY_INITIAL_DELAY": "1.25s", "CATALOG_WORKER_DEPENDENCY_RETRY_CEILING": "2.5s"},
		{"CATALOG_FEDERATION_MAX_LIVE_PAGES_PER_SOURCE": "3"},
	} {
		args := make([]string, 0, 2*len(policy))
		for name, value := range policy {
			args = append(args, "--set-string", "configEnv."+name+"="+value)
		}
		changed := renderChart(t, args...)
		data := mapAt(t, oneObject(t, changed, "ConfigMap", "configuration"), "data")
		for name, value := range policy {
			if data[name] != value {
				t.Fatalf("rendered retry policy %s: %v", name, data[name])
			}
		}
		validateRenderedConfiguration(t, changed)
		for _, component := range []string{"api", "worker"} {
			before := mapAt(t, oneObject(t, defaults, "Deployment", component), "spec", "template", "metadata", "annotations")
			after := mapAt(t, oneObject(t, changed, "Deployment", component), "spec", "template", "metadata", "annotations")
			if reflect.DeepEqual(before, after) {
				t.Fatalf("%s policy change did not change rollout checksum", component)
			}
		}
	}
}

func TestDefaultRenderContract(t *testing.T) {
	objects := renderChart(t)
	if len(objects) != 12 {
		t.Fatalf("default render contains %d objects, want 12", len(objects))
	}

	wantInventory := map[string]int{
		"ConfigMap/configuration":     1,
		"ConfigMap/federation-policy": 1,
		"ConfigMap/migration":         1,
		"Deployment/api":              1,
		"Deployment/worker":           1,
		"Job/migration":               1,
		"PodDisruptionBudget/api":     1,
		"PodDisruptionBudget/worker":  1,
		"Service/api":                 1,
		"ServiceAccount/api":          1,
		"ServiceAccount/migration":    1,
		"ServiceAccount/worker":       1,
	}
	gotInventory := make(map[string]int, len(wantInventory))
	for _, resource := range objects {
		name := stringAt(t, resource, "metadata", "name")
		if len(name) == 0 || len(name) > 63 {
			t.Errorf("%s has invalid generated name %q", stringAt(t, resource, "kind"), name)
		}
		labels := mapAt(t, resource, "metadata", "labels")
		for key, want := range map[string]string{
			"app.kubernetes.io/instance":   testRelease,
			"app.kubernetes.io/managed-by": "Helm",
			"app.kubernetes.io/name":       "open-agentic-catalog",
			"app.kubernetes.io/part-of":    "open-agentic-catalog",
			"app.kubernetes.io/version":    "0.1.0",
		} {
			if got := stringValue(t, labels[key]); got != want {
				t.Errorf("%s %s label %s = %q, want %q", stringAt(t, resource, "kind"), name, key, got, want)
			}
		}
		component := stringValue(t, labels["app.kubernetes.io/component"])
		gotInventory[stringAt(t, resource, "kind")+"/"+component]++
	}
	if !reflect.DeepEqual(gotInventory, wantInventory) {
		t.Fatalf("default resource inventory = %v, want %v", gotInventory, wantInventory)
	}

	api := oneObject(t, objects, "Deployment", "api")
	worker := oneObject(t, objects, "Deployment", "worker")
	assertRuntimeDeployment(t, api, "api", "open-agentic-catalog-open-agentic-catalog-api", "catalog-api", "http", "/livez")
	assertRuntimeDeployment(t, worker, "worker", "open-agentic-catalog-open-agentic-catalog-worker", "catalog-worker", "operations", "/startupz")
	for _, deployment := range []object{api, worker} {
		if _, found := mapAt(t, deployment, "spec", "template", "spec")["topologySpreadConstraints"]; found {
			t.Errorf("default %s workload unexpectedly imposes topology spread", stringAt(t, deployment, "metadata", "name"))
		}
	}

	service := oneObject(t, objects, "Service", "api")
	assertSelectorMatches(t, service, api)
	assertSelectorMatches(t, oneObject(t, objects, "PodDisruptionBudget", "api"), api)
	assertSelectorMatches(t, oneObject(t, objects, "PodDisruptionBudget", "worker"), worker)
	ports := sliceAt(t, service, "spec", "ports")
	if len(ports) != 1 || stringAt(t, asMap(t, ports[0]), "name") != "http" || stringAt(t, asMap(t, ports[0]), "targetPort") != "http" {
		t.Fatalf("product Service ports = %#v, want only named HTTP", ports)
	}

	for _, role := range []string{"api", "worker"} {
		account := oneObject(t, objects, "ServiceAccount", role)
		if boolAt(t, account, "automountServiceAccountToken") {
			t.Errorf("%s ServiceAccount automounts a token", role)
		}
	}
	migrationAccount := oneObject(t, objects, "ServiceAccount", "migration")
	assertHook(t, migrationAccount, "-20")
	if boolAt(t, migrationAccount, "automountServiceAccountToken") {
		t.Error("migration ServiceAccount automounts a token")
	}

	migrationConfig := oneObject(t, objects, "ConfigMap", "migration")
	assertHook(t, migrationConfig, "-10")
	migration := oneObject(t, objects, "Job", "migration")
	assertHook(t, migration, "-5")
	if got := stringAt(t, migration, "metadata", "name"); got != "open-agentic-catalog-open-agentic-catalog-migrate" {
		t.Errorf("migration Job name = %q", got)
	}
	if got := stringAt(t, migration, "spec", "template", "spec", "serviceAccountName"); got != "open-agentic-catalog-open-agentic-catalog-migration" {
		t.Errorf("migration ServiceAccount = %q", got)
	}
	assertPodSecurity(t, mapAt(t, migration, "spec", "template", "spec"), "migrate")
	if got := stringAt(t, migration, "spec", "template", "spec", "restartPolicy"); got != "Never" {
		t.Errorf("migration restartPolicy = %q", got)
	}
	migrationContainer := namedMap(t, sliceAt(t, migration, "spec", "template", "spec", "containers"), "migrate")
	if got := stringSlice(t, migrationContainer["args"]); !slices.Equal(got, []string{"--api-role", "catalog_api", "--worker-role", "catalog_worker"}) {
		t.Errorf("migration role arguments = %v", got)
	}
	assertDatabaseSecretEnvironment(t, migrationContainer, "open-agentic-catalog-migration-database", "dsn")
	for _, forbidden := range []string{"CATALOG_CURSOR_SIGNING_KEY", "CATALOG_WEBHOOK_ENCRYPTION_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		if _, found := environmentEntry(t, migrationContainer, forbidden); found {
			t.Errorf("migration container received %s", forbidden)
		}
	}

	assertDatabaseSecretEnvironment(t, namedMap(t, sliceAt(t, api, "spec", "template", "spec", "containers"), "catalog-api"), "open-agentic-catalog-api-database", "dsn")
	assertDatabaseSecretEnvironment(t, namedMap(t, sliceAt(t, worker, "spec", "template", "spec", "containers"), "catalog-worker"), "open-agentic-catalog-worker-database", "dsn")

	configuration := oneObject(t, objects, "ConfigMap", "configuration")
	data := mapAt(t, configuration, "data")
	if _, found := data["CATALOG_HTTP_CORS_ORIGINS"]; found {
		t.Error("default chart unexpectedly enables a placeholder CORS origin")
	}
	for _, secret := range []string{"CATALOG_DATABASE_DSN", "CATALOG_CURSOR_SIGNING_KEY", "CATALOG_WEBHOOK_ENCRYPTION_KEY"} {
		if _, found := data[secret]; found {
			t.Errorf("configuration ConfigMap contains secret setting %s", secret)
		}
	}
	validateRenderedConfiguration(t, objects)
}

func TestSharedAndOptionalProfiles(t *testing.T) {
	t.Run("shared tenancy and external identities", func(t *testing.T) {
		objects := renderChart(t, "--values", chartPath(t, "ci", "shared-values.yaml"))
		for _, resource := range objects {
			if name := stringAt(t, resource, "metadata", "name"); len(name) > 63 {
				t.Errorf("generated name exceeds 63 bytes: %q", name)
			}
		}
		if got := objectsByKind(objects, "ServiceAccount"); len(got) != 0 {
			t.Fatalf("external-identity profile rendered ServiceAccounts: %v", objectNames(t, got))
		}
		for role, want := range map[string]string{"api": "catalog-api", "worker": "catalog-worker"} {
			deployment := oneObject(t, objects, "Deployment", role)
			if got := stringAt(t, deployment, "spec", "template", "spec", "serviceAccountName"); got != want {
				t.Errorf("%s ServiceAccount = %q, want %q", role, got, want)
			}
		}
		migration := oneObject(t, objects, "Job", "migration")
		if got := stringAt(t, migration, "spec", "template", "spec", "serviceAccountName"); got != "catalog-migration" {
			t.Errorf("migration ServiceAccount = %q", got)
		}
		api := oneObject(t, objects, "Deployment", "api")
		container := namedMap(t, sliceAt(t, api, "spec", "template", "spec", "containers"), "catalog-api")
		if got := stringAt(t, container, "image"); !strings.Contains(got, "@sha256:") || strings.Contains(got, ":0.1.0") {
			t.Errorf("digest-selected image = %q", got)
		}
		data := mapAt(t, oneObject(t, objects, "ConfigMap", "configuration"), "data")
		if got := stringValue(t, data["CATALOG_AUTH_ORGANIZATION_CLAIM"]); got != "organization_id" {
			t.Errorf("shared organization claim = %q", got)
		}
		if _, found := data["CATALOG_AUTH_DEDICATED_TENANT_ID"]; found {
			t.Error("shared profile retained the dedicated tenant")
		}
		validateRenderedConfiguration(t, objects)
	})

	t.Run("optional inputs stay role scoped", func(t *testing.T) {
		objects := renderChart(t, "--values", chartPath(t, "ci", "optional-values.yaml"))
		if got := objectsByKindAndComponent(objects, "ConfigMap", "federation-policy"); len(got) != 0 {
			t.Error("external federation policy profile rendered a managed policy ConfigMap")
		}
		api := namedMap(t, sliceAt(t, oneObject(t, objects, "Deployment", "api"), "spec", "template", "spec", "containers"), "catalog-api")
		worker := namedMap(t, sliceAt(t, oneObject(t, objects, "Deployment", "worker"), "spec", "template", "spec", "containers"), "catalog-worker")
		for _, name := range []string{"CATALOG_WEBHOOK_ENCRYPTION_KEY"} {
			if _, found := environmentEntry(t, api, name); !found {
				t.Errorf("API optional profile omitted %s", name)
			}
		}
		if _, found := environmentEntry(t, worker, "CATALOG_WEBHOOK_ENCRYPTION_KEY"); !found {
			t.Error("worker optional profile omitted webhook encryption key")
		}
		for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
			if _, found := environmentEntry(t, worker, name); found {
				t.Errorf("worker received obsolete object credential %s", name)
			}
			if _, found := environmentEntry(t, api, name); found {
				t.Errorf("API received obsolete object credential %s", name)
			}
		}
		validateRenderedConfigurationWithFixtures(t, objects, renderedConfigFixtures{
			externalPolicies: map[string]map[string]string{"catalog-federation-policy": {"policy.json": testExternalPolicy}},
		})
	})

	t.Run("YAML-looking strings remain strings", func(t *testing.T) {
		objects := renderChartRelease(t, "true", "--values", chartPath(t, "ci", "ambiguous-values.yaml"))
		api := oneObject(t, objects, "Deployment", "api")
		labels := mapAt(t, api, "metadata", "labels")
		if stringValue(t, labels["app.kubernetes.io/name"]) != "false" || stringValue(t, labels["app.kubernetes.io/instance"]) != "true" {
			t.Errorf("ambiguous labels were not preserved as strings: %#v", labels)
		}
		pod := mapAt(t, api, "spec", "template", "spec")
		if got := stringValue(t, pod["serviceAccountName"]); got != "true" {
			t.Errorf("ambiguous ServiceAccount name = %q", got)
		}
		container := namedMap(t, asSlice(t, pod["containers"]), "catalog-api")
		if got := stringValue(t, container["image"]); got != "example.com/catalog/true:null" {
			t.Errorf("ambiguous image values rendered as %q", got)
		}
		assertDatabaseSecretEnvironment(t, container, "true", "false")
		validateRenderedConfigurationWithFixtures(t, objects, renderedConfigFixtures{
			externalPolicies: map[string]map[string]string{"true": {"null": testExternalPolicy}},
		})
	})

	t.Run("ordinary resource annotations remain configurable", func(t *testing.T) {
		objects := renderChart(t,
			"--set-string", `service.annotations.example\.com/owner=platform`,
			"--set-string", `serviceAccounts.api.annotations.example\.com/owner=platform`,
			"--set-string", `podAnnotations.example\.com/rollout=revision-2`,
		)
		for _, resource := range []object{
			oneObject(t, objects, "Service", "api"),
			oneObject(t, objects, "ServiceAccount", "api"),
		} {
			if got := stringAt(t, resource, "metadata", "annotations", "example.com/owner"); got != "platform" {
				t.Errorf("%s annotation = %q", stringAt(t, resource, "kind"), got)
			}
		}
		for _, role := range []string{"api", "worker"} {
			deployment := oneObject(t, objects, "Deployment", role)
			if got := stringAt(t, deployment, "spec", "template", "metadata", "annotations", "example.com/rollout"); got != "revision-2" {
				t.Errorf("%s rollout marker = %q", role, got)
			}
			for _, key := range []string{"checksum/config", "checksum/federation-policy", "checksum/webhook-egress"} {
				if got := stringAt(t, deployment, "spec", "template", "metadata", "annotations", key); len(got) != 64 {
					t.Errorf("%s chart-owned %s = %q", role, key, got)
				}
			}
		}
	})

	t.Run("role-specific topology spread remains opt in", func(t *testing.T) {
		objects := renderChart(t,
			"--set", "api.topologySpread.enabled=true",
			"--set", "worker.topologySpread.enabled=true",
			"--set-string", "worker.topologySpread.whenUnsatisfiable=ScheduleAnyway",
		)
		for role, wantPolicy := range map[string]string{"api": "DoNotSchedule", "worker": "ScheduleAnyway"} {
			pod := mapAt(t, oneObject(t, objects, "Deployment", role), "spec", "template", "spec")
			constraints := asSlice(t, pod["topologySpreadConstraints"])
			if len(constraints) != 1 {
				t.Fatalf("%s topology constraints = %#v", role, constraints)
			}
			constraint := asMap(t, constraints[0])
			if got := intValue(t, constraint["maxSkew"]); got != 1 {
				t.Errorf("%s topology maxSkew = %d", role, got)
			}
			if got := stringValue(t, constraint["topologyKey"]); got != "kubernetes.io/hostname" {
				t.Errorf("%s topology key = %q", role, got)
			}
			if got := stringValue(t, constraint["whenUnsatisfiable"]); got != wantPolicy {
				t.Errorf("%s topology policy = %q, want %q", role, got, wantPolicy)
			}
			if got := stringSlice(t, constraint["matchLabelKeys"]); !slices.Equal(got, []string{"pod-template-hash"}) {
				t.Errorf("%s topology matchLabelKeys = %v", role, got)
			}
			selector := mapAt(t, constraint, "labelSelector", "matchLabels")
			if got := stringValue(t, selector["app.kubernetes.io/component"]); got != role {
				t.Errorf("%s topology selector component = %q", role, got)
			}
		}
	})

	t.Run("upgrade render retains migration ordering", func(t *testing.T) {
		objects := renderChart(t, "--is-upgrade", "--values", chartPath(t, "ci", "optional-values.yaml"))
		assertHook(t, oneObject(t, objects, "ServiceAccount", "migration"), "-20")
		assertHook(t, oneObject(t, objects, "ConfigMap", "migration"), "-10")
		migration := oneObject(t, objects, "Job", "migration")
		assertHook(t, migration, "-5")
		if got := stringAt(t, migration, "metadata", "name"); got != "open-agentic-catalog-open-agentic-catalog-migrate" {
			t.Errorf("upgrade migration Job name = %q", got)
		}
	})
}

func TestUnsafeValuesAreRejected(t *testing.T) {
	longRole := strings.Repeat("é", 32)
	longReference := strings.Repeat("a", 64)
	tests := map[string][]string{
		"malformed tracing JSON":           {"--set-literal", "configEnv.CATALOG_TRACING={invalid}"},
		"tracing array":                    {"--set-literal", "configEnv.CATALOG_TRACING=[]"},
		"invalid tracing Secret":           {"--set-string", "secrets.tracingInputs.name=Invalid_Name"},
		"invalid OIDC trust reference":     {"--set-string", "auth.trustBundleConfigMap=Invalid_Name"},
		"OIDC trust path bypass":           {"--set-string", "configEnv.CATALOG_AUTH_TRUST_BUNDLE_FILE=/arbitrary/ca.pem"},
		"duplicate database reference":     {"--set-string", "secrets.workerDatabase.name=open-agentic-catalog-api-database"},
		"duplicate ServiceAccount":         {"--set-string", "serviceAccounts.worker.name=open-agentic-catalog-open-agentic-catalog-api"},
		"enabled credentials no Secret":    {"--set-string", "configEnv.CATALOG_CREDENTIAL_RESOLUTION_ENABLED=true", "--set-string", "configEnv.CATALOG_CREDENTIAL_RESOLUTION_DIRECTORY=/etc/open-agentic-catalog/federation/credentials"},
		"enabled webhooks no Secret":       {"--set-string", "configEnv.CATALOG_WEBHOOKS_ENABLED=true"},
		"invalid database role bytes":      {"--set-string", "databaseRoles.api=" + longRole},
		"invalid image digest":             {"--set-string", "image.digest=sha256:abcd"},
		"invalid migration deadline":       {"--set", "migration.activeDeadlineSeconds=0"},
		"invalid reference":                {"--set-string", "secrets.apiDatabase.name=" + longReference},
		"invalid service type":             {"--set-string", "service.type=ExternalName"},
		"invalid tenant":                   {"--set-string", "configEnv.CATALOG_AUTH_DEDICATED_TENANT_ID=invalid"},
		"invalid termination grace":        {"--set", "terminationGracePeriodSeconds=0"},
		"invalid topology key":             {"--set-string", "api.topologySpread.topologyKey=/invalid"},
		"invalid topology policy":          {"--set-string", "worker.topologySpread.whenUnsatisfiable=Ignore"},
		"missing external identity":        {"--set", "serviceAccounts.api.create=false"},
		"obsolete object settings":         {"--set-string", "configEnv.CATALOG_OBJECT_DRIVER=s3"},
		"reserved pod annotation":          {"--set-string", "podAnnotations.checksum/config=override"},
		"reserved federation checksum":     {"--set-string", "podAnnotations.checksum/federation-policy=override"},
		"reserved webhook checksum":        {"--set-string", "podAnnotations.checksum/webhook-egress=override"},
		"reserved database trust checksum": {"--set-string", "podAnnotations.checksum/database-trust-reference=override"},
		"numeric Service name override":    {"--set-string", "fullnameOverride=123-catalog"},
		"reserved resource retention":      {"--set-string", "service.annotations.helm\\.sh/resource-policy=keep"},
		"reserved Service hook":            {"--set-string", "service.annotations.helm\\.sh/hook=pre-install"},
		"reserved ServiceAccount hook":     {"--set-string", "serviceAccounts.api.annotations.helm\\.sh/hook=pre-install"},
		"same database role":               {"--set-string", "databaseRoles.api=catalog_runtime", "--set-string", "databaseRoles.worker=catalog_runtime"},
		"zero finding limit":               {"--set-string", "configEnv.CATALOG_VALIDATION_MAX_FINDINGS=0"},
		"excessive finding limit":          {"--set-string", "configEnv.CATALOG_VALIDATION_MAX_FINDINGS=513"},
		"undersized diagnostic budget":     {"--set-string", "configEnv.CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES=1023"},
		"excessive diagnostic budget":      {"--set-string", "configEnv.CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES=262145"},
		"zero Job limit":                   {"--set-string", "configEnv.CATALOG_JOBS_MAX_ATTEMPTS=0"},
		"excessive Job limit":              {"--set-string", "configEnv.CATALOG_JOBS_MAX_ATTEMPTS=21"},
		"zero live page limit":             {"--set-string", "configEnv.CATALOG_FEDERATION_MAX_LIVE_PAGES_PER_SOURCE=0"},
		"excessive live page limit":        {"--set-string", "configEnv.CATALOG_FEDERATION_MAX_LIVE_PAGES_PER_SOURCE=6"},
		"enabled development fixture":      {"--set-string", `configEnv.CATALOG_DEMO_ARCHIVES={"origin":"https://127.0.0.1:18443"}`},
		"unknown configuration":            {"--set-string", "configEnv.CATALOG_UNKNOWN=value"},
		"unknown top-level value":          {"--set-string", "unknown=value"},
	}
	for name, arguments := range tests {
		t.Run(name, func(t *testing.T) {
			if output, err := helm(t, append([]string{"template", testRelease, chartPath(t), "--namespace", testNamespace, "--kube-version", kubeVersion()}, arguments...)...); err == nil {
				t.Fatalf("unsafe values rendered successfully:\n%s", output)
			}
		})
	}
}

func TestDatabaseTrustReferenceRollout(t *testing.T) {
	settings := make([]string, 0, 6)
	settings = append(settings, "--set-string", "databaseTrustBundleConfigMap=database-ca", "--set-string", "podAnnotations.example\\.com/rollout=revision-2")
	before := renderChart(t, settings...)
	after := renderChart(t, append(settings, "--set-string", "databaseTrustBundleConfigMap=replacement-ca")...)
	for _, role := range []string{"api", "worker"} {
		old := mapAt(t, oneObject(t, before, "Deployment", role), "spec", "template", "metadata", "annotations")
		current := mapAt(t, oneObject(t, after, "Deployment", role), "spec", "template", "metadata", "annotations")
		key := "checksum/database-trust-reference"
		if len(stringValue(t, old[key])) != 64 || old[key] == current[key] || current["example.com/rollout"] != "revision-2" {
			t.Fatalf("%s database trust rollout annotations: %v -> %v", role, old, current)
		}
	}
}

func TestServiceNamePortability(t *testing.T) {
	for _, release := range []string{"123", "1-catalog"} {
		t.Run(release, func(t *testing.T) {
			output, err := helm(t, "template", release, chartPath(t), "--namespace", testNamespace, "--kube-version", kubeVersion())
			if err == nil || !strings.Contains(output, "fullnameOverride") {
				t.Fatalf("numeric-leading Service name must fail with an override hint: err=%v output=%s", err, output)
			}
			objects := renderChartRelease(t, release, "--set-string", "fullnameOverride=catalog-123")
			service := oneObject(t, objects, "Service", "api")
			if got := stringAt(t, service, "metadata", "name"); got != "catalog-123" {
				t.Fatalf("explicit Service name = %q", got)
			}
			if got := stringAt(t, service, "spec", "selector", "app.kubernetes.io/instance"); got != release {
				t.Fatalf("release selector changed to %q", got)
			}
		})
	}
}

func TestPackagedChartExcludesTestInputsAndRenders(t *testing.T) {
	destination := t.TempDir()
	if output, err := helm(t, "package", "--destination", destination, chartPath(t)); err != nil {
		t.Fatalf("package chart: %v\n%s", err, output)
	}
	packages, err := filepath.Glob(filepath.Join(destination, "*.tgz"))
	if err != nil || len(packages) != 1 {
		t.Fatalf("packaged charts = %v, %v", packages, err)
	}
	if output, err := helm(t, "lint", "--strict", "--kube-version", kubeVersion(), packages[0]); err != nil {
		t.Fatalf("lint packaged chart: %v\n%s", err, output)
	}
	if output, err := helm(t, "template", testRelease, packages[0], "--namespace", testNamespace, "--kube-version", kubeVersion()); err != nil {
		t.Fatalf("render packaged chart: %v\n%s", err, output)
	}
	listing, err := exec.CommandContext(t.Context(), "tar", "-tzf", packages[0]).CombinedOutput() // #nosec G204 -- the path is the sole chart package generated under t.TempDir.
	if err != nil {
		t.Fatalf("list packaged chart: %v\n%s", err, listing)
	}
	if strings.Contains(string(listing), "/ci/") {
		t.Fatalf("packaged chart contains test-only inputs:\n%s", listing)
	}
}

func assertRuntimeDeployment(t *testing.T, deployment object, role, serviceAccount, containerName, portName, startupPath string) {
	t.Helper()
	pod := mapAt(t, deployment, "spec", "template", "spec")
	if got := stringValue(t, pod["serviceAccountName"]); got != serviceAccount {
		t.Errorf("%s ServiceAccount = %q, want %q", role, got, serviceAccount)
	}
	assertPodSecurity(t, pod, containerName)
	container := namedMap(t, asSlice(t, pod["containers"]), containerName)
	ports := asSlice(t, container["ports"])
	if len(ports) != 1 || stringAt(t, asMap(t, ports[0]), "name") != portName {
		t.Errorf("%s ports = %#v", role, ports)
	}
	startup := mapAt(t, container, "startupProbe", "httpGet")
	if stringValue(t, startup["path"]) != startupPath || stringValue(t, startup["port"]) != portName {
		t.Errorf("%s startup probe = %#v", role, startup)
	}
	for _, probe := range []string{"livenessProbe", "readinessProbe"} {
		httpGet := mapAt(t, container, probe, "httpGet")
		if stringValue(t, httpGet["port"]) != portName {
			t.Errorf("%s %s does not use %s", role, probe, portName)
		}
	}
	assertSelectorMatches(t, deployment, deployment)
}

func assertPodSecurity(t *testing.T, pod map[string]any, containerName string) {
	t.Helper()
	if boolValue(t, pod["automountServiceAccountToken"]) || boolValue(t, pod["enableServiceLinks"]) {
		t.Errorf("%s pod enables a service-account token or service links", containerName)
	}
	podSecurity := asMap(t, pod["securityContext"])
	if !boolValue(t, podSecurity["runAsNonRoot"]) || stringAt(t, podSecurity, "seccompProfile", "type") != "RuntimeDefault" {
		t.Errorf("%s pod securityContext = %#v", containerName, podSecurity)
	}
	container := namedMap(t, asSlice(t, pod["containers"]), containerName)
	security := asMap(t, container["securityContext"])
	if boolValue(t, security["allowPrivilegeEscalation"]) || !boolValue(t, security["readOnlyRootFilesystem"]) {
		t.Errorf("%s container securityContext = %#v", containerName, security)
	}
	if drops := stringSlice(t, valueAt(t, security, "capabilities", "drop")); !slices.Equal(drops, []string{"ALL"}) {
		t.Errorf("%s dropped capabilities = %v", containerName, drops)
	}
	if _, exists := security["runAsUser"]; exists {
		t.Errorf("%s pins runAsUser instead of preserving arbitrary-UID admission", containerName)
	}
	if len(asMap(t, container["resources"])) == 0 {
		t.Errorf("%s has no resource settings", containerName)
	}
	volumes := asSlice(t, pod["volumes"])
	tmp := namedMap(t, volumes, "tmp")
	if got := stringAt(t, tmp, "emptyDir", "sizeLimit"); got != "64Mi" {
		t.Errorf("%s temporary volume limit = %q", containerName, got)
	}
}

func assertSelectorMatches(t *testing.T, selected, deployment object) {
	t.Helper()
	selector := mapAt(t, selected, "spec", "selector")
	if matchLabels, found := selector["matchLabels"]; found {
		selector = asMap(t, matchLabels)
	}
	podLabels := mapAt(t, deployment, "spec", "template", "metadata", "labels")
	for key, value := range selector {
		if podLabels[key] != value {
			t.Errorf("%s selector %s=%v does not match pod label %v", stringAt(t, selected, "kind"), key, value, podLabels[key])
		}
	}
}

func assertHook(t *testing.T, resource object, weight string) {
	t.Helper()
	annotations := mapAt(t, resource, "metadata", "annotations")
	if stringValue(t, annotations["helm.sh/hook"]) != "pre-install,pre-upgrade" || stringValue(t, annotations["helm.sh/hook-weight"]) != weight || stringValue(t, annotations["helm.sh/hook-delete-policy"]) != "before-hook-creation,hook-succeeded" {
		t.Errorf("%s hook annotations = %#v", stringAt(t, resource, "kind"), annotations)
	}
}

func assertDatabaseSecretEnvironment(t *testing.T, container map[string]any, name, key string) {
	t.Helper()
	const variable = "CATALOG_DATABASE_DSN"
	entry, found := environmentEntry(t, container, variable)
	if !found {
		t.Errorf("environment variable %s is missing", variable)
		return
	}
	reference := mapAt(t, entry, "valueFrom", "secretKeyRef")
	if stringValue(t, reference["name"]) != name || stringValue(t, reference["key"]) != key {
		t.Errorf("%s Secret reference = %#v", variable, reference)
	}
}

func environmentEntry(t *testing.T, container map[string]any, name string) (map[string]any, bool) {
	t.Helper()
	for _, raw := range optionalSlice(t, container["env"]) {
		entry := asMap(t, raw)
		if stringValue(t, entry["name"]) == name {
			return entry, true
		}
	}
	return nil, false
}

func validateRenderedConfiguration(t *testing.T, objects []object) {
	t.Helper()
	validateRenderedConfigurationWithFixtures(t, objects, renderedConfigFixtures{})
}

func withEnvironment(t *testing.T, values map[string]string, run func()) {
	t.Helper()
	type priorValue struct {
		value string
		set   bool
	}
	names := config.EnvironmentVariables()
	prior := make(map[string]priorValue, len(names))
	for _, name := range names {
		value, set := os.LookupEnv(name)
		prior[name] = priorValue{value: value, set: set}
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, name := range names {
			value := prior[name]
			var err error
			if value.set {
				err = os.Setenv(name, value.value)
			} else {
				err = os.Unsetenv(name)
			}
			if err != nil {
				t.Errorf("restore %s: %v", name, err)
			}
		}
	}()
	for name, value := range values {
		if err := os.Setenv(name, value); err != nil {
			t.Fatal(err)
		}
	}
	run()
}

func renderChart(t *testing.T, extra ...string) []object {
	t.Helper()
	return renderChartRelease(t, testRelease, extra...)
}

func renderChartRelease(t *testing.T, release string, extra ...string) []object {
	t.Helper()
	arguments := []string{"template", release, chartPath(t), "--namespace", testNamespace, "--kube-version", kubeVersion()}
	output, err := helm(t, append(arguments, extra...)...)
	if err != nil {
		t.Fatalf("render chart: %v\n%s", err, output)
	}
	return decodeRender(t, output)
}

func decodeRender(t *testing.T, output string) []object {
	t.Helper()
	decoder := yaml.NewDecoder(strings.NewReader(output))
	var objects []object
	for {
		var resource object
		err := decoder.Decode(&resource)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode rendered chart: %v", err)
		}
		if len(resource) != 0 {
			objects = append(objects, resource)
		}
	}
	return objects
}

func helm(t *testing.T, arguments ...string) (string, error) {
	t.Helper()
	// Offline Helm commands otherwise use the client's compiled-in Kubernetes
	// version, which need not match the chart's declared minimum.
	if len(arguments) > 0 && (arguments[0] == "template" || arguments[0] == "lint") && !slices.Contains(arguments, "--kube-version") {
		arguments = append(arguments, "--kube-version", kubeVersion())
	}
	executable := os.Getenv("HELM")
	if executable == "" {
		executable = "helm"
	}
	command := exec.CommandContext(t.Context(), executable, arguments...) // #nosec G204,G702 -- HELM is an explicit developer-selected local executable and arguments are test-owned chart inputs.
	output, err := command.CombinedOutput()
	return string(output), err
}

func chartPath(t *testing.T, suffix ...string) string {
	t.Helper()
	chart := os.Getenv("HELM_CHART")
	if chart == "" {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		chart = filepath.Join(root, "deployments", "helm", "open-agentic-catalog")
	}
	return filepath.Join(append([]string{chart}, suffix...)...)
}

func kubeVersion() string {
	if version := os.Getenv("HELM_KUBE_VERSION"); version != "" {
		return version
	}
	return "1.34.0"
}

func oneObject(t *testing.T, objects []object, kind, component string) object {
	t.Helper()
	matches := objectsByKindAndComponent(objects, kind, component)
	if len(matches) != 1 {
		t.Fatalf("%s/%s objects = %v", kind, component, objectNames(t, matches))
	}
	return matches[0]
}

func objectsByKind(objects []object, kind string) []object {
	var matches []object
	for _, resource := range objects {
		if value, _ := resource["kind"].(string); value == kind {
			matches = append(matches, resource)
		}
	}
	return matches
}

func objectsByKindAndComponent(objects []object, kind, component string) []object {
	var matches []object
	for _, resource := range objectsByKind(objects, kind) {
		metadata, _ := rawMap(resource["metadata"])
		labels, _ := rawMap(metadata["labels"])
		if value, _ := labels["app.kubernetes.io/component"].(string); value == component {
			matches = append(matches, resource)
		}
	}
	return matches
}

func objectNames(t *testing.T, objects []object) []string {
	t.Helper()
	names := make([]string, 0, len(objects))
	for _, resource := range objects {
		names = append(names, stringAt(t, resource, "metadata", "name"))
	}
	return names
}

func namedMap(t *testing.T, values []any, name string) map[string]any {
	t.Helper()
	for _, raw := range values {
		value := asMap(t, raw)
		if stringValue(t, value["name"]) == name {
			return value
		}
	}
	t.Fatalf("named item %q not found", name)
	return nil
}

func mapAt(t *testing.T, value any, path ...string) map[string]any {
	t.Helper()
	return asMap(t, valueAt(t, value, path...))
}

func sliceAt(t *testing.T, value any, path ...string) []any {
	t.Helper()
	return asSlice(t, valueAt(t, value, path...))
}

func stringAt(t *testing.T, value any, path ...string) string {
	t.Helper()
	return stringValue(t, valueAt(t, value, path...))
}

func boolAt(t *testing.T, value any, path ...string) bool {
	t.Helper()
	return boolValue(t, valueAt(t, value, path...))
}

func valueAt(t *testing.T, value any, path ...string) any {
	t.Helper()
	current := value
	for _, part := range path {
		mapping := asMap(t, current)
		var found bool
		current, found = mapping[part]
		if !found {
			t.Fatalf("path %s is missing", strings.Join(path, "."))
		}
	}
	return current
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()
	mapping, ok := rawMap(value)
	if !ok {
		t.Fatalf("value %#v is not a map", value)
	}
	return mapping
}

func rawMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case object:
		return map[string]any(typed), true
	default:
		return nil, false
	}
}

func asSlice(t *testing.T, value any) []any {
	t.Helper()
	values, ok := value.([]any)
	if !ok {
		t.Fatalf("value %#v is not a list", value)
	}
	return values
}

func optionalSlice(t *testing.T, value any) []any {
	t.Helper()
	if value == nil {
		return nil
	}
	return asSlice(t, value)
}

func stringValue(t *testing.T, value any) string {
	t.Helper()
	text, ok := value.(string)
	if !ok {
		t.Fatalf("value %#v is not a string", value)
	}
	return text
}

func boolValue(t *testing.T, value any) bool {
	t.Helper()
	boolean, ok := value.(bool)
	if !ok {
		t.Fatalf("value %#v is not a boolean", value)
	}
	return boolean
}

func intValue(t *testing.T, value any) int {
	t.Helper()
	number, ok := value.(int)
	if !ok {
		t.Fatalf("value %#v is not an integer", value)
	}
	return number
}

func stringSlice(t *testing.T, value any) []string {
	t.Helper()
	values := asSlice(t, value)
	result := make([]string, 0, len(values))
	for _, raw := range values {
		result = append(result, stringValue(t, raw))
	}
	return result
}
