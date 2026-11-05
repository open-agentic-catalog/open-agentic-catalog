// Router units cover credential admission and local trust without contacting a
// destination. Valid credentials stop at an intentional fetcher input rejection.
package fetcher

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRouterCredentialPreflightAndHeaderOwnership(t *testing.T) {
	_, _, certPEM, keyPEM := privateTLSFixtures(t)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	resolverFailure := errors.New("resolver unavailable")
	for _, tc := range []struct {
		name                                    string
		change                                  func(*Request, *ResolvedCredential)
		want                                    error
		message                                 string
		noResolver, skipResolve, resolveFailure bool
	}{
		{name: "missing resolver", noResolver: true, skipResolve: true, message: "source credential resolution is unavailable"},
		{name: "missing tenant", change: func(r *Request, _ *ResolvedCredential) { r.TenantID = "" }, skipResolve: true, message: "requires a tenant"},
		{name: "malformed URL", change: func(r *Request, _ *ResolvedCredential) { r.URL = "https://%zz" }, skipResolve: true, want: ErrInvalidTarget},
		{name: "secret URL query", change: func(r *Request, _ *ResolvedCredential) { r.URL += "?access_token=fixture-private" }, skipResolve: true, want: ErrInvalidTarget},
		{name: "resolver error", resolveFailure: true, want: resolverFailure},
		{name: "wrong credential origin", change: func(_ *Request, c *ResolvedCredential) { c.Origin = "https://other.example:443" }, want: ErrPolicyDenied, message: "different origin"},
		{name: "missing revision", change: func(_ *Request, c *ResolvedCredential) { c.Revision = "" }, want: ErrPolicyDenied, message: "identity"},
		{name: "revision whitespace", change: func(_ *Request, c *ResolvedCredential) { c.Revision = " revision-1" }, want: ErrPolicyDenied, message: "identity"},
		{name: "revision control", change: func(_ *Request, c *ResolvedCredential) { c.Revision = "revision\x7f" }, want: ErrPolicyDenied, message: "identity"},
		{name: "revision length", change: func(_ *Request, c *ResolvedCredential) { c.Revision = strings.Repeat("a", 129) }, want: ErrPolicyDenied, message: "identity"},
		{name: "stale credential revision", change: func(r *Request, _ *ResolvedCredential) { r.ExpectedCredentialRevision = "old" }, want: ErrCredentialChanged},
		{name: "empty credential", change: func(_ *Request, c *ResolvedCredential) { c.Headers = nil }, want: ErrPolicyDenied, message: "shape"},
		{name: "multiple headers", change: func(_ *Request, c *ResolvedCredential) { c.Headers["X-Api-Key"] = "fixture-private" }, want: ErrPolicyDenied, message: "shape"},
		{name: "multiple certificates", change: func(_ *Request, c *ResolvedCredential) {
			c.Headers = nil
			c.Certificates = []tls.Certificate{pair, pair}
		}, want: ErrPolicyDenied, message: "shape"},
		{name: "mixed mechanisms", change: func(_ *Request, c *ResolvedCredential) { c.Certificates = []tls.Certificate{pair} }, want: ErrPolicyDenied, message: "shape"},
		{name: "unsupported header", change: func(_ *Request, c *ResolvedCredential) { c.Headers = map[string]string{"X-Private": "fixture-private"} }, want: ErrPolicyDenied, message: "unsupported"},
		{name: "authorization collision", change: func(r *Request, _ *ResolvedCredential) { r.Headers["aUtHoRiZaTiOn"] = "fixture-private-caller" }, want: ErrPolicyDenied, message: "conflicts"},
		{name: "API key collision", change: func(r *Request, c *ResolvedCredential) {
			r.Headers["x-api-key"] = "fixture-private-caller"
			c.Headers = map[string]string{"X-API-KEY": "fixture-private"}
		}, want: ErrPolicyDenied, message: "conflicts"},
		{name: "bearer accepted with nil headers", change: func(r *Request, _ *ResolvedCredential) { r.Headers = nil }, message: "fetcher supports GET and POST only"},
		{name: "API key accepted", change: func(_ *Request, c *ResolvedCredential) { c.Headers = map[string]string{"x-api-key": "fixture-private"} }, message: "fetcher supports GET and POST only"},
		{name: "mTLS accepted", change: func(_ *Request, c *ResolvedCredential) { c.Headers = nil; c.Certificates = []tls.Certificate{pair} }, message: "fetcher supports GET and POST only"},
		{name: "invalid header value", change: func(r *Request, c *ResolvedCredential) {
			r.Method = http.MethodGet
			c.Headers["Authorization"] = "fixture-private\r\nInjected: value"
		}, message: `request header "Authorization" is invalid`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := validRouterPolicy()
			policy.Public = PublicRoute{Mode: PublicAllowlist, Origins: []string{"https://public.example"}}
			// DELETE is rejected by Fetcher before creating an HTTP request or resolving
			// DNS. This distinguishes successful credential admission from transmission.
			request := Request{URL: "https://PUBLIC.EXAMPLE:443/catalog", Method: http.MethodDelete, TenantID: routerTestTenant, CredentialBinding: "partner", ExpectedPolicyDigest: policy.Digest, ExpectedCredentialRevision: "revision-1", Headers: map[string]string{"Ce-Id": "caller-id"}}
			credential := ResolvedCredential{Origin: "https://public.example:443", Revision: "revision-1", Headers: map[string]string{"Authorization": "Bearer fixture-private"}}
			if tc.change != nil {
				tc.change(&request, &credential)
			}
			beforeHeaders, beforeCredential := maps.Clone(request.Headers), maps.Clone(credential.Headers)
			calls := 0
			var resolver CredentialResolver = credentialResolverFunc(func(_ context.Context, got CredentialRequest) (ResolvedCredential, error) {
				calls++
				want := CredentialRequest{TenantID: routerTestTenant, Binding: "partner", Origin: "https://public.example:443"}
				if got != want {
					t.Errorf("credential request = %#v, want %#v", got, want)
				}
				if tc.resolveFailure {
					return ResolvedCredential{}, resolverFailure
				}
				return credential, nil
			})
			if tc.noResolver {
				resolver = nil
			}
			router, err := NewRouter(policy, resolver)
			if err != nil {
				t.Fatal(err)
			}
			result, err := router.Fetch(canceledDialContext(t), request)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || !strings.Contains(err.Error(), tc.message) || strings.Contains(err.Error(), "fixture-private") || !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("preflight result=%#v, error=%v", result, err)
			}
			wantCalls := 1
			if tc.skipResolve {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("resolver calls=%d, want %d", calls, wantCalls)
			}
			if !reflect.DeepEqual(request.Headers, beforeHeaders) || !reflect.DeepEqual(credential.Headers, beforeCredential) {
				t.Fatal("credential injection mutated caller or resolver headers")
			}
		})
	}
}

