package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggingConfigurationRoles(t *testing.T) {
	for _, role := range []Role{RoleAPI, RoleWorker, RoleAll, RoleMigration} {
		for _, level := range []string{"debug", "info", "warn", "error", "", "INFO", "secret"} {
			for _, format := range []string{"text", "json", "", "JSON", "secret"} {
				c := Defaults()
				c.Auth.DevelopmentTokens = map[string]DevelopmentUser{"fixture": {Subject: "fixture", TenantID: "00000000-0000-7000-8000-000000000001"}}
				if role == RoleMigration {
					c.Database = Database{Driver: "postgres", DSN: "postgres://owner@invalid/catalog", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
				}
				c.LogLevel, c.LogFormat = level, format
				err := c.ValidateFor(role)
				valid := (level == "debug" || level == "info" || level == "warn" || level == "error") && (format == "text" || format == "json")
				if valid && err != nil || !valid && err == nil {
					t.Fatalf("%s %q %q: %v", role, level, format, err)
				}
				if err != nil && strings.Contains(err.Error(), "secret") {
					t.Fatal("private setting echoed")
				}
			}
		}
	}
}

func TestLoggingLoadDefaultsStrictnessOverridesAndReport(t *testing.T) {
	clearRuntimeEnvironment(t)
	path := filepath.Join(t.TempDir(), "logging.json")
	write := func(raw string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ raw, level, format string }{{`{}`, "info", "text"}, {`{"logLevel":"debug","logFormat":"json"}`, "debug", "json"}} {
		write(tc.raw)
		c, err := LoadFor(path, RoleWorker)
		if err != nil || c.LogLevel != tc.level || c.LogFormat != tc.format || c.Report().LogLevel != tc.level || c.Report().LogFormat != tc.format {
			t.Fatalf("load/report: %+v %v", c.Report(), err)
		}
	}
	for _, raw := range []string{`{"logLevel":""}`, `{"logFormat":""}`, `{"logLevel":null}`, `{"logFormat":null}`, `{"logLevel":1}`, `{"logFormat":true}`, `{"logLevel":"info","logLevel":"debug"}`, `{"logFormat":"text","logFormat":"json"}`, `{"logLevel":"INFO"}`, `{"logFormat":"JSON"}`} {
		write(raw)
		if _, err := LoadFor(path, RoleWorker); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	write(`{"logLevel":"warn","logFormat":"text"}`)
	t.Setenv("CATALOG_LOG_LEVEL", "debug")
	t.Setenv("CATALOG_LOG_FORMAT", "json")
	c, err := LoadFor(path, RoleWorker)
	if err != nil || c.LogLevel != "debug" || c.LogFormat != "json" {
		t.Fatalf("override: %s %s %v", c.LogLevel, c.LogFormat, err)
	}
	for _, name := range []string{"CATALOG_LOG_LEVEL", "CATALOG_LOG_FORMAT"} {
		for _, value := range []string{"", "SYNTHETIC_PRIVATE_SETTING"} {
			t.Run(name+value, func(t *testing.T) {
				t.Setenv(name, value)
				_, err := LoadFor(path, RoleWorker)
				if err == nil || strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_SETTING") {
					t.Fatalf("invalid override: %v", err)
				}
			})
		}
	}
}
