//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// TestOIDCAuthenticatorRejectsDiscoveryFailure preserves constructor failure admission.
func TestOIDCAuthenticatorRejectsDiscoveryFailure(t *testing.T) {
	store, err := identity.NewMemoryStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := New(context.Background(), config.Auth{Mode: "oidc", Issuer: server.URL, Audience: "catalog"}, store); err == nil {
		t.Fatal("failed OIDC discovery was accepted")
	}
}

func TestOIDCAuthenticatorVerifiesAndMapsClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(writer).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})}}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	issuer = server.URL
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const namespaceID = "00000000-0000-7000-8000-000000000010"
	store, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenantID, Issuer: issuer, Subject: "oidc-user", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}, Namespaces: map[string][]identity.Role{namespaceID: {identity.RoleContributor}}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	authenticator, err := newOIDC(context.Background(), config.Auth{Mode: "oidc", Issuer: issuer, Audience: "open-agentic-catalog", TenancyMode: "dedicated", DedicatedTenantID: tenantID}, store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": issuer, "sub": "oidc-user", "aud": "open-agentic-catalog", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "tenant_id": "00000000-0000-7000-8000-000000000999", "roles": []string{"tenant_admin"}, "scope": "catalog.read sources.read", "namespace_roles": map[string][]string{namespaceID: {"publisher"}}}
	token := signOIDCTestToken(t, key, claims)
	actor, err := authenticator.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if actor.Subject != "oidc-user" || actor.TenantID != tenantID || len(actor.Roles) != 0 || actor.Namespaces != nil || actor.Claims["scope:catalog.read"] != "true" || actor.Claims["scope:sources.read"] != "true" {
		t.Fatalf("mapped OIDC actor = %#v", actor)
	}
	decision, decisionErr := store.CheckPermissions(t.Context(), actor, []identity.PermissionCheck{{Permission: "resource.read"}, {Permission: "resource.create", NamespaceID: namespaceID}, {Permission: "access.manage"}, {Permission: "release.publish", NamespaceID: namespaceID}})
	if decisionErr != nil || !reflect.DeepEqual(decision.Allowed, []bool{true, true, false, false}) {
		t.Fatalf("token-carried Catalog roles expanded authority: %#v", actor)
	}
	// Change exactly one claim on a known-good token. A missing iat must not
	// accidentally make an audience, expiry or subject regression pass.
	for _, test := range []struct {
		name, claim string
		value       any
	}{
		{"wrong audience", "aud", "another-service"},
		{"missing audience", "aud", nil},
		{"expired", "exp", now.Add(-time.Hour).Unix()},
		{"missing expiry", "exp", nil},
		{"missing subject", "sub", nil},
		{"missing issued at", "iat", nil},
		{"wrong issuer", "iss", "https://another-issuer.example"},
		{"future not before", "nbf", now.Add(30 * time.Minute).Unix()},
		{"future issued at", "iat", now.Add(accessTokenClockSkew + time.Minute).Unix()},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := cloneAnyMap(claims)
			if test.value == nil {
				delete(invalid, test.claim)
			} else {
				invalid[test.claim] = test.value
			}
			if _, err := authenticator.Authenticate(context.Background(), signOIDCTestToken(t, key, invalid)); err == nil {
				t.Fatal("invalid claim was accepted")
			}
		})
	}
	t.Run("signature from untrusted key", func(t *testing.T) {
		otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := authenticator.Authenticate(context.Background(), signOIDCTestToken(t, otherKey, claims)); err == nil {
			t.Fatal("valid claims signed by an untrusted key were accepted")
		}
	})
	if _, err := authenticator.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("rejected tokens poisoned the valid-token path: %v", err)
	}
	duplicateClaims := fmt.Sprintf(`{"iss":%q,"sub":"oidc-user","sub":"other","aud":"open-agentic-catalog","iat":%d,"exp":%d}`, issuer, now.Unix(), now.Add(time.Hour).Unix())
	if _, err := authenticator.Authenticate(context.Background(), signOIDCRawTestToken(t, key, "test-key", "RS256", duplicateClaims)); err == nil {
		t.Fatal("OIDC token with duplicate claims was accepted")
	}
	if _, err := authenticator.Authenticate(context.Background(), signOIDCRawTestToken(t, key, "test-key", "HS256", string(mustJSON(t, claims)))); err == nil {
		t.Fatal("OIDC token with an unsupported algorithm was accepted")
	}
}

