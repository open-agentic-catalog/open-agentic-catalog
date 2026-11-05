// Indexed record lookup preserves the distinction between an absent version and an
// explicitly supplied version value.

package httpserver

import (
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// handleIndexedCatalogRecord preserves absent-versus-empty version identity while reading a current indexed record.
func (s *Server) handleIndexedCatalogRecord(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	present := query.Get("versionPresent")
	if present != "true" && present != "false" || present == "false" && query.Has("version") {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "versionPresent must be true or false; omit version when false"))
		return
	}
	reply, err := s.ReadIndexed(r.Context(), catalog.IndexedIdentity{Identifier: query.Get("identifier"), VersionPresent: present == "true", Version: query.Get("version")})
	s.respondRead(w, r, reply, err)
}
