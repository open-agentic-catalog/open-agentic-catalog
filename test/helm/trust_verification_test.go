//go:build helm

package helm_test

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestVerificationKeysAreOptionalAndRuntimeOnly(t *testing.T) {
	uiValues := filepath.Join(chartPath(t), "ci/ui-values.yaml")
	baseline := renderChart(t, "--values", uiValues)
	for _, enabled := range []bool{false, true} {
		value := `{"enabled":false}`
		if enabled {
			value = `{"enabled":true,"policies":[{"name":"owned-ans","profile":"ans-registration-v1","registrationAuthority":"ans-ra","logOrigin":"owned-ans","evidenceOrigin":"https://log.example","publisherDomains":["publisher.example"],"registrationKeys":[{"id":"ra","file":"/etc/open-agentic-catalog/trust-verification/ra.pem"}],"logKeyFiles":["/etc/open-agentic-catalog/trust-verification/log.pem"]}]}`
		}
		objects := renderChart(t, "--values", uiValues, "--is-upgrade", "--set-string", "trustVerification.publicKeysConfigMap=ans-public-keys-v1", "--set-literal", "configEnv.CATALOG_TRUST_VERIFICATION="+value)
		if stringAt(t, oneObject(t, objects, "ConfigMap", "configuration"), "data", "CATALOG_TRUST_VERIFICATION") != value {
			t.Fatal("verification settings changed")
		}
		for _, role := range []string{"api", "worker"} {
			deployment := oneObject(t, objects, "Deployment", role)
			volumeFound, mountFound := false, false
			for _, raw := range sliceAt(t, deployment, "spec", "template", "spec", "volumes") {
				v := asMap(t, raw)
				if stringAt(t, v, "name") == "trust-verification" {
					volumeFound = true
					if stringAt(t, v, "configMap", "name") != "ans-public-keys-v1" {
						t.Fatal("verification keys did not use separate ConfigMap")
					}
				}
			}
			for _, container := range sliceAt(t, deployment, "spec", "template", "spec", "containers") {
				for _, raw := range sliceAt(t, container, "volumeMounts") {
					m := asMap(t, raw)
					if stringAt(t, m, "name") == "trust-verification" {
						mountFound = true
						if stringAt(t, m, "mountPath") != "/etc/open-agentic-catalog/trust-verification" || !boolAt(t, m, "readOnly") {
							t.Fatal("verification mount is not fixed/read-only")
						}
					}
				}
			}
			if volumeFound != enabled || mountFound != enabled {
				t.Fatal("disabled/missing verification mount", role, enabled)
			}
			if enabled && stringAt(t, oneObject(t, baseline, "Deployment", role), "spec", "template", "metadata", "annotations", "checksum/config") == stringAt(t, deployment, "spec", "template", "metadata", "annotations", "checksum/config") {
				t.Fatal("verification change did not roll", role)
			}
		}
		for _, kind := range []string{"Job", "ConfigMap"} {
			if !reflect.DeepEqual(oneObject(t, baseline, kind, "migration"), oneObject(t, objects, kind, "migration")) {
				t.Fatal("verification changed migration authority", kind)
			}
		}
		for _, kind := range []string{"Deployment", "ConfigMap"} {
			if !reflect.DeepEqual(oneObject(t, baseline, kind, "ui"), oneObject(t, objects, kind, "ui")) {
				t.Fatal("verification changed the independent UI workload", kind)
			}
		}
		if enabled {
			rotated := renderChart(t, "--values", uiValues, "--is-upgrade", "--set-string", "trustVerification.publicKeysConfigMap=ans-public-keys-v2", "--set-literal", "configEnv.CATALOG_TRUST_VERIFICATION="+value)
			for _, role := range []string{"api", "worker"} {
				if reflect.DeepEqual(mapAt(t, oneObject(t, objects, "Deployment", role), "spec", "template"), mapAt(t, oneObject(t, rotated, "Deployment", role), "spec", "template")) {
					t.Fatal("changing key map did not roll", role)
				}
			}
		}
	}
	for _, value := range []string{`[]`, `null`, `{invalid}`, `{"enabled":"false"}`} {
		if output, err := helm(t, "template", testRelease, chartPath(t), "--set-literal", "configEnv.CATALOG_TRUST_VERIFICATION="+value); err == nil {
			t.Fatalf("unsafe verification value rendered: %s", output)
		}
	}
	if output, err := helm(t, "template", testRelease, chartPath(t), "--set-literal", `configEnv.CATALOG_TRUST_VERIFICATION={"enabled":true}`); err == nil {
		t.Fatalf("enabled verification without keys rendered: %s", output)
	}
}
