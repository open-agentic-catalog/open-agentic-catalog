//go:build helm

package helm_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func poolerValues(t *testing.T) []string {
	t.Helper()
	return []string{"-f", filepath.Join(chartPath(t), "ci", "pgbouncer.yaml")}
}

func TestPgBouncerIsolationAndConfiguration(t *testing.T) {
	baseline := renderChart(t)
	for _, resource := range baseline {
		labels, _ := mapAt(t, resource, "metadata")["labels"].(map[string]any)
		if labels["app.kubernetes.io/component"] == "pgbouncer" {
			t.Fatal("disabled pooler rendered resources")
		}
	}
	objects := renderChart(t, poolerValues(t)...)
	pod := mapAt(t, oneObject(t, objects, "Deployment", "pgbouncer"), "spec", "template", "spec")
	deployment := oneObject(t, objects, "Deployment", "pgbouncer")
	if mapAt(t, deployment, "spec")["replicas"] != 1 || stringAt(t, deployment, "spec", "strategy", "type") != "Recreate" {
		t.Fatal("single pooler replacement contract")
	}
	container := asMap(t, asSlice(t, pod["containers"])[0])
	if !reflect.DeepEqual(asSlice(t, container["command"]), []any{"/usr/bin/pgbouncer"}) || pod["automountServiceAccountToken"] != false || mapAt(t, container, "securityContext")["readOnlyRootFilesystem"] != true {
		t.Fatal("unsafe pooler runtime")
	}
	if len(asSlice(t, container["volumeMounts"])) != 4 || len(asSlice(t, pod["volumes"])) != 4 {
		t.Fatal("pooler input boundary")
	}
	service := oneObject(t, objects, "Service", "pgbouncer")
	if stringAt(t, service, "spec", "type") != "ClusterIP" {
		t.Fatal("pooler must remain private")
	}
	ini := stringAt(t, oneObject(t, objects, "ConfigMap", "pgbouncer"), "data", "pgbouncer.ini")
	for _, required := range []string{"pool_mode = session", "server_tls_sslmode = verify-full", "client_tls_sslmode = require", "auth_type = scram-sha-256", "reserve_pool_size = 0", "server_reset_query = DISCARD ALL", "catalog_api = pool_size=60", "catalog_worker = pool_size=60"} {
		if !strings.Contains(ini, required) {
			t.Fatalf("missing pooler contract %q", required)
		}
	}
	for _, forbidden := range []string{"user=", "auth_user", "auth_query", "password", "ignore_startup_parameters", "catalog_owner", "[peers]", "* ="} {
		if strings.Contains(ini, forbidden) {
			t.Fatalf("unsafe pooler configuration %q", forbidden)
		}
	}
	// Enabling does not rewrite a credential reference or make owner hooks depend
	// on this Deployment (which is installed only after the pre-install hook).
	for _, role := range []string{"api", "worker", "migration"} {
		kind := "Deployment"
		if role == "migration" {
			kind = "Job"
		}
		before := asMap(t, sliceAt(t, oneObject(t, baseline, kind, role), "spec", "template", "spec", "containers")[0])
		after := asMap(t, sliceAt(t, oneObject(t, objects, kind, role), "spec", "template", "spec", "containers")[0])
		old, _ := environmentEntry(t, before, "CATALOG_DATABASE_DSN")
		current, _ := environmentEntry(t, after, "CATALOG_DATABASE_DSN")
		if !reflect.DeepEqual(old, current) {
			t.Fatal("enabling pooler rewrote a database Secret")
		}
		found := false
		for _, raw := range asSlice(t, after["volumeMounts"]) {
			mount := asMap(t, raw)
			if mount["name"] == "database-trust" {
				found = mount["mountPath"] == "/etc/open-agentic-catalog/database" && mount["readOnly"] == true
			}
		}
		if !found {
			t.Fatalf("missing read-only database trust on %s", role)
		}
	}
	policy := oneObject(t, objects, "NetworkPolicy", "pgbouncer")
	if len(sliceAt(t, policy, "spec", "ingress")) != 1 || len(sliceAt(t, policy, "spec", "egress")) != 2 {
		t.Fatal("pooler ingress/egress not bounded")
	}
	changed := renderChart(t, append(poolerValues(t), "--set", "pgbouncer.apiPoolSize=61")...)
	before := stringAt(t, deployment, "spec", "template", "metadata", "annotations", "checksum/config")
	after := stringAt(t, oneObject(t, changed, "Deployment", "pgbouncer"), "spec", "template", "metadata", "annotations", "checksum/config")
	if before == after {
		t.Fatal("pooler config did not roll")
	}
	reapplied := renderChart(t, poolerValues(t)...)
	if !reflect.DeepEqual(objects, reapplied) {
		t.Fatal("no-op values changed rendered objects")
	}
	validateRenderedConfiguration(t, objects)
}

func TestPgBouncerUnsafeInputs(t *testing.T) {
	for _, input := range []string{"pgbouncer.poolMode=transaction", "pgbouncer.replicas=2", "pgbouncer.authSecret=", "pgbouncer.tlsSecret=", "pgbouncer.upstreamCAConfigMap=", "pgbouncer.upstream.host=", "pgbouncer.upstream.host=postgres\npassword=private", "pgbouncer.upstream.database=*", "databaseRoles.api=role-name", "pgbouncer.maxClientConnections=20", "pgbouncer.queryWaitTimeoutSeconds=0"} {
		t.Run(input, func(t *testing.T) {
			args := make([]string, 0, 16)
			args = append(args, "template", testRelease, chartPath(t))
			args = append(args, poolerValues(t)...)
			_, err := helm(t, append(args, "--set-string", input)...)
			if err == nil {
				t.Fatal("unsafe pooler input accepted")
			}
		})
	}
	_, err := helm(t, "template", testRelease, chartPath(t), "--set", "pgbouncer.enabled=true")
	if err == nil {
		t.Fatal("pooler missing required inputs accepted")
	}
}

func TestPgBouncerServiceAccountIsolation(t *testing.T) {
	for _, fullname := range []string{"catalog-custom", strings.Repeat("x", 63)} {
		for _, create := range []string{"true", "false"} {
			settings := append(poolerValues(t), "--set-string", "fullnameOverride="+fullname)
			for _, role := range []string{"api", "worker", "migration"} {
				settings = append(settings, "--set-string", "serviceAccounts."+role+".name=custom-"+role,
					"--set", "serviceAccounts."+role+".create="+create)
			}
			objects := renderChart(t, settings...)
			want := "catalog-custom-pgbouncer"
			if len(fullname) == 63 {
				want = strings.Repeat("x", 53) + "-pgbouncer"
			}
			if got := stringAt(t, oneObject(t, objects, "ServiceAccount", "pgbouncer"), "metadata", "name"); got != want {
				t.Fatalf("pooler account = %q, want %q", got, want)
			}
			for _, role := range []string{"api", "worker", "migration"} {
				t.Run(fullname+"/"+role+"/create="+create, func(t *testing.T) {
					args := append([]string{"template", testRelease, chartPath(t)}, settings...)
					args = append(args, "--set-string", "serviceAccounts."+role+".name="+want)
					output, err := helm(t, args...)
					if err == nil || !strings.Contains(output, "PgBouncer ServiceAccount must be distinct") {
						t.Fatalf("pooler/backend account collision admitted: %v: %s", err, output)
					}
					if output, err := helm(t, append(args, "--set", "pgbouncer.enabled=false")...); err != nil {
						t.Fatalf("disabled pooler reserved an account: %v: %s", err, output)
					}
				})
			}
		}
	}
}
