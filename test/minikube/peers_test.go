//go:build minikube

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIndependentCatalogNamespaceAndInputs(t *testing.T) {
	primary := &harness{temporary: t.TempDir(), application: "catalog:test", options: options{scenario: "peers"}, secrets: fixtureSecrets{ownerPassword: "primary-owner", apiPassword: "primary-api", workerPassword: "primary-worker", cursorKey: "primary-cursor"}}
	secondary, err := primary.secondaryCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if secondary.deploymentTool != filepath.Join(primary.temporary, "catalog-deploy") {
		t.Fatal("secondary Catalog lost its owned deployment executable")
	}
	if primary.catalogNamespace() != namespace || secondary.catalogNamespace() != catalogPeerNamespace {
		t.Fatal("Catalog namespace ownership differs")
	}
	if secondary.secrets.ownerPassword == primary.secrets.ownerPassword || secondary.secrets.apiPassword == primary.secrets.apiPassword || secondary.secrets.workerPassword == primary.secrets.workerPassword || secondary.secrets.cursorKey == primary.secrets.cursorKey {
		t.Fatal("Catalogs share injected credentials")
	}
	if secondary.secrets.ownerPassword == "" || secondary.secrets.apiPassword == secondary.secrets.workerPassword || secondary.secrets.cursorKey == "" {
		t.Fatal("independent credentials missing or shared")
	}
	for _, h := range []*harness{primary, secondary} {
		for _, object := range h.dependencyObjects() {
			metadata, ok := object["metadata"].(map[string]any)
			if !ok || metadata["namespace"] != h.catalogNamespace() {
				t.Fatal("dependency escapes its Catalog namespace")
			}
		}
		path, err := h.writeHelmValues("independent", "catalog_api")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- this test owns the generated path under t.TempDir.
		if err != nil {
			t.Fatal(err)
		}
		var values struct {
			Config map[string]string `json:"configEnv"`
		}
		if json.Unmarshal(raw, &values) != nil {
			t.Fatal("invalid fixture JSON")
		}
		side := "b"
		if h == secondary {
			side = "a"
		}
		if values.Config["CATALOG_PUBLIC_BASE_URL"] != peerOrigin(side) || values.Config["CATALOG_JOBS_MAX_ATTEMPTS"] != "7" {
			t.Fatal("peer values select wrong origin or retry budget")
		}
		var hosts []map[string]string
		if json.Unmarshal([]byte(values.Config["CATALOG_DISCOVERY_HOSTS"]), &hosts) != nil || len(hosts) != 2 {
			t.Fatal("peer discovery hosts missing")
		}
		if hosts[0]["host"] != peerHost(side) || hosts[0]["mode"] != "anonymous" || hosts[1]["host"] != "catalog."+h.catalogNamespace()+".svc.cluster.local" || hosts[1]["mode"] != "authenticated" {
			t.Fatal("peer publication and authenticated discovery audiences differ")
		}
	}
}

func TestCatalogPeerEvidenceRejectsIncompleteSuccess(t *testing.T) {
	positive := catalogPeerEvidence{Phase: "replacement", Checks: []string{"both-catalogs-retain-native-and-synchronized-state-after-replacement"}, Kinds: []string{"agent", "mcp_server", "skill"}, Catalogs: 2}
	raw, err := json.Marshal(positive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCatalogPeerEvidence(raw, "replacement"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*catalogPeerEvidence){
		func(v *catalogPeerEvidence) { v.Checks = nil }, func(v *catalogPeerEvidence) { v.Checks = append(v.Checks, v.Checks[0]) }, func(v *catalogPeerEvidence) { v.Kinds = []string{"agent"} }, func(v *catalogPeerEvidence) { v.Catalogs = 1 }, func(v *catalogPeerEvidence) { v.Phase = "lifecycle" },
	} {
		value := positive
		change(&value)
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeCatalogPeerEvidence(raw, "replacement"); err == nil {
			t.Fatal("incomplete peer qualification admitted")
		}
	}
	if _, err := decodeCatalogPeerEvidence(raw, "unknown"); err == nil {
		t.Fatal("unknown phase admitted")
	}
}

func TestCatalogPeerCNISelection(t *testing.T) {
	for _, scenario := range []string{"basic", "ui", "replicas", "monitoring", "tracing", "peers"} {
		if (options{scenario: scenario}).clusterCNI() != "bridge" {
			t.Fatalf("%s requires the ordinary bridge fixture", scenario)
		}
	}
	for _, scenario := range []string{"topology", "semantic", "pgbouncer", "agntcy", "gateway", "ans"} {
		if (options{scenario: scenario}).clusterCNI() != "calico" {
			t.Fatalf("%s lost Calico qualification", scenario)
		}
	}
}
