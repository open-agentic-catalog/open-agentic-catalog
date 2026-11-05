//go:build functional

package runtime

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestTracingStartsOnlyForEnabledDaemonConstruction(t *testing.T) {
	for _, role := range []config.Role{config.RoleAPI, config.RoleWorker} {
		for _, test := range []struct {
			name                    string
			start, enabled, ambient bool
			wantError               string
		}{
			{name: "operator enabled profile", enabled: true, ambient: true},
			{name: "disabled daemon", start: true, ambient: true},
			{name: "enabled daemon rejects SDK environment", start: true, enabled: true, ambient: true, wantError: "OTEL_*"},
			{name: "enabled daemon requires mounted input", start: true, enabled: true, wantError: "regular file"},
		} {
			t.Run(string(role)+"/"+test.name, func(t *testing.T) {
				unsetEnvironment(t, "CATALOG_TRACING")
				ambient := ""
				if test.ambient {
					ambient = "/private-input-must-not-be-opened"
				}
				t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", ambient)
				cfg := localRuntimeConfig()
				cfg.Tracing = config.Defaults().Tracing
				cfg.Tracing.Enabled = test.enabled
				cfg.Tracing.Endpoint = "https://collector.invalid/v1/traces"
				cfg.Tracing.Installation = "runtime-fixture"
				cfg.Tracing.HeadersFile = filepath.Join(t.TempDir(), "missing-private-input")
				runtime, err := Build(t.Context(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: role, StartTracing: test.start, Logger: slog.New(slog.DiscardHandler)})
				if runtime != nil {
					defer runtime.Close()
				}
				if test.wantError != "" {
					if err == nil || !strings.Contains(err.Error(), test.wantError) || strings.Contains(err.Error(), "private-input") {
						t.Fatalf("expected safe tracing startup failure, got %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if runtime.Process == nil || runtime.Process.ID() == "" {
					t.Fatal("disabled tracing lost process identity")
				}
				if runtime.Tracing != nil {
					t.Fatal("operator or disabled daemon started an exporter")
				}
			})
		}
	}
}
