//go:build functional

package httpserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type publisherDNSGate struct{ started, release chan struct{} }

type publisherHTTPFixture struct {
	server    *httptest.Server
	clock     *clock.Fake
	path      string
	publisher catalog.Publisher
	lookups   atomic.Int64
	gate      atomic.Pointer[publisherDNSGate]
	txt       atomic.Value
}

func publisherHTTP(t *testing.T, mode string) *publisherHTTPFixture {
	t.Helper()
	f := &publisherHTTPFixture{clock: clock.NewFake(time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))}
	f.txt.Store("not-the-challenge")
	f.server = newConfiguredHTTPServer(t, func(deps *Dependencies) {
		policy := catalog.DefaultPublisherDomainPolicy()
		policy.Mode = mode
		key := sha256.Sum256([]byte("publisher-http-contract"))
		service, err := catalog.NewService(catalog.Options{Repository: catalog.NewMemoryRepository(), Clock: f.clock, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], PublisherDomainPolicy: policy, PublisherTXT: domainHTTPResolver(func(ctx context.Context, _ string) ([]string, error) {
			f.lookups.Add(1)
			if gate := f.gate.Load(); gate != nil {
				close(gate.started)
				select {
				case <-gate.release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			value, ok := f.txt.Load().(string)
			if !ok {
				return nil, fmt.Errorf("fixture TXT value has the wrong type")
			}
			return []string{value}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		deps.RequestTimeout = 10 * time.Second
		deps.Catalog = service
		deps.Now = f.clock.Now
		deps.EnforceOAuthScopes = true
		auth := deps.Authenticator
		deps.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			scopes := "publishers.read publishers.manage catalog.read tenant.admin"
			switch token {
			case "read-only":
				token = "admin"
				scopes = "publishers.read"
			case "delegated-manager":
				token = "reader-two"
				scopes = "publishers.manage"
			case "manage-only":
				token = "admin"
				scopes = "publishers.manage"
			}
			actor, err := auth.Authenticate(ctx, token)
			actor.Claims = map[string]string{}
			for _, scope := range strings.Fields(scopes) {
				actor.Claims["scope:"+scope] = "true"
			}
			return actor, err
		})
	})
	t.Cleanup(f.server.Close)
	created := requestJSON(t, f.server.URL, http.MethodPost, "/api/v1/publishers", "admin", "create-domain-fixture", "", map[string]any{"displayName": "DNS fixture", "domains": []string{"example.test"}})
	requireStatus(t, created, http.StatusCreated)
	decodeResult(t, created, &f.publisher)
	f.path = "/api/v1/publishers/" + f.publisher.ID + "/domains/example.test/verification"
	return f
}

func TestHTTPPublisherVerificationContractAndReplay(t *testing.T) {
	t.Parallel()
	f := publisherHTTP(t, catalog.DomainVerificationOptional)
	body := map[string]any{"reason": "Check owned DNS evidence"}
	status := requestJSON(t, f.server.URL, http.MethodGet, f.path, "admin", "", "", nil)
	requireStatus(t, status, http.StatusOK)
	requireControlSchema(t, "PublisherDomainVerification", status.body)
	if status.header.Get("Cache-Control") != "private, no-store" || status.header.Get("ETag") == "" || strings.Contains(string(status.body), "nonce") {
		t.Fatal("status response is unsafe", string(status.body))
	}
	if string(status.body) != string(requestJSON(t, f.server.URL, http.MethodGet, f.path, "admin", "", "", nil).body) {
		t.Fatal("absent status changed without a mutation")
	}
	for i, tag := range []string{"", "*", `W/` + status.header.Get("ETag"), `"old"`, status.header.Get("ETag") + `, "another"`} {
		requireControlProblem(t, requestJSON(t, f.server.URL, http.MethodPost, f.path+":issue", "admin", fmt.Sprintf("stale-%d", i), tag, body), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	}
	requireStatus(t, requestJSON(t, f.server.URL, http.MethodGet, f.path, "", "", "", nil), http.StatusUnauthorized)
	requireStatus(t, requestJSON(t, f.server.URL, http.MethodGet, f.path, "other-admin", "", "", nil), http.StatusNotFound)
	requireStatus(t, requestJSON(t, f.server.URL, http.MethodGet, f.path+"/challenge", "read-only", "", "", nil), http.StatusForbidden)
	requireStatus(t, requestJSON(t, f.server.URL, http.MethodGet, f.path, "manage-only", "", "", nil), http.StatusForbidden)
	requireStatus(t, requestJSON(t, f.server.URL, http.MethodPost, f.path+":issue", "reader", "reader-issue", status.header.Get("ETag"), body), http.StatusForbidden)
	requireStatus(t, requestJSON(t, f.server.URL, http.MethodPost, f.path+":issue?unexpected=1", "admin", "unexpected-query", status.header.Get("ETag"), body), http.StatusBadRequest)
	for i, input := range []map[string]any{{"reason": "Check", "unknown": true}, {"reason": nil}, {"reason": ""}} {
		requireStatus(t, requestJSON(t, f.server.URL, http.MethodPost, f.path+":issue", "admin", "invalid-body-"+string(rune('a'+i)), status.header.Get("ETag"), input), http.StatusBadRequest)
	}
	if f.lookups.Load() != 0 {
		t.Fatal("admission performed DNS")
	}
	// Management preparation and mutations do not require the companion read scope.
	issued := requestJSON(t, f.server.URL, http.MethodPost, f.path+":issue", "manage-only", "issue", status.header.Get("ETag"), body)
	requireStatus(t, issued, http.StatusOK)
	requireControlSchema(t, "PublisherDomainChallenge", issued.body)
	var challenge catalog.PublisherDomainChallenge
	decodeResult(t, issued, &challenge)
	if challenge.Value == "" || challenge.Name != "_open-agentic-catalog-challenge.example.test." {
		t.Fatal("missing exact DNS instructions")
	}
	status = requestJSON(t, f.server.URL, http.MethodGet, f.path, "read-only", "", "", nil)
	if strings.Contains(string(status.body), challenge.Value) || strings.Contains(string(status.body), `"value"`) {
		t.Fatal("read-only status disclosed the nonce")
	}
	instructions := requestJSON(t, f.server.URL, http.MethodGet, f.path+"/challenge", "manage-only", "", "", nil)
	requireStatus(t, instructions, http.StatusOK)
	requireControlSchema(t, "PublisherDomainChallenge", instructions.body)
	if instructions.header.Get("ETag") != status.header.Get("ETag") {
		t.Fatal("management and status preconditions disagree")
	}
	negative := requestJSON(t, f.server.URL, http.MethodPost, f.path+":check", "manage-only", "negative-check", issued.header.Get("ETag"), body)
	requireStatus(t, negative, http.StatusOK)
	requireControlSchema(t, "PublisherDomainVerification", negative.body)
	var proof catalog.PublisherDomainStatus
	decodeResult(t, negative, &proof)
	if proof.State != "not_verified" || proof.LastCheckOutcome != "mismatch" || f.lookups.Load() != 1 {
		t.Fatal("negative check became proof", proof)
	}
	repeated := requestJSON(t, f.server.URL, http.MethodPost, f.path+":check", "manage-only", "negative-check", issued.header.Get("ETag"), body)
	requireStatus(t, repeated, http.StatusOK)
	if repeated.header.Get("Idempotency-Replayed") != "true" || string(repeated.body) != string(negative.body) || f.lookups.Load() != 1 {
		t.Fatal("retry repeated DNS")
	}
	f.txt.Store(challenge.Value)
	checked := requestJSON(t, f.server.URL, http.MethodPost, f.path+":check", "manage-only", "positive-check", negative.header.Get("ETag"), body)
	requireStatus(t, checked, http.StatusOK)
	decodeResult(t, checked, &proof)
	if proof.State != "verified" || proof.LastCheckOutcome != "matched" || proof.ChallengeState != "consumed" || f.lookups.Load() != 2 {
		t.Fatal("matching check did not seal proof", proof)
	}
	consumed := requestJSON(t, f.server.URL, http.MethodGet, f.path+"/challenge", "admin", "", "", nil)
	challenge = catalog.PublisherDomainChallenge{}
	decodeResult(t, consumed, &challenge)
	if challenge.Value != "" {
		t.Fatal("consumed nonce was disclosed")
	}
	// A new key cannot reuse a consumed challenge, even with its current ETag.
	requireControlProblem(t, requestJSON(t, f.server.URL, http.MethodPost, f.path+":check", "manage-only", "consumed-new-command", checked.header.Get("ETag"), body), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	if f.lookups.Load() != 2 {
		t.Fatal("consumed challenge performed another lookup")
	}
	approved := requestJSON(t, f.server.URL, http.MethodPost, "/api/v1/publishers/"+f.publisher.ID+"/publication-domains/example.test:approve", "admin", "approve", `"rev-1"`, body)
	requireStatus(t, approved, http.StatusOK)
	// New challenges preserve earlier proof. Clearing proof preserves approval.
	renewed := requestJSON(t, f.server.URL, http.MethodPost, f.path+":issue", "admin", "renew", checked.header.Get("ETag"), body)
	requireStatus(t, renewed, http.StatusOK)
	decodeResult(t, renewed, &challenge)
	if challenge.Status.State != "verified" || !challenge.Status.VerifiedAt.Equal(proof.VerifiedAt) {
		t.Fatal("renewal erased proof")
	}
	f.clock.Advance(time.Minute)
	fresh := requestJSON(t, f.server.URL, http.MethodGet, f.path, "admin", "", "", nil)
	if fresh.header.Get("ETag") != renewed.header.Get("ETag") {
		t.Fatal("a moving observation changed the tag")
	}
	// A failed renewal changes the last result, not the earlier proof or its expiry.
	f.txt.Store("not-the-renewed-challenge")
	failedRenewal := requestJSON(t, f.server.URL, http.MethodPost, f.path+":check", "admin", "failed-renewal", fresh.header.Get("ETag"), body)
	requireStatus(t, failedRenewal, http.StatusOK)
	requireControlSchema(t, "PublisherDomainVerification", failedRenewal.body)
	var renewalStatus catalog.PublisherDomainStatus
	decodeResult(t, failedRenewal, &renewalStatus)
	if renewalStatus.State != "verified" || renewalStatus.LastCheckOutcome != "mismatch" || !renewalStatus.VerifiedAt.Equal(proof.VerifiedAt) || !renewalStatus.ExpiresAt.Equal(proof.ExpiresAt) || renewalStatus.LastCheckChallengeID != challenge.ChallengeID || f.lookups.Load() != 3 {
		t.Fatal("failed renewal changed valid proof or lost its outcome", renewalStatus)
	}
	f.txt.Store(challenge.Value)
	renewalReplay := requestJSON(t, f.server.URL, http.MethodPost, f.path+":check", "admin", "failed-renewal", fresh.header.Get("ETag"), body)
	requireStatus(t, renewalReplay, http.StatusOK)
	if string(renewalReplay.body) != string(failedRenewal.body) || renewalReplay.header.Get("Idempotency-Replayed") != "true" || f.lookups.Load() != 3 {
		t.Fatal("replaying failed renewal repeated DNS or changed the result")
	}
	cleared := requestJSON(t, f.server.URL, http.MethodPost, f.path+":clear", "admin", "clear", failedRenewal.header.Get("ETag"), body)
	requireStatus(t, cleared, http.StatusOK)
	requireControlSchema(t, "PublisherDomainVerification", cleared.body)
	readPublisher := requestJSON(t, f.server.URL, http.MethodGet, "/api/v1/publishers/"+f.publisher.ID, "admin", "", "", nil)
	var p catalog.Publisher
	decodeResult(t, readPublisher, &p)
	if len(p.ApprovedPublicationDomains) != 1 {
		t.Fatal("clearing proof revoked publication approval")
	}
	// Historical evidence is replayed unchanged after state replacement and clearing.
	replay := requestJSON(t, f.server.URL, http.MethodPost, f.path+":check", "manage-only", "positive-check", negative.header.Get("ETag"), body)
	requireStatus(t, replay, http.StatusOK)
	if string(replay.body) != string(checked.body) || replay.header.Get("Idempotency-Replayed") != "true" || f.lookups.Load() != 3 {
		t.Fatal("clear rewrote historical proof")
	}
	current := requestJSON(t, f.server.URL, http.MethodGet, f.path, "admin", "", "", nil)
	decodeResult(t, current, &proof)
	if proof.State != "not_verified" || proof.ChallengeState != "not_issued" {
		t.Fatal("readback used historical proof")
	}
}

func TestHTTPPublisherVerificationDisabledAndFreshness(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{catalog.DomainVerificationDisabled, catalog.DomainVerificationRequired} {
		t.Run(mode, func(t *testing.T) {
			f := publisherHTTP(t, mode)
			status := requestJSON(t, f.server.URL, http.MethodGet, f.path, "admin", "", "", nil)
			requireStatus(t, status, http.StatusOK)
			caps := requestJSON(t, f.server.URL, http.MethodGet, "/api/v1/capabilities", "admin", "", "", nil)
			requireControlSchema(t, "Capabilities", caps.body)
			if !strings.Contains(string(caps.body), `"publisherDomainVerification":"`+mode+`"`) {
				t.Fatal("capabilities omitted effective mode")
			}
			body := map[string]any{"reason": "Qualify capability and expiry"}
			if mode == catalog.DomainVerificationDisabled {
				requireStatus(t, requestJSON(t, f.server.URL, http.MethodGet, f.path+"/challenge", "admin", "", "", nil), http.StatusNotFound)
				for _, action := range []string{"issue", "check", "clear"} {
					requireStatus(t, requestJSON(t, f.server.URL, http.MethodPost, f.path+":"+action, "admin", action, status.header.Get("ETag"), body), http.StatusNotFound)
				}
				if f.lookups.Load() != 0 {
					t.Fatal("disabled DNS was queried")
				}
				return
			}
			issued := requestJSON(t, f.server.URL, http.MethodPost, f.path+":issue", "admin", "issue", status.header.Get("ETag"), body)
			requireStatus(t, issued, http.StatusOK)
			var challenge catalog.PublisherDomainChallenge
			decodeResult(t, issued, &challenge)
			f.clock.Advance(challenge.ExpiresAt.Sub(f.clock.Now()))
			expired := requestJSON(t, f.server.URL, http.MethodGet, f.path, "admin", "", "", nil)
			requireStatus(t, expired, http.StatusOK)
			if expired.header.Get("ETag") == issued.header.Get("ETag") {
				t.Fatal("expiry reused a strong tag")
			}
			requireStatus(t, requestJSON(t, f.server.URL, http.MethodPost, f.path+":check", "admin", "stale-expired", issued.header.Get("ETag"), body), http.StatusPreconditionFailed)
			requireStatus(t, requestJSON(t, f.server.URL, http.MethodPost, f.path+":check", "admin", "expired", expired.header.Get("ETag"), body), http.StatusPreconditionFailed)
			replay := requestJSON(t, f.server.URL, http.MethodPost, f.path+":issue", "admin", "issue", status.header.Get("ETag"), body)
			requireStatus(t, replay, http.StatusOK)
			if string(replay.body) != string(issued.body) || f.lookups.Load() != 0 {
				t.Fatal("expiry caused replay to issue or query again")
			}
		})
	}
}

func TestHTTPPublisherVerificationReplayAfterDomainRemoval(t *testing.T) {
	t.Parallel()
	f := publisherHTTP(t, catalog.DomainVerificationOptional)
	body := map[string]any{"reason": "Qualify retained commands"}
	absent := requestJSON(t, f.server.URL, "GET", f.path, "admin", "", "", nil)
	issued := requestJSON(t, f.server.URL, "POST", f.path+":issue", "admin", "issue", absent.header.Get("ETag"), body)
	requireStatus(t, issued, 200)
	var challenge catalog.PublisherDomainChallenge
	decodeResult(t, issued, &challenge)
	f.txt.Store(challenge.Value)
	checked := requestJSON(t, f.server.URL, "POST", f.path+":check", "admin", "check", issued.header.Get("ETag"), body)
	requireStatus(t, checked, 200)
	path := "/api/v1/publishers/" + f.publisher.ID
	removed := requestJSON(t, f.server.URL, "PATCH", path, "admin", "remove", `"rev-1"`, map[string]any{"displayName": "DNS fixture", "domains": []string{"other.test"}, "reason": "Remove verified domain"})
	requireStatus(t, removed, 200)
	requireStatus(t, requestJSON(t, f.server.URL, "GET", f.path, "admin", "", "", nil), 404)
	for _, old := range []struct {
		action, key, tag string
		body             []byte
	}{{"issue", "issue", absent.header.Get("ETag"), issued.body}, {"check", "check", issued.header.Get("ETag"), checked.body}} {
		replay := requestJSON(t, f.server.URL, "POST", f.path+":"+old.action, "admin", old.key, old.tag, body)
		requireStatus(t, replay, 200)
		if replay.header.Get("Idempotency-Replayed") != "true" || string(replay.body) != string(old.body) || !strings.Contains(strings.Join(replay.header.Values("Cache-Control"), ","), "private") {
			t.Fatal("removal altered historical replay")
		}
	}
	readded := requestJSON(t, f.server.URL, "PATCH", path, "admin", "readd", removed.header.Get("ETag"), map[string]any{"displayName": "DNS fixture", "domains": []string{"example.test"}, "reason": "Readd domain without historical proof"})
	requireStatus(t, readded, 200)
	newStatus := requestJSON(t, f.server.URL, "GET", f.path, "admin", "", "", nil)
	requireStatus(t, newStatus, 200)
	var status catalog.PublisherDomainStatus
	decodeResult(t, newStatus, &status)
	if status.State != "not_verified" || newStatus.header.Get("ETag") == absent.header.Get("ETag") || f.lookups.Load() != 1 {
		t.Fatal("readded domain reused old state or preconditions")
	}
	requireStatus(t, requestJSON(t, f.server.URL, "POST", f.path+":issue", "admin", "stale-absent", absent.header.Get("ETag"), body), 412)
}

func TestHTTPPublisherVerificationRechecksCurrentAuthorityAfterDNS(t *testing.T) {
	t.Parallel()
	f := publisherHTTP(t, catalog.DomainVerificationOptional)
	memberResult := requestJSON(t, f.server.URL, "GET", "/api/v1/me", "reader-two", "", "", nil)
	requireStatus(t, memberResult, 200)
	var member struct {
		ID string `json:"membershipId"`
	}
	decodeResult(t, memberResult, &member)
	roleReply := requestJSON(t, f.server.URL, "POST", "/api/v1/access/roles", "admin", "manager-role", "", map[string]any{"name": "DNS manager only", "permissions": []string{"publisher.read", "publisher.manage"}, "reason": "Qualify independent command permission"})
	requireStatus(t, roleReply, 201)
	var role identity.RoleDefinition
	decodeResult(t, roleReply, &role)
	grantReply := requestJSON(t, f.server.URL, "POST", "/api/v1/access/assignments", "admin", "manager-grant", "", map[string]any{"roleDefinitionId": role.ID, "membershipId": member.ID, "reason": "Permit DNS management"})
	requireStatus(t, grantReply, 201)
	var grant identity.AccessAssignment
	decodeResult(t, grantReply, &grant)
	// The custom role preserves required companion permissions. Its token has only the management OAuth scope.
	prepared := requestJSON(t, f.server.URL, "GET", f.path+"/challenge", "delegated-manager", "", "", nil)
	requireStatus(t, prepared, 200)
	requireStatus(t, requestJSON(t, f.server.URL, "GET", f.path, "delegated-manager", "", "", nil), 403)
	body := map[string]any{"reason": "Check delegated domain control"}
	issued := requestJSON(t, f.server.URL, "POST", f.path+":issue", "delegated-manager", "issue", prepared.header.Get("ETag"), body)
	requireStatus(t, issued, 200)
	var challenge catalog.PublisherDomainChallenge
	decodeResult(t, issued, &challenge)
	f.txt.Store(challenge.Value)
	gate := &publisherDNSGate{started: make(chan struct{}), release: make(chan struct{})}
	f.gate.Store(gate)
	released := false
	t.Cleanup(func() {
		if !released {
			close(gate.release)
		}
	})
	done := make(chan httpResult, 1)
	go func() {
		done <- requestJSON(t, f.server.URL, "POST", f.path+":check", "delegated-manager", "check", issued.header.Get("ETag"), body)
	}()
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("check never entered DNS")
	}
	revoked := requestJSON(t, f.server.URL, "POST", "/api/v1/access/assignments/"+grant.ID+":revoke", "admin", "revoke-during-DNS", grantReply.header.Get("ETag"), map[string]any{"reason": "Revoke while lookup is pending"})
	requireStatus(t, revoked, 200)
	close(gate.release)
	released = true
	select {
	case result := <-done:
		requireStatus(t, result, 403)
	case <-time.After(5 * time.Second):
		t.Fatal("check did not finish")
	}
	statusReply := requestJSON(t, f.server.URL, "GET", f.path, "admin", "", "", nil)
	var status catalog.PublisherDomainStatus
	decodeResult(t, statusReply, &status)
	if status.State != "not_verified" || status.LastCheckOutcome != "" || f.lookups.Load() != 1 {
		t.Fatal("revoked manager committed evidence")
	}
	requireStatus(t, requestJSON(t, f.server.URL, "POST", f.path+":issue", "delegated-manager", "issue", prepared.header.Get("ETag"), body), 403)
}

func TestHTTPPublisherVerificationApproverDoesNotManageDNS(t *testing.T) {
	t.Parallel()
	f := publisherHTTP(t, catalog.DomainVerificationOptional)
	memberResult := requestJSON(t, f.server.URL, "GET", "/api/v1/me", "reader-two", "", "", nil)
	requireStatus(t, memberResult, 200)
	var member struct {
		ID string `json:"membershipId"`
	}
	decodeResult(t, memberResult, &member)
	roleReply := requestJSON(t, f.server.URL, "POST", "/api/v1/access/roles", "admin", "approver-role", "", map[string]any{"name": "Domain approver only", "permissions": []string{"publisher.read", "publisher.approve_domain"}, "reason": "Separate approval from DNS"})
	requireStatus(t, roleReply, 201)
	var role identity.RoleDefinition
	decodeResult(t, roleReply, &role)
	grantReply := requestJSON(t, f.server.URL, "POST", "/api/v1/access/assignments", "admin", "approver-grant", "", map[string]any{"roleDefinitionId": role.ID, "membershipId": member.ID, "reason": "Delegate only approval"})
	requireStatus(t, grantReply, 201)
	status := requestJSON(t, f.server.URL, "GET", f.path, "reader-two", "", "", nil)
	requireStatus(t, status, 200)
	requireStatus(t, requestJSON(t, f.server.URL, "GET", f.path+"/challenge", "reader-two", "", "", nil), 403)
	for _, action := range []string{"issue", "check", "clear"} {
		requireStatus(t, requestJSON(t, f.server.URL, "POST", f.path+":"+action, "reader-two", action, status.header.Get("ETag"), map[string]any{"reason": "Refuse DNS management"}), 403)
	}
	pub := requestJSON(t, f.server.URL, "GET", "/api/v1/publishers/"+f.publisher.ID, "reader-two", "", "", nil)
	requireStatus(t, pub, 200)
	requireStatus(t, requestJSON(t, f.server.URL, "POST", "/api/v1/publishers/"+f.publisher.ID+"/publication-domains/example.test:approve", "reader-two", "approve-only", pub.header.Get("ETag"), map[string]any{"reason": "Approve without DNS management"}), 200)
	if f.lookups.Load() != 0 {
		t.Fatal("approval-only workflow queried DNS")
	}
}
