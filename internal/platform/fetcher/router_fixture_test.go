// Shared router fixtures resolve controlled credentials and generate certificate
// bytes locally. They do not start a TLS server or perform network requests.
package fetcher

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type credentialResolverFunc func(context.Context, CredentialRequest) (ResolvedCredential, error)

func (f credentialResolverFunc) Resolve(ctx context.Context, request CredentialRequest) (ResolvedCredential, error) {
	return f(ctx, request)
}

func privateTLSFixtures(t *testing.T) ([]byte, tls.Certificate, []byte, []byte) {
	t.Helper()
	ca, server, issueClient := privateTLSClientFixtures(t)
	client, key := issueClient(3, nil)
	return ca, server, client, key
}

// Additional leaves share the same authority as the existing private fixture.
// Callers can vary client dates without changing server trust or parsing rules.
func privateTLSClientFixtures(t *testing.T) ([]byte, tls.Certificate, func(int64, func(*x509.Certificate)) ([]byte, []byte)) {
	t.Helper()
	now := time.Now()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Catalog test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	issue := func(serial int64, commonName string, usages []x509.ExtKeyUsage, addresses []net.IP, change func(*x509.Certificate)) ([]byte, []byte) {
		t.Helper()
		publicKey, privateKey, generateErr := ed25519.GenerateKey(rand.Reader)
		if generateErr != nil {
			t.Fatal(generateErr)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: commonName}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages, IPAddresses: addresses}
		if change != nil {
			change(template)
		}
		certificateDER, createErr := x509.CreateCertificate(rand.Reader, template, caCertificate, publicKey, caPrivate)
		if createErr != nil {
			t.Fatal(createErr)
		}
		privateDER, marshalErr := x509.MarshalPKCS8PrivateKey(privateKey)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	}
	serverPEM, serverKey := issue(2, "catalog-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, []net.IP{net.ParseIP("127.0.0.1")}, nil)
	serverCertificate, err := tls.X509KeyPair(serverPEM, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	return caPEM, serverCertificate, func(serial int64, change func(*x509.Certificate)) ([]byte, []byte) {
		t.Helper()
		return issue(serial, "catalog-client", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, change)
	}
}

// Stage immutable leaf files before publishing their metadata. Exclusive create
// prevents the fixture from changing a leaf retained by an older generation.
func stageMTLSCredentialFixture(t *testing.T, directory, origin, revision, expires string, certificate, key []byte) credentialMetadata {
	t.Helper()
	metadata := credentialMetadata{SchemaVersion: 1, TenantID: routerTestTenant, Revision: revision, ExpiresAt: expires, Origin: origin, Type: "mtls", CertificateFile: revision + ".pem", PrivateKeyFile: revision + ".key"}
	for name, raw := range map[string][]byte{metadata.CertificateFile: certificate, metadata.PrivateKeyFile: key} {
		file, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- Owned t.TempDir and fixed test generation names; exclusive creation prevents replacement.
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(raw)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("could not stage owned credential leaf")
		}
	}
	return metadata
}

func publishCredentialMetadataFixture(t *testing.T, directory, binding string, metadata credentialMetadata) {
	t.Helper()
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(directory, binding+".next")
	if err := os.WriteFile(staged, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, filepath.Join(directory, binding+".json")); err != nil {
		t.Fatal(err)
	}
}
