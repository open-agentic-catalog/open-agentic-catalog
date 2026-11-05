// Worker restart settings are independent of healthy cadence and Job budgets.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerDependencyRetryRolesAndBounds(t *testing.T) {
	d := Defaults().Worker
	if d.DependencyRetryInitialDelay.Duration != time.Second || d.DependencyRetryCeiling.Duration != 30*time.Second {
		t.Fatalf("worker retry defaults: %+v", d)
	}
	for _, role := range []Role{RoleWorker, RoleAll, RoleAPI, RoleMigration} {
		for _, tc := range []struct {
			name             string
			initial, ceiling time.Duration
			valid            bool
		}{
			{"minimum", time.Second, time.Second, true},
			{"maximum", 5 * time.Minute, 5 * time.Minute, true},
			{"fractional", 1250 * time.Millisecond, 2500 * time.Millisecond, true},
			{"zero initial", 0, time.Second, false},
			{"negative initial", -time.Second, time.Second, false},
			{"short initial", time.Second - time.Nanosecond, time.Second, false},
			{"zero ceiling", time.Second, 0, false},
			{"negative ceiling", time.Second, -time.Second, false},
			{"long ceiling", time.Second, 5*time.Minute + time.Nanosecond, false},
			{"inverted", 2 * time.Second, time.Second, false},
		} {
			t.Run(string(role)+"/"+tc.name, func(t *testing.T) {
				c := Defaults()
				c.Auth.DevelopmentTokens = map[string]DevelopmentUser{"fixture": {Subject: "fixture", TenantID: "00000000-0000-7000-8000-000000000001"}}
				c.Worker.DependencyRetryInitialDelay.Duration, c.Worker.DependencyRetryCeiling.Duration = tc.initial, tc.ceiling
				if role == RoleMigration {
					c.Database = Database{Driver: "postgres", DSN: "postgres://owner@invalid/catalog", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
				}
				err := c.ValidateFor(role)
				valid := tc.valid || role == RoleAPI || role == RoleMigration
				if valid && err != nil || !valid && (err == nil || !strings.Contains(err.Error(), "worker dependency retry")) {
					t.Fatalf("valid=%t validation=%v", valid, err)
				}
			})
		}
	}
}

func TestWorkerDependencyRetryLoadAndReport(t *testing.T) {
	clearRuntimeEnvironment(t)
	path := filepath.Join(t.TempDir(), "worker.json")
	if err := os.WriteFile(path, []byte(`{"worker":{"dependencyRetryInitialDelay":"1.25s","dependencyRetryCeiling":"2.5s"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFor(path, RoleWorker)
	if err != nil || c.Worker.DependencyRetryInitialDelay.Duration != 1250*time.Millisecond || c.Worker.DependencyRetryCeiling.Duration != 2500*time.Millisecond {
		t.Fatalf("JSON policy: %+v %v", c.Worker, err)
	}
	t.Setenv("CATALOG_WORKER_DEPENDENCY_RETRY_INITIAL_DELAY", "3.25s")
	t.Setenv("CATALOG_WORKER_DEPENDENCY_RETRY_CEILING", "4.5s")
	c, err = LoadFor(path, RoleWorker)
	if err != nil || c.Worker.DependencyRetryInitialDelay.Duration != 3250*time.Millisecond || c.Worker.DependencyRetryCeiling.Duration != 4500*time.Millisecond || c.Report().Worker != c.Worker {
		t.Fatalf("override/report: %+v %v", c.Worker, err)
	}
	for _, pair := range [][2]string{{"CATALOG_WORKER_DEPENDENCY_RETRY_INITIAL_DELAY", ""}, {"CATALOG_WORKER_DEPENDENCY_RETRY_INITIAL_DELAY", "0s"}, {"CATALOG_WORKER_DEPENDENCY_RETRY_INITIAL_DELAY", "5s"}, {"CATALOG_WORKER_DEPENDENCY_RETRY_CEILING", ""}, {"CATALOG_WORKER_DEPENDENCY_RETRY_CEILING", "3s"}, {"CATALOG_WORKER_DEPENDENCY_RETRY_CEILING", "301s"}, {"CATALOG_WORKER_DEPENDENCY_RETRY_CEILING", "30"}} {
		t.Run(pair[0]+"="+pair[1], func(t *testing.T) {
			t.Setenv(pair[0], pair[1])
			if _, err := LoadFor(path, RoleWorker); err == nil {
				t.Fatal("invalid supervisor override accepted")
			}
		})
	}
	for _, raw := range []string{`{"worker":{"dependencyRetryUnknown":"1s"}}`, `{"worker":{"dependencyRetryCeiling":"5s","dependencyRetryCeiling":"6s"}}`, `{"worker":{"dependencyRetryInitialDelay":1}}`, `{"worker":{"dependencyRetryInitialDelay":null}}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFor(path, RoleWorker); err == nil {
			t.Fatalf("invalid JSON accepted: %s", raw)
		}
	}
}
