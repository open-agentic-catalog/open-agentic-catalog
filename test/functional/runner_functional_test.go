//go:build functional

package functional_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Keep the Python orchestration safety tests in the service-free Go gate.
func TestFunctionalSupervisor(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py")
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("functional supervisor safety tests: %v\n%s", err, output)
	}
}
