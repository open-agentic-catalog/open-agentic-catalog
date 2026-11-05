package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// Each case starts with an accepted configuration and checks the particular
// validation error, so an unrelated earlier guard cannot satisfy the test.
func TestRuntimeValidationEdgesReachTheirIntendedGuard(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, want string
		production bool
		mutate     func(*Config)
	}{
		{"empty API listener", "http.address is required", false, func(c *Config) { c.HTTP.Address = "" }},
		{"public URL empty host", "publicBaseUrl must be an absolute", false, func(c *Config) { c.HTTP.PublicBaseURL = "https:///" }},
		{"public URL empty port", "publicBaseUrl must be an absolute", false, func(c *Config) { c.HTTP.PublicBaseURL = "https://catalog.example.com:" }},
		{"OIDC empty host", "OIDC issuer must be an absolute", true, func(c *Config) { c.Auth.Issuer = "https:///issuer" }},
		{"OIDC empty port", "OIDC issuer must be an absolute", true, func(c *Config) { c.Auth.Issuer = "https://identity.example.com:" }},
		{"numeric dotted publisher domain", "canonical publisher domain", false, func(c *Config) { c.Discovery.Hosts[0].PublisherDomain = "123.456" }},
		{"production local profile", "production requires a non-local profile", true, func(c *Config) { c.Profile = "local" }},
		{"production memory", "production requires PostgreSQL", true, func(c *Config) {
			c.Database = Database{Driver: "memory", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
		}},
		{"production development auth", "production API requires OIDC", true, func(c *Config) { c.Auth = validTestConfig().Auth }},
		{"shutdown bound", "shutdownTimeout must be positive", false, func(c *Config) { c.HTTP.ShutdownTimeout.Duration = 0 }},
		{"policy identity", "resolved federation policy identity is incomplete", false, func(c *Config) { c.Federation.PolicyDigest = "invalid" }},
		{"disabled credential directory", "disabled credential resolution cannot retain", false, func(c *Config) { c.CredentialResolution.Directory = "/credentials" }},
		{"OIDC tenancy mode", "tenancyMode must be dedicated or shared", true, func(c *Config) { c.Auth.TenancyMode = "other" }},
		{"discovery host count", "at most 64 discovery hosts", false, func(c *Config) { c.Discovery.Hosts = make([]DiscoveryHost, 65) }},
		{"discovery mode", "host mode must be disabled, anonymous, or authenticated", false, func(c *Config) { c.Discovery.Hosts[0].Mode = "private" }},
		{"empty namespace roles", "development namespace assignment is invalid", false, func(c *Config) {
			c.Auth.DevelopmentTokens = map[string]DevelopmentUser{"fixture": {Subject: "reader", TenantID: "00000000-0000-7000-8000-000000000001", Namespaces: map[string][]identity.Role{"00000000-0000-7000-8000-000000000002": {}}}}
		}},
		{"namespace administrative role", "namespace assignment role is invalid", false, func(c *Config) {
			c.Auth.DevelopmentTokens = map[string]DevelopmentUser{"fixture": {Subject: "reader", TenantID: "00000000-0000-7000-8000-000000000001", Namespaces: map[string][]identity.Role{"00000000-0000-7000-8000-000000000002": {identity.RoleTenantAdmin}}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validTestConfig()
			if tc.production {
				makeProductionConfig(&c)
			}
			if err := c.ValidateFor(RoleAPI); err != nil {
				t.Fatalf("valid control: %v", err)
			}
			tc.mutate(&c)
			if err := c.ValidateFor(RoleAPI); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
	c := validTestConfig()
	if err := c.ValidateFor(Role("unknown")); err == nil || !strings.Contains(err.Error(), "unsupported role") {
		t.Fatalf("unknown role: %v", err)
	}
}

func TestSharedOIDCClaimNameBounds(t *testing.T) {
	t.Parallel()
	for _, claim := range []string{"org.AZ09_-:id", strings.Repeat("a", 128), "", strings.Repeat("a", 129)} {
		t.Run(fmt.Sprintf("claim_%d_bytes", len(claim)), func(t *testing.T) {
			c := validTestConfig()
			makeProductionConfig(&c)
			c.Auth.TenancyMode, c.Auth.DedicatedTenantID = "shared", ""
			c.Auth.OrganizationClaim = "organization"
			if err := c.ValidateFor(RoleAPI); err != nil {
				t.Fatalf("valid shared tenancy: %v", err)
			}
			c.Auth.OrganizationClaim = claim
			err := c.ValidateFor(RoleAPI)
			if claim == "" || len(claim) > 128 {
				if err == nil || !strings.Contains(err.Error(), "shared OIDC tenancy requires organizationClaim") {
					t.Fatalf("invalid claim returned %v", err)
				}
			} else if err != nil {
				t.Fatalf("valid claim rejected: %v", err)
			}
		})
	}
}

func TestLoadPropagatesSelectedPolicyFailures(t *testing.T) {
	clearRuntimeEnvironment(t)
	for _, kind := range []string{"federation", "webhook"} {
		t.Run(kind, func(t *testing.T) {
			c := validTestConfig()
			missing := filepath.Join(t.TempDir(), "missing-policy.json")
			if kind == "federation" {
				c.Federation.PolicyFile = missing
			} else {
				c.Webhooks.Enabled, c.Webhooks.PolicyFile = true, missing
			}
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFor(path, RoleAPI); err == nil || !strings.Contains(err.Error(), "read federation policy") {
				t.Fatalf("missing %s policy: %v", kind, err)
			}
		})
	}
}

func TestMountedSecretRejectsEmptyPathAndDirectory(t *testing.T) {
	for _, path := range []string{"", t.TempDir()} {
		lookup := func(name string) (string, bool) { return path, name == "FIXTURE_SECRET_FILE" }
		if value, present, err := readEnvironmentSecret("FIXTURE_SECRET", "FIXTURE_SECRET_FILE", lookup); err == nil || present || value != "" {
			t.Fatalf("unreadable mounted secret returned a value: present=%v error=%v", present, err)
		}
	}
}
