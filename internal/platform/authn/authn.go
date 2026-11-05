// Package authn validates development identities and production OIDC access tokens.
// Authentication resolves current Catalog membership after token verification;
// issuer claims cannot grant Catalog roles. OIDC network access is bounded to
// the operator-configured issuer workflow with its own private trust bundle.
package authn

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// Authenticator validates a bearer token and resolves it to current Catalog
// membership; token claims alone never create Catalog roles.
type Authenticator interface {
	Authenticate(context.Context, string) (identity.Actor, error)
}

const DevelopmentIssuer = "urn:open-agentic-catalog:development"

const maxAccessTokenBytes = 32 << 10
const maxAccessTokenPartBytes = 16 << 10
const accessTokenClockSkew = 5 * time.Minute
const oidcHTTPTimeout = 10 * time.Second
const maxOIDCResponseBytes = 1 << 20
const oidcKeyCacheTTL = 15 * time.Minute
const maxOIDCRedirects = 3

var supportedAccessTokenAlgorithms = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"}

// New constructs the configured authenticator and requires a current membership
// resolver. OIDC construction performs discovery; invalid development identities
// and unsupported authentication modes fail before an authenticator is returned.
func New(ctx context.Context, configuration config.Auth, resolver identity.Resolver) (Authenticator, error) {
	if resolver == nil {
		return nil, errors.New("catalog identity resolver is required")
	}
	switch configuration.Mode {
	case "development":
		if configuration.TrustBundleFile != "" {
			return nil, errors.New("development authentication cannot configure OIDC private trust")
		}
		users := make(map[string]identity.Principal, len(configuration.DevelopmentTokens))
		for token, user := range configuration.DevelopmentTokens {
			if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") || strings.TrimSpace(user.Subject) == "" || !identity.ValidTenantID(user.TenantID) {
				return nil, errors.New("development identity is incomplete")
			}
			users[token] = identity.Principal{Issuer: DevelopmentIssuer, Subject: user.Subject, TenantID: user.TenantID}
		}
		return &Development{users: users, resolver: resolver}, nil
	case "oidc":
		authenticator, err := newOIDC(ctx, configuration, resolver, time.Now)
		if err != nil {
			return nil, err
		}
		return authenticator, nil
	default:
		return nil, fmt.Errorf("unsupported authentication mode %q", configuration.Mode)
	}
}

// Development maps exact configured tokens to principals, then resolves current
// membership on each request so configuration tokens do not freeze authorization.
type Development struct {
	users    map[string]identity.Principal
	resolver identity.Resolver
}

// Authenticate accepts only an exact configured token and resolves its principal
// through the current Catalog identity store.
func (d *Development) Authenticate(ctx context.Context, token string) (identity.Actor, error) {
	principal, ok := d.users[token]
	if !ok {
		return identity.Actor{}, errors.New("development bearer token is invalid")
	}
	actor, err := d.resolver.ResolvePrincipal(ctx, principal)
	if err != nil {
		return identity.Actor{}, fmt.Errorf("resolve development Catalog membership: %w", err)
	}
	return actor, nil
}

// OIDC verifies tokens against one operator-configured issuer and keeps its key
// refresh path separate from tenant-controlled outbound federation.
type OIDC struct {
	mu                sync.RWMutex
	refreshMu         sync.Mutex
	verifier          *oidc.IDTokenVerifier
	verifierCreatedAt time.Time
	resolver          identity.Resolver
	issuer            string
	audience          string
	tenancyMode       string
	dedicatedTenantID string
	organizationClaim string
	client            *http.Client
	now               func() time.Time
	keyCacheTTL       time.Duration
	trustBundleDigest string
}

// TrustBundleDigest identifies the exact startup-loaded private bundle without
// exposing its deployment path or certificate bytes in operator diagnostics.
func (o *OIDC) TrustBundleDigest() string { return o.trustBundleDigest }

