package cliauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"golang.org/x/oauth2"
)

func TestCodeExchangeErrorUsesOnlyFixedDiagnostics(t *testing.T) {
	for code, want := range map[string]string{
		"invalid_grant":           "issuer refused code, redirect or PKCE binding",
		"invalid_client":          "issuer refused public client registration",
		"unauthorized_client":     "issuer refused public client registration",
		"invalid_scope":           "issuer refused scope or access",
		"access_denied":           "issuer refused scope or access",
		"not_allowed":             "issuer refused scope or access",
		"invalid_request":         "issuer rejected token request parameters",
		"unsupported_grant_type":  "issuer does not support code exchange",
		"server_error":            "issuer is unavailable",
		"temporarily_unavailable": "issuer is unavailable",
		"private-code":            "auth: authorization code exchange failed",
	} {
		err := codeExchangeError(&oauth2.RetrieveError{ErrorCode: code, ErrorDescription: "private-description", Body: []byte("private-body")})
		if !strings.Contains(safeerr.Message(err), want) || strings.Contains(safeerr.Message(err), "private-") {
			t.Fatal("exchange diagnostic exposed issuer data or lost classification")
		}
	}
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{errors.New("private-dependency-error"), "auth: authorization code exchange failed"},
		{safeerr.New("auth: issuer request failed"), "auth: authorization code exchange failed: issuer request failed"},
		{safeerr.New("auth: issuer returned an invalid or oversized response"), "auth: authorization code exchange failed: invalid or oversized issuer response"},
	} {
		if safeerr.Message(codeExchangeError(tc.cause)) != tc.want {
			t.Fatal("exchange exposed private cause or lost transport classification")
		}
	}
}

func privateDirectory(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil { // #nosec G302 -- Private test directory requires owner traversal.
		t.Fatal(err)
	}
	return path
}

func storedControl() credentials {
	return credentials{Format: 1, Issuer: "https://issuer.example/realm", ClientID: "cli", APIOrigin: "https://api.example", Subject: "human", TenantID: "00000000-0000-7000-8000-000000000001", MembershipID: "00000000-0000-7000-8000-000000000002", Nonce: "nonce", AccessToken: "access-secret", RefreshToken: "refresh-secret", ExpiresAt: time.Now().Add(time.Hour)}
}

