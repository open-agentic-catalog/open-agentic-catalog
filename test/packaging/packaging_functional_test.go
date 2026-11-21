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
	// The Python suite runs its own bounded Git, Go and Make subprocesses.
	// Give the whole suite room to complete alongside the other race tests.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	python, err := packagingPython("../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, python, "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py") // #nosec G204 -- interpreter is the repository-owned venv or fixed python3 fallback; arguments are fixed.
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		t.Fatalf("packaging regressions: %v\n%s", err, output)
	}
}
