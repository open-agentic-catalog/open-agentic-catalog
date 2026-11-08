// Member-access units exercise one metadata port through the direct handler.
// Routed authentication, revocation concurrency and SQL remain separate tests.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func pass2MemberAccessRequest(t *testing.T, query, member string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin)), http.MethodGet, "/api/v1/memberships/"+member+"/access"+query, nil)
	r.SetPathValue("membershipId", member)
	return r
}

func pass2MemberContribution(key string) identity.MemberContribution {
	realm := projectionActor(identity.RoleTenantAdmin).RealmID
	return identity.MemberContribution{
		Assignment: identity.AccessAssignment{ID: key, TenantID: projectionTenant, RealmID: realm, RoleDefinitionID: projectionResource, MembershipID: projectionItem, State: identity.AssignmentActive, Revision: 1, GrantedBy: "administrator", GrantReason: "Reviewed read access", GrantedAt: projectionTime},
		Role:       identity.RoleDefinition{ID: projectionResource, TenantID: projectionTenant, Name: "Read Catalog resources", State: identity.DefinitionActive, Revision: 1, Permissions: []identity.Permission{"resource.read"}, CreatedAt: projectionTime, UpdatedAt: projectionTime},
		Effective:  true,
	}
}

func TestPass2MemberAccessProjectionAndPageBounds(t *testing.T) {
	t.Parallel()
	for _, limit := range []string{"", "1", "100"} {
		for _, realm := range []string{"current", "other"} {
			t.Run("limit="+limit+"/realm="+realm, func(t *testing.T) {
				query, wantLimit := "", 21
				if limit != "" {
					query = "?limit=" + limit
					wantLimit = 2
					if limit == "100" {
						wantLimit = 101
					}
				}
				request := pass2MemberAccessRequest(t, query, projectionItem)
				member := projectionMembership()
				coverage := "current_realm"
				if realm == "other" {
					member.RealmID, coverage = projectionResource, "unavailable"
				}
				item := pass2MemberContribution(projectionNext)
				item.Assignment.RealmID = member.RealmID
				calls := 0
				store := pass2IdentitySummaryStore{permission: pass2Permission(t, request, true), members: func(c context.Context, actor identity.Actor, q identity.AuthorizationPageRequest) (identity.MemberContributionSelection, error) {
					calls++
					if !sameProjectionContext(c, request.Context()) || actor.TenantID != projectionTenant || q.MembershipID != projectionItem || q.Limit != wantLimit || q.AfterID != "" || q.AuthorizationRevision != 8 || q.GroupID != "" || q.RoleDefinitionID != "" {
						t.Fatal("member contribution read lost actor, target, policy or bounded page", actor, q)
					}
					return identity.MemberContributionSelection{Membership: member, Items: []identity.MemberContribution{item}}, nil
				}}
				server := projectionServer(nil)
				server.identity = pass2IdentityService(t, store)
				response := httptest.NewRecorder()
				server.handleMemberAccess(response, request)
				requireControlJSON(t, response, http.StatusOK, identity.MemberAccessPage{AuthorizationPage: identity.AuthorizationPage[identity.MemberContribution]{Items: []identity.MemberContribution{item}, AuthorizationRevision: 8}, Membership: member, GroupCoverage: coverage})
				if calls != 1 {
					t.Fatal("member-access read count", calls)
				}
			})
		}
	}
}

