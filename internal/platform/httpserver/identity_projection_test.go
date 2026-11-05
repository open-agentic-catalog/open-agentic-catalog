// Identity handler projections retain tenant, revision and request attribution
// at existing store ports without running membership lifecycle workflows.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type identityProjectionStore struct {
	identity.Store
	get     func(context.Context, string, string) (identity.Membership, error)
	members func(context.Context, string, string, string, int) ([]identity.Membership, error)
	change  func(context.Context, identity.ChangeMembershipParams) (identity.MembershipLifecycleResult, error)
	roles   func(context.Context, string, identity.RoleAssignmentListRequest) ([]identity.RoleAssignment, error)
	grant   func(context.Context, identity.GrantRoleParams) (identity.RoleAssignment, error)
	revoke  func(context.Context, identity.RevokeRoleParams) (identity.RoleAssignment, error)
}

func (s identityProjectionStore) GetMembership(c context.Context, t, k string) (identity.Membership, error) {
	return s.get(c, t, k)
}
func (s identityProjectionStore) ListMemberships(c context.Context, t, subject, key string, n int, filters ...identity.MembershipFilter) ([]identity.Membership, error) {
	return s.members(c, t, subject, key, n)
}
func (s identityProjectionStore) ChangeMembership(c context.Context, p identity.ChangeMembershipParams) (identity.MembershipLifecycleResult, error) {
	return s.change(c, p)
}
func (s identityProjectionStore) ListRoleAssignments(c context.Context, t string, p identity.RoleAssignmentListRequest) ([]identity.RoleAssignment, error) {
	return s.roles(c, t, p)
}
func (s identityProjectionStore) GrantRole(c context.Context, p identity.GrantRoleParams) (identity.RoleAssignment, error) {
	return s.grant(c, p)
}
func (s identityProjectionStore) RevokeRole(c context.Context, p identity.RevokeRoleParams) (identity.RoleAssignment, error) {
	return s.revoke(c, p)
}
func identityProjectionServer(t *testing.T, store identityProjectionStore) *Server {
	t.Helper()
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(projectionTime), IDs: id.Generator{}, CursorKey: []byte(strings.Repeat("i", 32))})
	if err != nil {
		t.Fatal(err)
	}
	s := projectionServer(nil)
	s.identity = service
	return s
}
func projectionMembership() identity.Membership {
	return identity.Membership{ID: projectionItem, TenantID: projectionTenant, RealmID: projectionActor(identity.RoleTenantAdmin).RealmID, Subject: "member", ActorType: identity.ActorHuman, State: identity.MembershipActive, Revision: 4, CreatedAt: projectionTime, UpdatedAt: projectionTime}
}
func projectionAssignment() identity.RoleAssignment {
	return identity.RoleAssignment{ID: projectionNext, TenantID: projectionTenant, MembershipID: projectionItem, Subject: "member", ActorType: identity.ActorHuman, Role: identity.RoleReader, State: identity.AssignmentActive, Revision: 4, GrantedBy: "administrator", GrantReason: "Read access", GrantedAt: projectionTime}
}

