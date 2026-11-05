//go:build helm

package helm_test

import (
	"reflect"
	"testing"
)

func TestListingMediaConfigurationIsExactAndRollsRuntimes(t *testing.T) {
	baseline := renderChart(t)
	const value = `{"allowedOrigins":[{"origin":"https://media.internal.example","networkProfile":"internal"}],"limits":{"maxScreenshots":2}}`
	objects := renderChart(t, "--is-upgrade", "--set-literal", "configEnv.CATALOG_LISTING_MEDIA="+value)
	if got := stringAt(t, oneObject(t, objects, "ConfigMap", "configuration"), "data", "CATALOG_LISTING_MEDIA"); got != value {
		t.Fatalf("media JSON changed: %q", got)
	}
	if got := stringAt(t, oneObject(t, baseline, "ConfigMap", "configuration"), "data", "CATALOG_LISTING_MEDIA"); got != `{"allowedOrigins":[]}` {
		t.Fatalf("media origins default must deny all: %q", got)
	}
	for _, role := range []string{"api", "worker"} {
		old := stringAt(t, oneObject(t, baseline, "Deployment", role), "spec", "template", "metadata", "annotations", "checksum/config")
		newChecksum := stringAt(t, oneObject(t, objects, "Deployment", role), "spec", "template", "metadata", "annotations", "checksum/config")
		if old == newChecksum {
			t.Fatalf("%s media policy change did not roll runtime", role)
		}
	}
	for _, kind := range []string{"ConfigMap", "Job"} {
		if !reflect.DeepEqual(oneObject(t, baseline, kind, "migration"), oneObject(t, objects, kind, "migration")) {
			t.Fatalf("media changed operator %s", kind)
		}
	}
}
