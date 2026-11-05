//go:build functional

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTLSProbeVerifiesTrustAndReportsNegotiatedProtocol(t *testing.T) {
	ca, key, err := createCA()
	if err != nil {
		t.Fatal(err)
	}
	cert, private, err := createServerCertificate(ca, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/ard.json" {
			t.Error("unexpected TLS probe path")
		}
		_, _ = w.Write([]byte(`{"private":"not-emitted"}`))
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, h2 := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		result := fixtureTLSProbe(ctx, strings.TrimPrefix(server.URL, "https://"), roots, h2)
		cancel()
		protocol := "HTTP/1.1"
		if h2 {
			protocol = "HTTP/2.0"
		}
		if result.Outcome != "connected" || result.Stage != "complete" || result.Protocol != protocol {
			t.Fatalf("TLS probe: %+v", result)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result := fixtureTLSProbe(ctx, strings.TrimPrefix(server.URL, "https://"), x509.NewCertPool(), true)
	if result.Outcome != "error" || result.Stage != "tls" {
		t.Fatalf("untrusted TLS accepted: %+v", result)
	}
}
