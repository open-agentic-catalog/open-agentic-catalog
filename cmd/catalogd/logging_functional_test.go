//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// Reuse the executable built by the terminal-privacy test; capture actual process streams.
func verifyExecutableLogging(t *testing.T, ctx context.Context, binary, name string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Discovery.Hosts = []config.DiscoveryHost{{Host: "127.0.0.1", TenantID: "00000000-0000-7000-8000-000000000001", Mode: "anonymous", PublisherDomain: "catalog.example.com"}}
	cfg.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{"SYNTHETIC_PRIVATE_TOKEN": {Subject: "fixture", TenantID: cfg.Discovery.Hosts[0].TenantID, Roles: []identity.Role{identity.RoleTenantAdmin}}}
	file := filepath.Join(t.TempDir(), "logging.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Omission really exercises executable defaults, not explicit fixture values.
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "logLevel")
	delete(fields, "logFormat")
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	holder, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	commands := []string{"worker"}
	if name == "catalogctl" {
		commands = []string{"diagnostics", "bootstrap"}
	}
	for _, action := range commands {
		for _, settings := range []struct{ level, format string }{{"", ""}, {"debug", "text"}, {"debug", "json"}, {"info", "text"}, {"info", "json"}, {"warn", "text"}, {"warn", "json"}, {"error", "text"}, {"error", "json"}} {
			t.Run(action+"/"+settings.level+"/"+settings.format, func(t *testing.T) {
				cmd := exec.CommandContext(ctx, binary, action, "--config", file) // #nosec G204,G702 -- fixed locally built executable, owned configuration and fixed command list.
				cmd.Env = []string{"CATALOG_WORKER_OPERATIONS_ADDRESS=" + holder.Addr().String()}
				if settings.level != "" {
					cmd.Env = append(cmd.Env, "CATALOG_LOG_LEVEL="+settings.level, "CATALOG_LOG_FORMAT="+settings.format)
				}
				cmd.WaitDelay = time.Second
				var out, diagnostic bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &diagnostic
				err := cmd.Run()
				logs := out.String()
				if name == "catalogctl" {
					if err != nil || !json.Valid(bytes.TrimSpace(out.Bytes())) {
						t.Fatalf("result stream: %v %s %s", err, out.String(), diagnostic.String())
					}
					logs = diagnostic.String()
					if action == "diagnostics" {
						var result struct {
							Configuration config.Report `json:"configuration"`
						}
						if err := json.Unmarshal(out.Bytes(), &result); err != nil {
							t.Fatal(err)
						}
						wantLevel, wantFormat := settings.level, settings.format
						if wantLevel == "" {
							wantLevel, wantFormat = "info", "text"
						}
						if result.Configuration.LogLevel != wantLevel || result.Configuration.LogFormat != wantFormat {
							t.Fatalf("selected report: %+v", result.Configuration)
						}
					}
				} else {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "process.failed") || strings.Count(diagnostic.String(), "\n") != 1 {
						t.Fatalf("terminal result: %v %s", err, diagnostic.String())
					}
					if json.Valid(bytes.TrimSpace(diagnostic.Bytes())) != (settings.format == "json") {
						t.Fatalf("terminal format: %s", diagnostic.String())
					}
					if settings.format == "json" {
						var record map[string]any
						if err := json.Unmarshal(diagnostic.Bytes(), &record); err != nil {
							t.Fatal(err)
						}
						if record["event"] != "process.failed" || record["level"] != "ERROR" || record["service"] != "open-agentic-catalog" || record["command"] != "catalogd" || record["role"] != "worker" || record["error_code"] != "INTERNAL" {
							t.Fatalf("terminal classification: %v", record)
						}
					} else if !strings.Contains(diagnostic.String(), "level=ERROR") || !strings.Contains(diagnostic.String(), "service=open-agentic-catalog role=worker") || !strings.Contains(diagnostic.String(), "command=catalogd") || !strings.Contains(diagnostic.String(), "error_code=INTERNAL") {
						t.Fatalf("terminal classification: %s", diagnostic.String())
					}
				}
				admitted := settings.level == "" || settings.level == "debug" || settings.level == "info"
				if strings.Contains(logs, "process.logging_configured") != admitted {
					t.Fatalf("startup filtering: %s", logs)
				}
				if admitted {
					for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
						if json.Valid([]byte(line)) != (settings.format == "json") {
							t.Fatalf("service format: %s", line)
						}
					}
				} else if logs != "" {
					t.Fatalf("filtered runtime diagnostics: %s", logs)
				}
				if strings.Contains(out.String()+diagnostic.String(), "SYNTHETIC_PRIVATE_TOKEN") {
					t.Fatal("private token in logs")
				}
			})
		}
	}
}
