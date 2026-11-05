package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebhookPolicyIsIndependentOptionalAndRedacted(t *testing.T) {
	cfg := Defaults()
	cfg.Webhooks.PolicyFile = filepath.Join(t.TempDir(), "private-policy.json")
	if err := loadWebhookPolicy(&cfg); err != nil {
		t.Fatal("disabled webhooks opened a policy file")
	}
	cfg.Webhooks.Enabled = true
	if err := loadWebhookPolicy(&cfg); err == nil {
		t.Fatal("enabled missing policy accepted")
	}
	valid := `{"schemaVersion":1,"policyId":"webhooks-only","revision":1,"public":{"mode":"disabled"},"privateProfiles":[{"name":"receiver","origins":["https://receiver.internal"],"addressRanges":["10.42.0.0/16"],"assignedTenantIds":["` + policyTestTenant + `"],"transportRef":"direct"}]}`
	if err := os.WriteFile(cfg.Webhooks.PolicyFile, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	federationDigest := cfg.Federation.PolicyDigest
	if err := loadWebhookPolicy(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Webhooks.Policy.PolicyID != "webhooks-only" || cfg.Webhooks.PolicyDigest == "" || cfg.Federation.PolicyDigest != federationDigest || cfg.Federation.Policy.PolicyID == "webhooks-only" {
		t.Fatal("webhook policy leaked into federation")
	}
	raw := string(mustJSON(t, cfg.Report()))
	if strings.Contains(raw, "receiver.internal") || strings.Contains(raw, "10.42") || strings.Contains(raw, cfg.Webhooks.PolicyFile) || !strings.Contains(raw, cfg.Webhooks.PolicyDigest) {
		t.Fatal("webhook policy diagnostics exposed topology or omitted digest")
	}
	for _, invalid := range []string{strings.ReplaceAll(valid, "https://receiver.internal", "http://receiver.internal"), strings.ReplaceAll(valid, `"revision":1`, `"revision":1,"revision":2`), valid + `{}`, strings.ReplaceAll(valid, `"mode":"disabled"`, `"mode":"disabled","unknown":true`)} {
		if err := os.WriteFile(cfg.Webhooks.PolicyFile, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if err := loadWebhookPolicy(&cfg); err == nil {
			t.Fatal("unsafe webhook policy accepted")
		}
	}
	cfg.Webhooks.Enabled = false
	if err := loadWebhookPolicy(&cfg); err != nil || cfg.Webhooks.PolicyDigest != "" {
		t.Fatal("disabled webhook policy retained dependencies")
	}
	cfg.Webhooks.Enabled = true
	cfg.Webhooks.PolicyFile = ""
	if err := loadWebhookPolicy(&cfg); err != nil || cfg.Webhooks.PolicyDigest != "" {
		t.Fatal("public-only webhook profile inherited federation policy")
	}
}
