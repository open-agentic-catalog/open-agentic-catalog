// Identity handlers expose current membership and grant administration through the
// identity service's authorization boundary.

package httpserver

import (
	"errors"
	"net/http"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// handleMembershipsList validates bounded limit/cursor input before delegating membership pagination to identity.
func (s *Server) handleMembershipsList(writer http.ResponseWriter, request *http.Request) {
	limit, err := parseLimit(request, 20)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	query, err := parseQueryParameters(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	if err := validateDirectoryQuery(request, "subject", "state", "actorType", "realmId"); err != nil {
		s.fail(writer, request, err)
		return
	}
	cursorValues := query["cursor"]
	if len(cursorValues) > 1 || len(cursorValues) == 1 && (cursorValues[0] == "" || len(cursorValues[0]) > 2048) {
		s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor must be one non-empty value of at most 2048 characters"))
		return
	}
	cursor := ""
	if len(cursorValues) == 1 {
		cursor = cursorValues[0]
	}
	page, err := s.identity.ListMemberships(request.Context(), limit, cursor, identity.MembershipFilter{Subject: query.Get("subject"), State: identity.MembershipState(query.Get("state")), ActorType: identity.ActorType(query.Get("actorType")), RealmID: query.Get("realmId")})
	if err != nil {
		s.fail(writer, request, catalogIdentityError(err))
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleMembershipGet returns one authorized membership and its lifecycle ETag.
func (s *Server) handleMembershipGet(writer http.ResponseWriter, request *http.Request) {
	membership, err := s.identity.GetMembership(request.Context(), request.PathValue("membershipId"))
	if err != nil {
		s.fail(writer, request, catalogIdentityError(err))
		return
	}
	setETag(writer, membership.Revision)
	s.respondJSON(writer, request, http.StatusOK, membership)
}

// handleMembershipTransition requires optimistic concurrency and delegates
// last-administrator and offboarding safety to the identity service.
func (s *Server) handleMembershipTransition(writer http.ResponseWriter, request *http.Request, transition identity.MembershipTransition) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	reason, err := reasonBody(writer, request, s.maxBodyBytes)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	membershipID := request.PathValue("membershipId")
	var result identity.MembershipLifecycleResult
	switch transition {
	case identity.MembershipSuspend:
		result, err = s.identity.SuspendMembership(request.Context(), membershipID, reason, requestID(request.Context()), revision)
	case identity.MembershipReactivate:
		result, err = s.identity.ReactivateMembership(request.Context(), membershipID, reason, requestID(request.Context()), revision)
	case identity.MembershipOffboard:
		result, err = s.identity.OffboardMembership(request.Context(), membershipID, reason, requestID(request.Context()), revision)
	default:
		err = identity.E(identity.CodeInvalidArgument, "membership transition is invalid")
	}
	if err != nil {
		s.fail(writer, request, catalogIdentityError(err))
		return
	}
	setETag(writer, result.Membership.Revision)
	s.respondJSON(writer, request, http.StatusOK, result)
}

// dispatchMembershipCommand routes supported lifecycle suffixes after validating the membership UUID.
func (s *Server) dispatchMembershipCommand(writer http.ResponseWriter, request *http.Request) {
	value := request.PathValue("membershipCommand")
	for suffix, transition := range map[string]identity.MembershipTransition{
		":suspend":    identity.MembershipSuspend,
		":reactivate": identity.MembershipReactivate,
		":offboard":   identity.MembershipOffboard,
	} {
		if membershipID, ok := strings.CutSuffix(value, suffix); ok && membershipID != "" {
			if !s.setUUIDPathValue(writer, request, "membershipId", membershipID) {
				return
			}
			s.handleMembershipTransition(writer, request, transition)
			return
		}
	}
	writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "membership command was not found"))
}

// handleRoleAssignmentsList pages grants using subject/namespace/role/ID ordering and an optional membership filter.
func (s *Server) handleRoleAssignmentsList(writer http.ResponseWriter, request *http.Request) {
	memberID := request.URL.Query().Get("membershipId")
	filters := map[string]string{}
	if memberID != "" {
		filters["membershipId"] = memberID
	}
	pageRequest, err := s.parseAdminPage(request, "role-assignments", "", "subject,namespaceId,role,id", filters)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := identity.RoleAssignmentListRequest{Limit: pageRequest.Limit, MembershipID: memberID}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 4 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterSubject = pageRequest.Position[0]
		listRequest.AfterNamespaceID = pageRequest.Position[1]
		listRequest.AfterRole = identity.Role(pageRequest.Position[2])
		listRequest.AfterID = pageRequest.Position[3]
	}
	items, err := s.identity.ListRoleAssignments(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, catalogIdentityError(err))
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item identity.RoleAssignment) []string {
		return []string{item.Subject, item.NamespaceID, string(item.Role), item.ID}
	})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleRoleAssignmentCreate creates an authorized grant with request attribution and returns its Location and ETag.
func (s *Server) handleRoleAssignmentCreate(writer http.ResponseWriter, request *http.Request) {
	var input identity.GrantRoleInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	assignment, err := s.identity.GrantRole(request.Context(), input, requestID(request.Context()))
	if err != nil {
		s.fail(writer, request, catalogIdentityError(err))
		return
	}
	setETag(writer, assignment.Revision)
	writer.Header().Set("Location", "/api/v1/role-assignments/"+assignment.ID)
	s.respondJSON(writer, request, http.StatusCreated, assignment)
}

// handleRoleAssignmentDelete requires reason and If-Match before revoking the retained assignment.
func (s *Server) handleRoleAssignmentDelete(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	reason, err := reasonBody(writer, request, s.maxBodyBytes)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	assignment, err := s.identity.RevokeRole(request.Context(), request.PathValue("assignmentId"), reason, requestID(request.Context()), revision)
	if err != nil {
		s.fail(writer, request, catalogIdentityError(err))
		return
	}
	setETag(writer, assignment.Revision)
	s.respondJSON(writer, request, http.StatusOK, assignment)
}

// catalogIdentityError maps identity errors to control API codes while preserving existing Catalog domain errors.
func catalogIdentityError(err error) error {
	if err == nil {
		return nil
	}
	var domainError *catalog.DomainError
	if errors.As(err, &domainError) {
		return err
	}
	code := identity.ErrorCodeOf(err)
	catalogCode := catalog.CodeInternal
	switch code {
	case identity.CodeInvalidArgument:
		catalogCode = catalog.CodeInvalidArgument
	case identity.CodeUnauthenticated:
		catalogCode = catalog.CodeUnauthenticated
	case identity.CodeForbidden:
		catalogCode = catalog.CodeForbidden
	case identity.CodeNotFound:
		catalogCode = catalog.CodeNotFound
	case identity.CodeConflict:
		catalogCode = catalog.CodeConflict
	case identity.CodePreconditionFailed:
		catalogCode = catalog.CodePreconditionFailed
	case identity.CodeSeparationOfDuties:
		catalogCode = catalog.CodeSeparationOfDuties
	case identity.CodeDependency:
		catalogCode = catalog.CodeDependencyUnavailable
	case identity.CodeInternal:
		catalogCode = catalog.CodeInternal
	}
	return catalog.Wrap(catalogCode, err.Error(), err)
}
