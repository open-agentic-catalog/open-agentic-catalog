//go:build functional

package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMakeLaunchRejectsRemovedObjectSettings(t *testing.T) {
	t.Parallel()
	repository := openTestRepository(t)
	for _, target := range []string{"run", "config-check", "env-check", "diagnostics", "compose-up"} {
		t.Run(target, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "obsolete.env")
			if err := os.WriteFile(path, []byte("CATALOG_OBJECT_DRIVER=private-obsolete-value\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			// #nosec G204 -- Fixed Make executable and test-owned target allowlist/path; no user input or shell.
			command := exec.CommandContext(ctx, "make", "--no-print-directory", "--dry-run", target, "CATALOG_ENV_FILE="+path)
			command.Dir = repository.Name()
			command.Env = []string{"PATH=" + os.Getenv("PATH")}
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "CATALOG_OBJECT_DRIVER is no longer supported") || strings.Contains(string(output), "private-obsolete-value") {
				t.Fatalf("obsolete Make launch setting: err=%v output=%s", err, output)
			}
		})
	}
}
