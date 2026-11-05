// Unit tests exercise isolated validation and controlled dependencies.
package authn

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

type resolverFunc func(context.Context, identity.Principal) (identity.Actor, error)

func (resolve resolverFunc) ResolvePrincipal(ctx context.Context, principal identity.Principal) (identity.Actor, error) {
	return resolve(ctx, principal)
}

func TestDevelopmentAuthenticator(t *testing.T) {
	const namespaceID = "00000000-0000-7000-8000-000000000010"
	const tenantID = "00000000-0000-7000-8000-000000000001"
	configuration := config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{"token": {Subject: "user", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleReader}, Namespaces: map[string][]identity.Role{namespaceID: {identity.RoleContributor}}}}}
	store, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenantID, Issuer: DevelopmentIssuer, Subject: "user", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}, Namespaces: map[string][]identity.Role{namespaceID: {identity.RoleContributor}}}})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := New(context.Background(), configuration, store)
	if err != nil {
		t.Fatal(err)
	}
	configuration.DevelopmentTokens["token"].Namespaces[namespaceID][0] = identity.RolePublisher
	actor, err := authenticator.Authenticate(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if actor.Subject != "user" || len(actor.Roles) != 0 || actor.Namespaces != nil {
		t.Fatal("authentication hydrated grants instead of resolving a compact principal")
	}
	checks := []identity.PermissionCheck{{Permission: "resource.read"}, {Permission: "resource.create", NamespaceID: namespaceID}, {Permission: "access.manage"}, {Permission: "release.publish", NamespaceID: namespaceID}}
	decision, err := store.CheckPermissions(t.Context(), actor, checks)
	if err != nil || !reflect.DeepEqual(decision.Allowed, []bool{true, true, false, false}) {
		t.Fatal("configured token data changed stored authority", decision, err)
	}
	actor.Roles = []identity.Role{identity.RoleTenantAdmin}
	actor.Namespaces = map[string][]identity.Role{namespaceID: {identity.RolePublisher}}
	again, err := authenticator.Authenticate(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	decision, err = store.CheckPermissions(t.Context(), again, checks)
	if err != nil || !reflect.DeepEqual(decision.Allowed, []bool{true, true, false, false}) || len(again.Roles) != 0 || again.Namespaces != nil {
		t.Fatal("caller annotations changed current authority", decision, err)
	}
	if _, err := authenticator.Authenticate(context.Background(), "wrong"); err == nil {
		t.Fatal("invalid token was accepted")
	}
}

func TestDevelopmentAuthenticatorRejectsInvalidIdentity(t *testing.T) {
	for _, tc := range []struct{ name, token, subject, tenant string }{
		{"tenant", "token", "user", "not-a-uuid"},
		{"empty token", "", "user", "00000000-0000-7000-8000-000000000001"},
		{"blank token", " ", "user", "00000000-0000-7000-8000-000000000001"},
		{"header injection", "token\r\n", "user", "00000000-0000-7000-8000-000000000001"},
		{"empty subject", "token", "", "00000000-0000-7000-8000-000000000001"},
		{"blank subject", "token", " ", "00000000-0000-7000-8000-000000000001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := identity.NewMemoryStore(nil)
			if err != nil {
				t.Fatal(err)
			}
			authenticator, err := New(t.Context(), config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{tc.token: {Subject: tc.subject, TenantID: tc.tenant}}}, store)
			if err == nil || authenticator != nil {
				t.Fatal("invalid development identity was accepted")
			}
		})
	}

	store, err := identity.NewMemoryStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), config.Auth{Mode: "unsupported"}, store); err == nil {
		t.Fatal("unsupported authentication mode was accepted")
	}
	if _, err := New(context.Background(), config.Auth{Mode: "development"}, nil); err == nil {
		t.Fatal("nil identity resolver was accepted")
	}
	if _, err := newOIDC(context.Background(), config.Auth{}, store, nil); err == nil {
		t.Fatal("nil OIDC clock was accepted")
	}

	membershipErr := errors.New("membership unavailable")
	failing, err := New(context.Background(), config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
		"token": {Subject: "user", TenantID: "00000000-0000-7000-8000-000000000001"},
	}}, resolverFunc(func(context.Context, identity.Principal) (identity.Actor, error) {
		return identity.Actor{}, membershipErr
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.Authenticate(context.Background(), "token"); !errors.Is(err, membershipErr) {
		t.Fatalf("development membership failure = %v", err)
	}

}

func TestOIDCHTTPTargetsPreserveIssuerTransportSecurity(t *testing.T) {
	for name, test := range map[string]struct {
		target       string
		requireHTTPS bool
		valid        bool
	}{
		"HTTPS target":           {target: "https://keys.example.com/jwks", requireHTTPS: true, valid: true},
		"HTTP local target":      {target: "http://127.0.0.1:8080/jwks", valid: true},
		"HTTPS downgrade":        {target: "http://keys.example.com/jwks", requireHTTPS: true},
		"credential-bearing URL": {target: "https://user@keys.example.com/jwks", requireHTTPS: true},
		"unsupported scheme":     {target: "file:///tmp/jwks", requireHTTPS: true},
	} {
		t.Run(name, func(t *testing.T) {
			target, err := url.Parse(test.target)
			if err != nil {
				t.Fatal(err)
			}
			err = validateOIDCRequestURL(target, test.requireHTTPS)
			if test.valid && err != nil {
				t.Fatalf("safe OIDC target was rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("unsafe OIDC target was accepted")
			}
		})
	}

	client := newOIDCHTTPClient("https://identity.example.com")
	transport, ok := client.Transport.(oidcResponseTransport)
	if !ok {
		t.Fatal("OIDC response boundary was not retained")
	}
	transport.base = oidcRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("HTTPS downgrade reached the transport")
		return nil, errors.New("unexpected transport call")
	})
	client.Transport = transport
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:1/keys", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "preserve HTTPS") {
		t.Fatalf("HTTPS issuer downgrade returned %v", err)
	}
}

type oidcRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip oidcRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type oidcTestBody struct {
	io.Reader
	closeErr error
	closed   bool
}

func (body *oidcTestBody) Close() error {
	body.closed = true
	return body.closeErr
}

func TestOIDCResponseTransportBoundsAndValidatesProviderDocuments(t *testing.T) {
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://identity.example.com/keys", nil)
	if err != nil {
		t.Fatal(err)
	}
	baseErr := errors.New("transport unavailable")
	transport := oidcResponseTransport{base: oidcRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, baseErr
	}), requireHTTPS: true}
	response, err := transport.RoundTrip(request)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, baseErr) {
		t.Fatalf("base transport error = %v", err)
	}

	nilBodyResponse := &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header)}
	transport.base = oidcRoundTripFunc(func(*http.Request) (*http.Response, error) { return nilBodyResponse, nil })
	response, err = transport.RoundTrip(request)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil || response != nilBodyResponse {
		t.Fatalf("nil-body response = %#v, %v", response, err)
	}

	exactBody := &oidcTestBody{Reader: strings.NewReader("{}" + strings.Repeat(" ", maxOIDCResponseBytes-2))}
	transport.base = oidcRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: exactBody, ContentLength: maxOIDCResponseBytes}, nil
	})
	response, err = transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("exact provider byte limit rejected: %v", err)
	}
	exactRaw, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || len(exactRaw) != maxOIDCResponseBytes || !exactBody.closed {
		t.Fatalf("exact provider response: bytes=%d closed=%t error=%v", len(exactRaw), exactBody.closed, readErr)
	}

	declaredOversizeBody := &oidcTestBody{Reader: strings.NewReader(`{}`)}
	transport.base = oidcRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: declaredOversizeBody, ContentLength: maxOIDCResponseBytes + 1}, nil
	})
	response, err = transport.RoundTrip(request)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err == nil || !declaredOversizeBody.closed {
		t.Fatalf("declared oversized response = %v, closed=%t", err, declaredOversizeBody.closed)
	}

	expandedBody := &oidcTestBody{Reader: strings.NewReader(strings.Repeat("x", maxOIDCResponseBytes+1))}
	transport.base = oidcRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: expandedBody, ContentLength: -1}, nil
	})
	response, err = transport.RoundTrip(request)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err == nil || !expandedBody.closed {
		t.Fatalf("streamed oversized response = %v, closed=%t", err, expandedBody.closed)
	}

	invalidJSONBody := &oidcTestBody{Reader: strings.NewReader(`{"issuer":`)}
	transport.base = oidcRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: invalidJSONBody}, nil
	})
	response, err = transport.RoundTrip(request)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err == nil || !invalidJSONBody.closed {
		t.Fatalf("invalid provider JSON = %v, closed=%t", err, invalidJSONBody.closed)
	}

	closeFailure := errors.New("close failed")
	closeFailureBody := &oidcTestBody{Reader: strings.NewReader(`{}`), closeErr: closeFailure}
	transport.base = oidcRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: closeFailureBody}, nil
	})
	response, err = transport.RoundTrip(request)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, closeFailure) {
		t.Fatalf("response close error = %v", err)
	}

	for _, status := range []int{http.StatusMultipleChoices} {
		errorBody := &oidcTestBody{Reader: strings.NewReader("temporarily unavailable")}
		transport.base = oidcRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: errorBody}, nil
		})
		response, err = transport.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || string(raw) != "temporarily unavailable" || response.ContentLength != int64(len(raw)) || !errorBody.closed {
			t.Fatalf("bounded error response = %q, length=%d, closed=%t, error=%v", raw, response.ContentLength, errorBody.closed, err)
		}
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		private := strings.NewReader("PRIVATE_ISSUER_BODY")
		outageBody := &oidcTestBody{Reader: private}
		transport.base = oidcRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: outageBody}, nil
		})
		response, err = transport.RoundTrip(request)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if response != nil || identity.ErrorCodeOf(err) != identity.CodeDependency || !outageBody.closed || private.Len() != len("PRIVATE_ISSUER_BODY") || strings.Contains(err.Error(), "PRIVATE_ISSUER_BODY") {
			t.Fatalf("issuer failure must close its unread body and remain safe: status=%d response=%v error=%v closed=%t", status, response, err, outageBody.closed)
		}
	}

}

