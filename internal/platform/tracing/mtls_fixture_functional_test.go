//go:build functional

package tracing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

type collectorIdentity struct {
	pair             tls.Certificate
	certificate, key []byte
}

func signCollectorIdentity(t *testing.T, template *x509.Certificate, issuer *collectorIdentity) collectorIdentity {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	parent, signer := template, private
	if issuer != nil {
		parent = issuer.pair.Leaf
		var ok bool
		signer, ok = issuer.pair.PrivateKey.(ed25519.PrivateKey)
		if !ok {
			t.Fatal("unexpected fixture signing key")
		}
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, parent, public, signer)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	identity := collectorIdentity{certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})}
	identity.pair, err = tls.X509KeyPair(identity.certificate, identity.key)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func newCollectorCA(t *testing.T, name string) collectorIdentity {
	t.Helper()
	now := time.Now()
	return signCollectorIdentity(t, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}, nil)
}

func newCollectorLeaf(t *testing.T, ca *collectorIdentity, serial int64, usage x509.ExtKeyUsage, change func(*x509.Certificate)) collectorIdentity {
	t.Helper()
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
	if usage == x509.ExtKeyUsageServerAuth {
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	if change != nil {
		change(template)
	}
	return signCollectorIdentity(t, template, ca)
}

func collectorRoots(authorities ...collectorIdentity) *x509.CertPool {
	roots := x509.NewCertPool()
	for _, authority := range authorities {
		roots.AddCert(authority.pair.Leaf)
	}
	return roots
}

// Complete the real handshake before HTTP admission, retaining typed TLS errors.
// Failed peers never enter the OTLP handler. Successful peers remain *tls.Conn,
// so net/http retains normal TLS state and verified chains on the request.
type verifyingCollectorListener struct {
	net.Listener
	configuration atomic.Pointer[tls.Config]
	failures      chan error
	ctx           context.Context //nolint:containedctx // net.Listener.Accept has no context parameter; Close cancels this owned handshake lifetime.
	cancel        context.CancelFunc
}

func (l *verifyingCollectorListener) Accept() (net.Conn, error) {
	for {
		raw, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		connection := tls.Server(raw, l.configuration.Load())
		ctx, cancel := context.WithTimeout(l.ctx, 2*time.Second)
		err = connection.HandshakeContext(ctx)
		cancel()
		if err == nil {
			return connection, nil
		}
		_ = connection.Close()
		select {
		case l.failures <- err:
		case <-l.ctx.Done():
			return nil, l.ctx.Err()
		}
	}
}

func (l *verifyingCollectorListener) Close() error {
	l.cancel()
	return l.Listener.Close()
}

type collectorFixture struct {
	server   *httptest.Server
	listener *verifyingCollectorListener
	received chan []byte
	calls    atomic.Int32
}

func newVerifyingCollector(t *testing.T, serverIdentity collectorIdentity, clientRoots *x509.CertPool) *collectorFixture {
	t.Helper()
	f := &collectorFixture{received: make(chan []byte, 16)}
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path != "/v1/traces" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-protobuf" || r.TLS == nil || len(r.TLS.PeerCertificates) != 1 || len(r.TLS.VerifiedChains) != 1 || r.TLS.DidResume {
			t.Error("collector received an unverified identity or incorrect OTLP request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		var data collector.ExportTraceServiceRequest
		if err != nil || len(raw) > 1<<20 || proto.Unmarshal(raw, &data) != nil || len(data.ResourceSpans) != 1 || len(data.ResourceSpans[0].ScopeSpans) != 1 || len(data.ResourceSpans[0].ScopeSpans[0].Spans) != 1 {
			t.Error("collector did not receive one real OTLP span")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case f.received <- r.TLS.PeerCertificates[0].Raw:
		default:
			t.Error("collector receipt bound exceeded")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Every export requires another handshake, including authority retirement.
		w.Header().Set("Connection", "close")
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	f.listener = &verifyingCollectorListener{Listener: f.server.Listener, failures: make(chan error, 16), ctx: ctx, cancel: cancel}
	f.setTLS(serverIdentity, clientRoots)
	f.server.Listener = f.listener
	f.server.Start()
	t.Cleanup(f.server.Close)
	return f
}

func (f *collectorFixture) setTLS(serverIdentity collectorIdentity, clientRoots *x509.CertPool) {
	f.listener.configuration.Store(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverIdentity.pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientRoots})
}

func (f *collectorFixture) config(t *testing.T, serverCA collectorIdentity, client *collectorIdentity) config.Tracing {
	t.Helper()
	c := config.Defaults().Tracing
	c.Enabled, c.SampleRatio, c.Installation = true, 1, "collector-mtls-fixture"
	c.Endpoint = strings.Replace(f.server.URL, "http://", "https://", 1) + "/v1/traces"
	c.AllowedAddressPrefixes = []string{"127.0.0.0/8"}
	c.ExportTimeout.Duration = time.Second
	c.CAFile = inputFile(t, "collector-ca.pem", serverCA.certificate)
	if client != nil {
		c.ClientCertificateFile = inputFile(t, "client.pem", client.certificate)
		c.ClientKeyFile = inputFile(t, "client-key.pem", client.key)
	}
	return c
}

func newCollectorProvider(t *testing.T, c config.Tracing) (*Provider, *observability.Registry) {
	t.Helper()
	metrics := observability.New(time.Now)
	p, err := New(t.Context(), c, config.RoleAPI, "fixture", "00000000-0000-7000-8000-000000000001", nil, metrics)
	if err != nil {
		t.Fatalf("valid certificate/key inputs failed before peer verification: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := p.Shutdown(ctx); err != nil {
			t.Error("collector provider cleanup failed")
		}
	})
	return p, metrics
}

func flushCollectorSpan(t *testing.T, p *Provider, success bool) {
	t.Helper()
	p.client.CloseIdleConnections()
	_, end := p.Start(t.Context(), "collector identity fixture", catalog.TraceServer, "")
	end("ok")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	started := time.Now()
	err := p.provider.ForceFlush(ctx)
	if time.Since(started) > 3*time.Second || errors.Is(err, context.DeadlineExceeded) || (err == nil) != success {
		t.Fatalf("bounded export success=%v, error=%v", success, err)
	}
}