func TestOIDCAuthenticatorSharedTenancyFailsClosed(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(writer).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys"})
		case "/keys":
			_ = json.NewEncoder(writer).Encode(oidcTestKeySet("shared-key", &key.PublicKey))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	issuer = server.URL
	const tenantID = "00000000-0000-7000-8000-000000000001"
	store, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenantID, Issuer: issuer, ExternalOrganization: "organization-one", Subject: "shared-user", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}}})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := New(context.Background(), config.Auth{Mode: "oidc", Issuer: issuer, Audience: "open-agentic-catalog", TenancyMode: "shared", OrganizationClaim: "organization_id"}, store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := map[string]any{"iss": issuer, "sub": "shared-user", "aud": "open-agentic-catalog", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	valid := cloneAnyMap(base)
	valid["organization_id"] = "organization-one"
	if actor, err := authenticator.Authenticate(context.Background(), signOIDCTestTokenWithKeyID(t, key, "shared-key", valid)); err != nil || actor.TenantID != tenantID || len(actor.Roles) != 0 || actor.Namespaces != nil {
		t.Fatalf("shared identity = %#v, %v", actor, err)
	}
	for name, organization := range map[string]any{"missing": nil, "wrong type": []string{"organization-one"}, "unknown": "organization-two"} {
		t.Run(name, func(t *testing.T) {
			claims := cloneAnyMap(base)
			if organization != nil {
				claims["organization_id"] = organization
			}
			if _, err := authenticator.Authenticate(context.Background(), signOIDCTestTokenWithKeyID(t, key, "shared-key", claims)); err == nil {
				t.Fatal("unmapped shared organization was accepted")
			}
		})
	}
}

func TestOIDCKeyRotationAndBoundedProviderOutage(t *testing.T) {
	firstKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	var stateMu sync.RWMutex
	currentKeyID := "first-key"
	currentKey := &firstKey.PublicKey
	unavailable := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		stateMu.RLock()
		keyID, key, down := currentKeyID, currentKey, unavailable
		stateMu.RUnlock()
		if down {
			http.Error(writer, "unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(writer).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys"})
		case "/keys":
			_ = json.NewEncoder(writer).Encode(oidcTestKeySet(keyID, key))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	issuer = server.URL
	const tenantID = "00000000-0000-7000-8000-000000000001"
	store, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenantID, Issuer: issuer, Subject: "oidc-user", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}}})
	if err != nil {
		t.Fatal(err)
	}
	currentTime := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	authenticator, err := newOIDC(context.Background(), config.Auth{Mode: "oidc", Issuer: issuer, Audience: "open-agentic-catalog", TenancyMode: "dedicated", DedicatedTenantID: tenantID}, store, func() time.Time { return currentTime })
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": issuer, "sub": "oidc-user", "aud": "open-agentic-catalog", "iat": currentTime.Unix(), "exp": currentTime.Add(time.Hour).Unix()}
	firstToken := signOIDCTestTokenWithKeyID(t, firstKey, "first-key", claims)
	if _, err := authenticator.Authenticate(context.Background(), firstToken); err != nil {
		t.Fatal(err)
	}
	stateMu.Lock()
	unavailable = true
	stateMu.Unlock()
	currentTime = currentTime.Add(time.Minute)
	if _, err := authenticator.Authenticate(context.Background(), firstToken); err != nil {
		t.Fatalf("cached key was unavailable inside the safe window: %v", err)
	}
	stateMu.Lock()
	unavailable = false
	currentKeyID = "second-key"
	currentKey = &secondKey.PublicKey
	stateMu.Unlock()
	secondToken := signOIDCTestTokenWithKeyID(t, secondKey, "second-key", claims)
	// RemoteKeySet wakes its callers before clearing the completed in-flight
	// fetch. A rotation immediately after verification can briefly reuse that
	// first response. Check bounded propagation, not goroutine scheduling; every
	// attempt still verifies the new signature through the real verifier.
	rotationDeadline := time.Now().Add(time.Second)
	for {
		_, err := authenticator.Authenticate(context.Background(), secondToken)
		if err == nil {
			break
		}
		if time.Now().After(rotationDeadline) {
			t.Fatalf("rotated signing key was not accepted: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	currentTime = currentTime.Add(oidcKeyCacheTTL + time.Second)
	if _, err := authenticator.Authenticate(context.Background(), secondToken); err != nil {
		t.Fatalf("expired provider metadata was not refreshed: %v", err)
	}
	refreshedAt := authenticator.verifierCreatedAt
	currentTime = currentTime.Add(oidcKeyCacheTTL + time.Second)
	if _, err := authenticator.Authenticate(context.Background(), firstToken); err == nil {
		t.Fatal("a stale signing key was accepted by refreshed provider metadata")
	}
	if !authenticator.verifierCreatedAt.Equal(refreshedAt) {
		t.Fatal("failed candidate verification replaced the last valid verifier")
	}
	stateMu.Lock()
	unavailable = true
	stateMu.Unlock()
	currentTime = currentTime.Add(oidcKeyCacheTTL + time.Second)
	if _, err := authenticator.Authenticate(context.Background(), secondToken); err == nil {
		t.Fatal("cached signing key remained trusted after the safe provider-outage window")
	}
}

func TestOIDCDiscoveryResponsesAreBoundedAndDuplicateSafe(t *testing.T) {
	store, err := identity.NewMemoryStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, response := range map[string]string{
		"duplicate": `{"issuer":"one","issuer":"two"}`,
		"oversized": `{"padding":"` + strings.Repeat("x", maxOIDCResponseBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(response))
			}))
			defer server.Close()
			if _, err := New(context.Background(), config.Auth{Mode: "oidc", Issuer: server.URL, Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: "00000000-0000-7000-8000-000000000001"}, store); err == nil {
				t.Fatal("unsafe OIDC discovery response was accepted")
			}
		})
	}
}