// Authenticate checks compact-JWT encoding, signature, issuer, audience, subject,
// and issuance time before resolving tenancy and current Catalog membership.
// OAuth scopes are retained as claims and do not become Catalog roles.
func (o *OIDC) Authenticate(ctx context.Context, token string) (identity.Actor, error) {
	if err := validateJWTEncoding(token); err != nil {
		return identity.Actor{}, err
	}
	verified, err := o.verify(ctx, token)
	if err != nil {
		return identity.Actor{}, fmt.Errorf("verify OIDC token: %w", err)
	}
	var claims map[string]any
	if err := verified.Claims(&claims); err != nil {
		return identity.Actor{}, fmt.Errorf("decode OIDC claims: %w", err)
	}
	subject := verified.Subject
	if !identity.ValidSubject(subject) {
		return identity.Actor{}, errors.New("OIDC access token is missing subject")
	}
	if verified.IssuedAt.IsZero() || verified.IssuedAt.After(o.now().UTC().Add(accessTokenClockSkew)) {
		return identity.Actor{}, errors.New("OIDC access token has an invalid issued-at time")
	}
	principal := identity.Principal{Issuer: o.issuer, Subject: subject, Claims: make(map[string]string)}
	if o.tenancyMode == "dedicated" {
		principal.TenantID = o.dedicatedTenantID
	} else {
		organization, _ := claims[o.organizationClaim].(string)
		if !identity.ValidSubject(organization) {
			return identity.Actor{}, errors.New("OIDC access token is missing the configured organization claim")
		}
		principal.ExternalOrganization = organization
	}
	if scopes, ok := claims["scope"].(string); ok {
		for _, scope := range strings.Fields(scopes) {
			principal.Claims["scope:"+scope] = "true"
		}
	}
	actor, err := o.resolver.ResolvePrincipal(ctx, principal)
	if err != nil {
		return identity.Actor{}, fmt.Errorf("resolve Catalog membership: %w", err)
	}
	return actor, nil
}

// newOIDC isolates issuer HTTP trust, performs initial provider discovery, and
// binds verification to an injected clock. No partially initialized verifier is returned.
func newOIDC(ctx context.Context, configuration config.Auth, resolver identity.Resolver, now func() time.Time) (*OIDC, error) {
	if now == nil {
		return nil, errors.New("OIDC clock is required")
	}
	client := newOIDCHTTPClient(configuration.Issuer)
	var trustBundleDigest string
	if configuration.TrustBundleFile != "" {
		roots, digest, err := loadOIDCTrustBundle(configuration.TrustBundleFile)
		if err != nil {
			return nil, err
		}
		// The standard transport was cloned by newOIDCHTTPClient. Never mutate
		// the process default or silently ignore private trust on a custom base.
		transport, ok := client.Transport.(oidcResponseTransport).base.(*http.Transport)
		if !ok {
			return nil, errors.New("OIDC private trust requires an isolated HTTP transport")
		}
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.RootCAs = roots
		trustBundleDigest = digest
	}
	authenticator := &OIDC{
		resolver:          resolver,
		issuer:            configuration.Issuer,
		audience:          configuration.Audience,
		tenancyMode:       configuration.TenancyMode,
		dedicatedTenantID: configuration.DedicatedTenantID,
		organizationClaim: configuration.OrganizationClaim,
		client:            client,
		trustBundleDigest: trustBundleDigest,
		now:               now,
		keyCacheTTL:       oidcKeyCacheTTL,
	}
	verifier, err := authenticator.discoverVerifier(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	authenticator.verifier = verifier
	authenticator.verifierCreatedAt = now().UTC()
	return authenticator, nil
}

// verify serializes expired-cache refreshes and installs a candidate verifier
// only after it validates this token. Refresh failure never falls back to stale keys.
func (o *OIDC) verifyCached(ctx context.Context, token string) (*oidc.IDToken, error) {
	now := o.now().UTC()
	o.mu.RLock()
	verifier := o.verifier
	createdAt := o.verifierCreatedAt
	o.mu.RUnlock()
	if verifierCacheFresh(now, createdAt, o.keyCacheTTL) {
		return verifier.Verify(ctx, token)
	}
	o.refreshMu.Lock()
	defer o.refreshMu.Unlock()
	o.mu.RLock()
	verifier = o.verifier
	createdAt = o.verifierCreatedAt
	o.mu.RUnlock()
	now = o.now().UTC()
	if verifierCacheFresh(now, createdAt, o.keyCacheTTL) {
		return verifier.Verify(ctx, token)
	}
	candidate, err := o.discoverVerifier(ctx)
	if err != nil {
		return nil, identity.Wrap(identity.CodeDependency, "OIDC provider metadata is temporarily unavailable", err)
	}
	verified, err := candidate.Verify(ctx, token)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.verifier = candidate
	o.verifierCreatedAt = now
	o.mu.Unlock()
	return verified, nil
}

// verifierCacheFresh expires at the TTL boundary and treats clock rollback as stale.
func verifierCacheFresh(now, createdAt time.Time, ttl time.Duration) bool {
	return !now.Before(createdAt) && now.Sub(createdAt) < ttl
}

// discoverVerifier binds provider metadata and key retrieval to the dedicated
// issuer client and the service's explicit asymmetric signing-algorithm allowlist.
func (o *OIDC) discoverVerifier(ctx context.Context) (*oidc.IDTokenVerifier, error) {
	providerContext := oidc.ClientContext(ctx, o.client)
	provider, err := oidc.NewProvider(providerContext, o.issuer)
	if err != nil {
		return nil, err
	}
	var metadata struct {
		JWKSURL string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil || metadata.JWKSURL == "" {
		return nil, errors.New("OIDC provider key endpoint is missing")
	}
	keys := classifiedOIDCKeys{keys: oidc.NewRemoteKeySet(providerContext, metadata.JWKSURL)}
	return oidc.NewVerifier(o.issuer, keys, &oidc.Config{ClientID: o.audience, SupportedSigningAlgs: supportedAccessTokenAlgorithms, Now: o.now}), nil
}

// newOIDCHTTPClient bounds issuer requests and redirects without changing the
// default transport. HTTPS issuers cannot redirect discovery or key reads to HTTP.
func newOIDCHTTPClient(issuer string) *http.Client {
	transport := http.DefaultTransport
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}
	issuerURL, _ := url.Parse(issuer)
	requireHTTPS := issuerURL != nil && issuerURL.Scheme == "https"
	return &http.Client{
		Transport: oidcResponseTransport{base: transport, requireHTTPS: requireHTTPS},
		Timeout:   oidcHTTPTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > maxOIDCRedirects {
				return errors.New("OIDC redirect limit exceeded")
			}
			if err := validateOIDCRequestURL(request.URL, requireHTTPS); err != nil {
				return err
			}
			if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && request.URL.Scheme != "https" {
				return errors.New("OIDC redirect cannot downgrade HTTPS")
			}
			return nil
		},
	}
}

