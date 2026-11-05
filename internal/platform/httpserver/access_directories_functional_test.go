//go:build functional

package httpserver

import (
	"net/url"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestAccessDirectoryFiltersAndMemberContributionHTTPContracts(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	const other = "00000000-0000-7000-8000-000000000002"
	seeds := []identity.Seed{
		{TenantID: tenant, Issuer: "realm-a", Subject: "admin", ActorType: identity.ActorHuman, RoleTemplates: []string{"tenant_admin"}},
		{TenantID: tenant, Issuer: "realm-a", Subject: "same", ActorType: identity.ActorHuman, RoleTemplates: []string{"library_member"}},
		{TenantID: tenant, Issuer: "realm-b", Subject: "same", ActorType: identity.ActorHuman, RoleTemplates: []string{"tenant_admin"}},
		{TenantID: other, Issuer: "realm-other", Subject: "other", ActorType: identity.ActorHuman, RoleTemplates: []string{"tenant_admin"}},
	}
	store, err := identity.NewMemoryStore(seeds)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	s, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: c, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	auth := accessLifecycleAuthenticator{store: store, principals: map[string]identity.Principal{}}
	for _, seed := range seeds {
		token := seed.Issuer
		auth.principals[token] = identity.Principal{TenantID: seed.TenantID, Issuer: seed.Issuer, Subject: seed.Subject, Claims: map[string]string{"scope:tenant.admin": "true"}}
	}
	// Distinct tokens for a same-realm administrator, specialist and no-scope control.
	auth.principals["admin"] = identity.Principal{TenantID: tenant, Issuer: "realm-a", Subject: "admin", Claims: map[string]string{"scope:tenant.admin": "true"}}
	auth.principals["specialist"] = identity.Principal{TenantID: tenant, Issuer: "realm-a", Subject: "same", Claims: map[string]string{"scope:tenant.admin": "true"}}
	auth.principals["no-scope"] = identity.Principal{TenantID: tenant, Issuer: "realm-a", Subject: "admin"}
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.Identity = s
		d.Authenticator = auth
		d.Now = c.Now
		d.EnforceOAuthScopes = true
	})
	defer server.Close()
	get := func(path, token string) httpResult {
		return requestJSON(t, server.URL, "GET", path, token, "", "", nil)
	}
	// Exact subject matches separate same-tenant realm identities and paginates.
	first := get("/api/v1/memberships?subject=same&actorType=human&state=active&limit=1", "admin")
	requireStatus(t, first, 200)
	requireControlSchema(t, "MembershipPage", first.body)
	var members identity.MembershipPage
	decodeResult(t, first, &members)
	if len(members.Items) != 1 || members.NextCursor == "" {
		t.Fatal(members)
	}
	next := get("/api/v1/memberships?subject=same&actorType=human&state=active&limit=1&cursor="+url.QueryEscape(members.NextCursor), "admin")
	requireStatus(t, next, 200)
	var second identity.MembershipPage
	decodeResult(t, next, &second)
	if len(second.Items) != 1 || second.Items[0].ID == members.Items[0].ID || second.Items[0].RealmID == members.Items[0].RealmID || second.NextCursor != "" {
		t.Fatal(second)
	}
	changed := get("/api/v1/memberships?subject=Same&limit=1&cursor="+url.QueryEscape(members.NextCursor), "admin")
	requireControlProblem(t, changed, 400, catalog.CodeInvalidArgument)
	current := get("/api/v1/memberships?subject=same&realmId="+members.Items[0].RealmID, "admin")
	requireStatus(t, current, 200)
	var exact identity.MembershipPage
	decodeResult(t, current, &exact)
	if len(exact.Items) != 1 || exact.Items[0].ID != members.Items[0].ID {
		t.Fatal(exact)
	}
	for _, path := range []string{"/api/v1/memberships?subject=", "/api/v1/memberships?subject=same&subject=same", "/api/v1/memberships?state=archived", "/api/v1/memberships?actorType=robot", "/api/v1/memberships?realmId=invalid", "/api/v1/access/roles?name=", "/api/v1/access/roles?name=x&name=y", "/api/v1/access/roles?state=suspended", "/api/v1/access/roles?key=invented", "/api/v1/access/groups?state=offboarded", "/api/v1/access/groups?key=reader"} {
		requireControlProblem(t, get(path, "admin"), 400, catalog.CodeInvalidArgument)
	}
	roles := get("/api/v1/access/roles?key=library_member&state=active&limit=1", "admin")
	requireStatus(t, roles, 200)
	var definitions identity.AuthorizationPage[identity.RoleDefinition]
	decodeResult(t, roles, &definitions)
	if len(definitions.Items) != 1 || definitions.Items[0].Key != "library_member" {
		t.Fatal(definitions)
	}
	groups := get("/api/v1/access/groups?name=Missing&state=suspended", "admin")
	requireStatus(t, groups, 200)
	var groupPage identity.AuthorizationPage[identity.AccessGroup]
	decodeResult(t, groups, &groupPage)
	if len(groupPage.Items) != 0 {
		t.Fatal(groupPage)
	}
	self := get("/api/v1/me", "specialist")
	requireStatus(t, self, 200)
	var target struct {
		MembershipID string `json:"membershipId"`
		RealmID      string `json:"realmId"`
	}
	decodeResult(t, self, &target)
	path := "/api/v1/memberships/" + target.MembershipID + "/access"
	response := get(path, "admin")
	requireStatus(t, response, 200)
	requireControlSchema(t, "MemberAccessPage", response.body)
	var projection identity.MemberAccessPage
	decodeResult(t, response, &projection)
	if projection.Membership.ID != target.MembershipID || projection.GroupCoverage != "current_realm" || len(projection.Items) != 1 || projection.Items[0].Role.Key != "library_member" || !projection.Items[0].Effective {
		t.Fatal("specialist contribution omitted or misbound", projection)
	}
	for _, token := range []string{"specialist", "no-scope"} {
		requireControlProblem(t, get(path, token), 403, catalog.CodeForbidden)
	}
	foreign := get(path, "realm-b")
	requireStatus(t, foreign, 200)
	decodeResult(t, foreign, &projection)
	if projection.GroupCoverage != "unavailable" {
		t.Fatal("other-realm group coverage was claimed", projection)
	}
	requireControlProblem(t, get(path, "realm-other"), 404, catalog.CodeNotFound)
	requireControlProblem(t, get(path+"?limit=0", "admin"), 400, catalog.CodeInvalidArgument)
	requireControlProblem(t, get(path+"?cursor=invalid", "admin"), 400, catalog.CodeInvalidArgument)
	requireControlProblem(t, get(path+"?unknown=x", "admin"), 400, catalog.CodeInvalidArgument)
}
