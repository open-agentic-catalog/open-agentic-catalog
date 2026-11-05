//go:build functional

package cliauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// This local issuer verifies the actual code/PKCE exchange. A real Catalog
// authenticator verifies the access JWT and resolves retained human membership.
type loginFixture struct {
	issuer, api                         *httptest.Server
	options                             Options
	key                                 *rsa.PrivateKey
	mu                                  sync.Mutex
	nonce, challenge, redirect, refresh string
	mode                                string
	exchanges, refreshes, meCalls       int
	authTime                            int64
}

func newLoginFixture(t *testing.T, mode string) *loginFixture {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &loginFixture{key: k, mode: mode, refresh: "refresh-original", authTime: time.Now().Add(-time.Minute).Unix()}
	f.issuer = httptest.NewTLSServer(http.HandlerFunc(f.serveIssuer))
	t.Cleanup(f.issuer.Close)
	ca := filepath.Join(privateDirectory(t), "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.issuer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	issuerPath := "/realm"
	switch mode {
	case "issuer-root":
		issuerPath = ""
	case "issuer-root-slash":
		issuerPath = "/"
	case "issuer-path-slash":
		issuerPath = "/realm/"
	}
	f.options = Options{Issuer: f.issuer.URL + issuerPath, ClientID: "catalog-cli", File: filepath.Join(privateDirectory(t), "login"), Listen: "127.0.0.1:0", IssuerCAFile: ca, Timeout: 5 * time.Second, Scopes: []string{"offline_access"}}
	seeds := []identity.Seed{{TenantID: storedControl().TenantID, Issuer: f.options.Issuer, Subject: "human", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}}}
	if mode == "missing-membership" {
		seeds = nil
	}
	store, err := identity.NewMemoryStore(seeds)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := authn.New(t.Context(), config.Auth{Mode: "oidc", Issuer: f.options.Issuer, Audience: "catalog-api", TenancyMode: "dedicated", DedicatedTenantID: storedControl().TenantID, TrustBundleFile: ca}, store)
	if err != nil {
		t.Fatal(err)
	}
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.meCalls++
		mode := f.mode
		f.mu.Unlock()
		a, err := auth.Authenticate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"private-server-diagnostic"}`)
			return
		}
		if mode == "api-subject" {
			a.Subject = "other"
		}
		if mode == "service" {
			a.ActorType = identity.ActorService
		}
		if mode == "membership-change" {
			a.MembershipID = "00000000-0000-7000-8000-000000000099"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"subject": a.Subject, "actorType": a.ActorType, "tenantId": a.TenantID, "membershipId": a.MembershipID})
	}))
	t.Cleanup(f.api.Close)
	f.options.APIOrigin = f.api.URL
	return f
}

func (f *loginFixture) sign(claims map[string]any) string {
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
	if err != nil {
		panic(err)
	}
	b, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	object, err := s.Sign(b)
	if err != nil {
		panic(err)
	}
	encoded, err := object.CompactSerialize()
	if err != nil {
		panic(err)
	}
	return encoded
}

func (f *loginFixture) serveIssuer(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	issuer := f.options.Issuer
	// Discovery removes the terminal slash from its path only. Metadata and
	// signed claims retain the issuer's exact identity.
	path := strings.TrimSuffix(strings.TrimPrefix(issuer, f.issuer.URL), "/")
	endpoint := f.issuer.URL + path
	switch r.URL.Path {
	case path + "/.well-known/openid-configuration":
		value := map[string]any{"issuer": issuer, "authorization_endpoint": endpoint + "/authorize", "token_endpoint": endpoint + "/token", "jwks_uri": endpoint + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}}
		if f.mode == "discovery-issuer" {
			value["issuer"] = "https://other.invalid"
		}
		if f.mode == "discovery-issuer-slash" || f.mode == "refresh-discovery-issuer-slash" {
			value["issuer"] = strings.TrimSuffix(issuer, "/")
		}
		if f.mode == "endpoint-origin" {
			value["token_endpoint"] = "https://do-not-contact.invalid/token"
		}
		if f.mode == "jwks-origin" {
			value["jwks_uri"] = "https://do-not-contact.invalid/keys"
		}
		if err := json.NewEncoder(w).Encode(value); err != nil {
			panic(err)
		}
	case path + "/keys":
		if err := json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}}); err != nil {
			panic(err)
		}
	case path + "/authorize":
		q := r.URL.Query()
		f.nonce = q.Get("nonce")
		f.challenge = q.Get("code_challenge")
		f.redirect = q.Get("redirect_uri")
		if q.Get("client_id") != "catalog-cli" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || f.nonce == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		redirect, _ := url.Parse(f.redirect)
		redirect.RawQuery = url.Values{"state": {q.Get("state")}, "code": {"one-use-private-code"}, "iss": {issuer}}.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	case path + "/token":
		if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("client_id") != "catalog-cli" || r.Form.Has("client_secret") || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		refresh := r.Form.Get("grant_type") == "refresh_token"
		if refresh {
			f.refreshes++
			if f.mode == "revoked" || r.Form.Get("refresh_token") != f.refresh {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"private-provider-diagnostic"}`)
				return
			}
			f.refresh = "refresh-rotated"
		} else {
			f.exchanges++
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "one-use-private-code" || r.Form.Get("redirect_uri") != f.redirect || base64.RawURLEncoding.EncodeToString(hash[:]) != f.challenge || f.exchanges != 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
		}
		if f.mode == "scope-denied" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"not_allowed","error_description":"private-provider-diagnostic"}`)
			return
		}
		claims := map[string]any{"iss": issuer, "sub": "human", "aud": "catalog-api", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
		if f.mode == "api-audience" {
			claims["aud"] = "other-api"
		}
		access := f.sign(claims)
		claims["aud"] = "catalog-cli"
		claims["nonce"] = f.nonce
		claims["auth_time"] = f.authTime
		if refresh {
			switch f.mode {
			case "refresh-empty-nonce":
				claims["nonce"] = ""
			case "refresh-zero-auth-time":
				claims["auth_time"] = 0
			case "refresh-empty-azp":
				claims["azp"] = ""
			case "no-refresh-claims":
				delete(claims, "nonce")
				delete(claims, "auth_time")
			}
		}
		if f.mode == "nonce" {
			claims["nonce"] = "wrong"
		}
		if f.mode == "id-subject" || refresh && f.mode == "refresh-subject" {
			claims["sub"] = "other"
		}
		if f.mode == "id-audience" {
			claims["aud"] = "other-client"
		}
		if f.mode == "id-issuer" {
			claims["iss"] = "https://other.invalid"
		}
		if f.mode == "id-issuer-slash" || refresh && f.mode == "refresh-id-issuer-slash" {
			claims["iss"] = strings.TrimSuffix(issuer, "/")
		}
		if f.mode == "azp" {
			claims["azp"] = "other-client"
		}
		if f.mode == "expired-id" {
			claims["exp"] = time.Now().Add(-time.Minute).Unix()
		}
		if f.mode == "missing-iat" {
			delete(claims, "iat")
		}
		if f.mode == "bad-at-hash" {
			claims["at_hash"] = "not-the-access-token-hash"
		}
		id := f.sign(claims)
		if f.mode == "bad-signature" {
			parts := strings.Split(id, ".")
			signature, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil {
				panic(err)
			}
			signature[0] ^= 1
			parts[2] = base64.RawURLEncoding.EncodeToString(signature)
			id = strings.Join(parts, ".")
		}
		if f.mode == "id-substitution" {
			access = id
		}
		value := map[string]any{"access_token": access, "refresh_token": f.refresh, "token_type": "Bearer", "expires_in": 3600, "id_token": id}
		if refresh && f.mode == "no-refresh-id" {
			delete(value, "id_token")
		}
		if refresh {
			switch f.mode {
			case "refresh-id-null":
				value["id_token"] = nil
			case "refresh-id-empty":
				value["id_token"] = ""
			case "refresh-id-number":
				value["id_token"] = 42
			case "refresh-id-object":
				value["id_token"] = map[string]any{}
			}
		}
		if refresh && f.mode == "unchanged-refresh" {
			delete(value, "refresh_token")
		}
		if f.mode == "missing-expiry" {
			delete(value, "expires_in")
		}
		if f.mode == "oversized" {
			value["access_token"] = strings.Repeat("x", maxCredentials)
		}
		if err := json.NewEncoder(w).Encode(value); err != nil {
			panic(err)
		}
	default:
		http.NotFound(w, r)
	}
}

type loginInstructions struct{ urls chan string }

func (w loginInstructions) Write(b []byte) (int, error) {
	w.urls <- strings.TrimSpace(strings.TrimPrefix(string(b), "Open this URL in your browser:\n"))
	return len(b), nil
}

func (f *loginFixture) login(t *testing.T) error {
	t.Helper()
	instructions := loginInstructions{make(chan string, 1)}
	done := make(chan error, 1)
	go func() { done <- Login(t.Context(), f.options, instructions) }()
	select {
	case err := <-done:
		return err
	case raw := <-instructions.urls:
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := f.issuer.Client().Do(request)
		if err != nil {
			t.Fatal("browser authorization exchange failed")
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatal("callback not accepted")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("login did not produce instructions")
	}
	return <-done
}

func TestLoginActualPKCEAndRotatingRefresh(t *testing.T) {
	for _, mode := range []string{"valid", "issuer-root", "issuer-root-slash", "issuer-path-slash", "no-refresh-id", "no-refresh-claims", "unchanged-refresh"} {
		t.Run(mode, func(t *testing.T) {
			f := newLoginFixture(t, mode)
			if err := f.login(t); err != nil {
				t.Fatal(err)
			}
			s, err := openStore(t.Context(), f.options.File)
			if err != nil {
				t.Fatal(err)
			}
			c, err := s.read()
			s.close()
			if err != nil || c.RefreshToken != "refresh-original" || c.Issuer != f.options.Issuer {
				t.Fatal("credentials not saved")
			}
			c.ExpiresAt = time.Now().Add(-time.Second)
			writeControl(t, f.options.File, c)
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					if _, err := AccessToken(t.Context(), f.options.File, f.options.APIOrigin, "", f.options.IssuerCAFile); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			f.mu.Lock()
			count, me := f.refreshes, f.meCalls
			f.mu.Unlock()
			if count != 1 || me != 2 {
				t.Fatalf("rotation not serialized: refresh=%d me=%d", count, me)
			}
			s, err = openStore(t.Context(), f.options.File)
			if err != nil {
				t.Fatal(err)
			}
			next, err := s.read()
			s.close()
			wantRefresh := "refresh-rotated"
			if mode == "unchanged-refresh" {
				wantRefresh = "refresh-original"
			}
			if err != nil || next.RefreshToken != wantRefresh || !next.ExpiresAt.After(time.Now()) || next.Nonce != c.Nonce || next.AuthTime != f.authTime || next.Issuer != c.Issuer {
				t.Fatal("rotated credentials not retained")
			}
			if err = Logout(t.Context(), f.options.File); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoginRejectsIssuerAndIdentityFaults(t *testing.T) {
	for _, mode := range []string{"discovery-issuer", "discovery-issuer-slash", "endpoint-origin", "jwks-origin", "nonce", "id-subject", "api-subject", "service", "id-audience", "id-issuer", "id-issuer-slash", "azp", "expired-id", "missing-iat", "bad-at-hash", "bad-signature", "id-substitution", "api-audience", "missing-membership", "missing-expiry", "oversized", "scope-denied"} {
		t.Run(mode, func(t *testing.T) {
			// Construct the API authenticator from the valid issuer before injecting
			// discovery faults, so that the intended CLI guard is reached.
			initial := "valid"
			if mode == "missing-membership" {
				initial = mode
			}
			if strings.HasSuffix(mode, "issuer-slash") {
				initial = "issuer-path-slash"
			}
			f := newLoginFixture(t, initial)
			f.mu.Lock()
			f.mode = mode
			f.mu.Unlock()
			err := f.login(t)
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("invalid login accepted or unsafe error")
			}
			if mode == "discovery-issuer-slash" || mode == "id-issuer-slash" {
				want, exchanges := "auth: issuer discovery failed", 0
				if mode == "id-issuer-slash" {
					want, exchanges = "auth: ID token verification failed", 1
				}
				f.mu.Lock()
				calls, me := f.exchanges, f.meCalls
				f.mu.Unlock()
				if safeerr.Message(err) != want || calls != exchanges || me != 0 {
					t.Fatal("issuer slash mismatch escaped its intended guard or reached Catalog admission")
				}
			}
			if _, err = os.Stat(f.options.File); !os.IsNotExist(err) {
				t.Fatal("failed login saved credentials")
			}
		})
	}
}

func awaitInstructions(t *testing.T, w loginInstructions, done <-chan error) string {
	t.Helper()
	select {
	case raw := <-w.urls:
		return raw
	case err := <-done:
		t.Fatalf("login ended before authorization instructions: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("authorization instructions timed out")
	}
	return ""
}

func TestLoginDoesNotReplaceConcurrentCredentials(t *testing.T) {
	f := newLoginFixture(t, "valid")
	w := loginInstructions{make(chan string, 1)}
	done := make(chan error, 1)
	go func() { done <- Login(t.Context(), f.options, w) }()
	raw := awaitInstructions(t, w, done)
	control := storedControl()
	rawCredentials, err := json.Marshal(control) // #nosec G117 -- Synthetic secrets stay in this test's private file.
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately bypass the advisory lock to simulate an external substitution.
	if err = os.WriteFile(f.options.File, rawCredentials, 0600); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := f.issuer.Client().Do(request)
	if err != nil {
		t.Fatal("local browser exchange failed")
	}
	_ = response.Body.Close()
	if err = <-done; err == nil {
		t.Fatal("login overwrote another credential binding")
	}
	token, err := AccessToken(t.Context(), f.options.File, control.APIOrigin, "", "")
	if err != nil || token != control.AccessToken {
		t.Fatal("concurrent credentials changed")
	}
	if err = Login(t.Context(), f.options, io.Discard); err == nil {
		t.Fatal("login replaced an existing file")
	}
}

func TestRefreshFailsClosedWithoutChangingFile(t *testing.T) {
	for _, mode := range []string{"revoked", "refresh-subject", "refresh-id-issuer-slash", "refresh-discovery-issuer-slash", "membership-change", "api-audience", "id-substitution", "refresh-id-null", "refresh-id-empty", "refresh-id-number", "refresh-id-object", "refresh-empty-nonce", "refresh-zero-auth-time", "refresh-empty-azp"} {
		t.Run(mode, func(t *testing.T) {
			initial := "valid"
			if strings.HasSuffix(mode, "issuer-slash") {
				initial = "issuer-path-slash"
			}
			f := newLoginFixture(t, initial)
			if err := f.login(t); err != nil {
				t.Fatal(err)
			}
			s, err := openStore(t.Context(), f.options.File)
			if err != nil {
				t.Fatal(err)
			}
			c, err := s.read()
			s.close()
			if err != nil {
				t.Fatal(err)
			}
			c.ExpiresAt = time.Now().Add(-time.Second)
			writeControl(t, f.options.File, c)
			before, err := os.ReadFile(f.options.File)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.mode = mode
			f.mu.Unlock()
			token, err := AccessToken(t.Context(), f.options.File, f.options.APIOrigin, "", f.options.IssuerCAFile)
			if err == nil || token != "" || strings.Contains(err.Error(), "private-") {
				t.Fatal("failed refresh returned a stale token or unsafe error")
			}
			f.mu.Lock()
			attempts, meCalls := f.refreshes, f.meCalls
			f.mu.Unlock()
			expectedAttempts := 1
			if mode == "refresh-discovery-issuer-slash" {
				expectedAttempts = 0
			}
			if attempts != expectedAttempts {
				t.Fatal("failed refresh was retried")
			}
			if mode == "refresh-id-issuer-slash" && safeerr.Message(err) != "auth: ID token verification failed" || mode == "refresh-discovery-issuer-slash" && safeerr.Message(err) != "auth: issuer discovery failed" {
				t.Fatal("refresh issuer mismatch did not reach the exact identity guard")
			}
			if strings.HasPrefix(mode, "refresh-") && meCalls != 1 {
				t.Fatal("malformed refresh ID token reached the Catalog")
			}
			after, err := os.ReadFile(f.options.File)
			if err != nil || string(before) != string(after) {
				t.Fatal("failed refresh changed credentials")
			}
		})
	}
}

func TestConcurrentLogoutCannotResurrectLogin(t *testing.T) {
	f := newLoginFixture(t, "valid")
	w := loginInstructions{make(chan string, 1)}
	done := make(chan error, 1)
	go func() { done <- Login(t.Context(), f.options, w) }()
	raw := awaitInstructions(t, w, done)
	loggedOut := make(chan error, 1)
	go func() { loggedOut <- Logout(t.Context(), f.options.File) }()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := f.issuer.Client().Do(request)
	if err != nil {
		t.Fatal("local browser exchange failed")
	}
	_ = response.Body.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = <-loggedOut; err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(t.Context(), f.options.File)
	if err != nil || s.LoggedIn {
		t.Fatal("in-flight login recreated credentials after logout")
	}
}

func TestLoginCancellationAndCallbackCleanup(t *testing.T) {
	f := newLoginFixture(t, "valid")
	ctx, cancel := context.WithCancel(t.Context())
	w := loginInstructions{make(chan string, 1)}
	done := make(chan error, 1)
	go func() { done <- Login(ctx, f.options, w) }()
	raw := awaitInstructions(t, w, done)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	callback := u.Query().Get("redirect_uri")
	cancel()
	if err = <-done; err == nil {
		t.Fatal("cancelled login succeeded")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, callback, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("callback listener retained after cancellation")
	}
	if _, err = os.Stat(f.options.File); !os.IsNotExist(err) {
		t.Fatal("cancelled login saved credentials")
	}
}
