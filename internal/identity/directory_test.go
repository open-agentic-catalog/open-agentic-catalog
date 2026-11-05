package identity

import (
	"fmt"
	"testing"
	"time"
)

func TestMembershipDirectoryFiltersBindExactSelectionAndContinuation(t *testing.T) {
	seeds := []Seed{{TenantID: administrationTenant, Issuer: "issuer", Subject: "admin", ActorType: ActorHuman, RoleTemplates: []string{"tenant_admin"}}}
	for i := 0; i < 30; i++ {
		seeds = append(seeds, Seed{TenantID: administrationTenant, Issuer: fmt.Sprintf("issuer-%d", i%2), Subject: fmt.Sprintf("person-%02d", i/2), ActorType: ActorHuman})
	}
	store, err := NewMemoryStore(seeds)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{at: time.Unix(100, 0)}, IDs: &identityTestIDs{next: 1000}, CursorKey: identityTestCursorKey})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.ResolvePrincipal(t.Context(), Principal{TenantID: administrationTenant, Issuer: "issuer", Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(t.Context(), admin)
	filter := MembershipFilter{Subject: "person-00", State: MembershipActive, ActorType: ActorHuman}
	first, err := s.ListMemberships(ctx, 1, "", filter)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	second, err := s.ListMemberships(ctx, 1, first.NextCursor, filter)
	if err != nil || len(second.Items) != 1 || second.Items[0].RealmID == first.Items[0].RealmID || second.NextCursor != "" {
		t.Fatal(second, err)
	}
	for _, changed := range []MembershipFilter{{Subject: "person-01"}, {Subject: "person-00", State: MembershipSuspended}, {Subject: "person-00", ActorType: ActorService}, {Subject: "person-00", RealmID: first.Items[0].RealmID}} {
		if _, err := s.ListMemberships(ctx, 1, first.NextCursor, changed); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal("cursor escaped filters", err)
		}
	}
	filter.RealmID = first.Items[0].RealmID
	page, err := s.ListMemberships(ctx, 25, "", filter)
	if err != nil || len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatal(page, err)
	}
	filter.Subject = "Person-00"
	page, err = s.ListMemberships(ctx, 25, "", filter)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("subject lost exact case", page, err)
	}
	for _, invalid := range []MembershipFilter{{Subject: " "}, {State: "unknown"}, {ActorType: "robot"}, {RealmID: "wrong"}} {
		if _, err := s.ListMemberships(ctx, 25, "", invalid); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal("invalid filter", err)
		}
	}
	if _, err := ValidateMembershipFilters([]MembershipFilter{{}, {}}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("ambiguous filters", err)
	}
}

func TestRoleAndGroupDirectoriesFilterBeforePagination(t *testing.T) {
	_, s, admin, _, _ := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	for range 2 {
		administrationRole(t, s, ctx, "resource.read")
	}
	filter := DirectoryFilter{Name: "Reviewed access", State: "active"}
	first, err := s.ListRoleDefinitions(ctx, 1, "", filter)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	second, err := s.ListRoleDefinitions(ctx, 1, first.NextCursor, filter)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID || second.NextCursor != "" {
		t.Fatal(second, err)
	}
	for _, bad := range []DirectoryFilter{{Name: "reviewed access"}, {Name: filter.Name, State: "archived"}, {Key: "reader"}} {
		if _, err := s.ListRoleDefinitions(ctx, 1, first.NextCursor, bad); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal("role cursor escaped filters", err)
		}
	}
	page, err := s.ListRoleDefinitions(ctx, 25, "", DirectoryFilter{Key: "reader"})
	if err != nil || len(page.Items) != 1 || !page.Items[0].Builtin || page.Items[0].Key != "reader" {
		t.Fatal("built-in lookup confused display names", page, err)
	}
	for range 2 {
		if _, err := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Exact team", Reason: "Qualify exact lookup"}, "", "group"); err != nil {
			t.Fatal(err)
		}
	}
	groups, err := s.ListAccessGroups(ctx, 1, "", DirectoryFilter{Name: "Exact team", State: "active"})
	if err != nil || len(groups.Items) != 1 || groups.NextCursor == "" {
		t.Fatal(groups, err)
	}
	if _, err := s.ListAccessGroups(ctx, 1, groups.NextCursor, DirectoryFilter{Name: "exact team"}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("group cursor escaped filters", err)
	}
	for _, invalid := range []DirectoryFilter{{Name: " "}, {State: "offboarded"}, {Key: "invented"}} {
		if _, err := s.ListRoleDefinitions(ctx, 25, "", invalid); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal("invalid role filter", err)
		}
	}
	if _, err := s.ListAccessGroups(ctx, 25, "", DirectoryFilter{Key: "reader"}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("group accepted role key", err)
	}
	if _, err := directoryRequest([]DirectoryFilter{{}, {}}, false); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("ambiguous directory filters", err)
	}
}

