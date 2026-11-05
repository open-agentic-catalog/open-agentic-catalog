//go:build functional

package runtime

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Check the actual recipe without downloading advisory data or scanning the
// caller's checkout. Real scanner execution remains the make security gate.
func TestMakeSecurityUsesChartBaselineAndPropagatesFailures(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writeFormattingFixture(t, directory, "Makefile", string(readTestFile(t, openTestRepository(t), "Makefile")))
	writeFormattingFixture(t, directory, "go.mod", "module example.com/securityfixture\n\ngo 1.27.0\n")
	run := func(args ...string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		// #nosec G204 -- Fixed Make executable and test-owned inputs only.
		command := exec.CommandContext(ctx, "make", append([]string{"--no-print-directory", "security", "CATALOG_ENV_FILE=/dev/null"}, args...)...)
		command.Dir = directory
		command.Env = []string{"PATH=" + os.Getenv("PATH")}
		output, err := command.CombinedOutput()
		return string(output), err
	}
	for _, version := range []string{"1.34.0", "1.35.1"} {
		t.Run(version, func(t *testing.T) {
			args := []string{"--dry-run"}
			if version != "1.34.0" {
				args = append(args, "HELM_KUBE_VERSION="+version)
			}
			output, err := run(args...)
			if err != nil {
				t.Fatalf("security recipe failed: %v: %s", err, output)
			}
			requireTextFragments(t, output, []string{
				"--helm-kube-version \"" + version + "\"",
				"--scanners vuln,secret,misconfig",
				"--severity HIGH,CRITICAL --exit-code 1",
				"--skip-dirs output",
			})
		})
	}
	for _, scanner := range []string{"GO", "TRIVY"} {
		t.Run(scanner+" failure", func(t *testing.T) {
			args := []string{"GO=true", "TRIVY=true", scanner + "=false"}
			output, err := run(args...)
			if err == nil {
				t.Fatalf("%s failure was ignored: %s", scanner, output)
			}
			if scanner == "GO" && strings.Contains(output, "--scanners") {
				t.Fatalf("continued after the Go scanner failed: %s", output)
			}
		})
	}
}