// TestIdentityMutationProjection checks each dispatch once against an independent
// store response, including returned revision and classified storage failures.
func TestIdentityMutationProjection(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"get", "suspend", "reactivate", "offboard", "grant", "revoke"} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", action, failure), func(t *testing.T) {
				actor := projectionActor(identity.RoleTenantAdmin)
				ctx := context.WithValue(identityfixture.WithActor(t.Context(), actor), requestIDKey{}, "identity-request")
				member := projectionMembership()
				if action == "reactivate" {
					member.State = identity.MembershipSuspended
				}
				assignment := projectionAssignment()
				reads, writes := 0, 0
				failureErr := identity.E(identity.CodeConflict, "concurrent identity update")
				checkEvidence := func(e identity.ChangeEvidence, expectedAction, target string) {
					if e.Action != expectedAction || e.TargetID != target || e.Actor != actor.MembershipID || e.RequestID != "identity-request" || e.Reason != "Access maintenance" || e.FormatVersion == nil {
						t.Fatalf("identity evidence=%+v", e)
					}
				}
				store := identityProjectionStore{
					get: func(got context.Context, tenant, key string) (identity.Membership, error) {
						reads++
						if !sameProjectionContext(got, ctx) || tenant != actor.TenantID || key != member.ID {
							t.Fatal("identity get binding")
						}
						if action == "get" && failure {
							return identity.Membership{}, identity.E(identity.CodeNotFound, "not found")
						}
						return member, nil
					},
					change: func(got context.Context, p identity.ChangeMembershipParams) (identity.MembershipLifecycleResult, error) {
						writes++
						if !sameProjectionContext(got, ctx) || p.TenantID != actor.TenantID || p.MembershipID != member.ID || p.ExpectedRevision != 4 || string(p.Transition) != action {
							t.Fatalf("transition=%+v", p)
						}
						state := identity.MembershipSuspended
						event := "identity.membership.suspended"
						if action == "reactivate" {
							state = identity.MembershipActive
							event = "identity.membership.reactivated"
						}
						if action == "offboard" {
							state = identity.MembershipOffboarded
							event = "identity.membership.offboarded"
						}
						checkEvidence(p.Evidence, event, member.ID)
						if failure {
							return identity.MembershipLifecycleResult{}, failureErr
						}
						member.State = state
						member.Revision = 5
						result := identity.MembershipLifecycleResult{Membership: member}
						if action == "offboard" {
							result.RevokedRoleAssignments = 2
							result.RevokedNamespaceMemberships = 3
						}
						return result, nil
					},
					grant: func(got context.Context, p identity.GrantRoleParams) (identity.RoleAssignment, error) {
						writes++
						if !sameProjectionContext(got, ctx) || p.TenantID != actor.TenantID || p.RealmID != actor.RealmID || p.Subject != "member" || p.Role != identity.RoleReader || p.ActorType != identity.ActorHuman || p.NamespaceID != "" {
							t.Fatalf("grant=%+v", p)
						}
						checkEvidence(p.Evidence, "identity.role_assignment.granted", p.AssignmentID)
						if failure {
							return identity.RoleAssignment{}, failureErr
						}
						assignment.ID = p.AssignmentID
						assignment.MembershipID = p.MembershipID
						assignment.Revision = 1
						assignment.GrantedBy = actor.Subject
						assignment.GrantReason = p.Reason
						assignment.GrantedAt = p.At
						return assignment, nil
					},
					revoke: func(got context.Context, p identity.RevokeRoleParams) (identity.RoleAssignment, error) {
						writes++
						if !sameProjectionContext(got, ctx) || p.TenantID != actor.TenantID || p.AssignmentID != assignment.ID || p.ExpectedRevision != 4 {
							t.Fatalf("revoke=%+v", p)
						}
						checkEvidence(p.Evidence, "identity.role_assignment.revoked", assignment.ID)
						if failure {
							return identity.RoleAssignment{}, failureErr
						}
						assignment.Revision = 5
						assignment.State = identity.AssignmentRevoked
						assignment.RevokedBy = actor.Subject
						assignment.RevokeReason = p.Reason
						assignment.RevokedAt = &p.At
						return assignment, nil
					},
				}
				s := identityProjectionServer(t, store)
				path := "/api/v1/memberships/" + member.ID + ":" + action
				r := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(`{"reason":" Access maintenance "}`))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("If-Match", `"rev-4"`)
				w := httptest.NewRecorder()
				status := 200
				switch action {
				case "get":
					r.Method = http.MethodGet
					r.SetPathValue("membershipId", member.ID)
					s.handleMembershipGet(w, r)
				case "grant":
					r = httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/role-assignments", strings.NewReader(`{"subject":"member","actorType":"human","role":"reader","reason":"Access maintenance"}`))
					r.Header.Set("Content-Type", "application/json")
					s.handleRoleAssignmentCreate(w, r)
					status = 201
				case "revoke":
					r.SetPathValue("assignmentId", assignment.ID)
					s.handleRoleAssignmentDelete(w, r)
				default:
					r.SetPathValue("membershipCommand", member.ID+":"+action)
					s.dispatchMembershipCommand(w, r)
				}
				wantReads, wantWrites := 1, 1
				if action == "get" {
					wantWrites = 0
				}
				if action == "grant" || action == "revoke" {
					wantReads = 0
				}
				if reads != wantReads || writes != wantWrites {
					t.Fatalf("reads=%d writes=%d", reads, writes)
				}
				if failure {
					wantStatus, wantCode := 409, catalog.CodeConflict
					if action == "get" {
						wantStatus = 404
						wantCode = catalog.CodeNotFound
					}
					requireProjectionProblem(t, w, wantStatus, wantCode)
					return
				}
				revision := member.Revision
				switch action {
				case "grant", "revoke":
					revision = assignment.Revision
					var got identity.RoleAssignment
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, assignment) {
						t.Fatal("assignment projection lost")
					}
				case "get":
					var got identity.Membership
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if got != member {
						t.Fatal("membership projection lost")
					}
				default:
					var got identity.MembershipLifecycleResult
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					want := identity.MembershipLifecycleResult{Membership: member}
					if action == "offboard" {
						want.RevokedRoleAssignments = 2
						want.RevokedNamespaceMemberships = 3
					}
					if got != want {
						t.Fatal("transition result lost")
					}
				}
				if w.Code != status || w.Header().Get("ETag") != fmt.Sprintf(`"rev-%d"`, revision) {
					t.Fatalf("identity receipt=%d %s", w.Code, w.Body)
				}
				if action == "grant" && w.Header().Get("Location") != "/api/v1/role-assignments/"+assignment.ID {
					t.Fatal("grant location lost")
				}
			})
		}
	}
}

