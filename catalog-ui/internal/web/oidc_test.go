// OIDC units isolate discovery, login-state creation and pre-exchange decisions.
package web

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/gorilla/securecookie"
	"golang.org/x/oauth2"
)

const unitOIDCIssuer = "https://issuer.example.test/realm"
const unitOIDCDiscovery = `{"issuer":"https://issuer.example.test/realm","authorization_endpoint":"https://issuer.example.test/realm/authorize?ui=fixed","token_endpoint":"https://issuer.example.test/realm/token","jwks_uri":"https://issuer.example.test/realm/keys","response_types_supported":["code"],"subject_types_supported":["public"],"id_token_signing_alg_values_supported":["RS256"]}`

func TestOIDCLoginLoadsLocalSecretAndTrust(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"trimmed secret", "missing secret", "blank secret", "issuer trust failure"} {
		t.Run(mode, func(t *testing.T) {
			c := oidcFixtureConfig(t, "https://api.example.test", unitOIDCIssuer)
			contents := " \t synthetic-client-secret\r\n"
			wantError := ""
			switch mode {
			case "missing secret":
				if err := os.Remove(c.ClientSecretFile); err != nil {
					t.Fatal(err)
				}
				wantError = "UI OIDC client secret file is empty or unreadable"
			case "blank secret":
				contents = " \t\r\n"
				wantError = "UI OIDC client secret file is empty or unreadable"
			case "issuer trust failure":
				c.IssuerCAFile = c.KeysFile
				wantError = "UI private trust contains no certificates"
			}
			if mode != "missing secret" {
				if err := os.WriteFile(c.ClientSecretFile, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.validate(); err != nil {
				t.Fatalf("fixture configuration is invalid: %v", err)
			}
			s := &Server{config: c}
			login, err := s.newOIDCLogin()
			if wantError != "" {
				if login != nil || err == nil || err.Error() != wantError {
					t.Fatalf("OIDC construction = %v, %v; want no login and %q", login != nil, err, wantError)
				}
				return
			}
			if err != nil || login == nil || login.client == nil {
				t.Fatalf("valid OIDC construction failed: %v", err)
			}
			t.Cleanup(login.client.CloseIdleConnections)
			if login.secret != "synthetic-client-secret" {
				t.Fatal("OIDC constructor did not trim the mounted secret")
			}
			if err := os.WriteFile(c.ClientSecretFile, []byte("replacement-client-secret\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if login.secret != "synthetic-client-secret" {
				t.Fatal("mounted replacement changed an initialized login client")
			}
			replacement, err := s.newOIDCLogin()
			if err != nil || replacement == nil {
				t.Fatal("replacement login client could not load the updated secret")
			}
			t.Cleanup(replacement.client.CloseIdleConnections)
			if replacement.secret != "replacement-client-secret" || login.secret != "synthetic-client-secret" {
				t.Fatal("login client activation did not preserve its startup credential")
			}
		})
	}
}

func TestDirectHostOIDCProvider(t *testing.T) {
	t.Parallel()
	const unavailable = "identity provider unavailable"
	const outside = "identity provider endpoint is outside its configured origin"
	for _, test := range []struct {
		name, member, value, raw, wantError string
		status                              int
		transportError                      bool
	}{
		{name: "valid configuration", status: 200},
		{name: "issuer mismatch", status: 200, member: "issuer", value: "https://issuer.example.test/another-realm", wantError: unavailable},
		{name: "foreign authorization", status: 200, member: "authorization_endpoint", value: "https://foreign.invalid/authorize", wantError: outside},
		{name: "foreign token", status: 200, member: "token_endpoint", value: "https://foreign.invalid/token", wantError: outside},
		{name: "scheme downgrade", status: 200, member: "authorization_endpoint", value: "http://issuer.example.test/authorize", wantError: outside},
		{name: "different port", status: 200, member: "token_endpoint", value: "https://issuer.example.test:8443/token", wantError: outside},
		{name: "credentials", status: 200, member: "authorization_endpoint", value: "https://synthetic-user@issuer.example.test/authorize", wantError: outside},
		{name: "fragment", status: 200, member: "token_endpoint", value: unitOIDCIssuer + "/token#private", wantError: outside},
		{name: "malformed endpoint", status: 200, member: "token_endpoint", value: unitOIDCIssuer + "/%zz", wantError: outside},
		{name: "relative endpoint", status: 200, member: "authorization_endpoint", value: "/authorize", wantError: outside},
		{name: "malformed discovery", status: 200, raw: `{"issuer":"private diagnostic"`, wantError: unavailable},
		{name: "unavailable", status: 503, raw: "private provider diagnostic", wantError: unavailable},
		{name: "transport failure", transportError: true, wantError: unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := oidcFixtureConfig(t, "https://api.example.test", unitOIDCIssuer)
			c.BasePath, c.Scopes = "/nested", []string{"catalog.read"}
			s := directHost(t, c)
			raw := unitOIDCDiscovery
			if test.member != "" {
				var doc map[string]any
				if err := json.Unmarshal([]byte(raw), &doc); err != nil {
					t.Fatal(err)
				}
				doc[test.member] = test.value
				encoded, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				raw = string(encoded)
			}
			if test.raw != "" {
				raw = test.raw
			}
			// OAuth configuration must retain the host startup credential when its
			// projected input has changed before the next login attempt.
			if test.name == "valid configuration" {
				if err := os.WriteFile(c.ClientSecretFile, []byte("replacement-client-secret"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			body := &transportResponseBody{reader: strings.NewReader(raw)}
			calls := 0
			s.login.client.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls != 1 || r.Method != http.MethodGet || r.URL.String() != unitOIDCIssuer+"/.well-known/openid-configuration" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("discovery changed its fixed destination, sent credentials or fetched another endpoint")
				}
				if test.transportError {
					return nil, errors.New("private transport diagnostic")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
			})
			provider, configuration, err := s.provider(oidc.ClientContext(t.Context(), s.login.client))
			if test.wantError != "" {
				if err == nil || err.Error() != test.wantError || provider != nil || !reflect.DeepEqual(configuration, oauth2.Config{}) {
					t.Fatal("unsafe discovery returned usable configuration or an unsanitized error")
				}
			} else {
				want := oauth2.Config{ClientID: "catalog-ui", ClientSecret: "synthetic-client-secret", Endpoint: oauth2.Endpoint{AuthURL: unitOIDCIssuer + "/authorize?ui=fixed", TokenURL: unitOIDCIssuer + "/token"}, RedirectURL: c.Origin + "/nested/auth/callback", Scopes: []string{"openid", "catalog.read"}}
				if err != nil || provider == nil || !reflect.DeepEqual(configuration, want) || provider.Endpoint() != want.Endpoint {
					t.Fatal("discovery lost configured credentials, endpoints, callback or scopes")
				}
			}
			wantRead, wantCloses := len(raw), 1
			if test.transportError {
				wantRead, wantCloses = 0, 0
			}
			if calls != 1 || body.bytesRead != wantRead || body.closes != wantCloses {
				t.Fatalf("discovery ownership: calls=%d read=%d closes=%d", calls, body.bytesRead, body.closes)
			}
		})
	}
}

func TestDirectHostOIDCLoginState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		base             string
		seconds, pending int
	}{
		{base: "", seconds: 3600},
		{base: "/nested", seconds: 60, pending: 3},
	} {
		t.Run("deployment"+test.base, func(t *testing.T) {
			c := oidcFixtureConfig(t, "https://api.example.test", unitOIDCIssuer)
			c.Environment, c.Origin = "production", "https://ui.example.test"
			c.BasePath, c.SessionSeconds, c.Scopes = test.base, test.seconds, []string{"catalog.read"}
			s := directHost(t, c)
			at := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return at }
			body := &transportResponseBody{reader: strings.NewReader(unitOIDCDiscovery)}
			calls := 0
			s.login.client.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls != 1 || r.Method != http.MethodGet || r.URL.String() != unitOIDCIssuer+"/.well-known/openid-configuration" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("login start contacted an unexpected endpoint or disclosed credentials")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
			})
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, c.Origin+c.BasePath+"/auth/login", strings.NewReader(`{}`))
			r.Header.Set("Origin", c.Origin)
			r.Header.Set("X-Catalog-Request", "1")
			r.Header.Set("Content-Type", "application/json")
			// Other cookie names must not reduce the four-pending-login allowance.
			r.Header.Set("Cookie", c.CookieName+"-loginish=unrelated")
			for _, id := range []string{"a", "b", "c"}[:test.pending] {
				state := loginState{Binding: s.binding, State: strings.Repeat(id, 43), Nonce: "nonce", Verifier: "verifier", Expires: at.Add(5 * time.Minute).Unix()}
				r.AddCookie(pendingLoginCookie(t, s, state, state.State))
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			var reply map[string]string
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || json.Unmarshal(w.Body.Bytes(), &reply) != nil || len(reply) != 1 || reply["location"] == "" {
				t.Fatal("login start did not return one authorization location")
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatal("login start must write only its new pending cookie")
			}
			cookie := cookies[0]
			var state loginState
			if err := securecookie.DecodeMulti(cookie.Name, cookie.Value, &state, s.codecs...); err != nil {
				t.Fatal("pending cookie could not be authenticated and decoded")
			}
			if state.Binding != s.binding || state.Binding == "" || state.Expires != at.Add(5*time.Minute).Unix() || cookie.Name != c.CookieName+"-login-"+state.State || cookie.Path != c.BasePath+"/" || cookie.Domain != "" || cookie.MaxAge != 300 || !cookie.Expires.Equal(at.Add(5*time.Minute)) || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatal("pending state lost deployment binding, five-minute expiry or cookie protections")
			}
			for _, value := range []string{state.State, state.Nonce, state.Verifier} {
				raw, err := base64.RawURLEncoding.DecodeString(value)
				if err != nil || len(value) != 43 || len(raw) != 32 {
					t.Fatal("login state, nonce and verifier require URL-safe 32-byte values")
				}
			}
			authorize, err := url.Parse(reply["location"])
			if err != nil {
				t.Fatal("invalid authorization location")
			}
			hash := sha256.Sum256([]byte(state.Verifier))
			wantQuery := url.Values{"ui": {"fixed"}, "client_id": {"catalog-ui"}, "redirect_uri": {c.Origin + c.BasePath + "/auth/callback"}, "response_type": {"code"}, "scope": {"openid catalog.read"}, "state": {state.State}, "nonce": {state.Nonce}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}}
			if !reflect.DeepEqual(authorize.Query(), wantQuery) {
				t.Fatal("authorization query lost state/nonce, independent S256 derivation, scopes or callback")
			}
			authorize.RawQuery = ""
			if authorize.String() != unitOIDCIssuer+"/authorize" {
				t.Fatal("login changed its authorization destination")
			}
			for _, secret := range []string{state.Verifier, "synthetic-client-secret"} {
				if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Header().Get("Set-Cookie"), secret) {
					t.Fatal("login exposed its verifier or client secret")
				}
			}
			if calls != 1 || body.closes != 1 || body.bytesRead != len(unitOIDCDiscovery) || len(s.admission) != 0 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Location") != "" {
				t.Fatal("login start retained resources or returned a cacheable/redirecting response")
			}
		})
	}
}

