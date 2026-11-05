package config

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestOIDCTrustConfigurationIsRoleScopedAndRedacted(t *testing.T) {
	for _, test := range []struct {
		name       string
		path       string
		production bool
		valid      bool
	}{
		{name: "public default", valid: true},
		{name: "absolute", path: "/run/oidc/ca.pem", production: true, valid: true},
		{name: "relative test", path: "oidc/ca.pem", valid: true},
		{name: "relative production", path: "oidc/ca.pem", production: true},
		{name: "unclean", path: "/run/oidc/../ca.pem"},
		{name: "whitespace", path: " "},
		{name: "control", path: "/run/ca\x00.pem"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.Auth = Auth{Mode: "oidc", Issuer: "https://identity.example.com", Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: "00000000-0000-7000-8000-000000000001", TrustBundleFile: test.path}
			if test.production {
				cfg.Environment = "production"
			}
			if err := cfg.validateAuth(); (err == nil) != test.valid {
				t.Fatalf("trust shape validation = %v, want valid %t", err, test.valid)
			}
		})
	}
	cfg := validTestConfig()
	cfg.Auth.TrustBundleFile = "/not-mounted/private-issuer-ca.pem"
	if err := cfg.ValidateFor(RoleAPI); err == nil {
		t.Fatal("development authentication retained an OIDC trust setting")
	}
	if err := cfg.ValidateFor(RoleWorker); err != nil {
		t.Fatalf("worker required an API-only trust input: %v", err)
	}
	report := cfg.Report()
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Auth.TrustBundleConfigured || bytes.Contains(raw, []byte(cfg.Auth.TrustBundleFile)) {
		t.Fatal("trust diagnostics must report only whether a bundle is configured")
	}
}
