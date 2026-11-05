// OIDC units retain cryptographic verification while controlling issuer documents
// and time. No fixture starts a server or uses the default network transport.
package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

const unitIssuer = "https://issuer.invalid"
const unitTenant = "00000000-0000-7000-8000-000000000001"

func unitOIDCClaims(now time.Time) map[string]any {
	return map[string]any{"iss": unitIssuer, "sub": "unit-user", "aud": "catalog", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
}

func unitSigningKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// Defer this in refusal fixtures so a panic is an explicit assertion failure.
func failOnOIDCPanic(t *testing.T) {
	t.Helper()
	if recovered := recover(); recovered != nil {
		t.Fatalf("OIDC operation panicked instead of returning an error: %v", recovered)
	}
}

func oidcJSONResponse(request *http.Request, status int, body []byte) *http.Response {
	return &http.Response{Request: request, StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), ContentLength: int64(len(body))}
}

func unitOIDCDocuments(t *testing.T, key *rsa.PrivateKey) http.RoundTripper {
	t.Helper()
	metadata := mustJSON(t, map[string]any{"issuer": unitIssuer, "jwks_uri": unitIssuer + "/keys"})
	keys := mustJSON(t, oidcTestKeySet("test-key", &key.PublicKey))
	return oidcRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case unitIssuer + "/.well-known/openid-configuration":
			return oidcJSONResponse(request, http.StatusOK, metadata), nil
		case unitIssuer + "/keys":
			return oidcJSONResponse(request, http.StatusOK, keys), nil
		default:
			return nil, errors.New("unexpected issuer request")
		}
	})
}

func unitOIDC(t *testing.T, now func() time.Time, transport http.RoundTripper, resolver identity.Resolver) *OIDC {
	t.Helper()
	client := newOIDCHTTPClient(unitIssuer)
	client.Transport = oidcResponseTransport{base: transport, requireHTTPS: true}
	authenticator := &OIDC{issuer: unitIssuer, audience: "catalog", tenancyMode: "dedicated", dedicatedTenantID: unitTenant, organizationClaim: "organization_id", client: client, now: now, keyCacheTTL: oidcKeyCacheTTL, resolver: resolver}
	verifier, err := authenticator.discoverVerifier(t.Context())
	if err != nil || verifier == nil {
		t.Fatalf("create test OIDC verifier: verifier=%v error=%v", verifier, err)
	}
	authenticator.verifier, authenticator.verifierCreatedAt = verifier, now()
	return authenticator
}

func TestOIDCUnitRejectsOversizedRSAKeysAndRecovers(t *testing.T) {
	key := unitSigningKey(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	token := signOIDCTestToken(t, key, unitOIDCClaims(now))
	for _, bits := range []int{4097, 8192} {
		t.Run(big.NewInt(int64(bits)).String()+" bits", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				defer failOnOIDCPanic(t)
				// Synthetic public moduli exercise parsing without generating large keys.
				modulus := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
				modulus.SetBit(modulus, 0, 1)
				keys := mustJSON(t, map[string]any{"keys": []map[string]any{{
					"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256",
					"n": base64.RawURLEncoding.EncodeToString(modulus.Bytes()), "e": "AQAB",
				}}})
				documents := unitOIDCDocuments(t, key)
				oversized, membershipCalls, keyCalls := true, 0, 0
				authenticator := unitOIDC(t, func() time.Time { return now }, oidcRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					if request.URL.Path == "/keys" {
						keyCalls++
						if oversized {
							return oidcJSONResponse(request, http.StatusOK, keys), nil
						}
					}
					return documents.RoundTrip(request)
				}), resolverFunc(func(_ context.Context, principal identity.Principal) (identity.Actor, error) {
					membershipCalls++
					return identity.Actor{Subject: principal.Subject, TenantID: principal.TenantID}, nil
				}))
				actor, err := authenticator.Authenticate(t.Context(), token)
				// A signature mismatch alone would also fail with the vulnerable library.
				// Require the parser's size rejection before any membership lookup.
				if err == nil || !strings.Contains(err.Error(), "modulus too large") || !reflect.DeepEqual(actor, identity.Actor{}) || membershipCalls != 0 || keyCalls != 1 {
					t.Fatalf("oversized JWK: actor=%#v membership=%d keys=%d error=%v", actor, membershipCalls, keyCalls, err)
				}
				// Finish the failed single-flight fetch before the next request.
				synctest.Wait()
				oversized = false
				actor, err = authenticator.Authenticate(t.Context(), token)
				if err != nil || actor.Subject != "unit-user" || actor.TenantID != unitTenant || membershipCalls != 1 || keyCalls != 2 {
					t.Fatalf("valid JWK recovery: actor=%#v membership=%d keys=%d error=%v", actor, membershipCalls, keyCalls, err)
				}
			})
		})
	}
}

