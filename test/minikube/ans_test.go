//go:build minikube

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestANSDevelopmentConfigurationKeepsProvidersLocal(t *testing.T) {
	ra := ansConfiguration("ra", []string{"ra-secret", "tl-secret"}, "")
	dns, dnsOK := ra["dns"].(map[string]string)
	vlei, vleiOK := ra["vlei"].(map[string]string)
	if !dnsOK || !vleiOK || dns["type"] != "noop" || vlei["type"] != "noop" {
		t.Fatal("development checks selected a real provider")
	}
	tl := ansConfiguration("tl", []string{"ra-secret", "tl-secret"}, "native-public-key")
	keys, keysOK := tl["producerKeys"].([]any)
	if !keysOK || len(keys) != 1 {
		t.Fatal("missing native producer key")
	}
	producer, producerOK := keys[0].(map[string]string)
	if !producerOK || producer["publicKeyPem"] != "native-public-key" {
		t.Fatal("native producer identity lost")
	}
	finder := ansConfiguration("finder", nil, "")
	search, searchOK := finder["search"].(map[string]any)
	if !searchOK {
		t.Fatal("missing native Search settings")
	}
	if search["source-url"] != ansOrigin || search["max-page-size"] != 1 || search["default-page-size"] != 1 {
		t.Fatal("origin or strict paging not bound")
	}
	for _, cfg := range []map[string]any{ra, tl, finder} {
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, external := range []string{"8.8.8.8", "letsencrypt", "https://api.", "acme-v02"} {
			if strings.Contains(string(raw), external) {
				t.Fatal("external provider configured")
			}
		}
	}
}
func TestANSReportRejectsIncompleteQualification(t *testing.T) {
	phases := map[string]any{}
	for _, name := range []string{"registrations", "feed", "search", "filters", "explore", "negative", "schemas", "official", "tls", "shared_identity"} {
		phases[name] = []string{"evidence"}
	}
	report := map[string]any{"status": "pass", "counts": map[string]int{"registered": 4, "discoverable": 3}, "phases": phases}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateANSQualification(raw); err != nil {
		t.Fatal(err)
	}
	for name := range phases {
		saved := phases[name]
		delete(phases, name)
		raw, err = json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if validateANSQualification(raw) == nil {
			t.Fatal("accepted missing phase", name)
		}
		phases[name] = saved
	}
	report["status"] = "fail"
	raw, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if validateANSQualification(raw) == nil {
		t.Fatal("accepted failed native controls")
	}
	report["status"] = "pass"
	report["counts"] = map[string]int{"registered": 4, "discoverable": 0}
	raw, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if validateANSQualification(raw) == nil {
		t.Fatal("accepted empty discovery")
	}
}
func TestANSManagedBuildAndRedaction(t *testing.T) {
	h := &harness{ans: &ansState{report: ansReport{Build: map[string]string{"version": "1.2.3"}, Workloads: map[string]string{}}, secrets: []string{"credential"}}}
	if h.managedBuild()["version"] != "1.2.3" {
		t.Fatal("unmanaged ANS Catalog build")
	}
	h.setManagedCatalogValues("hash")
	h.managedWorkloads()["api"] = "id"
	if h.ans.report.CatalogValues != "hash" || h.ans.report.Workloads["api"] != "id" {
		t.Fatal("ANS managed evidence lost")
	}
	if strings.Contains(h.redactDiagnostic("credential"), "credential") {
		t.Fatal("ANS secret leaked")
	}
}

func TestANSPersistenceRejectsMissingBindingsAndSQLInput(t *testing.T) {
	raw := []byte(`{"state":{"source_id":"00000000-0000-7000-8000-000000000011","inspection_id":"00000000-0000-7000-8000-000000000012"}}`)
	query, err := ansPersistenceQuery(raw)
	if err != nil || !strings.Contains(query, "s.tenant_id='"+primaryTenantID+"'") || !strings.Contains(query, "j.tenant_id=s.tenant_id") {
		t.Fatal("lost tenant/SQL binding", err)
	}
	for _, bad := range []string{`{}`, `{"state":{"source_id":"';DROP TABLE jobs;--"}}`, `null`, `broken`} {
		if _, err := ansPersistenceQuery([]byte(bad)); err == nil {
			t.Fatal("accepted invalid SQL evidence")
		}
	}
	evidence := map[string]any{}
	for _, key := range []string{"source_bound", "inspection_bound", "approval_bound", "audience_bound", "inspection_audit", "connection_audit", "no_snapshots", "no_observations", "no_sync_jobs"} {
		evidence[key] = true
	}
	if err := ansPersistenceMatches(evidence); err != nil {
		t.Fatal(err)
	}
	for key := range evidence {
		evidence[key] = false
		if ansPersistenceMatches(evidence) == nil {
			t.Fatal("accepted false binding", key)
		}
		evidence[key] = true
	}
}

func TestANSPhaseRequiresNativeAndCatalogEvidence(t *testing.T) {
	report := map[string]any{"phase": "discovery", "status": "pass", "evidence": map[string]bool{"matched": true}, "captures": []int{1}, "native_captures": []int{1}}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateANSPhase(raw, "discovery"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"phase", "status", "evidence", "captures", "native_captures"} {
		saved := report[key]
		delete(report, key)
		raw, err = json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if validateANSPhase(raw, "discovery") == nil {
			t.Fatal("accepted incomplete phase", key)
		}
		report[key] = saved
	}
}
