package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

func trustConfiguration(t *testing.T) Config {
	t.Helper()
	c := validTestConfig()
	c.Federation.Policy.Public.Origins = []string{"https://tl.ans-qualification.svc.cluster.local"}
	c.TrustVerification.Enabled = true
	ra, err := filepath.Abs("../../standards/anstrust/testdata/native/ra.pem")
	if err != nil {
		t.Fatal(err)
	}
	log, err := filepath.Abs("../../standards/anstrust/testdata/native/tl.pem")
	if err != nil {
		t.Fatal(err)
	}
	c.TrustVerification.Policies = []TrustPolicy{{Name: "owned-ans", Profile: anstrust.Profile, RegistrationAuthority: "ans-ra-local", LogOrigin: "owned-ans", EvidenceOrigin: "https://tl.ans-qualification.svc.cluster.local", PublisherDomains: []string{"trust-v1.ans.test", "trust-v2.ans.test"}, RegistrationKeys: []TrustKey{{ID: "ans-ra-signer", File: ra}}, LogKeyFiles: []string{log}}}
	return c
}

func TestTrustVerificationStrictConfigurationAndDefaults(t *testing.T) {
	clearRuntimeEnvironment(t)
	c, err := LoadFor("../../../config/local.json", RoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	if c.TrustVerification.Enabled || c.TrustVerification.Timeout.Duration != 15*time.Second || c.TrustVerification.MaxRequests != 4 || c.TrustVerification.FutureSkew.Duration != time.Minute || c.TrustVerification.Limits.BundleBytes != 1<<20 {
		t.Fatal("unsafe defaults")
	}
	for _, raw := range []string{`null`, `[]`, `{"enabled":null}`, `{"Enabled":true}`, `{"enabled":false,"enabled":true}`, `{"policies":[null]}`, `{"unknown":true}`, `{"timeout":15}`, `{"limits":{"receiptBytes":null}}`, `{"policies":[{"name":"a","extra":1}]}`, `{} {}`} {
		if err := applyTrustVerification(&c, raw); err == nil {
			t.Fatalf("accepted ambiguous override %s", raw)
		}
	}
	t.Setenv("CATALOG_TRUST_VERIFICATION", `{"timeout":"9s","futureSkew":"0s","limits":{"receiptBytes":8192}}`)
	c, err = LoadFor("../../../config/local.json", RoleAPI)
	if err != nil || c.TrustVerification.Timeout.Duration != 9*time.Second || c.TrustVerification.FutureSkew.Duration != 0 || c.TrustVerification.Limits.ReceiptBytes != 8192 || c.TrustVerification.MaxRequests != 4 {
		t.Fatalf("overlay: %+v %v", c.TrustVerification, err)
	}
}

func TestTrustVerificationPolicyAdmissionAndRoleBudgets(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"missing policies": func(c *Config) { c.TrustVerification.Policies = nil },
		"duplicate names": func(c *Config) {
			c.TrustVerification.Policies = append(c.TrustVerification.Policies, c.TrustVerification.Policies[0])
		},
		"unknown profile":   func(c *Config) { c.TrustVerification.Policies[0].Profile = "other" },
		"unknown route":     func(c *Config) { c.TrustVerification.Policies[0].NetworkProfile = "missing" },
		"unapproved origin": func(c *Config) { c.Federation.Policy.Public.Origins = nil },
		"bad origin":        func(c *Config) { c.TrustVerification.Policies[0].EvidenceOrigin = "https://user:secret@tl.test" },
		"bad domain":        func(c *Config) { c.TrustVerification.Policies[0].PublisherDomains = []string{"*.ans.test"} },
		"duplicate key hint": func(c *Config) {
			c.TrustVerification.Policies[0].RegistrationKeys = append(c.TrustVerification.Policies[0].RegistrationKeys, c.TrustVerification.Policies[0].RegistrationKeys[0])
		},
		"relative key":         func(c *Config) { c.TrustVerification.Policies[0].LogKeyFiles = []string{"tl.pem"} },
		"zero deadline":        func(c *Config) { c.TrustVerification.Timeout.Duration = 0 },
		"too many requests":    func(c *Config) { c.TrustVerification.MaxRequests = 5 },
		"oversized receipt":    func(c *Config) { c.TrustVerification.Limits.ReceiptBytes = anstrust.MaxReceiptBytes + 1 },
		"zero bundle":          func(c *Config) { c.TrustVerification.Limits.BundleBytes = 0 },
		"skew exceeds profile": func(c *Config) { c.TrustVerification.FutureSkew.Duration = 61 * time.Second },
		"age exceeds profile":  func(c *Config) { c.TrustVerification.MaxStatusAge.Duration = 25 * time.Hour },
	} {
		t.Run(name, func(t *testing.T) {
			c := trustConfiguration(t)
			change(&c)
			if c.ValidateFor(RoleAPI) == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
	c := trustConfiguration(t)
	if err := c.ValidateFor(RoleAll); err != nil {
		t.Fatal(err)
	}
	c.Worker.LeaseDuration.Duration = 30 * time.Second
	c.TrustVerification.Timeout.Duration = 30 * time.Second
	if err := c.ValidateFor(RoleAPI); err != nil {
		t.Fatal(err)
	}
	if c.ValidateFor(RoleWorker) == nil {
		t.Fatal("worker accepted insufficient lease margin")
	}
	c.Database.Driver = "postgres"
	c.Database.DSN = "postgres://unused.invalid/catalog"
	if err := c.ValidateFor(RoleMigration); err != nil {
		t.Fatal("owner consumed verification timing", err)
	}
}

func TestTrustVerificationDisabledLoadingAndKeyFailures(t *testing.T) {
	c := trustConfiguration(t)
	if err := loadTrustVerification(&c); err != nil {
		t.Fatal(err)
	}
	_, v, ok := c.TrustVerification.Policy("owned-ans")
	if !ok || v == nil {
		t.Fatal("authority not loaded")
	}
	if _, _, ok := c.TrustVerification.Policy("unknown"); ok {
		t.Fatal("unknown authority admitted")
	}
	before := c.TrustVerification.PolicyDigest(c.Federation.PolicyDigest)
	if !strings.HasPrefix(before, "sha256:") {
		t.Fatal("missing policy identity")
	}
	c.TrustVerification.Timeout.Duration = time.Second
	if before == c.TrustVerification.PolicyDigest(c.Federation.PolicyDigest) {
		t.Fatal("budget change lost from identity")
	}
	if before == trustConfiguration(t).TrustVerification.PolicyDigest(c.Federation.PolicyDigest) {
		t.Fatal("unloaded policy has active identity")
	}
	raw, err := json.Marshal(c.Report())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"ans-ra-local", "ans-ra-signer", "trust-v1.ans.test", "ra.pem", "PUBLIC KEY"} {
		if bytes.Contains(raw, []byte(value)) {
			t.Fatalf("diagnostics expose %s", value)
		}
	}
	c.TrustVerification.Enabled = false
	c.TrustVerification.Policies[0].RegistrationKeys[0].File = "/missing/never-read.pem"
	if err := loadTrustVerification(&c); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.TrustVerification.Policy("owned-ans"); ok {
		t.Fatal("disabled policy remained usable")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "invalid.pem")
	for name, content := range map[string][]byte{"oversized": bytes.Repeat([]byte("a"), 4097), "private": []byte("-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n"), "invalid": []byte("not a key")} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(bad, content, 0o600); err != nil {
				t.Fatal(err)
			}
			c := trustConfiguration(t)
			c.TrustVerification.Policies[0].LogKeyFiles = []string{bad}
			if loadTrustVerification(&c) == nil {
				t.Fatal("accepted unusable key")
			}
		})
	}
	for _, path := range []string{dir, filepath.Join(dir, "missing.pem")} {
		c := trustConfiguration(t)
		c.TrustVerification.Policies[0].LogKeyFiles = []string{path}
		if loadTrustVerification(&c) == nil {
			t.Fatal("read invalid key file")
		}
	}
	c = trustConfiguration(t)
	c.TrustVerification.Policies[0].LogKeyFiles = append(c.TrustVerification.Policies[0].LogKeyFiles, c.TrustVerification.Policies[0].LogKeyFiles[0])
	if loadTrustVerification(&c) == nil {
		t.Fatal("duplicate full key accepted")
	}
}
