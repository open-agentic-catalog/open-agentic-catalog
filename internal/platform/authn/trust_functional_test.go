//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package authn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/webhooksender"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

func TestOIDCPrivateTrustIsTransportScoped(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oidcTestKeySet("test-key", &key.PublicKey))
	}))
	defer keys.Close()
	keys.Config.ErrorLog = log.New(io.Discard, "", 0)
	var keyEndpoint atomic.Value
	keyEndpoint.Store(keys.URL + "/keys")
	var issuer string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": keyEndpoint.Load()})
	}))
	defer server.Close()
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	issuer = server.URL
	path := writeOIDCTrustFile(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	const tenant = "00000000-0000-7000-8000-000000000001"
	resolver := resolverFunc(func(_ context.Context, p identity.Principal) (identity.Actor, error) {
		return identity.Actor{TenantID: p.TenantID, Subject: p.Subject, Roles: []identity.Role{identity.RoleReader}}, nil
	})
	cfg := config.Auth{Mode: "oidc", Issuer: issuer, Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: tenant}
	if _, err := New(t.Context(), cfg, resolver); err == nil {
		t.Fatal("private issuer was accepted without configured trust")
	}
	cfg.TrustBundleFile = path
	verifier, err := newOIDC(t.Context(), cfg, resolver, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	loadedDigest := verifier.TrustBundleDigest()
	if !strings.HasPrefix(loadedDigest, "sha256:") {
		t.Fatal("verifier omitted the loaded trust digest")
	}
	now := time.Now()
	token := signOIDCTestToken(t, key, map[string]any{"iss": issuer, "sub": "reader", "aud": "catalog", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	actor, err := verifier.Authenticate(t.Context(), token)
	if err != nil || actor.TenantID != tenant || actor.Subject != "reader" {
		t.Fatalf("private HTTPS discovery and different-origin keys = %#v, %v", actor, err)
	}
	boundedTransport, ok := verifier.client.Transport.(oidcResponseTransport)
	if !ok {
		t.Fatal("OIDC response boundary was not retained")
	}
	transport, ok := boundedTransport.base.(*http.Transport)
	if !ok {
		t.Fatal("OIDC transport was not isolated")
	}
	defer transport.CloseIdleConnections()
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("private trust disabled certificate verification")
	}
	// Changing the input file cannot mutate the already-loaded transport.
	if err := os.WriteFile(path, []byte("invalid replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.discoverVerifier(t.Context()); err != nil {
		t.Fatalf("startup-loaded trust unexpectedly changed: %v", err)
	}
	if verifier.TrustBundleDigest() != loadedDigest {
		t.Fatal("mounted-file replacement changed the retained trust digest")
	}
	if _, err := New(t.Context(), cfg, resolver); err == nil {
		t.Fatal("a new verifier accepted the invalid replacement bundle")
	}
	for _, client := range []*http.Client{{Transport: http.DefaultTransport, Timeout: time.Second}, newOIDCHTTPClient(issuer)} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, issuer, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request) // #nosec G107 -- invocation-owned HTTPS server.
		if response != nil {
			_ = response.Body.Close()
		}
		var certificateError *tls.CertificateVerificationError
		if !errors.As(err, &certificateError) {
			t.Fatalf("unrelated client must reject private CA: %v", err)
		}
	}
	outbound, err := fetcher.New(fetcher.Policy{Timeout: time.Second, MaxResponseBytes: 1024, AllowTestAddresses: true, AllowedAddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var isolatedCertificateError *tls.CertificateVerificationError
	if _, err := outbound.Fetch(t.Context(), fetcher.Request{URL: issuer}); !errors.As(err, &isolatedCertificateError) {
		t.Fatalf("unrelated federation transport must reject the OIDC CA: %v", err)
	}
	sender, err := webhooksender.New(outbound)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Send(t.Context(), webhook.SendRequest{URL: issuer}); !errors.As(err, &isolatedCertificateError) {
		t.Fatalf("unrelated webhook transport must reject the OIDC CA: %v", err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, strings.Replace(issuer, "127.0.0.1", "localhost", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := verifier.client.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	var hostnameError x509.HostnameError
	if !errors.As(err, &hostnameError) {
		t.Fatalf("trusted issuer with mismatched hostname = %v", err)
	}
	// A different private root on a discovered key endpoint remains untrusted.
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	untrusted := httptest.NewUnstartedServer(keys.Config.Handler)
	untrusted.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: privateKey}}}
	untrusted.Config.ErrorLog = log.New(io.Discard, "", 0)
	untrusted.StartTLS()
	defer untrusted.Close()
	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, untrusted.URL+"/keys", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = verifier.client.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	var certificateError *tls.CertificateVerificationError
	if !errors.As(err, &certificateError) {
		t.Fatalf("untrusted key endpoint = %v", err)
	}
	// Also exercise the verifier's discovered-key path, not just its client.
	// A fresh verifier cannot satisfy this token from the earlier key cache.
	keyEndpoint.Store(untrusted.URL + "/keys")
	cfg.TrustBundleFile = writeOIDCTrustFile(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	fresh, err := New(t.Context(), cfg, resolver)
	if err != nil {
		t.Fatalf("trusted discovery with an untrusted key endpoint: %v", err)
	}
	// The verifier preserves a safe dependency classification. The direct
	// transport assertion above retains the typed TLS check.
	if _, err := fresh.Authenticate(t.Context(), token); err == nil || identity.ErrorCodeOf(err) != identity.CodeDependency {
		t.Fatalf("authentication through untrusted discovered keys = %v", err)
	}
}
