package identity

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
)

const administrationTenant = "00000000-0000-7000-8000-000000000001"
const administrationNamespace = "aaaaaaaa-0000-7000-8000-000000000010"
const administrationOtherNamespace = "aaaaaaaa-0000-7000-8000-000000000011"

func TestAdministrationAuditRejectsMismatchedEvidenceAndNotifications(t *testing.T) {
	_, service, admin, _, _ := administrationFixture(t)
	change, err := service.administrationChange(WithActor(t.Context(), admin), "", 0, "Reviewed private administration", "authorization.role.created", "role_definition", "request")
	if err != nil {
		t.Fatal(err)
	}
	for _, trial := range []struct {
		name   string
		mutate func(*AdministrationChange)
	}{
		{"action", func(c *AdministrationChange) { c.Evidence.Action = "authorization.role.updated" }},
		{"target type", func(c *AdministrationChange) { c.Evidence.TargetType = "group" }},
		{"target identity", func(c *AdministrationChange) { c.Evidence.TargetID = administrationNamespace }},
		{"principal", func(c *AdministrationChange) { c.Evidence.Actor = "different" }},
		{"time", func(c *AdministrationChange) { c.Evidence.At = c.At.Add(time.Second) }},
		{"realm", func(c *AdministrationChange) { c.Evidence.Details["realmId"] = "different" }},
		{"actor type", func(c *AdministrationChange) { c.Evidence.Details["actorType"] = "service" }},
		{"notification id", func(c *AdministrationChange) { c.Evidence.OutboxID = c.ID }},
		{"notification type", func(c *AdministrationChange) { c.Evidence.OutboxType = "invented" }},
		{"notification subject", func(c *AdministrationChange) { c.Evidence.OutboxSubject = "private" }},
		{"notification data", func(c *AdministrationChange) { c.Evidence.OutboxData = []byte(`{}`) }},
		{"notification time", func(c *AdministrationChange) { c.Evidence.OutboxPublished = &c.At }},
		{"invalid audit", func(c *AdministrationChange) { c.Evidence.AuditID = "invalid" }},
	} {
		t.Run(trial.name, func(t *testing.T) {
			candidate := change
			candidate.Evidence.Details = maps.Clone(change.Evidence.Details)
			trial.mutate(&candidate)
			if _, err := AdministrationRecord(candidate, "authorization.role.created", "role_definition", nil); ErrorCodeOf(err) != CodeInternal {
				t.Fatal("unbound audit or notification accepted", err)
			}
		})
	}
	record, err := AdministrationRecord(change, "authorization.role.created", "role_definition", map[string]any{"roleName": "Reviewed"})
	if err != nil || record.Actor != admin.MembershipID || record.RequestID != "request" || record.TargetID != change.ID {
		t.Fatal("valid bound audit rejected", err)
	}
	record.Details["realmId"] = "detached"
	if change.Evidence.Details["realmId"] != admin.RealmID {
		t.Fatal("record projection altered retained evidence")
	}
}

