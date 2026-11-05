package web

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOIDCConfigurationScopeAndEnvironmentBounds(t *testing.T) {
	c := fixtureConfig(t, "https://api.example.test")
	c.Environment, c.Origin = "production", "https://ui.example.test"
	c.AuthMode, c.DevelopmentUsersFile = "oidc", ""
	c.Issuer, c.ClientID, c.ClientSecretFile = "https://issuer.example.test/realm", "catalog-ui", "mounted-secret"
	c.Scopes = []string{"catalog.read", "catalog.write"}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	// Each scope must be otherwise valid so only the count guard can reject 17.
	scopes := make([]string, 17)
	for i := range scopes {
		scopes[i] = fmt.Sprintf("catalog.permission-%d", i)
	}
	c.Scopes = scopes[:16]
	if err := c.validate(); err != nil {
		t.Fatalf("maximum supported scope count rejected: %v", err)
	}
	for name, change := range map[string]func(*Config){
		"HTTP production issuer": func(c *Config) { c.Issuer = "http://issuer.example.test" },
		"HTTP production UI":     func(c *Config) { c.Origin = "http://127.0.0.1:4173" },
		"missing client":         func(c *Config) { c.ClientID = "" },
		"missing secret":         func(c *Config) { c.ClientSecretFile = "" },
		"development users":      func(c *Config) { c.DevelopmentUsersFile = "users.json" },
		"too many scopes":        func(c *Config) { c.Scopes = scopes },
		"refresh scope":          func(c *Config) { c.Scopes = []string{"offline_access"} },
		"implicit scope":         func(c *Config) { c.Scopes = []string{"openid"} },
		"empty scope":            func(c *Config) { c.Scopes = []string{""} },
		"oversized scope":        func(c *Config) { c.Scopes = []string{strings.Repeat("x", 129)} },
		"scope separator":        func(c *Config) { c.Scopes = []string{"read write"} },
		"duplicate scope":        func(c *Config) { c.Scopes = []string{"read", "read"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := c
			change(&changed)
			if err := changed.validate(); err == nil {
				t.Fatal("unsafe OIDC configuration accepted")
			}
		})
	}
}

func TestPrivateTrustClientConstruction(t *testing.T) {
	// Construct trust material locally; this test never dials or starts a server.
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: at, NotAfter: at.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, public, private)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"missing", nil, "cannot read UI private trust"},
		{"not a certificate", []byte("not a certificate"), "UI private trust contains no certificates"},
		{"bounded file", []byte(strings.Repeat("x", (1<<20)+1)), "cannot read UI private trust"},
		{"valid", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ca.pem")
			if tc.raw != nil {
				if err := os.WriteFile(path, tc.raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			origin := &url.URL{Scheme: "https", Host: "api.example.test"}
			client, err := newClient(origin, path, 4096, 3*time.Second, true)
			if tc.want != "" {
				if err == nil || err.Error() != tc.want || client != nil {
					t.Fatalf("client=%v error=%v, want %s", client, err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			transport, ok := client.Transport.(fixedTransport)
			if !ok || transport.origin != origin || transport.limit != 4096 || !transport.jsonOnly || client.Timeout != 3*time.Second {
				t.Fatal("lost fixed-origin client bounds")
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(tc.raw) || !roots.Equal(transport.base.TLSClientConfig.RootCAs) || transport.base.TLSClientConfig.MinVersion != tls.VersionTLS12 {
				t.Fatal("private trust or TLS minimum not retained")
			}
			if !errors.Is(client.CheckRedirect(nil, nil), http.ErrUseLastResponse) || transport.base.Proxy != nil {
				t.Fatal("redirects or environment proxy enabled")
			}
		})
	}
}

func TestAccessExpiryRejectsMalformedClaims(t *testing.T) {
	for _, payload := range []string{`{`, `[]`, `{}`, `{"exp":0}`, `{"exp":-1}`, `{"exp":"123"}`, `{"exp":1.5}`, `{"exp":9223372036854775808}`} {
		t.Run(payload, func(t *testing.T) {
			expiry, err := accessExpiry("header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature")
			if err == nil || !expiry.IsZero() {
				t.Fatalf("invalid claims accepted: %v, %v", expiry, err)
			}
		})
	}
	for _, token := range []string{"opaque", "header.%.signature"} {
		if _, err := accessExpiry(token); err == nil {
			t.Fatal("invalid JWT encoding accepted")
		}
	}
	// This helper reads lifetime only; it does not authenticate the JWT.
	expiry, err := accessExpiry("header." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1800000000,"aud":"catalog"}`)) + ".signature")
	if err != nil || !expiry.Equal(time.Unix(1800000000, 0)) {
		t.Fatalf("valid expiry not preserved: %v, %v", expiry, err)
	}
}

func TestSessionLifetimeCapAndTokenBounds(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	s := &Server{config: fixtureConfig(t, "http://127.0.0.1:8080"), now: func() time.Time { return at }}
	if err := s.initCookies(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := s.setSession(w, "access-token", at.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Expires.Equal(at.Add(time.Hour)) || cookies[0].MaxAge != 3600 {
		t.Fatal("cookie exceeds configured session lifetime")
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/session", nil)
	r.AddCookie(cookies[0])
	value, err := s.readSession(r)
	if err != nil || value.Expires != at.Add(time.Hour).Unix() || value.Token != "access-token" {
		t.Fatalf("encoded lifetime not capped: %+v, %v", value, err)
	}
	for _, token := range []string{"", strings.Repeat("x", (16<<10)+1), "non-ascii-\u00e9", "delete\x7f", "space token"} {
		if validToken(token) {
			t.Fatal("invalid token accepted")
		}
	}
	if !validToken(strings.Repeat("x", 16<<10)) {
		t.Fatal("token at the byte limit rejected")
	}
}

func TestSessionCookieAdmission(t *testing.T) {
	t.Parallel()
	s := directHost(t, fixtureConfig(t, "https://api.example.test"))
	valid := session{Binding: s.binding, Token: "access-token", Expires: s.now().Add(time.Second).Unix()}
	for _, test := range []struct {
		name     string
		change   func(*session)
		copies   int
		accepted bool
	}{
		{"one unexpired cookie", nil, 1, true},
		{"no cookie", nil, 0, false},
		{"duplicate cookies", nil, 2, false},
		{"other deployment", func(v *session) { v.Binding = "other-deployment" }, 1, false},
		{"exact expiry", func(v *session) { v.Expires = s.now().Unix() }, 1, false},
		{"past expiry", func(v *session) { v.Expires = s.now().Add(-time.Second).Unix() }, 1, false},
		{"unsafe token", func(v *session) { v.Token = "bad\nheader" }, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			if test.change != nil {
				test.change(&candidate)
			}
			// Authenticate each payload, including independently invalid fields,
			// so a signature failure cannot mask session admission checks.
			encoded, err := s.codecs[0].Encode(s.config.CookieName, candidate)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/session", nil)
			for range test.copies {
				r.AddCookie(s.cookie(s.config.CookieName, encoded, time.Unix(candidate.Expires, 0)))
			}
			got, err := s.readSession(r)
			if test.accepted {
				if err != nil || got != candidate {
					t.Fatalf("valid session changed: %+v, %v", got, err)
				}
			} else if err == nil || got != (session{}) {
				t.Fatal("invalid session returned usable credentials")
			}
		})
	}
}