func writeControl(t *testing.T, path string, c credentials) {
	t.Helper()
	s, err := openStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err = s.write(c); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialStorageLifecycle(t *testing.T) {
	path := filepath.Join(privateDirectory(t), "login.json")

	s, err := Inspect(t.Context(), path)
	if err != nil || s.LoggedIn {
		t.Fatalf("absent status: %+v %v", s, err)
	}
	c := storedControl()
	writeControl(t, path, c)
	s, err = Inspect(t.Context(), path)
	if err != nil || !s.LoggedIn || s.Expired || !s.CanRefresh || s.ExpiresAt == nil {
		t.Fatalf("status: %+v %v", s, err)
	}
	b, err := json.Marshal(s)
	if err != nil || strings.Contains(string(b), "secret") {
		t.Fatal("status exposes credential values")
	}
	token, err := AccessToken(t.Context(), path, c.APIOrigin, "", "")
	if err != nil || token != c.AccessToken {
		t.Fatal("valid credential not selected")
	}
	if _, err = AccessToken(t.Context(), path, "https://other.example", "", ""); err == nil {
		t.Fatal("cross-origin use accepted")
	}
	c.ExpiresAt = time.Now().Add(-time.Minute)
	c.RefreshToken = ""
	writeControl(t, path, c)
	if _, err = AccessToken(t.Context(), path, c.APIOrigin, "", ""); err == nil {
		t.Fatal("expired token accepted")
	}
	if err = Logout(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err = Logout(t.Context(), path); err != nil {
		t.Fatal("logout is not idempotent")
	}
	if _, err = AccessToken(t.Context(), path, c.APIOrigin, "", ""); err == nil {
		t.Fatal("absent credentials accepted")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "login.json.lock" {
		t.Fatal("temporary credential material retained")
	}
}

func TestCredentialStoreRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"directory-mode", "leaf-mode", "symlink", "hardlink", "lock-symlink", "fifo", "directory", "lock-mode", "invalid-json", "unknown", "duplicate", "null", "case", "large", "binding", "parent-symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateDirectory(t)
			path := filepath.Join(dir, "login")
			writeControl(t, path, storedControl())
			b, err := os.ReadFile(path) // #nosec G304 -- Invocation-owned temporary credential fixture.
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory-mode":
				err = os.Chmod(dir, 0755) // #nosec G302 -- Deliberately unsafe directory is the rejection fixture.
			case "leaf-mode":
				err = os.Chmod(path, 0644) // #nosec G302 -- Deliberately unsafe file is the rejection fixture.
			case "symlink":
				err = os.Rename(path, path+".original")
				if err == nil {
					err = os.Symlink(path+".original", path)
				}
			case "hardlink":
				err = os.Link(path, path+".hard")
			case "fifo":
				err = os.Remove(path)
				if err == nil {
					err = syscall.Mkfifo(path, 0600)
				}
			case "directory":
				err = os.Remove(path)
				if err == nil {
					err = os.Mkdir(path, 0700)
				}
			case "lock-mode":
				err = os.Chmod(path+".lock", 0644) // #nosec G302 -- Deliberately unsafe lock fixture.
			case "lock-symlink":
				err = os.Remove(path + ".lock")
				if err == nil {
					err = os.Symlink(path, path+".lock")
				}
			case "parent-symlink":
				alias := filepath.Join(privateDirectory(t), "alias")
				err = os.Symlink(dir, alias)
				path = filepath.Join(alias, "login")
			default:
				raw := string(b)
				switch kind {
				case "invalid-json":
					raw = "["
				case "unknown":
					raw = strings.Replace(raw, `"format":1`, `"format":1,"unknown":true`, 1)
				case "duplicate":
					raw = strings.Replace(raw, `"format":1`, `"format":1,"format":1`, 1)
				case "null":
					raw = strings.Replace(raw, `"refreshToken":"refresh-secret"`, `"refreshToken":null`, 1)
				case "case":
					raw = strings.Replace(raw, `"format"`, `"Format"`, 1)
				case "large":
					raw = strings.Repeat("x", maxCredentials+1)
				case "binding":
					raw = strings.Replace(raw, `"format":1`, `"format":2`, 1)
				}
				err = os.WriteFile(path, []byte(raw), 0600) // #nosec G703 -- Deliberate corruption of this test's own private temporary file.
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Inspect(t.Context(), path); err == nil {
				t.Fatal("unsafe credentials read")
			}
			if err = Logout(t.Context(), path); err == nil {
				t.Fatal("unsafe credentials removed")
			}
		})
	}
}

func TestCredentialLockHonorsCancellation(t *testing.T) {
	path := filepath.Join(privateDirectory(t), "login")
	s, err := openStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err = openStore(ctx, path); err == nil {
		t.Fatal("concurrent lock accepted")
	}
	if _, err = openStore(t.Context(), ""); err == nil {
		t.Fatal("empty path accepted")
	}
	if err = s.write(credentials{}); err == nil {
		t.Fatal("invalid credentials written")
	}
}

// A login recipient can close its server as soon as it receives a result.
// Observe the flush boundary without a real listener or scheduler race.
type callbackAcknowledgment struct {
	*httptest.ResponseRecorder
	results   <-chan callbackResult
	premature bool
}

func (w *callbackAcknowledgment) Flush() {
	w.premature = len(w.results) != 0
	w.ResponseRecorder.Flush()
}