func TestDirectHostOIDCLoginStartRejections(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, input, code      string
		pending, status, calls int
	}{
		{name: "development alias", input: `{"alias":"reader"}`, status: 400, code: "development_login_disabled"},
		{name: "four pending logins", input: `{}`, pending: 4, status: 429, code: "too_many_pending_logins"},
		{name: "failed discovery", input: `{}`, calls: 1, status: 503, code: "identity_provider_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := directHost(t, oidcFixtureConfig(t, "https://api.example.test", unitOIDCIssuer))
			body := &transportResponseBody{reader: strings.NewReader("private provider diagnostic")}
			calls := 0
			s.login.client.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls != test.calls || r.Method != http.MethodGet || r.URL.String() != unitOIDCIssuer+"/.well-known/openid-configuration" {
					t.Fatal("rejected login performed unexpected outbound work")
				}
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: body}, nil
			})
			cookies := make([]*http.Cookie, 0, test.pending)
			for _, id := range []string{"a", "b", "c", "d"}[:test.pending] {
				state := loginState{Binding: s.binding, State: strings.Repeat(id, 43), Nonce: "nonce", Verifier: "verifier", Expires: s.now().Add(5 * time.Minute).Unix()}
				cookies = append(cookies, pendingLoginCookie(t, s, state, state.State))
			}
			w := uiRequest(s, http.MethodPost, "/auth/login", test.input, cookies...)
			assertUIProblem(t, w, test.status, test.code)
			if calls != test.calls || body.closes != test.calls || len(s.admission) != 0 || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("rejected login performed extra work, retained admission or changed browser cookies")
			}
		})
	}
}

