package identity

import (
	"context"
	"strings"
	"testing"
)

func TestTenantAndRoleValidation(t *testing.T) {
	t.Parallel()

	for _, tenantID := range []string{
		"00000000-0000-7000-8000-000000000001",
		"ABCDEF12-3456-7890-ABCD-EF1234567890",
	} {
		if !ValidTenantID(tenantID) {
			t.Errorf("ValidTenantID(%q) = false", tenantID)
		}
	}
	for _, tenantID := range []string{"", "default", "00000000-0000-7000-8000-00000000001", "00000000-0000-7000-8000-00000000000z"} {
		if ValidTenantID(tenantID) {
			t.Errorf("ValidTenantID(%q) = true", tenantID)
		}
	}

	for _, role := range []Role{RoleReader, RoleContributor, RoleReviewer, RolePublisher, RoleSourceAdmin, RoleSecurityReviewer, RoleTenantAdmin, RoleServiceClient} {
		if !ValidRole(role) {
			t.Errorf("ValidRole(%q) = false", role)
		}
	}
	if ValidRole("") || ValidRole("administrator") {
		t.Fatal("unknown role was accepted")
	}
}

func TestActorRoleResolutionAndContext(t *testing.T) {
	t.Parallel()

	actor := Actor{
		TenantID: "00000000-0000-7000-8000-000000000001",
		Subject:  "user-1",
		Roles:    []Role{RoleReader},
		Namespaces: map[string][]Role{
			"payments": {RoleContributor},
		},
	}
	if !actor.HasRole(RoleReader, "") {
		t.Fatal("global reader role was not recognized")
	}
	if !actor.HasRole(RoleContributor, "payments") {
		t.Fatal("namespace contributor role was not recognized")
	}
	if actor.HasRole(RoleContributor, "orders") || actor.HasRole(RoleContributor, "") {
		t.Fatal("namespace role escaped its namespace")
	}
	admin := Actor{Roles: []Role{RoleTenantAdmin}}
	if !admin.HasRole(RoleSecurityReviewer, "payments") {
		t.Fatal("tenant administrator did not inherit tenant roles")
	}

	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("actor unexpectedly present in an empty context")
	}
	stored, ok := FromContext(WithActor(context.Background(), actor))
	if !ok || stored.Subject != actor.Subject || stored.TenantID != actor.TenantID {
		t.Fatalf("context actor = %#v, present=%v", stored, ok)
	}
}

func TestAuthorizationDigestIsCanonicalAndBindsPermissions(t *testing.T) {
	t.Parallel()
	first := Actor{
		TenantID: "abcdefab-0000-7000-8000-000000000001",
		Subject:  "user-1",
		Roles:    []Role{RoleContributor, RoleReader, RoleReader},
		Namespaces: map[string][]Role{
			"namespace-b": {RoleReviewer},
			"namespace-a": {RolePublisher, RoleContributor},
		},
		Claims: map[string]string{"scope:catalog.write": "true", "scope:catalog.read": "true"},
	}
	second := Actor{
		TenantID: first.TenantID,
		Subject:  first.Subject,
		Roles:    []Role{RoleReader, RoleContributor},
		Namespaces: map[string][]Role{
			"namespace-a":     {RoleContributor, RolePublisher},
			"namespace-empty": {},
			"namespace-b":     {RoleReviewer},
		},
		Claims: map[string]string{"scope:catalog.read": "true", "scope:catalog.write": "true"},
	}
	if first.AuthorizationDigest() != second.AuthorizationDigest() {
		t.Fatalf("equivalent authorizations produced %q and %q", first.AuthorizationDigest(), second.AuthorizationDigest())
	}
	second.TenantID = strings.ToUpper(first.TenantID)
	if first.AuthorizationDigest() != second.AuthorizationDigest() {
		t.Fatal("equivalent UUID tenant casing changed the authorization digest")
	}
	second.TenantID = first.TenantID
	second.Namespaces["namespace-a"] = []Role{RoleContributor}
	if first.AuthorizationDigest() == second.AuthorizationDigest() {
		t.Fatal("changed namespace authorization retained the same digest")
	}
}
