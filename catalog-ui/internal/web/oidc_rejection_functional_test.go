//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestOIDCCallbackRejectsBrowserState denies malformed or unbound callbacks before any outbound request.
func TestOIDCCallbackRejectsBrowserState(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	s := newFixture(t, oidcFixtureConfig(t, upstream.URL, upstream.URL))
	now := time.Now().UTC().Truncate(time.Second)
	s.now = func() time.Time { return now }
	id := strings.Repeat("s", 43)
	for _, tc := range []struct {
		name, query, cookieMode, code string
		change                        func(*loginState)
		status                        int
		clear                         bool
	}{
		{name: "malformed query", query: "state=%zz", code: "invalid_login_callback", status: 400},
		{name: "duplicate parameter", query: "state=" + id + "&state=" + id, code: "invalid_login_callback", status: 400},
		{name: "unknown parameter", query: "state=" + id + "&redirect=private-upstream-diagnostic", code: "invalid_login_callback", status: 400},
		{name: "missing state", query: "code=private-code", code: "invalid_login_state", status: 400},
		{name: "short state", query: "state=short", code: "invalid_login_state", status: 400},
		{name: "unsafe state", query: "state=" + url.QueryEscape(strings.Repeat("s", 42)+"+"), code: "invalid_login_state", status: 400},
		{name: "missing cookie", cookieMode: "missing", code: "invalid_login_state", status: 400},
		{name: "duplicate cookie", cookieMode: "duplicate", code: "invalid_login_state", status: 400},
		{name: "tampered cookie", cookieMode: "tampered", code: "invalid_login_state", status: 400},
		{name: "deployment mismatch", change: func(s *loginState) { s.Binding = "another-deployment" }, code: "invalid_login_state", status: 400},
		{name: "state mismatch", change: func(s *loginState) { s.State = strings.Repeat("x", 43) }, code: "invalid_login_state", status: 400},
		{name: "expiry boundary", change: func(s *loginState) { s.Expires = now.Unix() }, code: "invalid_login_state", status: 400},
		{name: "expired", change: func(s *loginState) { s.Expires = now.Add(-time.Second).Unix() }, code: "invalid_login_state", status: 400},
		{name: "provider error", query: "state=" + id + "&error=access_denied&error_description=private-upstream-diagnostic", code: "login_not_completed", status: 401, clear: true},
		{name: "missing code", query: "state=" + id, code: "login_not_completed", status: 401, clear: true},
		{name: "oversized code", query: "state=" + id + "&code=" + strings.Repeat("c", 4097), code: "login_not_completed", status: 401, clear: true},
		{name: "issuer mismatch", query: "state=" + id + "&code=private-code&iss=https://other.invalid", code: "login_not_completed", status: 401, clear: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := loginState{Binding: s.binding, State: id, Nonce: "nonce", Verifier: "verifier", Expires: now.Add(5 * time.Minute).Unix()}
			if tc.change != nil {
				tc.change(&state)
			}
			cookie := pendingLoginCookie(t, s, state, id)
			cookies := []*http.Cookie{cookie}
			switch tc.cookieMode {
			case "missing":
				cookies = nil
			case "duplicate":
				cookies = []*http.Cookie{cookie, cookie}
			case "tampered":
				cookies[0] = s.cookie(cookie.Name, cookie.Value+"invalid", time.Unix(state.Expires, 0))
			}
			query := tc.query
			if query == "" {
				query = "state=" + id + "&code=private-code"
			}
			w := uiRequest(s, "GET", "/auth/callback?"+query, "", cookies...)
			assertUIProblem(t, w, tc.status, tc.code)
			cleared := w.Result().Cookies()
			if tc.clear {
				if len(cleared) != 1 || cleared[0].Name != cookie.Name || cleared[0].MaxAge != -1 || cleared[0].Value != "" {
					t.Fatalf("pending cookie not cleared: %v", cleared)
				}
			} else if len(cleared) != 0 {
				t.Fatal("unvalidated callback changed browser cookies")
			}
			if calls.Load() != 0 {
				t.Fatal("invalid callback contacted issuer or API")
			}
			if len(s.admission) != 0 {
				t.Fatal("callback leaked admission slot")
			}
		})
	}
}

