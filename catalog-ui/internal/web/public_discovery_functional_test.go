//go:build functional

// Real HTTPS forwarding checks the credential-free request on the wire.
package web

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestPublicDiscoveryHTTPS proves private CA trust, authority and redirect refusal with real peers.
func TestPublicDiscoveryHTTPS(t *testing.T) {
	t.Parallel()
	var calls, redirects atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirects.Add(1); w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-Host") != "" {
			t.Error("private authority reached public API")
		}
		if r.URL.Path != "/.well-known/ard.json" && r.URL.Path != "/ard/v1/agents" {
			t.Error("unexpected public path")
		}
		if r.URL.Path == "/ard/v1/agents" {
			http.Redirect(w, r, target.URL, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":[]}`))
	}))
	defer api.Close()
	c := fixtureConfig(t, api.URL)
	c.PublicDiscoveryEnabled = true
	c.APICAFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(c.APICAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	s := newFixture(t, c)
	if w := uiRequest(s, "GET", "/_discovery/manifest", ""); w.Code != 200 {
		t.Fatalf("trusted public read=%d %s", w.Code, w.Body.String())
	}
	if w := uiRequest(s, "GET", "/_discovery/agents", ""); w.Code != 502 || !strings.Contains(w.Body.String(), "catalog_redirect_refused") {
		t.Fatal("redirect was not refused")
	}
	if calls.Load() != 2 || redirects.Load() != 0 {
		t.Fatal("unexpected destination contacted")
	}
	c.APICAFile = ""
	untrusted := newFixture(t, c)
	before := calls.Load()
	if w := uiRequest(untrusted, "GET", "/_discovery/manifest", ""); w.Code != 502 || calls.Load() != before {
		t.Fatal("untrusted TLS admitted or reached handler")
	}
}
