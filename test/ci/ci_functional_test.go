//go:build functional

package ci

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestLocalWorkflowFixtures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_functional_*.py")
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("local workflow fixtures: %v\n%s", err, output)
	}
}
