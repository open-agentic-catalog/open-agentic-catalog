//go:build helm

package helm_test

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestPodResolverOptionsReachOnlyPodSpecifications(t *testing.T) {
	args := make([]string, 0, 6)
	args = append(args, "--values", filepath.Join(chartPath(t), "ci/ui-values.yaml"), "--values", filepath.Join(chartPath(t), "ci/pgbouncer.yaml"))
	baseline := renderChart(t, args...)
	options := `[{"name":"use-vc"},{"name":"ndots","value":"2"}]`
	configured := renderChart(t, append(args, "--set-json", "podDNSOptions="+options)...)
	for _, tc := range []struct{ kind, role string }{{"Deployment", "api"}, {"Deployment", "worker"}, {"Deployment", "ui"}, {"Deployment", "pgbouncer"}, {"Job", "migration"}} {
		before := mapAt(t, oneObject(t, baseline, tc.kind, tc.role), "spec", "template", "spec")
		if _, present := before["dnsConfig"]; present {
			t.Fatalf("default %s adds resolver options", tc.role)
		}
		after := mapAt(t, oneObject(t, configured, tc.kind, tc.role), "spec", "template", "spec")
		options := sliceAt(t, after, "dnsConfig", "options")
		if len(options) != 2 || stringAt(t, options[0], "name") != "use-vc" || len(asMap(t, options[0])) != 1 || stringAt(t, options[1], "name") != "ndots" || stringAt(t, options[1], "value") != "2" {
			t.Fatalf("%s lost DNS options: %#v", tc.role, options)
		}

		delete(after, "dnsConfig")
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("resolver options changed unrelated %s Pod fields", tc.role)
		}
	}
	for _, value := range []string{`{}`, `[{}]`, `[{"name":""}]`, `[{"name":"use-vc","unknown":true}]`, `[{"name":"timeout","value":1}]`, `[{"name":"use-vc"},{"name":"use-vc"}]`} {
		if _, err := helm(t, "template", testRelease, chartPath(t), "--set-json", "podDNSOptions="+value); err == nil {
			t.Fatalf("accepted invalid DNS options %s", value)
		}
	}
}
