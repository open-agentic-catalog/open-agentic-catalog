// Access mutation units observe scoped writes, guards and evidence handoff.
package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestIdentityRoleGrantBindingsAndFailures(t *testing.T) {
	for _, mode := range []string{"new membership", "existing membership", "global role", "lock", "tenant read", "realm read", "realm mismatch", "member read", "inactive member", "actor mismatch", "member insert", "namespace write", "assignment write", "audit write", "outbox write", "evidence mismatch"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, member := accessRepositoryFixture(t)
			assignment := accessAssignment(member)
			at := member.CreatedAt.Add(time.Hour)
			params := identity.GrantRoleParams{TenantID: member.TenantID, RealmID: member.RealmID, MembershipID: "00000000-0000-7000-8000-000000000011", NamespaceMembershipID: "00000000-0000-7000-8000-000000000040", AssignmentID: assignment.ID, Subject: member.Subject, ActorType: member.ActorType, Role: assignment.Role, NamespaceID: assignment.NamespaceID, Actor: "admin", Reason: "grant reviewed access", At: at, Evidence: currentTestIdentityEvidence(t, "identity.role_assignment.granted", "role_assignment", assignment.ID, at)}
			params.Evidence.Reason = params.Reason
			failure := errors.New("grant failure")
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{member.TenantID}), scriptedValueRow([]any{member.TenantID}), postgresMembershipRow(t, member)}
			writes, reads := 5, 3
			code := identity.CodeDependency
			success := false
			switch mode {
			case "new membership", "member insert":
				tx.queryRows[2] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				writes = 6
				if mode == "new membership" {
					success = true
				} else {
					writes = 2
					tx.execErrors = []error{nil, failure}
				}
			case "existing membership":
				success = true
			case "global role":
				params.NamespaceID = ""
				params.Role = identity.RoleReader
				writes = 4
				success = true
			case "lock":
				tx.execErrors = []error{failure}
				writes, reads = 1, 0
			case "tenant read":
				tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
				writes, reads = 1, 1
			case "realm read":
				tx.queryRows[1] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				writes, reads, code = 1, 2, identity.CodeNotFound
			case "realm mismatch":
				tx.queryRows[1] = scriptedValueRow([]any{"00000000-0000-7000-8000-000000000002"})
				writes, reads, code = 1, 2, identity.CodeNotFound
			case "member read":
				tx.queryRows[2] = scannerFunc(func(...any) error { return failure })
				writes = 1
			case "inactive member":
				member.State = identity.MembershipSuspended
				tx.queryRows[2] = postgresMembershipRow(t, member)
				writes, code = 1, identity.CodeConflict
			case "actor mismatch":
				params.ActorType = identity.ActorService
				writes, code = 1, identity.CodeConflict
			case "namespace write":
				tx.execErrors = []error{nil, failure}
				writes = 2
			case "assignment write":
				tx.execErrors = []error{nil, nil, failure}
				writes = 3
			case "audit write":
				tx.execErrors = []error{nil, nil, nil, failure}
				writes = 4
			case "outbox write":
				tx.execErrors = []error{nil, nil, nil, nil, failure}
			case "evidence mismatch":
				params.Evidence.TargetID = member.ID
				writes = 3
			}
			got, err := store.GrantRole(ctx, params)
			if success {
				want := assignment
				want.GrantedAt = at
				want.GrantReason = params.Reason
				want.NamespaceID = params.NamespaceID
				want.Role = params.Role
				if mode == "new membership" {
					want.MembershipID = params.MembershipID
				}
				if err != nil || !reflect.DeepEqual(got, want) || tx.rollbacks != 0 {
					t.Fatalf("grant=%+v want=%+v error=%v", got, want, err)
				}
				index := 1
				if mode == "new membership" {
					if !reflect.DeepEqual(tx.arguments[index], []any{params.MembershipID, member.TenantID, member.RealmID, member.Subject, member.ActorType, identity.MembershipActive, int64(1), at, at}) {
						t.Fatalf("new member args=%v", tx.arguments[index])
					}
					index++
				}
				if params.NamespaceID != "" {
					if !reflect.DeepEqual(tx.arguments[index], []any{params.NamespaceMembershipID, member.TenantID, want.MembershipID, params.NamespaceID, params.Actor, at}) {
						t.Fatalf("namespace binding=%v", tx.arguments[index])
					}
					index++
				}
				if !reflect.DeepEqual(tx.arguments[index], []any{want.ID, want.TenantID, want.MembershipID, want.NamespaceID, want.Role, want.State, want.Revision, want.GrantedBy, want.GrantReason, want.GrantedAt}) {
					t.Fatalf("assignment binding=%v", tx.arguments[index])
				}
				if tx.arguments[index+1][2] != member.TenantID || tx.arguments[index+1][6] != assignment.ID || tx.arguments[index+2][3] != assignment.ID {
					t.Fatal("evidence lost tenant or target binding")
				}
			} else if identity.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, identity.RoleAssignment{}) || tx.rollbacks != 1 {
				t.Fatalf("failed grant=%+v %v rollbacks=%d", got, err, tx.rollbacks)
			}
			if len(tx.statements) != writes || len(tx.queryRowStatements) != reads || tx.commits != 0 {
				t.Fatalf("writes/reads=%d/%d want=%d/%d commits=%d", len(tx.statements), len(tx.queryRowStatements), writes, reads, tx.commits)
			}
			expected := [][]any{{member.TenantID}, {member.TenantID, member.RealmID}, {member.TenantID, member.RealmID, member.Subject}}
			if reads > 0 && !reflect.DeepEqual(tx.queryRowArguments, expected[:reads]) {
				t.Fatalf("grant read scope=%v", tx.queryRowArguments)
			}
			switch mode {
			case "lock", "tenant read", "member read", "member insert", "namespace write", "assignment write", "audit write", "outbox write":
				if !errors.Is(err, failure) {
					t.Fatal("driver cause lost")
				}
			}
		})
	}
}

