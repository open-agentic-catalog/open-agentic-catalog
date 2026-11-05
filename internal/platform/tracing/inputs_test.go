package tracing

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

type tracingCertificateFixture struct {
	root, leaf               *x509.Certificate
	rootPEM, leafPEM, keyPEM []byte
	public                   ed25519.PublicKey
}

// Construct signed inputs independently of the loader, without a TLS listener.
func newTracingCertificateFixture(t *testing.T, name string) tracingCertificateFixture {
	t.Helper()
	rootPublic, rootPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	notBefore := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	rootTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name + " root"},
		NotBefore: notBefore, NotAfter: notBefore.AddDate(1, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootPublic, rootPrivate)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: name + " client"},
		NotBefore: notBefore, NotAfter: notBefore.AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, public, rootPrivate)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return tracingCertificateFixture{
		root: root, leaf: leaf, public: public,
		rootPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}),
		leafPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
}

func verifyTracingClient(t *testing.T, leaf *x509.Certificate, roots *x509.CertPool) {
	t.Helper()
	chains, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, CurrentTime: time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil || len(chains) != 1 || len(chains[0]) != 2 {
		t.Fatalf("private client trust chain=%v error=%v", chains, err)
	}
}

func TestMountedTLSInputsLoadMatchingCredentialsAndIsolatedTrust(t *testing.T) {
	fixture := newTracingCertificateFixture(t, "fixture")
	publicRoots, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	expectedRoots := publicRoots.Clone()
	expectedRoots.AddCert(fixture.root)
	c := config.Defaults().Tracing
	c.HeadersFile = inputFile(t, "headers.json", []byte(`{"authorization":"Bearer fixture"}`))
	c.CAFile = inputFile(t, "ca.pem", fixture.rootPEM)
	c.ClientCertificateFile = inputFile(t, "client.pem", fixture.leafPEM)
	c.ClientKeyFile = inputFile(t, "client-key.pem", fixture.keyPEM)
	headers, roots, certificates, err := loadInputs(c)
	if err != nil || len(headers) != 1 || headers["Authorization"] != "Bearer fixture" || roots == nil || len(certificates) != 1 {
		t.Fatalf("valid mounted inputs: headers=%v roots=%v certificates=%d error=%v", headers, roots, len(certificates), err)
	}
	if !roots.Equal(expectedRoots) || roots.Equal(publicRoots) {
		t.Fatal("private CA loading did not preserve public roots and add the supplied root")
	}
	verifyTracingClient(t, fixture.leaf, roots)
	if len(certificates[0].Certificate) != 1 || !bytes.Equal(certificates[0].Certificate[0], fixture.leaf.Raw) {
		t.Fatal("client certificate bytes changed during loading")
	}
	signer, ok := certificates[0].PrivateKey.(crypto.Signer)
	if !ok {
		t.Fatal("loaded client key is not a signer")
	}
	message := []byte("independent client key possession check")
	signature, err := signer.Sign(rand.Reader, message, crypto.Hash(0))
	if err != nil || !ed25519.Verify(fixture.public, message, signature) {
		t.Fatalf("loaded key does not match the independently constructed certificate: %v", err)
	}
	if _, err := fixture.leaf.Verify(x509.VerifyOptions{Roots: publicRoots, CurrentTime: fixture.leaf.NotBefore.Add(time.Hour), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("loading a private root altered the public-only trust pool")
	}

	other := newTracingCertificateFixture(t, "unrelated")
	c.ClientKeyFile = inputFile(t, "mismatched-key.pem", other.keyPEM)
	headers, roots, certificates, err = loadInputs(c)
	if err == nil || err.Error() != "tracing client certificate/key pair is invalid" || headers != nil || roots != nil || certificates != nil {
		t.Fatalf("mismatched valid key returned usable inputs or unsafe error: %v", err)
	}
}

func TestPrivateRootBundleValidatesEveryCertificateBlock(t *testing.T) {
	first := newTracingCertificateFixture(t, "first")
	second := newTracingCertificateFixture(t, "second")
	bundle := append([]byte(" \n\t"), first.rootPEM...)
	bundle = append(bundle, []byte("\n\t")...)
	bundle = append(bundle, second.rootPEM...)
	bundle = append(bundle, []byte(" \n")...)
	roots := x509.NewCertPool()
	if err := addPrivateRoots(roots, bundle); err != nil {
		t.Fatal(err)
	}
	verifyTracingClient(t, first.leaf, roots)
	verifyTracingClient(t, second.leaf, roots)
	third := newTracingCertificateFixture(t, "untrusted")
	if _, err := third.leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: third.leaf.NotBefore.Add(time.Hour), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("bundle trusted an unrelated private CA")
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"PEM headers", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Fixture": "private-sentinel"}, Bytes: second.root.Raw}), "malformed certificate bundle"},
		{"private key block", second.keyPEM, "malformed certificate bundle"},
		{"nested begin", []byte("-----BEGIN CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----"), "malformed certificate bundle"},
		{"invalid DER", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("private-sentinel")}), "invalid certificate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A valid first block must not mask a malformed subsequent block.
			raw := append(bytes.Clone(first.rootPEM), tc.raw...)
			if err := addPrivateRoots(x509.NewCertPool(), raw); err == nil || err.Error() != tc.want {
				t.Fatalf("bundle error=%v, want %q", err, tc.want)
			}
			c := config.Defaults().Tracing
			c.HeadersFile = inputFile(t, "headers.json", []byte(`{"Authorization":"Bearer fixture"}`))
			c.CAFile = inputFile(t, "invalid-ca.pem", raw)
			headers, roots, certificates, err := loadInputs(c)
			if err == nil || err.Error() != "tracing private CA bundle is invalid" || headers != nil || roots != nil || certificates != nil {
				t.Fatalf("invalid bundle returned usable inputs or unsafe error: %v", err)
			}
		})
	}
}