func TestOIDCRedirectAndCompactJWTGuardrails(t *testing.T) {
	if err := validateJWTEncoding(strings.Repeat("x", maxAccessTokenBytes+1)); err == nil {
		t.Fatal("oversized OIDC token was accepted")
	}
	client := newOIDCHTTPClient("https://identity.example.com")
	httpsRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://identity.example.com/keys", nil)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://identity.example.com/keys", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(httpsRequest, []*http.Request{httpsRequest}); err != nil {
		t.Fatalf("same-security redirect was rejected: %v", err)
	}
	if err := client.CheckRedirect(httpRequest, []*http.Request{httpsRequest}); err == nil {
		t.Fatal("HTTPS redirect downgrade was accepted")
	}
	httpIssuerClient := newOIDCHTTPClient("http://identity.example.com")
	if err := httpIssuerClient.CheckRedirect(httpRequest, []*http.Request{httpsRequest}); err == nil {
		t.Fatal("redirect downgraded an HTTPS hop for an HTTP development issuer")
	}
	if err := client.CheckRedirect(httpsRequest, []*http.Request{httpsRequest, httpsRequest, httpsRequest}); err != nil {
		t.Fatalf("last permitted OIDC redirect rejected: %v", err)
	}
	if err := httpIssuerClient.CheckRedirect(httpRequest, []*http.Request{httpRequest}); err != nil {
		t.Fatalf("HTTP development issuer redirect rejected: %v", err)
	}
	if err := client.CheckRedirect(httpsRequest, []*http.Request{httpsRequest, httpsRequest, httpsRequest, httpsRequest}); err == nil {
		t.Fatal("OIDC redirect limit was not enforced")
	}
	if err := validateOIDCRequestURL(nil, true); err == nil {
		t.Fatal("nil OIDC request URL was accepted")
	}

	validJSON := base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	prefix := validJSON + "." + validJSON + "."
	exactToken := prefix + strings.Repeat("a", maxAccessTokenBytes-len(prefix))
	if err := validateJWTEncoding(exactToken); err != nil {
		t.Fatalf("exact compact JWT byte limit rejected: %v", err)
	}
	if err := validateJWTEncoding(exactToken + "a"); err == nil {
		t.Fatal("oversized well-formed compact JWT accepted")
	}
	// Test valid JSON at the decoded limit so JSON rejection cannot mask a
	// missing size check. Encoding validation does not authenticate signatures.
	for _, n := range []int{maxAccessTokenPartBytes, maxAccessTokenPartBytes + 1} {
		part := base64.RawURLEncoding.EncodeToString([]byte("{}" + strings.Repeat(" ", n-2)))
		for _, token := range []string{part + "." + validJSON + ".signature", validJSON + "." + part + ".signature"} {
			if err := validateJWTEncoding(token); (err == nil) != (n == maxAccessTokenPartBytes) {
				t.Fatalf("decoded JSON part size %d returned %v", n, err)
			}
		}
	}
	partial := base64.RawURLEncoding.EncodeToString([]byte("{} ")) + "!"
	decoded, decodeErr := base64.RawURLEncoding.DecodeString(partial)
	if decodeErr == nil || string(decoded) != "{} " {
		t.Fatal("fixture must decode complete JSON with a trailing encoding error")
	}
	for _, token := range []string{partial + "." + validJSON + ".signature", validJSON + "." + partial + ".signature"} {
		if err := validateJWTEncoding(token); err == nil {
			t.Fatal("partially decoded malformed JWT accepted")
		}
	}

	for name, token := range map[string]string{
		"empty":          "",
		"wrong parts":    validJSON + ".signature",
		"invalid base64": "%." + validJSON + ".signature",
		"empty header":   "." + validJSON + ".signature",
		"invalid JSON":   base64.RawURLEncoding.EncodeToString([]byte(`{"alg":`)) + "." + validJSON + ".signature",
		"oversized part": base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", maxAccessTokenPartBytes+1))) + "." + validJSON + ".signature",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateJWTEncoding(token); err == nil {
				t.Fatalf("unsafe compact JWT %q was accepted", name)
			}
		})
	}
	if err := validateJWTEncoding(validJSON + "." + validJSON + ".signature"); err != nil {
		t.Fatalf("structurally valid compact JWT was rejected: %v", err)
	}
}
