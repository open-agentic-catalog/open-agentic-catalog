//go:build integration

package database

import (
	"reflect"
	"slices"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestPostgresCurrentSelfAccessUsesScopedLivePolicy(t *testing.T) {
	_, store, service, admin, alice, direct := authorizationCoreFixture(t)
	ctx := identity.WithActor(t.Context(), admin)
	role, err := service.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Scoped author", Permissions: []identity.Permission{"namespace.read", "resource.create"}, Reason: "Qualify compact self context"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	group, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Scoped authors", Reason: "Qualify group capability hints"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Add scoped author", "add", false); err != nil {
		t.Fatal(err)
	}
	grant, err := service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: authorizationCoreNamespace, Reason: "Grant scoped author"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	read := func(actor identity.Actor, namespace string) identity.SelfAccessProjection {
		t.Helper()
		got, err := identity.CurrentSelfAccess(identity.WithActor(t.Context(), actor), identity.SelfAccessRequest{Limit: 1, NamespaceID: namespace})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := read(alice, "")
	want := []identity.Permission{"namespace.read", "ratings.own", "resource.create", "resource.read", "resource.read_content", "resource.read_history"}
	if len(alice.Roles) != 0 || alice.Namespaces != nil || len(got.Permissions) != 0 || !slices.Equal(got.AvailablePermissions, want) || !reflect.DeepEqual(got.NamespaceGrants, []identity.NamespacePermissionAccess{{NamespaceID: authorizationCoreNamespace, Permissions: want}}) {
		t.Fatalf("scoped compact self projection=%+v", got)
	}
	if missing := read(alice, housekeepingTenantB); len(missing.NamespaceGrants) != 0 || len(missing.Permissions) != 0 {
		t.Fatal("foreign/missing namespace manufactured a grant")
	}
	if global := read(admin, ""); len(global.Permissions) != len(identity.Permissions()) || len(global.NamespaceGrants) != 0 {
		t.Fatalf("finite tenant hints=%+v", global)
	}
	prior := got.AuthorizationRevision
	if _, err = service.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Suspend group path"}, identity.GroupSuspended, "suspend"); err != nil {
		t.Fatal(err)
	}
	got = read(alice, "")
	if got.AuthorizationRevision <= prior || slices.Contains(got.AvailablePermissions, "resource.create") || len(got.NamespaceGrants) != 1 {
		t.Fatalf("group suspension lost direct path or retained group path=%+v", got)
	}
	if _, err = store.ReadSelfAccess(t.Context(), alice, identity.SelfAccessRequest{Limit: 1, AuthorizationRevision: prior}); err == nil {
		t.Fatal("stale self snapshot admitted")
	}
	if _, err = service.RevokeRole(ctx, direct.ID, "Remove last direct path", "revoke", direct.Revision); err != nil {
		t.Fatal(err)
	}
	got = read(alice, "")
	if len(got.Permissions) != 0 || len(got.AvailablePermissions) != 0 || len(got.NamespaceGrants) != 0 {
		t.Fatalf("retained ineffective assignments leaked hints=%+v grant=%s", got, grant.ID)
	}
	foreign := alice
	foreign.TenantID = housekeepingTenantB
	if _, err = store.ReadSelfAccess(t.Context(), foreign, identity.SelfAccessRequest{Limit: 1, AuthorizationRevision: got.AuthorizationRevision}); err == nil {
		t.Fatal("cross-tenant self admitted")
	}
}
