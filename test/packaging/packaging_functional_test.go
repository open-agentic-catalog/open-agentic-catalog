//go:build functional

package packaging

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestPackaging keeps service-free packaging regressions in the normal Go gate.
func TestPackaging(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	python, err := packagingPython("../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, python, "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py") // #nosec G204 -- interpreter is the repository-owned venv or fixed python3 fallback; arguments are fixed.
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("packaging regressions: %v\n%s", err, output)
	}
}
