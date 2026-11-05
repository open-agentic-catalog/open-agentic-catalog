//go:build minikube

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestLocalIssuerKeepsAuthorityAndExplicitLeafPolicySeparate(t *testing.T) {
	objects := localIssuerObjects()
	raw, err := json.Marshal(objects)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			CA struct {
				Secret string `json:"secretName"`
			} `json:"ca"`
			Secret      string   `json:"secretName"`
			Names       []string `json:"dnsNames"`
			Duration    string   `json:"duration"`
			RenewBefore string   `json:"renewBefore"`
			Issuer      struct {
				Name  string `json:"name"`
				Kind  string `json:"kind"`
				Group string `json:"group"`
			} `json:"issuerRef"`
			Key struct {
				Algorithm      string `json:"algorithm"`
				Size           int    `json:"size"`
				RotationPolicy string `json:"rotationPolicy"`
			} `json:"privateKey"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &got) != nil || len(got) != 2 {
		t.Fatal("unexpected issuer inventory")
	}
	if got[0].Kind != "Issuer" || got[0].Metadata.Namespace != namespace || got[0].Spec.CA.Secret != localCASecret {
		t.Fatal("issuer must use the owned namespaced CA Secret")
	}
	s := got[1].Spec
	if got[1].Kind != "Certificate" || got[1].Metadata.Namespace != namespace || s.Secret != certificateSecret || len(s.Names) != 1 || s.Names[0] != certificateHost || s.Duration != "2h" || s.RenewBefore != "30m" || s.Issuer.Name != "catalog-local-ca" || s.Issuer.Kind != "Issuer" || s.Issuer.Group != "cert-manager.io" || s.Key.Algorithm != "ECDSA" || s.Key.Size != 256 || s.Key.RotationPolicy != "Always" {
		t.Fatal("leaf identity, issuer or explicit rotation policy changed")
	}
}

func TestIssuedCertificateRequiresIndependentTrustAndExactUsableIdentity(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	material, err := localIssuerMaterial(now)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(material["tls.crt"]))
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA || ca.CheckSignatureFrom(ca) != nil || !ca.NotAfter.Equal(now.Add(48*time.Hour)) {
		t.Fatal("invalid fixture CA")
	}
	block, _ = pem.Decode([]byte(material["tls.key"]))
	signer, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
	leaf := func(change func(*x509.Certificate)) []byte {
		t.Helper()
		spec := &x509.Certificate{SerialNumber: big.NewInt(42), DNSNames: []string{certificateHost}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		change(spec)
		der, e := x509.CreateCertificate(rand.Reader, spec, ca, &key.PublicKey, signer)
		if e != nil {
			t.Fatal(e)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	good := leaf(func(*x509.Certificate) {})
	root := []byte(material["tls.crt"])
	got, err := inspectIssuedCertificate(good, keyPEM, root, now)
	if err != nil || got.Serial != "42" || len(got.SHA256) != 64 || len(got.PublicKeySHA256) != 64 || !got.NotAfter.Equal(now.Add(2*time.Hour)) {
		t.Fatal("valid independently signed certificate rejected")
	}
	for _, change := range []func(*x509.Certificate){
		func(v *x509.Certificate) { v.DNSNames = []string{"other.test"} },
		func(v *x509.Certificate) { v.DNSNames = append(v.DNSNames, "other.test") },
		func(v *x509.Certificate) { v.NotAfter = now.Add(-time.Second) },
		func(v *x509.Certificate) { v.NotBefore = now.Add(time.Minute) },
		func(v *x509.Certificate) { v.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} },
		func(v *x509.Certificate) { v.IsCA = true; v.BasicConstraintsValid = true },
	} {
		if _, err := inspectIssuedCertificate(leaf(change), keyPEM, root, now); err == nil {
			t.Fatal("unusable or unexpected leaf accepted")
		}
	}
	other, err := localIssuerMaterial(now)
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range [][3][]byte{{good, keyPEM, []byte(other["tls.crt"])}, {good, []byte(other["tls.key"]), root}, {good, keyPEM, nil}, {nil, keyPEM, root}, {good, nil, root}} {
		if _, err := inspectIssuedCertificate(values[0], values[1], values[2], now); err == nil {
			t.Fatal("missing keys or unrelated authority accepted")
		}
	}
}