// Unlike unitOIDC, this case enters through the public factory and observes its
// configured audience and tenancy through verified principals. The process-wide
// override is inert for every request, including unexpected targets and failures.
// Keep this test and its subtests serial while DefaultTransport is overridden.
func TestOIDCUnitFactoryConfiguresVerifierAndMembership(t *testing.T) {
	key := unitSigningKey(t)
	documents := unitOIDCDocuments(t, key)
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	for _, mode := range []string{"dedicated", "shared"} {
		t.Run(mode, func(t *testing.T) {
			// This checks factory configuration, not cache expiry. Keep its
			// freshness assertion independent of the host's wall clock.
			synctest.Test(t, func(t *testing.T) {
				defer failOnOIDCPanic(t)
				var metadataCalls, keyCalls atomic.Int32
				http.DefaultTransport = oidcRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					if request.Method != http.MethodGet {
						t.Errorf("unexpected issuer method: %s", request.Method)
						return nil, errors.New("unexpected issuer method")
					}
					switch request.URL.String() {
					case unitIssuer + "/.well-known/openid-configuration":
						metadataCalls.Add(1)
					case unitIssuer + "/keys":
						keyCalls.Add(1)
					default:
						t.Errorf("unexpected issuer request: %s", request.URL)
						return nil, errors.New("unexpected issuer request")
					}
					return documents.RoundTrip(request)
				})
				const configuredTenant = "00000000-0000-7000-8000-000000000042"
				configuration := config.Auth{Mode: "oidc", Issuer: unitIssuer, Audience: "factory-client", TenancyMode: mode, DedicatedTenantID: configuredTenant, OrganizationClaim: "catalog_organization"}
				wantPrincipal := identity.Principal{Issuer: unitIssuer, Subject: "unit-user", Claims: map[string]string{}}
				if mode == "dedicated" {
					wantPrincipal.TenantID = configuredTenant
				} else {
					wantPrincipal.ExternalOrganization = "factory-organization"
				}
				wantActor := identity.Actor{Subject: "current-member", TenantID: configuredTenant, Roles: []identity.Role{identity.RoleReader}}
				var principals []identity.Principal
				authenticator, err := New(t.Context(), configuration, resolverFunc(func(_ context.Context, principal identity.Principal) (identity.Actor, error) {
					principals = append(principals, principal)
					return wantActor, nil
				}))
				if err != nil || authenticator == nil {
					t.Fatalf("construct configured OIDC authenticator: %v", err)
				}
				if metadataCalls.Load() != 1 || keyCalls.Load() != 0 || len(principals) != 0 {
					t.Fatalf("startup calls: metadata=%d keys=%d membership=%d", metadataCalls.Load(), keyCalls.Load(), len(principals))
				}
				claims := unitOIDCClaims(time.Now())
				claims["aud"] = "factory-client"
				claims["catalog_organization"] = "factory-organization"
				claims["organization_id"] = "unconfigured-organization"
				actor, err := authenticator.Authenticate(t.Context(), signOIDCTestToken(t, key, claims))
				if err != nil || !reflect.DeepEqual(actor, wantActor) || !reflect.DeepEqual(principals, []identity.Principal{wantPrincipal}) {
					t.Fatalf("configured membership handoff: actor=%#v principals=%#v error=%v", actor, principals, err)
				}
				if metadataCalls.Load() != 1 || keyCalls.Load() != 1 {
					t.Fatalf("verified-token calls: metadata=%d keys=%d", metadataCalls.Load(), keyCalls.Load())
				}
				// A token for the neighboring default audience must not reach membership.
				claims["aud"] = "catalog"
				actor, err = authenticator.Authenticate(t.Context(), signOIDCTestToken(t, key, claims))
				if err == nil || !strings.Contains(err.Error(), "audience") || !reflect.DeepEqual(actor, identity.Actor{}) || len(principals) != 1 {
					t.Fatalf("configured audience denial: actor=%#v membership=%d error=%v", actor, len(principals), err)
				}
			})
		})
	}
}

