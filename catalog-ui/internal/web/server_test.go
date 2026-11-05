// Unit tests exercise isolated validation and controlled dependencies.
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testAccess = `{"tenantId":"00000000-0000-7000-8000-000000000001","membershipId":"00000000-0000-7000-8000-000000000002","subject":"reader","actorType":"human","realmId":"00000000-0000-7000-8000-000000000003","authorizationRevision":1,"permissions":["resource.read"],"availablePermissions":["resource.read"],"namespaceGrants":[],"scopeEnforcement":false,"scopes":[]}`

// fixtureConfig writes synthetic mounted inputs and assets under the test's temporary directory.
func fixtureConfig(t *testing.T, upstream string) Config {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("index.html", `<!doctype html><script type="module" src="./assets/app-test.js"></script>`)
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	write("assets/app-test.js", "export const ok = true;")
	return Config{LogLevel: "info", LogFormat: "text", Listen: "127.0.0.1:0", Environment: "development", Origin: "http://127.0.0.1:4173", Assets: dir, Upstream: upstream, CookieName: "catalog-ui-test", KeysFile: write("keys.json", `[{"hash":"`+strings.Repeat("ab", 64)+`","block":"`+strings.Repeat("cd", 32)+`"}]`), SessionSeconds: 3600, RequestBytes: 2 << 20, ResponseBytes: 8 << 20, TimeoutSeconds: 5, AuthMode: "development", DevelopmentUsersFile: write("users.json", `[{"alias":"reader","label":"Test reader","token":"test-token"}]`)}
}

// newFixture constructs a real host and registers outbound connection cleanup.
func newFixture(t *testing.T, c Config) *Server {
	t.Helper()
	s, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// uiRequest supplies the host's required origin/header envelope without bypassing dispatch.
func uiRequest(s *Server, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, s.config.Origin+s.config.BasePath+path, strings.NewReader(body))
	r.Header.Set("Origin", s.config.Origin)
	r.Header.Set("X-Catalog-Request", "1")
	r.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestSessionEncodingRejectsInvalidTokens(t *testing.T) {
	s := directHost(t, fixtureConfig(t, "http://127.0.0.1:8080"))
	s.now = time.Now
	if err := s.setSession(httptest.NewRecorder(), strings.Repeat("t", 4000), s.now().Add(time.Hour)); err == nil {
		t.Fatal("oversized cookie accepted")
	}
	if err := s.setSession(httptest.NewRecorder(), "bad\nheader", s.now().Add(time.Hour)); err == nil {
		t.Fatal("invalid token accepted")
	}
	if err := s.setSession(httptest.NewRecorder(), "token", s.now().Add(-time.Second)); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestBrowserRequestRejections(t *testing.T) {
	c := fixtureConfig(t, "http://127.0.0.1:8080")
	s := directHost(t, c)
	for name, change := range map[string]func(*http.Request){"missing header": func(r *http.Request) { r.Header.Del("X-Catalog-Request") }, "foreign origin": func(r *http.Request) { r.Header.Set("Origin", "https://attacker.invalid") }, "missing origin": func(r *http.Request) { r.Header.Del("Origin") }, "cross site": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, "same site": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), "POST", c.Origin+"/auth/login", strings.NewReader(`{"alias":"reader"}`))
			r.Header.Set("Origin", c.Origin)
			r.Header.Set("X-Catalog-Request", "1")
			change(r)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("got %d", w.Code)
			}
		})
	}
	for _, body := range []string{`{"alias":"reader","role":"tenant_admin"}`, `{"alias":"reader","alias":"other"}`, `{} {}`, strings.Repeat("a", 4097)} {
		if w := uiRequest(s, "POST", "/auth/login", body); w.Code != 400 {
			t.Fatalf("invalid login: %d", w.Code)
		}
	}
	w := uiRequest(s, "GET", "/auth/options", "")
	if strings.Contains(w.Body.String(), "test-token") {
		t.Fatal("token exposed")
	}
	r := httptest.NewRequestWithContext(t.Context(), "GET", "http://attacker.invalid/runtime.json", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("foreign Host accepted")
	}
}