func administrationFixture(t *testing.T) (*MemoryStore, *Service, Actor, Actor, *recordingIdentityEvidenceSink) {
	t.Helper()
	sink := &recordingIdentityEvidenceSink{}
	store, err := NewMemoryStore([]Seed{{TenantID: administrationTenant, Issuer: "issuer", Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}}, {TenantID: administrationTenant, Issuer: "issuer", Subject: "alice", ActorType: ActorHuman}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{at: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}, IDs: &identityTestIDs{next: 1000}, CursorKey: identityTestCursorKey})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Resolve(t.Context(), Principal{TenantID: administrationTenant, Issuer: "issuer", Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.ResolvePrincipal(t.Context(), Principal{TenantID: administrationTenant, Issuer: "issuer", Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	return store, service, admin, alice, sink
}
func administrationRole(t *testing.T, s *Service, ctx context.Context, permissions ...Permission) RoleDefinition {
	t.Helper()
	v, err := s.SaveRoleDefinition(ctx, "", 0, RoleDefinitionInput{Name: "Reviewed access", Permissions: permissions, Reason: "Create reviewed role"}, false, "create-role")
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func administrationDecision(t *testing.T, s *Service, a Actor, want ...bool) int64 {
	t.Helper()
	d, err := s.CurrentPermissionDecisions(WithActor(t.Context(), a), []PermissionCheck{{Permission: "resource.read", NamespaceID: administrationNamespace}, {Permission: "resource.read", NamespaceID: administrationOtherNamespace}, {Permission: "resource.edit", NamespaceID: administrationNamespace}})
	if err != nil || !reflect.DeepEqual(d.Allowed, want) {
		t.Fatalf("current decisions=%+v err=%v want=%v", d, err, want)
	}
	return d.Revision
}
func TestCustomPermissionSetValidation(t *testing.T) {
	for _, v := range [][]Permission{nil, {"*"}, {"access.manage"}, {"RESOURCE.READ"}, {"resource.read", "resource.read"}, {"resource.edit"}, {"source.manage", "source.read"}, make([]Permission, 44)} {
		if ErrorCodeOf(func() error { _, err := ValidateCustomPermissions(v); return err }()) != CodeInvalidArgument {
			t.Fatalf("accepted invalid set %v", v)
		}
	}
	var maximum []Permission
	for _, descriptor := range permissionRegistry {
		if descriptor.Delegable {
			maximum = append(maximum, descriptor.Key)
		}
	}
	if len(maximum) != 49 {
		t.Fatalf("delegable registry changed: %d", len(maximum))
	}
	if got, err := ValidateCustomPermissions(maximum); err != nil || len(got) != len(maximum) {
		t.Fatalf("maximum custom permissions = %v, %v", got, err)
	}
	if _, err := ValidateCustomPermissions(append(maximum, "access.manage")); ErrorCodeOf(err) != CodeInvalidArgument || !strings.Contains(err.Error(), "1-49") {
		t.Fatalf("over-capacity diagnostic: %v", err)
	}
	v := []Permission{"resource.edit", "resource.read_content", "resource.read", "resource.read_history"}
	got, err := ValidateCustomPermissions(v)
	if err != nil || !slices.IsSorted(got) || !NamespaceCapable(got) {
		t.Fatalf("valid set %v %v", got, err)
	}
	got[0] = "changed"
	if v[0] != "resource.edit" {
		t.Fatal("caller set mutated")
	}
	if NamespaceCapable([]Permission{"audit.read"}) || NamespaceCapable([]Permission{"invented"}) {
		t.Fatal("tenant permission accepted in namespace")
	}
	for _, name := range []string{"", " padded", "bad\nname", "bad\x00name", strings.Repeat("界", 121), string([]byte{255})} {
		if validateDisplay(name, "") == nil {
			t.Fatalf("invalid name %q", name)
		}
	}
	if validateDisplay(strings.Repeat("界", 120), strings.Repeat("界", 2000)) != nil || validateDisplay("valid", strings.Repeat("界", 2001)) == nil {
		t.Fatal("Unicode display bounds")
	}
}
func TestAuthorizationAdministrationComposesAndRevokesCurrentPaths(t *testing.T) {
	_, s, admin, alice, sink := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	role := administrationRole(t, s, ctx, "resource.read")
	group, err := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Authors", Reason: "Create access group"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Add reviewed member", "member", false)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, NamespaceID: administrationNamespace, Reason: "Grant direct read"}, "direct")
	if err != nil {
		t.Fatal(err)
	}
	grouped, err := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: administrationNamespace, Reason: "Grant group read"}, "grouped")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, NamespaceID: administrationOtherNamespace, Reason: "Grant second scope"}, "other")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []AccessAssignmentInput{{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, NamespaceID: administrationNamespace, Reason: "Duplicate direct"}, {RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: administrationNamespace, Reason: "Duplicate group"}} {
		if _, err := s.GrantAuthorizationAssignment(ctx, input, "duplicate"); ErrorCodeOf(err) != CodeConflict {
			t.Fatalf("duplicate accepted %v", err)
		}
	}
	administrationDecision(t, s, alice, true, true, false)
	_, err = s.RevokeAuthorizationAssignment(ctx, direct.ID, direct.Revision, "Revoke direct path", "revoke")
	if err != nil {
		t.Fatal(err)
	}
	administrationDecision(t, s, alice, true, true, false)
	_, err = s.ChangeAccessGroupMember(ctx, group.ID, member.ID, "", member.Revision, "Remove group path", "remove", true)
	if err != nil {
		t.Fatal(err)
	}
	administrationDecision(t, s, alice, false, true, false)
	// A different namespace remains valid after group removal; exact-set edits
	// change current authority without rewriting assignment history.
	role, err = s.SaveRoleDefinition(ctx, role.ID, role.Revision, RoleDefinitionInput{Name: role.Name, Permissions: []Permission{"namespace.read"}, Reason: "Replace permission set"}, false, "edit")
	if err != nil {
		t.Fatal(err)
	}
	administrationDecision(t, s, alice, false, false, false)
	if _, err := s.SaveRoleDefinition(ctx, role.ID, role.Revision, RoleDefinitionInput{Reason: "Archive assigned role"}, true, "archive"); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("archive active role %v", err)
	}
	if _, err := s.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Archive assigned group"}, GroupArchived, "archive"); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("archive active group %v", err)
	}
	if _, err := s.RevokeAuthorizationAssignment(ctx, grouped.ID, grouped.Revision, "Remove retained grant", "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Archive unused group"}, GroupArchived, "archive"); err != nil {
		t.Fatal(err)
	}
	for _, e := range sink.records {
		if strings.HasPrefix(e.Action, "authorization.") && (e.OutboxID != "" || e.OutboxType != "" || len(e.OutboxData) > 0) {
			t.Fatal("private graph mutation leaked to notifications")
		}
	}
}
func TestAuthorizationAdministrationRollbackPagingAndProtections(t *testing.T) {
	_, s, admin, alice, sink := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	role := administrationRole(t, s, ctx, "resource.read")
	rev := administrationDecision(t, s, alice, false, false, false)
	input := RoleDefinitionInput{Name: "Renamed role", Permissions: []Permission{"resource.read"}, Reason: "Cosmetic rename"}
	role, err := s.SaveRoleDefinition(ctx, role.ID, role.Revision, input, false, "rename")
	if err != nil {
		t.Fatal(err)
	}
	if administrationDecision(t, s, alice, false, false, false) != rev {
		t.Fatal("cosmetic edit invalidated authority")
	}
	sink.err = errors.New("audit unavailable")
	if _, err := s.SaveRoleDefinition(ctx, role.ID, role.Revision, RoleDefinitionInput{Name: role.Name, Permissions: []Permission{"namespace.read"}, Reason: "Reject unaudited edit"}, false, "failure"); err == nil {
		t.Fatal("missing audit accepted")
	}
	sink.err = nil
	stored, err := s.GetRoleDefinition(ctx, role.ID)
	if err != nil || stored.Revision != role.Revision || !slices.Equal(stored.Permissions, role.Permissions) {
		t.Fatal("failed audit changed role")
	}
	if _, err := s.ListRoleDefinitions(WithActor(t.Context(), alice), 20, ""); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("member admin read %v", err)
	}
	page, err := s.ListRoleDefinitions(ctx, 1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatal("first bounded page", err)
	}
	next, err := s.ListRoleDefinitions(ctx, 1, page.NextCursor)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID <= page.Items[0].ID {
		t.Fatal("continuation", err)
	}
	if _, err := s.ListAccessGroups(ctx, 1, page.NextCursor); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("cross-collection cursor accepted", err)
	}
	if _, err := s.ListRoleDefinitions(ctx, 2, page.NextCursor); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("changed limit accepted", err)
	}
	_, err = s.SaveRoleDefinition(ctx, role.ID, role.Revision, RoleDefinitionInput{Name: role.Name, Permissions: []Permission{"namespace.read"}, Reason: "Authority change"}, false, "edit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListRoleDefinitions(ctx, 1, page.NextCursor); err == nil {
		t.Fatal("stale authority cursor accepted")
	}
	all, err := s.ListRoleDefinitions(ctx, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all.Items {
		if d.Key == "tenant_admin" {
			if _, err := s.SaveRoleDefinition(ctx, d.ID, d.Revision, input, false, "builtin"); ErrorCodeOf(err) != CodeConflict {
				t.Fatal("builtin changed", err)
			}
			group, err := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Group", Reason: "Create protected test group"}, "", "group")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: d.ID, GroupID: group.ID, Reason: "Forbidden admin group"}, "grant"); ErrorCodeOf(err) != CodeConflict {
				t.Fatal("group administrator bypass", err)
			}
		}
	}
	if _, err := s.ListAccessGroupMembers(ctx, "", 10, ""); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("unbounded group selector")
	}
}
func TestAuthorizationAdministrationSuspensionOffboardingAndScopeEdits(t *testing.T) {
	_, s, admin, alice, _ := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	role := administrationRole(t, s, ctx, "resource.read")
	group, err := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Retained group", Reason: "Create lifecycle group"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Add lifecycle member", "member", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: administrationNamespace, Reason: "Grant scoped group"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	administrationDecision(t, s, alice, true, false, false)
	group, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Suspend group access"}, GroupSuspended, "suspend")
	if err != nil {
		t.Fatal(err)
	}
	administrationDecision(t, s, alice, false, false, false)
	group, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Reactivate group access"}, GroupActive, "reactivate")
	if err != nil {
		t.Fatal(err)
	}
	administrationDecision(t, s, alice, true, false, false)
	if _, err := s.SaveRoleDefinition(ctx, role.ID, role.Revision, RoleDefinitionInput{Name: role.Name, Permissions: []Permission{"audit.read"}, Reason: "Conflicting tenant scope"}, false, "scope"); ErrorCodeOf(err) != CodeConflict {
		t.Fatal("scope conflict accepted", err)
	}
	result, err := s.SuspendMembership(ctx, alice.MembershipID, "Suspend member", "suspend", 1)
	if err != nil {
		t.Fatal(err)
	}
	administrationDecision(t, s, alice, false, false, false)
	result, err = s.ReactivateMembership(ctx, alice.MembershipID, "Restore member", "reactivate", result.Membership.Revision)
	if err != nil {
		t.Fatal(err)
	}
	administrationDecision(t, s, alice, true, false, false)
	result, err = s.OffboardMembership(ctx, alice.MembershipID, "Offboard member", "offboard", result.Membership.Revision)
	if err != nil || result.RevokedGroupMemberships != 1 {
		t.Fatalf("offboard %+v %v", result, err)
	}
	administrationDecision(t, s, alice, false, false, false)
	members, err := s.ListAccessGroupMembers(ctx, group.ID, 100, "")
	if err != nil || len(members.Items) != 1 || members.Items[0].State != AssignmentRevoked {
		t.Fatal("missing immutable revocation history", err)
	}
}

