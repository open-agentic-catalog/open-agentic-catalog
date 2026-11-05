//go:build functional

package fetcher

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
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

// Count accepted connections, not just HTTP requests. Retain native verification
// errors before HTTP admission. Handshake deadlines also bound fixture cleanup.
type sourceTLSListener struct {
	net.Listener
	contacts atomic.Int64
	rejected chan error
}

func (l *sourceTLSListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.contacts.Add(1)
		peer, ok := connection.(*tls.Conn)
		if !ok {
			_ = connection.Close()
			return nil, errors.New("source fixture listener requires TLS")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = peer.HandshakeContext(ctx)
		cancel()
		if err == nil {
			return peer, nil
		}
		_ = peer.Close()
		select {
		case l.rejected <- err:
		default:
		}
	}
}

func TestRouterSeparatesMTLSCertificateExpiryFromBindingExpiry(t *testing.T) {
	ca, serverIdentity, issue := privateTLSClientFixtures(t)
	first, firstKey := issue(3, nil)
	expired, expiredKey := issue(4, func(v *x509.Certificate) {
		v.NotBefore, v.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	})
	successor, successorKey := issue(5, nil)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("private source authority is unavailable")
	}
	var handlerCalls atomic.Int64
	received := make(chan []byte, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalls.Add(1)
		if r.URL.Path != "/catalog" || r.TLS == nil || len(r.TLS.PeerCertificates) != 1 || len(r.TLS.VerifiedChains) != 1 || r.TLS.DidResume {
			t.Error("source received an unverified identity or incorrect request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case received <- r.TLS.PeerCertificates[0].Raw:
		default:
			t.Error("source receipt bound exceeded")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connection", "close")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	listener := &sourceTLSListener{Listener: tls.NewListener(server.Listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverIdentity}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}), rejected: make(chan error, 8)}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	endpoint := strings.Replace(server.URL, "http://", "https://", 1)
	origin, err := NormalizeOrigin(endpoint, false)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	trustFile := filepath.Join(directory, "source-ca.pem")
	if err := os.WriteFile(trustFile, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewFileCredentialResolver(directory)
	if err != nil {
		t.Fatal(err)
	}
	var resolutions atomic.Int64
	countedResolver := credentialResolverFunc(func(ctx context.Context, request CredentialRequest) (ResolvedCredential, error) {
		resolutions.Add(1)
		return resolver.Resolve(ctx, request)
	})
	policy := RoutingPolicy{ID: "source-mtls", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64), Public: PublicRoute{Mode: PublicDisabled},
		PrivateRoutes: []PrivateRoute{{Name: "private-source", Origins: []string{origin}, AssignedTenantIDs: []string{routerTestTenant}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, TrustBundleFile: trustFile, AllowTestAddresses: true}},
		Timeout:       time.Second, MaxResponseBytes: 1024, MaxRedirects: 1}
	router, err := NewRouter(policy, countedResolver)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{URL: endpoint + "/catalog", TenantID: routerTestTenant, NetworkProfile: "private-source", CredentialBinding: "partner", ExpectedPolicyDigest: policy.Digest}
	fetchValid := func(revision string, certificate []byte) {
		t.Helper()
		before := listener.contacts.Load()
		result, err := router.Fetch(t.Context(), request)
		if err != nil || result.Status != http.StatusOK || string(result.Body) != `{"ok":true}` || result.CredentialRevision != revision || result.NetworkProfile != request.NetworkProfile || result.PolicyID != policy.ID || result.PolicyRevision != policy.Revision || result.PolicyDigest != policy.Digest || listener.contacts.Load() != before+1 {
			t.Fatalf("fresh credentialed fetch lost its data, route or generation: %v", err)
		}
		block, _ := pem.Decode(certificate)
		select {
		case actual := <-received:
			if !bytes.Equal(actual, block.Bytes) {
				t.Fatal("source received a different client certificate")
			}
		default:
			t.Fatal("successful fetch has no verified peer receipt")
		}
	}

	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	firstMetadata := stageMTLSCredentialFixture(t, directory, origin, "generation-one", future, first, firstKey)
	publishCredentialMetadataFixture(t, directory, request.CredentialBinding, firstMetadata)
	fetchValid(firstMetadata.Revision, first)

	// Stage the complete successor without publishing it. The old generation
	// still works, and both valid leaves remain acceptable at the same peer.
	next := stageMTLSCredentialFixture(t, directory, origin, "generation-four", future, successor, successorKey)
	fetchValid(firstMetadata.Revision, first)
	invalidLeaf := stageMTLSCredentialFixture(t, directory, origin, "generation-two", future, expired, expiredKey)
	publishCredentialMetadataFixture(t, directory, request.CredentialBinding, invalidLeaf)
	resolved, err := resolver.Resolve(t.Context(), CredentialRequest{TenantID: request.TenantID, Binding: request.CredentialBinding, Origin: origin})
	block, _ := pem.Decode(expired)
	if err != nil || resolved.Revision != invalidLeaf.Revision || len(resolved.Certificates) != 1 || !bytes.Equal(resolved.Certificates[0].Certificate[0], block.Bytes) {
		t.Fatalf("expired-leaf specimen failed before peer certificate verification: %v", err)
	}
	beforeContacts, beforeHandlers := listener.contacts.Load(), handlerCalls.Load()
	result, err := router.Fetch(t.Context(), request)
	if err == nil || listener.contacts.Load() != beforeContacts+1 || handlerCalls.Load() != beforeHandlers || result.Status != 0 || len(result.Body) != 0 || result.CredentialRevision != "" {
		t.Fatal("expired client certificate did not fail before HTTP disclosure")
	}
	if strings.Contains(err.Error(), directory) || strings.Contains(err.Error(), "PRIVATE KEY") || strings.Contains(err.Error(), "BEGIN CERTIFICATE") {
		t.Fatal("TLS refusal disclosed mounted credential data")
	}
	observation, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	select {
	case handshakeErr := <-listener.rejected:
		var verification *tls.CertificateVerificationError
		var invalid x509.CertificateInvalidError
		if !errors.As(handshakeErr, &verification) || len(verification.UnverifiedCertificates) != 1 || !bytes.Equal(verification.UnverifiedCertificates[0].Raw, block.Bytes) || !errors.As(verification.Err, &invalid) || invalid.Reason != x509.Expired {
			t.Fatalf("source did not reject the exact leaf at its X.509 expiry guard: %v", handshakeErr)
		}
	case <-observation.Done():
		t.Fatal("expired certificate has no peer verification evidence")
	}

	oldExpiry := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	invalidMetadata := stageMTLSCredentialFixture(t, directory, origin, "generation-three", oldExpiry, first, firstKey)
	publishCredentialMetadataFixture(t, directory, request.CredentialBinding, invalidMetadata)
	beforeContacts = listener.contacts.Load()
	_, err = router.Fetch(t.Context(), request)
	if err == nil || err.Error() != "resolve source credential: credential binding has expired" || listener.contacts.Load() != beforeContacts || handlerCalls.Load() != beforeHandlers {
		t.Fatalf("expired metadata did not stop the request before contact: %v", err)
	}

	// The same router and resolver read this atomic publication on their next
	// request. Neither the tenant, origin, network profile nor policy changes.
	publishCredentialMetadataFixture(t, directory, request.CredentialBinding, next)
	fetchValid(next.Revision, successor)
	for _, name := range []string{firstMetadata.CertificateFile, firstMetadata.PrivateKeyFile} {
		retained, err := os.ReadFile(filepath.Join(directory, name)) // #nosec G304 -- Exact fixture-owned leaf names inside t.TempDir; no external paths.
		wanted := first
		if name == firstMetadata.PrivateKeyFile {
			wanted = firstKey
		}
		if err != nil || !bytes.Equal(retained, wanted) {
			t.Fatal("successor publication modified an old credential leaf")
		}
	}
	for _, kind := range []string{"tenant", "origin", "profile", "stale-revision"} {
		t.Run(kind, func(t *testing.T) {
			denied := request
			want := ErrPolicyDenied
			switch kind {
			case "tenant":
				denied.TenantID = "00000000-0000-7000-8000-000000000002"
			case "origin":
				denied.URL = strings.Replace(endpoint, "127.0.0.1", "localhost", 1) + "/catalog"
			case "profile":
				denied.NetworkProfile = "unassigned"
			case "stale-revision":
				denied.ExpectedCredentialRevision = firstMetadata.Revision
				want = ErrCredentialChanged
			}
			beforeContacts, beforeResolutions := listener.contacts.Load(), resolutions.Load()
			if _, err := router.Fetch(t.Context(), denied); !errors.Is(err, want) || listener.contacts.Load() != beforeContacts || handlerCalls.Load() != beforeHandlers+1 {
				t.Fatalf("scope or revision refusal made contact or returned the wrong error: %v", err)
			}
			if kind != "stale-revision" && resolutions.Load() != beforeResolutions {
				t.Fatal("route refusal reached credential resolution")
			}
		})
	}
	fetchValid(next.Revision, successor)
}