// TestIdentityListProjection preserves both membership and role-assignment
// continuation formats and the explicit membership filter at the store boundary.
func TestIdentityListProjection(t *testing.T) {
	t.Parallel()
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
	for _, selection := range []struct{ kind, subject string }{
		{"memberships", "member"}, {"roles", "member"},
		{"memberships", strings.Repeat("<>&", 170) + "<>"}, {"roles", strings.Repeat("<>&", 170) + "<>"},
		{"memberships", strings.Repeat("\\", 512)}, {"roles", strings.Repeat("\\", 512)},
	} {
		kind := selection.kind
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/subject=%s/failure=%t", kind, selection.subject[:1], failure), func(t *testing.T) {
				calls := 0
				member := projectionMembership()
				member.Subject = selection.subject
				if !identity.ValidSubject(member.Subject) {
					t.Fatal("invalid page fixture subject")
				}
				role := projectionAssignment()
				role.Subject = member.Subject
				role.NamespaceID = projectionResource
				store := identityProjectionStore{get: func(got context.Context, tenant, key string) (identity.Membership, error) {
					if !sameProjectionContext(got, ctx) || tenant != projectionTenant || key != member.ID {
						t.Fatal("membership filter binding")
					}
					return member, nil
				},
					members: func(got context.Context, tenant, subject, key string, limit int) ([]identity.Membership, error) {
						calls++
						wantSubject, wantID := "", ""
						if calls == 2 {
							wantSubject = member.Subject
							wantID = member.ID
						}
						if !sameProjectionContext(got, ctx) || tenant != projectionTenant || limit != 2 || subject != wantSubject || key != wantID {
							t.Fatal("membership page binding")
						}
						if failure {
							return nil, identity.E(identity.CodeDependency, "unavailable")
						}
						if calls == 2 {
							return []identity.Membership{}, nil
						}
						next := member
						next.ID = projectionNext
						next.Subject = "next"
						return []identity.Membership{member, next}, nil
					},
					roles: func(got context.Context, tenant string, q identity.RoleAssignmentListRequest) ([]identity.RoleAssignment, error) {
						calls++
						want := identity.RoleAssignmentListRequest{MembershipID: member.ID, Limit: 2}
						if calls == 2 {
							want.AfterSubject = role.Subject
							want.AfterNamespaceID = role.NamespaceID
							want.AfterRole = role.Role
							want.AfterID = role.ID
						}
						if !sameProjectionContext(got, ctx) || tenant != projectionTenant || q != want {
							t.Fatalf("roles query=%+v", q)
						}
						if failure {
							return nil, identity.E(identity.CodeDependency, "unavailable")
						}
						if calls == 2 {
							return []identity.RoleAssignment{}, nil
						}
						next := role
						next.ID = projectionResource
						next.Role = identity.RoleReviewer
						return []identity.RoleAssignment{role, next}, nil
					},
				}
				s := identityProjectionServer(t, store)
				path := "/api/v1/memberships?limit=1"
				handler := s.handleMembershipsList
				if kind == "roles" {
					path = "/api/v1/role-assignments?limit=1&membershipId=" + member.ID
					handler = s.handleRoleAssignmentsList
				}
				for page := 0; page < 2; page++ {
					w := httptest.NewRecorder()
					handler(w, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
					if failure {
						requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
						break
					}
					var got struct {
						Items      []json.RawMessage `json:"items"`
						NextCursor string            `json:"nextCursor"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if w.Code != 200 || got.Items == nil {
						t.Fatalf("identity list=%d %s", w.Code, w.Body)
					}
					if page == 0 {
						if len(got.Items) != 1 || got.NextCursor == "" || len(got.NextCursor) > adminCursorMaximumSize {
							t.Fatal("identity page unbounded")
						}
						var item struct {
							ID      string `json:"id"`
							Subject string `json:"subject"`
						}
						if err := json.Unmarshal(got.Items[0], &item); err != nil {
							t.Fatal(err)
						}
						want := member.ID
						if kind == "roles" {
							want = role.ID
						}
						if item.ID != want || item.Subject != member.Subject {
							t.Fatal("identity page lost item")
						}
						path += "&cursor=" + got.NextCursor
					} else if len(got.Items) != 0 || got.NextCursor != "" {
						t.Fatal("identity final page invalid")
					}
				}
			})
		}
	}
}

func TestCatalogIdentityErrorPreservesSuccessfulAdmission(t *testing.T) {
	if err := catalogIdentityError(nil); err != nil {
		t.Fatalf("successful admission became an error: %v", err)
	}
}
