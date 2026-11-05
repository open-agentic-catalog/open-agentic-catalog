//go:build functional

// Relevance tooling regressions run without Docker, model downloads or a DB.
package relevance_test

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestHarness keeps the Python fixture/metric/cleanup checks in the default gate.
func TestHarness(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("relevance harness: %v\n%s", err, output)
	}
}