func TestDirectHostOIDCCallbackPreflight(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC)
	id := strings.Repeat("s", 43)
	query := "state=" + id + "&code=private-code"
	wrongIssuer := query + "&iss=https%3A%2F%2Fother.invalid"
	for _, test := range []struct {
		name, query, cookie, code string
		change                    func(*loginState)
		status                    int
		clear                     bool
	}{
		{name: "malformed query", query: "state=%zz", status: 400, code: "invalid_login_callback"},
		{name: "duplicate parameter", query: query + "&state=" + id, status: 400, code: "invalid_login_callback"},
		{name: "unknown parameter", query: query + "&redirect=private-target", status: 400, code: "invalid_login_callback"},
		{name: "unsafe state", query: "state=" + url.QueryEscape(strings.Repeat("s", 42)+"+"), status: 400, code: "invalid_login_state"},
		{name: "missing cookie", cookie: "missing", status: 400, code: "invalid_login_state"},
		{name: "tampered cookie", cookie: "tampered", status: 400, code: "invalid_login_state"},
		{name: "deployment mismatch", change: func(state *loginState) { state.Binding = "another-deployment" }, status: 400, code: "invalid_login_state"},
		{name: "expiry boundary", query: wrongIssuer, change: func(state *loginState) { state.Expires = at.Unix() }, status: 400, code: "invalid_login_state"},
		{name: "unexpired state issuer mismatch", query: wrongIssuer, change: func(state *loginState) { state.Expires = at.Add(time.Second).Unix() }, status: 401, code: "login_not_completed", clear: true},
		{name: "oversized code", query: "state=" + id + "&code=" + strings.Repeat("c", 4097), status: 401, code: "login_not_completed", clear: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := oidcFixtureConfig(t, "https://api.example.test", unitOIDCIssuer)
			c.Environment, c.Origin, c.BasePath = "production", "https://ui.example.test", "/nested"
			s := directHost(t, c)
			s.now = func() time.Time { return at }
			state := loginState{Binding: s.binding, State: id, Nonce: "nonce", Verifier: "verifier", Expires: at.Add(5 * time.Minute).Unix()}
			if test.change != nil {
				test.change(&state)
			}
			cookie := pendingLoginCookie(t, s, state, id)
			q := test.query
			if q == "" {
				q = query
			}
			// Provider redirects carry neither Origin nor browser command headers.
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, c.Origin+c.BasePath+"/auth/callback?"+q, nil)
			switch test.cookie {
			case "missing":
			case "tampered":
				r.Header.Set("Cookie", cookie.Name+"="+cookie.Value+"invalid")
			default:
				r.AddCookie(cookie)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			assertUIProblem(t, w, test.status, test.code)
			cookies := w.Result().Cookies()
			if test.clear {
				if len(cookies) != 1 || cookies[0].Name != cookie.Name || cookies[0].MaxAge != -1 || cookies[0].Value != "" || cookies[0].Path != "/nested/" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
					t.Fatal("validated callback did not clear only its pending cookie with the deployment protections")
				}
			} else if len(cookies) != 0 {
				t.Fatal("unvalidated callback changed browser cookies")
			}
			if len(s.admission) != 0 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("callback retained admission or returned a cacheable response")
			}
		})
	}
}

