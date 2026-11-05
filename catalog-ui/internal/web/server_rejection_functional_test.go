//go:build functional

// Host rejection tests protect startup validation and fresh session access checks.
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// TestHostRejectsMountedInputs exercises constructor failures before network access.
func TestHostRejectsMountedInputs(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer api.Close()
	for _, tc := range []struct {
		name, users, secret, want string
		oidc                      bool
	}{
		{name: "invalid config", want: "UI session must be 60..28800 seconds and timeout 1..300 seconds"},
		{name: "API trust", want: "UI private trust contains no certificates"},
		{name: "empty users", users: "[]", want: "UI development users must contain 1..32 fixed users"},
		{name: "malformed users", users: "[", want: "UI development users must contain 1..32 fixed users"},
		{name: "invalid user", users: `[{"alias":"reader","label":"","token":"synthetic-token"}]`, want: "invalid or duplicate UI development user"},
		{name: "duplicate user", users: `[{"alias":"reader","label":"Reader","token":"synthetic-token"},{"alias":"reader","label":"Other","token":"synthetic-token"}]`, want: "invalid or duplicate UI development user"},
		{name: "empty secret", oidc: true, secret: " ", want: "UI OIDC client secret file is empty or unreadable"},
		{name: "missing secret", oidc: true, want: "UI OIDC client secret file is empty or unreadable"},
		{name: "issuer trust", oidc: true, want: "UI private trust contains no certificates"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			if tc.oidc {
				c = oidcFixtureConfig(t, api.URL, api.URL)
			} else {
				c = fixtureConfig(t, api.URL)
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.users != "" {
				write(c.DevelopmentUsersFile, tc.users)
			}
			switch tc.name {
			case "invalid config":
				c.SessionSeconds = 0
			case "API trust":
				c.APICAFile = c.KeysFile
			case "empty secret":
				write(c.ClientSecretFile, tc.secret)
			case "missing secret":
				if err := os.Remove(c.ClientSecretFile); err != nil {
					t.Fatal(err)
				}
			case "issuer trust":
				c.IssuerCAFile = c.KeysFile
			}
			s, err := New(c, nil)
			if s != nil {
				s.Close()
				t.Fatal("invalid mounted input constructed a host")
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
			if calls.Load() != 0 {
				t.Fatal("constructor contacted API or issuer")
			}
		})
	}
}

// TestCurrentSessionAccessFailures rechecks current authority and hides upstream details.
func TestCurrentSessionAccessFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status, want     int
		cancel           bool
	}{
		{name: "denied", status: 403, body: "private-upstream-diagnostic", want: 401, code: "catalog_login_denied"},
		{name: "unavailable", status: 503, body: "private-upstream-diagnostic", want: 503, code: "catalog_unavailable"},
		{name: "malformed", status: 200, body: `{"subject":`, want: 503, code: "catalog_unavailable"},
		{name: "oversized context", status: 200, body: strings.Repeat(" ", 64<<10) + testAccess, want: 503, code: "catalog_unavailable"},
		{name: "canceled", status: 200, body: testAccess, cancel: true, want: 503, code: "catalog_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/me" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("session access lost path or bearer binding")
				}
				if calls.Add(1) == 1 {
					_, _ = w.Write([]byte(testAccess))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer api.Close()
			s := newFixture(t, fixtureConfig(t, api.URL))
			cookie := loginFixture(t, s)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, s.config.Origin+"/auth/session", nil)
			r.Header.Set("X-Catalog-Request", "1")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			assertUIProblem(t, w, tc.want, tc.code)
			wantCalls := int32(2)
			if tc.cancel {
				wantCalls = 1
			}
			if calls.Load() != wantCalls || len(w.Result().Cookies()) != 0 || len(s.admission) != 0 {
				t.Fatalf("unexpected calls/cookies/admission = %d/%v/%d", calls.Load(), w.Result().Cookies(), len(s.admission))
			}
		})
	}
}
