//go:build helm

package helm_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestKubernetesMinimum checks Helm's actual version constraint on source and
// packaged charts; vendor suffix examples are parsing cases, not distro tests.
func TestKubernetesMinimum(t *testing.T) {
	directory := t.TempDir()
	if output, err := helm(t, "package", chartPath(t), "--destination", directory); err != nil {
		t.Fatalf("package application chart: %v\n%s", err, output)
	}
	archives, err := filepath.Glob(filepath.Join(directory, "*.tgz"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("application archives: %v, %v", archives, err)
	}
	charts := map[string]string{
		"application-source":  chartPath(t),
		"application-package": archives[0],
		"monitoring-source":   filepath.Join(chartPath(t), "..", "open-agentic-catalog-monitoring"),
		"monitoring-package":  monitoringPackage(t),
	}
	for name, chart := range charts {
		t.Run(name, func(t *testing.T) {
			for _, version := range []string{"1.34.0", "1.34.1", "1.35.1", "v1.34.0+k3s1", "1.34.0-eks-example", "1.34.0-gke.1", "1.34.0-rc.1"} {
				t.Run(version, func(t *testing.T) {
					if output, err := helm(t, "template", "compatibility", chart, "--kube-version", version); err != nil {
						t.Fatalf("accepted version rejected: %v\n%s", err, output)
					}
				})
			}
			for _, version := range []string{"1.33.99", "1.33.99-eks-example", "1.33.99+k3s1", "1.32.0"} {
				t.Run(version, func(t *testing.T) {
					output, err := helm(t, "template", "compatibility", chart, "--kube-version", version)
					if err == nil || !strings.Contains(output, "requires kubeVersion: >=1.34.0-0") {
						t.Fatalf("expected minimum-version rejection: %v\n%s", err, output)
					}
				})
			}
		})
	}
}
