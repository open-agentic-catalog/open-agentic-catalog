//go:build integration

package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresAccessDirectoriesAndMemberContributions(t *testing.T) {
	db, store, _, admin, alice, seeded := authorizationCoreFixture(t)
	c := clock.NewFake(seeded.GrantedAt.Add(time.Second))
	s, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: c, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	for i := range 26 {
		if _, err := s.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: fmt.Sprintf("Earlier role %02d", i), Permissions: []identity.Permission{"resource.read"}, Reason: "Qualify selection before the UI page bound"}, false, "noise-role"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: fmt.Sprintf("Earlier group %02d", i), Reason: "Qualify selection before the UI page bound"}, "", "noise-group"); err != nil {
			t.Fatal(err)
		}
	}
	c.Advance(time.Second)
	// More than a UI page, so a filter applied after hydration cannot pass.
	for i := 0; i < 28; i++ {
		if _, err := s.GrantRole(ctx, identity.GrantRoleInput{Subject: fmt.Sprintf("directory-%02d", i), ActorType: identity.ActorHuman, Role: identity.RoleServiceClient, Reason: "Qualify paged directory"}, "seed-member"); err != nil {
			t.Fatal(err)
		}
	}
	members, err := s.ListMemberships(ctx, 1, "", identity.MembershipFilter{Subject: "directory-27", State: identity.MembershipActive, ActorType: identity.ActorHuman, RealmID: admin.RealmID})
	if err != nil || len(members.Items) != 1 || members.Items[0].Subject != "directory-27" || members.NextCursor != "" {
		t.Fatal("membership filter was not selected before limit", members, err)
	}
	wrongCase, err := s.ListMemberships(ctx, 25, "", identity.MembershipFilter{Subject: "Directory-27"})
	if err != nil || len(wrongCase.Items) != 0 {
		t.Fatal("exact subject case lost", wrongCase, err)
	}
	first, err := s.ListMemberships(ctx, 1, "", identity.MembershipFilter{State: identity.MembershipActive})
	if err != nil || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	if _, err := s.ListMemberships(ctx, 1, first.NextCursor, identity.MembershipFilter{ActorType: identity.ActorHuman}); identity.ErrorCodeOf(err) != identity.CodeInvalidArgument {
		t.Fatal("membership cursor ignored filters", err)
	}
	for range 2 {
		if _, err := s.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Exact duplicate", Permissions: []identity.Permission{"resource.read"}, Reason: "Qualify duplicate display names"}, false, "new-role"); err != nil {
			t.Fatal(err)
		}
	}
	roles, err := s.ListRoleDefinitions(ctx, 1, "", identity.DirectoryFilter{Name: "Exact duplicate", State: "active"})
	if err != nil || len(roles.Items) != 1 || roles.NextCursor == "" {
		t.Fatal(roles, err)
	}
	more, err := s.ListRoleDefinitions(ctx, 1, roles.NextCursor, identity.DirectoryFilter{Name: "Exact duplicate", State: "active"})
	if err != nil || len(more.Items) != 1 || more.Items[0].ID == roles.Items[0].ID || more.NextCursor != "" {
		t.Fatal(more, err)
	}
	if _, err := s.ListRoleDefinitions(ctx, 1, roles.NextCursor, identity.DirectoryFilter{Name: "exact duplicate"}); identity.ErrorCodeOf(err) != identity.CodeInvalidArgument {
		t.Fatal("role cursor ignored exact name", err)
	}
	library, err := s.ListRoleDefinitions(ctx, 1, "", identity.DirectoryFilter{Key: "library_member", State: "active"})
	if err != nil || len(library.Items) != 1 || !library.Items[0].Builtin || library.Items[0].Key != "library_member" {
		t.Fatal(library, err)
	}
	group, err := s.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Exact team", Reason: "Qualify inspected paths"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	groupTwo, err := s.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Exact team", Reason: "Qualify duplicate groups"}, "", "group-two")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := s.ListAccessGroups(ctx, 1, "", identity.DirectoryFilter{Name: "Exact team", State: "active"})
	if err != nil || len(groups.Items) != 1 || groups.NextCursor == "" {
		t.Fatal(groups, err)
	}
	filtered, err := s.ListAccessGroups(ctx, 25, "", identity.DirectoryFilter{Name: "Exact team", State: "active"})
	if err != nil || len(filtered.Items) != 2 || filtered.NextCursor != "" {
		t.Fatal("name filter ran after a UI-sized SQL page", filtered, err)
	}
	groupMore, err := s.ListAccessGroups(ctx, 1, groups.NextCursor, identity.DirectoryFilter{Name: "Exact team", State: "active"})
	if err != nil || len(groupMore.Items) != 1 || groupMore.NextCursor != "" || groupMore.Items[0].ID == groups.Items[0].ID {
		t.Fatal(groupMore, err)
	}
	if _, err := s.ListAccessGroups(ctx, 1, groups.NextCursor, identity.DirectoryFilter{Name: "exact team"}); identity.ErrorCodeOf(err) != identity.CodeInvalidArgument {
		t.Fatal("group cursor ignored filter", err)
	}
	relation, err := s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Join inspected group", "join", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: library.Items[0].ID, MembershipID: alice.MembershipID, Reason: "Explicit specialist grant"}, "library"); err != nil {
		t.Fatal(err)
	}
	groupGrant, err := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: roles.Items[0].ID, GroupID: group.ID, NamespaceID: authorizationCoreNamespace, Reason: "Exact group path"}, "group-grant")
	if err != nil {
		t.Fatal(err)
	}
	var auditsBefore, auditsAfter int
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM audit_events`).Scan(&auditsBefore); err != nil {
		t.Fatal(err)
	}
	page, err := s.MemberAccess(ctx, alice.MembershipID, 1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" || page.GroupCoverage != "current_realm" {
		t.Fatal(page, err)
	}
	if _, err := s.MemberAccess(ctx, admin.MembershipID, 1, page.NextCursor); identity.ErrorCodeOf(err) != identity.CodeInvalidArgument {
		t.Fatal("contribution cursor escaped target", err)
	}
	full, err := s.MemberAccess(ctx, alice.MembershipID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range full.Items {
		if item.Assignment.ID == groupGrant.ID {
			found = true
			if !item.Effective || item.Group == nil || item.Group.ID != group.ID || item.Relation == nil || item.Relation.ID != relation.ID || item.Role.ID != roles.Items[0].ID {
				t.Fatal("group contribution lost references", item)
			}
		}
	}
	if !found {
		t.Fatal("missing group path")
	}
	if _, err := s.MemberAccess(identity.WithActor(t.Context(), alice), alice.MembershipID, 25, ""); identity.ErrorCodeOf(err) != identity.CodeForbidden {
		t.Fatal("recipient acquired administration", err)
	}
	readOwnCaptions := func() (map[string]string, int64) {
		t.Helper()
		captions, cursor := map[string]string{}, ""
		var revision int64
		for range 10 {
			page, err := s.CurrentAccessAssignments(identity.WithActor(t.Context(), alice), 1, cursor)
			if err != nil {
				t.Fatal(err)
			}
			if revision != 0 && revision != page.AuthorizationRevision {
				t.Fatal("self caption pages crossed policy revisions")
			}
			revision = page.AuthorizationRevision
			for _, item := range page.Items {
				if _, exists := captions[item.Assignment.ID]; exists || item.Assignment.TenantID != alice.TenantID || item.Assignment.RealmID != alice.RealmID || item.Assignment.GroupID == "" && item.GroupName != "" {
					t.Fatal("self captions duplicated or escaped exact paths", item)
				}
				captions[item.Assignment.ID] = item.GroupName
			}
			if page.NextCursor == "" {
				return captions, revision
			}
			cursor = page.NextCursor
		}
		t.Fatal("self caption fixture exceeded its bounded expected population")
		return nil, 0
	}
	selfCaptions, selfRevision := readOwnCaptions()
	if selfCaptions[groupGrant.ID] != group.Name || selfRevision != full.AuthorizationRevision {
		t.Fatal("recipient did not receive its current group caption", selfCaptions)
	}
	if _, err := s.GetAccessGroup(identity.WithActor(t.Context(), alice), group.ID); identity.ErrorCodeOf(err) != identity.CodeForbidden {
		t.Fatal("self caption granted protected group access", err)
	}
	if _, err := s.MemberAccess(ctx, "00000000-0000-7000-8000-000000009999", 25, ""); identity.ErrorCodeOf(err) != identity.CodeNotFound {
		t.Fatal("missing target disclosed existence", err)
	}
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM audit_events`).Scan(&auditsAfter); err != nil || auditsBefore != auditsAfter {
		t.Fatal("read committed evidence", auditsBefore, auditsAfter, err)
	}
	revision := full.AuthorizationRevision
	group, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Name: "Renamed without authority change", Reason: "Cosmetic caption refresh"}, "", "rename")
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := s.MemberAccess(ctx, alice.MembershipID, 100, "")
	if err != nil || renamed.AuthorizationRevision != revision {
		t.Fatal("cosmetic name changed authority", renamed, err)
	}
	for _, item := range renamed.Items {
		if item.Assignment.ID == groupGrant.ID && (item.Group == nil || item.Group.Name != group.Name) {
			t.Fatal("caption remained stale", item)
		}
	}
	selfCaptions, selfRevision = readOwnCaptions()
	if selfCaptions[groupGrant.ID] != group.Name || selfRevision != revision {
		t.Fatal("self caption stayed stale after cosmetic metadata update", selfCaptions)
	}
	_, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Suspend retained paths"}, identity.GroupSuspended, "suspend")
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := s.MemberAccess(ctx, alice.MembershipID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range suspended.Items {
		if item.Assignment.ID == groupGrant.ID && (item.Effective || item.Relation == nil || item.Relation.State != identity.AssignmentActive) {
			t.Fatal("suspension erased relation or retained authority", item)
		}
	}
	selfCaptions, _ = readOwnCaptions()
	if _, exists := selfCaptions[groupGrant.ID]; exists {
		t.Fatal("suspended group's caption remained an effective self path")
	}
	if _, err := s.SaveAccessGroup(ctx, groupTwo.ID, groupTwo.Revision, identity.GroupInput{Reason: "Archive empty control"}, identity.GroupArchived, "archive-empty"); err != nil {
		t.Fatal(err)
	}
	archived, err := s.ListAccessGroups(ctx, 25, "", identity.DirectoryFilter{State: "archived"})
	if err != nil || len(archived.Items) != 1 || archived.Items[0].ID != groupTwo.ID {
		t.Fatal("state filter mixed archived and suspended groups", archived, err)
	}
	// Equal subject text in another same-tenant realm is another principal.
	input := developmentProvisionInput(identity.Seed{TenantID: admin.TenantID, Subject: alice.Subject, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	input.Issuer = "https://other-directory.example"
	foreign, err := ProvisionIdentity(t.Context(), db.operator, input)
	if err != nil {
		t.Fatal(err)
	}
	foreignActor, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: admin.TenantID, Issuer: input.Issuer, Subject: alice.Subject})
	if err != nil {
		t.Fatal(err)
	}
	limited, err := s.MemberAccess(identity.WithActor(t.Context(), foreignActor), alice.MembershipID, 100, "")
	if err != nil || limited.GroupCoverage != "unavailable" || limited.Membership.ID != alice.MembershipID {
		t.Fatal("foreign realm coverage was not explicit", limited, err)
	}
	for _, item := range limited.Items {
		if item.Assignment.GroupID != "" || item.Group != nil || item.Relation != nil {
			t.Fatal("foreign group metadata disclosed", item)
		}
	}
	exact, err := s.ListMemberships(ctx, 1, "", identity.MembershipFilter{Subject: alice.Subject, RealmID: foreign.RealmID})
	if err != nil || len(exact.Items) != 1 || exact.Items[0].ID != foreign.MembershipID || exact.NextCursor != "" {
		t.Fatal("exact realm filter merged equal subjects", exact, err)
	}
	input.TenantID = housekeepingTenantB
	input.TenantDisplayName = "Housekeeping tenant B"
	input.Subject = "other-tenant-member"
	input.Issuer = "https://other-tenant-directory.example"
	other, err := ProvisionIdentity(t.Context(), db.operator, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MemberAccess(ctx, other.MembershipID, 25, ""); identity.ErrorCodeOf(err) != identity.CodeNotFound {
		t.Fatal("cross-tenant target disclosed", err)
	}
	otherPage, err := s.ListMemberships(ctx, 25, "", identity.MembershipFilter{RealmID: other.RealmID})
	if err != nil || len(otherPage.Items) != 0 {
		t.Fatal("realm filter escaped tenant", otherPage, err)
	}
}
