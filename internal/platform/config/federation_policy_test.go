package config

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const policyTestTenant = "00000000-0000-7000-8000-000000000001"

func TestShippedFederationPoliciesMatchTheirDocumentedScope(t *testing.T) {
	clearRuntimeEnvironment(t)
	for _, test := range []struct {
		name    string
		profile string
		policy  FederationPolicy
	}{
		{
			name: "federation-policy.local.json", profile: "postgres.json",
			policy: FederationPolicy{
				SchemaVersion: 1, PolicyID: "compose-local", Revision: 1,
				Public: FederationPublicPolicy{Mode: "allowlist", Origins: []string{}},
				PrivateProfiles: []FederationPrivateProfile{{
					Name: "compose-catalogs", Origins: []string{"http://api:8080"},
					AddressRanges: []string{"172.30.255.0/24"}, AssignedTenantIDs: []string{policyTestTenant}, TransportRef: "direct",
				}},
			},
		},
		{
			name: "federation-policy.production.example.json", profile: "production.example.json",
			policy: FederationPolicy{
				SchemaVersion: 1, PolicyID: "production-example", Revision: 1,
				Public: FederationPublicPolicy{Mode: "allowlist", Origins: []string{"https://catalog.partner.example:443"}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, err := filepath.Abs(filepath.Join("..", "..", "..", "config", test.name))
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("CATALOG_FEDERATION_POLICY_FILE", path)
			var digest string
			for _, role := range []Role{RoleAPI, RoleWorker} {
				cfg, err := LoadFor(filepath.Join("..", "..", "..", "config", test.profile), role)
				if err != nil {
					t.Fatal(err)
				}
				want, err := normalizeFederationPolicy(test.policy, cfg.Environment)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(cfg.Federation.Policy, want) {
					t.Fatalf("%s policy differs from the documented scope: %#v", role, cfg.Federation.Policy)
				}
				if cfg.Federation.PolicyDigest == "" || digest != "" && digest != cfg.Federation.PolicyDigest {
					t.Fatal("API and worker must load the same non-empty policy digest")
				}
				digest = cfg.Federation.PolicyDigest
			}
		})
	}
}

func TestFederationPolicyNormalizesExactOriginsAndStableDigest(t *testing.T) {
	first := `{
  "schemaVersion":1,
  "policyId":"enterprise-eu",
  "revision":7,
  "public":{"mode":"allowlist","origins":["https://B.example:443","https://a.example"]},
  "additionalDeniedAddressRanges":["10.2.0.0/16","10.1.0.0/16"],
  "privateProfiles":[{
    "name":"private-catalogs",
    "origins":["http://catalog.internal:8080"],
    "addressRanges":["10.42.0.0/16"],
    "assignedTenantIds":["` + policyTestTenant + `"],
    "transportRef":"direct"
  },{
    "name":"another-private",
    "origins":["http://another.internal:8080"],
    "addressRanges":["10.43.0.0/16"],
    "assignedTenantIds":["` + policyTestTenant + `"],
    "transportRef":"direct"
  }]
}`
	second := strings.ReplaceAll(first, `"https://B.example:443","https://a.example"`, `"https://a.example:443","https://b.example"`)
	second = strings.ReplaceAll(second, `"10.2.0.0/16","10.1.0.0/16"`, `"10.1.0.0/16","10.2.0.0/16"`)
	var reordered FederationPolicy
	if err := json.Unmarshal([]byte(second), &reordered); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(reordered.PrivateProfiles)
	second = string(mustJSON(t, reordered))
	load := func(raw string) Config {
		t.Helper()
		path := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := Defaults()
		cfg.Environment = "test"
		cfg.Federation.PolicyFile = path
		if err := loadFederationPolicy(&cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	left := load(first)
	right := load(second)
	if left.Federation.PolicyDigest != right.Federation.PolicyDigest {
		t.Fatalf("semantic policy digest changed with ordering/default ports: %s != %s", left.Federation.PolicyDigest, right.Federation.PolicyDigest)
	}
	if got := left.Federation.Policy.Public.Origins; len(got) != 2 || got[0] != "https://a.example:443" || got[1] != "https://b.example:443" {
		t.Fatalf("normalized origins = %#v", got)
	}
	if profiles := left.Federation.Policy.PrivateProfiles; len(profiles) != 2 || profiles[0].Name != "another-private" || profiles[1].Name != "private-catalogs" || !reflect.DeepEqual(left.Federation.Policy, right.Federation.Policy) {
		t.Fatalf("normalized profiles differ: %#v / %#v", left.Federation.Policy, right.Federation.Policy)
	}
	report := left.Report()
	if report.Federation.PolicyID != "enterprise-eu" || report.Federation.PolicyRevision != 7 || report.Federation.PrivateProfileCount != 2 {
		t.Fatalf("safe policy report = %#v", report.Federation)
	}
	if raw := string(mustJSON(t, report)); strings.Contains(raw, pathTestDirectory(left.Federation.PolicyFile)) || strings.Contains(raw, "catalog.internal") || strings.Contains(raw, "10.42") {
		t.Fatalf("safe report disclosed policy path or private topology: %s", raw)
	}
	left.Federation.Policy.Public.Origins = []string{"https://catalog.example", "https://CATALOG.example:443/"}
	if _, err := normalizeFederationPolicy(left.Federation.Policy, "test"); err == nil || !strings.Contains(err.Error(), "origins must be unique after normalization") {
		t.Fatalf("equivalent duplicate origins returned %v", err)
	}
}

func TestFederationPolicyRejectsAmbiguousOrOverbroadDocuments(t *testing.T) {
	valid := `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":[]}}`
	cases := map[string]string{
		"case alias":              strings.Replace(valid, "schemaVersion", "SchemaVersion", 1),
		"duplicate key":           `{"schemaVersion":1,"policyId":"strict","policyId":"other","revision":1,"public":{"mode":"allowlist","origins":[]}}`,
		"unknown field":           `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":[]},"allowPrivateNetworks":true}`,
		"null":                    `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":null}}`,
		"trailing":                valid + `{}`,
		"wildcard":                `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":["https://*.example.com"]}}`,
		"origin path":             `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":["https://catalog.example/path"]}}`,
		"any with allowlist":      `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"any_public_https","origins":["https://catalog.example"]}}`,
		"public private range":    `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":[]},"privateProfiles":[{"name":"private","origins":["https://catalog.internal"],"addressRanges":["93.184.216.0/24"],"assignedTenantIds":["` + policyTestTenant + `"],"transportRef":"direct"}]}`,
		"overbroad private range": `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":[]},"privateProfiles":[{"name":"private","origins":["https://catalog.internal"],"addressRanges":["10.0.0.0/7"],"assignedTenantIds":["` + policyTestTenant + `"],"transportRef":"direct"}]}`,
		"overbroad IPv6 range":    `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":[]},"privateProfiles":[{"name":"private","origins":["https://catalog.internal"],"addressRanges":["fc00::/6"],"assignedTenantIds":["` + policyTestTenant + `"],"transportRef":"direct"}]}`,
		"mapped address range":    `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":[]},"additionalDeniedAddressRanges":["::ffff:10.0.0.0/104"]}`,
		"zero revision":           strings.ReplaceAll(valid, `"revision":1`, `"revision":0`),
		"noncanonical range":      `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":[]},"additionalDeniedAddressRanges":["10.42.0.1/16"]}`,
		"long policy ID":          `{"schemaVersion":1,"policyId":"` + strings.Repeat("a", 64) + `","revision":1,"public":{"mode":"allowlist","origins":[]}}`,
		"long profile name":       `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":[]},"privateProfiles":[{"name":"` + strings.Repeat("a", 64) + `","origins":["https://catalog.internal"],"addressRanges":["10.42.0.0/16"],"assignedTenantIds":["` + policyTestTenant + `"],"transportRef":"direct"}]}`,
		"duplicate tenant":        `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":[]},"privateProfiles":[{"name":"private","origins":["https://catalog.internal"],"addressRanges":["10.42.0.0/16"],"assignedTenantIds":["` + policyTestTenant + `","` + policyTestTenant + `"],"transportRef":"direct"}]}`,
		"unbounded range":         `{"schemaVersion":1,"policyId":"strict","revision":1,"public":{"mode":"allowlist","origins":[]},"additionalDeniedAddressRanges":["0.0.0.0/0"]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := Defaults()
			cfg.Environment = "test"
			cfg.Federation.PolicyFile = path
			if err := loadFederationPolicy(&cfg); err == nil {
				t.Fatal("unsafe federation policy was accepted")
			}
		})
	}
	oversizedPath := filepath.Join(t.TempDir(), "oversized-policy.json")
	if err := os.WriteFile(oversizedPath, []byte(strings.Repeat("x", maxFederationPolicyBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	cfg.Environment = "test"
	cfg.Federation.PolicyFile = oversizedPath
	if err := loadFederationPolicy(&cfg); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("oversized federation policy returned %v", err)
	}
}

func TestProductionPolicyRequiresHTTPSAndAbsoluteMounts(t *testing.T) {
	cfg := Defaults()
	cfg.Environment = "production"
	cfg.Federation.PolicyFile = "policy.json"
	if err := loadFederationPolicy(&cfg); err == nil || !strings.Contains(err.Error(), "absolute clean path") {
		t.Fatalf("relative production policy path returned %v", err)
	}
	cfg = Defaults()
	cfg.Environment = "test"
	cfg.Federation.PolicyFile = filepath.Join(t.TempDir(), "missing-policy.json")
	if err := loadFederationPolicy(&cfg); err == nil || !strings.Contains(err.Error(), "read federation policy") {
		t.Fatalf("missing federation policy returned %v", err)
	}
	cfg.Federation.PolicyFile = t.TempDir()
	if err := loadFederationPolicy(&cfg); err == nil || !strings.Contains(err.Error(), "read federation policy") {
		t.Fatalf("policy directory returned %v", err)
	}

	policy := FederationPolicy{SchemaVersion: 1, PolicyID: "production", Revision: 1, Public: FederationPublicPolicy{Mode: "allowlist", Origins: []string{"http://catalog.example"}}}
	if _, err := normalizeFederationPolicy(policy, "production"); err == nil {
		t.Fatal("production HTTP origin was accepted")
	}
	policy.Public.Origins = nil
	policy.PrivateProfiles = []FederationPrivateProfile{{Name: "private", Origins: []string{"https://catalog.internal"}, AddressRanges: []string{"10.42.0.0/16"}, AssignedTenantIDs: []string{policyTestTenant}, TrustBundleFile: "relative.pem", TransportRef: "direct"}}
	if _, err := normalizeFederationPolicy(policy, "production"); err == nil {
		t.Fatal("relative production trust bundle was accepted")
	}
}

func TestFederationPolicyRejectsInvalidTopologyAssignments(t *testing.T) {
	privateProfile := func() FederationPrivateProfile {
		return FederationPrivateProfile{Name: "private", Origins: []string{"https://catalog.internal"}, AddressRanges: []string{"10.42.0.0/16"}, AssignedTenantIDs: []string{policyTestTenant}, TransportRef: "direct"}
	}
	tests := []struct {
		name   string
		mutate func(*FederationPolicy)
	}{
		{name: "disabled public origins", mutate: func(policy *FederationPolicy) {
			policy.Public = FederationPublicPolicy{Mode: "disabled", Origins: []string{"https://catalog.example"}}
		}},
		{name: "unknown public mode", mutate: func(policy *FederationPolicy) {
			policy.Public.Mode = "proxy"
		}},
		{name: "invalid denied origin", mutate: func(policy *FederationPolicy) {
			policy.AdditionalDeniedOrigins = []string{"https://denied.example/path"}
		}},
		{name: "duplicate denied range", mutate: func(policy *FederationPolicy) {
			policy.AdditionalDeniedAddressRanges = []string{"10.0.0.0/8", "10.0.0.0/8"}
		}},
		{name: "private profile limit", mutate: func(policy *FederationPolicy) {
			policy.PrivateProfiles = make([]FederationPrivateProfile, 33)
		}},
		{name: "duplicate private profile", mutate: func(policy *FederationPolicy) {
			profile := privateProfile()
			policy.PrivateProfiles = []FederationPrivateProfile{profile, profile}
		}},
		{name: "missing private origins", mutate: func(policy *FederationPolicy) {
			profile := privateProfile()
			profile.Origins = nil
			policy.PrivateProfiles = []FederationPrivateProfile{profile}
		}},
		{name: "missing private ranges", mutate: func(policy *FederationPolicy) {
			profile := privateProfile()
			profile.AddressRanges = nil
			policy.PrivateProfiles = []FederationPrivateProfile{profile}
		}},
		{name: "missing assigned tenants", mutate: func(policy *FederationPolicy) {
			profile := privateProfile()
			profile.AssignedTenantIDs = nil
			policy.PrivateProfiles = []FederationPrivateProfile{profile}
		}},
		{name: "invalid assigned tenant", mutate: func(policy *FederationPolicy) {
			profile := privateProfile()
			profile.AssignedTenantIDs = []string{"not-a-tenant"}
			policy.PrivateProfiles = []FederationPrivateProfile{profile}
		}},
		{name: "unsupported private transport", mutate: func(policy *FederationPolicy) {
			profile := privateProfile()
			profile.TransportRef = "proxy"
			policy.PrivateProfiles = []FederationPrivateProfile{profile}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := FederationPolicy{SchemaVersion: 1, PolicyID: "strict", Revision: 1, Public: FederationPublicPolicy{Mode: "allowlist"}}
			test.mutate(&policy)
			if _, err := normalizeFederationPolicy(policy, "production"); err == nil {
				t.Fatal("invalid federation topology was accepted")
			}
		})
	}
	if _, err := normalizePolicyOrigins(make([]string, 257), false, 256); err == nil {
		t.Fatal("over-limit origin list was accepted")
	}
	if _, err := normalizeAddressRanges(make([]string, 257), false, false, 256); err == nil {
		t.Fatal("over-limit address-range list was accepted")
	}
}

func TestFederationPolicyDigestBindsExactTrustBundleBytes(t *testing.T) {
	directory := t.TempDir()
	trustPath := filepath.Join(directory, "private-ca.pem")
	certificate := federationTestCertificate(t)
	if err := os.WriteFile(trustPath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "policy.json")
	policy := `{"schemaVersion":1,"policyId":"private-trust","revision":1,"public":{"mode":"allowlist","origins":[]},"privateProfiles":[{"name":"private","origins":["https://catalog.internal"],"addressRanges":["10.42.0.0/16"],"assignedTenantIds":["` + policyTestTenant + `"],"trustBundleFile":"` + trustPath + `","transportRef":"direct"}]}`
	if err := os.WriteFile(policyPath, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	load := func() Config {
		t.Helper()
		cfg := Defaults()
		cfg.Environment = "production"
		cfg.Federation.PolicyFile = policyPath
		if err := loadFederationPolicy(&cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	first := load()
	if got := first.Federation.Policy.PrivateProfiles[0].TrustBundlePEM; len(got) == 0 {
		t.Fatal("validated trust bytes were not retained for the routing policy")
	}
	if err := os.WriteFile(trustPath, append(append([]byte(nil), certificate...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	second := load()
	if first.Federation.PolicyDigest == second.Federation.PolicyDigest {
		t.Fatal("policy digest did not change with exact trust-bundle bytes")
	}
	if err := os.WriteFile(trustPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	cfg.Environment = "production"
	cfg.Federation.PolicyFile = policyPath
	if err := loadFederationPolicy(&cfg); err == nil || !strings.Contains(err.Error(), "contains no certificates") {
		t.Fatalf("invalid trust bundle returned %v", err)
	}
	if err := os.WriteFile(trustPath, make([]byte, maxFederationTrustBundleBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadFederationPolicy(&cfg); err == nil || !strings.Contains(err.Error(), "exceeds 4 MiB") {
		t.Fatalf("oversized trust bundle returned %v", err)
	}
	if err := os.Remove(trustPath); err != nil {
		t.Fatal(err)
	}
	if err := loadFederationPolicy(&cfg); err == nil || !strings.Contains(err.Error(), "read federation trust bundle") {
		t.Fatalf("missing trust bundle returned %v", err)
	}
}

// TestFederationPolicyCombinedTrustBundleBound checks aggregate input limits
// with certificate-bearing files without starting a TLS server.
func TestFederationPolicyCombinedTrustBundleBound(t *testing.T) {
	certificate := federationTestCertificate(t)
	directory := t.TempDir()
	largePath, smallPath := filepath.Join(directory, "large.pem"), filepath.Join(directory, "small.pem")
	large := append(bytes.Clone(certificate), bytes.Repeat([]byte("\n"), maxFederationTrustBundleBytes-len(certificate))...)
	for path, body := range map[string][]byte{largePath: large, smallPath: certificate} {
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	policy := Defaults().Federation.Policy
	for _, name := range []string{"a", "b", "c", "d"} {
		policy.PrivateProfiles = append(policy.PrivateProfiles, FederationPrivateProfile{Name: name, Origins: []string{fmt.Sprintf("https://%s.internal", name)}, AddressRanges: []string{"10.42.0.0/16"}, AssignedTenantIDs: []string{policyTestTenant}, TransportRef: "direct", TrustBundleFile: largePath})
	}
	policyPath := filepath.Join(directory, "policy.json")
	for _, extra := range []bool{false, true} {
		if extra {
			profile := policy.PrivateProfiles[0]
			profile.Name, profile.TrustBundleFile = "e", smallPath
			policy.PrivateProfiles = append(policy.PrivateProfiles, profile)
		}
		if err := os.WriteFile(policyPath, mustJSON(t, policy), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := Defaults()
		cfg.Environment, cfg.Federation.PolicyFile = "test", policyPath
		before := cfg.Federation
		err := loadFederationPolicy(&cfg)
		if extra {
			if err == nil || !strings.Contains(err.Error(), "16 MiB combined limit") || !reflect.DeepEqual(cfg.Federation, before) {
				t.Fatalf("combined trust overflow: err=%v, partial policy installed=%v", err, !reflect.DeepEqual(cfg.Federation, before))
			}
			continue
		}
		if err != nil || len(cfg.Federation.Policy.PrivateProfiles) != 4 || !validConfigurationDigest(cfg.Federation.PolicyDigest) {
			t.Fatalf("exact combined bound: %v", err)
		}
		for _, profile := range cfg.Federation.Policy.PrivateProfiles {
			if !bytes.Equal(profile.TrustBundlePEM, large) {
				t.Fatalf("profile %s lost exact trust bytes", profile.Name)
			}
		}
	}
}

// federationTestCertificate supplies certificate bytes without a listener or TLS exchange.
func federationTestCertificate(t *testing.T) []byte {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Unix(0, 0), NotAfter: time.Unix(1<<31, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func pathTestDirectory(path string) string {
	return filepath.Dir(path)
}

// Valid entries isolate collection ceilings from per-entry validation failures.
func TestFederationPolicyAcceptsExactTopologyLimits(t *testing.T) {
	policy := Defaults().Federation.Policy
	policy.PolicyID = strings.Repeat("a", 63)
	policy.Public.Mode = "allowlist"
	for i := range 256 {
		policy.Public.Origins = append(policy.Public.Origins, fmt.Sprintf("https://public-%d.example", i))
		policy.AdditionalDeniedOrigins = append(policy.AdditionalDeniedOrigins, fmt.Sprintf("https://denied-%d.example", i))
		policy.AdditionalDeniedAddressRanges = append(policy.AdditionalDeniedAddressRanges, fmt.Sprintf("10.0.%d.0/24", i))
	}
	profile := FederationPrivateProfile{Name: strings.Repeat("b", 63), TransportRef: "direct"}
	for i := range 64 {
		profile.Origins = append(profile.Origins, fmt.Sprintf("https://private-%d.internal", i))
		profile.AddressRanges = append(profile.AddressRanges, fmt.Sprintf("10.1.%d.0/24", i))
	}
	for i := range 128 {
		profile.AssignedTenantIDs = append(profile.AssignedTenantIDs, fmt.Sprintf("00000000-0000-7000-8000-%012d", i+1))
	}
	for i := range 32 {
		next := profile
		if i > 0 {
			next.Name = fmt.Sprintf("profile-%02d", i)
		}
		policy.PrivateProfiles = append(policy.PrivateProfiles, next)
	}
	normalized, err := normalizeFederationPolicy(policy, "production")
	if err != nil || len(normalized.PrivateProfiles) != 32 || len(normalized.Public.Origins) != 256 || len(normalized.PrivateProfiles[0].AssignedTenantIDs) != 128 {
		t.Fatalf("exact policy limits: %v", err)
	}
	for _, test := range []struct {
		name, message string
		change        func(*FederationPolicy)
	}{
		{"profiles", "more than 32", func(p *FederationPolicy) {
			extra := profile
			extra.Name = "extra"
			p.PrivateProfiles = append(p.PrivateProfiles, extra)
		}},
		{"public origins", "too many origins", func(p *FederationPolicy) { p.Public.Origins = append(p.Public.Origins, "https://extra.example") }},
		{"denied origins", "too many origins", func(p *FederationPolicy) {
			p.AdditionalDeniedOrigins = append(p.AdditionalDeniedOrigins, "https://extra.example")
		}},
		{"denied ranges", "too many address ranges", func(p *FederationPolicy) {
			p.AdditionalDeniedAddressRanges = append(p.AdditionalDeniedAddressRanges, "10.2.0.0/24")
		}},
		{"private origins", "valid exact origins", func(p *FederationPolicy) {
			p.PrivateProfiles[0].Origins = append(p.PrivateProfiles[0].Origins, "https://extra.internal")
		}},
		{"private ranges", "bounded private address ranges", func(p *FederationPolicy) {
			p.PrivateProfiles[0].AddressRanges = append(p.PrivateProfiles[0].AddressRanges, "10.3.0.0/24")
		}},
		{"tenants", "requires assigned tenants", func(p *FederationPolicy) {
			p.PrivateProfiles[0].AssignedTenantIDs = append(p.PrivateProfiles[0].AssignedTenantIDs, "00000000-0000-7000-8000-000000000129")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var overflow FederationPolicy
			if err := json.Unmarshal(mustJSON(t, normalized), &overflow); err != nil {
				t.Fatal(err)
			}
			test.change(&overflow)
			if _, err := normalizeFederationPolicy(overflow, "production"); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("overflow: %v; want %q", err, test.message)
			}
		})
	}
	// Each entire reviewed private range is permitted, but a wider CIDR is not.
	for _, value := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"} {
		got, err := normalizeAddressRanges([]string{value}, true, false, 64)
		if err != nil || len(got) != 1 || got[0] != value {
			t.Fatalf("private range %q: %v, %v", value, got, err)
		}
	}
}
