//go:build minikube

package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
	"time"
)

func TestFixtureCertificatesRejectInvalidIPAddresses(t *testing.T) {
	for _, address := range []net.IP{nil, {}, {1, 2, 3}} {
		if _, err := serviceTLSMaterial(time.Now(), "owned", "peer", []string{"gateway"}, address); err == nil {
			t.Fatal("accepted invalid IP SAN", address)
		}
	}
	material, err := serviceTLSMaterial(time.Now(), "owned", gatewayNamespace, []string{"gateway"})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(material["gateway.crt"]))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.VerifyHostname("gateway." + gatewayNamespace + ".svc.cluster.local"); err != nil {
		t.Fatal(err)
	}
	if len(leaf.IPAddresses) != 0 {
		t.Fatal("unexpected IP SAN")
	}
	block, _ = pem.Decode([]byte(material["ca.pem"]))
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.AuthorityKeyId) == 0 || !bytes.Equal(leaf.AuthorityKeyId, ca.SubjectKeyId) {
		t.Fatal("missing issuer key binding required by strict TLS clients")
	}
}

func TestPgBouncerTrustIsEndpointSpecific(t *testing.T) {
	material, err := databaseTLSMaterial(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(material["ca.pem"])) {
		t.Fatal("invalid CA")
	}
	for _, name := range []string{"postgres", "catalog-pgbouncer"} {
		block, _ := pem.Decode([]byte(material[name+".crt"]))
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Fatal(err)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "unrelated"}); err == nil {
			t.Fatal("endpoint trust bypass")
		}
	}
	if material["postgres.key"] == material["catalog-pgbouncer.key"] {
		t.Fatal("endpoints share private key")
	}
	if (options{scenario: "pgbouncer"}).runtimeReplicas() != 2 {
		t.Fatal("pooler replica test weakened")
	}
	values := pooledChartValues()
	if values["enabled"] != true || values["image"] != pgbouncerImage || values["apiPoolSize"] != 16 || values["workerPoolSize"] != 16 {
		t.Fatal("pooler fixture does not carry both isolated budgets")
	}
}

func TestPgBouncerBackendCountEvidence(t *testing.T) {
	for _, test := range []struct {
		raw   string
		valid bool
	}{
		{"2|3|0\n", true}, {"1|1|0", true}, {"0|1|0", false},
		{"2|0|0", false}, {"2|3|1", false}, {"2|3|-1", false},
		{"2|3", false}, {"2|3|0|1", false}, {"2|3|0junk", false},
		{"Defaulted container postgres\n2|3|0", false},
	} {
		t.Run(test.raw, func(t *testing.T) {
			if err := validatePooledBackendCounts([]byte(test.raw)); (err == nil) != test.valid {
				t.Fatalf("backend evidence valid=%v: %v", test.valid, err)
			}
		})
	}
}
