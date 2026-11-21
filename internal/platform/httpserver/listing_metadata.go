package httpserver

import (
	"net/http"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func (s *Server) handleListingMetadataGaps(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	values := r.URL.Query()
	checks := catalog.ListingMetadataChecks{Documentation: true, Support: true, License: true}
	if values.Has("checks") {
		var err error
		checks, err = catalog.ParseListingMetadataChecks(values.Get("checks"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	ns := strings.ToLower(values.Get("namespaceId"))
	kind := catalog.Kind(values.Get("kind"))
	if values.Has("namespaceId") && !identity.ValidTenantID(ns) || values.Has("kind") && !kind.Valid() {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "invalid namespaceId or kind"))
		return
	}
	page, err := s.parseAdminPage(r, "listing-metadata-gaps", "", "listing-id", map[string]string{"checks": strings.Join(checks.Fields(), ","), "namespaceId": ns, "kind": string(kind)})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := catalog.ListingMetadataQuery{Checks: checks, NamespaceID: ns, Kind: kind, Limit: page.Limit}
	if len(page.Position) > 0 {
		if len(page.Position) != 1 || !identity.ValidTenantID(page.Position[0]) {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "invalid Listing metadata cursor"))
			return
		}
		q.AfterID = page.Position[0]
	}
	items, err := s.catalog.ListListingMetadataGaps(r.Context(), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := finishCursorPage(s, page, items, func(item catalog.ListingMetadataGap) []string { return []string{item.ListingID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, result)
}
