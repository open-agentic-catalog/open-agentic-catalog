//go:build functional

package runtime

// Exercise Python lint orchestration in disposable trees. Real Ruff rule and
// configuration qualification stays separate from the default Go test suite.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMakePythonLint verifies the real recipe with a test-owned executable,
// including paths with spaces, failure propagation and read-only arguments.
func TestMakePythonLint(t *testing.T) {
	t.Parallel()
	repository := openTestRepository(t)
	for _, status := range []int{0, 1, 2} {
		t.Run("tool exit "+strconv.Itoa(status), func(t *testing.T) {
			directory := t.TempDir()
			writeFormattingFixture(t, directory, "Makefile", string(readTestFile(t, repository, "Makefile")))
			writeFormattingFixture(t, directory, "ruff.toml", string(readTestFile(t, repository, "ruff.toml")))
			writeFormattingFixture(t, directory, "go.mod", "module example.com/pythonfixture\n\ngo 1.27.0\n")
			const source = "import unused_module\n"
			writeFormattingFixture(t, directory, "test/example.py", source)
			const stub = "#!/bin/sh\nprintf '%s\\n' \"$@\" > arguments\nexit \"$RUFF_TEST_EXIT\"\n"
			writeFormattingFixture(t, directory, "tools with spaces/ruff stub", stub)
			executable := filepath.Join(directory, "tools with spaces/ruff stub")
			// #nosec G302 -- Owner-only execution is required for this temporary test stub.
			if err := os.Chmod(executable, 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			// #nosec G204 -- Fixed Make command and test-owned executable only.
			command := exec.CommandContext(ctx, "make", "--no-print-directory", "python-lint", "CATALOG_ENV_FILE=/dev/null", "RUFF="+executable)
			command.Dir = directory
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "RUFF_TEST_EXIT=" + strconv.Itoa(status)}
			output, err := command.CombinedOutput()
			if (err == nil) != (status == 0) {
				t.Fatalf("tool exit %d: err=%v output=%s", status, err, output)
			}
			if got := string(readFormattingFixture(t, directory, "arguments")); got != "check\n--config\nruff.toml\n--no-cache\n--no-fix\ntest\nextensions/vscode/tooling\nexamples/langgraph\nexamples/tutorials/local-oidc\nexamples/mcp-scanner\n" {
				t.Fatalf("unexpected Ruff arguments: %q", got)
			}
			if got := string(readFormattingFixture(t, directory, "test/example.py")); got != source {
				t.Fatal("lint recipe changed source")
			}
			if _, err := os.Stat(filepath.Join(directory, ".ruff_cache")); !os.IsNotExist(err) {
				t.Fatalf("unexpected cache or stat failure: %v", err)
			}
			if status != 0 {
				return
			}
			// Removing only this fixture executable must produce the install hint,
			// not silently fall back to Ruff on the developer's PATH.
			if err := os.Remove(executable); err != nil {
				t.Fatal(err)
			}
			// #nosec G204 -- Fixed Make command and the now-missing test-owned path only.
			command = exec.CommandContext(ctx, "make", "--no-print-directory", "python-lint", "CATALOG_ENV_FILE=/dev/null", "RUFF="+executable)
			command.Dir = directory
			command.Env = []string{"PATH=" + os.Getenv("PATH")}
			output, err = command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "Ruff is required. Install the version in ruff.toml") {
				t.Fatalf("missing tool: err=%v output=%s", err, output)
			}
		})
	}
}
