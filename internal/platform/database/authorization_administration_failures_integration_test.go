//go:build integration

package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Fixed identifiers deliberately collide at the audit INSERT, after legal
// policy writes and their revision triggers have executed in the transaction.
// No schema guard, privilege or production evidence validation is weakened.
type authorizationAuditCollisionIDs struct{ values []string }

func (g *authorizationAuditCollisionIDs) New(time.Time) (string, error) {
	if len(g.values) == 0 {
		return "", errors.New("unexpected authorization fixture identifier request")
	}
	value := g.values[0]
	g.values = g.values[1:]
	return value, nil
}

// Compare exact retained rows rather than counts alone: an update may change
// grants, attribution or timestamps without changing a collection's size.
func authorizationRetainedRows(ctx context.Context, t *testing.T, db *housekeepingDatabase, tenant string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"role_definitions", "role_permissions", "access_groups", "access_group_members", "role_assignments", "tenant_authorization_state", "audit_events", "outbox_events"} {
		var rows string
		if err := db.operator.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb) FROM `+table+` r WHERE tenant_id=$1`, tenant).Scan(&rows); err != nil {
			t.Fatal(table, err)
		}
		result[table] = rows
	}
	return result
}

func TestPostgresAuthorizationEveryMutationRollsBackLateAuditFailure(t *testing.T) {
	for _, trial := range []struct{ name, table string }{
		{"create role", "role_definitions"},
		{"update role", "role_definitions"},
		{"archive role", "role_definitions"},
		{"create group", "access_groups"},
		{"update group", "access_groups"},
		{"suspend group", "access_groups"},
		{"reactivate group", "access_groups"},
		{"archive group", "access_groups"},
		{"add member", "access_group_members"},
		{"revoke member", "access_group_members"},
		{"grant assignment", "role_assignments"},
		{"revoke assignment", "role_assignments"},
	} {
		name := trial.name
		t.Run(name, func(t *testing.T) {
			db, store, _, admin, alice, seed := authorizationCoreFixture(t)
			at := seed.GrantedAt.Add(time.Second)
			options := identity.ServiceOptions{Store: store, Clock: clock.NewFake(at), IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")}
			service, err := identity.NewService(options)
			if err != nil {
				t.Fatal(err)
			}
			ctx := identity.WithActor(t.Context(), admin)
			role, err := service.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Retained role", Permissions: []identity.Permission{"resource.read"}, Reason: "Valid role control"}, false, "role")
			if err != nil {
				t.Fatal(err)
			}
			unused, err := service.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Unused role", Permissions: []identity.Permission{"namespace.read"}, Reason: "Valid archival control"}, false, "unused")
			if err != nil {
				t.Fatal(err)
			}
			group, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Retained group", Reason: "Valid group control"}, "", "group")
			if err != nil {
				t.Fatal(err)
			}
			member, err := service.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Valid member control", "member", false)
			if err != nil {
				t.Fatal(err)
			}
			assignment, err := service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, Reason: "Valid assignment control"}, "assignment")
			if err != nil {
				t.Fatal(err)
			}
			if name == "archive group" {
				if _, err = service.ChangeAccessGroupMember(ctx, group.ID, member.ID, "", member.Revision, "Explicit archival prerequisite cleanup", "cleanup-member", true); err != nil {
					t.Fatal(err)
				}
			}

			if name == "reactivate group" {
				group, err = service.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Valid suspension control"}, identity.GroupSuspended, "suspend")
				if err != nil {
					t.Fatal(err)
				}
			}
			mutate := func(s *identity.Service) error {
				var err error
				switch name {
				case "create role":
					_, err = s.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "New role", Permissions: []identity.Permission{"namespace.read"}, Reason: "Late audit qualification"}, false, "mutation")
				case "update role":
					_, err = s.SaveRoleDefinition(ctx, role.ID, role.Revision, identity.RoleDefinitionInput{Name: "Changed role", Permissions: []identity.Permission{"namespace.read"}, Reason: "Late audit qualification"}, false, "mutation")
				case "archive role":
					_, err = s.SaveRoleDefinition(ctx, unused.ID, unused.Revision, identity.RoleDefinitionInput{Reason: "Late audit qualification"}, true, "mutation")
				case "create group":
					_, err = s.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "New group", Reason: "Late audit qualification"}, "", "mutation")
				case "update group":
					_, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Name: "Changed group", Reason: "Late audit qualification"}, "", "mutation")
				case "suspend group":
					_, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Late audit qualification"}, identity.GroupSuspended, "mutation")
				case "reactivate group":
					_, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Late audit qualification"}, identity.GroupActive, "mutation")
				case "archive group":
					_, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Late audit qualification"}, identity.GroupArchived, "mutation")
				case "add member":
					_, err = s.ChangeAccessGroupMember(ctx, group.ID, "", admin.MembershipID, 0, "Late audit qualification", "mutation", false)
				case "revoke member":
					_, err = s.ChangeAccessGroupMember(ctx, group.ID, member.ID, "", member.Revision, "Late audit qualification", "mutation", true)
				case "grant assignment":
					_, err = s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, Reason: "Late audit qualification"}, "mutation")
				case "revoke assignment":
					_, err = s.RevokeAuthorizationAssignment(ctx, assignment.ID, assignment.Revision, "Late audit qualification", "mutation")
				}
				return err
			}
			var auditID string
			if err := db.operator.QueryRow(t.Context(), `SELECT id::text FROM audit_events WHERE tenant_id=$1 ORDER BY id LIMIT 1`, admin.TenantID).Scan(&auditID); err != nil {
				t.Fatal(err)
			}
			ids := &authorizationAuditCollisionIDs{values: []string{auditID}}
			if name == "create role" || name == "create group" || name == "add member" || name == "grant assignment" {
				newID, err := (id.Generator{}).New(at)
				if err != nil {
					t.Fatal(err)
				}
				ids.values = []string{newID, auditID}
			}
			options.IDs = ids
			faulted, err := identity.NewService(options)
			if err != nil {
				t.Fatal(err)
			}
			before := authorizationRetainedRows(ctx, t, db, admin.TenantID)
			err = mutate(faulted)
			var pgerr *pgconn.PgError
			if identity.ErrorCodeOf(err) != identity.CodeDependency || !errors.As(err, &pgerr) || pgerr.Code != "23505" || pgerr.TableName != "audit_events" || len(ids.values) != 0 {
				t.Fatal("fault did not reach the real late audit INSERT", err, pgerr)
			}
			if after := authorizationRetainedRows(ctx, t, db, admin.TenantID); !reflect.DeepEqual(before, after) {
				t.Fatal("late audit failure committed policy, revision, audit or outbox changes")
			}
			// The exact operation succeeds when the sole injected failure is removed.
			if err := mutate(service); err != nil {
				t.Fatal("valid recovery control failed", err)
			}
			after := authorizationRetainedRows(ctx, t, db, admin.TenantID)
			var oldAudits, newAudits []json.RawMessage
			if err := json.Unmarshal([]byte(before["audit_events"]), &oldAudits); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(after["audit_events"]), &newAudits); err != nil {
				t.Fatal(err)
			}
			if after[trial.table] == before[trial.table] || len(newAudits) != len(oldAudits)+1 || after["outbox_events"] != before["outbox_events"] {
				t.Fatal("successful recovery must change its target table and append one private audit without notification")
			}
		})
	}
}

func TestPostgresAuthorizationRefusalsPreserveExactRetainedState(t *testing.T) {
	db, store, _, admin, alice, seed := authorizationCoreFixture(t)
	// Administration and persisted history use microsecond timestamps.
	serviceClock := clock.NewFake(seed.GrantedAt.Add(time.Second).Truncate(time.Microsecond))
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: serviceClock, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	role, err := service.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Known role", Permissions: []identity.Permission{"resource.read"}, Reason: "Valid refusal control"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	group, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Known group", Reason: "Valid refusal control"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Other group", Reason: "Valid parent control"}, "", "other-group")
	if err != nil {
		t.Fatal(err)
	}
	member, err := service.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Valid refusal control", "member", false)
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, Reason: "Valid refusal control"}, "assignment")
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := service.ListRoleDefinitions(ctx, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	var builtin identity.RoleDefinition
	for _, candidate := range definitions.Items {
		if candidate.Key == "reader" {
			builtin = candidate
		}
	}
	if builtin.ID == "" {
		t.Fatal("built-in refusal control missing")
	}
	terminalRole, err := service.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Terminal role", Permissions: []identity.Permission{"namespace.read"}, Reason: "Valid terminal control"}, false, "terminal-role")
	if err != nil {
		t.Fatal(err)
	}
	terminalRole, err = service.SaveRoleDefinition(ctx, terminalRole.ID, terminalRole.Revision, identity.RoleDefinitionInput{Reason: "Archive terminal control"}, true, "archive-role")
	if err != nil {
		t.Fatal(err)
	}
	terminalGroup, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Terminal group", Reason: "Valid terminal control"}, "", "terminal-group")
	if err != nil {
		t.Fatal(err)
	}
	terminalGroup, err = service.SaveAccessGroup(ctx, terminalGroup.ID, terminalGroup.Revision, identity.GroupInput{Reason: "Archive terminal control"}, identity.GroupArchived, "archive-group")
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Suspended group", Reason: "Valid suspension control"}, "", "suspended-group")
	if err != nil {
		t.Fatal(err)
	}
	suspended, err = service.SaveAccessGroup(ctx, suspended.ID, suspended.Revision, identity.GroupInput{Reason: "Suspend group control"}, identity.GroupSuspended, "suspend-group")
	if err != nil {
		t.Fatal(err)
	}
	terminalMember, err := service.ChangeAccessGroupMember(ctx, group.ID, "", admin.MembershipID, 0, "Valid terminal control", "terminal-member", false)
	if err != nil {
		t.Fatal(err)
	}
	terminalMember, err = service.ChangeAccessGroupMember(ctx, group.ID, terminalMember.ID, "", terminalMember.Revision, "Revoke terminal control", "revoke-member", true)
	if err != nil {
		t.Fatal(err)
	}
	terminalAssignment, err := service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, NamespaceID: authorizationCoreNamespace, Reason: "Valid terminal control"}, "terminal-assignment")
	if err != nil {
		t.Fatal(err)
	}
	terminalAssignment, err = service.RevokeAuthorizationAssignment(ctx, terminalAssignment.ID, terminalAssignment.Revision, "Revoke terminal control", "revoke-assignment")
	if err != nil {
		t.Fatal(err)
	}
	const missing = "00000000-0000-7000-8000-000000000999"
	for _, name := range []string{
		"missing role update", "missing group update", "missing member revoke", "wrong member parent", "missing assignment revoke",
		"stale role update", "stale group update", "stale member revoke", "stale assignment revoke", "built-in update",
		"missing group members", "missing role assignments", "missing group assignments", "missing member add",
		"missing grant role", "missing grant group", "missing grant member", "missing grant namespace", "already active group",
		"archived role update", "archived role grant", "archived group update", "archived group add",
		"suspended group add", "suspended group grant", "terminal member revoke", "terminal assignment revoke",
		"backdated assignment revoke", "backdated group reactivate",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := identity.WithActor(t.Context(), admin)
			before := authorizationRetainedRows(ctx, t, db, admin.TenantID)
			want := identity.CodeNotFound
			var err error
			switch name {
			case "missing role update":
				_, err = service.SaveRoleDefinition(ctx, missing, 1, identity.RoleDefinitionInput{Name: role.Name, Permissions: role.Permissions, Reason: "Missing target"}, false, "update")
			case "missing group update":
				_, err = service.SaveAccessGroup(ctx, missing, 1, identity.GroupInput{Name: group.Name, Reason: "Missing target"}, "", "update")
			case "missing member revoke":
				_, err = service.ChangeAccessGroupMember(ctx, group.ID, missing, "", 1, "Missing target", "revoke", true)
			case "wrong member parent":
				_, err = service.ChangeAccessGroupMember(ctx, other.ID, member.ID, "", member.Revision, "Wrong group parent", "revoke", true)
			case "missing assignment revoke":
				_, err = service.RevokeAuthorizationAssignment(ctx, missing, 1, "Missing target", "revoke")
			case "stale role update":
				want = identity.CodePreconditionFailed
				_, err = service.SaveRoleDefinition(ctx, role.ID, role.Revision+1, identity.RoleDefinitionInput{Name: role.Name, Permissions: role.Permissions, Reason: "Stale revision"}, false, "update")
			case "stale group update":
				want = identity.CodePreconditionFailed
				_, err = service.SaveAccessGroup(ctx, group.ID, group.Revision+1, identity.GroupInput{Name: group.Name, Reason: "Stale revision"}, "", "update")
			case "stale member revoke":
				want = identity.CodePreconditionFailed
				_, err = service.ChangeAccessGroupMember(ctx, group.ID, member.ID, "", member.Revision+1, "Stale revision", "revoke", true)
			case "stale assignment revoke":
				want = identity.CodePreconditionFailed
				_, err = service.RevokeAuthorizationAssignment(ctx, assignment.ID, assignment.Revision+1, "Stale revision", "revoke")
			case "built-in update":
				want = identity.CodeConflict
				_, err = service.SaveRoleDefinition(ctx, builtin.ID, builtin.Revision, identity.RoleDefinitionInput{Name: "Changed builtin", Permissions: []identity.Permission{"resource.read"}, Reason: "Protected builtin"}, false, "update")
			case "missing group members":
				_, err = service.ListAccessGroupMembers(ctx, missing, 20, "")
			case "missing role assignments":
				_, err = service.ListAuthorizationAssignments(ctx, identity.AuthorizationPageRequest{RoleDefinitionID: missing}, 20, "")
			case "missing group assignments":
				_, err = service.ListAuthorizationAssignments(ctx, identity.AuthorizationPageRequest{GroupID: missing}, 20, "")
			case "missing member add":
				_, err = service.ChangeAccessGroupMember(ctx, group.ID, "", missing, 0, "Missing target", "add", false)
			case "missing grant role":
				_, err = service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: missing, MembershipID: alice.MembershipID, Reason: "Missing target"}, "grant")
			case "missing grant group":
				_, err = service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: missing, Reason: "Missing target"}, "grant")
			case "missing grant member":
				_, err = service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: missing, Reason: "Missing target"}, "grant")
			case "missing grant namespace":
				_, err = service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, NamespaceID: missing, Reason: "Missing target"}, "grant")
			case "already active group":
				want = identity.CodeConflict
				_, err = service.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "No reactivation of active group"}, identity.GroupActive, "reactivate")
			case "archived role update":
				want = identity.CodeConflict
				_, err = service.SaveRoleDefinition(ctx, terminalRole.ID, terminalRole.Revision, identity.RoleDefinitionInput{Name: terminalRole.Name, Permissions: terminalRole.Permissions, Reason: "No terminal role edit"}, false, "update")
			case "archived role grant":
				want = identity.CodeConflict
				_, err = service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: terminalRole.ID, MembershipID: alice.MembershipID, Reason: "No terminal role grant"}, "grant")
			case "archived group update":
				want = identity.CodeConflict
				_, err = service.SaveAccessGroup(ctx, terminalGroup.ID, terminalGroup.Revision, identity.GroupInput{Name: terminalGroup.Name, Reason: "No terminal group edit"}, "", "update")
			case "archived group add":
				want = identity.CodeConflict
				_, err = service.ChangeAccessGroupMember(ctx, terminalGroup.ID, "", alice.MembershipID, 0, "No archived group member", "add", false)
			case "suspended group add":
				want = identity.CodeConflict
				_, err = service.ChangeAccessGroupMember(ctx, suspended.ID, "", alice.MembershipID, 0, "No suspended group member", "add", false)
			case "suspended group grant":
				want = identity.CodeConflict
				_, err = service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: suspended.ID, Reason: "No suspended group grant"}, "grant")
			case "terminal member revoke":
				want = identity.CodeConflict
				_, err = service.ChangeAccessGroupMember(ctx, group.ID, terminalMember.ID, "", terminalMember.Revision, "No terminal relation rewrite", "revoke", true)
			case "terminal assignment revoke":
				want = identity.CodeConflict
				_, err = service.RevokeAuthorizationAssignment(ctx, terminalAssignment.ID, terminalAssignment.Revision, "No terminal assignment rewrite", "revoke")
			case "backdated assignment revoke":
				want = identity.CodeConflict
				serviceClock.Advance(-time.Microsecond)
				defer serviceClock.Advance(time.Microsecond)
				_, err = service.RevokeAuthorizationAssignment(ctx, assignment.ID, assignment.Revision, "Reject backdated revoke", "backdated")
			case "backdated group reactivate":
				want = identity.CodeConflict
				serviceClock.Advance(-time.Microsecond)
				defer serviceClock.Advance(time.Microsecond)
				_, err = service.SaveAccessGroup(ctx, suspended.ID, suspended.Revision, identity.GroupInput{Reason: "Reject backdated reactivation"}, identity.GroupActive, "backdated")
			}
			if identity.ErrorCodeOf(err) != want {
				t.Fatalf("err=%v want=%s", err, want)
			}
			stage := "command"
			if name == "backdated assignment revoke" {
				stage = "authorization.assignment.time_regression"
			} else if name == "backdated group reactivate" {
				stage = "authorization.group.time_regression"
			}
			if safeerr.Stage(err) != stage {
				t.Fatalf("refusal diagnostic=%q want=%q", safeerr.Stage(err), stage)
			}
			if after := authorizationRetainedRows(ctx, t, db, admin.TenantID); !reflect.DeepEqual(before, after) {
				t.Fatal("refused operation changed retained state or evidence")
			}
		})
	}
	// Reuse the exact refused targets and revisions at the retained timestamp.
	// No host clock changes, sleeps or automatic command retries are involved.
	at := serviceClock.Now()
	revoked, err := service.RevokeAuthorizationAssignment(ctx, assignment.ID, assignment.Revision, "Revoke at grant time", "boundary-revoke")
	if err != nil || revoked.State != identity.AssignmentRevoked || revoked.Revision != assignment.Revision+1 || revoked.RevokedAt == nil || !revoked.RevokedAt.Equal(at) {
		t.Fatalf("equal-time revoke: %+v, %v", revoked, err)
	}
	reactivated, err := service.SaveAccessGroup(ctx, suspended.ID, suspended.Revision, identity.GroupInput{Reason: "Reactivate at suspension time"}, identity.GroupActive, "boundary-reactivate")
	if err != nil || reactivated.State != identity.GroupActive || reactivated.Revision != suspended.Revision+1 || !reactivated.UpdatedAt.Equal(at) {
		t.Fatalf("equal-time reactivation: %+v, %v", reactivated, err)
	}
	for _, trial := range []struct{ requestID, action, target string }{
		{"boundary-revoke", "authorization.assignment.revoked", assignment.ID},
		{"boundary-reactivate", "authorization.group.reactivated", suspended.ID},
	} {
		var count int
		if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND request_id=$2 AND action=$3 AND target_id=$4 AND occurred_at=$5`, admin.TenantID, trial.requestID, trial.action, trial.target, at).Scan(&count); err != nil || count != 1 {
			t.Fatalf("boundary transition audit %s: count=%d err=%v", trial.requestID, count, err)
		}
	}
}
