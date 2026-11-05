//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package web

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	jose "github.com/go-jose/go-jose/v4"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// loginFixture obtains a real encrypted session through the development login handler.
func loginFixture(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	w := uiRequest(s, "POST", "/auth/login", `{"alias":"reader"}`)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session")
	}
	return cookies[0]
}

func TestDevelopmentSessionAndFixedProxy(t *testing.T) {
	var received *http.Request
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" {
			if r.Header.Get("Authorization") != "Bearer test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, testAccess)
			return
		}
		received = r.Clone(r.Context())
		received.Header = r.Header.Clone()
		w.Header().Set("ETag", `"rev-2"`)
		w.Header().Set("X-Request-ID", "request-1")
		w.Header().Set("Idempotency-Replayed", "true")
		w.Header().Set("Set-Cookie", "upstream-cookie=secret")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"id":"job"}`)
	}))
	defer api.Close()
	c := fixtureConfig(t, api.URL)
	c.BasePath = "/catalog-ui"
	s := newFixture(t, c)
	cookie := loginFixture(t, s)
	if !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/catalog-ui/" || strings.Contains(cookie.Value, "test-token") {
		t.Fatal("unsafe session cookie")
	}
	w := uiRequest(s, "GET", "/auth/session", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reader"`) || strings.Contains(w.Body.String(), "test-token") {
		t.Fatalf("session: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequestWithContext(t.Context(), "POST", c.Origin+c.BasePath+"/_catalog/api/v1/version-revisions/00000000-0000-7000-8000-000000000001:validate", strings.NewReader(`{}`))
	r.Header.Set("Origin", c.Origin)
	r.Header.Set("X-Catalog-Request", "1")
	r.AddCookie(cookie)
	r.Header.Set("Authorization", "Bearer attacker")
	r.Header.Set("Forwarded", "for=attacker")
	r.Header.Set("X-Tenant-Id", "attacker")
	r.Header.Set("Idempotency-Key", "command-one")
	r.Header.Set("If-Match", `"rev-1"`)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 202 || received == nil {
		t.Fatalf("proxy: %d %s", w.Code, w.Body.String())
	}
	if received.Header.Get("Authorization") != "Bearer test-token" || received.Header.Get("Cookie") != "" || received.Header.Get("Forwarded") != "" || received.Header.Get("X-Tenant-Id") != "" || received.Header.Get("Idempotency-Key") != "command-one" || received.Header.Get("If-Match") != `"rev-1"` {
		t.Fatal("proxy credential/header boundary")
	}
	if w.Header().Get("Set-Cookie") != "" || w.Header().Get("ETag") != `"rev-2"` || w.Header().Get("Idempotency-Replayed") != "true" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response header boundary")
	}
	for _, path := range []string{"/_catalog/metrics", "/_catalog/api/v1/unknown", "/api/v1/me", "/auth/unknown", "/assets/missing.js"} {
		if w := uiRequest(s, "GET", path, "", cookie); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	for _, route := range []string{"/my-work", "/saved-items", "/saved-items/new", "/saved-items/00000000-0000-7000-8000-000000000001", "/settings", "/my-ratings", "/ratings", "/my-feedback", "/feedback", "/catalog/record-id", "/namespaces", "/namespaces/new", "/namespaces/record-id", "/publishers", "/publishers/new", "/publishers/record-id", "/jobs", "/jobs/record-id", "/service", "/access", "/resources/new", "/versions/new", "/version-revisions/id", "/listings/id", "/listing-revisions/id", "/reviews/id", "/releases/id", "/members/id", "/publication", "/audit", "/explore", "/webhooks/id", "/ratings/00000000-0000-7000-8000-000000000001", "/my-ratings/00000000-0000-7000-8000-000000000001", "/feedback/00000000-0000-7000-8000-000000000001", "/my-feedback/00000000-0000-7000-8000-000000000001", "/sources", "/sources/new", "/sources/id", "/inspections/id", "/source-snapshots/id", "/observations/id", "/observation-group", "/federation-search"} {
		if w := uiRequest(s, "GET", route, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `src="/catalog-ui/assets/`) {
			t.Errorf("subpath deep-link assets for %s: %d", route, w.Code)
		}
	}
	for _, route := range []string{"/namespaces/id/unknown", "/publishers/id/unknown", "/jobs/id/unknown", "/service/unknown", "/sources/id/unknown", "/admin"} {
		if w := uiRequest(s, "GET", route, ""); w.Code != 404 {
			t.Errorf("unknown navigation %s: %d", route, w.Code)
		}
	}
	if w := uiRequest(s, "GET", "/assets/app-test.js", ""); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("asset caching")
	}
	if w := uiRequest(s, "POST", "/auth/logout", "{}", cookie); w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout")
	}
	if w := uiRequest(s, "GET", "/auth/session", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"access":null`) {
		t.Fatal("unauthenticated session")
	}
}

func TestSessionIsolationExpiryRotationAndBounds(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, testAccess) }))
	defer api.Close()
	c := fixtureConfig(t, api.URL)
	s := newFixture(t, c)
	cookie := loginFixture(t, s)
	second := newFixture(t, c)
	// A slightly earlier replica clock must not reject a freshly signed expiry.
	// The writer bounds lifetime, while the codec bounds cookie age on reading.
	second.now = func() time.Time { return time.Now().Add(-2 * time.Second) }
	if response := uiRequest(second, "GET", "/auth/session", "", cookie); response.Code != 200 {
		t.Fatalf("cookie failed on another replica: status=%d body=%s", response.Code, response.Body.String())
	}
	for name, modify := range map[string]func(*Config){"deployment": func(c *Config) { c.CookieName = "another-ui" }, "upstream": func(c *Config) { c.Upstream = "http://127.0.0.1:1" }, "base": func(c *Config) { c.BasePath = "/another" }} {
		t.Run(name, func(t *testing.T) {
			changed := c
			modify(&changed)
			other := newFixture(t, changed)
			response := uiRequest(other, "GET", "/auth/session", "", cookie)
			if response.Code != 401 && !(response.Code == 200 && strings.Contains(response.Body.String(), `"access":null`)) {
				t.Fatal("session crossed binding")
			}
		})
	}
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if uiRequest(s, "GET", "/auth/session", "", cookie).Code != 401 {
		t.Fatal("expired cookie accepted")
	}
	s.now = time.Now
	if uiRequest(s, "GET", "/auth/session", "", cookie, cookie).Code != 401 {
		t.Fatal("duplicate cookie accepted")
	}
	cookie.Value += "tampered" // #nosec G124 -- Deliberately corrupt an existing test cookie without changing its tested security attributes.
	if uiRequest(s, "GET", "/auth/session", "", cookie).Code != 401 {
		t.Fatal("tampered cookie accepted")
	}
	cookie = loginFixture(t, s)
	keys := `[{"hash":"` + strings.Repeat("ef", 64) + `","block":"` + strings.Repeat("12", 32) + `"},{"hash":"` + strings.Repeat("ab", 64) + `","block":"` + strings.Repeat("cd", 32) + `"}]`
	if err := os.WriteFile(c.KeysFile, []byte(keys), 0600); err != nil {
		t.Fatal(err)
	}
	rotated := newFixture(t, c)
	if uiRequest(rotated, "GET", "/auth/session", "", cookie).Code != 200 {
		t.Fatal("previous key rejected during rotation")
	}
}

func TestBrowserOriginAndLoginRejections(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer api.Close()
	c := fixtureConfig(t, api.URL)
	s := newFixture(t, c)
	for _, alias := range []string{"unknown", "reader"} {
		if w := uiRequest(s, "POST", "/auth/login", `{"alias":"`+alias+`"}`); w.Code != 401 || len(w.Result().Cookies()) != 0 {
			t.Fatal("login without Catalog access")
		}
	}
}

func TestOIDCCrossReplicaCodeFlow(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer, nonce, challenge string
	idAudience := "catalog-ui"
	var alterIdentity func(map[string]any)
	var alterTokens func(map[string]any)
	apiBody, apiStatus := testAccess, http.StatusOK
	keysStatus := http.StatusOK
	var apiCalls atomic.Int32
	wrongNonce := false
	serviceIdentity := false
	var used bool
	sign := func(claims map[string]any) string {
		t.Helper()
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		object, err := signer.Sign(raw)
		if err != nil {
			t.Fatal(err)
		}
		signed, err := object.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		apiCalls.Add(1)
		body := apiBody
		if serviceIdentity {
			body = strings.Replace(body, `"actorType":"human"`, `"actorType":"service"`, 1)
		}
		w.WriteHeader(apiStatus)
		_, _ = io.WriteString(w, body)
	}))
	defer api.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(w, 200, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			if keysStatus >= http.StatusBadRequest {
				writeJSON(w, keysStatus, map[string]string{"error": "private-key-service-diagnostic"})
				return
			}
			writeJSON(w, keysStatus, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if used || r.Form.Get("code") != "one-time-code" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
				writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			used = true
			expiry := time.Now().Add(time.Hour).Unix()
			tokenNonce := nonce
			if wrongNonce {
				tokenNonce = "incorrect"
			}
			claims := map[string]any{"iss": issuer, "aud": idAudience, "sub": "reader", "exp": expiry, "iat": time.Now().Unix(), "nonce": tokenNonce}
			if alterIdentity != nil {
				alterIdentity(claims)
			}
			tokens := map[string]any{"access_token": sign(map[string]any{"iss": issuer, "aud": "catalog-api", "sub": "reader", "exp": expiry}), "token_type": "Bearer", "expires_in": 3600, "id_token": sign(claims)}
			if alterTokens != nil {
				alterTokens(tokens)
			}
			writeJSON(w, 200, tokens)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	c := oidcFixtureConfig(t, api.URL, issuer)
	s := newFixture(t, c)
	second := newFixture(t, c)
	logs := hostLogFixture(second, "debug", "json")
	w := uiRequest(s, "POST", "/auth/login", "{}")
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var reply map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(reply["location"])
	if err != nil {
		t.Fatal(err)
	}
	query := authorize.Query()
	nonce = query.Get("nonce")
	challenge = query.Get("code_challenge")
	if query.Get("code_challenge_method") != "S256" || query.Get("client_id") != "catalog-ui" || query.Get("scope") != "openid" || nonce == "" {
		t.Fatal("invalid authorization request")
	}
	flow := w.Result().Cookies()[0]
	w = uiRequest(second, "GET", "/auth/callback?state="+query.Get("state")+"&code=one-time-code", "", flow)
	if w.Code != 303 {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == c.CookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || uiRequest(s, "GET", "/auth/session", "", sessionCookie).Code != 200 {
		t.Fatal("session did not cross replica")
	}
	if w := uiRequest(second, "GET", "/auth/callback?state="+query.Get("state")+"&code=one-time-code", "", flow); w.Code != 401 {
		t.Fatal("authorization code replay")
	}

	// Repeat the real discovery/PKCE/token/API exchange across startup ring stages.
	// Keep the original hosts and all later rejection/replay assertions intact.
	oldPair := keyPair{Hash: strings.Repeat("ab", 64), Block: strings.Repeat("cd", 32)}
	newPair := keyPair{Hash: strings.Repeat("ef", 64), Block: strings.Repeat("12", 32)}
	writeRotationRing(t, c, oldPair, newPair)
	stagedHost := newFixture(t, c)
	writeRotationRing(t, c, newPair, oldPair)
	promotedHost := newFixture(t, c)
	writeRotationRing(t, c, newPair)
	retiredHost := newFixture(t, c)
	for _, phase := range []struct {
		name           string
		writer, reader *Server
		key, other     keyPair
	}{
		{"stage old writer to overlap reader", s, stagedHost, oldPair, newPair},
		{"stage old writer to promoted reader", stagedHost, promotedHost, oldPair, newPair},
		{"promoted writer to staged reader", promotedHost, stagedHost, newPair, oldPair},
		{"new writer after retirement", promotedHost, retiredHost, newPair, oldPair},
	} {
		t.Run(phase.name, func(t *testing.T) {
			used = false
			start := uiRequest(phase.writer, "POST", "/auth/login", "{}")
			if start.Code != 200 || len(start.Result().Cookies()) != 1 {
				t.Fatal("rotation real login failed")
			}
			var location map[string]string
			if err := json.Unmarshal(start.Body.Bytes(), &location); err != nil {
				t.Fatal(err)
			}
			auth, err := url.Parse(location["location"])
			if err != nil {
				t.Fatal(err)
			}
			q := auth.Query()
			nonce, challenge = q.Get("nonce"), q.Get("code_challenge")
			pending := start.Result().Cookies()[0]
			rotationKeyOracle(t, phase.key, pending, true)
			rotationKeyOracle(t, phase.other, pending, false)
			if phase.key == oldPair {
				denied := uiRequest(retiredHost, "GET", "/auth/callback?state="+q.Get("state")+"&code=one-time-code", "", pending)
				assertUIProblem(t, denied, 400, "invalid_login_state")
				if used {
					t.Fatal("retired state reached token exchange")
				}
			}
			callback := uiRequest(phase.reader, "GET", "/auth/callback?state="+q.Get("state")+"&code=one-time-code", "", pending)
			if callback.Code != 303 || !used {
				t.Fatal("rotation real callback failed")
			}
			var access *http.Cookie
			for _, cookie := range callback.Result().Cookies() {
				if cookie.Name == c.CookieName {
					access = cookie
				}
			}
			if access == nil || uiRequest(phase.reader, "GET", "/auth/session", "", access).Code != 200 {
				t.Fatal("rotation callback missing valid session")
			}
		})
	}
	if uiRequest(s, "POST", "/auth/login", `{"alias":"reader"}`).Code != 400 {
		t.Fatal("OIDC accepted development alias")
	}
	for _, tc := range []struct {
		name, code        string
		identity, tokens  func(map[string]any)
		body              string
		apiStatus, status int
		apiCalls          int32
		lifetime          time.Duration
	}{
		{name: "multiple audiences with authorized party and shorter exchange lifetime", identity: func(c map[string]any) { c["aud"] = []string{"catalog-ui", "other-client"}; c["azp"] = "catalog-ui" }, tokens: func(c map[string]any) { c["expires_in"] = 120 }, status: http.StatusSeeOther, apiCalls: 1, lifetime: 2 * time.Minute},
		{name: "shorter access-token lifetime", tokens: func(c map[string]any) {
			c["access_token"] = sign(map[string]any{"sub": "reader", "exp": time.Now().Add(2 * time.Minute).Unix()})
		}, status: http.StatusSeeOther, apiCalls: 1, lifetime: 2 * time.Minute},
		{name: "wrong ID-token audience", code: "invalid_login_identity", status: 401},
		{name: "wrong nonce", code: "invalid_login_identity", status: 401},
		{name: "keys unavailable", code: "invalid_login_identity", status: 401},
		{name: "keys refused", code: "invalid_login_identity", status: 401},
		{name: "keys pending", code: "invalid_login_identity", status: 401},
		{name: "service identity", code: "catalog_login_denied", status: 401, apiCalls: 1},
		{name: "wrong authorized party", identity: func(c map[string]any) { c["azp"] = "other-client" }, code: "invalid_login_identity", status: 401},
		{name: "multiple audiences without authorized party", identity: func(c map[string]any) { c["aud"] = []string{"catalog-ui", "other-client"} }, code: "invalid_login_identity", status: 401},
		{name: "malformed authorized party", identity: func(c map[string]any) { c["azp"] = 123 }, code: "invalid_login_identity", status: 401},
		{name: "wrong access hash", identity: func(c map[string]any) { c["at_hash"] = "wrong-hash" }, code: "invalid_login_identity", status: 401},
		{name: "missing ID token", tokens: func(c map[string]any) { delete(c, "id_token") }, code: "invalid_login_identity", status: 401},
		{name: "oversized ID token", tokens: func(c map[string]any) { c["id_token"] = strings.Repeat("x", (16<<10)+1) }, code: "invalid_login_identity", status: 401},
		{name: "wrong token type", tokens: func(c map[string]any) { c["token_type"] = "MAC" }, code: "login_exchange_failed", status: 401},
		{name: "missing token lifetime", tokens: func(c map[string]any) { delete(c, "expires_in") }, code: "login_exchange_failed", status: 401},
		{name: "API denial", apiStatus: 403, body: "private-upstream-diagnostic", code: "catalog_login_denied", status: 401, apiCalls: 1},
		{name: "API unavailable", apiStatus: 503, body: "private-upstream-diagnostic", code: "catalog_unavailable", status: 503, apiCalls: 1},
		{name: "API invalid context", body: `{"private":"private-upstream-diagnostic"}`, code: "catalog_unavailable", status: 503, apiCalls: 1},
		{name: "subject mismatch", body: strings.Replace(testAccess, `"subject":"reader"`, `"subject":"other"`, 1), code: "catalog_login_denied", status: 401, apiCalls: 1},
		{name: "invalid access expiry", tokens: func(c map[string]any) { c["access_token"] = sign(map[string]any{"sub": "reader"}) }, code: "invalid_access_token_expiry", status: 401, apiCalls: 1},
		{name: "oversized session", tokens: func(c map[string]any) {
			c["access_token"] = sign(map[string]any{"sub": "reader", "exp": time.Now().Add(time.Hour).Unix(), "padding": strings.Repeat("x", 4000)})
		}, code: "session_token_too_large", status: 422, apiCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			// Each scenario starts with healthy diagnostic state; protocol/key
			// caches still belong to each real provider exchange.
			second.dependencies = [2]dependencyDiagnostic{}
			name := tc.name
			keysStatus = http.StatusOK
			if name == "keys unavailable" {
				keysStatus = http.StatusServiceUnavailable
			}
			if name == "keys refused" {
				keysStatus = http.StatusForbidden
			}
			if name == "keys pending" {
				keysStatus = http.StatusAccepted
			}
			alterIdentity, alterTokens = tc.identity, tc.tokens
			apiBody, apiStatus = testAccess, http.StatusOK
			if tc.body != "" {
				apiBody = tc.body
			}
			if tc.apiStatus != 0 {
				apiStatus = tc.apiStatus
			}
			apiCalls.Store(0)
			used = false
			idAudience = "catalog-ui"
			if name == "wrong ID-token audience" {
				idAudience = "catalog-api"
			}
			wrongNonce = name == "wrong nonce"
			serviceIdentity = name == "service identity"
			start := uiRequest(s, "POST", "/auth/login", "{}")
			if start.Code != 200 {
				t.Fatalf("start: %d", start.Code)
			}
			if err := json.Unmarshal(start.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			authorize, err := url.Parse(reply["location"])
			if err != nil {
				t.Fatal(err)
			}
			query := authorize.Query()
			nonce = query.Get("nonce")
			challenge = query.Get("code_challenge")
			before := time.Now()
			response := uiRequest(second, "GET", "/auth/callback?state="+query.Get("state")+"&code=one-time-code", "", start.Result().Cookies()[0])
			records := hostLogRecords(t, logs)
			wantLevel, wantEvent := "DEBUG", "ui.request_failed"
			if tc.status == http.StatusSeeOther {
				wantEvent = "ui.session_established"
			}
			if name == "keys unavailable" || name == "keys refused" || name == "keys pending" || name == "wrong token type" || name == "missing token lifetime" || name == "API unavailable" || name == "API invalid context" {
				wantLevel, wantEvent = "WARN", "dependency.unavailable"
			}
			if name == "oversized session" {
				wantLevel = "WARN"
			}
			if len(records) != 1 || records[0]["level"] != wantLevel || records[0]["event"] != wantEvent || strings.Contains(logs.String(), "private-") || strings.Contains(logs.String(), nonce) || strings.Contains(logs.String(), "one-time-code") || strings.Contains(logs.String(), start.Result().Cookies()[0].Value) {
				t.Fatalf("callback diagnostic: %s", logs.String())
			}
			if tc.status == http.StatusSeeOther {
				cookies := response.Result().Cookies()
				if response.Code != http.StatusSeeOther || response.Header().Get("Location") != c.BasePath+"/catalog" || len(cookies) != 2 || cookies[0].MaxAge != -1 || cookies[1].Name != c.CookieName || apiCalls.Load() != 1 {
					t.Fatalf("accepted login = %d %v, API calls = %d", response.Code, response.Header(), apiCalls.Load())
				}
				request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/session", nil)
				request.AddCookie(cookies[1])
				retained, err := second.readSession(request)
				expiry := time.Unix(retained.Expires, 0)
				after := time.Now()
				// OAuth exchange expiry uses wall time, which can move backward.
				// Bound its Unix-second value by both observed endpoints.
				earliest := time.Unix(min(before.Unix(), after.Unix()), 0).Add(tc.lifetime)
				latest := time.Unix(max(before.Unix(), after.Unix()), 0).Add(tc.lifetime)
				if err != nil || expiry.Before(earliest) || expiry.After(latest) || !cookies[1].Expires.Equal(expiry) {
					t.Fatalf("session expiry = %v, cookie expiry = %v, expected range = [%v, %v], error = %v", expiry, cookies[1].Expires, earliest, latest, err)
				}
				return
			}
			assertUIProblem(t, response, tc.status, tc.code)
			if apiCalls.Load() != tc.apiCalls {
				t.Fatalf("API calls = %d, want %d", apiCalls.Load(), tc.apiCalls)
			}
			if len(response.Result().Cookies()) != 1 || response.Result().Cookies()[0].MaxAge != -1 {
				t.Fatal("rejected exchange did not clear pending login")
			}
			for _, cookie := range response.Result().Cookies() {
				if cookie.Name == c.CookieName {
					t.Fatal("invalid login set session")
				}
			}
		})
	}
}