func TestMemberContributionsRetainPathsAndRespectAdministrationAndRealms(t *testing.T) {
	store, s, admin, alice, sink := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	role := administrationRole(t, s, ctx, "resource.read")
	group, err := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Exact team", Reason: "Qualify contribution view"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	relation, err := s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Add current member", "join", false)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, NamespaceID: administrationNamespace, Reason: "Exact direct path"}, "direct")
	if err != nil {
		t.Fatal(err)
	}
	groupGrant, err := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: administrationOtherNamespace, Reason: "Disjoint group path"}, "group")
	if err != nil {
		t.Fatal(err)
	}
	before := len(sink.records)
	page, err := s.MemberAccess(ctx, alice.MembershipID, 1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" || page.GroupCoverage != "current_realm" {
		t.Fatal(page, err)
	}
	next, err := s.MemberAccess(ctx, alice.MembershipID, 1, page.NextCursor)
	if err != nil || len(next.Items) != 1 || next.NextCursor != "" || next.Items[0].Assignment.ID == page.Items[0].Assignment.ID {
		t.Fatal(next, err)
	}
	if _, err := s.MemberAccess(ctx, admin.MembershipID, 1, page.NextCursor); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("member cursor escaped its target", err)
	}
	if _, err := s.MemberAccess(WithActor(t.Context(), alice), alice.MembershipID, 25, ""); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("recipient acquired admin projection", err)
	}
	if len(sink.records) != before {
		t.Fatal("read wrote success evidence")
	}
	if _, err := s.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Suspend retained group"}, GroupSuspended, "suspend"); err != nil {
		t.Fatal(err)
	}
	page, err = s.MemberAccess(ctx, alice.MembershipID, 25, "")
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	for _, item := range page.Items {
		if item.Assignment.ID == direct.ID && !item.Effective {
			t.Fatal("group suspension removed direct path")
		}
		if item.Assignment.ID == groupGrant.ID && (item.Effective || item.Group == nil || item.Group.State != GroupSuspended || item.Relation == nil || item.Relation.ID != relation.ID) {
			t.Fatal("suspended path was hidden or treated as effective", item)
		}
	}
	// A same-tenant administrator from another realm sees direct policy only.
	foreignRealm := deterministicUUID("realm", "other", administrationTenant, "")
	foreignMember := deterministicUUID("membership", foreignRealm, "foreign-admin")
	store.realms[foreignRealm] = memoryRealm{ID: foreignRealm, TenantID: administrationTenant, Issuer: "other", State: RealmActive}
	store.memberships[foreignMember] = Membership{ID: foreignMember, TenantID: administrationTenant, RealmID: foreignRealm, Subject: "foreign-admin", ActorType: ActorHuman, State: MembershipActive, Revision: 1}
	store.accessAssignments[administrationOtherNamespace] = AccessAssignment{ID: administrationOtherNamespace, TenantID: administrationTenant, RealmID: foreignRealm, MembershipID: foreignMember, RoleDefinitionID: deterministicUUID("definition", administrationTenant, "tenant_admin"), State: AssignmentActive, Revision: 1}
	foreign, err := store.ResolvePrincipal(t.Context(), Principal{TenantID: administrationTenant, Issuer: "other", Subject: "foreign-admin"})
	if err != nil {
		t.Fatal(err)
	}
	limited, err := s.MemberAccess(WithActor(t.Context(), foreign), alice.MembershipID, 25, "")
	if err != nil || limited.GroupCoverage != "unavailable" || len(limited.Items) != 1 || limited.Items[0].Assignment.ID != direct.ID {
		t.Fatal("foreign realm disclosed groups", limited, err)
	}
	removed, err := s.ChangeAccessGroupMember(ctx, group.ID, relation.ID, alice.MembershipID, relation.Revision, "Remove only this retained path", "remove", true)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := s.MemberAccess(ctx, alice.MembershipID, 25, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range retained.Items {
		if item.Assignment.ID == groupGrant.ID {
			found = true
			if item.Effective || item.Relation == nil || item.Relation.ID != removed.ID || item.Relation.State != AssignmentRevoked {
				t.Fatal("revoked relation history changed eligibility", item)
			}
		}
	}
	if !found {
		t.Fatal("removed group relation path was lost")
	}
	currentGroup, err := s.GetAccessGroup(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAccessGroup(ctx, group.ID, currentGroup.Revision, GroupInput{Reason: "Explicitly reactivate group"}, GroupActive, "reactivate"); err != nil {
		t.Fatal(err)
	}
	replacement, err := s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Explicitly rejoin group", "rejoin", false)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.MemberAccess(ctx, alice.MembershipID, 25, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range current.Items {
		if item.Assignment.ID == groupGrant.ID && (!item.Effective || item.Relation == nil || item.Relation.ID != replacement.ID) {
			t.Fatal("latest active relation was not selected over retained revoked history", item)
		}
	}
}

