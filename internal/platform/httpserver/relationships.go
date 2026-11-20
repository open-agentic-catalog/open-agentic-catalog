package httpserver

import (
	"net/http"
	"strconv"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func (s *Server) handleReleaseRelationships(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("releaseId")
	view := r.URL.Query().Get("view")
	page, err := s.parseAdminPage(r, "release-relationships", id, "source-release,ordinal", map[string]string{"view": view})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	request := catalog.RelationshipPageRequest{Limit: page.Limit, View: catalog.RelationshipView(view)}
	if len(page.Position) != 0 {
		if len(page.Position) != 3 || !identity.ValidTenantID(page.Position[0]) {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "invalid relationship cursor"))
			return
		}
		request.AfterReleaseID, request.Generation = page.Position[0], page.Position[2]
		request.AfterOrdinal, err = strconv.Atoi(page.Position[1])
		if err != nil || request.AfterOrdinal < 1 || request.AfterOrdinal > 64 {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "invalid relationship position"))
			return
		}
	}
	selection, err := s.catalog.ListReleaseRelationships(r.Context(), id, request)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reply, err := finishCursorPage(s, page, selection.Items, func(edge catalog.ReleasedRelationship) []string {
		return []string{edge.SourceReleaseID, strconv.Itoa(edge.Ordinal), selection.Generation}
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, reply)
}