// TestOIDCProviderFailures confines discovery endpoints and hides provider errors during start and callback.
func TestOIDCProviderFailures(t *testing.T) {
	for _, mode := range []string{"unavailable", "invalid JSON", "foreign authorization", "foreign token", "endpoint credentials", "endpoint fragment", "exchange failure", "exchange outage", "exchange credentials", "exchange missing token"} {
		t.Run(mode, func(t *testing.T) {
			var issuer string
			var exchanges atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Request-ID") != "" || r.Header.Get("Traceparent") != "" {
					t.Error("UI correlation reached issuer")
				}
				if r.URL.Path == "/token" {
					exchanges.Add(1)
					if mode == "exchange missing token" {
						writeJSON(w, http.StatusOK, map[string]string{"unexpected": "private-upstream-diagnostic"})
						return
					}
					status, code := 400, "invalid_grant"
					if mode == "exchange outage" {
						status, code = 503, "server_error"
					}
					if mode == "exchange credentials" {
						status, code = 401, "invalid_client"
					}
					writeJSON(w, status, map[string]string{"error": code, "error_description": "private-upstream-diagnostic"})
					return
				}
				if r.URL.Path != "/.well-known/openid-configuration" {
					t.Errorf("unexpected issuer path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if mode == "unavailable" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if mode == "invalid JSON" {
					_, _ = w.Write([]byte(`{"issuer":`))
					return
				}
				auth, endpoint := issuer+"/authorize", issuer+"/token"
				switch mode {
				case "foreign authorization":
					auth = "https://other.invalid/authorize"
				case "foreign token":
					endpoint = "https://other.invalid/token"
				case "endpoint credentials":
					endpoint = strings.Replace(issuer, "http://", "http://private:secret@", 1) + "/token"
				case "endpoint fragment":
					auth += "#private"
				}
				writeJSON(w, 200, map[string]any{"issuer": issuer, "authorization_endpoint": auth, "token_endpoint": endpoint, "jwks_uri": issuer + "/keys"})
			}))
			defer provider.Close()
			issuer = provider.URL
			s := newFixture(t, oidcFixtureConfig(t, issuer, issuer))
			logs := hostLogFixture(s, "debug", "json")
			if !strings.HasPrefix(mode, "exchange") {
				w := uiRequest(s, "POST", "/auth/login", "{}")
				assertUIProblem(t, w, 503, "identity_provider_unavailable")
				if len(w.Result().Cookies()) != 0 {
					t.Fatal("failed discovery created login cookie")
				}
			}
			id := strings.Repeat("s", 43)
			state := loginState{Binding: s.binding, State: id, Nonce: "nonce", Verifier: "verifier", Expires: s.now().Add(5 * time.Minute).Unix()}
			cookie := pendingLoginCookie(t, s, state, id)
			w := uiRequest(s, "GET", "/auth/callback?state="+id+"&code=private-code", "", cookie)
			if strings.HasPrefix(mode, "exchange") {
				assertUIProblem(t, w, 401, "login_exchange_failed")
				// OAuth may retry once with the alternate client-auth encoding.
				if exchanges.Load() < 1 || exchanges.Load() > 2 {
					t.Fatalf("exchange calls=%d", exchanges.Load())
				}
			} else {
				assertUIProblem(t, w, 503, "identity_provider_unavailable")
				if exchanges.Load() != 0 {
					t.Fatal("unsafe discovery reached token exchange")
				}
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != cookie.Name || cookies[0].MaxAge != -1 {
				t.Fatal("failed callback did not clear only the pending cookie")
			}
			records := hostLogRecords(t, logs)
			level, event := "WARN", "dependency.unavailable"
			if mode == "exchange failure" {
				level, event = "DEBUG", "ui.request_failed"
			}
			if len(records) != 1 || records[0]["level"] != level || records[0]["event"] != event || strings.Contains(logs.String(), "private-") || strings.Contains(logs.String(), id) || strings.Contains(logs.String(), cookie.Value) {
				t.Fatalf("issuer diagnostics: %s", logs.String())
			}
		})
	}
}

// TestOIDCPendingLoginLimit rejects another login when four are pending, before discovery.
func TestOIDCPendingLoginLimit(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer provider.Close()
	s := newFixture(t, oidcFixtureConfig(t, provider.URL, provider.URL))
	cookies := make([]*http.Cookie, 0, 4)
	for _, id := range []string{"a", "b", "c", "d"} {
		cookies = append(cookies, s.cookie(s.config.CookieName+"-login-"+id, "pending", s.now().Add(time.Minute)))
	}
	w := uiRequest(s, "POST", "/auth/login", "{}", cookies[:3]...)
	assertUIProblem(t, w, 503, "identity_provider_unavailable")
	if calls.Load() != 1 {
		t.Fatal("three pending logins incorrectly bypassed discovery")
	}
	w = uiRequest(s, "POST", "/auth/login", "{}", cookies...)
	assertUIProblem(t, w, 429, "too_many_pending_logins")
	if calls.Load() != 1 || len(w.Result().Cookies()) != 0 {
		t.Fatal("pending-login limit performed work or set a cookie")
	}
}
