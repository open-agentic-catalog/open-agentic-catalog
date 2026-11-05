package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestDatabasePoolSettingsLoadingAndReport(t *testing.T) {
	clearRuntimeEnvironment(t)
	path := filepath.Join(t.TempDir(), "pool.json")
	write := func(raw string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, raw              string
		lifetime, idle, health time.Duration
	}{
		{"defaults", `{}`, 30 * time.Minute, 5 * time.Minute, 30 * time.Second},
		{"explicit", `{"database":{"maxConnLifetime":"45m","maxConnIdleTime":"2m","healthCheckPeriod":"1.5s"}}`, 45 * time.Minute, 2 * time.Minute, 1500 * time.Millisecond},
		{"independent extremes", `{"database":{"maxConnLifetime":"1ns","maxConnIdleTime":"2562047h47m16.854775807s","healthCheckPeriod":"1h"}}`, 1, time.Duration(1<<63 - 1), time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.raw)
			c, err := LoadFor(path, RoleWorker)
			if err != nil {
				t.Fatal(err)
			}
			if c.Database.MaxConnLifetime.Duration != tc.lifetime || c.Database.MaxConnIdleTime.Duration != tc.idle || c.Database.HealthCheckPeriod.Duration != tc.health {
				t.Fatalf("settings: %+v", c.Database)
			}
			c.Database.DSN = "postgres://private-secret@localhost/catalog"
			raw, err := json.Marshal(c.Report().Database)
			if err != nil {
				t.Fatal(err)
			}
			var report map[string]any
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			if report["maxConnLifetime"] != tc.lifetime.String() || report["maxConnIdleTime"] != tc.idle.String() || report["healthCheckPeriod"] != tc.health.String() || report["dsnConfigured"] != true || strings.Contains(string(raw), "private-secret") {
				t.Fatalf("report: %s", raw)
			}
		})
	}
	write(`{"database":{"maxConnLifetime":"1h","maxConnIdleTime":"1h","healthCheckPeriod":"1h"}}`)
	t.Setenv("CATALOG_DATABASE_MAX_CONN_LIFETIME", "40m")
	t.Setenv("CATALOG_DATABASE_MAX_CONN_IDLE_TIME", "4m")
	t.Setenv("CATALOG_DATABASE_HEALTH_CHECK_PERIOD", "4s")
	c, err := LoadFor(path, RoleWorker)
	if err != nil || c.Database.MaxConnLifetime.Duration != 40*time.Minute || c.Database.MaxConnIdleTime.Duration != 4*time.Minute || c.Database.HealthCheckPeriod.Duration != 4*time.Second {
		t.Fatalf("environment precedence: %+v, %v", c.Database, err)
	}
}

func TestDatabasePoolSettingsRejectInvalidInputs(t *testing.T) {
	clearRuntimeEnvironment(t)
	path := filepath.Join(t.TempDir(), "pool.json")
	for _, field := range []struct{ name, env string }{
		{"maxConnLifetime", "CATALOG_DATABASE_MAX_CONN_LIFETIME"},
		{"maxConnIdleTime", "CATALOG_DATABASE_MAX_CONN_IDLE_TIME"},
		{"healthCheckPeriod", "CATALOG_DATABASE_HEALTH_CHECK_PERIOD"},
	} {
		for _, value := range []string{`"0s"`, `"-1s"`, `""`, `null`, `123`, `true`, `"2562047h47m16.854775808s"`, `"PRIVATE_SETTING"`} {
			t.Run(field.name+value, func(t *testing.T) {
				if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"database":{%q:%s}}`, field.name, value)), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := LoadFor(path, RoleWorker); err == nil || strings.Contains(safeerr.Message(err), "PRIVATE_SETTING") {
					t.Fatalf("invalid JSON setting: %v", err)
				}
			})
		}
		for _, value := range []string{"0", "-1s", "", "2562047h47m16.854775808s", "PRIVATE_SETTING"} {
			t.Run(field.env+value, func(t *testing.T) {
				t.Setenv(field.env, value)
				if _, err := LoadFor("", RoleWorker); err == nil || strings.Contains(safeerr.Message(err), "PRIVATE_SETTING") {
					t.Fatalf("invalid environment setting: %v", err)
				}
			})
		}
	}
}

func TestDatabasePoolSettingsValidatedForEveryRole(t *testing.T) {
	for _, role := range []Role{RoleAPI, RoleWorker, RoleAll, RoleMigration} {
		c := Defaults()
		c.Auth.DevelopmentTokens = map[string]DevelopmentUser{"fixture": {Subject: "fixture", TenantID: "00000000-0000-7000-8000-000000000001"}}
		if role == RoleMigration {
			c.Database.Driver, c.Database.DSN = "postgres", "postgres://owner@invalid/catalog"
		}
		if err := c.ValidateFor(role); err != nil {
			t.Fatalf("valid %s: %v", role, err)
		}
		for _, field := range []string{"maxConnLifetime", "maxConnIdleTime", "healthCheckPeriod"} {
			invalid := c
			switch field {
			case "maxConnLifetime":
				invalid.Database.MaxConnLifetime.Duration = 0
			case "maxConnIdleTime":
				invalid.Database.MaxConnIdleTime.Duration = 0
			case "healthCheckPeriod":
				invalid.Database.HealthCheckPeriod.Duration = 0
			}
			if err := invalid.ValidateFor(role); err == nil || !strings.Contains(err.Error(), "database."+field) {
				t.Fatalf("%s %s: %v", role, field, err)
			}
		}
	}
}
