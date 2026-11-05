//go:build minikube

package main

import (
	"testing"
	"time"
)

func TestCertificateRenewalRequiresNewLeafSerialAndKey(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	before := issuedCertificate{SHA256: "first", PublicKeySHA256: "first-key", Serial: "1", NotAfter: now}
	after := issuedCertificate{SHA256: "second", PublicKeySHA256: "second-key", Serial: "2", NotAfter: now}
	if err := requireRenewedCertificate(before, after); err != nil {
		t.Fatal("new certificate/key rejected at equal validity precision")
	}
	for _, change := range []func(*issuedCertificate){
		func(v *issuedCertificate) { *v = before },
		func(v *issuedCertificate) { v.SHA256 = before.SHA256 },
		func(v *issuedCertificate) { v.PublicKeySHA256 = before.PublicKeySHA256 },
		func(v *issuedCertificate) { v.Serial = before.Serial },
		func(v *issuedCertificate) { v.NotAfter = now.Add(-time.Second) },
		func(v *issuedCertificate) { v.SHA256 = "" },
		func(v *issuedCertificate) { v.Serial = "" },
		func(v *issuedCertificate) { v.PublicKeySHA256 = "" },
		func(v *issuedCertificate) { v.NotAfter = time.Time{} },
	} {
		candidate := after
		change(&candidate)
		if requireRenewedCertificate(before, candidate) == nil {
			t.Fatal("incomplete or unchanged renewal evidence accepted")
		}
	}
	if requireRenewedCertificate(issuedCertificate{}, after) == nil {
		t.Fatal("missing original evidence accepted")
	}
}

func TestCertificateProbeRequiresLiteralLoopbackAndIndependentRoot(t *testing.T) {
	material, err := localIssuerMaterial(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"localhost:443", "192.0.2.1:443", "127.0.0.1:0", "127.0.0.1", "", "https://127.0.0.1:443"} {
		if _, err := certificateIngressClient(address, []byte(material["tls.crt"])); err == nil {
			t.Fatal("non-tunnel destination accepted")
		}
	}
	if _, err := certificateIngressClient("127.0.0.1:443", nil); err == nil {
		t.Fatal("missing independent trust accepted")
	}
	for _, address := range []string{"127.0.0.1:443", "[::1]:443"} {
		client, err := certificateIngressClient(address, []byte(material["tls.crt"]))
		if err != nil {
			t.Fatal("literal loopback tunnel rejected")
		}
		client.CloseIdleConnections()
	}
}
