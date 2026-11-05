// Login exchanges use fixed issuer transport and establish only API-accepted human sessions.

package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/securecookie"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"golang.org/x/oauth2"
)

// oidcLogin retains the issuer-only client and mounted confidential-client secret.
type oidcLogin struct {
	client *http.Client
	secret string
}

// loginState carries five-minute callback evidence in an authenticated encrypted cookie.
type loginState struct {
	Binding  string `json:"b"`
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Expires  int64  `json:"e"`
}

// newOIDCLogin loads local credentials and trust without contacting the provider.
func (s *Server) newOIDCLogin() (*oidcLogin, error) {
	raw, err := readFile(s.config.ClientSecretFile, 16<<10)
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return nil, safeerr.New("UI OIDC client secret file is empty or unreadable")
	}
	issuer, _ := fixedURL(s.config.Issuer)
	client, err := newClient(issuer, s.config.IssuerCAFile, 1<<20, 15*time.Second, true)
	if err != nil {
		return nil, err
	}
	return &oidcLogin{client: client, secret: strings.TrimSpace(string(raw))}, nil
}

// provider discovers issuer metadata and confines authorization/token endpoints to its origin.
// The caller supplies an OIDC context carrying the dedicated issuer client.
func (s *Server) provider(ctx context.Context) (*oidc.Provider, oauth2.Config, error) {
	provider, err := oidc.NewProvider(ctx, s.config.Issuer)
	if err != nil {
		return nil, oauth2.Config{}, safeerr.New("identity provider unavailable")
	}
	endpoint := provider.Endpoint()
	issuer, _ := fixedURL(s.config.Issuer)
	for _, raw := range []string{endpoint.AuthURL, endpoint.TokenURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != issuer.Scheme || u.Host != issuer.Host || u.User != nil || u.Fragment != "" {
			return nil, oauth2.Config{}, safeerr.New("identity provider endpoint is outside its configured origin")
		}
	}
	return provider, oauth2.Config{ClientID: s.config.ClientID, ClientSecret: s.login.secret, Endpoint: endpoint, RedirectURL: s.config.Origin + s.config.BasePath + "/auth/callback", Scopes: append([]string{oidc.ScopeOpenID}, s.config.Scopes...)}, nil
}

// startLogin validates a development alias or prepares bounded OIDC state, nonce and PKCE.
// ServeHTTP applies browser-origin and admission checks before this handler runs.
func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var input struct {
		Alias string `json:"alias,omitempty"`
	}
	raw, readErr := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || media != "application/json" || readErr != nil || len(raw) > 4096 || strictJSON(raw, &input) != nil {
		problem(w, 400, "invalid_login_request")
		return
	}
	if s.config.AuthMode == "development" {
		for _, user := range s.users {
			if user.Alias == input.Alias {
				if _, err := s.access(r.Context(), user.Token); err != nil {
					accessProblem(w, err)
					return
				}
				if err := s.setSession(w, user.Token, s.now().Add(time.Duration(s.config.SessionSeconds)*time.Second)); err != nil {
					problem(w, 422, "session_token_too_large")
					return
				}
				if observed := hostObservation(r.Context()); observed != nil {
					observed.sessionEstablished = true
				}
				writeJSON(w, 200, map[string]string{"location": s.config.BasePath + "/catalog"})
				return
			}
		}
		problem(w, 401, "unknown_development_user")
		return
	}
	if input.Alias != "" {
		problem(w, 400, "development_login_disabled")
		return
	}
	pending := 0
	for _, cookie := range r.Cookies() {
		if strings.HasPrefix(cookie.Name, s.config.CookieName+"-login-") {
			pending++
		}
	}
	if pending >= 4 {
		problem(w, 429, "too_many_pending_logins")
		return
	}
	ctx, cancel := context.WithTimeout(s.issuerContext(r.Context()), 15*time.Second)
	defer cancel()
	_, configuration, err := s.provider(ctx)
	if err != nil {
		problem(w, 503, "identity_provider_unavailable")
		return
	}
	if observed := hostObservation(ctx); observed != nil {
		observed.issuerOK = true
	}
	state := loginState{Binding: s.binding, State: oauth2.GenerateVerifier(), Nonce: oauth2.GenerateVerifier(), Verifier: oauth2.GenerateVerifier(), Expires: s.now().Add(5 * time.Minute).Unix()}
	name := s.config.CookieName + "-login-" + state.State
	encoded, err := s.codecs[0].Encode(name, state)
	if err != nil {
		problem(w, 500, "login_unavailable")
		return
	}
	http.SetCookie(w, s.cookie(name, encoded, time.Unix(state.Expires, 0)))
	writeJSON(w, 200, map[string]string{"location": configuration.AuthCodeURL(state.State, oidc.Nonce(state.Nonce), oauth2.S256ChallengeOption(state.Verifier))})
}

