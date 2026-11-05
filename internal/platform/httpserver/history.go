// History handlers bind bounded cursors to exact resource parents and return
// immutable version, review and release metadata.

package httpserver

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"net/http"
)

// historyPosition requires a single UUID continuation position for bounded history queries.
func historyPosition(page adminPageRequest) (catalog.HistoryPageRequest, error) {
	request := catalog.HistoryPageRequest{Limit: page.Limit}
	if len(page.Position) > 0 {
		if len(page.Position) != 1 || !identity.ValidTenantID(page.Position[0]) {
			return request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid")
		}
		request.AfterID = page.Position[0]
	}
	return request, nil
}

// handleVersionsList pages versions with continuation bound to the exact resource parent.
func (s *Server) handleVersionsList(w http.ResponseWriter, r *http.Request) {
	page, err := s.parseAdminPage(r, "history-Versions", r.PathValue("resourceId"), "id", nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	request, err := historyPosition(page)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := s.catalog.ListVersions(r.Context(), r.PathValue("resourceId"), request)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reply, err := finishCursorPage(s, page, items, func(item catalog.ResourceVersion) []string { return []string{item.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, reply)
}

// handleVersionRevisionsList pages immutable revision summaries for one version without loading full evidence.
func (s *Server) handleVersionRevisionsList(w http.ResponseWriter, r *http.Request) {
	page, err := s.parseAdminPage(r, "history-VersionRevisions", r.PathValue("versionId"), "id", nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	request, err := historyPosition(page)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := s.catalog.ListVersionRevisions(r.Context(), r.PathValue("versionId"), request)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reply, err := finishCursorPage(s, page, items, func(item catalog.RevisionSummary) []string { return []string{item.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, reply)
}

// handleListingsList pages listing identities with continuation bound to the exact resource parent.
func (s *Server) handleListingsList(w http.ResponseWriter, r *http.Request) {
	page, err := s.parseAdminPage(r, "history-Listings", r.PathValue("resourceId"), "id", nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	request, err := historyPosition(page)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := s.catalog.ListListings(r.Context(), r.PathValue("resourceId"), request)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reply, err := finishCursorPage(s, page, items, func(item catalog.CatalogListing) []string { return []string{item.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, reply)
}

// handleListingRevisionsList pages immutable listing revision summaries under their exact listing parent.
func (s *Server) handleListingRevisionsList(w http.ResponseWriter, r *http.Request) {
	page, err := s.parseAdminPage(r, "history-ListingRevisions", r.PathValue("listingId"), "id", nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	request, err := historyPosition(page)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := s.catalog.ListListingRevisions(r.Context(), r.PathValue("listingId"), request)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reply, err := finishCursorPage(s, page, items, func(item catalog.RevisionSummary) []string { return []string{item.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, reply)
}

// handleReleasesList pages retained releases under one listing with parent-bound continuation.
func (s *Server) handleReleasesList(w http.ResponseWriter, r *http.Request) {
	page, err := s.parseAdminPage(r, "history-Releases", r.PathValue("listingId"), "id", nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	request, err := historyPosition(page)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := s.catalog.ListReleases(r.Context(), r.PathValue("listingId"), request)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reply, err := finishCursorPage(s, page, items, func(item catalog.ListingRelease) []string { return []string{item.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, reply)
}

// handleSubmissionsList pages review history with listing/state filters bound into its cursor.
func (s *Server) handleSubmissionsList(w http.ResponseWriter, r *http.Request) {
	filters := map[string]string{"listingId": r.URL.Query().Get("listingId"), "state": r.URL.Query().Get("state"), "unreleased": r.URL.Query().Get("unreleased")}
	if _, exists := r.URL.Query()["unreleased"]; exists && filters["unreleased"] != "true" && filters["unreleased"] != "false" {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "unreleased must be true or false"))
		return
	}
	page, err := s.parseAdminPage(r, "review-submissions", "", "id", filters)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	position, err := historyPosition(page)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := s.catalog.ListSubmissions(r.Context(), catalog.SubmissionListRequest{HistoryPageRequest: position, ListingID: filters["listingId"], State: catalog.SubmissionState(filters["state"]), Unreleased: filters["unreleased"] == "true"})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reply, err := finishCursorPage(s, page, items, func(item catalog.ReviewSubmission) []string { return []string{item.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, reply)
}

// handleSubmissionEvidenceGet returns the selected submission's retained validation evidence through domain authorization.
func (s *Server) handleSubmissionEvidenceGet(w http.ResponseWriter, r *http.Request) {
	item, err := s.catalog.GetSubmissionEvidence(r.Context(), r.PathValue("submissionId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, item)
}