func TestProviderStartupRejectsInvalidInputsWithoutExport(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*config.Tracing)
	}{
		{"invalid config", "outside its bounds", func(c *config.Tracing) { c.SampleRatio = 2 }},
		{"missing headers", "tracing input must be a regular file", func(c *config.Tracing) { c.HeadersFile = filepath.Join(t.TempDir(), "missing") }},
		{"invalid port", "tracing: invalid export transport", func(c *config.Tracing) { c.Endpoint = "https://collector.example:0/v1/traces" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Defaults().Tracing
			c.Enabled, c.Installation, c.Endpoint = true, "fixture", "https://collector.example/v1/traces"
			tc.change(&c)
			p, err := New(t.Context(), c, config.RoleAPI, "fixture", "00000000-0000-7000-8000-000000000001", nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || p != nil {
				t.Fatalf("startup result=%v error=%v, want %q", p, err, tc.want)
			}
		})
	}
}

func TestMountedInputFailuresDoNotExposePathsOrContents(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "private-missing-input")
	invalid := inputFile(t, "private-invalid-input", []byte("private-invalid-content"))
	invalidDER := inputFile(t, "private-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("private-invalid-content")}))
	for _, test := range []struct {
		name string
		set  func(*config.Tracing)
		want string
	}{
		{"missing CA", func(c *config.Tracing) { c.CAFile = missing }, "tracing input must be a regular file"},
		{"invalid CA DER", func(c *config.Tracing) { c.CAFile = invalidDER }, "tracing private CA bundle is invalid"},
		{"missing client certificate", func(c *config.Tracing) { c.ClientCertificateFile, c.ClientKeyFile = missing, invalid }, "tracing input must be a regular file"},
		{"missing client key", func(c *config.Tracing) { c.ClientCertificateFile, c.ClientKeyFile = invalid, missing }, "tracing input must be a regular file"},
		{"invalid client pair", func(c *config.Tracing) { c.ClientCertificateFile, c.ClientKeyFile = invalid, invalid }, "tracing client certificate/key pair is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := config.Defaults().Tracing
			test.set(&c)
			headers, roots, certificates, err := loadInputs(c)
			if err == nil || err.Error() != test.want {
				t.Fatalf("expected fixed safe error %q, got %v", test.want, err)
			}
			if headers != nil || roots != nil || certificates != nil {
				t.Fatal("input failure returned partially usable credentials")
			}
		})
	}
}

func TestAuthenticationHeaderValueBound(t *testing.T) {
	for _, size := range []int{0, 2048, 2049} {
		c := config.Defaults().Tracing
		value := strings.Repeat("s", size)
		c.HeadersFile = inputFile(t, "headers.json", []byte(`{"authorization":"`+value+`"}`))
		headers, _, _, err := loadInputs(c)
		if size == 2048 {
			if err != nil || len(headers) != 1 || headers["Authorization"] != value {
				t.Fatalf("exact bound or header canonicalization failed: %v", err)
			}
		} else if err == nil || err.Error() != "tracing authentication headers are invalid" {
			t.Fatalf("size %d: expected safe rejection, got %v", size, err)
		}
	}
}
