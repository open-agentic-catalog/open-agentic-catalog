//go:build minikube

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"time"
)

const certificateHost = "catalog-mcp.test"
const certificateSecret = "catalog-mcp-tls" // #nosec G101 -- Secret reference only.
const localCASecret = "catalog-test-ca"     // #nosec G101 -- Secret reference only.

// The caller keeps the signer in the private fixture and the namespaced Secret.
// Consumers receive only the independent public root, never the signing key.
func localIssuerMaterial(now time.Time) (map[string]string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Catalog disposable ingress CA"}, IsCA: true, BasicConstraintsValid: true, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return map[string]string{"tls.crt": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "tls.key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))}, nil
}

func localIssuerObjects() []kubernetesObject {
	metadata := func(name string) map[string]any {
		return map[string]any{"name": name, "namespace": namespace, "labels": testLabels("cert-manager")}
	}
	return []kubernetesObject{
		{"apiVersion": "cert-manager.io/v1", "kind": "Issuer", "metadata": metadata("catalog-local-ca"), "spec": map[string]any{"ca": map[string]string{"secretName": localCASecret}}},
		{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": metadata("catalog-mcp"), "spec": map[string]any{
			"secretName": certificateSecret, "dnsNames": []string{certificateHost}, "duration": "2h", "renewBefore": "30m",
			"issuerRef":  map[string]string{"name": "catalog-local-ca", "kind": "Issuer", "group": "cert-manager.io"},
			"privateKey": map[string]any{"algorithm": "ECDSA", "size": 256, "rotationPolicy": "Always"},
			"usages":     []string{"digital signature", "server auth"},
		}},
	}
}

type issuedCertificate struct {
	SHA256          string    `json:"sha256"`
	PublicKeySHA256 string    `json:"publicKeySha256"`
	Serial          string    `json:"serial"`
	NotAfter        time.Time `json:"notAfter"`
}

// Readiness alone cannot establish a usable Secret. Verify the exact key pair,
// SAN and chain against the independently retained public CA at the selected time.
func inspectIssuedCertificate(certificate, key, root []byte, now time.Time) (issuedCertificate, error) {
	var result issuedCertificate
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil || len(pair.Certificate) == 0 {
		return result, errors.New("issued TLS Secret has no matching certificate and private key")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != certificateHost || len(leaf.IPAddresses) != 0 {
		return result, errors.New("issued certificate has an unexpected identity")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(root) {
		return result, errors.New("independent CA is unavailable")
	}
	intermediates := x509.NewCertPool()
	for _, raw := range pair.Certificate[1:] {
		cert, e := x509.ParseCertificate(raw)
		if e != nil {
			return result, errors.New("invalid issued certificate chain")
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: certificateHost, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return result, errors.New("issued certificate fails independent CA, hostname or validity verification")
	}
	return certificateIdentity(leaf), nil
}