// Membership is resolved anew for each verified principal, never from JWT roles.
func TestOIDCUnitClaimsAndCurrentMembership(t *testing.T) {
	key := unitSigningKey(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, mode := range []string{"dedicated", "shared"} {
		t.Run(mode, func(t *testing.T) {
			var principals []identity.Principal
			role := identity.RoleReader
			var membershipErr error
			authenticator := unitOIDC(t, func() time.Time { return now }, unitOIDCDocuments(t, key), resolverFunc(func(_ context.Context, principal identity.Principal) (identity.Actor, error) {
				principals = append(principals, principal)
				if membershipErr != nil {
					return identity.Actor{}, membershipErr
				}
				return identity.Actor{Subject: principal.Subject, TenantID: unitTenant, Roles: []identity.Role{role}}, nil
			}))
			authenticator.tenancyMode = mode
			claims := unitOIDCClaims(now)
			claims["organization_id"], claims["tenant_id"] = "external-org", "untrusted-tenant"
			claims["roles"], claims["groups"] = []string{"tenant_admin"}, []string{"administrators"}
			claims["scope"] = "catalog.read\t sources.read\ncatalog.read"
			token := signOIDCTestToken(t, key, claims)
			for _, currentRole := range []identity.Role{identity.RoleReader, identity.RoleContributor} {
				role = currentRole
				actor, err := authenticator.Authenticate(t.Context(), token)
				if err != nil || actor.Subject != "unit-user" || actor.TenantID != unitTenant || !reflect.DeepEqual(actor.Roles, []identity.Role{role}) {
					t.Fatalf("current membership: %#v, %v", actor, err)
				}
			}
			want := identity.Principal{Issuer: unitIssuer, Subject: "unit-user", Claims: map[string]string{"scope:catalog.read": "true", "scope:sources.read": "true"}}
			if mode == "dedicated" {
				want.TenantID = unitTenant
			} else {
				want.ExternalOrganization = "external-org"
			}
			if !reflect.DeepEqual(principals, []identity.Principal{want, want}) {
				t.Fatalf("principal mapping: %#v", principals)
			}
			membershipErr = errors.New("membership suspended")
			actor, err := authenticator.Authenticate(t.Context(), token)
			if !errors.Is(err, membershipErr) || !reflect.DeepEqual(actor, identity.Actor{}) || len(principals) != 3 {
				t.Fatalf("membership denial: %#v, %v", actor, err)
			}
		})
	}
}

func TestOIDCUnitRejectsClaimsBeforeMembership(t *testing.T) {
	key := unitSigningKey(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	calls := 0
	authenticator := unitOIDC(t, func() time.Time { return now }, unitOIDCDocuments(t, key), resolverFunc(func(context.Context, identity.Principal) (identity.Actor, error) {
		calls++
		return identity.Actor{Subject: "unit-user"}, nil
	}))
	base := unitOIDCClaims(now)
	base["organization_id"] = "external-org"
	authenticator.tenancyMode = "shared"
	if _, err := authenticator.Authenticate(t.Context(), signOIDCTestToken(t, key, base)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, claim string
		value       any
	}{
		{"wrong issuer", "iss", "https://other.invalid"},
		{"wrong audience", "aud", "another-service"},
		{"missing audience", "aud", nil},
		{"expired", "exp", now.Add(-time.Second).Unix()},
		{"missing expiry", "exp", nil},
		{"future not before", "nbf", now.Add(time.Hour).Unix()},
		{"missing subject", "sub", nil},
		{"blank subject", "sub", " "},
		{"overlong subject", "sub", strings.Repeat("x", 513)},
		{"control in subject", "sub", "user\x7f"},
		{"missing issued at", "iat", nil},
		{"future issued at", "iat", now.Add(accessTokenClockSkew + time.Second).Unix()},
		{"missing organization", "organization_id", nil},
		{"organization type", "organization_id", []string{"external-org"}},
		{"organization whitespace", "organization_id", " external-org"},
		{"unrepresentable claim number", "extension", json.Number("1e400")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := cloneAnyMap(base)
			if tc.value == nil {
				delete(claims, tc.claim)
			} else {
				claims[tc.claim] = tc.value
			}
			actor, err := authenticator.Authenticate(t.Context(), signOIDCTestToken(t, key, claims))
			if err == nil || calls != 1 || !reflect.DeepEqual(actor, identity.Actor{}) {
				t.Fatalf("claim rejection: %#v, %v, resolver calls=%d", actor, err, calls)
			}
		})
	}
	// The exact permitted issuance boundary is valid; an optional non-string scope
	// supplies no claims and cannot create role authority.
	claims := cloneAnyMap(base)
	claims["iat"], claims["scope"] = now.Add(accessTokenClockSkew).Unix(), []string{"catalog.admin"}
	authenticator.resolver = resolverFunc(func(_ context.Context, p identity.Principal) (identity.Actor, error) {
		if len(p.Claims) != 0 {
			t.Errorf("non-string scopes granted claims: %v", p.Claims)
		}
		return identity.Actor{Subject: p.Subject}, nil
	})
	if actor, err := authenticator.Authenticate(t.Context(), signOIDCTestToken(t, key, claims)); err != nil || actor.Subject != "unit-user" {
		t.Fatalf("boundary control: %#v, %v", actor, err)
	}
}

func TestOIDCUnitRejectsInvalidTokensBeforeMembership(t *testing.T) {
	key, other := unitSigningKey(t), unitSigningKey(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var requests, clockCalls atomic.Int32
	resolverCalls := 0
	documents := unitOIDCDocuments(t, key)
	wantActor := identity.Actor{Subject: "unit-user", TenantID: unitTenant}
	authenticator := unitOIDC(t, func() time.Time { clockCalls.Add(1); return now }, oidcRoundTripFunc(func(r *http.Request) (*http.Response, error) { requests.Add(1); return documents.RoundTrip(r) }), resolverFunc(func(context.Context, identity.Principal) (identity.Actor, error) {
		resolverCalls++
		return wantActor, nil
	}))
	claims := unitOIDCClaims(now)
	raw := string(mustJSON(t, claims))
	duplicate := strings.TrimSuffix(raw, "}") + `,"sub":"another-user"}`
	valid := signOIDCTestToken(t, key, claims)
	checkValid := func(wantCalls int) {
		t.Helper()
		beforeClock := clockCalls.Load()
		actor, err := authenticator.Authenticate(t.Context(), valid)
		if err != nil || !reflect.DeepEqual(actor, wantActor) || resolverCalls != wantCalls || clockCalls.Load() <= beforeClock {
			t.Fatalf("valid signed-token control: actor=%#v error=%v resolver calls=%d clock calls=%d", actor, err, resolverCalls, clockCalls.Load()-beforeClock)
		}
	}
	checkValid(1) // Warm the actual verifier/key cache before testing early refusal.
	type tokenCase struct {
		name, token string
		local       bool
	}
	refusals := jwtEncodingRefusals()
	cases := make([]tokenCase, 0, 5+len(refusals))
	cases = append(cases, []tokenCase{
		{"missing bearer", "", true},
		{"duplicate claims", signOIDCRawTestToken(t, key, "test-key", "RS256", duplicate), true},
		{"none algorithm", signOIDCRawTestToken(t, key, "test-key", "none", raw), false},
		{"symmetric algorithm", signOIDCRawTestToken(t, key, "test-key", "HS256", raw), false},
		{"untrusted signature", signOIDCTestToken(t, other, claims), false},
	}...)
	for _, refusal := range refusals {
		cases = append(cases, tokenCase{refusal.name, refusal.token, true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := requests.Load()
			beforeClock := clockCalls.Load()
			beforeResolver := resolverCalls
			actor, err := authenticator.Authenticate(t.Context(), tc.token)
			if err == nil || resolverCalls != beforeResolver || !reflect.DeepEqual(actor, identity.Actor{}) {
				t.Fatalf("token rejection: %#v, %v", actor, err)
			}
			if tc.local && (requests.Load() != before || clockCalls.Load() != beforeClock) {
				t.Fatal("encoding rejection entered verification or contacted the issuer")
			}
			if !tc.local && clockCalls.Load() <= beforeClock {
				t.Fatal("signature or algorithm rejection did not enter the verifier")
			}
		})
	}
	before := requests.Load()
	checkValid(2)
	if requests.Load() != before {
		t.Fatal("cached valid-token control unexpectedly contacted the issuer")
	}
}

func TestOIDCUnitCacheBoundariesAndFailedRefresh(t *testing.T) {
	key := unitSigningKey(t)
	epoch := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"TTL boundary", "clock rollback", "discovery outage", "candidate rejects signature"} {
		t.Run(scenario, func(t *testing.T) {
			now := epoch
			documents := unitOIDCDocuments(t, key)
			var metadataCalls, keyCalls atomic.Int32
			var outage, wrongKey atomic.Bool
			emptyKeys := []byte(`{"keys":[]}`)
			authenticator := unitOIDC(t, func() time.Time { return now }, oidcRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/.well-known/openid-configuration" {
					metadataCalls.Add(1)
					if outage.Load() {
						return oidcJSONResponse(r, http.StatusServiceUnavailable, []byte("unavailable")), nil
					}
				} else {
					keyCalls.Add(1)
					if wrongKey.Load() {
						return oidcJSONResponse(r, http.StatusOK, emptyKeys), nil
					}
				}
				return documents.RoundTrip(r)
			}), resolverFunc(func(context.Context, identity.Principal) (identity.Actor, error) {
				return identity.Actor{Subject: "unit-user"}, nil
			}))
			token := signOIDCTestToken(t, key, unitOIDCClaims(epoch))
			if _, err := authenticator.Authenticate(t.Context(), token); err != nil {
				t.Fatal(err)
			}
			original, createdAt := authenticator.verifier, authenticator.verifierCreatedAt
			// Warm keys bridge an outage only strictly inside the cache TTL.
			outage.Store(true)
			now = epoch.Add(oidcKeyCacheTTL - time.Nanosecond)
			if _, err := authenticator.Authenticate(t.Context(), token); err != nil || metadataCalls.Load() != 1 || keyCalls.Load() != 1 {
				t.Fatalf("fresh cache: %v, metadata=%d, keys=%d", err, metadataCalls.Load(), keyCalls.Load())
			}
			outage.Store(scenario == "discovery outage")
			wrongKey.Store(scenario == "candidate rejects signature")
			now = epoch.Add(oidcKeyCacheTTL)
			if scenario == "clock rollback" {
				now = epoch.Add(-time.Nanosecond)
			}
			_, err := authenticator.Authenticate(t.Context(), token)
			failure := scenario == "discovery outage" || scenario == "candidate rejects signature"
			if (err != nil) != failure || metadataCalls.Load() != 2 {
				t.Fatalf("refresh: %v, metadata=%d", err, metadataCalls.Load())
			}
			if failure {
				if authenticator.verifier != original || !authenticator.verifierCreatedAt.Equal(createdAt) {
					t.Fatal("failed refresh installed candidate or renewed stale cache")
				}
				outage.Store(false)
				wrongKey.Store(false)
				if _, err := authenticator.Authenticate(t.Context(), token); err != nil {
					t.Fatalf("refresh recovery: %v", err)
				}
				if metadataCalls.Load() != 3 {
					t.Fatal("recovery reused failed candidate")
				}
			}
			if authenticator.verifier == original || !authenticator.verifierCreatedAt.Equal(now) {
				t.Fatal("successful refresh did not replace expired verifier")
			}
		})
	}
}

