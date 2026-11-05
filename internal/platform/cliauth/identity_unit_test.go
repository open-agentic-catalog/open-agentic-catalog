package cliauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"golang.org/x/oauth2"
)

// The real verifier reads only these in-memory issuer/key documents. No
// listener, DNS lookup, default transport or application process is involved.
func idUnitProvider(t *testing.T, key *rsa.PrivateKey) (context.Context, *oidc.Provider) {
	t.Helper()
	const issuer = "https://issuer.invalid/realm"
	metadata, err := json.Marshal(map[string]any{"issuer": issuer, "jwks_uri": "https://issuer.invalid/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "unit", Algorithm: "RS256", Use: "sig"}}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: authRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != "issuer.invalid" {
			t.Error("ID verifier attempted an unexpected destination or method")
			return nil, errors.New("unexpected memory request")
		}
		var body []byte
		switch r.URL.Path {
		case "/realm/.well-known/openid-configuration":
			body = metadata
		case "/keys":
			body = keys
		default:
			t.Error("ID verifier attempted an unexpected issuer path")
			return nil, errors.New("unexpected memory request")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})}
	ctx := oidc.ClientContext(t.Context(), client)
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, provider
}

func TestIDTokenIdentityAndRefreshBindingsWithoutNetwork(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ctx, provider := idUnitProvider(t, key)
	now := time.Now().UTC()
	authTime := now.Add(-time.Hour).Unix()
	o := Options{Issuer: "https://issuer.invalid/realm", ClientID: "cli"}
	const binding = "auth: ID token identity or client binding is invalid"
	const changed = "auth: ID token nonce or identity changed"
	const verification = "auth: ID token verification failed"
	const missing = "auth: issuer did not return a bounded ID token"
	for _, tc := range []struct {
		name, raw, failure string
		refresh, wrongKey  bool
		change             func(map[string]any)
	}{
		{name: "initial"},
		{name: "optional authentication time", change: func(c map[string]any) { delete(c, "auth_time") }},
		{name: "refresh", refresh: true},
		{name: "refresh without optional claims", refresh: true, change: func(c map[string]any) { delete(c, "nonce"); delete(c, "auth_time") }},
		{name: "refresh without ID token", refresh: true, raw: "absent"},
		{name: "missing initial ID token", raw: "absent", failure: missing},
		{name: "oversized ID token", raw: "oversized", failure: missing},
		{name: "access-token substitution", raw: "substitution", failure: missing},
		{name: "malformed ID token", raw: "malformed", failure: verification},
		{name: "wrong signature", wrongKey: true, failure: verification},
		{name: "wrong issuer", failure: verification, change: func(c map[string]any) { c["iss"] = "https://other.invalid" }},
		{name: "issuer trailing slash differs", failure: verification, change: func(c map[string]any) { c["iss"] = o.Issuer + "/" }},
		{name: "wrong audience", failure: verification, change: func(c map[string]any) { c["aud"] = "other-client" }},
		{name: "expired", failure: verification, change: func(c map[string]any) { c["exp"] = now.Add(-time.Hour).Unix() }},
		{name: "multiple audiences", failure: binding, change: func(c map[string]any) { c["aud"] = []string{"cli", "other-client"} }},
		{name: "authorized party", failure: binding, change: func(c map[string]any) { c["azp"] = "other-client" }},
		{name: "empty subject", failure: binding, change: func(c map[string]any) { c["sub"] = "" }},
		{name: "missing issue time", failure: binding, change: func(c map[string]any) { delete(c, "iat") }},
		{name: "future issue time", failure: binding, change: func(c map[string]any) { c["iat"] = now.Add(time.Hour).Unix() }},
		{name: "negative authentication time", failure: binding, change: func(c map[string]any) { c["auth_time"] = -1 }},
		{name: "missing initial nonce", failure: changed, change: func(c map[string]any) { delete(c, "nonce") }},
		{name: "initial nonce", failure: changed, change: func(c map[string]any) { c["nonce"] = "other-nonce" }},
		{name: "refresh subject", refresh: true, failure: changed, change: func(c map[string]any) { c["sub"] = "other-human" }},
		{name: "refresh nonce", refresh: true, failure: changed, change: func(c map[string]any) { c["nonce"] = "other-nonce" }},
		{name: "refresh authentication time", refresh: true, failure: changed, change: func(c map[string]any) { c["auth_time"] = authTime + 1 }},
		{name: "zero refresh authentication time", refresh: true, failure: changed, change: func(c map[string]any) { c["auth_time"] = 0 }},
		{name: "invalid access-token hash", failure: "auth: ID token does not bind the access token", change: func(c map[string]any) { c["at_hash"] = "invalid" }},
		{name: "valid access-token hash", change: func(c map[string]any) {
			hash := sha256.Sum256([]byte("unit-access"))
			c["at_hash"] = base64.RawURLEncoding.EncodeToString(hash[:len(hash)/2])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{"iss": o.Issuer, "sub": "human", "aud": o.ClientID, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": "nonce", "auth_time": authTime}
			if tc.change != nil {
				tc.change(claims)
			}
			signingKey := key
			if tc.wrongKey {
				signingKey = other
			}
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signingKey}, (&jose.SignerOptions{}).WithHeader("kid", "unit"))
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(claims)
			if err != nil {
				t.Fatal(err)
			}
			signed, err := signer.Sign(body)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := signed.CompactSerialize()
			if err != nil {
				t.Fatal(err)
			}
			token := &oauth2.Token{AccessToken: "unit-access", TokenType: "Bearer", Expiry: now.Add(time.Hour)}
			switch tc.raw {
			case "absent":
				raw = ""
			case "oversized":
				raw = strings.Repeat("x", controlclient.MaxToken+1)
			case "malformed":
				raw = "PRIVATE_MALFORMED_TOKEN"
			case "substitution":
				token.AccessToken = raw
			}
			extra := map[string]any{}
			if tc.raw != "absent" {
				extra["id_token"] = raw
			}
			token = token.WithExtra(extra)
			var previous *credentials
			if tc.refresh {
				previous = &credentials{Subject: "human", Nonce: "nonce", AuthTime: authTime}
			}
			subject, receivedAuthTime, err := verifyID(ctx, provider, o, token, "nonce", previous)
			if tc.failure != "" {
				if err == nil || safeerr.Message(err) != tc.failure || subject != "" || receivedAuthTime != 0 {
					t.Fatalf("invalid ID token escaped its intended guard: %v", err)
				}
				return
			}
			wantAuthTime := authTime
			if !tc.refresh && claims["auth_time"] == nil {
				wantAuthTime = 0
			}
			if err != nil || subject != "human" || receivedAuthTime != wantAuthTime {
				t.Fatalf("valid identity or refresh binding changed: subject=%q authTime=%d error=%v", subject, receivedAuthTime, err)
			}
		})
	}
}
