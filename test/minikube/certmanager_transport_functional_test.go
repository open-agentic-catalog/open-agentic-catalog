//go:build minikube && functional

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCertificateSecretReadUsesCallerCancellation(t *testing.T) {
	directory := t.TempDir()
	tool := filepath.Join(directory, "kubectl")
	marker := filepath.Join(directory, "started")
	// The tool signals actual entry, then replaces itself with a blocked child.
	// Cancellation must reap that child without waiting for the scenario budget.
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf entered > \"$2\"\nexec sleep 30\n"), 0o700); err != nil { // #nosec G306 -- Private executable fixture; only its owner can read, write or run it.
		t.Fatal(err)
	}
	outer, cancelOuter := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelOuter()
	selected, cancelRead := context.WithCancel(t.Context())
	defer cancelRead()
	h := &harness{ctx: outer, options: options{kubectl: tool, repositoryRoot: directory}, kubeconfig: marker, profile: "owned-fixture"}
	done := make(chan error, 1)
	go func() { _, err := h.readCertificateSecret(selected, certificateSecret); done <- err }()
	t.Cleanup(func() {
		cancelRead()
		cancelOuter()
		select {
		case err := <-done:
			if err == nil {
				t.Error("canceled fixture read completed successfully")
			}
		case <-time.After(2 * time.Second):
			t.Error("owned fixture process did not stop after cancellation")
		}
	})
	started, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	if err := waitTopology(started, func() (bool, error) { _, err := os.Stat(marker); return err == nil, nil }); err != nil {
		t.Fatal("owned tool did not enter the blocking read")
	}
	cancelRead()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled Secret read succeeded")
		}
		// Leave the result available for the independent cleanup join.
		done <- err
	case <-time.After(2 * time.Second):
		t.Fatal("Secret read ignored its caller context")
	}
}

func certificateProbeLeaf(t *testing.T, material map[string]string, serial int64, expired bool) *tls.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(material["tls.crt"]))
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
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
	now := time.Now()
	spec := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{certificateHost}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if expired {
		spec.NotBefore = now.Add(-2 * time.Hour)
		spec.NotAfter = now.Add(-time.Hour)
	}
	raw, err := x509.CreateCertificate(rand.Reader, spec, ca, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	return &pair
}

func TestCertificateProbeObservesReplacementAndRejectsTLSFailuresBeforeHTTP(t *testing.T) {
	material, err := localIssuerMaterial(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[tls.Certificate]
	current.Store(certificateProbeLeaf(t, material, 1, false))
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Host != certificateHost || r.URL.Path != "/.well-known/oauth-protected-resource/mcp" {
			t.Error("probe changed the public Host or metadata path")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"resource":"https://`+certificateHost+`/mcp","authorization_servers":["`+oidcIssuer+`"]}`)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return current.Load(), nil }}
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := certificateIngressClient(server.Listener.Addr().String(), []byte(material["tls.crt"]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	before, err := readIngressCertificate(t.Context(), client)
	if err != nil || before.Serial != "1" {
		t.Fatal("initial served identity unavailable")
	}
	current.Store(certificateProbeLeaf(t, material, 2, false))
	after, err := readIngressCertificate(t.Context(), client)
	if err != nil || after.Serial != "2" {
		t.Fatal("new handshake reused the old certificate")
	}
	if err := requireRenewedCertificate(before, after); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatal("probe made unexpected HTTP requests")
	}
	other, err := localIssuerMaterial(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wrongAuthority, err := certificateIngressClient(server.Listener.Addr().String(), []byte(other["tls.crt"]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wrongAuthority.CloseIdleConnections)
	wrongHost, err := certificateIngressClient(server.Listener.Addr().String(), []byte(material["tls.crt"]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wrongHost.CloseIdleConnections)
	transport, ok := wrongHost.Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected fixture transport")
	}
	transport.TLSClientConfig.ServerName = "wrong.test"
	for _, bad := range []*http.Client{wrongAuthority, wrongHost} {
		if _, err := readIngressCertificate(t.Context(), bad); err == nil {
			t.Fatal("TLS authority or hostname failure accepted")
		}
	}
	current.Store(certificateProbeLeaf(t, material, 3, true))
	if _, err := readIngressCertificate(t.Context(), client); err == nil {
		t.Fatal("expired served certificate accepted")
	}
	if requests.Load() != 2 {
		t.Fatal("rejected TLS clients reached the HTTP handler")
	}
}

func TestCertificateProbeRejectsRedirectsWrongMetadataAndOversizedReplies(t *testing.T) {
	material, err := localIssuerMaterial(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var mode atomic.Int32
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch mode.Load() {
		case 0:
			http.Redirect(w, r, "https://"+certificateHost+"/other", http.StatusTemporaryRedirect)
		case 1:
			_, _ = io.WriteString(w, `{"resource":"https://other.test/mcp","authorization_servers":["`+oidcIssuer+`"]}`)
		case 2:
			_, _ = io.WriteString(w, strings.Repeat("x", (64<<10)+1))
		case 3:
			_, _ = io.WriteString(w, `{"resource":"https://`+certificateHost+`/mcp","authorization_servers":["https://other.test"]}`)
		}
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*certificateProbeLeaf(t, material, 1, false)}}
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := certificateIngressClient(server.Listener.Addr().String(), []byte(material["tls.crt"]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	for i := range 4 {
		mode.Store(int32(i)) // #nosec G115 -- The loop has exactly four cases.
		if _, err := readIngressCertificate(t.Context(), client); err == nil {
			t.Fatal("redirect, incorrect metadata or oversized response accepted")
		}
	}
	if requests.Load() != 4 {
		t.Fatal("probe followed a redirect or repeated a request")
	}
}