// A callback consumes signed identity evidence and an independent API identity.
// These units isolate their agreement with recording transports, not a login server.
func TestDirectHostOIDCCallbackIdentityAndSessionBinding(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "callback-unit", Algorithm: "RS256", Use: "sig"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, failure, actor string
		change               func(map[string]any)
		apiStatus, apiCalls  int
	}{
		{name: "signed identity agrees with API", apiStatus: 200, apiCalls: 1},
		{name: "multiple audiences with authorized party", change: func(c map[string]any) { c["aud"] = []string{"catalog-ui", "other-client"}; c["azp"] = "catalog-ui" }, apiStatus: 200, apiCalls: 1},
		{name: "multiple audiences without authorized party", change: func(c map[string]any) { c["aud"] = []string{"catalog-ui", "other-client"} }, failure: "invalid_login_identity"},
		{name: "wrong authorized party", change: func(c map[string]any) { c["azp"] = "other-client" }, failure: "invalid_login_identity"},
		{name: "wrong access token hash", change: func(c map[string]any) { c["at_hash"] = "other-access-token" }, failure: "invalid_login_identity"},
		{name: "API subject differs", actor: "other-human", failure: "catalog_login_denied", apiStatus: 200, apiCalls: 1},
		{name: "API service identity", actor: "service", failure: "catalog_login_denied", apiStatus: 200, apiCalls: 1},
		{name: "API refuses membership", failure: "catalog_login_denied", apiStatus: 403, apiCalls: 1},
		{name: "missing access expiry", failure: "invalid_access_token_expiry", apiStatus: 200, apiCalls: 1},
		{name: "oversized session token", failure: "session_token_too_large", apiStatus: 200, apiCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := oidcFixtureConfig(t, "https://api.example.test", unitOIDCIssuer)
			c.Environment, c.Origin, c.BasePath = "production", "https://ui.example.test", "/nested"
			s := directHost(t, c)
			state := loginState{Binding: s.binding, State: strings.Repeat("s", 43), Nonce: "callback-nonce", Verifier: "callback-verifier", Expires: s.now().Add(5 * time.Minute).Unix()}
			accessClaims := map[string]any{"exp": s.now().Add(30 * time.Minute).Unix()}
			if tc.name == "missing access expiry" {
				delete(accessClaims, "exp")
			}
			if tc.name == "oversized session token" {
				accessClaims["extra"] = strings.Repeat("x", 3000)
			}
			payload, err := json.Marshal(accessClaims)
			if err != nil {
				t.Fatal(err)
			}
			// Only the API authenticates this access token. The UI reads its expiry ceiling.
			accessToken := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
			hash := sha256.Sum256([]byte(accessToken))
			claims := map[string]any{"iss": unitOIDCIssuer, "aud": "catalog-ui", "sub": "reader", "iat": s.now().Unix(), "exp": s.now().Add(time.Hour).Unix(), "nonce": state.Nonce, "at_hash": base64.RawURLEncoding.EncodeToString(hash[:16])}
			if tc.change != nil {
				tc.change(claims)
			}
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "callback-unit"))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(claims)
			if err != nil {
				t.Fatal(err)
			}
			signed, err := signer.Sign(raw)
			if err != nil {
				t.Fatal(err)
			}
			idToken, err := signed.CompactSerialize()
			if err != nil {
				t.Fatal(err)
			}
			tokenBody, err := json.Marshal(map[string]any{"access_token": accessToken, "token_type": "Bearer", "expires_in": 3600, "id_token": idToken})
			if err != nil {
				t.Fatal(err)
			}
			issuerCalls := map[string]int{}
			var bodies []*transportResponseBody
			response := func(r *http.Request, status int, raw string) *http.Response {
				body := &transportResponseBody{reader: strings.NewReader(raw)}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: body, Request: r}
			}
			s.login.client.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				issuerCalls[r.URL.Path]++
				switch {
				case r.Method == http.MethodGet && r.URL.String() == unitOIDCIssuer+"/.well-known/openid-configuration":
					return response(r, 200, unitOIDCDiscovery), nil
				case r.Method == http.MethodGet && r.URL.String() == unitOIDCIssuer+"/keys":
					return response(r, 200, string(keys)), nil
				case r.Method == http.MethodPost && r.URL.String() == unitOIDCIssuer+"/token":
					if err := r.ParseForm(); err != nil || r.PostForm.Get("code") != "callback-code" || r.PostForm.Get("code_verifier") != state.Verifier {
						t.Fatal("callback lost its code or PKCE binding")
					}
					return response(r, 200, string(tokenBody)), nil
				default:
					t.Fatalf("unexpected issuer request: %s %s", r.Method, r.URL.Path)
					return nil, errors.New("unexpected issuer request")
				}
			})
			apiCalls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				apiCalls++
				if r.Method != http.MethodGet || r.URL.String() != c.Upstream+"/api/v1/me" || r.Header.Get("Authorization") != "Bearer "+accessToken || r.Header.Get("Cookie") != "" {
					t.Fatal("callback changed its API identity request or sent browser cookies")
				}
				body := testAccess
				if tc.actor == "other-human" {
					body = strings.Replace(body, `"subject":"reader"`, `"subject":"other-human"`, 1)
				} else if tc.actor == "service" {
					body = strings.Replace(body, `"actorType":"human"`, `"actorType":"service"`, 1)
				}
				return response(r, tc.apiStatus, body), nil
			})
			pending := pendingLoginCookie(t, s, state, state.State)
			w := uiRequest(s, http.MethodGet, "/auth/callback?state="+state.State+"&code=callback-code", "", pending)
			cookies := w.Result().Cookies()
			if apiCalls != tc.apiCalls || len(issuerCalls) != 3 || issuerCalls["/realm/token"] != 1 || issuerCalls["/realm/keys"] != 1 || len(s.admission) != 0 || len(cookies) == 0 || cookies[0].Name != pending.Name || cookies[0].MaxAge != -1 {
				t.Fatalf("callback boundary calls/cookies = %d %v %v", apiCalls, issuerCalls, cookies)
			}
			for _, body := range bodies {
				if body.closes != 1 {
					t.Fatal("callback did not close its dependency response")
				}
			}
			if tc.failure != "" {
				status := http.StatusUnauthorized
				if tc.failure == "session_token_too_large" {
					status = http.StatusUnprocessableEntity
				}
				assertUIProblem(t, w, status, tc.failure)
				if len(cookies) != 1 || strings.Contains(w.Body.String(), accessToken) || strings.Contains(w.Body.String(), idToken) {
					t.Fatal("rejected callback issued a session or exposed credentials")
				}
				return
			}
			if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/nested/catalog" || len(cookies) != 2 {
				t.Fatal("accepted callback omitted the fixed redirect or session")
			}
			var got session
			if err := securecookie.DecodeMulti(c.CookieName, cookies[1].Value, &got, s.codecs...); err != nil || got.Binding != s.binding || got.Token != accessToken || got.Expires != s.now().Add(30*time.Minute).Unix() || !cookies[1].Secure || !cookies[1].HttpOnly || cookies[1].SameSite != http.SameSiteLaxMode || cookies[1].Path != "/nested/" {
				t.Fatal("session lost identity, access-token expiry ceiling or browser protections", err)
			}
		})
	}
}