func TestAuthorizationAdministrationRejectsBackdatedTransitions(t *testing.T) {
	for _, name := range []string{"assignment revoke", "group reactivate"} {
		t.Run(name, func(t *testing.T) {
			store, service, admin, alice, sink := administrationFixture(t)
			ctx := WithActor(t.Context(), admin)
			role := administrationRole(t, service, ctx, "resource.read")
			group, err := service.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Clock boundary", Reason: "Create clock control"}, "", "create")
			if err != nil {
				t.Fatal(err)
			}
			at := service.clock.Now().Add(time.Second)
			service.clock = identityTestClock{at: at}
			group, err = service.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Suspend clock control"}, GroupSuspended, "suspend")
			if err != nil {
				t.Fatal(err)
			}
			assignment, err := service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, Reason: "Grant clock control"}, "grant")
			if err != nil {
				t.Fatal(err)
			}
			// Detached snapshots catch changed row fields and evidence, not only counts.
			snapshot := func() string {
				t.Helper()
				value, err := json.Marshal([]any{store.definitions, store.groups, store.groupMembers, store.accessAssignments, store.revisions, store.audit, sink.records, sink.tenantIDs})
				if err != nil {
					t.Fatal(err)
				}
				return string(value)
			}
			before := snapshot()
			service.clock = identityTestClock{at: at.Add(-time.Microsecond)}
			if name == "assignment revoke" {
				_, err = service.RevokeAuthorizationAssignment(ctx, assignment.ID, assignment.Revision, "Reject backdated revoke", "backdated")
			} else {
				_, err = service.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Reject backdated reactivation"}, GroupActive, "backdated")
			}
			if ErrorCodeOf(err) != CodeConflict || snapshot() != before {
				t.Fatalf("backdated transition must conflict without changing policy or evidence: %v", err)
			}
			// The same legal transition at the retained timestamp must succeed.
			service.clock = identityTestClock{at: at}
			revision, records := store.revisions[admin.TenantID], len(sink.records)
			if name == "assignment revoke" {
				value, err := service.RevokeAuthorizationAssignment(ctx, assignment.ID, assignment.Revision, "Revoke at grant time", "boundary")
				if err != nil || value.State != AssignmentRevoked || value.Revision != assignment.Revision+1 || value.RevokedAt == nil || !value.RevokedAt.Equal(at) {
					t.Fatalf("equal-time revoke: %+v, %v", value, err)
				}
			} else {
				value, err := service.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Reactivate at suspension time"}, GroupActive, "boundary")
				if err != nil || value.State != GroupActive || value.Revision != group.Revision+1 || !value.UpdatedAt.Equal(at) {
					t.Fatalf("equal-time reactivation: %+v, %v", value, err)
				}
			}
			if store.revisions[admin.TenantID] != revision+1 || len(sink.records) != records+1 || !sink.records[records].At.Equal(at) || sink.records[records].RequestID != "boundary" {
				t.Fatal("successful boundary transition must advance policy once with matching audit time")
			}
		})
	}
}