func TestIdentityRoleRevocationGuardsAndEvidence(t *testing.T) {
	for _, mode := range []string{"namespace", "global", "nonfinal admin", "final admin", "admin count", "lock", "missing", "stale", "already revoked", "assignment write", "namespace write", "audit write", "outbox write", "evidence mismatch"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, member := accessRepositoryFixture(t)
			assignment := accessAssignment(member)
			at := member.CreatedAt.Add(time.Hour)
			params := identity.RevokeRoleParams{RealmID: member.RealmID, TenantID: member.TenantID, AssignmentID: assignment.ID, ExpectedRevision: assignment.Revision, Actor: "admin", Reason: "remove reviewed access", At: at, Evidence: currentTestIdentityEvidence(t, "identity.role_assignment.revoked", "role_assignment", assignment.ID, at)}
			params.Evidence.Reason = params.Reason
			failure := errors.New("revocation failure")
			code := identity.CodeDependency
			writes, reads := 5, 2
			success := false
			if mode == "global" {
				assignment.NamespaceID = ""
				assignment.Role = identity.RoleReader
			}
			if mode == "nonfinal admin" || mode == "final admin" || mode == "admin count" {
				assignment.NamespaceID = ""
				assignment.Role = identity.RoleTenantAdmin
				reads = 3
			}
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{member.TenantID}), scriptedValueRow(accessAssignmentValues(assignment)), scriptedValueRow([]any{2})}
			switch mode {
			case "namespace":
				success = true
			case "global", "nonfinal admin":
				success = true
				writes = 4
			case "final admin":
				tx.queryRows[2] = scriptedValueRow([]any{1})
				writes, code = 1, identity.CodeSeparationOfDuties
			case "admin count":
				tx.queryRows[2] = scannerFunc(func(...any) error { return failure })
				writes = 1
			case "lock":
				tx.execErrors = []error{failure}
				writes, reads = 1, 0
			case "missing":
				tx.queryRows[1] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				writes, code = 1, identity.CodeNotFound
			case "stale":
				params.ExpectedRevision++
				writes, code = 1, identity.CodePreconditionFailed
			case "already revoked":
				assignment.State = identity.AssignmentRevoked
				assignment.Revision++
				assignment.RevokedBy = "previous-admin"
				assignment.RevokeReason = "previous removal"
				assignment.RevokedAt = &at
				params.ExpectedRevision = assignment.Revision
				tx.queryRows[1] = scriptedValueRow(accessAssignmentValues(assignment))
				writes, code = 1, identity.CodeConflict
			case "assignment write":
				tx.execErrors = []error{nil, failure}
				writes = 2
			case "namespace write":
				tx.execErrors = []error{nil, nil, failure}
				writes = 3
			case "audit write":
				tx.execErrors = []error{nil, nil, nil, failure}
				writes = 4
			case "outbox write":
				tx.execErrors = []error{nil, nil, nil, nil, failure}
			case "evidence mismatch":
				params.Evidence.TargetID = member.ID
				writes = 3
			}
			got, err := store.RevokeRole(ctx, params)
			if success {
				want := assignment
				want.State = identity.AssignmentRevoked
				want.Revision++
				want.RevokedBy = params.Actor
				want.RevokeReason = params.Reason
				want.RevokedAt = &at
				if err != nil || !reflect.DeepEqual(got, want) || tx.rollbacks != 0 {
					t.Fatalf("revoke=%+v want=%+v error=%v", got, want, err)
				}
				if !reflect.DeepEqual(tx.arguments[1], []any{assignment.ID, want.Revision, params.Actor, params.Reason, &at, member.TenantID}) {
					t.Fatalf("revoke binding=%v", tx.arguments[1])
				}
				index := 2
				if assignment.NamespaceID != "" {
					if !reflect.DeepEqual(tx.arguments[index], []any{member.TenantID, member.ID, params.Actor, params.Reason, at, assignment.NamespaceID}) {
						t.Fatalf("namespace cleanup scope=%v", tx.arguments[index])
					}
					index++
				}
				if tx.arguments[index][2] != member.TenantID || tx.arguments[index][6] != assignment.ID || tx.arguments[index+1][3] != assignment.ID {
					t.Fatal("evidence binding lost")
				}
			} else if identity.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, identity.RoleAssignment{}) || tx.rollbacks != 1 {
				t.Fatalf("failed revoke=%+v %v", got, err)
			}
			if len(tx.statements) != writes || len(tx.queryRowStatements) != reads || tx.commits != 0 {
				t.Fatalf("writes/reads=%d/%d want=%d/%d", len(tx.statements), len(tx.queryRowStatements), writes, reads)
			}
			expected := [][]any{{member.TenantID}, {member.TenantID, assignment.ID, member.RealmID}, {member.TenantID}}
			if reads > 0 && !reflect.DeepEqual(tx.queryRowArguments, expected[:reads]) {
				t.Fatalf("revoke scope=%v", tx.queryRowArguments)
			}
			switch mode {
			case "admin count", "lock", "assignment write", "namespace write", "audit write", "outbox write":
				if !errors.Is(err, failure) {
					t.Fatal("driver cause lost")
				}
			}
		})
	}
}