func TestOIDCUnitConcurrentExpiredCacheRefresh(t *testing.T) {
	key := unitSigningKey(t)
	epoch := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	const workers = 8
	var metadataCalls, keyCalls, resolverCalls, clockCalls atomic.Int32
	documents := unitOIDCDocuments(t, key)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	now := func() time.Time { return epoch }
	var authenticator *OIDC
	authenticator = unitOIDC(t, func() time.Time { return now() }, oidcRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		// The refresh guard must cover both discovery and candidate key loading.
		if authenticator != nil && authenticator.refreshMu.TryLock() {
			authenticator.refreshMu.Unlock()
			t.Errorf("refresh request without serialization: %s", r.URL.Path)
		}
		if r.URL.Path == "/.well-known/openid-configuration" {
			metadataCalls.Add(1)
		} else {
			keyCalls.Add(1)
		}
		return documents.RoundTrip(r)
	}), resolverFunc(func(context.Context, identity.Principal) (identity.Actor, error) {
		resolverCalls.Add(1)
		return identity.Actor{}, nil
	}))
	token := signOIDCTestToken(t, key, unitOIDCClaims(epoch))
	arrived, release := make(chan struct{}, workers), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	// Every caller samples expiry before any can install a newer verifier.
	// A later snapshot may contain that newer verifier, but its creation time
	// is after the caller's sample, so it must still take the guarded recheck.
	expired := epoch.Add(oidcKeyCacheTTL)
	now = func() time.Time {
		if clockCalls.Add(1) <= workers {
			arrived <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return expired
		}
		return expired.Add(time.Nanosecond)
	}
	var done sync.WaitGroup
	results := make(chan error, workers)
	t.Cleanup(func() { cancel(); unblock(); done.Wait() })
	for range workers {
		done.Go(func() { _, err := authenticator.Authenticate(ctx, token); results <- err })
	}
	for range workers {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("concurrent verification did not sample expiry")
		}
	}
	unblock()
	for range workers {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("concurrent verification did not finish")
		}
	}
	if metadataCalls.Load() != 2 || keyCalls.Load() != 1 || resolverCalls.Load() != workers {
		t.Fatalf("refresh calls: metadata=%d keys=%d membership=%d", metadataCalls.Load(), keyCalls.Load(), resolverCalls.Load())
	}
}

