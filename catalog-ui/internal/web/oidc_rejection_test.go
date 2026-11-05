// Unit tests exercise isolated validation and controlled dependencies.
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// oidcFixtureConfig reuses mounted inputs without contacting an external issuer.
func oidcFixtureConfig(t *testing.T, upstream, issuer string) Config {
	t.Helper()
	c := fixtureConfig(t, upstream)
	c.AuthMode, c.DevelopmentUsersFile = "oidc", ""
	c.Issuer, c.ClientID = issuer, "catalog-ui"
	c.ClientSecretFile = filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(c.ClientSecretFile, []byte("synthetic-client-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	return c
}

// assertUIProblem permits only the fixed public error envelope, with no upstream details.
func assertUIProblem(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": float64(status), "code": code}
	if w.Code != status || w.Header().Get("Content-Type") != "application/problem+json" || !reflect.DeepEqual(got, want) || w.Header().Get("Location") != "" {
		t.Fatalf("response = %d %v %s, want %d/%s", w.Code, w.Header(), w.Body.String(), status, code)
	}
}

// pendingLoginCookie signs test-owned callback evidence with the host's actual codec.
func pendingLoginCookie(t *testing.T, s *Server, state loginState, id string) *http.Cookie {
	t.Helper()
	name := s.config.CookieName + "-login-" + id
	value, err := s.codecs[0].Encode(name, state)
	if err != nil {
		t.Fatal(err)
	}
	return s.cookie(name, value, time.Unix(state.Expires, 0))
}
