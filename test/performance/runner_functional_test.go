//go:build functional

package main

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestPerformanceSupervisor(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("performance safety tests: %v\n%s", err, output)
	}
}