func TestOIDCUnitDiscoveryAndKeyFailures(t *testing.T) {
	key := unitSigningKey(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, path, body string
		status           int
		transportErr     bool
	}{
		{"metadata transport", "/.well-known/openid-configuration", "", 0, true},
		{"metadata outage", "/.well-known/openid-configuration", `unavailable`, 503, false},
		{"metadata issuer mismatch", "/.well-known/openid-configuration", `{"issuer":"https://other.invalid","jwks_uri":"https://issuer.invalid/keys"}`, 200, false},
		{"missing key endpoint", "/.well-known/openid-configuration", `{"issuer":"https://issuer.invalid"}`, 200, false},
		{"empty key endpoint", "/.well-known/openid-configuration", `{"issuer":"https://issuer.invalid","jwks_uri":""}`, 200, false},
		{"duplicate metadata", "/.well-known/openid-configuration", `{"issuer":"https://issuer.invalid","issuer":"https://other.invalid"}`, 200, false},
		{"key outage", "/keys", `unavailable`, 503, false},
		{"duplicate key document", "/keys", `{"keys":[],"keys":[]}`, 200, false},
		{"missing keys", "/keys", `{"keys":[]}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			documents := unitOIDCDocuments(t, key)
			defer failOnOIDCPanic(t)
			client := newOIDCHTTPClient(unitIssuer)
			client.Transport = oidcResponseTransport{requireHTTPS: true, base: oidcRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == tc.path {
					if tc.transportErr {
						return nil, errors.New("issuer unavailable")
					}
					return oidcJSONResponse(r, tc.status, []byte(tc.body)), nil
				}
				return documents.RoundTrip(r)
			})}
			authenticator := &OIDC{issuer: unitIssuer, audience: "catalog", client: client, now: func() time.Time { return now }}
			verifier, err := authenticator.discoverVerifier(t.Context())
			if tc.path != "/keys" {
				if err == nil || verifier != nil {
					t.Fatal("invalid discovery returned a verifier")
				}
				return
			}
			if err != nil || verifier == nil {
				t.Fatalf("create key-response verifier: verifier=%v error=%v", verifier, err)
			}
			if token, err := verifier.Verify(t.Context(), signOIDCTestToken(t, key, unitOIDCClaims(now))); err == nil || token != nil {
				t.Fatal("invalid key response authenticated token")
			}
		})
	}
}

func TestOIDCUnitStartupRejectsBeforeNetwork(t *testing.T) {
	resolver := resolverFunc(func(context.Context, identity.Principal) (identity.Actor, error) {
		t.Error("startup resolved membership")
		return identity.Actor{}, nil
	})
	missing := filepath.Join(t.TempDir(), "private-ca.pem")
	for _, tc := range []struct {
		name string
		auth config.Auth
		want string
	}{
		{"development trust", config.Auth{Mode: "development", TrustBundleFile: missing}, "development authentication cannot configure OIDC private trust"},
		{"OIDC missing trust", config.Auth{Mode: "oidc", Issuer: unitIssuer, TrustBundleFile: missing}, "OIDC trust bundle must be a readable regular file"},
		{"OIDC unsafe discovery", config.Auth{Mode: "oidc", Issuer: "https://user@issuer.invalid"}, "OIDC request target is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Retain the validation error while preventing discovery if its guard breaks.
			defer failOnOIDCPanic(t)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			authenticator, err := New(ctx, tc.auth, resolver)
			if err == nil || authenticator != nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), missing) {
				t.Fatalf("startup rejection: %v, %v", authenticator, err)
			}
		})
	}
}
