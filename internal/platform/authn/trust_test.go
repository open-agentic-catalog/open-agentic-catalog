// Unit tests exercise isolated validation and controlled dependencies.
package authn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeOIDCTrustFile(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOIDCTrustBundleRejectsInvalidFiles(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Unix(0, 0), NotAfter: time.Unix(1<<31, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	valid := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	for name, raw := range map[string][]byte{
		"empty":                  nil,
		"whitespace":             []byte(" \n\t"),
		"text":                   []byte("not a certificate"),
		"private key":            pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a certificate")}),
		"invalid DER":            pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid DER")}),
		"trailing text":          append(bytes.Clone(valid), []byte("unparsed")...),
		"malformed before valid": append([]byte("-----BEGIN CERTIFICATE-----\ninvalid\n"), valid...),
		"malformed PEM":          []byte("-----BEGIN CERTIFICATE-----\n%%%\n-----END CERTIFICATE-----"),
		"PEM headers":            pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Unexpected": "header"}, Bytes: certificate.Raw}),
		"oversized":              bytes.Repeat([]byte(" "), maxOIDCTrustBundleBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			path := writeOIDCTrustFile(t, raw)
			if _, _, err := loadOIDCTrustBundle(path); err == nil || strings.Contains(err.Error(), path) {
				t.Fatalf("invalid trust bundle result = %v; must reject without exposing its path", err)
			}
		})
	}
	for _, path := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		if _, _, err := loadOIDCTrustBundle(path); err == nil || strings.Contains(err.Error(), path) {
			t.Fatalf("non-file trust bundle result = %v", err)
		}
	}
	for name, raw := range map[string][]byte{
		"single":      valid,
		"multiple":    append(bytes.Clone(valid), valid...),
		"exact limit": append(bytes.Clone(valid), bytes.Repeat([]byte(" "), maxOIDCTrustBundleBytes-len(valid))...),
	} {
		t.Run(name, func(t *testing.T) {
			roots, digest, err := loadOIDCTrustBundle(writeOIDCTrustFile(t, raw))
			if err != nil {
				t.Fatal(err)
			}
			public, err := x509.SystemCertPool()
			if err != nil {
				t.Fatal(err)
			}
			public.AddCert(certificate)
			if !roots.Equal(public) {
				t.Fatal("private bundle did not preserve the complete public root set")
			}
			wantDigest := sha256.Sum256(raw)
			authenticator := &OIDC{trustBundleDigest: digest}
			if digest != "sha256:"+hex.EncodeToString(wantDigest[:]) || authenticator.TrustBundleDigest() != digest {
				t.Fatal("trust digest does not identify the exact loaded bytes")
			}
		})
	}
	path := writeOIDCTrustFile(t, valid)
	link := filepath.Join(t.TempDir(), "projected-ca.pem")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOIDCTrustBundle(link); err != nil {
		t.Fatalf("projected trust file: %v", err)
	}
}
