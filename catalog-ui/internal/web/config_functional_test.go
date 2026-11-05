//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyLimitsRedirectsAndConditionalResponses(t *testing.T) {
	status := http.StatusNotModified
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" {
			_, _ = w.Write([]byte(testAccess))
			return
		}
		w.Header().Set("Content-Digest", "sha-256=:exact:")
		w.Header().Set("ETag", `"exact"`)
		if status == http.StatusFound {
			w.Header().Set("Location", "https://attacker.invalid/secret")
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(strings.Repeat("x", 8192)))
		}
	}))
	defer api.Close()
	c := fixtureConfig(t, api.URL)
	c.RequestBytes = 512
	c.ResponseBytes = 4096
	s := newFixture(t, c)
	cookie := loginFixture(t, s)
	w := uiRequest(s, "GET", "/_catalog/api/v1/resources", "", cookie)
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("ETag") != `"exact"` {
		t.Fatal("conditional response not preserved")
	}
	status = http.StatusFound
	if w := uiRequest(s, "GET", "/_catalog/api/v1/resources", "", cookie); w.Code != 502 || strings.Contains(w.Body.String(), "attacker") {
		t.Fatal("redirect boundary")
	}
	status = http.StatusOK
	if w := uiRequest(s, "GET", "/_catalog/api/v1/resources", "", cookie); w.Code != 502 || strings.Contains(w.Body.String(), strings.Repeat("x", 64)) {
		t.Fatal("response bound")
	}
	if w := uiRequest(s, "POST", "/_catalog/api/v1/catalog/search", strings.Repeat("x", 513), cookie); w.Code != 413 {
		t.Fatal("request bound")
	}
	for range cap(s.admission) {
		s.admission <- struct{}{}
	}
	if w := uiRequest(s, "GET", "/auth/session", "", cookie); w.Code != 503 {
		t.Fatal("admission bound")
	}
	if w := uiRequest(s, "GET", "/livez", ""); w.Code != 200 {
		t.Fatal("busy server lost liveness")
	}
}
