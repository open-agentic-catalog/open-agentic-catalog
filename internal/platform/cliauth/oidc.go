package cliauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"golang.org/x/oauth2"
)

const exchangeTimeout = 15 * time.Second

// Options are independent client inputs. Issuer and API TLS roots are separate.
type Options struct {
	Issuer, ClientID, APIOrigin, IssuerCAFile, APICAFile, File, Listen string
	Scopes                                                             []string
	Timeout                                                            time.Duration
}

func issuerOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.RawPath != "" {
		return "", safeerr.New("auth: issuer must be an HTTPS URL without credentials, query or fragment; HTTP requires a literal loopback IP")
	}
	// This origin confines backchannel traffic. Keep the full issuer unchanged
	// for discovery metadata, callback, ID-token and stored identity checks.
	return controlclient.Origin(u.Scheme + "://" + u.Host)
}

func validText(s string, n int) bool {
	if len(s) == 0 || len(s) > n || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c <= 32 || c == 127 {
			return false
		}
	}
	return true
}

// OAuth codes and refresh tokens are opaque VSCHAR values, not Bearer header
// values. Do not impose the access-token alphabet on form-encoded secrets.
func validOpaque(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	for _, c := range value {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

func (o Options) validate() error {
	if _, err := issuerOrigin(o.Issuer); err != nil {
		return err
	}
	if _, err := controlclient.Origin(o.APIOrigin); err != nil {
		return err
	}
	if !validText(o.ClientID, 256) || o.File == "" || o.Timeout <= 0 || o.Timeout > 10*time.Minute || len(o.Scopes) > 16 {
		return safeerr.New("auth: supply issuer, public client ID, API origin and credential file; timeout must be greater than zero and at most 10m")
	}
	seen := map[string]bool{"openid": true}
	for _, scope := range o.Scopes {
		if !validText(scope, 256) || strings.ContainsAny(scope, "\"\\") || seen[scope] || !asciiScope(scope) {
			return safeerr.New("auth: additional scopes must be distinct bounded OAuth scope values; openid is automatic")
		}
		seen[scope] = true
	}
	return nil
}

func asciiScope(value string) bool {
	for _, c := range value {
		if c > 126 {
			return false
		}
	}
	return true
}

func validateCredentials(c credentials) error {
	if c.Format != 1 || (Options{Issuer: c.Issuer, ClientID: c.ClientID, APIOrigin: c.APIOrigin, File: "selected", Timeout: time.Minute}).validate() != nil || !identity.ValidSubject(c.Subject) || !identity.ValidTenantID(c.TenantID) || !identity.ValidTenantID(c.MembershipID) || !validText(c.Nonce, 128) || c.AuthTime < 0 || !controlclient.ValidToken(c.AccessToken) || c.RefreshToken != "" && !validOpaque(c.RefreshToken, controlclient.MaxToken) || c.ExpiresAt.IsZero() {
		return safeerr.New("auth: invalid credential binding")
	}
	return nil
}

// issuerTransport confines discovery, JWKS and token traffic to the selected
// issuer origin and validates bounded JSON before dependency decoders see it.
type issuerTransport struct {
	base   http.RoundTripper
	origin string
}

func (t issuerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != t.origin || r.URL.User != nil || r.URL.Fragment != "" {
		return nil, safeerr.New("auth: issuer endpoint is outside the configured origin")
	}
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, safeerr.New("auth: issuer request failed")
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxCredentials+1))
	_ = response.Body.Close()
	if err != nil || len(b) > maxCredentials || jsondoc.Validate(b) != nil || !bytes.HasPrefix(bytes.TrimSpace(b), []byte("{")) {
		return nil, safeerr.New("auth: issuer returned an invalid or oversized response")
	}
	if r.Method == http.MethodPost {
		// Refresh may omit id_token, but a supplied value must be a token.
		// oauth2.Token.Extra loses the distinction between absent and null.
		var fields map[string]json.RawMessage
		if json.Unmarshal(b, &fields) != nil {
			return nil, safeerr.New("auth: issuer returned an invalid or oversized response")
		}
		if raw, present := fields["id_token"]; present {
			var id string
			if json.Unmarshal(raw, &id) != nil || id == "" || len(id) > controlclient.MaxToken {
				return nil, safeerr.New("auth: issuer returned an invalid or oversized response")
			}
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(b))
	return response, nil
}

func provider(ctx context.Context, o Options) (*oidc.Provider, oauth2.Config, context.Context, func(), error) {
	origin, err := issuerOrigin(o.Issuer)
	if err != nil {
		return nil, oauth2.Config{}, ctx, func() {}, err
	}
	s, err := controlclient.New(ctx, controlclient.Options{BaseURL: origin, Token: "unused", CAFile: o.IssuerCAFile, Timeout: exchangeTimeout})
	if err != nil {
		return nil, oauth2.Config{}, ctx, func() {}, safeerr.New("auth: cannot configure issuer TLS trust")
	}
	client := *s.Client
	client.Transport = issuerTransport{s.Client.Transport, origin}
	ctx = oidc.ClientContext(ctx, &client)
	p, err := oidc.NewProvider(ctx, o.Issuer)
	if err != nil {
		s.Close()
		return nil, oauth2.Config{}, ctx, func() {}, safeerr.New("auth: issuer discovery failed")
	}
	ep := p.Endpoint()
	ep.AuthStyle = oauth2.AuthStyleInParams // Public client: no secret and no credential-probing retry.
	for _, raw := range []string{ep.AuthURL, ep.TokenURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme+"://"+u.Host != origin || u.User != nil || u.Fragment != "" || u.Opaque != "" {
			s.Close()
			return nil, oauth2.Config{}, ctx, func() {}, safeerr.New("auth: issuer endpoint is outside the configured origin")
		}
	}
	return p, oauth2.Config{ClientID: o.ClientID, Endpoint: ep, Scopes: append([]string{oidc.ScopeOpenID}, o.Scopes...)}, ctx, s.Close, nil
}

func validateToken(t *oauth2.Token, now time.Time) error {
	if t == nil || !controlclient.ValidToken(t.AccessToken) || t.RefreshToken != "" && !validOpaque(t.RefreshToken, controlclient.MaxToken) || !strings.EqualFold(t.TokenType, "Bearer") || !t.Expiry.After(now) || t.Expiry.After(now.Add(24*time.Hour)) {
		return safeerr.New("auth: issuer must return a bounded Bearer access token with an expiry of at most 24h")
	}
	return nil
}

func verifyID(ctx context.Context, p *oidc.Provider, o Options, t *oauth2.Token, nonce string, previous *credentials) (string, int64, error) {
	raw, _ := t.Extra("id_token").(string)
	if previous != nil && raw == "" {
		return previous.Subject, previous.AuthTime, nil
	}
	if len(raw) == 0 || len(raw) > controlclient.MaxToken || raw == t.AccessToken {
		return "", 0, safeerr.New("auth: issuer did not return a bounded ID token")
	}
	id, err := p.Verifier(&oidc.Config{ClientID: o.ClientID}).Verify(ctx, raw)
	if err != nil {
		return "", 0, safeerr.New("auth: ID token verification failed")
	}
	var claims struct {
		AuthorizedParty *string `json:"azp"`
		AuthTime        *int64  `json:"auth_time"`
		Nonce           *string `json:"nonce"`
	}
	if id.Claims(&claims) != nil || !identity.ValidSubject(id.Subject) || len(id.Audience) != 1 || id.Audience[0] != o.ClientID || claims.AuthorizedParty != nil && *claims.AuthorizedParty != o.ClientID || id.IssuedAt.IsZero() || id.IssuedAt.After(time.Now().Add(time.Minute)) || claims.AuthTime != nil && *claims.AuthTime < 0 {
		return "", 0, safeerr.New("auth: ID token identity or client binding is invalid")
	}
	// Optional refresh claims can be absent, but empty/zero values are still
	// supplied claims and must preserve the original authentication binding.
	if previous == nil && id.Nonce != nonce || previous != nil && (id.Subject != previous.Subject || claims.Nonce != nil && *claims.Nonce != previous.Nonce || claims.AuthTime != nil && *claims.AuthTime != previous.AuthTime) {
		return "", 0, safeerr.New("auth: ID token nonce or identity changed")
	}
	if id.AccessTokenHash != "" && id.VerifyAccessToken(t.AccessToken) != nil {
		return "", 0, safeerr.New("auth: ID token does not bind the access token")
	}
	if previous != nil {
		return previous.Subject, previous.AuthTime, nil
	}
	var authTime int64
	if claims.AuthTime != nil {
		authTime = *claims.AuthTime
	}
	return id.Subject, authTime, nil
}

func acceptHuman(ctx context.Context, o Options, token, subject string, previous *credentials) (credentials, error) {
	s, err := controlclient.New(ctx, controlclient.Options{BaseURL: o.APIOrigin, Token: token, CAFile: o.APICAFile, Timeout: exchangeTimeout})
	if err != nil {
		return credentials{}, safeerr.New("auth: cannot configure Catalog connection")
	}
	defer s.Close()
	e, err := controlclient.Exchange(ctx, s.Client, s.Origin, s.Token, controlclient.Request{Method: http.MethodGet, Path: "/api/v1/me", MaxResponseBytes: 256 << 10})
	if err != nil {
		return credentials{}, safeerr.New("auth: Catalog refused the access token or is unavailable; check API audience and active human membership")
	}
	var me struct {
		Subject      string `json:"subject"`
		ActorType    string `json:"actorType"`
		TenantID     string `json:"tenantId"`
		MembershipID string `json:"membershipId"`
	}
	if json.Unmarshal(e.Data, &me) != nil || me.Subject != subject || me.ActorType != "human" || !identity.ValidTenantID(me.TenantID) || !identity.ValidTenantID(me.MembershipID) || previous != nil && (me.TenantID != previous.TenantID || me.MembershipID != previous.MembershipID) {
		return credentials{}, safeerr.New("auth: Catalog identity does not match the signed-in human membership")
	}
	return credentials{Format: 1, Issuer: o.Issuer, ClientID: o.ClientID, APIOrigin: o.APIOrigin, Subject: subject, TenantID: me.TenantID, MembershipID: me.MembershipID}, nil
}

// AccessToken snapshots an explicit credential file, serializing refresh before
// the caller's one API request. It never retries that request or a token exchange.
func AccessToken(ctx context.Context, path, apiOrigin, apiCAFile, issuerCAFile string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, err := openStore(ctx, path)
	if err != nil {
		return "", err
	}
	defer s.close()
	c, err := s.read()
	if err != nil {
		return "", safeerr.New("auth: no usable login credentials; sign in again")
	}
	origin, err := controlclient.Origin(apiOrigin)
	if err != nil {
		return "", err
	}
	if origin != c.APIOrigin {
		return "", safeerr.New("auth: credential API origin does not match the selected API")
	}
	if c.ExpiresAt.After(time.Now().Add(30 * time.Second)) {
		return c.AccessToken, nil
	}
	if c.RefreshToken == "" {
		return "", safeerr.New("auth: credentials expire soon and have no refresh token; sign in again")
	}
	o := Options{Issuer: c.Issuer, ClientID: c.ClientID, APIOrigin: origin, File: path, APICAFile: apiCAFile, Timeout: 30 * time.Second}
	// Trust paths are selected at each invocation, never loaded from credentials.
	o.IssuerCAFile = issuerCAFile
	p, cfg, ctx, closeProvider, err := provider(ctx, o)
	if err != nil {
		return "", err
	}
	defer closeProvider()
	t, err := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: c.RefreshToken}).Token()
	if err != nil {
		return "", safeerr.New("auth: refresh failed; sign in again; no stale-token fallback")
	}
	if err = validateToken(t, time.Now()); err != nil {
		return "", err
	}
	sub, _, err := verifyID(ctx, p, o, t, c.Nonce, &c)
	if err != nil {
		return "", err
	}
	next, err := acceptHuman(ctx, o, t.AccessToken, sub, &c)
	if err != nil {
		return "", err
	}
	next.Nonce = c.Nonce
	next.AuthTime = c.AuthTime
	next.AccessToken = t.AccessToken
	next.RefreshToken = t.RefreshToken
	next.ExpiresAt = t.Expiry
	if err = s.write(next); err != nil {
		return "", err
	}
	return next.AccessToken, nil
}
