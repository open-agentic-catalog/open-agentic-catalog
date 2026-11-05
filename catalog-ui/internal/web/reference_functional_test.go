//go:build functional

package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/api"
)

func TestReferenceUsesConnectedContractAndExactDownload(t *testing.T) {
	for _, base := range []string{"", "/nested"} {
		t.Run(base, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/me" {
					_, _ = w.Write([]byte(testAccess))
					return
				}
				calls++
				if r.Method != http.MethodGet || r.URL.RequestURI() != "/openapi/control-v1.yaml" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Cookie") != "" {
					t.Error("unexpected contract request")
				}
				_, _ = w.Write(api.ControlV1)
			}))
			defer upstream.Close()
			config := fixtureConfig(t, upstream.URL)
			config.BasePath = base
			s := newFixture(t, config)
			if response := uiRequest(s, "GET", "/_reference", ""); response.Code != 401 {
				t.Fatalf("unauthenticated reference: %d", response.Code)
			}
			cookie := loginFixture(t, s)
			for _, method := range []string{"POST", "HEAD", "DELETE"} {
				if response := uiRequest(s, method, "/_reference", "", cookie); response.Code != 404 {
					t.Fatalf("unexpected method %s: %d", method, response.Code)
				}
			}
			request := httptest.NewRequestWithContext(t.Context(), "GET", config.Origin+base+"/_reference", nil)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			s.ServeHTTP(response, request)
			if response.Code != 403 || calls != 0 {
				t.Fatal("reference bypassed same-origin header or made an unauthorized upstream call")
			}
			response = uiRequest(s, "GET", "/_reference?ignored=1", "", cookie)
			var reply struct {
				Document map[string]any `json:"document"`
				YAML     string         `json:"yaml"`
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &reply) != nil || reply.YAML != string(api.ControlV1) || reply.Document["openapi"] != "3.1.0" || calls != 1 {
				t.Fatalf("reference projection failed: status %d", response.Code)
			}
			if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json" {
				t.Fatal("missing reference response boundaries")
			}
			if response = uiRequest(s, "GET", "/api-reference", ""); response.Code != 200 || !strings.Contains(response.Header().Get("Content-Type"), "text/html") {
				t.Fatal("reference bookmark does not reach the UI shell")
			}
			s.Drain()
			if response = uiRequest(s, "GET", "/_reference", "", cookie); response.Code != 503 || calls != 1 {
				t.Fatal("reference admitted during drain")
			}
		})
	}
}

func TestReferenceRejectsInvalidContractWithoutLeakingResponse(t *testing.T) {
	const valid = "openapi: 3.1.0\npaths:\n  /example:\n    get: {}\n"
	for name, input := range map[string]struct {
		status int
		body   string
	}{
		"redirect":         {302, "private-upstream-detail"},
		"failure":          {500, "private-upstream-detail"},
		"invalid":          {200, "[private-upstream-detail"},
		"empty":            {200, ""},
		"wrong version":    {200, "openapi: 2.0\npaths: {}"},
		"no paths":         {200, "openapi: 3.1.0"},
		"duplicate":        {200, valid + "openapi: 3.2.0"},
		"trailing":         {200, valid + "---\nprivate-upstream-detail"},
		"non json":         {200, valid + "extra: {true: value}"},
		"large":            {200, valid + "extra: " + strings.Repeat("x", 1<<20)},
		"large projection": {200, valid + "extra: " + strings.Repeat("<", 400000)},
		"recursive alias":  {200, valid + "extra: &recursive [*recursive]"},
	} {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/me" {
					_, _ = w.Write([]byte(testAccess))
					return
				}
				w.Header().Set("Location", "http://untrusted.invalid/contract")
				w.WriteHeader(input.status)
				_, _ = w.Write([]byte(input.body))
			}))
			defer upstream.Close()
			s := newFixture(t, fixtureConfig(t, upstream.URL))
			response := uiRequest(s, "GET", "/_reference", "", loginFixture(t, s))
			if response.Code != 502 || strings.Contains(response.Body.String(), "private-upstream-detail") || strings.Contains(response.Body.String(), "untrusted.invalid") {
				t.Fatalf("unsafe invalid-contract response: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