func TestAuthorizationAdministrationEveryMutationRollsBackFailedEvidence(t *testing.T) {
	for _, name := range []string{"create role", "create group", "add member", "grant", "revoke member", "revoke assignment", "suspend group", "archive role"} {
		t.Run(name, func(t *testing.T) {
			store, s, admin, alice, sink := administrationFixture(t)
			ctx := WithActor(t.Context(), admin)
			role := administrationRole(t, s, ctx, "resource.read")
			group, err := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Atomic group", Reason: "Create atomic group"}, "", "create")
			if err != nil {
				t.Fatal(err)
			}
			member, err := s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Add atomic member", "add", false)
			if err != nil {
				t.Fatal(err)
			}
			assignment, err := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, Reason: "Grant atomic assignment"}, "grant")
			if err != nil {
				t.Fatal(err)
			}
			unused := administrationRole(t, s, ctx, "namespace.read")
			rev := store.revisions[admin.TenantID]
			counts := []int{len(store.definitions), len(store.groups), len(store.groupMembers), len(store.accessAssignments), len(store.audit), len(sink.records)}
			sink.err = errors.New("private sink unavailable")
			switch name {
			case "create role":
				_, err = s.SaveRoleDefinition(ctx, "", 0, RoleDefinitionInput{Name: "Failure", Permissions: []Permission{"resource.read"}, Reason: "Reject failed evidence"}, false, "failure")
			case "create group":
				_, err = s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Failure", Reason: "Reject failed evidence"}, "", "failure")
			case "add member":
				_, err = s.ChangeAccessGroupMember(ctx, group.ID, "", admin.MembershipID, 0, "Reject failed evidence", "failure", false)
			case "grant":
				_, err = s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, Reason: "Reject failed evidence"}, "failure")
			case "revoke member":
				_, err = s.ChangeAccessGroupMember(ctx, group.ID, member.ID, "", member.Revision, "Reject failed evidence", "failure", true)
			case "revoke assignment":
				_, err = s.RevokeAuthorizationAssignment(ctx, assignment.ID, assignment.Revision, "Reject failed evidence", "failure")
			case "suspend group":
				_, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Reject failed evidence"}, GroupSuspended, "failure")
			case "archive role":
				_, err = s.SaveRoleDefinition(ctx, unused.ID, unused.Revision, RoleDefinitionInput{Reason: "Reject failed evidence"}, true, "failure")
			}
			if err == nil {
				t.Fatal("unaudited mutation accepted")
			}
			if store.revisions[admin.TenantID] != rev || !reflect.DeepEqual(counts, []int{len(store.definitions), len(store.groups), len(store.groupMembers), len(store.accessAssignments), len(store.audit), len(sink.records)}) || store.groups[group.ID].Revision != 1 || store.groupMembers[member.ID].State != AssignmentActive || store.accessAssignments[assignment.ID].State != AssignmentActive || store.definitions[unused.ID].State != DefinitionActive {
				t.Fatal("failed evidence changed retained state")
			}
		})
	}
}
func TestAuthorizationAdministrationProtectsCurrentAdministratorsAndTerminalHistory(t *testing.T) {
	store, s, admin, alice, _ := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	all, err := s.ListRoleDefinitions(ctx, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	adminRole := ""
	for _, d := range all.Items {
		if d.Key == "tenant_admin" {
			adminRole = d.ID
		}
	}
	assignments, err := s.ListAuthorizationAssignments(ctx, AuthorizationPageRequest{RoleDefinitionID: adminRole}, 100, "")
	if err != nil || len(assignments.Items) != 1 {
		t.Fatal(err)
	}
	if _, err := s.RevokeAuthorizationAssignment(ctx, assignments.Items[0].ID, 1, "Keep last administrator", "last"); ErrorCodeOf(err) != CodeSeparationOfDuties {
		t.Fatal("last administrator removed", err)
	}
	added, err := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: adminRole, MembershipID: alice.MembershipID, Reason: "Add second direct administrator"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	aliceCtx := WithActor(t.Context(), alice)
	if _, err := s.ListAccessGroups(aliceCtx, 1, ""); err != nil {
		t.Fatal("current direct admin not recognized", err)
	}
	if _, err := s.GrantRole(ctx, GrantRoleInput{Subject: alice.Subject, ActorType: ActorHuman, Role: RoleTenantAdmin, Reason: "Duplicate across old and new paths"}, "duplicate"); ErrorCodeOf(err) != CodeConflict {
		t.Fatal("duplicate logical assignment accepted", err)
	}
	revoked, err := s.RevokeAuthorizationAssignment(ctx, added.ID, added.Revision, "Revoke temporary administrator", "revoke")
	if err != nil {
		t.Fatal(err)
	}
	*revoked.RevokedAt = time.Time{} // Repository outputs must not mutate history.
	page, err := s.ListAuthorizationAssignments(ctx, AuthorizationPageRequest{MembershipID: alice.MembershipID}, 100, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].RevokedAt.IsZero() {
		t.Fatal("returned timestamp aliases stored history", err)
	}
	if err := s.WithAdministrationDisclosure(aliceCtx, func(context.Context) error { t.Fatal("revoked caller received private response"); return nil }); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("stale current authority", err)
	}
	if _, err := s.SaveAccessGroup(aliceCtx, "", 0, GroupInput{Name: "Forbidden", Reason: "Stale administrator"}, "", "stale"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("stale writer admitted", err)
	}
	group, err := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Terminal group", Reason: "Create terminal group"}, "", "create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAccessGroup(ctx, group.ID, 99, GroupInput{Name: group.Name, Reason: "Stale group revision"}, "", "stale"); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatal("stale group edit", err)
	}
	member, err := s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Add history member", "add", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Duplicate member", "duplicate", false); ErrorCodeOf(err) != CodeConflict {
		t.Fatal("duplicate member", err)
	}
	revokedMember, err := s.ChangeAccessGroupMember(ctx, group.ID, member.ID, "", 1, "Remove history member", "remove", true)
	if err != nil {
		t.Fatal(err)
	}
	*revokedMember.RevokedAt = time.Time{}
	members, err := s.ListAccessGroupMembers(ctx, group.ID, 100, "")
	if err != nil || members.Items[0].RevokedAt.IsZero() {
		t.Fatal("returned group timestamp aliases history")
	}
	group, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Archive terminal group"}, GroupArchived, "archive")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Forbidden reactivation"}, GroupActive, "reactivate"); ErrorCodeOf(err) != CodeConflict {
		t.Fatal("archived group reactivated", err)
	}
	role := administrationRole(t, s, ctx, "resource.read")
	role, err = s.SaveRoleDefinition(ctx, role.ID, role.Revision, RoleDefinitionInput{Reason: "Archive unused role"}, true, "archive")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: admin.MembershipID, Reason: "Archived role grant"}, "grant"); ErrorCodeOf(err) != CodeConflict {
		t.Fatal("archived role reused", err)
	}
	foreign := admin
	foreign.TenantID = "00000000-0000-7000-8000-000000000002"
	if _, err := store.GetDefinition(WithActor(t.Context(), foreign), admin, role.ID); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("actor binding crossed tenant", err)
	}
}

