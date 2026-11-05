//go:build helm

package helm_test

import (
	"reflect"
	"testing"
)

func TestDatabasePoolConfigurationDelivery(t *testing.T) {
	defaults := renderChart(t)
	settings := []struct{ name, defaultValue, configured string }{
		{"CATALOG_DATABASE_MAX_CONN_LIFETIME", "30m", "45m"},
		{"CATALOG_DATABASE_MAX_CONN_IDLE_TIME", "5m", "3m"},
		{"CATALOG_DATABASE_HEALTH_CHECK_PERIOD", "30s", "15s"},
	}
	args := make([]string, 0, 6)
	for _, setting := range settings {
		args = append(args, "--set-string", "configEnv."+setting.name+"="+setting.configured)
	}
	changed := renderChart(t, args...)
	for _, component := range []string{"configuration", "migration"} {
		before := mapAt(t, oneObject(t, defaults, "ConfigMap", component), "data")
		after := mapAt(t, oneObject(t, changed, "ConfigMap", component), "data")
		for _, setting := range settings {
			if before[setting.name] != setting.defaultValue || after[setting.name] != setting.configured {
				t.Fatalf("%s %s: default=%v configured=%v", component, setting.name, before[setting.name], after[setting.name])
			}
		}
	}
	validateRenderedConfiguration(t, changed)
	for _, component := range []string{"api", "worker"} {
		before := mapAt(t, oneObject(t, defaults, "Deployment", component), "spec", "template", "metadata", "annotations")
		after := mapAt(t, oneObject(t, changed, "Deployment", component), "spec", "template", "metadata", "annotations")
		if reflect.DeepEqual(before, after) {
			t.Fatalf("pool change did not roll %s", component)
		}
	}
}
