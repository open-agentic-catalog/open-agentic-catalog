//go:build minikube

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointReportRequiresRealAdvancementAndExactHistory(t *testing.T) {
	for _, phase := range []string{"history", "history-missing", "history-corrupt"} {
		checks := map[string]bool{}
		for _, key := range strings.Fields(ansTrustRequiredChecks[phase]) {
			checks[key] = true
		}
		history := map[string]any{"oldSize": 3, "newSize": 4, "oldRoot": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "newRoot": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
			"historyPath": "/v1/log/checkpoint/history?fromSize=3&toSize=3&limit=1", "originalHistorySHA256": strings.Repeat("a", 64), "jobId": "00000000-0000-7000-8000-000000000001"}
		r := map[string]any{"phase": phase, "status": "pass", "checks": checks, "captures": []int{1}, "checkpoint_history": map[string]any{phase: history}}
		validate := func() error {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			return validateANSTrustPhase(raw, phase)
		}
		if err := validate(); err != nil {
			t.Fatal(err)
		}
		for key, invalid := range map[string]any{"oldSize": 0, "newSize": 3, "oldRoot": "invalid", "newRoot": history["oldRoot"], "historyPath": "/v1/log/checkpoint/history?limit=100", "originalHistorySHA256": "", "jobId": "unbound"} {
			saved := history[key]
			history[key] = invalid
			if validate() == nil {
				t.Fatal("invalid checkpoint evidence admitted", phase, key)
			}
			history[key] = saved
		}
		delete(r, "checkpoint_history")
		if validate() == nil {
			t.Fatal("missing history evidence admitted")
		}
	}
}

func TestTrustPhaseRejectsIncompleteAndFalseEvidence(t *testing.T) {
	checks := map[string]bool{}
	for _, name := range strings.Fields(ansTrustRequiredChecks["selected"]) {
		checks[name] = true
	}
	r := map[string]any{"phase": "selected", "status": "pass", "checks": checks, "captures": []int{1}}
	encode := func() []byte {
		t.Helper()
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if err := validateANSTrustPhase(encode(), "selected"); err != nil {
		t.Fatal(err)
	}
	for _, name := range strings.Fields(ansTrustRequiredChecks["selected"]) {
		delete(checks, name)
		if validateANSTrustPhase(encode(), "selected") == nil {
			t.Fatal("accepted missing assertion", name)
		}
		checks[name] = true
	}
	for _, key := range []string{"phase", "status", "checks", "captures"} {
		saved := r[key]
		delete(r, key)
		if validateANSTrustPhase(encode(), "selected") == nil {
			t.Fatal("accepted missing", key)
		}
		r[key] = saved
	}
	r["checks"] = map[string]bool{"v1": true, "v2": false}
	if validateANSTrustPhase(encode(), "selected") == nil {
		t.Fatal("accepted false check")
	}
	r["checks"] = map[string]bool{"v1": true}
	r["error"] = "failed"
	if validateANSTrustPhase(encode(), "selected") == nil {
		t.Fatal("accepted contradictory evidence")
	}
}

func TestTrustSQLAndConfigAreExplicitlyBound(t *testing.T) {
	for _, raw := range []string{`{}`, `{"source_id":"'; DROP TABLE jobs;--"}`, `null`} {
		if _, err := ansTrustPersistenceQuery([]byte(raw)); err == nil {
			t.Fatal("unbounded SQL input")
		}
	}
	query, err := ansTrustPersistenceQuery([]byte(`{"source_id":"00000000-0000-7000-8000-000000000011"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"j.tenant_id='" + primaryTenantID + "'", "a.tenant_id=j.tenant_id", "source.trust.completed", "j.result->>'softwareTrust'"} {
		if !strings.Contains(query, expected) {
			t.Fatal("missing SQL binding", expected)
		}
	}
	cfg := ansTrustConfiguration(true, []string{"tl.pem", "other.pem"}, "5s")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{ansOrigin, "functional-fixture", "/trust-verification/other.pem", "5s"} {
		if !strings.Contains(string(raw), expected) {
			t.Fatal("missing configuration", expected)
		}
	}
	if ansTrustConfiguration(false, []string{"tl.pem"}, "1h")["enabled"] != false {
		t.Fatal("disable ignored")
	}
}

func TestTrustHelmValuesRespectAttemptLeaseMargin(t *testing.T) {
	h := &harness{temporary: t.TempDir(), suffix: "abcdef", application: "open-agentic-catalog:minikube-abcdef", options: options{scenario: "ans"}, ans: &ansState{trustConfig: ansTrustConfiguration(true, []string{"tl.pem"}, "1h")}}
	path, err := h.writeHelmValues("trust-test", "catalog_api")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	env, ok := values["configEnv"].(map[string]any)
	if !ok {
		t.Fatal("missing generated runtime settings")
	}
	if env["CATALOG_WORKER_LEASE_DURATION"] != "30s" || env["CATALOG_TRUST_VERIFICATION"] == nil || values["trustVerification"] == nil {
		t.Fatal("trust lease/config/key delivery mismatch")
	}
}

func TestEverySelectedTrustPhaseHasAnAssertionInventory(t *testing.T) {
	for _, phase := range append(append([]string{}, ansTrustPhases...), "restart-complete", "finder-complete", "revocation-complete") {
		if len(strings.Fields(ansTrustRequiredChecks[phase])) == 0 {
			t.Fatal("missing phase assertions", phase)
		}
	}
	if validateANSTrustPhase([]byte(`{"phase":"unknown","status":"pass","checks":{"arbitrary":true},"captures":[1]}`), "unknown") == nil {
		t.Fatal("unselected phase accepted")
	}
}
