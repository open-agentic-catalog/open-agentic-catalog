//go:build functional

package formal

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestFormalHarness checks the launcher without acquiring or invoking Gobra.
func TestFormalHarness(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py")
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("formal harness regressions: %v\n%s", err, output)
	}
}
