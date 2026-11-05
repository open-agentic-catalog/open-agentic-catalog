//go:build functional

// Embedding fixture regressions belong to the service-free default suite.
package embeddings_test

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestHarness verifies fixture configuration and cleanup without real providers.
func TestHarness(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("embedding fixture: %v\n%s", err, output)
	}
}
