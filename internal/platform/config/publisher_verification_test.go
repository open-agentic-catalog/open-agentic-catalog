package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublisherVerificationLoadingAndLimits(t *testing.T) {
	clearRuntimeEnvironment(t)
	base := Defaults()
	basePath := filepath.Join(t.TempDir(), "base.json")
	auth := `"auth":{"developmentTokens":{"test":{"subject":"admin","tenantId":"00000000-0000-7000-8000-000000000001","roles":["tenant_admin"]}}}`
	if err := os.WriteFile(basePath, []byte("{"+auth+"}"), 0600); err != nil {
		t.Fatal(err)
	}
	if p := base.PublisherDomainVerification.Policy(); p.Mode != "disabled" || p.ChallengeTTL != 24*time.Hour || p.ProofMaxAge != 720*time.Hour || p.LookupTimeout != 5*time.Second {
		t.Fatalf("defaults: %+v", p)
	}
	for _, raw := range []string{`null`, `[]`, `{"mode":null}`, `{"mode":"optional","Mode":"disabled"}`, `{"mode":"optional","mode":"disabled"}`, `{"unknown":1}`, `{"lookupTimeout":1}`, `{"mode":"automatic"}`, `{"challengeTTL":"4m"}`, `{"challengeTTL":"169h"}`, `{"proofMaxAge":"59m"}`, `{"proofMaxAge":"8761h"}`, `{"lookupTimeout":"99ms"}`, `{"lookupTimeout":"11s"}`} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("CATALOG_PUBLISHER_DOMAIN_VERIFICATION", raw)
			if _, err := LoadFor(basePath, RoleAll); err == nil {
				t.Fatal("invalid override accepted")
			}
		})
	}
	for _, mode := range []string{"disabled", "optional", "required_for_approval"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CATALOG_PUBLISHER_DOMAIN_VERIFICATION", `{"mode":"`+mode+`","challengeTTL":"5m","proofMaxAge":"1h","lookupTimeout":"100ms"}`)
			value, err := LoadFor(basePath, RoleAll)
			if err != nil {
				t.Fatal(err)
			}
			if value.PublisherDomainVerification.Mode != mode || value.PublisherDomainVerification.ChallengeTTL.Duration != 5*time.Minute {
				t.Fatal("override not delivered")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"publisherDomainVerification":{"mode":"optional","challengeTTL":"1h"},`+auth+`}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALOG_PUBLISHER_DOMAIN_VERIFICATION", `{"mode":"required_for_approval"}`)
	loaded, err := LoadFor(path, RoleAll)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PublisherDomainVerification.Mode != "required_for_approval" || loaded.PublisherDomainVerification.ChallengeTTL.Duration != time.Hour {
		t.Fatal("file/default/environment precedence changed")
	}
	report := loaded.Report()
	if report.PublisherDomainVerification.Mode != loaded.PublisherDomainVerification.Mode {
		t.Fatal("diagnostics omitted policy")
	}
}