func TestOIDCBrowserCallbackFailureReturnsToLocalSignIn(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"", "/nested"} {
		c := oidcFixtureConfig(t, "https://api.example.test", unitOIDCIssuer)
		c.BasePath = base
		s := directHost(t, c)
		for _, accept := range []string{"text/html,application/xhtml+xml", "application/json"} {
			r := httptest.NewRequestWithContext(t.Context(), "GET", c.Origin+base+"/auth/callback?state=privateSentinel&error_description=privateProviderText", nil)
			r.Header.Set("Accept", accept)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if strings.Contains(accept, "text/html") {
				if w.Code != http.StatusSeeOther || w.Header().Get("Location") != base+"/?sign_in_error=invalid_login_state" {
					t.Fatalf("browser refusal: %d %q", w.Code, w.Header().Get("Location"))
				}
			} else if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_login_state"`) {
				t.Fatalf("JSON refusal: %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "privateSentinel") || strings.Contains(w.Body.String(), "privateProviderText") || len(w.Result().Cookies()) != 0 {
				t.Fatal("refusal disclosed callback values or created cookies")
			}
		}
	}
}

func TestDevelopmentCookieCannotAuthenticateOIDCHostWithSharedKeys(t *testing.T) {
	t.Parallel()
	development := fixtureConfig(t, "https://api.example.test")
	development.Origin = "https://127.0.0.1:4180"
	previous := directHost(t, development)
	cookie := directHostSession(t, previous)
	control := httptest.NewRequestWithContext(t.Context(), "GET", development.Origin+"/auth/session", nil)
	control.AddCookie(cookie)
	if _, err := previous.readSession(control); err != nil {
		t.Fatal("development control cookie is invalid")
	}
	c := oidcFixtureConfig(t, development.Upstream, unitOIDCIssuer)
	c.Environment, c.Origin, c.BasePath, c.CookieName, c.KeysFile = "development", development.Origin, development.BasePath, development.CookieName, development.KeysFile
	current := directHost(t, c)
	response := uiRequest(current, "GET", "/auth/session", "", cookie)
	if response.Code != http.StatusUnauthorized && !(response.Code == http.StatusOK && strings.Contains(response.Body.String(), `"access":null`)) {
		t.Fatalf("development session crossed login modes: %d", response.Code)
	}
}
