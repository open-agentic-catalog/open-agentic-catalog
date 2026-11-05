//go:build helm

package helm_test

import (
	"reflect"
	"testing"
)

func TestServiceActivityDisclosureDefaultsOffAndUsesExplicitInput(t *testing.T) {
	before := renderChart(t)
	after := renderChart(t, "--set-string", "configEnv.CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED=true")
	for _, component := range []string{"configuration"} {
		defaults := mapAt(t, oneObject(t, before, "ConfigMap", component), "data")
		enabled := mapAt(t, oneObject(t, after, "ConfigMap", component), "data")
		if defaults["CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED"] != "false" || enabled["CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED"] != "true" {
			t.Fatalf("%s activity disclosure must default off and preserve explicit activation", component)
		}
	}
	if !reflect.DeepEqual(oneObject(t, before, "ConfigMap", "migration"), oneObject(t, after, "ConfigMap", "migration")) {
		t.Fatal("API disclosure setting changed migration configuration")
	}
	validateRenderedConfiguration(t, after)
}
