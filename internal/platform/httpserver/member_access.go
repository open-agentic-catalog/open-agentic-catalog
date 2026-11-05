package httpserver

import (
	"net/http"
	"slices"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func validateDirectoryQuery(r *http.Request, fields ...string) error {
	query, err := parseQueryParameters(r)
	if err != nil {
		return err
	}
	for name, values := range query {
		if name == "limit" || name == "cursor" {
			continue
		}
		if !slices.Contains(fields, name) || len(values) != 1 || values[0] == "" || len(values[0]) > 2048 {
			return catalog.E(catalog.CodeInvalidArgument, "directory filters must be known single nonempty bounded values")
		}
	}
	return nil
}

func (s *Server) handleMemberAccess(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := authorizationPageInputs(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.identity.MemberAccess(r.Context(), r.PathValue("membershipId"), limit, cursor)
	if err != nil {
		s.fail(w, r, catalogIdentityError(err))
		return
	}
	s.respondJSON(w, r, http.StatusOK, page)
}
