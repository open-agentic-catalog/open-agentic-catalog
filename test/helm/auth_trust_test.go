//go:build helm

package helm_test

import "testing"

func TestOIDCTrustMountIsAPIOnly(t *testing.T) {
	baseline := renderChart(t)
	objects := renderChart(t, "--set-string", "auth.trustBundleConfigMap=issuer-roots-v1")
	for _, role := range []string{"api", "worker", "migration"} {
		kind := "Deployment"
		if role == "migration" {
			kind = "Job"
		}
		resource := oneObject(t, objects, kind, role)
		pod := mapAt(t, resource, "spec", "template", "spec")
		container := asMap(t, asSlice(t, pod["containers"])[0])
		entry, present := environmentEntry(t, container, "CATALOG_AUTH_TRUST_BUNDLE_FILE")
		if present != (role == "api") {
			t.Fatalf("%s trust environment presence = %t", role, present)
		}
		mountFound, volumeFound := false, false
		for _, item := range asSlice(t, container["volumeMounts"]) {
			mount := asMap(t, item)
			if stringAt(t, mount, "name") == "oidc-trust" {
				mountFound = true
				if mount["readOnly"] != true || stringAt(t, mount, "mountPath") != "/etc/open-agentic-catalog/oidc/trust" {
					t.Fatal("OIDC trust mount is not fixed and read-only")
				}
			}
		}
		for _, item := range asSlice(t, pod["volumes"]) {
			volume := asMap(t, item)
			if stringAt(t, volume, "name") == "oidc-trust" {
				volumeFound = true
				if stringAt(t, volume, "configMap", "name") != "issuer-roots-v1" {
					t.Fatal("OIDC trust references the wrong ConfigMap")
				}
				items := sliceAt(t, volume, "configMap", "items")
				if len(items) != 1 || stringAt(t, asMap(t, items[0]), "key") != "ca.pem" || stringAt(t, asMap(t, items[0]), "path") != "ca.pem" {
					t.Fatal("OIDC trust must project only ca.pem")
				}
			}
		}
		if mountFound != (role == "api") || volumeFound != (role == "api") {
			t.Fatalf("%s trust mount = %t, volume = %t", role, mountFound, volumeFound)
		}
		if role == "api" && stringAt(t, entry, "value") != "/etc/open-agentic-catalog/oidc/trust/ca.pem" {
			t.Fatal("API trust path and mount disagree")
		}
		if role != "migration" {
			before := stringAt(t, oneObject(t, baseline, kind, role), "spec", "template", "metadata", "annotations", "checksum/config")
			after := stringAt(t, resource, "spec", "template", "metadata", "annotations", "checksum/config")
			if (before != after) != (role == "api") {
				t.Fatalf("%s configuration digest did not preserve API-only trust rollout", role)
			}
		}
	}
	if _, found := mapAt(t, oneObject(t, objects, "ConfigMap", "configuration"), "data")["CATALOG_AUTH_TRUST_BUNDLE_FILE"]; found {
		t.Fatal("shared configuration must not distribute the API-only trust path")
	}
	validateRenderedConfiguration(t, objects)
}

func TestSPIREValuesSelectOnlyTheConfiguredIssuer(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		issuer    string
		privateCA bool
	}{
		{name: "default", issuer: "https://identity.example.com"},
		{name: "workload issuer", args: []string{"--values", chartPath(t, "examples", "spire-values.yaml")}, issuer: "https://identity.test:8447", privateCA: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := renderChart(t, tc.args...)
			validateRenderedConfiguration(t, objects)
			data := mapAt(t, oneObject(t, objects, "ConfigMap", "configuration"), "data")
			if data["CATALOG_AUTH_MODE"] != "oidc" || data["CATALOG_AUTH_ISSUER"] != tc.issuer {
				t.Fatalf("unexpected authentication mode or issuer: %v, %v", data["CATALOG_AUTH_MODE"], data["CATALOG_AUTH_ISSUER"])
			}
			api := oneObject(t, objects, "Deployment", "api")
			mounted := false
			for _, item := range sliceAt(t, api, "spec", "template", "spec", "volumes") {
				volume := asMap(t, item)
				if volume["name"] == "oidc-trust" {
					mounted = true
					if stringAt(t, volume, "configMap", "name") != "spire-issuer-ca" {
						t.Fatal("API would load a different issuer CA")
					}
				}
			}
			if mounted != tc.privateCA {
				t.Fatalf("issuer CA mounted = %t", mounted)
			}
		})
	}
}