func TestCallbackRejectsForgedInputsWithoutConsumingState(t *testing.T) {
	state := "known-state"
	issuer := "https://issuer.example/realm"
	for _, suffix := range []string{"?state=wrong&code=secret", "?state=known-state&state=known-state&code=secret", "?state=known-state&code=secret&iss=https://other.example", "?state=known-state&code=secret&error=denied", "?state=known-state&code=", "?state=known-state&code=secret&unknown=x", "?state=known-state&code=secret;extra=x", "?state=known-state&code=" + strings.Repeat("x", 17000)} {
		c := &callback{host: "127.0.0.1:12345", state: state, issuer: issuer, result: make(chan callbackResult, 1)}
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+c.host+"/oauth/callback"+suffix, nil)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 400 || c.used.Load() || len(c.result) != 0 || strings.Contains(w.Body.String(), "secret") {
			t.Fatal("forged callback consumed state or leaked input")
		}
		r = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+c.host+"/oauth/callback?state=known-state&code=valid", nil)
		w = httptest.NewRecorder()
		ack := &callbackAcknowledgment{ResponseRecorder: w, results: c.result}
		c.ServeHTTP(ack, r)
		if w.Code != 200 || (<-c.result).code != "valid" || w.Header().Get("Cache-Control") != "no-store" || !w.Flushed || ack.premature {
			t.Fatal("valid callback rejected")
		}
		w = httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 409 {
			t.Fatal("callback replay accepted")
		}
	}
	for _, target := range []string{"http://other.example/oauth/callback?state=known-state&code=x", "http://127.0.0.1:12345/wrong?state=known-state&code=x"} {
		c := &callback{host: "127.0.0.1:12345", state: state, issuer: issuer, result: make(chan callbackResult, 1)}
		if _, err := c.parse(httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)); err == nil {
			t.Fatal("incorrect callback authority/path accepted")
		}
	}
	c := &callback{host: "127.0.0.1:12345", state: state, issuer: issuer, result: make(chan callbackResult, 1)}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+c.host+"/oauth/callback?state=known-state&error=access_denied&error_description=private", nil)
	w := httptest.NewRecorder()
	ack := &callbackAcknowledgment{ResponseRecorder: w, results: c.result}
	c.ServeHTTP(ack, r)
	if w.Code != 200 || (<-c.result).err == nil || strings.Contains(w.Body.String(), "private") || !w.Flushed || ack.premature {
		t.Fatal("issuer denial not safely handled")
	}
}

func TestCallbackPreservesExactIssuerSlash(t *testing.T) {
	for _, path := range []string{"", "/", "/realm", "/realm/"} {
		t.Run(path, func(t *testing.T) {
			issuer := "https://issuer.example" + path
			other := issuer + "/"
			if strings.HasSuffix(issuer, "/") {
				other = strings.TrimSuffix(issuer, "/")
			}
			c := &callback{host: "127.0.0.1:12345", state: "known-state", issuer: issuer, result: make(chan callbackResult, 1)}
			query := url.Values{"state": {c.state}, "code": {"valid"}, "iss": {other}}
			request := func() *http.Request {
				return httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+c.host+"/oauth/callback?"+query.Encode(), nil)
			}
			w := httptest.NewRecorder()
			c.ServeHTTP(w, request())
			if w.Code != http.StatusBadRequest || c.used.Load() || len(c.result) != 0 {
				t.Fatal("callback accepted an issuer differing only by its trailing slash")
			}
			query.Set("iss", issuer)
			w = httptest.NewRecorder()
			c.ServeHTTP(w, request())
			if w.Code != http.StatusOK || !c.used.Load() || len(c.result) != 1 || (<-c.result).code != "valid" {
				t.Fatal("callback rejected the exact configured issuer after an invalid response")
			}
		})
	}
}

type authRoundTrip func(*http.Request) (*http.Response, error)

func (f authRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestIssuerTransportBoundsAndOrigin(t *testing.T) {
	for _, tc := range []struct {
		method, raw string
		valid       bool
	}{
		{http.MethodGet, `{"keys":[]}`, true},
		{http.MethodGet, `{"keys":[],"keys":[]}`, false},
		{http.MethodGet, `[]`, false},
		{http.MethodGet, strings.Repeat("x", maxCredentials+1), false},
		{http.MethodGet, `{"id_token":null}`, true}, // Discovery extensions are not token responses.
		{http.MethodPost, `{"access_token":"access"}`, true},
		{http.MethodPost, `{"id_token":"signed-token"}`, true},
		{http.MethodPost, `{"id_token":null}`, false},
		{http.MethodPost, `{"id_token":""}`, false},
		{http.MethodPost, `{"id_token":42}`, false},
		{http.MethodPost, `{"id_token":{}}`, false},
		{http.MethodPost, `{"id_token":"` + strings.Repeat("x", 16385) + `"}`, false},
	} {
		calls := 0
		upstream := &trackedBody{Reader: strings.NewReader(tc.raw)}
		transport := issuerTransport{origin: "https://issuer.example", base: authRoundTrip(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusOK, Body: upstream}, nil
		})}
		r := httptest.NewRequestWithContext(t.Context(), tc.method, "https://other.example/jwks", nil)
		response, err := transport.RoundTrip(r)
		if response != nil {
			_ = response.Body.Close()
		}
		if err == nil || calls != 0 {
			t.Fatal("issuer request escaped origin")
		}
		r = httptest.NewRequestWithContext(t.Context(), tc.method, "https://issuer.example/jwks", nil)
		response, err = transport.RoundTrip(r)
		if !upstream.closed {
			t.Fatal("issuer body was not closed")
		}
		if tc.valid {
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
		} else if err == nil {
			t.Fatal("invalid issuer JSON accepted")
		}
	}
	transport := issuerTransport{origin: "https://issuer.example", base: authRoundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("private") })}
	response, err := transport.RoundTrip(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://issuer.example/jwks", nil))
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("transport error unsafe")
	}
}

