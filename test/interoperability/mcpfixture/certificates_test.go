package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"
	"time"
)

func TestCertificatesBindOwnedLoopbackAndTolerateSmallClockSkew(t *testing.T) {
	directory := t.TempDir()
	if err := certificates(directory); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	roots := x509.NewCertPool()
	ca, err := root.ReadFile("ca.pem")
	if err != nil || !roots.AppendCertsFromPEM(ca) {
		t.Fatalf("fixture CA: %v", err)
	}
	raw, err := root.ReadFile("server.pem")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("missing leaf certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	caBlock, _ := pem.Decode(ca)
	issuer, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil || len(issuer.SubjectKeyId) == 0 || !bytes.Equal(leaf.AuthorityKeyId, issuer.SubjectKeyId) {
		t.Fatal("fixture leaf lacks the issuer key identifier required by strict TLS clients", err)
	}
	options := x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1", CurrentTime: time.Now().Add(-4 * time.Minute)}
	if _, err := leaf.Verify(options); err != nil {
		t.Fatalf("owned loopback with bounded clock skew: %v", err)
	}
	options.DNSName = "unselected.example"
	if _, err := leaf.Verify(options); err == nil {
		t.Fatal("certificate admits unselected name")
	}
	options.DNSName = "127.0.0.1"
	options.CurrentTime = time.Now().Add(25 * time.Hour)
	if _, err := leaf.Verify(options); err == nil {
		t.Fatal("fixture certificate does not expire")
	}
	info, err := root.Stat("server.key")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatal("private key permissions")
	}
	if _, err := root.Stat("ca.key"); !os.IsNotExist(err) {
		t.Fatal("CA signing key must not be persisted")
	}
}
