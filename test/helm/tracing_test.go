//go:build helm

package helm_test

import (
	"reflect"
	"strings"
	"testing"
)

func TestTracingInputsAreOptionalAndRuntimeScoped(t *testing.T) {
	for _, test := range []struct {
		name            string
		enabled, secret bool
	}{
		{"disabled without Secret", false, false},
		{"disabled with Secret", false, true},
		{"enabled without Secret", true, false},
		{"enabled with Secret", true, true},
	} {
		value := `{"enabled":false}`
		if test.enabled {
			value = `{"enabled":true,"endpoint":"https://collector.example/v1/traces","installation":"test"}`
		}
		t.Run(test.name, func(t *testing.T) {
			args := []string{"--set-literal", "configEnv.CATALOG_TRACING=" + value}
			if test.secret {
				args = append(args, "--set", "secrets.tracingInputs.name=trace-inputs")
			}
			objects := renderChart(t, args...)
			wantMount := test.enabled && test.secret
			for _, role := range []string{"api", "worker"} {
				deployment := oneObject(t, objects, "Deployment", role)
				found := false
				for _, raw := range sliceAt(t, deployment, "spec", "template", "spec", "volumes") {
					v := asMap(t, raw)
					if stringAt(t, v, "name") == "tracing-inputs" {
						found = true
						if stringAt(t, v, "secret", "secretName") != "trace-inputs" {
							t.Fatal("wrong Secret")
						}
					}
				}
				if found != wantMount {
					t.Errorf("%s tracing volume = %v, want %v", role, found, wantMount)
				}
				mounted := false
				for _, raw := range sliceAt(t, deployment, "spec", "template", "spec", "containers") {
					for _, mount := range sliceAt(t, raw, "volumeMounts") {
						if stringAt(t, mount, "name") == "tracing-inputs" {
							mounted = true
							if stringAt(t, mount, "mountPath") != "/etc/open-agentic-catalog/tracing" || !boolAt(t, mount, "readOnly") {
								t.Fatal("tracing Secret must use the documented read-only path")
							}
						}
					}
				}
				if mounted != wantMount {
					t.Errorf("%s tracing mount = %v, want %v", role, mounted, wantMount)
				}
			}
			migration := oneObject(t, objects, "Job", "migration")
			for _, raw := range sliceAt(t, migration, "spec", "template", "spec", "volumes") {
				if strings.Contains(stringAt(t, asMap(t, raw), "name"), "tracing") {
					t.Fatal("operator received tracing Secret")
				}
			}
		})
	}
}

func TestTracingConfigurationRollsRuntimesButNotMigration(t *testing.T) {
	baseline := renderChart(t)
	previous := baseline
	for _, value := range []string{
		`{"enabled":true,"endpoint":"https://collector.example/v1/traces","installation":"test"}`,
		`{"enabled":true,"endpoint":"https://other-collector.example/v1/traces","installation":"test"}`,
		`{"enabled":false}`,
	} {
		objects := renderChart(t, "--is-upgrade", "--set-literal", "configEnv.CATALOG_TRACING="+value)
		if got := stringAt(t, oneObject(t, objects, "ConfigMap", "configuration"), "data", "CATALOG_TRACING"); got != value {
			t.Fatalf("tracing JSON changed during rendering: %q", got)
		}
		for _, role := range []string{"api", "worker"} {
			oldChecksum := stringAt(t, oneObject(t, previous, "Deployment", role), "spec", "template", "metadata", "annotations", "checksum/config")
			newChecksum := stringAt(t, oneObject(t, objects, "Deployment", role), "spec", "template", "metadata", "annotations", "checksum/config")
			if oldChecksum == newChecksum {
				t.Errorf("%s tracing change did not change the rollout checksum", role)
			}
		}
		for _, kind := range []string{"ConfigMap", "Job"} {
			if !reflect.DeepEqual(oneObject(t, baseline, kind, "migration"), oneObject(t, objects, kind, "migration")) {
				t.Errorf("tracing change affected migration %s", kind)
			}
		}
		previous = objects
	}
}

func TestTracingFilesRequireTheirChartMount(t *testing.T) {
	for _, files := range []string{
		`"headersFile":"/etc/open-agentic-catalog/tracing/headers.json"`,
		`"caFile":"/etc/open-agentic-catalog/tracing/ca.pem"`,
		`"clientCertificateFile":"/etc/open-agentic-catalog/tracing/client.pem","clientKeyFile":"/etc/open-agentic-catalog/tracing/client.key"`,
	} {
		value := `{"enabled":true,"endpoint":"https://collector.example/v1/traces","installation":"test",` + files + `}`
		args := []string{"template", testRelease, chartPath(t), "--set-literal", "configEnv.CATALOG_TRACING=" + value}
		if output, err := helm(t, args...); err == nil || !strings.Contains(output, "secrets.tracingInputs is required") {
			t.Fatalf("tracing file without its mount: %v\n%s", err, output)
		}
		objects := renderChart(t, "--values", chartPath(t, "ci", "ui-values.yaml"), "--set-literal", "configEnv.CATALOG_TRACING="+value, "--set", "secrets.tracingInputs.name=trace-inputs")
		for _, role := range []string{"api", "worker"} {
			pod := mapAt(t, oneObject(t, objects, "Deployment", role), "spec", "template", "spec")
			if stringAt(t, namedMap(t, sliceAt(t, pod, "volumes"), "tracing-inputs"), "secret", "secretName") != "trace-inputs" {
				t.Fatal("valid file input lost its Secret mount")
			}
		}
		for _, role := range []string{"ui", "migration"} {
			kind := "Deployment"
			if role == "migration" {
				kind = "Job"
			}
			for _, volume := range sliceAt(t, oneObject(t, objects, kind, role), "spec", "template", "spec", "volumes") {
				if stringAt(t, volume, "name") == "tracing-inputs" {
					t.Fatal("tracing Secret escaped the runtime roles", role)
				}
			}
		}
		// File settings are inert while disabled and must not add a Secret dependency.
		disabled := strings.Replace(value, `"enabled":true`, `"enabled":false`, 1)
		objects = renderChart(t, "--set-literal", "configEnv.CATALOG_TRACING="+disabled)
		for _, role := range []string{"api", "worker"} {
			if strings.Contains(stringAt(t, oneObject(t, objects, "ConfigMap", "configuration"), "data", "CATALOG_TRACING"), `"enabled":true`) {
				t.Fatal("disabled tracing was enabled")
			}
			for _, volume := range sliceAt(t, oneObject(t, objects, "Deployment", role), "spec", "template", "spec", "volumes") {
				if stringAt(t, volume, "name") == "tracing-inputs" {
					t.Fatal("disabled tracing requires a Secret")
				}
			}
		}
	}
	// A deliberately reused, already projected CA does not need another Secret.
	renderChart(t, "--set", "databaseTrustBundleConfigMap=database-ca", "--set-literal", `configEnv.CATALOG_TRACING={"enabled":true,"endpoint":"https://collector.example/v1/traces","installation":"test","caFile":"/etc/open-agentic-catalog/database/ca.pem"}`)
	for _, invalid := range []string{`[]`, `null`, `{invalid}`, `{"enabled":"false"}`, `{"enabled":1}`} {
		if output, err := helm(t, "template", testRelease, chartPath(t), "--set", "secrets.tracingInputs.name=trace-inputs", "--set-literal", "configEnv.CATALOG_TRACING="+invalid); err == nil {
			t.Fatalf("invalid tracing JSON rendered: %s", output)
		}
	}
}
