//go:build functional

package hooks_test

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// Hook regressions use disposable Git indexes without installing a real hook.
func TestStagedChecks(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("staged check tests: %v\n%s", err, output)
	}
}
