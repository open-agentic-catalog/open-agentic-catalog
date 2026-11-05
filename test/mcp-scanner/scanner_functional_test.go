//go:build functional

package mcpscanner

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestOfflineScannerProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_functional_*.py")
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("offline scanner functional tests: %v\n%s", err, output)
	}
}
