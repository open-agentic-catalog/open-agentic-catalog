package identity

import (
	"errors"
	"fmt"
	"testing"
)

func TestNamedRevocationOfGeneralAssignmentRollsBackEvidenceFailure(t *testing.T) {
	store, s, admin, alice, sink := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	definitions, err := s.ListRoleDefinitions(ctx, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range definitions.Items {
		if d.Key != "reader" {
			continue
		}
		a, err := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: d.ID, MembershipID: alice.MembershipID, Reason: "Create general API Reader"}, "grant")
		if err != nil {
			t.Fatal(err)
		}
		sink.err = errors.New("injected evidence failure")
		if _, err = s.RevokeRole(ctx, a.ID, "Refuse partial evidence", "revoke", a.Revision); ErrorCodeOf(err) != CodeDependency || !errors.Is(err, sink.err) {
			t.Fatal("evidence failure accepted")
		}
		kept := store.accessAssignments[a.ID]
		if kept.State != AssignmentActive || kept.Revision != a.Revision || kept.RevokedAt != nil {
			t.Fatal("failed evidence changed grant", kept)
		}
		if _, exists := store.assignments[a.ID]; exists {
			t.Fatal("general assignment was duplicated into legacy storage")
		}
		return
	}
	t.Fatal("Reader template missing")
}

func TestNamedAssignmentsProjectExactTemplatesAndPreserveUnsupportedTargets(t *testing.T) {
	store, s, admin, alice, sink := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	page, err := s.ListRoleDefinitions(ctx, 100, "")
	if err != nil || len(page.Items) != 14 {
		t.Fatal(page, err)
	}
	for _, definition := range page.Items {
		t.Run(definition.Key, func(t *testing.T) {
			a, e := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: definition.ID, MembershipID: alice.MembershipID, Reason: "Qualify exact template projection"}, "grant-"+definition.Key)
			if e != nil {
				t.Fatal(e)
			}
			list, e := s.ListRoleAssignments(ctx, RoleAssignmentListRequest{MembershipID: alice.MembershipID, Limit: 100})
			if e != nil {
				t.Fatal(e)
			}
			eligible := false
			switch definition.Key {
			case "reader", "contributor", "reviewer", "publisher", "source_admin", "security_reviewer", "tenant_admin", "service_client":
				eligible = true
			}
			found := false
			// Retained history remains visible alongside the new active path.
			for _, item := range list {
				found = found || item.ID == a.ID
			}
			before := len(sink.records)
			v, e := s.RevokeRole(ctx, a.ID, "Qualify named revocation", "revoke-"+definition.Key, a.Revision)
			if eligible {
				if !found || e != nil || v.State != AssignmentRevoked || v.Revision != 2 {
					t.Fatal(list, v, e)
				}
			} else {
				if found || ErrorCodeOf(e) != CodeNotFound || len(sink.records) != before {
					t.Fatal("unsupported target changed", list, v, e)
				}
				if store.accessAssignments[a.ID].State != AssignmentActive {
					t.Fatal("unsupported assignment revoked")
				}
				if _, e = s.RevokeAuthorizationAssignment(ctx, a.ID, a.Revision, "Use general assignment API", "general-"+definition.Key); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
	custom := administrationRole(t, s, ctx, "resource.read")
	group, e := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Named projection control", Reason: "Create group"}, "", "group")
	if e != nil {
		t.Fatal(e)
	}
	for i, input := range []AccessAssignmentInput{{RoleDefinitionID: custom.ID, MembershipID: alice.MembershipID}, {RoleDefinitionID: "", GroupID: group.ID}} {
		if i == 1 {
			for _, d := range page.Items {
				if d.Key == "reader" {
					input.RoleDefinitionID = d.ID
				}
			}
		}
		input.Reason = "Qualify unsupported target"
		a, e := s.GrantAuthorizationAssignment(ctx, input, fmt.Sprint("unsupported-", i))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RevokeRole(ctx, a.ID, "Do not mutate unsupported target", fmt.Sprint("refuse-", i), a.Revision); ErrorCodeOf(e) != CodeNotFound {
			t.Fatal(e)
		}
		if store.accessAssignments[a.ID].State != AssignmentActive {
			t.Fatal("unsupported assignment mutated")
		}
	}
}

func TestGroupArchiveRequiresMemberAndAssignmentCleanup(t *testing.T) {
	store, s, admin, alice, sink := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	g, e := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Archive control", Reason: "Create group"}, "", "create")
	if e != nil {
		t.Fatal(e)
	}
	m, e := s.ChangeAccessGroupMember(ctx, g.ID, "", alice.MembershipID, 0, "Add member", "add", false)
	if e != nil {
		t.Fatal(e)
	}
	before := len(sink.records)
	if _, e = s.SaveAccessGroup(ctx, g.ID, g.Revision, GroupInput{Reason: "Refuse active relation"}, GroupArchived, "refuse"); ErrorCodeOf(e) != CodeConflict {
		t.Fatal(e)
	}
	if store.groups[g.ID].State != GroupActive || store.groupMembers[m.ID].State != AssignmentActive || len(sink.records) != before {
		t.Fatal("failed archival changed state/evidence")
	}
	if _, e = s.ChangeAccessGroupMember(ctx, g.ID, m.ID, "", m.Revision, "Explicit member cleanup", "remove", true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveAccessGroup(ctx, g.ID, g.Revision, GroupInput{Reason: "Archive after cleanup"}, GroupArchived, "archive"); e != nil {
		t.Fatal(e)
	}
}
