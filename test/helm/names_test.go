//go:build helm

package helm_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func packagedApplicationChart(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if output, err := helm(t, "package", chartPath(t), "--destination", directory); err != nil {
		t.Fatalf("package chart: %v\n%s", err, output)
	}
	archives, err := filepath.Glob(filepath.Join(directory, "*.tgz"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("chart archives: %v, %v", archives, err)
	}
	return archives[0]
}

func assertUniqueResourceNames(t *testing.T, objects []object) {
	t.Helper()
	seen := make(map[string]bool)
	for _, resource := range objects {
		metadata := mapAt(t, resource, "metadata")
		namespace, _ := metadata["namespace"].(string)
		if namespace == "" {
			namespace = testNamespace
		}
		key := stringAt(t, resource, "kind") + "/" + namespace + "/" + stringAt(t, resource, "metadata", "name")
		if seen[key] {
			t.Fatalf("duplicate Kubernetes identity: %s", key)
		}
		seen[key] = true
	}
}

func TestGeneratedNamesCannotReplaceAnotherChartResource(t *testing.T) {
	archive := packagedApplicationChart(t)
	for _, target := range []string{chartPath(t), archive} {
		for _, suffix := range []string{"ui", "pgbouncer", "migration", "federation-policy"} {
			for _, length := range []int{61, 62, 63} {
				for _, composed := range []bool{false, true} {
					name := strings.Repeat("a", length-len(suffix)-1) + "-" + suffix
					key := "fullnameOverride"
					if composed {
						key = "nameOverride"
						name = strings.Repeat("a", length-len(testRelease)-len(suffix)-2) + "-" + suffix
					}
					args := []string{"template", testRelease, target, "--namespace", testNamespace, "--kube-version", kubeVersion(), "--set-string", key + "=" + name}
					if suffix == "ui" || suffix == "pgbouncer" {
						profile := suffix + "-values.yaml"
						if suffix == "pgbouncer" {
							profile = "pgbouncer.yaml"
						}
						args = append(args, "--values", chartPath(t, "ci", profile))
					}
					output, err := helm(t, args...)
					if length >= 62 {
						if err == nil || !strings.Contains(output, "resource name collides") {
							t.Fatalf("%s %s length %d: expected collision refusal, got %v\n%s", suffix, key, length, err, output)
						}
					} else {
						if err != nil {
							t.Fatalf("valid neighboring name: %v\n%s", err, output)
						}
						assertUniqueResourceNames(t, decodeRender(t, output))
					}
				}
			}
		}
		for _, suffix := range []string{"ui", "pgbouncer", "federation-policy"} {
			args := []string{"template", testRelease, target, "--namespace", testNamespace, "--set-string", "fullnameOverride=" + strings.Repeat("a", 62-len(suffix)) + "-" + suffix}
			if suffix == "federation-policy" {
				args = append(args, "--set-string", "federation.policy.existingConfigMap=external-policy")
			}
			output, err := helm(t, args...)
			if err != nil {
				t.Fatalf("disabled or external %s: %v\n%s", suffix, err, output)
			}
			assertUniqueResourceNames(t, decodeRender(t, output))
		}
		output, err := helm(t, "template", testRelease, target, "--is-upgrade", "--set-string", "fullnameOverride="+strings.Repeat("a", 53)+"-migration")
		if err == nil || !strings.Contains(output, "resource name collides") {
			t.Fatalf("upgrade accepted colliding names: %v\n%s", err, output)
		}
	}
}
