//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package fetcher

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterResolvesAndRotatesOriginBoundCredentials(t *testing.T) {
	var requests atomic.Int64
	var wanted atomic.Value
	wanted.Store("Bearer first-secret")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		want, ok := wanted.Load().(string)
		if !ok {
			t.Error("wanted authorization fixture has an unexpected type")
			return
		}
		if got := request.Header.Get("Authorization"); got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	origin, err := NormalizeOrigin(server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	writeCredentialFixture(t, directory, "partner", "revision-1", origin, "partner.token", "first-secret")
	resolver, err := NewFileCredentialResolver(directory)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RoutingPolicy{
		ID: "local-test", Revision: 1, Digest: "sha256:" + strings.Repeat("c", 64), Public: PublicRoute{Mode: PublicDisabled},
		PrivateRoutes:     []PrivateRoute{{Name: "loopback-test", Origins: []string{origin}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, AssignedTenantIDs: []string{routerTestTenant}, AllowTestAddresses: true}},
		AllowHTTPForTests: true, Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 1,
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{URL: server.URL + "/catalog", TenantID: routerTestTenant, NetworkProfile: "loopback-test", CredentialBinding: "partner"}
	result, err := router.Fetch(context.Background(), request)
	if err != nil || result.CredentialRevision != "revision-1" || result.PolicyID != "local-test" || result.PolicyRevision != 1 || result.PolicyDigest == "" {
		t.Fatalf("credentialed fetch = %#v, %v", result, err)
	}
	wanted.Store("Bearer second-secret")
	writeCredentialFixture(t, directory, "partner", "revision-2", origin, "partner.token", "second-secret")
	result, err = router.Fetch(context.Background(), request)
	if err != nil || result.CredentialRevision != "revision-2" || requests.Load() != 2 {
		t.Fatalf("rotated fetch = %#v, requests=%d, %v", result, requests.Load(), err)
	}
	staleValidator := request
	staleValidator.ExpectedCredentialRevision = "revision-1"
	if _, err := router.Fetch(context.Background(), staleValidator); !errors.Is(err, ErrCredentialChanged) || requests.Load() != 2 {
		t.Fatalf("stale validator credential = requests %d, error %v", requests.Load(), err)
	}
	writeCredentialFixture(t, directory, "other-partner", "other-revision-1", origin, "other-partner.token", "second-secret")
	if err := os.Remove(filepath.Join(directory, "partner.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Fetch(context.Background(), request); err == nil || strings.Contains(err.Error(), directory) || requests.Load() != 2 {
		t.Fatalf("revoked binding = requests %d, error %v", requests.Load(), err)
	}
	request.CredentialBinding = "other-partner"
	result, err = router.Fetch(context.Background(), request)
	if err != nil || result.CredentialRevision != "other-revision-1" || requests.Load() != 3 {
		t.Fatalf("unrelated binding after revocation = %#v, requests=%d, %v", result, requests.Load(), err)
	}
}

func TestRouterRejectsInvalidResolvedCredentialBeforeContact(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	origin, err := NormalizeOrigin(server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	result := ResolvedCredential{Revision: "revision-1", Origin: origin, Headers: map[string]string{"Authorization": "Bearer secret"}}
	var resolutions atomic.Int64
	resolver := credentialResolverFunc(func(context.Context, CredentialRequest) (ResolvedCredential, error) {
		resolutions.Add(1)
		return result, nil
	})
	router, err := NewRouter(RoutingPolicy{
		ID: "credential-validation", Revision: 1, Digest: "sha256:" + strings.Repeat("f", 64), Public: PublicRoute{Mode: PublicDisabled},
		PrivateRoutes:     []PrivateRoute{{Name: "loopback-test", Origins: []string{origin}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, AssignedTenantIDs: []string{routerTestTenant}, AllowTestAddresses: true}},
		AllowHTTPForTests: true, Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 1,
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	base := Request{URL: server.URL, TenantID: routerTestTenant, NetworkProfile: "loopback-test", CredentialBinding: "partner"}
	publicPolicy := validRouterPolicy()
	publicPolicy.Public = PublicRoute{Mode: PublicAllowlist, Origins: []string{"https://public.example"}}
	publicRouter, err := NewRouter(publicPolicy, resolver)
	if err != nil {
		t.Fatal(err)
	}
	publicRequest := Request{URL: "https://public.example/catalog", CredentialBinding: "partner"}
	if _, err := publicRouter.Fetch(context.Background(), publicRequest); err == nil || !strings.Contains(err.Error(), "requires a tenant") {
		t.Fatalf("credential resolution without a tenant returned %v", err)
	}
	publicRequest.TenantID = routerTestTenant
	if _, err := publicRouter.Fetch(context.Background(), publicRequest); err == nil || !strings.Contains(err.Error(), "different origin") {
		t.Fatalf("cross-origin resolved credential returned %v", err)
	}
	withoutResolver, err := NewRouter(publicPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutResolver.Fetch(context.Background(), publicRequest); err == nil || !strings.Contains(err.Error(), "resolution is unavailable") {
		t.Fatalf("missing credential resolver returned %v", err)
	}
	unsafeURL := Request{URL: "https://public.example/catalog?access_token=secret"}
	if _, err := publicRouter.Fetch(context.Background(), unsafeURL); err == nil || resolutions.Load() != 1 {
		t.Fatalf("unsafe URL reached credential resolution: resolutions=%d, error=%v", resolutions.Load(), err)
	}
	conflicting := base
	conflicting.Headers = map[string]string{"authorization": "Bearer caller-value"}
	if _, err := router.Fetch(context.Background(), conflicting); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("case-insensitive credential conflict returned %v", err)
	}
	result.Revision = ""
	if _, err := router.Fetch(context.Background(), base); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("missing credential revision returned %v", err)
	}
	result.Revision = "revision-1"
	result.Headers = nil
	if _, err := router.Fetch(context.Background(), base); err == nil || !strings.Contains(err.Error(), "shape") {
		t.Fatalf("empty resolved credential returned %v", err)
	}
	result.Revision = " revision-1 "
	result.Headers = map[string]string{"Authorization": "Bearer secret"}
	if _, err := router.Fetch(context.Background(), base); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("whitespace credential revision returned %v", err)
	}
	result.Revision = "revision-1"
	result.Headers = map[string]string{"X-Custom-Credential": "secret"}
	if _, err := router.Fetch(context.Background(), base); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported credential header returned %v", err)
	}
	result.Headers = map[string]string{"Authorization": "Bearer secret"}
	result.Certificates = []tls.Certificate{{}}
	if _, err := router.Fetch(context.Background(), base); err == nil || !strings.Contains(err.Error(), "shape") {
		t.Fatalf("mixed credential mechanisms returned %v", err)
	}
	result.Certificates = nil
	beforeDenied := resolutions.Load()
	denied := base
	denied.URL = "http://127.0.0.1:1/catalog"
	if _, err := router.Fetch(context.Background(), denied); err == nil || resolutions.Load() != beforeDenied {
		t.Fatalf("denied origin resolved a credential: resolutions=%d, error=%v", resolutions.Load(), err)
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid credentials contacted the destination %d times", requests.Load())
	}
}

func TestCredentialedRouterRejectsOriginChangingRedirect(t *testing.T) {
	var targetRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetRequests.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, target.URL+"/next", http.StatusFound)
	}))
	defer source.Close()
	sourceOrigin, _ := NormalizeOrigin(source.URL, true)
	targetOrigin, _ := NormalizeOrigin(target.URL, true)
	directory := t.TempDir()
	writeCredentialFixture(t, directory, "partner", "revision-1", sourceOrigin, "partner.token", "secret")
	resolver, err := NewFileCredentialResolver(directory)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RoutingPolicy{
		ID: "redirect-test", Revision: 1, Digest: "sha256:" + strings.Repeat("d", 64), Public: PublicRoute{Mode: PublicDisabled},
		PrivateRoutes:     []PrivateRoute{{Name: "loopback-test", Origins: []string{sourceOrigin, targetOrigin}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, AssignedTenantIDs: []string{routerTestTenant}, AllowTestAddresses: true}},
		AllowHTTPForTests: true, Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2,
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Fetch(context.Background(), Request{URL: source.URL, TenantID: routerTestTenant, NetworkProfile: "loopback-test", CredentialBinding: "partner"})
	if err == nil || !strings.Contains(err.Error(), "origin-changing redirect denied") || targetRequests.Load() != 0 {
		t.Fatalf("credentialed redirect = requests %d, error %v", targetRequests.Load(), err)
	}
}