type oidcResponseTransport struct {
	base         http.RoundTripper
	requireHTTPS bool
}

// RoundTrip caps issuer response bytes and rejects malformed or duplicate-key
// JSON on success before the OIDC library consumes it. It closes the original body.
func (transport oidcResponseTransport) RoundTrip(request *http.Request) (reply *http.Response, failure error) {
	if err := validateOIDCRequestURL(request.URL, transport.requireHTTPS); err != nil {
		return nil, err
	}
	defer func() {
		if failure != nil {
			failure = identity.Wrap(identity.CodeDependency, "OIDC endpoint is temporarily unavailable", failure)
		}
	}()
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, errors.New("OIDC endpoint returned an unsuccessful status")
	}
	if response.Body == nil {
		return response, nil
	}
	if response.ContentLength > maxOIDCResponseBytes {
		_ = response.Body.Close()
		return nil, errors.New("OIDC response exceeds the byte limit")
	}
	raw, readErr := io.ReadAll(http.MaxBytesReader(nil, response.Body, maxOIDCResponseBytes))
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read bounded OIDC response: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close OIDC response: %w", closeErr)
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		if err := jsondoc.Validate(raw); err != nil {
			return nil, fmt.Errorf("OIDC response is invalid JSON: %w", err)
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))
	return response, nil
}

// validateOIDCRequestURL rejects embedded credentials, missing hosts, and schemes
// outside HTTP(S), preserving the configured issuer's HTTPS requirement.
func validateOIDCRequestURL(value *url.URL, requireHTTPS bool) error {
	if value == nil || value.User != nil || value.Host == "" || value.Scheme != "https" && value.Scheme != "http" {
		return errors.New("OIDC request target is invalid")
	}
	if requireHTTPS && value.Scheme != "https" {
		return errors.New("OIDC request target must preserve HTTPS")
	}
	return nil
}

// validateJWTEncoding bounds compact token and decoded JSON sizes and rejects
// ambiguous JSON before signature verification; it does not authenticate a token.
func validateJWTEncoding(token string) error {
	if token == "" || len(token) > maxAccessTokenBytes {
		return errors.New("OIDC access token size is invalid")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("OIDC access token is not a compact signed JWT")
	}
	for index, part := range parts[:2] {
		raw, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil || len(raw) == 0 || len(raw) > maxAccessTokenPartBytes {
			return errors.New("OIDC access token contains an invalid encoded JSON part")
		}
		if err := jsondoc.Validate(raw); err != nil {
			return fmt.Errorf("OIDC access token contains invalid JSON in part %d: %w", index+1, err)
		}
	}
	return nil
}
