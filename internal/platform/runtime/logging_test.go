package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestRuntimeLoggingSelectionAndInjection(t *testing.T) {
	for _, name := range config.EnvironmentVariables() {
		unsetEnvironment(t, name)
	}
	for _, role := range []config.Role{config.RoleAPI, config.RoleWorker, config.RoleAll} {
		for _, format := range []string{"text", "json"} {
			for _, injected := range []bool{false, true} {
				t.Run(string(role)+"/"+format+"/"+map[bool]string{false: "configured", true: "injected"}[injected], func(t *testing.T) {
					cfg := localRuntimeConfig()
					cfg.LogLevel = "error"
					cfg.LogFormat = format
					var output, owned bytes.Buffer
					options := BuildOptions{Role: role, ConfigPath: writeRuntimeConfig(t, cfg), Version: "fixture", LogOutput: &output}
					if injected {
						options.Logger = slog.New(slog.NewTextHandler(&owned, &slog.HandlerOptions{Level: slog.LevelDebug}))
					}
					app, err := Build(context.Background(), options)
					if err != nil {
						t.Fatal(err)
					}
					defer app.Close()
					if app.Config.LogLevel != "error" || app.Config.LogFormat != format {
						t.Fatal("selected settings lost")
					}
					app.Logger().Debug("debug control")
					app.Logger().Error("error control")
					if injected {
						options.Logger.Info("parent control")
						lines := strings.Split(strings.TrimSpace(owned.String()), "\n")
						if strings.Contains(lines[len(lines)-1], "service=") || !strings.Contains(owned.String(), "role="+string(role)) {
							t.Fatal("child metadata missing or parent mutated")
						}
						if output.Len() != 0 || !strings.Contains(owned.String(), "debug control") || !strings.Contains(owned.String(), "logger_source=injected") {
							t.Fatalf("injected logger replaced: %s %s", output.String(), owned.String())
						}
					} else if strings.Contains(output.String(), "logging configured") || strings.Contains(output.String(), "debug control") || !strings.Contains(output.String(), "error control") || json.Valid(bytes.TrimSpace(output.Bytes())) != (format == "json") {
						t.Fatalf("configuration ignored: %s", output.String())
					}
				})
			}
		}
	}
}