func TestRouterScopesPrivateCAAndMTLSCredentialToAssignedProfile(t *testing.T) {
	caPEM, serverCertificate, clientCertificate, clientKey := privateTLSFixtures(t)
	clientRoots := x509.NewCertPool()
	if !clientRoots.AppendCertsFromPEM(caPEM) {
		t.Fatal("append private client root")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.TLS == nil || len(request.TLS.PeerCertificates) != 1 || request.TLS.PeerCertificates[0].Subject.CommonName != "catalog-client" {
			t.Error("request did not present the assigned client identity")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientRoots,
	}
	server.StartTLS()
	defer server.Close()

	origin, err := NormalizeOrigin(server.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	trustFile := filepath.Join(directory, "private-ca.pem")
	if err := os.WriteFile(trustFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "client.pem"), clientCertificate, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "client-key.pem"), clientKey, 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := credentialMetadata{SchemaVersion: 1, TenantID: routerTestTenant, Revision: "client-1", Origin: origin, Type: "mtls", CertificateFile: "client.pem", PrivateKeyFile: "client-key.pem"}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "private-client.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewFileCredentialResolver(directory)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RoutingPolicy{
		ID: "private-mtls", Revision: 1, Digest: "sha256:" + strings.Repeat("e", 64), Public: PublicRoute{Mode: PublicDisabled},
		PrivateRoutes: []PrivateRoute{{Name: "private-catalogs", Origins: []string{origin}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, AssignedTenantIDs: []string{routerTestTenant}, TrustBundleFile: trustFile, AllowTestAddresses: true}},
		Timeout:       time.Second, MaxResponseBytes: 1024, MaxRedirects: 1,
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Fetch(context.Background(), Request{URL: server.URL, TenantID: routerTestTenant, NetworkProfile: "private-catalogs"}); err == nil {
		t.Fatal("private endpoint was reached without its assigned mTLS credential")
	}
	result, err := router.Fetch(context.Background(), Request{URL: server.URL, TenantID: routerTestTenant, NetworkProfile: "private-catalogs", CredentialBinding: "private-client"})
	if err != nil || result.Status != http.StatusOK || result.CredentialRevision != "client-1" {
		t.Fatalf("private CA and mTLS fetch = %#v, %v", result, err)
	}
}