func TestRouterPrivatePrefixContainment(t *testing.T) {
	for _, tc := range []struct {
		name, prefix         string
		testAddresses, valid bool
	}{
		{"RFC1918", "10.42.0.0/16", false, true},
		{"IPv6 private", "fd12::/32", false, true},
		{"IPv4 loopback fixture", "127.0.0.0/8", true, true},
		{"IPv6 loopback fixture", "::1/128", true, true},
		{"loopback without permission", "127.0.0.0/8", false, false},
		{"public even with test permission", "8.8.8.0/24", true, false},
		{"contains public IPv4", "10.0.0.0/7", false, false},
		{"contains non-loopback IPv4", "126.0.0.0/7", true, false},
		{"contains non-loopback IPv6", "::/127", true, false},
		{"contains non-private IPv6", "f800::/6", false, false},
		{"mapped IPv4", "::ffff:10.0.0.0/104", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := validRouterPolicy()
			policy.PrivateRoutes = []PrivateRoute{{Name: "private", Origins: []string{"https://private.example"}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix(tc.prefix)}, AssignedTenantIDs: []string{routerTestTenant}, AllowTestAddresses: tc.testAddresses}}
			router, err := NewRouter(policy, nil)
			if (err == nil) != tc.valid || (router != nil) != tc.valid {
				t.Fatalf("private range admission: router=%v, error=%v", router != nil, err)
			}
		})
	}
}

