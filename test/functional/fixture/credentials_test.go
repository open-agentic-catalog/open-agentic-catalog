package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialFixtureRequiresExactAuthentication(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := generate([]string{"--directory", directory, "--subnet", "172.31.255.240/28"}); err != nil {
		t.Fatal(err)
	}
	config, err := credentialTLS(directory)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /credentials/{kind}/manifest.json", credentialManifest(directory))
	for _, kind := range []string{"bearer", "api_key", "mtls", "unknown"} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/credentials/"+kind+"/manifest.json", nil)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatal("unauthenticated fixture accepted")
		}
		if kind == "unknown" {
			continue
		}
		if kind == "mtls" {
			for _, stem := range []string{"client", "client-two"} {
				pair, err := tls.LoadX509KeyPair(filepath.Join(directory, "credentials", stem+".pem"), filepath.Join(directory, "credentials", stem+"-key.pem"))
				if err != nil {
					t.Fatal(err)
				}
				leaf, err := x509.ParseCertificate(pair.Certificate[0])
				if err != nil {
					t.Fatal(err)
				}
				chains, err := leaf.Verify(x509.VerifyOptions{Roots: config.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := leaf.Verify(x509.VerifyOptions{Roots: config.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
					t.Fatal("client key accepted as server identity")
				}
				request.TLS = &tls.ConnectionState{VerifiedChains: chains}
				response = httptest.NewRecorder()
				mux.ServeHTTP(response, request)
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "entries") {
					t.Fatalf("valid %s client identity rejected", stem)
				}
			}
			continue
		} else {
			raw, err := os.ReadFile(filepath.Join(directory, "credentials", kind+".token")) // #nosec G304 -- closed literal fixture cases.
			if err != nil {
				t.Fatal(err)
			}
			if kind == "bearer" {
				request.Header.Set("Authorization", string(raw))
				response = httptest.NewRecorder()
				mux.ServeHTTP(response, request)
				if response.Code != http.StatusUnauthorized {
					t.Fatal("missing bearer scheme accepted")
				}
				request.Header.Set("Authorization", "Bearer "+string(raw))
			} else {
				request.Header.Set("X-Api-Key", string(raw))
			}
		}
		response = httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "entries") {
			t.Fatal("valid fixture authentication rejected")
		}
	}
	if _, err := credentialTLS(t.TempDir()); err == nil {
		t.Fatal("missing client CA accepted")
	}
}

func TestBoundedLiveFixtureSuccessFailureAndInput(t *testing.T) {
	t.Parallel()
	state := &fixtureState{mode: "initial"}
	for _, testcase := range []struct {
		mode, body string
		status     int
	}{{"initial", `{}`, 200}, {"partial", `{}`, 503}, {"initial", `{`, 400}, {"initial", `{"query":"` + strings.Repeat("x", 8192) + `"}`, 400}} {
		state.mode = testcase.mode
		response := httptest.NewRecorder()
		state.search(response, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ard/v1/search", strings.NewReader(testcase.body)))
		if response.Code != testcase.status {
			t.Fatalf("status = %d", response.Code)
		}
	}
}
