package httpserver

import (
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func (s *Server) handleReleaseWithdrawalPreview(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	values := request.URL.Query()["transition"]
	if len(values) != 1 || values[0] == "" {
		s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "transition must be one non-empty value"))
		return
	}
	preview, err := s.catalog.PreviewReleaseWithdrawal(request.Context(), request.PathValue("releaseId"), catalog.ReleaseState(values[0]))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, preview)
}
