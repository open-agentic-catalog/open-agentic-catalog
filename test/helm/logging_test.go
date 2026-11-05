//go:build helm

package helm_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestLoggingConfigurationDelivery(t *testing.T) {
	args := []string{"--values", filepath.Join(chartPath(t), "ci/ui-values.yaml")}
	baseline := renderChart(t, args...)
	for _, tc := range []struct {
		name, overrides             string
		backendLevel, backendFormat string
		uiLevel, uiFormat           string
	}{
		{"defaults", "", "info", "text", "info", "text"},
		{"backend level", "configEnv.CATALOG_LOG_LEVEL=warn", "warn", "text", "info", "text"},
		{"backend format", "configEnv.CATALOG_LOG_FORMAT=json", "info", "json", "info", "text"},
		{"UI level", "ui.config.logLevel=debug", "info", "text", "debug", "text"},
		{"UI format", "ui.config.logFormat=json", "info", "text", "info", "json"},
		{"independent settings", "configEnv.CATALOG_LOG_LEVEL=warn,configEnv.CATALOG_LOG_FORMAT=json,ui.config.logLevel=error", "warn", "json", "error", "text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := baseline
			if tc.overrides != "" {
				objects = renderChart(t, append(args, "--set-string", tc.overrides)...)
			}
			validateRenderedConfiguration(t, objects)
			for _, component := range []string{"configuration", "migration"} {
				data := mapAt(t, oneObject(t, objects, "ConfigMap", component), "data")
				if data["CATALOG_LOG_LEVEL"] != tc.backendLevel || data["CATALOG_LOG_FORMAT"] != tc.backendFormat {
					t.Fatalf("%s logging: %v", component, data)
				}
			}
			var settings map[string]any
			raw := stringAt(t, oneObject(t, objects, "ConfigMap", "ui"), "data", "config.json")
			if err := json.Unmarshal([]byte(raw), &settings); err != nil {
				t.Fatal(err)
			}
			if settings["logLevel"] != tc.uiLevel || settings["logFormat"] != tc.uiFormat {
				t.Fatalf("UI logging: %v", settings)
			}
			for _, role := range []string{"api", "worker", "ui"} {
				key := "checksum/config"
				changed := tc.backendLevel != "info" || tc.backendFormat != "text"
				if role == "ui" {
					key = "checksum/ui-config"
					changed = tc.uiLevel != "info" || tc.uiFormat != "text"
				}
				before := stringAt(t, oneObject(t, baseline, "Deployment", role), "spec", "template", "metadata", "annotations", key)
				after := stringAt(t, oneObject(t, objects, "Deployment", role), "spec", "template", "metadata", "annotations", key)
				if (before != after) != changed {
					t.Fatalf("%s rollout checksum changed = %t, want %t", role, before != after, changed)
				}
			}
		})
	}
	for _, path := range []string{"configEnv.CATALOG_LOG_LEVEL", "configEnv.CATALOG_LOG_FORMAT", "ui.config.logLevel", "ui.config.logFormat"} {
		for _, value := range []string{"", "invalid"} {
			if _, err := helm(t, "template", testRelease, chartPath(t), "--set-string", path+"="+value); err == nil {
				t.Fatalf("accepted %s=%s", path, value)
			}
		}
	}
}