// callback validates retained login state and provider identity, then requires the
// Catalog to accept the access token for the same human subject before issuing a cookie.
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		s.callbackProblem(w, r, 400, "invalid_login_callback")
		return
	}
	for key, values := range query {
		if len(values) != 1 || (key != "state" && key != "code" && key != "error" && key != "error_description" && key != "session_state" && key != "iss") {
			s.callbackProblem(w, r, 400, "invalid_login_callback")
			return
		}
	}
	stateID := query.Get("state")
	if len(stateID) != 43 || strings.ContainsAny(stateID, " /+\r\n;") {
		s.callbackProblem(w, r, 400, "invalid_login_state")
		return
	}
	name := s.config.CookieName + "-login-" + stateID
	cookies := r.CookiesNamed(name)
	var state loginState
	if len(cookies) != 1 || securecookie.DecodeMulti(name, cookies[0].Value, &state, s.codecs...) != nil || state.Binding != s.binding || state.State != stateID || state.Expires <= s.now().Unix() {
		s.callbackProblem(w, r, 400, "invalid_login_state")
		return
	}
	s.clearCookie(w, name)
	if query.Get("error") != "" || query.Get("code") == "" || len(query.Get("code")) > 4096 || (query.Get("iss") != "" && query.Get("iss") != s.config.Issuer) {
		s.callbackProblem(w, r, 401, "login_not_completed")
		return
	}
	ctx, cancel := context.WithTimeout(s.issuerContext(r.Context()), 30*time.Second)
	defer cancel()
	provider, configuration, err := s.provider(ctx)
	if err != nil {
		s.callbackProblem(w, r, 503, "identity_provider_unavailable")
		return
	}
	token, err := configuration.Exchange(ctx, query.Get("code"), oauth2.VerifierOption(state.Verifier))
	if err != nil || !strings.EqualFold(token.Type(), "Bearer") || !token.Expiry.After(s.now()) {
		var exchange *oauth2.RetrieveError
		denied := errors.As(err, &exchange) && (exchange.ErrorCode == "invalid_grant" || exchange.ErrorCode == "access_denied")
		if observed := hostObservation(ctx); observed != nil && !denied {
			observed.issuerFailed.Store(true)
		}
		s.callbackProblem(w, r, 401, "login_exchange_failed")
		return
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok || len(rawID) > 16<<10 {
		s.callbackProblem(w, r, 401, "invalid_login_identity")
		return
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: s.config.ClientID, Now: s.now}).Verify(ctx, rawID)
	if err != nil || idToken.Nonce != state.Nonce {
		s.callbackProblem(w, r, 401, "invalid_login_identity")
		return
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
	}
	if idToken.Claims(&claims) != nil || (claims.AuthorizedParty != "" && claims.AuthorizedParty != s.config.ClientID) || (len(idToken.Audience) > 1 && claims.AuthorizedParty != s.config.ClientID) || (idToken.AccessTokenHash != "" && idToken.VerifyAccessToken(token.AccessToken) != nil) {
		s.callbackProblem(w, r, 401, "invalid_login_identity")
		return
	}
	if observed := hostObservation(ctx); observed != nil {
		observed.issuerOK = true
	}
	access, err := s.access(ctx, token.AccessToken)
	if err != nil {
		if errors.Is(err, errCatalogAccessDenied) {
			s.callbackProblem(w, r, http.StatusUnauthorized, "catalog_login_denied")
		} else {
			s.callbackProblem(w, r, http.StatusServiceUnavailable, "catalog_unavailable")
		}
		return
	}
	var actor struct {
		Subject   string `json:"subject"`
		ActorType string `json:"actorType"`
	}
	if json.Unmarshal(access, &actor) != nil || actor.Subject != idToken.Subject || actor.ActorType != "human" {
		s.callbackProblem(w, r, 401, "catalog_login_denied")
		return
	}
	// The API has authenticated this JWT. Reading its expiry only shortens the
	// cookie lifetime; no unverified claim grants identity or permissions here.
	expiry, err := accessExpiry(token.AccessToken)
	if err != nil {
		s.callbackProblem(w, r, 401, "invalid_access_token_expiry")
		return
	}
	if token.Expiry.Before(expiry) {
		expiry = token.Expiry
	}
	if err := s.setSession(w, token.AccessToken, expiry); err != nil {
		s.callbackProblem(w, r, 422, "session_token_too_large")
		return
	}
	if observed := hostObservation(ctx); observed != nil {
		observed.sessionEstablished = true
	}
	http.Redirect(w, r, s.config.BasePath+"/catalog", http.StatusSeeOther) // #nosec G710 -- Config.Validate restricts BasePath to local path segments; no browser or provider redirect destination is used.
}

// callbackProblem sends browser navigations to a local retry screen. Fixed codes
// preserve safe diagnostics; JSON clients retain the original status and envelope.
func (s *Server) callbackProblem(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Add("Vary", "Accept")
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		problem(w, status, code)
		return
	}
	if observed, ok := w.(*hostResponse); ok {
		observed.code = code
	}
	http.Redirect(w, r, s.config.BasePath+"/?sign_in_error="+url.QueryEscape(code), http.StatusSeeOther) // #nosec G710 -- Validated local BasePath and a handler-owned fixed error code; no browser or provider destination is used.
}

// accessExpiry extracts an expiry ceiling from an already API-accepted JWT.
// It does not verify a signature or establish identity or authorization.
func accessExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, safeerr.New("access token must be JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, safeerr.New("invalid JWT payload")
	}
	var claims struct {
		Expires int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Expires <= 0 {
		return time.Time{}, safeerr.New("access token requires expiry")
	}
	return time.Unix(claims.Expires, 0), nil
}
