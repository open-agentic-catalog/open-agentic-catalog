//go:build functional

// Package interoperability keeps service-free harness checks in the default suite.
package interoperability

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestHarness invokes local Python regressions, never Docker or the HF client.
func TestHarness(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	const python = "../../.tools/dev/bin/python"
	arguments := []string{"-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py"}
	command := exec.CommandContext(ctx, "python3", arguments...)
	if _, err := os.Stat(python); err == nil {
		command = exec.CommandContext(ctx, python, arguments...)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("interoperability harness: %v\n%s", err, output)
	}
}
