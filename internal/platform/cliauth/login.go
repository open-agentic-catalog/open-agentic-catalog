package cliauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"golang.org/x/oauth2"
)

type callbackResult struct {
	code string
	err  error
}
type callback struct {
	host, state, issuer string
	used                atomic.Bool
	result              chan callbackResult
}

func (c *callback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	code, err := c.parse(r)
	if err != nil {
		http.Error(w, "Invalid login callback.", http.StatusBadRequest)
		return
	}
	if !c.used.CompareAndSwap(false, true) {
		http.Error(w, "Login callback already received.", http.StatusConflict)
		return
	}
	result := callbackResult{code: code}
	if code == "" {
		result.err = safeerr.New("auth: issuer declined authorization")
	}
	_, _ = io.WriteString(w, "Login response received. Return to the terminal.\n")
	// A fast exchange or issuer refusal can close the listener immediately.
	// Flush the browser acknowledgement before releasing the login goroutine.
	_ = http.NewResponseController(w).Flush()
	c.result <- result
}

func (c *callback) parse(r *http.Request) (string, error) {
	invalid := safeerr.New("auth: invalid callback")
	if r.Method != http.MethodGet || r.Host != c.host || r.URL.Path != "/oauth/callback" || r.URL.RawPath != "" || len(r.URL.RawQuery) > 16<<10 {
		return "", invalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", invalid
	}
	for key, v := range q {
		if len(v) != 1 || len(v[0]) > 8192 {
			return "", invalid
		}
		switch key {
		case "code", "state", "error", "error_description", "session_state", "iss":
		default:
			return "", invalid
		}
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(c.state)) != 1 || q.Has("iss") && q.Get("iss") != c.issuer {
		return "", invalid
	}
	if q.Get("error") != "" {
		if q.Has("code") {
			return "", invalid
		}
		return "", nil
	}
	if q.Has("error") || !validOpaque(q.Get("code"), 8192) {
		return "", invalid
	}
	return q.Get("code"), nil
}

func listenAddress(value string) error {
	host, port, err := net.SplitHostPort(value)
	n, e := strconv.Atoi(port)
	if err != nil || e != nil || n < 0 || n > 65535 || host != "127.0.0.1" && host != "::1" {
		return safeerr.New("auth: --listen must be 127.0.0.1:PORT or [::1]:PORT; zero selects a free port")
	}
	return nil
}

// Login prints only an authorization URL to diagnostics. The browser is opened
// manually; no shell, browser process, client secret or password flow is used.
func Login(ctx context.Context, o Options, diagnostics io.Writer) error {
	if err := o.validate(); err != nil {
		return err
	}
	if err := listenAddress(o.Listen); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	s, err := openStore(ctx, o.File)
	if err != nil {
		return err
	}
	defer s.close()
	_, err = s.read()
	// Keep the lock until login finishes. A concurrent logout must run after
	// this write, rather than return early and have credentials recreated.
	// Replacing an identity requires logout, even for concurrent logins.
	if err == nil {
		return safeerr.New("auth: credential file already exists; log out before a new login")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	p, cfg, issuerCtx, closeProvider, err := provider(ctx, o)
	if err != nil {
		return err
	}
	defer closeProvider()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.Listen)
	if err != nil {
		return safeerr.New("auth: cannot bind selected loopback callback")
	}
	cfg.RedirectURL = "http://" + listener.Addr().String() + "/oauth/callback"
	c := &callback{host: listener.Addr().String(), state: oauth2.GenerateVerifier(), issuer: o.Issuer, result: make(chan callbackResult, 1)}
	verifier, nonce := oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	server := &http.Server{Handler: c, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(done)
	}()
	defer func() {
		_ = server.Close()
		// Serve returns after Close; preserve the login result during cleanup.
		<-done
	}()
	authorizationURL := cfg.AuthCodeURL(c.state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	// A malformed issuer must not inject terminal controls into instructions.
	if strings.ContainsAny(authorizationURL, "\r\n\x1b") {
		return safeerr.New("auth: unsafe authorization URL")
	}
	if _, err = fmt.Fprintln(diagnostics, "Open this URL in your browser:\n"+authorizationURL); err != nil {
		return safeerr.New("auth: cannot write login instructions")
	}
	var result callbackResult
	select {
	case <-ctx.Done():
		return safeerr.New("auth: login cancelled or deadline exceeded")
	case result = <-c.result:
	}
	if result.err != nil {
		return result.err
	}
	t, err := cfg.Exchange(issuerCtx, result.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return codeExchangeError(err)
	}
	if err = validateToken(t, time.Now()); err != nil {
		return err
	}
	sub, authTime, err := verifyID(issuerCtx, p, o, t, nonce, nil)
	if err != nil {
		return err
	}
	next, err := acceptHuman(ctx, o, t.AccessToken, sub, nil)
	if err != nil {
		return err
	}
	next.Nonce = nonce
	next.AuthTime = authTime
	next.AccessToken = t.AccessToken
	next.RefreshToken = t.RefreshToken
	next.ExpiresAt = t.Expiry
	if _, err = s.read(); !errors.Is(err, os.ErrNotExist) {
		return safeerr.New("auth: credential file changed during login; no credentials replaced")
	}
	return s.write(next)
}

// Classify only fixed OAuth codes and our own transport messages. Dependency
// descriptions can contain the code, token, URL or other private input.
func codeExchangeError(err error) error {
	var refused *oauth2.RetrieveError
	if errors.As(err, &refused) {
		switch refused.ErrorCode {
		case "invalid_grant":
			return safeerr.New("auth: authorization code exchange failed: issuer refused code, redirect or PKCE binding")
		case "invalid_client", "unauthorized_client":
			return safeerr.New("auth: authorization code exchange failed: issuer refused public client registration")
		case "invalid_scope", "access_denied", "not_allowed":
			return safeerr.New("auth: authorization code exchange failed: issuer refused scope or access")
		case "invalid_request":
			return safeerr.New("auth: authorization code exchange failed: issuer rejected token request parameters")
		case "unsupported_grant_type":
			return safeerr.New("auth: authorization code exchange failed: issuer does not support code exchange")
		case "server_error", "temporarily_unavailable":
			return safeerr.New("auth: authorization code exchange failed: issuer is unavailable")
		default:
			return safeerr.New("auth: authorization code exchange failed: issuer refused token request")
		}
	}
	switch safeerr.Message(err) {
	case "auth: issuer request failed":
		return safeerr.New("auth: authorization code exchange failed: issuer request failed")
	case "auth: issuer returned an invalid or oversized response":
		return safeerr.New("auth: authorization code exchange failed: invalid or oversized issuer response")
	}
	return safeerr.New("auth: authorization code exchange failed")
}
