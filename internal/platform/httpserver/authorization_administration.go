package httpserver

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"net/http"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func authorizationPageInputs(request *http.Request) (int, string, error) {
	limit, err := parseLimit(request, 20)
	if err != nil {
		return 0, "", err
	}
	query, err := parseQueryParameters(request)
	if err != nil {
		return 0, "", err
	}
	values := query["cursor"]
	if len(values) > 1 || len(values) == 1 && (values[0] == "" || len(values[0]) > 2048) {
		return 0, "", catalog.E(catalog.CodeInvalidArgument, "cursor must be one nonempty bounded value")
	}
	return limit, query.Get("cursor"), nil
}
func (s *Server) handlePermissionCatalog(w http.ResponseWriter, r *http.Request) {
	s.respondJSON(w, r, http.StatusOK, identity.Permissions())
}
func (s *Server) handlePermissionDecisions(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Checks []identity.PermissionCheck `json:"checks"`
	}
	if err := decodeJSON(w, r, s.maxBodyBytes, &input); err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := s.identity.CurrentPermissionDecisions(r.Context(), input.Checks)
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	s.respondJSON(w, r, http.StatusOK, result)
}
func (s *Server) handleEffectiveAccess(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := authorizationPageInputs(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.identity.CurrentAccessAssignments(r.Context(), limit, cursor)
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	s.respondJSON(w, r, http.StatusOK, page)
}
func (s *Server) handleDefinitionsList(w http.ResponseWriter, r *http.Request) {
	if err := validateDirectoryQuery(r, "name", "state", "key"); err != nil {
		s.fail(w, r, err)
		return
	}
	limit, cursor, err := authorizationPageInputs(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.identity.ListRoleDefinitions(r.Context(), limit, cursor, identity.DirectoryFilter{Name: r.URL.Query().Get("name"), State: r.URL.Query().Get("state"), Key: r.URL.Query().Get("key")})
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	s.respondJSON(w, r, http.StatusOK, page)
}
func (s *Server) handleDefinitionGet(w http.ResponseWriter, r *http.Request) {
	value, err := s.identity.GetRoleDefinition(r.Context(), r.PathValue("roleId"))
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	setETag(w, value.Revision)
	s.respondJSON(w, r, http.StatusOK, value)
}
func (s *Server) handleDefinitionCreate(w http.ResponseWriter, r *http.Request) {
	s.writeDefinition(w, r, 0, false)
}
func (s *Server) handleDefinitionUpdate(w http.ResponseWriter, r *http.Request) {
	revision, err := expectedRevision(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeDefinition(w, r, revision, false)
}
func (s *Server) writeDefinition(w http.ResponseWriter, r *http.Request, revision int64, archive bool) {
	var input identity.RoleDefinitionInput
	if archive {
		reason, err := reasonBody(w, r, s.maxBodyBytes)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		input.Reason = reason
	} else if err := decodeJSON(w, r, s.maxBodyBytes, &input); err != nil {
		s.fail(w, r, err)
		return
	}
	value, err := s.identity.SaveRoleDefinition(r.Context(), r.PathValue("roleId"), revision, input, archive, requestID(r.Context()))
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	code := http.StatusOK
	if revision == 0 {
		code = http.StatusCreated
		w.Header().Set("Location", "/api/v1/access/roles/"+value.ID)
	}
	setETag(w, value.Revision)
	s.respondJSON(w, r, code, value)
}
func (s *Server) dispatchDefinitionCommand(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutSuffix(r.PathValue("roleCommand"), ":archive")
	if !ok {
		writeProblem(w, r, catalog.E(catalog.CodeNotFound, "role command was not found"))
		return
	}
	if !s.setUUIDPathValue(w, r, "roleId", id) {
		return
	}
	revision, err := expectedRevision(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeDefinition(w, r, revision, true)
}
func (s *Server) handleDefinitionAssignments(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := authorizationPageInputs(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.identity.ListAuthorizationAssignments(r.Context(), identity.AuthorizationPageRequest{RoleDefinitionID: r.PathValue("roleId")}, limit, cursor)
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	s.respondJSON(w, r, http.StatusOK, page)
}
func (s *Server) handleAccessGroupsList(w http.ResponseWriter, r *http.Request) {
	if err := validateDirectoryQuery(r, "name", "state"); err != nil {
		s.fail(w, r, err)
		return
	}
	limit, cursor, err := authorizationPageInputs(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.identity.ListAccessGroups(r.Context(), limit, cursor, identity.DirectoryFilter{Name: r.URL.Query().Get("name"), State: r.URL.Query().Get("state")})
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	s.respondJSON(w, r, http.StatusOK, page)
}
func (s *Server) handleAccessGroupGet(w http.ResponseWriter, r *http.Request) {
	value, err := s.identity.GetAccessGroup(r.Context(), r.PathValue("groupId"))
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	setETag(w, value.Revision)
	s.respondJSON(w, r, http.StatusOK, value)
}
func (s *Server) handleAccessGroupCreate(w http.ResponseWriter, r *http.Request) {
	s.writeAccessGroup(w, r, 0, "")
}
func (s *Server) handleAccessGroupUpdate(w http.ResponseWriter, r *http.Request) {
	revision, err := expectedRevision(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeAccessGroup(w, r, revision, "")
}
func (s *Server) writeAccessGroup(w http.ResponseWriter, r *http.Request, revision int64, transition identity.GroupState) {
	var input identity.GroupInput
	if transition != "" {
		reason, err := reasonBody(w, r, s.maxBodyBytes)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		input.Reason = reason
	} else if err := decodeJSON(w, r, s.maxBodyBytes, &input); err != nil {
		s.fail(w, r, err)
		return
	}
	value, err := s.identity.SaveAccessGroup(r.Context(), r.PathValue("groupId"), revision, input, transition, requestID(r.Context()))
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	code := http.StatusOK
	if revision == 0 {
		code = http.StatusCreated
		w.Header().Set("Location", "/api/v1/access/groups/"+value.ID)
	}
	setETag(w, value.Revision)
	s.respondJSON(w, r, code, value)
}
func (s *Server) dispatchAccessGroupCommand(w http.ResponseWriter, r *http.Request) {
	for suffix, state := range map[string]identity.GroupState{":suspend": identity.GroupSuspended, ":reactivate": identity.GroupActive, ":archive": identity.GroupArchived} {
		if id, ok := strings.CutSuffix(r.PathValue("groupCommand"), suffix); ok {
			if !s.setUUIDPathValue(w, r, "groupId", id) {
				return
			}
			revision, err := expectedRevision(r)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			s.writeAccessGroup(w, r, revision, state)
			return
		}
	}
	writeProblem(w, r, catalog.E(catalog.CodeNotFound, "group command was not found"))
}
func (s *Server) handleGroupMembersList(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := authorizationPageInputs(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.identity.ListAccessGroupMembers(r.Context(), r.PathValue("groupId"), limit, cursor)
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	s.respondJSON(w, r, http.StatusOK, page)
}
func (s *Server) handleGroupMemberCreate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MembershipID string `json:"membershipId"`
		Reason       string `json:"reason"`
	}
	if err := decodeJSON(w, r, s.maxBodyBytes, &input); err != nil {
		s.fail(w, r, err)
		return
	}
	value, err := s.identity.ChangeAccessGroupMember(r.Context(), r.PathValue("groupId"), "", input.MembershipID, 0, input.Reason, requestID(r.Context()), false)
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	setETag(w, value.Revision)
	s.respondJSON(w, r, http.StatusCreated, value)
}
func (s *Server) dispatchGroupMemberCommand(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutSuffix(r.PathValue("memberCommand"), ":revoke")
	if !ok {
		writeProblem(w, r, catalog.E(catalog.CodeNotFound, "group member command was not found"))
		return
	}
	if !s.setUUIDPathValue(w, r, "memberId", id) {
		return
	}
	revision, err := expectedRevision(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonBody(w, r, s.maxBodyBytes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	value, err := s.identity.ChangeAccessGroupMember(r.Context(), r.PathValue("groupId"), id, "", revision, reason, requestID(r.Context()), true)
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	setETag(w, value.Revision)
	s.respondJSON(w, r, http.StatusOK, value)
}
func (s *Server) handleAccessAssignmentsList(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := authorizationPageInputs(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	page, err := s.identity.ListAuthorizationAssignments(r.Context(), identity.AuthorizationPageRequest{RoleDefinitionID: q.Get("roleDefinitionId"), MembershipID: q.Get("membershipId"), GroupID: q.Get("groupId")}, limit, cursor)
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	s.respondJSON(w, r, http.StatusOK, page)
}
func (s *Server) handleAccessAssignmentCreate(w http.ResponseWriter, r *http.Request) {
	var input identity.AccessAssignmentInput
	if err := decodeJSON(w, r, s.maxBodyBytes, &input); err != nil {
		s.fail(w, r, err)
		return
	}
	value, err := s.identity.GrantAuthorizationAssignment(r.Context(), input, requestID(r.Context()))
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	setETag(w, value.Revision)
	s.respondJSON(w, r, http.StatusCreated, value)
}
func (s *Server) dispatchAccessAssignmentCommand(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutSuffix(r.PathValue("accessAssignmentCommand"), ":revoke")
	if !ok {
		writeProblem(w, r, catalog.E(catalog.CodeNotFound, "assignment command was not found"))
		return
	}
	if !s.setUUIDPathValue(w, r, "assignmentId", id) {
		return
	}
	revision, err := expectedRevision(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonBody(w, r, s.maxBodyBytes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	value, err := s.identity.RevokeAuthorizationAssignment(r.Context(), id, revision, reason, requestID(r.Context()))
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	setETag(w, value.Revision)
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) accessReplayAdmission(r *http.Request, authority idempotency.ReplayAuthority, emit func()) error {
	if authority.Version == 1 && authority.ValidationProblem && len(authority.Checks) == 0 && authority.JobID == "" {
		// The original safe parser error disclosed no domain data. Fresh
		// authentication/OAuth already ran; require a current retained member.
		if _, err := identity.CurrentDecisionDigest(r.Context()); err != nil {
			return catalogIdentityError(err)
		}
		emit()
		return nil
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/access/") {
		return catalogIdentityError(s.identity.WithAdministrationDisclosure(r.Context(), func(context.Context) error { emit(); return nil }))
	}
	if authority.Version == 1 && s.ratings != nil && strings.HasPrefix(r.URL.Path, "/api/v1/ratings") {
		// Recheck the selected source or local publication before disclosing a
		// submission receipt. A replay never submits a second opinion.
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/ratings" {
			var input ratingSubmitRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				return catalog.E(catalog.CodeForbidden, "rating replay target is unavailable")
			}
			admission, err := s.ratingAdmission(r.Context(), ratingRequest{input.Target, input.ReleaseID, input.Receipt})
			if err != nil {
				return err
			}
			if _, err = s.ratings.Read(r.Context(), admission); err != nil {
				return err
			}
		}
		if strings.HasSuffix(r.URL.Path, ":withdraw") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/ratings/"), ":withdraw")
			// Owner disposal does not require resource visibility or a retained
			// ratings grant. Reauthenticate membership and ownership, as for GET.
			if _, err := identity.CurrentDecisionDigest(r.Context()); err != nil {
				return catalogIdentityError(err)
			}
			return func(ctx context.Context) error {
				a, _ := identity.FromContext(ctx)
				owner, err := ratings.PrincipalKey(a)
				if err != nil {
					return err
				}
				value, err := s.ratings.Get(ctx, id)
				if err != nil {
					return err
				}
				if value.PrincipalKey != owner {
					return catalog.E(catalog.CodeNotFound, "rating was not found")
				}
				emit()
				return nil
			}(r.Context())
		}
	}
	if authority.Version != 1 || len(authority.Checks) == 0 {
		return catalog.E(catalog.CodeForbidden, "retained command has no current disclosure authority")
	}
	actor, ok := identity.FromContext(r.Context())
	if !ok || authority.HumanOnly && actor.ActorType != identity.ActorHuman {
		return catalog.E(catalog.CodeForbidden, "current command actor is not permitted")
	}
	return catalogIdentityError(identity.WithReplayDisclosure(r.Context(), authority.Checks, func(ctx context.Context) error {
		if authority.JobID != "" {
			if s.jobs == nil {
				return catalog.E(catalog.CodeForbidden, "current Job authority is unavailable")
			}
			if _, err := s.jobs.Get(ctx, authority.JobID); err != nil {
				return err
			}
		}
		emit()
		return nil
	}))
}