// Existing lifecycle tests own normal transitions and final-admin controls.
// These cases exercise earlier read failures and later evidence/write failures.
func TestIdentityMembershipTransitionFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"lock", "member read", "stale", "invalid reactivation", "realm read", "admin read", "admin count", "membership update", "audit write", "outbox write", "evidence mismatch"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, member := accessRepositoryFixture(t)
			at := member.CreatedAt.Add(time.Hour)
			params := identity.ChangeMembershipParams{TenantID: member.TenantID, MembershipID: member.ID, ExpectedRevision: 1, Transition: identity.MembershipSuspend, Actor: "admin", Reason: "suspend access", At: at, Evidence: currentTestIdentityEvidence(t, "identity.membership.suspended", "tenant_membership", member.ID, at)}
			params.Evidence.Reason = params.Reason
			failure := errors.New("membership transition failure")
			code := identity.CodeDependency
			writes, reads := 1, 3
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{member.TenantID}), postgresMembershipRow(t, member), scriptedValueRow([]any{false})}
			switch mode {
			case "lock":
				tx.execErrors = []error{failure}
				reads = 0
			case "member read":
				tx.queryRows[1] = scannerFunc(func(...any) error { return failure })
				reads = 2
			case "stale":
				params.ExpectedRevision++
				code = identity.CodePreconditionFailed
				reads = 2
			case "invalid reactivation":
				params.Transition = identity.MembershipReactivate
				code = identity.CodeConflict
				reads = 2
			case "realm read":
				params.Transition = identity.MembershipReactivate
				member.State = identity.MembershipSuspended
				tx.queryRows[1] = postgresMembershipRow(t, member)
				tx.queryRows[2] = scannerFunc(func(...any) error { return failure })
			case "admin read":
				tx.queryRows[2] = scannerFunc(func(...any) error { return failure })
			case "admin count":
				tx.queryRows[2] = scriptedValueRow([]any{true})
				tx.queryRows = append(tx.queryRows, scannerFunc(func(...any) error { return failure }))
				reads = 4
			case "membership update":
				tx.execErrors = []error{nil, failure}
				writes = 2
			case "audit write":
				tx.execErrors = []error{nil, nil, failure}
				writes = 3
			case "outbox write":
				tx.execErrors = []error{nil, nil, nil, failure}
				writes = 4
			case "evidence mismatch":
				params.Evidence.TargetID = member.RealmID
				writes = 2
			}
			got, err := store.ChangeMembership(ctx, params)
			if identity.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, identity.MembershipLifecycleResult{}) || tx.rollbacks != 1 || tx.commits != 0 || len(tx.statements) != writes || len(tx.queryRowStatements) != reads {
				t.Fatalf("transition=%+v %v writes/reads=%d/%d", got, err, len(tx.statements), len(tx.queryRowStatements))
			}
			if mode != "stale" && mode != "invalid reactivation" && mode != "evidence mismatch" && !errors.Is(err, failure) {
				t.Fatal("cause lost")
			}
			if writes > 1 && !reflect.DeepEqual(tx.arguments[1], []any{member.TenantID, member.ID, identity.MembershipSuspended, int64(2), at}) {
				t.Fatalf("transition scope=%v", tx.arguments[1])
			}
			if writes > 2 {
				raw, ok := tx.arguments[2][11].([]byte)
				if !ok {
					t.Fatal("audit details are not bytes")
				}
				var details struct {
					Facts struct {
						Transition string `json:"transition"`
						Roles      int    `json:"revokedRoleAssignments"`
						Namespaces int    `json:"revokedNamespaceMemberships"`
					} `json:"facts"`
				}
				if err := json.Unmarshal(raw, &details); err != nil || details.Facts.Transition != "suspend" || details.Facts.Roles != 0 || details.Facts.Namespaces != 0 {
					t.Fatalf("suspension evidence=%s %v", raw, err)
				}
			}
		})
	}
}
