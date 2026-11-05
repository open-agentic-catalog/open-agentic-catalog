package authn

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestOIDCDependencyFailureSurvivesVerifierAndRemainsCallerOwned(t *testing.T) {
	key := unitSigningKey(t)
	for _, failure := range []string{"http", "transport", "json", "unknown-key"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				defer failOnOIDCPanic(t)
				now := time.Now()
				documents := unitOIDCDocuments(t, key)
				var outage atomic.Bool
				outage.Store(true)
				var memberships atomic.Int32
				auth := unitOIDC(t, func() time.Time { return now }, oidcRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/keys" && outage.Load() {
						switch failure {
						case "http":
							return oidcJSONResponse(r, 503, []byte("PRIVATE_ISSUER_ERROR")), nil
						case "transport", "unknown-key":
							return nil, errors.New("PRIVATE_NETWORK_ERROR")
						default:
							return oidcJSONResponse(r, 200, []byte(`{"keys":[],"keys":[]}`)), nil
						}
					}
					return documents.RoundTrip(r)
				}), resolverFunc(func(context.Context, identity.Principal) (identity.Actor, error) {
					memberships.Add(1)
					return identity.Actor{Subject: "member", TenantID: unitTenant}, nil
				}))
				token := signOIDCTestToken(t, key, unitOIDCClaims(now))
				if failure == "unknown-key" {
					token = signOIDCTestTokenWithKeyID(t, key, "owned-unknown-key", unitOIDCClaims(now))
				}
				if _, err := auth.Authenticate(t.Context(), token); identity.ErrorCodeOf(err) != identity.CodeDependency || memberships.Load() != 0 {
					t.Fatalf("outage classification: %v memberships=%d", err, memberships.Load())
				}
				// RemoteKeySet signals its result before clearing the shared
				// in-flight request. Finish that task before ending this outage.
				synctest.Wait()
				outage.Store(false)
				token = signOIDCTestToken(t, key, unitOIDCClaims(now))
				if _, err := auth.Authenticate(t.Context(), token); err != nil || memberships.Load() != 1 {
					t.Fatalf("recovery: %v", err)
				}
				if failure == "unknown-key" {
					unknown := signOIDCTestTokenWithKeyID(t, key, "owned-unknown-key", unitOIDCClaims(now))
					if _, err := auth.Authenticate(t.Context(), unknown); err == nil || identity.ErrorCodeOf(err) == identity.CodeDependency || memberships.Load() != 1 {
						t.Fatalf("unknown key after recovery: %v memberships=%d", err, memberships.Load())
					}
				}
				wrongKey := unitSigningKey(t)
				if _, err := auth.Authenticate(t.Context(), signOIDCTestToken(t, wrongKey, unitOIDCClaims(now))); err == nil || identity.ErrorCodeOf(err) == identity.CodeDependency || memberships.Load() != 1 {
					t.Fatalf("invalid signature classified as outage: %v", err)
				}
			})
		})
	}
}
