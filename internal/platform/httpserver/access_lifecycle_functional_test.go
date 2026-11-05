//go:build functional

package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"testing"
	"time"
)

type accessLifecycleAuthenticator struct {
	store      *identity.MemoryStore
	principals map[string]identity.Principal
}

func (a accessLifecycleAuthenticator) Authenticate(ctx context.Context, token string) (identity.Actor, error) {
	p, ok := a.principals[token]
	if !ok {
		return identity.Actor{}, fmt.Errorf("review fixture token rejected")
	}
	return a.store.ResolvePrincipal(ctx, p)
}

func TestAccessLifecycleAssignmentProjection(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	store, err := identity.NewMemoryStore([]identity.Seed{
		{TenantID: tenant, Issuer: "review-a", Subject: "admin-a", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}},
		{TenantID: tenant, Issuer: "review-a", Subject: "exact-reader", ActorType: identity.ActorHuman, RoleTemplates: []string{"reader"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: c, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	auth := accessLifecycleAuthenticator{store: store, principals: map[string]identity.Principal{
		"admin-a":      {TenantID: tenant, Issuer: "review-a", Subject: "admin-a"},
		"exact-reader": {TenantID: tenant, Issuer: "review-a", Subject: "exact-reader"},
	}}
	server := newConfiguredHTTPServer(t, func(d *Dependencies) { d.Identity = service; d.Authenticator = auth; d.Now = c.Now })
	defer server.Close()
	self := requestJSON(t, server.URL, "GET", "/api/v1/access/self", "exact-reader", "", "", nil)
	requireStatus(t, self, 200)
	var effective identity.AuthorizationPage[identity.EffectiveAssignment]
	decodeResult(t, self, &effective)
	if len(effective.Items) != 1 || effective.Items[0].Role.Key != "reader" {
		t.Fatal("invalid exact Reader control")
	}
	a := effective.Items[0].Assignment
	checkReaderPermission := func(want bool) {
		t.Helper()
		r := requestJSON(t, server.URL, "POST", "/api/v1/access/capabilities", "exact-reader", "", "", map[string]any{"checks": []map[string]any{{"permission": "resource.read"}}})
		requireStatus(t, r, 200)
		var decision identity.PermissionDecision
		decodeResult(t, r, &decision)
		if len(decision.Allowed) != 1 || decision.Allowed[0] != want {
			t.Fatal("current Reader authority does not match the retained grant", decision)
		}
	}
	checkReaderPermission(true)
	old := requestJSON(t, server.URL, "GET", "/api/v1/role-assignments?membershipId="+a.MembershipID, "admin-a", "", "", nil)
	requireStatus(t, old, 200)
	var page struct {
		Items []identity.RoleAssignment `json:"items"`
	}
	decodeResult(t, old, &page)
	t.Logf("effective exact Reader assignments=1, named-role projection=%d", len(page.Items))
	if len(page.Items) != 1 {
		t.Error("named-role projection omitted a retained direct Reader assignment")
	}
	revoked := requestJSON(t, server.URL, "POST", "/api/v1/role-assignments/"+a.ID+":revoke", "admin-a", "old-revoke-exact-reader", `"rev-1"`, map[string]any{"reason": "Review same-realm Reader revocation"})
	t.Logf("same-realm exact Reader named-role revoke status=%d", revoked.status)
	requireStatus(t, revoked, 200)
	checkReaderPermission(false)
	for _, collection := range []string{"/api/v1/role-assignments", "/api/v1/access/assignments"} {
		r := requestJSON(t, server.URL, "GET", collection+"?membershipId="+a.MembershipID, "admin-a", "", "", nil)
		requireStatus(t, r, 200)
		var retained struct {
			Items []struct {
				ID       string `json:"id"`
				State    string `json:"state"`
				Revision int64  `json:"revision"`
			} `json:"items"`
		}
		decodeResult(t, r, &retained)
		if len(retained.Items) != 1 || retained.Items[0].ID != a.ID || retained.Items[0].State != "revoked" || retained.Items[0].Revision != 2 {
			t.Fatal("assignment APIs disagree after named revocation", collection, retained)
		}
	}
	replay := requestJSON(t, server.URL, "POST", "/api/v1/role-assignments/"+a.ID+":revoke", "admin-a", "old-revoke-exact-reader", `"rev-1"`, map[string]any{"reason": "Review same-realm Reader revocation"})
	requireStatus(t, replay, 200)
	if replay.header.Get("Idempotency-Replayed") != "true" || replay.header.Get("ETag") != revoked.header.Get("ETag") || !bytes.Equal(replay.body, revoked.body) {
		t.Fatal("named revocation replay changed retained response, revision or attribution")
	}
	checkReaderPermission(false)
}

func TestAccessLifecycleRealmAdministration(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	store, err := identity.NewMemoryStore([]identity.Seed{
		{TenantID: tenant, Issuer: "review-a", Subject: "admin-a", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}},
		{TenantID: tenant, Issuer: "review-b", Subject: "admin-b", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: c, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	auth := accessLifecycleAuthenticator{store: store, principals: map[string]identity.Principal{
		"admin-a": {TenantID: tenant, Issuer: "review-a", Subject: "admin-a", Claims: map[string]string{"scope:tenant.admin": "true"}},
		"admin-b": {TenantID: tenant, Issuer: "review-b", Subject: "admin-b", Claims: map[string]string{"scope:tenant.admin": "true"}},
	}}
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.Identity = service
		d.Authenticator = auth
		d.Now = c.Now
		d.EnforceOAuthScopes = true
	})
	defer server.Close()
	create := func(token, subject, key string) identity.RoleAssignment {
		r := requestJSON(t, server.URL, "POST", "/api/v1/role-assignments", token, key, "", map[string]any{"subject": subject, "actorType": "human", "role": "reader", "reason": "Create owned realm review control"})
		requireStatus(t, r, 201)
		var a identity.RoleAssignment
		decodeResult(t, r, &a)
		return a
	}
	local := create("admin-a", "local-reader", "local-control")
	r := requestJSON(t, server.URL, "POST", "/api/v1/role-assignments/"+local.ID+":revoke", "admin-a", "local-revoke", `"rev-1"`, map[string]any{"reason": "Verify successful same-realm control"})
	requireStatus(t, r, 200)
	foreign := create("admin-b", "foreign-reader", "foreign-control")
	inventory := requestJSON(t, server.URL, "GET", "/api/v1/role-assignments?membershipId="+foreign.MembershipID, "admin-a", "", "", nil)
	requireStatus(t, inventory, 200)
	var crossRealm struct {
		Items []identity.RoleAssignment `json:"items"`
	}
	decodeResult(t, inventory, &crossRealm)
	if len(crossRealm.Items) != 1 || crossRealm.Items[0].ID != foreign.ID || crossRealm.Items[0].State != identity.AssignmentActive {
		t.Fatal("tenant-wide inventory omitted or changed foreign grant", crossRealm)
	}
	modern := requestJSON(t, server.URL, "POST", "/api/v1/access/assignments/"+foreign.ID+":revoke", "admin-a", "modern-foreign-revoke", `"rev-1"`, map[string]any{"reason": "Review another realm refusal"})
	requireStatus(t, modern, 404)
	old := requestJSON(t, server.URL, "POST", "/api/v1/role-assignments/"+foreign.ID+":revoke", "admin-a", "old-foreign-revoke", `"rev-1"`, map[string]any{"reason": "Compare named-role realm behavior"})
	suspended := requestJSON(t, server.URL, "POST", "/api/v1/memberships/"+foreign.MembershipID+":suspend", "admin-a", "foreign-suspend", `"rev-1"`, map[string]any{"reason": "Compare tenant membership lifecycle boundary"})
	t.Logf("same-tenant other-realm statuses: general assignment revoke=%d; named-role revoke=%d; membership suspend=%d", modern.status, old.status, suspended.status)
	requireStatus(t, old, 404)
	requireStatus(t, suspended, 200)
	inventory = requestJSON(t, server.URL, "GET", "/api/v1/role-assignments?membershipId="+foreign.MembershipID, "admin-a", "", "", nil)
	requireStatus(t, inventory, 200)
	decodeResult(t, inventory, &crossRealm)
	if len(crossRealm.Items) != 1 || crossRealm.Items[0].State != identity.AssignmentActive || crossRealm.Items[0].Revision != foreign.Revision {
		t.Fatal("forbidden revocation changed retained assignment", crossRealm)
	}
}

func TestAccessLifecycleCreatorRealmCollision(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	store, err := identity.NewMemoryStore([]identity.Seed{
		{TenantID: tenant, Issuer: "review-a", Subject: "admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}},
		{TenantID: tenant, Issuer: "review-a", Subject: "collision", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleContributor}},
		{TenantID: tenant, Issuer: "review-b", Subject: "collision", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleContributor}},
		{TenantID: tenant, Issuer: "review-b", Subject: "different", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleContributor}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: c, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	auth := accessLifecycleAuthenticator{store: store, principals: map[string]identity.Principal{
		"admin":       {TenantID: tenant, Issuer: "review-a", Subject: "admin"},
		"creator-a":   {TenantID: tenant, Issuer: "review-a", Subject: "collision"},
		"colliding-b": {TenantID: tenant, Issuer: "review-b", Subject: "collision"},
		"different-b": {TenantID: tenant, Issuer: "review-b", Subject: "different"},
	}}
	server := newConfiguredHTTPServer(t, func(d *Dependencies) { d.Identity = service; d.Authenticator = auth; d.Now = c.Now })
	defer server.Close()
	nr := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "create-review-namespace", "", map[string]any{"slug": "realm-review", "displayName": "Realm review"})
	requireStatus(t, nr, 201)
	var ns struct {
		ID string `json:"id"`
	}
	decodeResult(t, nr, &ns)
	create := func(name string) string {
		r := requestJSON(t, server.URL, "POST", "/api/v1/resources", "creator-a", "create-"+name, "", map[string]any{"namespaceId": ns.ID, "canonicalIdentifier": "urn:review:" + name, "kind": "agent"})
		requireStatus(t, r, 201)
		var resource struct {
			ID string `json:"id"`
		}
		decodeResult(t, r, &resource)
		return resource.ID
	}
	ownID := create("same-owner")
	foreignID := create("foreign-owner")
	destroy := func(token, rid, key string) int {
		return requestJSON(t, server.URL, "POST", "/api/v1/resources/"+rid+":delete", token, key, `"rev-1"`, map[string]any{"reason": "Review creator identity enforcement"}).status
	}
	if status := destroy("creator-a", ownID, "same-owner-delete"); status != 200 && status != 204 {
		t.Fatalf("same-owner positive control=%d", status)
	}
	if status := destroy("different-b", foreignID, "different-owner-delete"); status != 403 {
		t.Fatalf("different subject negative control=%d", status)
	}
	a, err := auth.Authenticate(t.Context(), "creator-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := auth.Authenticate(t.Context(), "colliding-b")
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.CheckPermissions(t.Context(), b, []identity.PermissionCheck{{Permission: "access.manage"}})
	if err != nil || len(d.Allowed) != 1 || d.Allowed[0] {
		t.Fatal("collision caller unexpectedly has administrator authority")
	}
	if a.MembershipID == b.MembershipID || a.RealmID == b.RealmID || a.Subject != b.Subject {
		t.Fatal("collision fixture is not distinct principals with equal subjects")
	}
	status := destroy("colliding-b", foreignID, "colliding-owner-delete")
	t.Logf("distinct realm and membership with identical subject; protected admin=false; creator-only delete status=%d", status)
	if status != 403 {
		t.Fatalf("creator-only deletion admitted a different realm/membership: status=%d", status)
	}
	current := requestJSON(t, server.URL, "GET", "/api/v1/resources/"+foreignID, "creator-a", "", "", nil)
	requireStatus(t, current, 200)
	replay := destroy("colliding-b", foreignID, "colliding-owner-delete")
	if replay != 403 {
		t.Fatal("forbidden replay admitted", replay)
	}
	auditReply := requestJSON(t, server.URL, "GET", "/api/v1/audit-events?targetType=resource&targetId="+foreignID, "admin", "", "", nil)
	requireStatus(t, auditReply, 200)
	var evidence struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
	}
	decodeResult(t, auditReply, &evidence)
	for _, event := range evidence.Items {
		if event.Action == "resource.deleted" {
			t.Fatal("forbidden deletion emitted success evidence")
		}
	}

}
