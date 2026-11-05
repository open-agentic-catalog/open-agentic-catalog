// Unit tests exercise isolated validation and controlled dependencies.
package fetcher

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const routerTestTenant = "00000000-0000-7000-8000-000000000001"

func validRouterPolicy() RoutingPolicy {
	return RoutingPolicy{
		ID:               "1-policy",
		Revision:         1,
		Digest:           "sha256:" + strings.Repeat("1", 64),
		Public:           PublicRoute{Mode: PublicDisabled},
		Timeout:          time.Second,
		MaxResponseBytes: 1024,
		MaxRedirects:     1,
	}
}

func TestRouterConstructorValidatesAndNormalizesPolicy(t *testing.T) {
	policy := validRouterPolicy()
	policy.Public = PublicRoute{Mode: PublicAllowlist, Origins: []string{"https://CATALOG.EXAMPLE"}}
	policy.DeniedOrigins = []string{"https://DENIED.EXAMPLE"}
	router, err := NewRouter(policy, nil)
	if err != nil {
		t.Fatalf("valid normalized policy: %v", err)
	}
	if _, ok := router.public.origins["https://catalog.example:443"]; !ok {
		t.Fatalf("public origins were not normalized: %#v", router.public.origins)
	}
	if _, ok := router.denied["https://denied.example:443"]; !ok {
		t.Fatalf("denied origins were not normalized: %#v", router.denied)
	}

	privateProfile := PrivateRoute{
		Name:              "private",
		Origins:           []string{"https://private.example"},
		AddressPrefixes:   []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")},
		AssignedTenantIDs: []string{routerTestTenant},
	}
	tests := []struct {
		name   string
		mutate func(*RoutingPolicy)
	}{
		{name: "invalid ID", mutate: func(policy *RoutingPolicy) { policy.ID = "Upper" }},
		{name: "invalid ID character", mutate: func(policy *RoutingPolicy) { policy.ID = "bad_name" }},
		{name: "invalid digest", mutate: func(policy *RoutingPolicy) { policy.Digest = "sha256:not-a-digest" }},
		{name: "oversized response", mutate: func(policy *RoutingPolicy) { policy.MaxResponseBytes = 64<<20 + 1 }},
		{name: "disabled origins", mutate: func(policy *RoutingPolicy) { policy.Public.Origins = []string{"https://catalog.example"} }},
		{name: "any-public origins", mutate: func(policy *RoutingPolicy) {
			policy.Public = PublicRoute{Mode: PublicAnyPublicHTTPS, Origins: []string{"https://catalog.example"}}
		}},
		{name: "public HTTP", mutate: func(policy *RoutingPolicy) {
			policy.AllowHTTPForTests = true
			policy.Public = PublicRoute{Mode: PublicAllowlist, Origins: []string{"http://catalog.example"}}
		}},
		{name: "origin path", mutate: func(policy *RoutingPolicy) {
			policy.Public = PublicRoute{Mode: PublicAllowlist, Origins: []string{"https://catalog.example/path"}}
		}},
		{name: "denied origin path", mutate: func(policy *RoutingPolicy) { policy.DeniedOrigins = []string{"https://denied.example/path"} }},
		{name: "normalized duplicate", mutate: func(policy *RoutingPolicy) {
			policy.DeniedOrigins = []string{"https://denied.example", "https://DENIED.EXAMPLE:443"}
		}},
		{name: "missing private assignment", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.AssignedTenantIDs = nil
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "public private range", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.AddressPrefixes = []netip.Prefix{netip.MustParsePrefix("8.8.8.0/24")}
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "duplicate tenant assignment", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.AssignedTenantIDs = []string{routerTestTenant, routerTestTenant}
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "bounded topology", mutate: func(policy *RoutingPolicy) {
			policy.PrivateRoutes = make([]PrivateRoute, 33)
		}},
		{name: "invalid denied prefix", mutate: func(policy *RoutingPolicy) {
			policy.DeniedAddressPrefixes = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
		}},
		{name: "unknown public mode", mutate: func(policy *RoutingPolicy) {
			policy.Public.Mode = "proxy"
		}},
		{name: "public origin limit", mutate: func(policy *RoutingPolicy) {
			policy.Public = PublicRoute{Mode: PublicAllowlist, Origins: make([]string, 257)}
		}},
		{name: "invalid private name", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.Name = "Private"
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "duplicate private name", mutate: func(policy *RoutingPolicy) {
			policy.PrivateRoutes = []PrivateRoute{privateProfile, privateProfile}
		}},
		{name: "invalid private origin", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.Origins = []string{"https://private.example/path"}
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "noncanonical private prefix", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.AddressPrefixes = []netip.Prefix{netip.MustParsePrefix("10.42.1.1/16")}
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "duplicate private prefix", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.AddressPrefixes = []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16"), netip.MustParsePrefix("10.42.0.0/16")}
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "empty tenant assignment", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.AssignedTenantIDs = []string{""}
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "conflicting trust inputs", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.TrustBundleFile = "/missing.pem"
			profile.TrustBundlePEM = []byte("invalid")
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "missing trust file", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.TrustBundleFile = "/missing.pem"
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "oversized trust bundle", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.TrustBundlePEM = make([]byte, 4<<20+1)
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
		{name: "invalid trust bundle", mutate: func(policy *RoutingPolicy) {
			profile := privateProfile
			profile.TrustBundlePEM = []byte("not a certificate")
			policy.PrivateRoutes = []PrivateRoute{profile}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validRouterPolicy()
			test.mutate(&candidate)
			if _, err := NewRouter(candidate, nil); err == nil {
				t.Fatal("invalid routing policy was accepted")
			}
		})
	}
}

func TestRouterEnforcesExactPolicyProfileTenantAndDigest(t *testing.T) {
	policy := RoutingPolicy{
		ID: "test-policy", Revision: 3, Digest: "sha256:" + strings.Repeat("a", 64),
		Public:        PublicRoute{Mode: PublicAllowlist, Origins: []string{"https://public.example:443"}},
		PrivateRoutes: []PrivateRoute{{Name: "private-catalogs", Origins: []string{"https://private.example:8443"}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")}, AssignedTenantIDs: []string{routerTestTenant}}},
		DeniedOrigins: []string{"https://denied.example:443"}, Timeout: 50 * time.Millisecond, MaxResponseBytes: 1024, MaxRedirects: 1,
	}
	resolutions := 0
	router, err := NewRouter(policy, credentialResolverFunc(func(context.Context, CredentialRequest) (ResolvedCredential, error) {
		resolutions++
		return ResolvedCredential{}, errors.New("unexpected credential resolution")
	}))
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]Request{
		"public outside allowlist": {URL: "https://other.example/catalog"},
		"explicit denied origin":   {URL: "https://denied.example/catalog"},
		"private wrong port":       {URL: "https://private.example/catalog", TenantID: routerTestTenant, NetworkProfile: "private-catalogs"},
		"private wrong tenant":     {URL: "https://private.example:8443/catalog", TenantID: "00000000-0000-7000-8000-000000000002", NetworkProfile: "private-catalogs"},
		"unknown profile":          {URL: "https://private.example:8443/catalog", TenantID: routerTestTenant, NetworkProfile: "missing"},
		"stale policy":             {URL: "https://public.example/catalog", ExpectedPolicyDigest: "sha256:" + strings.Repeat("b", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			request.CredentialBinding = "partner"
			want := ErrPolicyDenied
			if name == "stale policy" {
				want = ErrPolicyChanged
			}
			if _, err := router.Fetch(canceledDialContext(t), request); !errors.Is(err, want) || resolutions != 0 {
				t.Fatalf("routing refusal: error=%v, credential resolutions=%d", err, resolutions)
			}
		})
	}
	if got := router.PolicyDigest(); got != policy.Digest {
		t.Fatalf("policy digest = %q", got)
	}
}

func TestAnyPublicHTTPSNeverWidensToHTTPFixtures(t *testing.T) {
	router, err := NewRouter(RoutingPolicy{
		ID: "public-https", Revision: 1, Digest: "sha256:" + strings.Repeat("8", 64),
		Public: PublicRoute{Mode: PublicAnyPublicHTTPS}, AllowHTTPForTests: true,
		Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Fetch(canceledDialContext(t), Request{URL: "http://127.0.0.1/catalog"}); err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("any-public HTTPS mode accepted HTTP: %v", err)
	}
}

func TestFileCredentialResolverRejectsUntrustedMetadata(t *testing.T) {
	directory := t.TempDir()
	resolver, err := NewFileCredentialResolver(directory)
	if err != nil {
		t.Fatal(err)
	}
	request := CredentialRequest{TenantID: routerTestTenant, Binding: "partner", Origin: "https://catalog.example:443"}
	cases := map[string]string{
		"duplicate":   `{"schemaVersion":1,"schemaVersion":1}`,
		"case alias":  `{"SchemaVersion":1,"tenantId":"` + routerTestTenant + `","revision":"1","origin":"https://catalog.example:443","type":"bearer","secretFile":"token"}`,
		"unknown":     `{"schemaVersion":1,"tenantId":"` + routerTestTenant + `","revision":"1","origin":"https://catalog.example:443","type":"bearer","secretFile":"token","extra":true}`,
		"null":        `{"schemaVersion":1,"tenantId":"` + routerTestTenant + `","revision":"1","origin":"https://catalog.example:443","type":"bearer","secretFile":null}`,
		"nested null": `{"schemaVersion":1,"tenantId":"` + routerTestTenant + `","revision":"1","origin":"https://catalog.example:443","type":"bearer","secretFile":"token","extra":[1,null]}`,
		"tenant":      `{"schemaVersion":1,"tenantId":"00000000-0000-7000-8000-000000000002","revision":"1","origin":"https://catalog.example:443","type":"bearer","secretFile":"token"}`,
		"origin":      `{"schemaVersion":1,"tenantId":"` + routerTestTenant + `","revision":"1","origin":"https://other.example:443","type":"bearer","secretFile":"token"}`,
		"origin path": `{"schemaVersion":1,"tenantId":"` + routerTestTenant + `","revision":"1","origin":"https://catalog.example:443/private","type":"bearer","secretFile":"token"}`,
		"revision":    `{"schemaVersion":1,"tenantId":"` + routerTestTenant + `","revision":" spaced ","origin":"https://catalog.example:443","type":"bearer","secretFile":"token"}`,
		"expired":     `{"schemaVersion":1,"tenantId":"` + routerTestTenant + `","revision":"1","expiresAt":"2000-01-01T00:00:00Z","origin":"https://catalog.example:443","type":"bearer","secretFile":"token"}`,
		"secret path": `{"schemaVersion":1,"tenantId":"` + routerTestTenant + `","revision":"1","origin":"https://catalog.example:443","type":"bearer","secretFile":"../token"}`,
		"trailing":    `{"schemaVersion":1} {}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(directory, "partner.json"), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := resolver.Resolve(context.Background(), request); err == nil || strings.Contains(err.Error(), directory) {
				t.Fatalf("untrusted credential metadata returned %v", err)
			}
		})
	}
	for _, binding := range []string{"../escape", "", ".", "..", strings.Repeat("a", 129)} {
		if _, err := resolver.Resolve(context.Background(), CredentialRequest{Binding: binding}); err == nil || !strings.Contains(err.Error(), "binding reference is invalid") {
			t.Fatalf("invalid credential binding returned %v", err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.Resolve(cancelled, request); err == nil {
		t.Fatal("cancelled credential resolution was accepted")
	}
}

func TestFileCredentialResolverBoundsLeafFilesAndSupportsAPIKeys(t *testing.T) {
	if _, err := NewFileCredentialResolver("relative"); err == nil {
		t.Fatal("relative credential directory was accepted")
	}
	directory := t.TempDir()
	regularFile := filepath.Join(directory, "regular-file")
	if err := os.WriteFile(regularFile, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileCredentialResolver(regularFile); err == nil {
		t.Fatal("regular file was accepted as a credential directory")
	}
	resolver, err := NewFileCredentialResolver(directory)
	if err != nil {
		t.Fatal(err)
	}
	request := CredentialRequest{TenantID: routerTestTenant, Binding: "partner", Origin: "https://catalog.example:443"}
	writeMetadata := func(metadata credentialMetadata) {
		t.Helper()
		raw, marshalErr := json.Marshal(metadata)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := os.WriteFile(filepath.Join(directory, "partner.json"), raw, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	base := credentialMetadata{SchemaVersion: 1, TenantID: routerTestTenant, Revision: "version-one", Origin: request.Origin, Type: "api_key", SecretFile: "partner.key"}
	writeMetadata(base)
	if err := os.WriteFile(filepath.Join(directory, base.SecretFile), []byte("fixture-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(context.Background(), request)
	if err != nil || resolved.Headers["X-Api-Key"] != "fixture-value" || resolved.Revision != base.Revision {
		t.Fatalf("API-key credential = %#v, %v", resolved, err)
	}

	if err := os.WriteFile(filepath.Join(directory, base.SecretFile), []byte("line-one\nline-two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "secret is invalid") {
		t.Fatalf("multiline credential leaf returned %v", err)
	}
	for name, raw := range map[string][]byte{
		"empty":             nil,
		"only newline":      []byte("\n"),
		"control character": []byte("fixture\tvalue"),
		"invalid UTF-8":     {0xff, 0xfe},
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(directory, base.SecretFile), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "secret is invalid") {
				t.Fatalf("invalid credential leaf returned %v", err)
			}
		})
	}
	if err := os.Remove(filepath.Join(directory, base.SecretFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "secret is unavailable") {
		t.Fatalf("missing credential leaf returned %v", err)
	}
	if err := os.Mkdir(filepath.Join(directory, base.SecretFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "secret is unavailable") || strings.Contains(err.Error(), directory) {
		t.Fatalf("unreadable credential leaf returned %v", err)
	}
	if err := os.Remove(filepath.Join(directory, base.SecretFile)); err != nil {
		t.Fatal(err)
	}

	base.Type = "unsupported"
	base.SecretFile = ""
	writeMetadata(base)
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "type is unsupported") {
		t.Fatalf("unsupported credential type returned %v", err)
	}
	base.Type = "bearer"
	base.SecretFile = "partner.token"
	base.CertificateFile = "unexpected.pem"
	writeMetadata(base)
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "metadata is invalid") {
		t.Fatalf("bearer certificate input returned %v", err)
	}
	base.Type = "api_key"
	writeMetadata(base)
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "metadata is invalid") {
		t.Fatalf("API-key certificate input returned %v", err)
	}

	base = credentialMetadata{SchemaVersion: 1, TenantID: routerTestTenant, Revision: "version-two", Origin: request.Origin, Type: "mtls", CertificateFile: "client.pem", PrivateKeyFile: "client.key", SecretFile: "unexpected"}
	writeMetadata(base)
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "metadata is invalid") {
		t.Fatalf("mTLS secret input returned %v", err)
	}
	base.SecretFile = ""
	writeMetadata(base)
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "certificate is unavailable") {
		t.Fatalf("missing client certificate returned %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, base.CertificateFile), []byte("invalid certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "private key is unavailable") {
		t.Fatalf("missing client private key returned %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, base.PrivateKeyFile), []byte("invalid private key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "certificate is invalid") {
		t.Fatalf("invalid client key pair returned %v", err)
	}

	if err := os.WriteFile(filepath.Join(directory, "partner.json"), make([]byte, 64<<10+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "binding is unavailable") {
		t.Fatalf("oversized credential metadata returned %v", err)
	}
	request.Binding = "missing"
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "binding is unavailable") {
		t.Fatalf("missing credential metadata returned %v", err)
	}
}

func TestCredentialMetadataGenerationMustRemainStable(t *testing.T) {
	directory := t.TempDir()
	metadataPath := filepath.Join(directory, "partner.json")
	original := []byte(`{"schemaVersion":1,"revision":"version-one"}`)
	if err := os.WriteFile(metadataPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCredentialMetadata(metadataPath, original); err != nil {
		t.Fatalf("stable metadata returned %v", err)
	}
	if err := os.WriteFile(metadataPath, []byte(`{"schemaVersion":1,"revision":"version-two"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCredentialMetadata(metadataPath, original); err == nil || strings.Contains(err.Error(), directory) {
		t.Fatalf("changed metadata returned %v", err)
	}
	if err := os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	if err := verifyCredentialMetadata(metadataPath, original); err == nil || strings.Contains(err.Error(), directory) {
		t.Fatalf("removed metadata returned %v", err)
	}
}

func writeCredentialFixture(t *testing.T, directory, binding, revision, origin, secretFile, secret string) {
	t.Helper()
	metadata := credentialMetadata{SchemaVersion: 1, TenantID: routerTestTenant, Revision: revision, Origin: origin, Type: "bearer", SecretFile: secretFile}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, binding+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, secretFile), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
