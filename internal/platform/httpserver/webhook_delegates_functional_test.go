//go:build functional

// Exact webhook delegates exercise real HTTP admission, current replay and
// sender side effects with retained human and service memberships.
package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

// TestHTTPWebhookExactDelegatesAndScopeCeilings proves permissions independently:
// a token ceiling cannot supply a grant, and a grant cannot bypass its ceiling.
func TestHTTPWebhookExactDelegatesAndScopeCeilings(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	actions := []string{"read", "manage", "test", "rotate", "read_evidence"}
	for _, kind := range []identity.ActorType{identity.ActorHuman, identity.ActorService} {
		t.Run(string(kind), func(t *testing.T) {
			var sends atomic.Int32
			sender := webhookProjectionSender(func(context.Context, webhook.SendRequest) (webhook.SendResult, error) {
				sends.Add(1)
				return webhook.SendResult{Status: http.StatusNoContent}, nil
			})
			server := newConfiguredHTTPServer(t, func(d *Dependencies) {
				users := map[string]config.DevelopmentUser{
					"admin":       {Subject: "admin", TenantID: tenant, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}},
					"other-admin": {Subject: "other-admin", TenantID: "00000000-0000-7000-8000-000000000002", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}},
				}
				for _, action := range actions {
					users[action] = config.DevelopmentUser{Subject: action, TenantID: tenant, ActorType: kind}
				}
				seeds := make([]identity.Seed, 0, len(users))
				for _, user := range users {
					seeds = append(seeds, identity.Seed{TenantID: user.TenantID, Issuer: authn.DevelopmentIssuer, Subject: user.Subject, ActorType: user.ActorType, Roles: user.Roles})
				}
				store, err := identity.NewMemoryStore(seeds)
				if err != nil {
					t.Fatal(err)
				}
				cursor := sha256.Sum256([]byte("webhook-delegates"))
				service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(d.Now()), IDs: id.Generator{}, CursorKey: cursor[:], AcknowledgeOutboxAtCreation: true})
				if err != nil {
					t.Fatal(err)
				}
				authenticator, err := authn.New(t.Context(), config.Auth{Mode: "development", DevelopmentTokens: users}, store)
				if err != nil {
					t.Fatal(err)
				}
				d.Identity = service
				d.EnforceOAuthScopes = true
				d.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
					scopes := map[string]string{"scope:webhooks.manage": "true", "scope:tenant.admin": "true"}
					if strings.HasSuffix(token, "-no-scope") {
						token = strings.TrimSuffix(token, "-no-scope")
						scopes = nil
					}
					actor, err := authenticator.Authenticate(ctx, token)
					actor.Claims = scopes
					return actor, err
				})
			}, sender)
			defer server.Close()
			created := requestJSON(t, server.URL, http.MethodPost, "/api/v1/webhooks", "admin", "delegate-endpoint", "", map[string]any{"name": "Delegated receiver", "url": "https://receiver.example/events", "eventTypes": []string{"namespace.created"}})
			requireWebhookDelegateResponse(t, created, http.StatusCreated, "")
			var endpoint webhook.CreateResult
			decodeWebhookDelegateResponse(t, created, &endpoint)
			path := "/api/v1/webhooks/" + endpoint.Endpoint.ID
			invoke := func(action, token, key, etag string) httpResult {
				method, target := http.MethodGet, path
				var body any
				switch action {
				case "manage":
					method, body = http.MethodPatch, map[string]any{"name": "Updated delegate receiver"}
				case "test":
					method, target, body = http.MethodPost, path+":test", map[string]any{}
				case "rotate":
					method, target, body = http.MethodPost, path+":rotate-secret", map[string]any{"revokePrevious": true}
				case "read_evidence":
					target += "/deliveries?limit=1"
				}
				return requestJSON(t, server.URL, method, target, token, key, etag, body)
			}
			initialETag := created.header.Get("ETag")
			if initialETag == "" {
				t.Fatal("endpoint creation omitted its current validator")
			}
			var testAssignment identity.AccessAssignment
			var assignmentETag string
			for _, action := range actions {
				self := requestJSON(t, server.URL, http.MethodGet, "/api/v1/me", action, "", "", nil)
				requireWebhookDelegateResponse(t, self, http.StatusOK, "")
				var member selfResponse
				decodeWebhookDelegateResponse(t, self, &member)
				if member.ActorType != kind || len(member.Permissions) != 0 {
					t.Fatal("delegate did not start with the exact grant-free identity")
				}
				for _, operation := range actions {
					requireWebhookDelegateResponse(t, invoke(operation, action, "no-grant-"+action+"-"+operation, initialETag), http.StatusForbidden, catalog.CodeForbidden)
				}
				permissions := []string{"webhook.read"}
				if action != "read" {
					permissions = append(permissions, "webhook."+action)
				}
				roleResponse := requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/roles", "admin", "role-"+action, "", map[string]any{"name": "Webhook " + action, "permissions": permissions, "reason": "Qualify exact delegation"})
				requireWebhookDelegateResponse(t, roleResponse, http.StatusCreated, "")
				var role identity.RoleDefinition
				decodeWebhookDelegateResponse(t, roleResponse, &role)
				grant := requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/assignments", "admin", "grant-"+action, "", map[string]any{"roleDefinitionId": role.ID, "membershipId": member.MembershipID, "reason": "Qualify exact delegation"})
				requireWebhookDelegateResponse(t, grant, http.StatusCreated, "")
				if action == "test" {
					decodeWebhookDelegateResponse(t, grant, &testAssignment)
					assignmentETag = grant.header.Get("ETag")
				}
			}
			unmodified := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
			requireWebhookDelegateResponse(t, unmodified, http.StatusOK, "")
			var initialState webhook.Endpoint
			decodeWebhookDelegateResponse(t, unmodified, &initialState)
			if initialState.Revision != 1 || initialState.SecretVersion != 1 || initialState.Name != "Delegated receiver" || unmodified.header.Get("ETag") != initialETag || sends.Load() != 0 {
				t.Fatal("scope-only callers changed the endpoint, signer or receiver")
			}
			for _, action := range actions {
				current := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
				requireWebhookDelegateResponse(t, current, http.StatusOK, "")
				etag := current.header.Get("ETag")
				before := sends.Load()
				positive := invoke(action, action, "positive-"+action, etag)
				requireWebhookDelegateResponse(t, positive, http.StatusOK, "")
				// Denials vary authority alone, even after a positive control changes
				// the endpoint revision. A stale validator must not mask that guard.
				if action == "manage" || action == "rotate" {
					etag = positive.header.Get("ETag")
					if etag == "" {
						t.Fatal("successful configuration change omitted its current validator")
					}
				}
				delta := int32(0)
				if action == "test" {
					delta = 1
				}
				if sends.Load() != before+delta {
					t.Fatal("an endpoint operation made an unexpected send")
				}
				missingScope := invoke(action, action+"-no-scope", "no-scope-"+action, etag)
				requireWebhookDelegateResponse(t, missingScope, http.StatusForbidden, catalog.CodeForbidden)
				if missingScope.header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="webhooks.manage"` {
					t.Fatal("scope refusal did not explain its independent ceiling")
				}
				for _, other := range actions {
					if other != action && action != "read" {
						requireWebhookDelegateResponse(t, invoke(action, other, "denied-"+action+"-"+other, etag), http.StatusForbidden, catalog.CodeForbidden)
					}
				}
				if sends.Load() != before+delta {
					t.Fatal("a refused operation reached the sender")
				}
			}
			current := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
			requireWebhookDelegateResponse(t, current, http.StatusOK, "")
			etag := current.header.Get("ETag")
			for i, test := range []struct {
				token, etag string
				status      int
				code        catalog.ErrorCode
			}{
				{"", etag, http.StatusUnauthorized, catalog.CodeUnauthenticated},
				{"other-admin", etag, http.StatusNotFound, catalog.CodeNotFound},
				{"test", `"rev-999"`, http.StatusPreconditionFailed, catalog.CodePreconditionFailed},
			} {
				requireWebhookDelegateResponse(t, invoke("test", test.token, fmt.Sprintf("no-send-%d", i), test.etag), test.status, test.code)
			}
			success := invoke("test", "test", "current-replay", etag)
			requireWebhookDelegateResponse(t, success, http.StatusOK, "")
			replay := invoke("test", "test", "current-replay", etag)
			requireWebhookDelegateResponse(t, replay, http.StatusOK, "")
			if string(success.body) != string(replay.body) || sends.Load() != 2 {
				t.Fatal("test replay repeated the send or changed its receipt")
			}
			var accepted webhook.Delivery
			decodeWebhookDelegateResponse(t, success, &accepted)
			if accepted.ID == "" {
				t.Fatal("accepted test omitted its delivery reference")
			}
			noScopeReplay := invoke("test", "test-no-scope", "current-replay", etag)
			requireWebhookDelegateResponse(t, noScopeReplay, http.StatusForbidden, catalog.CodeForbidden)
			if noScopeReplay.header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="webhooks.manage"` || noScopeReplay.header.Get("ETag") != "" || noScopeReplay.header.Get("Idempotency-Replayed") != "" || strings.Contains(string(noScopeReplay.body), accepted.ID) || sends.Load() != 2 {
				t.Fatal("scope loss disclosed or executed the retained command outcome")
			}
			restoredScopeReplay := invoke("test", "test", "current-replay", etag)
			requireWebhookDelegateResponse(t, restoredScopeReplay, http.StatusOK, "")
			if restoredScopeReplay.header.Get("Idempotency-Replayed") != "true" || string(restoredScopeReplay.body) != string(success.body) || sends.Load() != 2 {
				t.Fatal("restored OAuth scope failed to replay the original outcome without another send")
			}
			revoked := requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/assignments/"+testAssignment.ID+":revoke", "admin", "revoke-test", assignmentETag, map[string]any{"reason": "Remove delegated send authority"})
			requireWebhookDelegateResponse(t, revoked, http.StatusOK, "")
			requireWebhookDelegateResponse(t, invoke("test", "test", "current-replay", etag), http.StatusForbidden, catalog.CodeForbidden)
			if sends.Load() != 2 {
				t.Fatal("revoked replay or refused command reached the sender")
			}
		})
	}
}

// requireWebhookDelegateResponse never prints a response body: an incorrect
// authorization result could otherwise disclose a newly returned signing key.
func requireWebhookDelegateResponse(t *testing.T, result httpResult, status int, code catalog.ErrorCode) {
	t.Helper()
	if result.status != status {
		t.Fatalf("webhook status = %d, want %d", result.status, status)
	}
	if code != "" {
		var problem struct {
			Code catalog.ErrorCode `json:"code"`
		}
		if json.Unmarshal(result.body, &problem) != nil || problem.Code != code {
			t.Fatal("webhook refusal omitted the expected problem code")
		}
	}
}

// decodeWebhookDelegateResponse reports shape failures without private bytes.
func decodeWebhookDelegateResponse(t *testing.T, result httpResult, target any) {
	t.Helper()
	if json.Unmarshal(result.body, target) != nil {
		t.Fatal("invalid webhook delegate response")
	}
}