func TestAuthorizationAdministrationReadViewsAndOwnSummary(t *testing.T) {
	store, s, admin, alice, _ := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	role := administrationRole(t, s, ctx, "resource.read")
	first, err := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "First team", Reason: "Create first team"}, "", "first")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Second team", Reason: "Create second team"}, "", "second")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAccessGroup(ctx, first.ID)
	if err != nil || got.ID != first.ID || got.Revision != 1 {
		t.Fatal("group exact read", err)
	}
	page, err := s.ListAccessGroups(ctx, 1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatal("group page", err)
	}
	next, err := s.ListAccessGroups(ctx, 1, page.NextCursor)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID <= page.Items[0].ID {
		t.Fatal("group continuation", err)
	}
	_, err = s.ChangeAccessGroupMember(ctx, first.ID, "", alice.MembershipID, 0, "Join team", "join", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ChangeAccessGroupMember(ctx, first.ID, "", admin.MembershipID, 0, "Join administration team", "join-admin", false)
	if err != nil {
		t.Fatal(err)
	}
	members, err := s.ListAccessGroupMembers(ctx, first.ID, 1, "")
	if err != nil || len(members.Items) != 1 || members.NextCursor == "" {
		t.Fatal("bounded group-member page", members, err)
	}
	remainingMembers, err := s.ListAccessGroupMembers(ctx, first.ID, 1, members.NextCursor)
	if err != nil || len(remainingMembers.Items) != 1 || remainingMembers.NextCursor != "" || remainingMembers.Items[0].ID <= members.Items[0].ID || remainingMembers.Items[0].MembershipID == members.Items[0].MembershipID {
		t.Fatal("group-member continuation duplicated or skipped a member", remainingMembers, err)
	}
	assignmentIDs := make([]string, 0, 3)
	for _, input := range []AccessAssignmentInput{{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, NamespaceID: administrationNamespace, Reason: "Direct first scope"}, {RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, NamespaceID: administrationOtherNamespace, Reason: "Direct other scope"}, {RoleDefinitionID: role.ID, GroupID: first.ID, Reason: "Group tenant read"}} {
		assignment, err := s.GrantAuthorizationAssignment(ctx, input, "grant")
		if err != nil {
			t.Fatal(err)
		}
		assignmentIDs = append(assignmentIDs, assignment.ID)
	}
	slices.Sort(assignmentIDs)
	request := AuthorizationPageRequest{RoleDefinitionID: role.ID}
	assignments, err := s.ListAuthorizationAssignments(ctx, request, 2, "")
	if err != nil || len(assignments.Items) != 2 || assignments.NextCursor == "" || assignments.Items[0].ID != assignmentIDs[0] || assignments.Items[1].ID != assignmentIDs[1] {
		t.Fatal("bounded role assignment page", assignments, err)
	}
	remainingAssignments, err := s.ListAuthorizationAssignments(ctx, request, 2, assignments.NextCursor)
	if err != nil || len(remainingAssignments.Items) != 1 || remainingAssignments.Items[0].ID != assignmentIDs[2] || remainingAssignments.NextCursor != "" {
		t.Fatal("role assignment continuation duplicated or skipped authority", remainingAssignments, err)
	}
	own, err := s.CurrentAccessAssignments(WithActor(t.Context(), alice), 1, "")
	if err != nil || len(own.Items) != 1 || own.NextCursor == "" {
		t.Fatal("own bounded summary", err)
	}
	second, err := s.CurrentAccessAssignments(WithActor(t.Context(), alice), 1, own.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].Assignment.ID <= own.Items[0].Assignment.ID {
		t.Fatal("own continuation", err)
	}
	if _, err := s.CurrentAccessAssignments(ctx, 1, own.NextCursor); err == nil {
		t.Fatal("another caller reused private cursor")
	}
	full, err := s.CurrentAccessAssignments(WithActor(t.Context(), alice), 100, "")
	if err != nil || len(full.Items) != 3 {
		t.Fatal("own direct and group paths", err)
	}
	for _, v := range full.Items {
		if v.Role.ID != role.ID || !slices.Equal(v.Role.Permissions, []Permission{"resource.read"}) || v.Assignment.MembershipID != "" && v.Assignment.MembershipID != alice.MembershipID {
			t.Fatal("own summary disclosed wrong path")
		}
		if v.Assignment.GroupID != "" && v.GroupName != first.Name || v.Assignment.GroupID == "" && v.GroupName != "" {
			t.Fatal("self access lost the selected group caption or mislabeled a direct path", v)
		}
	}
	full.Items[0].Role.Permissions[0] = "access.manage"
	administrationDecision(t, s, alice, true, true, false)
	// Cosmetic changes need fresh captions even without a policy change, and
	// the recipient must not acquire protected group-directory access for them.
	renamed, err := s.SaveAccessGroup(ctx, first.ID, first.Revision, GroupInput{Name: "Renamed visible team", Reason: "Refresh the recipient caption"}, "", "rename")
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := s.CurrentAccessAssignments(WithActor(t.Context(), alice), 100, "")
	if err != nil || refreshed.AuthorizationRevision != full.AuthorizationRevision {
		t.Fatal("cosmetic group update invalidated policy or failed self refresh", err)
	}
	for _, item := range refreshed.Items {
		if item.Assignment.GroupID == first.ID && item.GroupName != renamed.Name {
			t.Fatal("self caption remained stale", item)
		}
	}
	if _, err := s.GetAccessGroup(WithActor(t.Context(), alice), first.ID); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("self caption added group-directory authority", err)
	}
	var emitted bool
	if err := s.WithAdministrationDisclosure(ctx, func(admitted context.Context) error {
		v, err := store.GetGroup(admitted, admin, first.ID)
		if err != nil || v.ID != first.ID {
			t.Fatal("read inside shared admission", err)
		}
		_, err = store.SaveGroup(admitted, SaveGroupParams{AdministrationChange: AdministrationChange{Actor: admin}})
		if ErrorCodeOf(err) != CodeConflict {
			t.Fatal("shared lock upgraded", err)
		}
		emitted = true
		return nil
	}); err != nil || !emitted {
		t.Fatal("current administration disclosure", err)
	}
	for _, id := range []string{"invalid", "00000000-0000-7000-8000-000000009999"} {
		_, err := s.GetAccessGroup(ctx, id)
		if err == nil {
			t.Fatal("invalid group read")
		}
		_, err = s.GetRoleDefinition(ctx, id)
		if err == nil {
			t.Fatal("invalid role read")
		}
	}
}

