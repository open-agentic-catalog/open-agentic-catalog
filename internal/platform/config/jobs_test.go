package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJobRetryConfigurationRolesAndBounds(t *testing.T) {
	defaults := Defaults()
	if defaults.Jobs.MaxAttempts != 5 || defaults.Jobs.RetryCeiling.Duration != time.Hour {
		t.Fatalf("Job defaults: %+v", defaults.Jobs)
	}
	for _, role := range []Role{RoleAPI, RoleWorker, RoleAll, RoleMigration} {
		for _, tc := range []struct {
			name     string
			attempts int
			ceiling  time.Duration
			valid    bool
		}{
			{"minimum", 1, 5 * time.Second, true}, {"maximum", 20, time.Hour, true}, {"fractional", 7, 5500 * time.Millisecond, true},
			{"zero attempts", 0, time.Hour, false}, {"negative attempts", -1, time.Hour, false}, {"excessive attempts", 21, time.Hour, false},
			{"zero delay", 5, 0, false}, {"negative delay", 5, -time.Second, false}, {"short delay", 5, 5*time.Second - time.Nanosecond, false}, {"long delay", 5, time.Hour + time.Nanosecond, false},
		} {
			t.Run(string(role)+"/"+tc.name, func(t *testing.T) {
				c := Defaults()
				c.Auth.DevelopmentTokens = map[string]DevelopmentUser{"fixture": {Subject: "fixture", TenantID: "00000000-0000-7000-8000-000000000001"}}
				c.Jobs = Jobs{MaxAttempts: tc.attempts, RetryCeiling: Duration{tc.ceiling}}
				if role == RoleMigration {
					c.Database = Database{Driver: "postgres", DSN: "postgres://owner@invalid/catalog", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
				}
				err := c.ValidateFor(role)
				valid := tc.valid || role == RoleMigration
				if valid && err != nil || !valid && (err == nil || !strings.Contains(err.Error(), "jobs.")) {
					t.Fatalf("valid=%t validation=%v", valid, err)
				}
			})
		}
	}
}

func TestJobRetryConfigurationLoadAndReport(t *testing.T) {
	clearRuntimeEnvironment(t)
	path := filepath.Join(t.TempDir(), "jobs.json")
	if err := os.WriteFile(path, []byte(`{"jobs":{"maxAttempts":7,"retryCeiling":"5.5s"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFor(path, RoleWorker)
	if err != nil || c.Jobs.MaxAttempts != 7 || c.Jobs.RetryCeiling.Duration != 5500*time.Millisecond {
		t.Fatalf("JSON configuration: %+v %v", c.Jobs, err)
	}
	t.Setenv("CATALOG_JOBS_MAX_ATTEMPTS", "9")
	t.Setenv("CATALOG_JOBS_RETRY_CEILING", "12.25s")
	c, err = LoadFor(path, RoleWorker)
	if err != nil || c.Jobs.MaxAttempts != 9 || c.Jobs.RetryCeiling.Duration != 12250*time.Millisecond || c.Report().Jobs != c.Jobs {
		t.Fatalf("override/report: %+v %v", c.Jobs, err)
	}
	for _, pair := range [][2]string{{"CATALOG_JOBS_MAX_ATTEMPTS", ""}, {"CATALOG_JOBS_MAX_ATTEMPTS", "0"}, {"CATALOG_JOBS_MAX_ATTEMPTS", "1.5"}, {"CATALOG_JOBS_RETRY_CEILING", ""}, {"CATALOG_JOBS_RETRY_CEILING", "4s"}, {"CATALOG_JOBS_RETRY_CEILING", "3600"}} {
		t.Run(pair[0]+"="+pair[1], func(t *testing.T) {
			t.Setenv(pair[0], pair[1])
			if _, err := LoadFor(path, RoleWorker); err == nil {
				t.Fatal("invalid Job override accepted")
			}
		})
	}
	for _, raw := range []string{`{"jobs":{"unknown":1}}`, `{"jobs":{"maxAttempts":7,"maxAttempts":8}}`, `{"jobs":{"retryCeiling":5}}`, `{"jobs":{"maxAttempts":null}}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFor(path, RoleWorker); err == nil {
			t.Fatalf("invalid Job JSON accepted: %s", raw)
		}
	}
}
