//go:build minikube

package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestPeerTrustSelectionAndValues(t *testing.T) {
	for _, value := range []string{"", "true", "ON", " on "} {
		if _, err := peerTrustSelection(value); err == nil {
			t.Fatal("invalid selection admitted", value)
		}
	}
	for _, value := range []string{"on", "off"} {
		enabled, err := peerTrustSelection(value)
		if err != nil || enabled != (value == "on") {
			t.Fatal(enabled, err)
		}
	}
	for _, enabled := range []bool{false, true} {
		h := &harness{temporary: t.TempDir(), application: "open-agentic-catalog:owned-fixture", options: options{scenario: "gateway"}}
		if enabled {
			h.peerTrustConfig = map[string]any{"enabled": true}
		}
		path, err := h.writeHelmValues("peer-test", "catalog_api")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- generated under this test's private directory.
		if err != nil {
			t.Fatal(err)
		}
		var values struct {
			Config map[string]string `json:"configEnv"`
			Trust  map[string]string `json:"trustVerification"`
		}
		if json.Unmarshal(raw, &values) != nil {
			t.Fatal("invalid values")
		}
		if (values.Trust["publicKeysConfigMap"] == "peer-public-keys") != enabled || (values.Config["CATALOG_TRUST_VERIFICATION"] != "") != enabled {
			t.Fatal("test selection escaped its scope")
		}
		if enabled && values.Config["CATALOG_WORKER_LEASE_DURATION"] != "30s" {
			t.Fatal("missing lease margin")
		}
	}
}

func TestPeerTrustOwnerSQLRejectsUnsafeBindings(t *testing.T) {
	source := "00000000-0000-7000-8000-000000000001"
	for _, job := range []string{"", "00000000-0000-7000-8000-000000000002"} {
		query, err := peerTrustPersistenceQuery(source, job)
		if err != nil || !strings.Contains(query, "source.trust_verify") || !strings.Contains(query, primaryTenantID) {
			t.Fatal(query, err)
		}
	}
	for _, pair := range [][2]string{{"", ""}, {"'; DROP TABLE jobs;--", ""}, {source, "invalid"}} {
		if _, err := peerTrustPersistenceQuery(pair[0], pair[1]); err == nil {
			t.Fatal("unsafe SQL admitted")
		}
	}
}

func TestPeerTrustDirectoryObserverRequiresSelection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		h := &harness{options: options{interopTrust: enabled}, agntcy: &agntcyState{router: "owned:router"}}
		for _, schema := range []bool{false, true} {
			deployment := h.agntcyRouterObjects(schema)[0]
			spec := directoryMap(t, directoryMap(t, directoryMap(t, deployment["spec"])["template"])["spec"])
			containers, ok := spec["containers"].([]any)
			if !ok || len(containers) != 1 {
				t.Fatal("unexpected router container inventory")
			}
			container := directoryMap(t, containers[0])
			arguments, ok := container["args"].([]string)
			if !ok || slices.Contains(arguments, "--record-requests") != (enabled && !schema) {
				t.Fatal("request observer escaped its explicit selection")
			}
		}
	}
}

func TestPeerEvidenceLogsRequirePositiveControl(t *testing.T) {
	valid := `GET /.well-known/ai-catalog.json HTTP/1.1" 200`
	if err := noPeerEvidenceRequests(valid); err != nil {
		t.Fatal(err)
	}
	for _, logs := range []string{"", valid + " GET /checkpoint HTTP/1.1", valid + " GET /v1/agents/id/receipt HTTP/1.1", valid + " GET /v1/agents/id/status-token HTTP/1.1", valid + " GET /v1/log/checkpoint/history?fromSize=1 HTTP/1.1"} {
		if noPeerEvidenceRequests(logs) == nil {
			t.Fatal("incomplete or unexpected calls admitted", logs)
		}
	}
}