func TestMembershipOnlyHumanAndServiceRequireSeparateSpecialistGrants(t *testing.T) {
	store, s, admin, _, _ := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	for _, actorType := range []ActorType{ActorHuman, ActorService} {
		grant, err := s.GrantRole(ctx, GrantRoleInput{Subject: "new-" + string(actorType), ActorType: actorType, Role: RoleServiceClient, Reason: "Membership only"}, "membership")
		if err != nil {
			t.Fatal(err)
		}
		actor, err := store.ResolvePrincipal(t.Context(), Principal{TenantID: admin.TenantID, Issuer: "issuer", Subject: grant.Subject})
		if err != nil || actor.ActorType != actorType {
			t.Fatal(actor, err)
		}
		checks := []PermissionCheck{{Permission: "resource.read"}, {Permission: "library.own"}}
		decision, err := store.CheckPermissions(t.Context(), actor, checks)
		if err != nil || decision.Allowed[0] || decision.Allowed[1] {
			t.Fatal("empty template granted product actions", decision, err)
		}
		definitions, err := s.ListRoleDefinitions(ctx, 1, "", DirectoryFilter{Key: "library_member"})
		if err != nil || len(definitions.Items) != 1 {
			t.Fatal(definitions, err)
		}
		if _, err := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{MembershipID: actor.MembershipID, RoleDefinitionID: definitions.Items[0].ID, Reason: "Separate specialist command"}, "specialist"); err != nil {
			t.Fatal(err)
		}
		decision, err = store.CheckPermissions(t.Context(), actor, checks)
		if err != nil || decision.Allowed[0] || !decision.Allowed[1] {
			t.Fatal("specialist path added temporary Reader authority", decision, err)
		}
	}
}

func TestMemberContributionsRetainMembershipLifecycleWithoutAssignments(t *testing.T) {
	_, s, admin, alice, _ := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	page, err := s.MemberAccess(ctx, alice.MembershipID, 25, "")
	if err != nil || page.Membership.State != MembershipActive || len(page.Items) != 0 || page.Items == nil {
		t.Fatal(page, err)
	}
	changed, err := s.SuspendMembership(ctx, alice.MembershipID, "Suspend empty membership", "suspend", page.Membership.Revision)
	if err != nil {
		t.Fatal(err)
	}
	page, err = s.MemberAccess(ctx, alice.MembershipID, 25, "")
	if err != nil || page.Membership.State != MembershipSuspended || page.Membership.Revision != changed.Membership.Revision || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	if _, err := s.MemberAccess(ctx, "invalid", 25, ""); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
	if _, err := s.MemberAccess(ctx, administrationNamespace, 25, ""); ErrorCodeOf(err) != CodeNotFound {
		t.Fatal(err)
	}
	if _, err := s.MemberAccess(ctx, alice.MembershipID, 0, ""); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
}