func TestPass2MemberAccessContinuationKeepsExactMemberAndPosition(t *testing.T) {
	t.Parallel()
	request := pass2MemberAccessRequest(t, "?limit=1", projectionItem)
	member := projectionMembership()
	first, second := pass2MemberContribution(projectionItem), pass2MemberContribution(projectionNext)
	calls := 0
	store := pass2IdentitySummaryStore{permission: pass2Permission(t, request, true), members: func(_ context.Context, _ identity.Actor, q identity.AuthorizationPageRequest) (identity.MemberContributionSelection, error) {
		calls++
		if q.MembershipID != member.ID || q.Limit != 2 || q.AuthorizationRevision != 8 {
			t.Fatal("continuation changed its member, bound or policy revision", q)
		}
		switch calls {
		case 1:
			if q.AfterID != "" {
				t.Fatal("first page acquired a position", q)
			}
			return identity.MemberContributionSelection{Membership: member, Items: []identity.MemberContribution{first, second}}, nil
		case 2:
			if q.AfterID != first.Assignment.ID {
				t.Fatal("continuation skipped the last returned assignment", q)
			}
			return identity.MemberContributionSelection{Membership: member, Items: []identity.MemberContribution{second}}, nil
		default:
			t.Fatal("changed continuation reached the member metadata store")
			return identity.MemberContributionSelection{}, nil
		}
	}}
	server := projectionServer(nil)
	server.identity = pass2IdentityService(t, store)
	response := httptest.NewRecorder()
	server.handleMemberAccess(response, request)
	var page identity.MemberAccessPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.NextCursor == "" {
		t.Fatal("lookahead did not produce member continuation", response.Code, response.Body, err)
	}
	requireControlJSON(t, response, http.StatusOK, identity.MemberAccessPage{AuthorizationPage: identity.AuthorizationPage[identity.MemberContribution]{Items: []identity.MemberContribution{first}, AuthorizationRevision: 8, NextCursor: page.NextCursor}, Membership: member, GroupCoverage: "current_realm"})
	cursor := url.QueryEscape(page.NextCursor)
	resume := request.Clone(request.Context())
	resume.URL.RawQuery = "limit=1&cursor=" + cursor
	response = httptest.NewRecorder()
	server.handleMemberAccess(response, resume)
	requireControlJSON(t, response, http.StatusOK, identity.MemberAccessPage{AuthorizationPage: identity.AuthorizationPage[identity.MemberContribution]{Items: []identity.MemberContribution{second}, AuthorizationRevision: 8}, Membership: member, GroupCoverage: "current_realm"})
	for _, change := range []string{"member", "limit"} {
		memberID, limit := projectionItem, "1"
		if change == "member" {
			memberID = projectionNext
		} else {
			limit = "2"
		}
		r := pass2MemberAccessRequest(t, "?limit="+limit+"&cursor="+cursor, memberID)
		response = httptest.NewRecorder()
		server.handleMemberAccess(response, r)
		requireProjectionProblem(t, response, http.StatusBadRequest, catalog.CodeInvalidArgument)
	}
	if calls != 2 {
		t.Fatal("changed cursor reached storage", calls)
	}
}

func TestPass2MemberAccessInvalidPageAndDeniedAuthoritySkipRead(t *testing.T) {
	t.Parallel()
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=bad", "?limit=1&limit=2", "?cursor=", "?cursor=a&cursor=b", "?cursor=" + strings.Repeat("x", 2049), "?cursor=%zz"} {
		t.Run(query[:min(len(query), 40)], func(t *testing.T) {
			request := pass2MemberAccessRequest(t, query, projectionItem)
			server := projectionServer(nil)
			// A malformed page must fail before selecting any identity service.
			response := httptest.NewRecorder()
			server.handleMemberAccess(response, request)
			requireProjectionProblem(t, response, http.StatusBadRequest, catalog.CodeInvalidArgument)
		})
	}
	request := pass2MemberAccessRequest(t, "", projectionItem)
	server := projectionServer(nil)
	server.identity = pass2IdentityService(t, pass2IdentitySummaryStore{permission: pass2Permission(t, request, false), members: func(context.Context, identity.Actor, identity.AuthorizationPageRequest) (identity.MemberContributionSelection, error) {
		t.Fatal("denied member-access request reached retained metadata")
		return identity.MemberContributionSelection{}, nil
	}})
	response := httptest.NewRecorder()
	server.handleMemberAccess(response, request)
	requireProjectionProblem(t, response, http.StatusForbidden, catalog.CodeForbidden)
}

func TestPass2MemberAccessIdentityFailuresStayClassified(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code   identity.ErrorCode
		status int
		want   catalog.ErrorCode
	}{{identity.CodeNotFound, http.StatusNotFound, catalog.CodeNotFound}, {identity.CodeConflict, http.StatusConflict, catalog.CodeConflict}, {identity.CodeDependency, http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable}} {
		t.Run(string(tc.code), func(t *testing.T) {
			request := pass2MemberAccessRequest(t, "", projectionItem)
			calls := 0
			server := projectionServer(nil)
			server.identity = pass2IdentityService(t, pass2IdentitySummaryStore{permission: pass2Permission(t, request, true), members: func(context.Context, identity.Actor, identity.AuthorizationPageRequest) (identity.MemberContributionSelection, error) {
				calls++
				return identity.MemberContributionSelection{}, identity.E(tc.code, "member metadata unavailable")
			}})
			response := httptest.NewRecorder()
			server.handleMemberAccess(response, request)
			requireProjectionProblem(t, response, tc.status, tc.want)
			if calls != 1 {
				t.Fatal("member-access failure did not come from the selected read", calls)
			}
		})
	}
}
