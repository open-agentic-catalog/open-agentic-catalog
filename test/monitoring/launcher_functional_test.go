//go:build functional

package monitoring_test

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestMonitoringLauncher(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("monitoring safety tests: %v\n%s", err, output)
	}
}
