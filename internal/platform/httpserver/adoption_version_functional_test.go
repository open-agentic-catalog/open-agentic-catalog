//go:build functional

package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestHTTPVersionAdoptionAndExactRecovery(t *testing.T) {
	const endpoint = "https://upstream.example/ard.json"
	const identifier = "urn:air:upstream.example:agents:versioned"
	const sourceID = "00000000-0000-7000-8000-000000007901"
	serviceClock := clock.NewFake(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	responses := routedFetcher{}
	setVersion := func(label string) {
		body := `{"entries":[]}`
		if label != "" {
			body = fmt.Sprintf(`{"entries":[{"identifier":%q,"version":%q,"displayName":"Upstream Agent","description":"Retained upstream definition","type":"application/a2a-agent-card+json","data":{"name":"Upstream Agent","version":%q}}]}`, identifier, label, label)
		}
		responses["GET "+endpoint] = federationHTTPResult(endpoint, "application/ard+json", body)
	}
	setVersion("1.0")
	store := federation.NewMemoryStore()
	var manager *federation.Manager
	var deps Dependencies
	testContext := t.Context()
	var principal identity.Actor
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		deps = *d
		actor, err := d.Authenticator.Authenticate(testContext, "admin")
		if err != nil {
			t.Fatal(err)
		}
		principal = actor
		actorContext := identity.WithActor(testContext, actor)
		manager, err = federation.NewManager(store, responses, serviceClock, id.Generator{}, federation.WithObservationAdopter(d.Catalog), federation.WithVersionAdoption(true))
		if err != nil {
			t.Fatal(err)
		}
		d.Federation = manager
		deps.Federation = manager
		_, err = store.CreateSource(actorContext, federation.Source{ID: sourceID, TenantID: actor.TenantID, DisplayName: "Upstream", Endpoint: endpoint, Type: federation.SourceARDManifest, Status: federation.SourceActive, ProfileID: "ard-0.91-aa3e598", Revision: 1, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: responses.PolicyDigest(), CreatedAt: serviceClock.Now(), UpdatedAt: serviceClock.Now()})
		if err != nil {
			t.Fatal(err)
		}
	})
	defer server.Close()
	ctx := identity.WithActor(testContext, principal)
	nsReply := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "adopt-version-ns", "", map[string]any{"slug": "upstream", "displayName": "Upstream"})
	requireStatus(t, nsReply, 201)
	var ns catalog.Namespace
	decodeResult(t, nsReply, &ns)
	pubReply := requestJSON(t, server.URL, "POST", "/api/v1/publishers", "admin", "adopt-version-publisher", "", map[string]any{"displayName": "Local publisher", "domains": []string{"example.com"}})
	requireStatus(t, pubReply, 201)
	var pub catalog.Publisher
	decodeResult(t, pubReply, &pub)
	selection := func() federation.Observation {
		serviceClock.Advance(time.Second)
		if _, err := manager.Sync(ctx, sourceID); err != nil {
			t.Fatal(err)
		}
		items, _, err := store.ListCurrentObservations(ctx, principal.TenantID, sourceID, 10)
		if err != nil || len(items) != 1 {
			t.Fatal("expected one current observation", items, err)
		}
		return items[0]
	}
	old := selection()
	occurrences := func(obs federation.Observation) federation.ObservationOccurrence {
		r := requestJSON(t, server.URL, "GET", "/api/v1/observations/"+obs.ID+"/occurrences", "admin", "", "", nil)
		requireStatus(t, r, 200)
		var page cursorPage[federation.ObservationOccurrence]
		decodeResult(t, r, &page)
		if len(page.Items) != 1 {
			t.Fatal("wrong occurrences")
		}
		return page.Items[0]
	}
	firstOccurrence := occurrences(old)
	initial := requestJSON(t, server.URL, "POST", "/api/v1/observations/"+old.ID+":adopt", "admin", "first-upstream-adoption", "", map[string]any{"snapshotId": firstOccurrence.SnapshotID, "retrievalDigest": firstOccurrence.RetrievalDigest, "entryLocation": firstOccurrence.EntryLocation, "namespaceId": ns.ID, "publisherId": pub.ID, "displayName": "Local customized title", "description": "Locally maintained description"})
	requireStatus(t, initial, 201)
	var first catalog.ExternalObservationAdoption
	decodeResult(t, initial, &first)
	setVersion("2.0")
	next := selection()
	occ := occurrences(next)
	targetReply := requestJSON(t, server.URL, "GET", "/api/v1/observations/"+next.ID+"/adoption-target", "admin", "", "", nil)
	requireStatus(t, targetReply, 200)
	requireControlSchema(t, "VersionAdoptionTarget", targetReply.body)
	var target catalog.AdoptionTarget
	decodeResult(t, targetReply, &target)
	if target.ResourceID != first.Resource.ID || target.PriorVersionRevisionID != first.VersionRevision.ID {
		t.Fatal("lost historical same-source target", target)
	}
	body := map[string]any{"observationId": next.ID, "snapshotId": occ.SnapshotID, "retrievalDigest": occ.RetrievalDigest, "entryLocation": occ.EntryLocation, "priorVersionRevisionId": target.PriorVersionRevisionID}
	path := "/api/v1/resources/" + target.ResourceID + "/versions:adopt"
	for _, token := range []string{"reader", "contributor", "other-admin"} {
		denied := requestJSON(t, server.URL, "POST", path, token, "denied-"+token, "", body)
		if denied.status != 403 && denied.status != 404 {
			t.Fatal("unauthorized append", denied.status)
		}
	}
	created := requestJSON(t, server.URL, "POST", path, "admin", "append-upstream-version", "", body)
	requireStatus(t, created, 201)
	requireControlSchema(t, "ResourceVersionAdoption", created.body)
	var result catalog.ExternalVersionAdoption
	decodeResult(t, created, &result)
	if result.ResourceID != first.Resource.ID || result.Version.Version != "2.0" || result.VersionRevision.Adoption == nil || result.VersionRevision.Adoption.ObservationID != next.ID {
		t.Fatal("wrong new draft", result)
	}

	if len(result.Evidence.Findings) == 0 || result.VersionRevision.Representations[0].ValidationState != "invalid" {
		t.Fatal("native errors were not retained on the draft")
	}
	listing := requestJSON(t, server.URL, "GET", "/api/v1/listing-revisions/"+first.ListingRevision.ID, "admin", "", "", nil)
	requireStatus(t, listing, 200)
	var retained catalog.ListingRevision
	decodeResult(t, listing, &retained)
	if retained.DisplayName != "Local customized title" || retained.VersionRevisionID != first.VersionRevision.ID {
		t.Fatal("Listing was overwritten")
	}
	requireControlProblem(t, requestJSON(t, server.URL, "POST", path, "admin", "different-key-duplicate", "", body), 409, catalog.CodeConflict)
	changed := map[string]any{}
	for k, v := range body {
		changed[k] = v
	}
	changed["entryLocation"] = "/entries/9"
	requireControlProblem(t, requestJSON(t, server.URL, "POST", path, "admin", "append-upstream-version", "", changed), 409, catalog.CodeIdempotencyKeyReused)

	// Both scopes apply to new commands and retained-response recovery. These
	// aliases deliberately resolve to the original principal, not another key owner.
	scoped := deps
	scoped.EnforceOAuthScopes = true
	auth := deps.Authenticator
	scoped.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
		actor, err := auth.Authenticate(ctx, "admin")
		actor.Claims = map[string]string{}
		if token != "no-write" {
			actor.Claims["scope:catalog.write"] = "true"
		}
		if token != "no-source" {
			actor.Claims["scope:sources.read"] = "true"
		}
		return actor, err
	})
	scopedServer, err := New(scoped)
	if err != nil {
		t.Fatal(err)
	}
	scopesHTTP := httptest.NewServer(scopedServer.Handler())
	defer scopesHTTP.Close()
	for _, token := range []string{"no-write", "no-source"} {
		requireControlProblem(t, requestJSON(t, scopesHTTP.URL, "POST", path, token, "append-upstream-version", "", body), 403, catalog.CodeForbidden)
		requireControlProblem(t, requestJSON(t, scopesHTTP.URL, "GET", "/api/v1/observations/"+next.ID+"/adoption-target", token, "", "", nil), 403, catalog.CodeForbidden)
		requireStatus(t, requestJSON(t, scopesHTTP.URL, "HEAD", "/api/v1/observations/"+next.ID+"/adoption-target", token, "", "", nil), http.StatusForbidden)
	}
	head := requestJSON(t, scopesHTTP.URL, "HEAD", "/api/v1/observations/"+next.ID+"/adoption-target", "both-scopes", "", "", nil)
	requireStatus(t, head, http.StatusOK)
	if len(head.body) != 0 {
		t.Fatal("HEAD lookup returned a response body")
	}
	requireStatus(t, requestJSON(t, scopesHTTP.URL, "POST", path, "both-scopes", "append-upstream-version", "", body), 201)
	for _, field := range []string{"snapshotId", "observationId", "priorVersionRevisionId", "retrievalDigest", "entryLocation"} {
		malformed := map[string]any{}
		for k, v := range body {
			malformed[k] = v
		}
		malformed[field] = "malformed"
		requireControlProblem(t, requestJSON(t, server.URL, "POST", path, "admin", "invalid-"+field, "", malformed), 400, catalog.CodeInvalidArgument)
	}

	// A delegate can edit this Resource without any Resource-create grant. Loss
	// of either independent permission must also deny its own retained receipt.
	self := requestJSON(t, server.URL, "GET", "/api/v1/me", "reader-two", "", "", nil)
	requireStatus(t, self, 200)
	var member struct {
		MembershipID string `json:"membershipId"`
	}
	decodeResult(t, self, &member)
	type grantedRole struct {
		id, etag string
		body     map[string]any
	}
	grants := make([]grantedRole, 0, 2)
	for i, permissions := range [][]string{{"source.read", "source.read_evidence"}, {"namespace.read", "resource.read", "resource.read_history", "resource.read_content", "resource.edit"}} {
		roleReply := requestJSON(t, server.URL, "POST", "/api/v1/access/roles", "admin", fmt.Sprintf("version-delegate-role-%d", i), "", map[string]any{"name": fmt.Sprintf("Version delegate %d", i), "permissions": permissions, "reason": "Qualify explicit Version authority"})
		requireStatus(t, roleReply, 201)
		var role identity.RoleDefinition
		decodeResult(t, roleReply, &role)
		grantBody := map[string]any{"membershipId": member.MembershipID, "roleDefinitionId": role.ID, "reason": "Grant only the required independent authority"}
		if i == 1 {
			grantBody["namespaceId"] = ns.ID
		}
		reply := requestJSON(t, server.URL, "POST", "/api/v1/access/assignments", "admin", fmt.Sprintf("version-delegate-grant-%d", i), "", grantBody)
		requireStatus(t, reply, 201)
		var grant identity.AccessAssignment
		decodeResult(t, reply, &grant)
		grants = append(grants, grantedRole{grant.ID, reply.header.Get("ETag"), grantBody})
	}
	setVersion("3.0")
	third := selection()
	thirdOccurrence := occurrences(third)
	thirdBody := map[string]any{"observationId": third.ID, "snapshotId": thirdOccurrence.SnapshotID, "retrievalDigest": thirdOccurrence.RetrievalDigest, "entryLocation": thirdOccurrence.EntryLocation, "priorVersionRevisionId": target.PriorVersionRevisionID}
	requireStatus(t, requestJSON(t, server.URL, "GET", "/api/v1/observations/"+third.ID+"/adoption-target", "reader-two", "", "", nil), 200)
	delegated := requestJSON(t, server.URL, "POST", path, "reader-two", "delegate-append", "", thirdBody)
	requireStatus(t, delegated, 201)
	for i, grant := range grants {
		requireStatus(t, requestJSON(t, server.URL, "POST", "/api/v1/access/assignments/"+grant.id+":revoke", "admin", fmt.Sprintf("revoke-version-delegate-%d", i), grant.etag, map[string]any{"reason": "Qualify current replay refusal"}), 200)
		denied := requestJSON(t, server.URL, "POST", path, "reader-two", "delegate-append", "", thirdBody)
		requireControlProblem(t, denied, 403, catalog.CodeForbidden)
		if denied.header.Get("Idempotency-Replayed") != "" || bytes.Contains(denied.body, []byte(third.ID)) {
			t.Fatal("revoked replay disclosed private receipt")
		}
		requireStatus(t, requestJSON(t, server.URL, "POST", "/api/v1/access/assignments", "admin", fmt.Sprintf("restore-version-delegate-%d", i), "", grant.body), 201)
		restored := requestJSON(t, server.URL, "POST", path, "reader-two", "delegate-append", "", thirdBody)
		requireStatus(t, restored, 201)
		if !bytes.Equal(restored.body, delegated.body) || restored.header.Get("Idempotency-Replayed") != "true" {
			t.Fatal("authority restoration repeated or changed append")
		}
	}
	// Authoritative withdrawal changes new-command eligibility, never the result
	// of an already committed command under unchanged current authority.
	setVersion("")
	serviceClock.Advance(time.Second)
	if _, err := manager.Sync(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	replay := requestJSON(t, server.URL, "POST", path, "admin", "append-upstream-version", "", body)
	requireStatus(t, replay, 201)
	if !bytes.Equal(replay.body, created.body) || replay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("uncertain result was not recovered")
	}
	requireControlProblem(t, requestJSON(t, server.URL, "POST", path, "admin", "new-key-after-withdrawal", "", body), 412, catalog.CodePreconditionFailed)
	disabled, err := federation.NewManager(store, responses, serviceClock, id.Generator{}, federation.WithObservationAdopter(deps.Catalog))
	if err != nil {
		t.Fatal(err)
	}
	deps.Federation = disabled
	closed, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	closedHTTP := httptest.NewServer(closed.Handler())
	defer closedHTTP.Close()
	requireControlProblem(t, requestJSON(t, closedHTTP.URL, "POST", path, "admin", "append-upstream-version", "", body), 403, catalog.CodeForbidden)
}
