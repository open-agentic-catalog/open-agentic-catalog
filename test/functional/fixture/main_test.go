// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndependentProviderCertificateIsScopedAndJVMReadable(t *testing.T) {
	t.Parallel()
	ca, key, err := createCA()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := writeOIDCCertificate(directory, ca, key); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(directory, "oidc-server.pem"), filepath.Join(directory, "oidc-server-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pair.PrivateKey.(*ecdsa.PrivateKey); !ok {
		t.Fatal("provider key is not ECDSA")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "catalog-oidc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: fixtureHostname}); err == nil {
		t.Fatal("provider leaf covers unrelated fixture")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: x509.NewCertPool(), DNSName: "catalog-oidc"}); err == nil {
		t.Fatal("provider leaf trusted without invocation CA")
	}
	if err := writeOIDCCertificate(filepath.Join(directory, "absent"), ca, key); err == nil {
		t.Fatal("accepted missing output directory")
	}
}

func TestOIDCMuxRejectsUnsafeOrIncompleteInputs(t *testing.T) {
	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.json")
	invalid := filepath.Join(directory, "invalid.json")
	if err := os.WriteFile(valid, []byte(`{"keys":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalid, []byte(`{"keys":`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]struct {
		issuer string
		path   string
	}{
		"unsupported scheme": {issuer: "ftp://identity.example", path: valid},
		"issuer path":        {issuer: "http://identity-fixture/tenant", path: valid},
		"issuer credentials": {issuer: "http://user@identity-fixture", path: valid},
		"invalid keys":       {issuer: "http://identity-fixture:8081", path: invalid},
		"missing keys":       {issuer: "http://identity-fixture:8081", path: filepath.Join(directory, "missing")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newOIDCMux(input.issuer, input.path); err == nil {
				t.Fatal("unsafe local OIDC fixture was accepted")
			}
		})
	}
}

func TestFixtureManifestRequestsAreObservable(t *testing.T) {
	state := &fixtureState{mode: "initial", modeRequests: make(map[string]int)}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://federation-fixture/.well-known/ard.json", nil)
	state.manifest(httptest.NewRecorder(), request)
	recorder := httptest.NewRecorder()
	state.report(recorder, request)
	var report stateResponse
	if err := json.NewDecoder(recorder.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.ManifestRequests != 1 {
		t.Fatalf("manifest requests = %d, want 1", report.ManifestRequests)
	}
}

func TestSourceRetryModesRetainResponseHints(t *testing.T) {
	for mode, status := range map[string]int{"throttled": http.StatusTooManyRequests, "unavailable": http.StatusServiceUnavailable, "denied": http.StatusForbidden} {
		t.Run(mode, func(t *testing.T) {
			state := &fixtureState{modeRequests: make(map[string]int)}
			control := httptest.NewRecorder()
			state.control(control, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://fixture/control", strings.NewReader(`{"mode":"`+mode+`"}`)))
			if control.Code != http.StatusOK {
				t.Fatalf("control status %d", control.Code)
			}
			response := httptest.NewRecorder()
			state.list(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://fixture/ard/v1/agents?pageSize=100", nil))
			hint := "2"
			if mode == "denied" {
				hint = "120"
			}
			if response.Code != status || response.Header().Get("Retry-After") != hint || state.modeRequests[mode] != 1 || state.activeListRequests != 0 {
				t.Fatalf("response: status=%d hint=%q state=%+v", response.Code, response.Header().Get("Retry-After"), state)
			}
		})
	}
}