func TestAuthInputAndTokenLimits(t *testing.T) {
	o := Options{Issuer: "https://issuer.example/realm", ClientID: "cli", APIOrigin: "https://api.example", File: "selected", Listen: "127.0.0.1:0", Timeout: time.Minute, Scopes: []string{"offline_access", "catalog.read"}}
	if o.validate() != nil || listenAddress(o.Listen) != nil {
		t.Fatal("valid login inputs rejected")
	}
	for _, origin := range []string{"https://issuer.example", "http://127.0.0.1:8000", "http://[::1]:8000"} {
		for _, path := range []string{"", "/", "/realm", "/realm/"} {
			valid := o
			valid.Issuer = origin + path
			got, err := issuerOrigin(valid.Issuer)
			if err != nil || got != origin || valid.validate() != nil || valid.Issuer != origin+path {
				t.Fatalf("valid exact issuer rejected or changed: %q", valid.Issuer)
			}
		}
	}
	for _, issuer := range []string{"https://user:secret@issuer.example", "https://issuer.example?x=y", "https://issuer.example/realm/?", "http://localhost", "http://issuer.example/", "ftp://issuer.example/", "https://issuer.example/#x", "https://issuer.example/realm/#", "//issuer.example", "https://issuer.example/%2fprivate", "https://issuer.example/%2fprivate/"} {
		bad := o
		bad.Issuer = issuer
		if bad.validate() == nil {
			t.Fatalf("invalid issuer accepted: %q", issuer)
		}
	}
	for _, scope := range []string{"openid", "a b", "", "a\"b"} {
		bad := o
		bad.Scopes = []string{scope}
		if bad.validate() == nil {
			t.Fatal("invalid scope accepted")
		}
	}
	for _, address := range []string{"localhost:0", "0.0.0.0:12345", "127.0.0.1:-1", "[::]:0", "127.0.0.1:65536"} {
		if listenAddress(address) == nil {
			t.Fatal("invalid loopback accepted")
		}
	}
	now := time.Now()
	if !validOpaque("refresh:opaque value", 16384) || validOpaque("refresh\nvalue", 16384) {
		t.Fatal("opaque OAuth secret alphabet is incorrect")
	}
	valid := oauth2.Token{AccessToken: "valid", RefreshToken: "refresh", TokenType: "Bearer", Expiry: now.Add(time.Hour)}
	if validateToken(&valid, now) != nil {
		t.Fatal("valid token rejected")
	}
	for _, change := range []func(*oauth2.Token){func(t *oauth2.Token) { t.TokenType = "Basic" }, func(t *oauth2.Token) { t.AccessToken = "private\nvalue" }, func(t *oauth2.Token) { t.Expiry = time.Time{} }, func(t *oauth2.Token) { t.Expiry = now.Add(25 * time.Hour) }} {
		bad := valid
		change(&bad)
		if validateToken(&bad, now) == nil {
			t.Fatal("invalid token accepted")
		}
	}
}

func FuzzLoginCallback(f *testing.F) {
	for _, seed := range []string{"state=expected&code=one-use", "state=forged&code=private", "state=expected&state=expected&code=private", "state=expected&error=denied"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, query string) {
		if len(query) > 64<<10 {
			t.Skip()
		}
		c := &callback{host: "127.0.0.1:12345", issuer: "https://issuer.example", state: "expected", result: make(chan callbackResult, 1)}
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+c.host+"/oauth/callback", nil)
		r.URL.RawQuery = query
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if c.used.Load() {
			values, err := url.ParseQuery(query)
			if err != nil || len(values["state"]) != 1 || values.Get("state") != "expected" || len(c.result) != 1 || w.Code != http.StatusOK {
				t.Fatal("callback accepted without a unique matching state")
			}
		} else if w.Code != http.StatusBadRequest || len(c.result) != 0 {
			t.Fatal("refused callback changed state")
		}
		if w.Body.String() != "Invalid login callback.\n" && w.Body.String() != "Login response received. Return to the terminal.\n" {
			t.Fatal("callback response disclosed input")
		}
	})
}
