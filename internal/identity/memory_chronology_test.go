package identity

import (
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestMemoryAuthorizationChronologyDiagnostics(t *testing.T) {
	for _, kind := range []string{"assignment", "group", "group_member"} {
		t.Run(kind, func(t *testing.T) {
			store, service, admin, alice, _ := administrationFixture(t)
			ctx := WithActor(t.Context(), admin)
			at := service.clock.Now()
			group, err := service.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Chronology control", Reason: "Create group"}, "", "create")
			if err != nil {
				t.Fatal(err)
			}
			member, err := service.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Add member", "add", false)
			if err != nil {
				t.Fatal(err)
			}
			role := deterministicUUID("definition", admin.TenantID, "reader")
			assignment, err := service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role, MembershipID: alice.MembershipID, Reason: "Grant reader"}, "grant")
			if err != nil {
				t.Fatal(err)
			}
			transition := func() error {
				switch kind {
				case "assignment":
					_, e := service.RevokeAuthorizationAssignment(ctx, assignment.ID, assignment.Revision, "Remove reader", "revoke")
					return e
				case "group":
					_, e := service.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Suspend group"}, GroupSuspended, "suspend")
					return e
				default:
					_, e := service.ChangeAccessGroupMember(ctx, group.ID, member.ID, "", member.Revision, "Remove member", "remove", true)
					return e
				}
			}
			beforeGroup, beforeMember, beforeAssignment := store.groups[group.ID], store.groupMembers[member.ID], store.accessAssignments[assignment.ID]
			auditCount, revision := len(store.audit), store.revisions[admin.TenantID]
			service.clock = identityTestClock{at: at.Add(-time.Nanosecond)}
			err = transition()
			if ErrorCodeOf(err) != CodeConflict || safeerr.Stage(err) != "authorization."+kind+".time_regression" {
				t.Fatalf("wrong refusal: code=%s stage=%s", ErrorCodeOf(err), safeerr.Stage(err))
			}
			if !reflect.DeepEqual(beforeGroup, store.groups[group.ID]) || !reflect.DeepEqual(beforeMember, store.groupMembers[member.ID]) || !reflect.DeepEqual(beforeAssignment, store.accessAssignments[assignment.ID]) || len(store.audit) != auditCount || store.revisions[admin.TenantID] != revision {
				t.Fatal("backdated transition changed retained state or evidence")
			}
			service.clock = identityTestClock{at: at}
			if err = transition(); err != nil {
				t.Fatalf("equal-time control failed: %v", err)
			}
			if len(store.audit) != auditCount+1 {
				t.Fatal("successful control did not retain its audit")
			}
			assignment.Revision = store.accessAssignments[assignment.ID].Revision
			member.Revision = store.groupMembers[member.ID].Revision
			group.Revision = store.groups[group.ID].Revision
			service.clock = identityTestClock{at: at.Add(time.Second)}
			if err = transition(); ErrorCodeOf(err) != CodeConflict || safeerr.Stage(err) != "command" {
				t.Fatalf("state refusal was misclassified: %v stage=%s", err, safeerr.Stage(err))
			}
		})
	}
}