func TestRouterPrivateTrustScopeAndCombinedBound(t *testing.T) {
	caPEM, _, clientPEM, _ := privateTLSFixtures(t)
	block, _ := pem.Decode(clientPEM)
	if block == nil {
		t.Fatal("missing client fixture certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	publicRoots, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	profile := PrivateRoute{Name: "private", Origins: []string{"https://private.example"}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")}, AssignedTenantIDs: []string{routerTestTenant}}
	for _, fileInput := range []bool{false, true} {
		t.Run(fmt.Sprintf("file=%v", fileInput), func(t *testing.T) {
			trusted := profile
			if fileInput {
				trusted.TrustBundleFile = filepath.Join(t.TempDir(), "private-ca.pem")
				if err := os.WriteFile(trusted.TrustBundleFile, caPEM, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				trusted.TrustBundlePEM = caPEM
			}
			untrusted := profile
			untrusted.Name = "untrusted"
			policy := validRouterPolicy()
			policy.PrivateRoutes = []PrivateRoute{trusted, untrusted}
			router, err := NewRouter(policy, nil)
			if err != nil {
				t.Fatal(err)
			}
			options := x509.VerifyOptions{Roots: router.private["private"].rootCAs, CurrentTime: leaf.NotBefore.Add(time.Minute), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
			if options.Roots == nil {
				t.Fatal("private trust was not loaded")
			}
			if _, err := leaf.Verify(options); err != nil {
				t.Fatalf("private root did not verify its leaf: %v", err)
			}
			if router.public.rootCAs != nil || router.private["untrusted"].rootCAs != nil {
				t.Fatal("private root escaped its assigned profile")
			}
			options.Roots = publicRoots
			if _, err := leaf.Verify(options); err == nil {
				t.Fatal("private leaf unexpectedly trusted by public roots")
			}
			after, err := x509.SystemCertPool()
			if err != nil || !after.Equal(publicRoots) {
				t.Fatal("router mutated system trust")
			}
		})
	}
	// Four exact-size bundles meet the combined limit; one more certificate
	// exceeds it even though every individual input is within its own limit.
	padded := append(bytes.Clone(caPEM), bytes.Repeat([]byte(" "), (4<<20)-len(caPEM))...)
	policy := validRouterPolicy()
	for i := range 4 {
		next := profile
		next.Name = fmt.Sprintf("private-%d", i)
		next.TrustBundlePEM = padded
		policy.PrivateRoutes = append(policy.PrivateRoutes, next)
	}
	if _, err := NewRouter(policy, nil); err != nil {
		t.Fatalf("exact combined trust limit: %v", err)
	}
	extra := profile
	extra.Name = "extra"
	extra.TrustBundlePEM = caPEM
	policy.PrivateRoutes = append(policy.PrivateRoutes, extra)
	if router, err := NewRouter(policy, nil); err == nil || router != nil || !strings.Contains(err.Error(), "combined size") {
		t.Fatalf("combined trust overflow: %v", err)
	}
}

func TestFileCredentialResolverLoadsBoundCredentials(t *testing.T) {
	directory := t.TempDir()
	resolver, err := NewFileCredentialResolver(directory)
	if err != nil {
		t.Fatal(err)
	}
	// The longest permitted binding and leaf names remain valid local inputs.
	request := CredentialRequest{TenantID: routerTestTenant, Binding: strings.Repeat("a", 128), Origin: "https://catalog.example:443"}
	writeCredentialFixture(t, directory, request.Binding, "bearer-1", "https://CATALOG.EXAMPLE/", strings.Repeat("s", 128), "fixture-private")
	got, err := resolver.Resolve(t.Context(), request)
	want := ResolvedCredential{Origin: request.Origin, Revision: "bearer-1", Headers: map[string]string{"Authorization": "Bearer fixture-private"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("bearer resolution mismatch: %v", err)
	}
	_, _, clientPEM, keyPEM := privateTLSFixtures(t)
	for name, raw := range map[string][]byte{"client.pem": clientPEM, "client.key": keyPEM} {
		if err := os.WriteFile(filepath.Join(directory, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	metadata := credentialMetadata{SchemaVersion: 1, TenantID: routerTestTenant, Revision: "mtls-2", Origin: request.Origin, Type: "mtls", CertificateFile: "client.pem", PrivateKeyFile: "client.key", ExpiresAt: "9999-12-31T23:59:59Z"}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, request.Binding+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err = resolver.Resolve(t.Context(), request)
	if err != nil || got.Origin != request.Origin || got.Revision != metadata.Revision || len(got.Headers) != 0 || len(got.Certificates) != 1 {
		t.Fatalf("mTLS resolution mismatch: %v", err)
	}
	expected, err := tls.X509KeyPair(clientPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Certificates[0], expected) {
		t.Fatal("resolver returned a different client identity")
	}
	if err := os.Remove(filepath.Join(directory, request.Binding+".json")); err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.Resolve(t.Context(), request); err == nil || !reflect.DeepEqual(got, ResolvedCredential{}) || strings.Contains(err.Error(), directory) {
		t.Fatalf("revoked binding returned a credential or unsafe error: %v", err)
	}
}
