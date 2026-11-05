// Contract projection units use recorders and controlled responses, never listeners.
package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

const referenceUnitYAML = `# Preserve the connected document, including this comment.
openapi: 3.1.0
info: {title: 'Connected <API>', version: '1.0'}
paths:
  /connected:
    $ref: https://schemas.example.test/path.yaml
`

func TestDirectHostReferenceProjectionAndBounds(t *testing.T) {
	t.Parallel()
	const limit = 1 << 20
	exactSource := referenceUnitYAML + "#" + strings.Repeat("x", limit-len(referenceUnitYAML)-2) + "\n"
	// Each '<' appears in both document and retained YAML, escaped to six bytes.
	withinProjection := strings.Repeat("<", 300000)
	oversizedProjection := strings.Repeat("<", 400000)
	oversizedSource := referenceUnitYAML + "x-detail: " + oversizedProjection + "\n"
	if len(exactSource) != limit || len(oversizedSource) >= limit || 12*len(oversizedProjection) <= 4*limit {
		t.Fatal("contract boundary fixtures have incorrect sizes")
	}
	for _, test := range []struct {
		name, source, extra, wantCode string
		status                        int
		readError                     bool
	}{
		{name: "connected projection", source: referenceUnitYAML, status: http.StatusOK},
		{name: "exact source limit", source: exactSource, status: http.StatusOK},
		{name: "escaped projection within limit", source: referenceUnitYAML + "x-detail: " + withinProjection + "\n", extra: withinProjection, status: http.StatusOK},
		{name: "source limit plus one", source: exactSource + " ", status: http.StatusOK, wantCode: "invalid_catalog_contract"},
		{name: "bounded source read", source: exactSource + strings.Repeat(" ", 128), status: http.StatusOK, wantCode: "invalid_catalog_contract"},
		{name: "projection limit", source: oversizedSource, status: http.StatusOK, wantCode: "invalid_catalog_contract"},
		{name: "body read failure", source: referenceUnitYAML, status: http.StatusOK, readError: true, wantCode: "invalid_catalog_contract"},
		{name: "malformed YAML", source: "[private-upstream-detail", status: http.StatusOK, wantCode: "invalid_catalog_contract"},
		{name: "trailing document", source: referenceUnitYAML + "---\nprivate-upstream-detail\n", status: http.StatusOK, wantCode: "invalid_catalog_contract"},
		{name: "non JSON mapping", source: referenceUnitYAML + "x-detail: {true: private-upstream-detail}\n", status: http.StatusOK, wantCode: "invalid_catalog_contract"},
		{name: "unsupported version", source: strings.Replace(referenceUnitYAML, "3.1.0", "'2.0'", 1), status: http.StatusOK, wantCode: "invalid_catalog_contract"},
		{name: "empty paths", source: "openapi: 3.1.0\npaths: {}\n", status: http.StatusOK, wantCode: "invalid_catalog_contract"},
		{name: "upstream failure", source: "private-upstream-detail", status: http.StatusServiceUnavailable, wantCode: "catalog_contract_unavailable"},
		{name: "upstream redirect", source: "private-upstream-detail", status: http.StatusFound, wantCode: "catalog_contract_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := directHost(t, fixtureConfig(t, "https://api.example.test"))
			cookie := directHostSession(t, s)
			body := &transportResponseBody{reader: strings.NewReader(test.source)}
			if test.readError {
				body.reader = io.MultiReader(body.reader, iotest.ErrReader(errors.New("private read diagnostic")))
			}
			calls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls != 1 || r.Method != http.MethodGet || r.URL.String() != "https://api.example.test/openapi/control-v1.yaml" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Tenant-Id") != "" {
					t.Fatal("contract read lost fixed path/session binding or fetched an external reference")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Location": {"https://untrusted.invalid/contract"}}, Body: body, ContentLength: -1}, nil
			})
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, s.config.Origin+"/_reference?url=https://untrusted.invalid/contract", nil)
			r.Header.Set("Origin", s.config.Origin)
			r.Header.Set("X-Catalog-Request", "1")
			r.Header.Set("Authorization", "Bearer browser-token")
			r.Header.Set("X-Tenant-Id", "browser-tenant")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			wantRead := min(len(test.source), limit+1)
			if test.status != http.StatusOK {
				wantRead = 0
			}
			if calls != 1 || body.closes != 1 || body.bytesRead != wantRead || len(s.admission) != 0 {
				t.Fatalf("contract read ownership: calls=%d closes=%d bytes=%d want=%d admission=%d", calls, body.closes, body.bytesRead, wantRead, len(s.admission))
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Location") != "" {
				t.Fatal("contract projection changed response privacy or browser state")
			}
			if test.wantCode != "" {
				// Report size-limit regressions without printing megabytes of fixture data.
				if w.Code != http.StatusBadGateway {
					t.Fatalf("invalid contract returned status=%d bytes=%d", w.Code, w.Body.Len())
				}
				assertUIProblem(t, w, http.StatusBadGateway, test.wantCode)
				return
			}
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.Len() > 4*limit {
				t.Fatalf("projection response status=%d type=%s size=%d", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
			}
			var reply map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			document := map[string]any{
				"openapi": "3.1.0",
				"info":    map[string]any{"title": "Connected <API>", "version": "1.0"},
				"paths":   map[string]any{"/connected": map[string]any{"$ref": "https://schemas.example.test/path.yaml"}},
			}
			if test.extra != "" {
				document["x-detail"] = test.extra
			}
			if !reflect.DeepEqual(reply, map[string]any{"document": document, "yaml": test.source}) {
				t.Fatal("projection changed the connected contract, retained YAML or unresolved external reference")
			}
		})
	}
}

func TestDirectHostReferenceRequiresSession(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing", "invalid"} {
		t.Run(name, func(t *testing.T) {
			s := directHost(t, fixtureConfig(t, "https://api.example.test"))
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, s.config.Origin+"/_reference", nil)
			r.Header.Set("Origin", s.config.Origin)
			r.Header.Set("X-Catalog-Request", "1")
			if name == "invalid" {
				r.Header.Set("Cookie", s.config.CookieName+"=invalid-session")
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			assertUIProblem(t, w, http.StatusUnauthorized, "login_required")
			if len(s.admission) != 0 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("session refusal retained admission, became cacheable or changed cookies")
			}
		})
	}
}
