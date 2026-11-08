package identity

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// policyPath is a finite model of current authority, independent of the store's
// effective-assignment and built-in template implementations.
type policyPath struct {
	permission Permission
	scope      string
	active     bool
}

func TestPropertyRetainedPermissionGraph(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		store, service, admin, actor, _ := administrationFixture(t)
		ctx := WithActor(t.Context(), admin)
		// Deliberately misleading legacy projections must never grant authority.
		actor.Roles = []Role{RoleTenantAdmin, RolePublisher}
		actor.Namespaces = map[string][]Role{administrationOtherNamespace: {RoleContributor}}
		checks := make([]PermissionCheck, 0, 10)
		for _, scope := range []string{"", administrationNamespace, administrationOtherNamespace} {
			for _, permission := range []Permission{"namespace.read", "resource.read", "resource.edit"} {
				checks = append(checks, PermissionCheck{Permission: permission, NamespaceID: scope})
			}
		}
		checks = append(checks, PermissionCheck{Permission: "library.own"})
		paths := make([]policyPath, 0, 6)
		memberActive, realmActive := true, true
		verify := func(stage string) {
			got, err := store.CheckPermissions(t.Context(), actor, checks)
			if err != nil || len(got.Allowed) != len(checks) {
				rt.Fatalf("%s: decisions=%+v error=%v", stage, got, err)
			}
			for i, check := range checks {
				want := false
				for _, path := range paths {
					if memberActive && realmActive && path.active && path.permission == check.Permission && (path.scope == "" || strings.EqualFold(path.scope, check.NamespaceID)) {
						want = true
					}
				}
				if got.Allowed[i] != want {
					rt.Fatalf("%s: check=%+v got=%v want=%v paths=%+v", stage, check, got.Allowed[i], want, paths)
				}
			}
		}
		verify("projections without retained authority")
		// Each case guarantees direct/group duplication and both tenant/exact
		// namespace scope; generated order, permission and extra paths vary.
		scope := rapid.SampledFrom([]string{"", administrationNamespace}).Draw(rt, "duplicate scope")
		permission := rapid.SampledFrom([]Permission{"resource.read", "namespace.read"}).Draw(rt, "duplicate permission")
		role, err := service.SaveRoleDefinition(ctx, "", 0, RoleDefinitionInput{Name: "Generated policy", Permissions: []Permission{permission}, Reason: "Create generated policy"}, false, "role")
		if err != nil {
			rt.Fatal(err)
		}
		group, err := service.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Generated group", Reason: "Create generated group"}, "", "group")
		if err != nil {
			rt.Fatal(err)
		}
		link, err := service.ChangeAccessGroupMember(ctx, group.ID, "", actor.MembershipID, 0, "Add generated member", "member", false)
		if err != nil {
			rt.Fatal(err)
		}
		var direct, grouped AccessAssignment
		for _, target := range rapid.Permutation([]string{"direct", "group"}).Draw(rt, "path insertion order") {
			input := AccessAssignmentInput{RoleDefinitionID: role.ID, NamespaceID: scope, Reason: "Grant generated path"}
			if target == "direct" {
				input.MembershipID = actor.MembershipID
			} else {
				input.GroupID = group.ID
			}
			assignment, grantErr := service.GrantAuthorizationAssignment(ctx, input, target)
			if grantErr != nil {
				rt.Fatal(grantErr)
			}
			if target == "direct" {
				direct = assignment
			} else {
				grouped = assignment
			}
		}
		paths = append(paths, policyPath{permission, scope, true}, policyPath{permission, scope, true})
		tenantRole, err := service.SaveRoleDefinition(ctx, "", 0, RoleDefinitionInput{Name: "Tenant personal library", Permissions: []Permission{"library.own"}, Reason: "Create independent tenant permission"}, false, "tenant-role")
		if err != nil {
			rt.Fatal(err)
		}
		if _, err = service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: tenantRole.ID, MembershipID: actor.MembershipID, Reason: "Grant tenant personal library"}, "tenant-grant"); err != nil {
			rt.Fatal(err)
		}
		paths = append(paths, policyPath{"library.own", "", true})
		for i := range rapid.IntRange(1, 3).Draw(rt, "independent paths") {
			p := rapid.SampledFrom([]Permission{"namespace.read", "resource.read"}).Draw(rt, "extra permission")
			other, saveErr := service.SaveRoleDefinition(ctx, "", 0, RoleDefinitionInput{Name: fmt.Sprintf("Independent %d", i), Permissions: []Permission{p}, Reason: "Create independent path"}, false, "extra")
			if saveErr != nil {
				rt.Fatal(saveErr)
			}
			if _, grantErr := service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: other.ID, MembershipID: actor.MembershipID, NamespaceID: administrationOtherNamespace, Reason: "Grant independent scope"}, "extra-grant"); grantErr != nil {
				rt.Fatal(grantErr)
			}
			paths = append(paths, policyPath{p, administrationOtherNamespace, true})
		}
		verify("all retained paths")
		if _, err = service.RevokeAuthorizationAssignment(ctx, direct.ID, direct.Revision, "Remove one duplicate path", "revoke-direct"); err != nil {
			rt.Fatal(err)
		}
		paths[0].active = false
		verify("group still grants after direct removal")
		group, err = service.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Suspend generated group"}, GroupSuspended, "suspend-group")
		if err != nil {
			rt.Fatal(err)
		}
		paths[1].active = false
		verify("suspended group")
		group, err = service.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Restore generated group"}, GroupActive, "restore-group")
		if err != nil {
			rt.Fatal(err)
		}
		paths[1].active = true
		verify("restored group")
		membership, err := service.SuspendMembership(ctx, actor.MembershipID, "Suspend generated member", "suspend-member", 1)
		if err != nil {
			rt.Fatal(err)
		}
		memberActive = false
		verify("suspended membership")
		if _, err = service.ReactivateMembership(ctx, actor.MembershipID, "Restore generated member", "restore-member", membership.Membership.Revision); err != nil {
			rt.Fatal(err)
		}
		memberActive = true
		// Realm suspension has no administration endpoint; model its retained
		// operator state directly, without manufacturing an invalid assignment.
		realm := store.realms[actor.RealmID]
		realm.State = RealmSuspended
		store.realms[realm.ID] = realm
		realmActive = false
		verify("suspended realm")
		realm.State = RealmActive
		store.realms[realm.ID] = realm
		realmActive = true
		verify("restored realm")
		changedPermission := Permission("namespace.read")
		if permission == changedPermission {
			changedPermission = "resource.read"
		}
		role, err = service.SaveRoleDefinition(ctx, role.ID, role.Revision, RoleDefinitionInput{Name: role.Name, Permissions: []Permission{changedPermission}, Reason: "Replace active retained permission"}, false, "change-active-role")
		if err != nil {
			rt.Fatal(err)
		}
		paths[1].permission = changedPermission
		verify("active definition replacement changes current decisions")
		if _, err = service.ChangeAccessGroupMember(ctx, group.ID, link.ID, actor.MembershipID, link.Revision, "Remove final group path", "unlink", true); err != nil {
			rt.Fatal(err)
		}
		paths[1].active = false
		verify("last duplicate path removed")
		if _, err = service.RevokeAuthorizationAssignment(ctx, grouped.ID, grouped.Revision, "Retire generated assignment", "revoke-group"); err != nil {
			rt.Fatal(err)
		}
		updated := []Permission{"namespace.read", "resource.read"}
		updated = rapid.Permutation(updated).Draw(rt, "permission insertion order")
		role, err = service.SaveRoleDefinition(ctx, role.ID, role.Revision, RoleDefinitionInput{Name: role.Name, Permissions: slices.Clone(updated), Reason: "Replace retained permissions"}, false, "update-role")
		if err != nil {
			rt.Fatal(err)
		}
		if _, err = service.SaveRoleDefinition(ctx, role.ID, role.Revision, RoleDefinitionInput{Reason: "Archive unused definition"}, true, "archive-role"); err != nil {
			rt.Fatal(err)
		}
		verify("archived definition confers no authority")
	})
}