type administrationLegacyStore struct{ Store }

func TestAuthorizationAdministrationInputAndAdapterFailures(t *testing.T) {
	store, s, admin, _, _ := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	for _, r := range []AuthorizationPageRequest{{Limit: 0, AuthorizationRevision: 1}, {Limit: 102, AuthorizationRevision: 1}, {Limit: 1, AuthorizationRevision: 0}, {Limit: 1, AuthorizationRevision: 1, AfterID: "bad"}} {
		if ErrorCodeOf(ValidateAuthorizationPage(r)) != CodeInvalidArgument {
			t.Fatal("invalid port page accepted")
		}
	}
	if _, err := s.CurrentPermissionDecisions(t.Context(), []PermissionCheck{{Permission: "resource.read"}}); ErrorCodeOf(err) != CodeUnauthenticated {
		t.Fatal("anonymous capability lookup", err)
	}
	if _, err := s.ListAccessGroups(t.Context(), 1, ""); ErrorCodeOf(err) != CodeUnauthenticated {
		t.Fatal("anonymous administration lookup", err)
	}
	if _, err := s.ListRoleDefinitions(ctx, 101, ""); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("unbounded API page", err)
	}
	if _, err := s.SaveRoleDefinition(ctx, "", 0, RoleDefinitionInput{Reason: "Archive nonexistent"}, true, "archive"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("archive missing ID", err)
	}
	if _, err := s.SaveRoleDefinition(ctx, "", 0, RoleDefinitionInput{Name: "Role", Permissions: []Permission{"resource.edit"}, Reason: "Missing companions"}, false, "bad"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("invalid permission set", err)
	}
	if _, err := s.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "", Reason: "Invalid group"}, "", "bad"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("invalid group display", err)
	}
	for _, input := range []AccessAssignmentInput{{RoleDefinitionID: "bad", MembershipID: admin.MembershipID}, {RoleDefinitionID: admin.MembershipID}, {RoleDefinitionID: admin.MembershipID, MembershipID: admin.MembershipID, GroupID: admin.MembershipID}, {RoleDefinitionID: admin.MembershipID, MembershipID: admin.MembershipID, NamespaceID: "bad"}} {
		if _, err := s.GrantAuthorizationAssignment(ctx, input, "bad"); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal("invalid assignment input", err)
		}
	}
	if _, err := s.RevokeAuthorizationAssignment(ctx, "", 1, "Missing identifier", "bad"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("missing revocation ID")
	}
	if _, err := s.ChangeAccessGroupMember(ctx, admin.MembershipID, admin.MembershipID, admin.MembershipID, 1, "Invalid addition revision", "bad", false); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("add accepts retained relation identity")
	}
	legacy, err := NewService(ServiceOptions{Store: administrationLegacyStore{Store: store}, Clock: identityTestClock{at: time.Now()}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.CurrentPermissionDecisions(ctx, []PermissionCheck{{Permission: "access.manage"}}); ErrorCodeOf(err) != CodeDependency {
		t.Fatal("missing administration adapter", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.GetGroup(canceled, admin, admin.MembershipID); ErrorCodeOf(err) != CodeDependency {
		t.Fatal("cancelled store read", err)
	}
	if _, err := store.SaveGroup(canceled, SaveGroupParams{AdministrationChange: AdministrationChange{Actor: admin}}); ErrorCodeOf(err) != CodeDependency {
		t.Fatal("cancelled store write", err)
	}
}

func TestAdministrationRevokesOriginalTemplateAssignmentConsistently(t *testing.T) {
	_, service, admin, alice, sink := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	grant, err := service.GrantRole(ctx, GrantRoleInput{Subject: alice.Subject, ActorType: ActorHuman, Role: RoleReader, Reason: "Grant retained-content read"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	beforeRevision := administrationDecision(t, service, alice, true, true, false)
	beforeEvidence := len(sink.records)
	revoked, err := service.RevokeAuthorizationAssignment(ctx, grant.ID, grant.Revision, "Remove retained-content read", "revoke")
	if err != nil || revoked.ID != grant.ID || revoked.State != AssignmentRevoked || revoked.Revision != grant.Revision+1 || revoked.RevokedAt == nil || revoked.RevokedBy != admin.Subject || revoked.RevokeReason != "Remove retained-content read" {
		t.Fatalf("original assignment revocation lost: %+v %v", revoked, err)
	}
	afterRevision := administrationDecision(t, service, alice, false, false, false)
	if afterRevision != beforeRevision+1 || len(sink.records) != beforeEvidence+1 || sink.records[beforeEvidence].Action != "authorization.assignment.revoked" {
		t.Fatal("effective policy or required evidence did not advance exactly once")
	}
	original, err := service.ListRoleAssignments(ctx, RoleAssignmentListRequest{MembershipID: alice.MembershipID, Limit: 100})
	if err != nil || len(original) != 1 || original[0].ID != revoked.ID || original[0].State != revoked.State || original[0].Revision != revoked.Revision || original[0].RevokedAt == nil || !original[0].RevokedAt.Equal(*revoked.RevokedAt) {
		t.Fatal("original assignment view did not retain the same revocation", original, err)
	}
	current, err := service.ListAuthorizationAssignments(ctx, AuthorizationPageRequest{MembershipID: alice.MembershipID}, 100, "")
	if err != nil || len(current.Items) != 1 || current.Items[0].ID != revoked.ID || current.Items[0].State != AssignmentRevoked || current.Items[0].Revision != revoked.Revision {
		t.Fatal("administration view disagrees with original assignment", current, err)
	}
}

func TestAdministrationChangeUsesCurrentPolicyDigest(t *testing.T) {
	store, service, admin, _, _ := administrationFixture(t)
	ctx := WithAdministration(WithActor(t.Context(), admin))
	decision, err := store.CheckPermissions(ctx, admin, []PermissionCheck{{Permission: "access.manage"}})
	if err != nil {
		t.Fatal(err)
	}
	SetPermissionRevision(ctx, decision.Revision)
	expected := DecisionDigest(ctx)
	change, err := service.administrationChange(ctx, "", 0, "Qualify current decision evidence", "authorization.role.created", "role_definition", "request")
	if err != nil || change.Evidence.Details["authorizationDigest"] != expected {
		t.Fatal("administration evidence omitted selected policy", change.Evidence.Details, err)
	}
}

func TestAdministrationIdentifierFailureLeavesAuthorityAndEvidenceUnchanged(t *testing.T) {
	for _, trial := range []struct {
		name   string
		failAt int
		update bool
	}{
		{"new object identifier", 1, false},
		{"new object audit identifier", 2, false},
		{"existing object audit identifier", 1, true},
	} {
		t.Run(trial.name, func(t *testing.T) {
			store, service, admin, _, sink := administrationFixture(t)
			ctx := WithActor(t.Context(), admin)
			role := administrationRole(t, service, ctx, "resource.read")
			ids := &failingIdentityIDs{failAt: trial.failAt}
			service.ids = ids
			revision, definitionCount, evidenceCount := store.revisions[admin.TenantID], len(store.definitions), len(sink.records)
			key, expected := "", int64(0)
			if trial.update {
				key, expected = role.ID, role.Revision
			}
			_, err := service.SaveRoleDefinition(ctx, key, expected, RoleDefinitionInput{Name: "Changed role", Permissions: []Permission{"namespace.read"}, Reason: "Test unavailable identifiers"}, false, "request")
			if ErrorCodeOf(err) != CodeInternal || ids.next != trial.failAt {
				t.Fatal("identifier failure was not classified", err, ids.next)
			}
			retained, err := service.GetRoleDefinition(ctx, role.ID)
			if err != nil || !reflect.DeepEqual(retained, role) || len(store.definitions) != definitionCount || len(sink.records) != evidenceCount || len(store.audit) != evidenceCount || store.revisions[admin.TenantID] != revision {
				t.Fatal("identifier failure changed authority or evidence", retained, err)
			}
		})
	}
}

func TestAdministrationEvidencePreservesExecutionAttributionAndRejectsInvalidReason(t *testing.T) {
	store, service, admin, _, sink := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	role := administrationRole(t, service, ctx, "resource.read")
	before := len(sink.records)
	if _, err := service.SaveRoleDefinition(ctx, role.ID, role.Revision, RoleDefinitionInput{Name: role.Name, Permissions: role.Permissions}, false, "request"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("missing audit reason accepted", err)
	}
	if len(sink.records) != before || store.definitions[role.ID].Revision != role.Revision {
		t.Fatal("invalid reason changed retained evidence or revision")
	}
	if _, err := service.SaveRoleDefinition(ctx, role.ID, 0, RoleDefinitionInput{Name: role.Name, Permissions: role.Permissions, Reason: "Missing current revision"}, false, "request"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("existing target accepted without a current revision", err)
	}
	if len(sink.records) != before || store.definitions[role.ID].Revision != role.Revision {
		t.Fatal("invalid revision changed retained evidence or revision")
	}
	execution := audit.Execution{RequestID: "command-request", CommandID: administrationOtherNamespace, AuthorizationDigest: "sha256:" + strings.Repeat("a", 64)}
	ctx = audit.WithExecution(ctx, execution)
	change, err := service.administrationChange(ctx, role.ID, role.Revision, "Preserve admitted attribution", "authorization.role.updated", "role_definition", "fallback-request")
	if err != nil || change.Evidence.RequestID != execution.RequestID || change.Evidence.Details["commandId"] != execution.CommandID || change.Evidence.Details["authorizationDigest"] != execution.AuthorizationDigest {
		t.Fatal("command-bound execution was overwritten", change.Evidence, err)
	}
	invalid := audit.WithExecution(ctx, audit.Execution{RequestID: "invalid\nrequest"})
	if _, err := service.administrationChange(invalid, role.ID, role.Revision, "Reject invalid attribution", "authorization.role.updated", "role_definition", "fallback-request"); ErrorCodeOf(err) != CodeInternal {
		t.Fatal("invalid execution attribution accepted", err)
	}
}

func TestAuthorizationMissingTargetsAndStaleRevisionsPreserveState(t *testing.T) {
	for _, name := range []string{
		"missing role read", "missing group read", "missing role update", "missing group update",
		"stale role update", "missing group members", "missing role assignments", "missing group assignments",
		"missing member revoke", "wrong group member revoke", "stale member revoke", "missing membership add",
		"missing assignment revoke", "stale assignment revoke", "missing assignment role", "missing assignment group", "missing assignment membership",
	} {
		t.Run(name, func(t *testing.T) {
			store, service, admin, alice, sink := administrationFixture(t)
			ctx := WithActor(t.Context(), admin)
			role := administrationRole(t, service, ctx, "resource.read")
			group, err := service.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Known group", Reason: "Valid group control"}, "", "create")
			if err != nil {
				t.Fatal(err)
			}
			otherGroup, err := service.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Other group", Reason: "Valid group control"}, "", "create-other")
			if err != nil {
				t.Fatal(err)
			}
			member, err := service.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Valid member control", "add", false)
			if err != nil {
				t.Fatal(err)
			}
			assignment, err := service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, Reason: "Valid assignment control"}, "grant")
			if err != nil {
				t.Fatal(err)
			}
			definitions, groups, members, assignments := maps.Clone(store.definitions), maps.Clone(store.groups), maps.Clone(store.groupMembers), maps.Clone(store.accessAssignments)
			for key, definition := range definitions {
				definition.Permissions = slices.Clone(definition.Permissions)
				definitions[key] = definition
			}
			revision, evidenceCount := store.revisions[admin.TenantID], len(sink.records)
			const missing = "00000000-0000-7000-8000-000000000999"
			want := CodeNotFound
			switch name {
			case "missing role read":
				_, err = service.GetRoleDefinition(ctx, missing)
			case "missing group read":
				_, err = service.GetAccessGroup(ctx, missing)
			case "missing role update":
				_, err = service.SaveRoleDefinition(ctx, missing, 1, RoleDefinitionInput{Name: role.Name, Permissions: role.Permissions, Reason: "Missing target"}, false, "update")
			case "missing group update":
				_, err = service.SaveAccessGroup(ctx, missing, 1, GroupInput{Name: group.Name, Reason: "Missing target"}, "", "update")
			case "stale role update":
				want = CodePreconditionFailed
				_, err = service.SaveRoleDefinition(ctx, role.ID, role.Revision+1, RoleDefinitionInput{Name: role.Name, Permissions: role.Permissions, Reason: "Stale revision"}, false, "update")
			case "missing group members":
				_, err = service.ListAccessGroupMembers(ctx, missing, 20, "")
			case "missing role assignments":
				_, err = service.ListAuthorizationAssignments(ctx, AuthorizationPageRequest{RoleDefinitionID: missing}, 20, "")
			case "missing group assignments":
				_, err = service.ListAuthorizationAssignments(ctx, AuthorizationPageRequest{GroupID: missing}, 20, "")
			case "missing member revoke":
				_, err = service.ChangeAccessGroupMember(ctx, group.ID, missing, "", 1, "Missing target", "revoke", true)
			case "wrong group member revoke":
				_, err = service.ChangeAccessGroupMember(ctx, otherGroup.ID, member.ID, "", member.Revision, "Wrong parent", "revoke", true)
			case "stale member revoke":
				want = CodePreconditionFailed
				_, err = service.ChangeAccessGroupMember(ctx, group.ID, member.ID, "", member.Revision+1, "Stale revision", "revoke", true)
			case "missing membership add":
				_, err = service.ChangeAccessGroupMember(ctx, group.ID, "", missing, 0, "Missing membership", "add", false)
			case "missing assignment revoke":
				_, err = service.RevokeAuthorizationAssignment(ctx, missing, 1, "Missing target", "revoke")
			case "stale assignment revoke":
				want = CodePreconditionFailed
				_, err = service.RevokeAuthorizationAssignment(ctx, assignment.ID, assignment.Revision+1, "Stale revision", "revoke")
			case "missing assignment role":
				_, err = service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: missing, MembershipID: alice.MembershipID, Reason: "Missing role"}, "grant")
			case "missing assignment group":
				_, err = service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: missing, Reason: "Missing group"}, "grant")
			case "missing assignment membership":
				_, err = service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: missing, Reason: "Missing membership"}, "grant")
			}
			if ErrorCodeOf(err) != want {
				t.Fatalf("err=%v want=%s", err, want)
			}
			if !reflect.DeepEqual(store.definitions, definitions) || !reflect.DeepEqual(store.groups, groups) || !reflect.DeepEqual(store.groupMembers, members) || !reflect.DeepEqual(store.accessAssignments, assignments) || store.revisions[admin.TenantID] != revision || len(sink.records) != evidenceCount || len(store.audit) != evidenceCount {
				t.Fatal("rejected operation changed authority or evidence")
			}
		})
	}
}
