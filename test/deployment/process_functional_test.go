//go:build helm

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealHelmTargetSnapshot(t *testing.T) {
	for _, values := range []string{"values.yaml", "ci/pgbouncer.yaml", "ci/ui-values.yaml"} {
		t.Run(values, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			o := testOptions()
			if selected := os.Getenv("HELM"); selected != "" {
				o.helm = selected
			}
			o.chart = filepath.Join("..", "..", "deployments", "helm", "open-agentic-catalog")
			o.values = filepath.Join(o.chart, values)
			prepared, err := snapshot(ctx, o, execute, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			raw, err := execute(ctx, o.helm, "template", "catalog", prepared.chart, "--values", prepared.values, "--kube-version", "1.34.0")
			if err != nil {
				t.Fatal(err)
			}
			targets, err := targets(raw, "catalog")
			if err != nil || len(targets) != 2 {
				t.Fatalf("actual chart: %v", err)
			}
		})
	}
}

func TestRealHelmCollisionStopsBeforeClusterCommands(t *testing.T) {
	o := testOptions()
	if selected := os.Getenv("HELM"); selected != "" {
		o.helm = selected
	}
	o.chart = filepath.Join("..", "..", "deployments", "helm", "open-agentic-catalog")
	raw, err := os.ReadFile(filepath.Join(o.chart, "ci", "ui-values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	files, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := files.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := files.WriteFile("values.yaml", append(raw, []byte("\nfullnameOverride: "+strings.Repeat("a", 60)+"-ui\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	o.values = filepath.Join(directory, "values.yaml")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	prepared, err := snapshot(ctx, o, execute, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rendered := false
	run := func(ctx context.Context, program string, args ...string) ([]byte, error) {
		if program != o.helm || len(args) == 0 || args[0] != "template" {
			t.Fatal("invalid chart reached a cluster command or deployment mutation")
		}
		rendered = true
		return execute(ctx, program, args...)
	}
	err = deploy(ctx, prepared, run, func(string) {})
	if !rendered || err == nil || !strings.Contains(err.Error(), "chart render failed; workloads unchanged") {
		t.Fatalf("collision was not refused before deployment: rendered=%t, err=%v", rendered, err)
	}
}
