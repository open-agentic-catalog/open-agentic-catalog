package httpserver

import (
	"fmt"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/collections"
)

func (s *Server) collectionRoutes(router *http.ServeMux) {
	if s.collections == nil {
		return
	}
	router.HandleFunc("GET /api/v1/collections", s.withQueryParameters(s.handleCollectionList, "namespaceId", "limit", "cursor"))
	router.HandleFunc("POST /api/v1/collections", s.withQueryParameters(s.handleCollectionCreate))
	router.HandleFunc("GET /api/v1/collections/{collectionId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleCollectionGet, "collectionId")))
	router.HandleFunc("PATCH /api/v1/collections/{collectionId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleCollectionChange, "collectionId")))
	router.HandleFunc("DELETE /api/v1/collections/{collectionId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleCollectionChange, "collectionId")))
	router.HandleFunc("GET /api/v1/collections/{collectionId}/members", s.withQueryParameters(s.withUUIDPathParameters(s.handleCollectionMembers, "collectionId"), "limit", "cursor"))
	router.HandleFunc("POST /api/v1/collections/{collectionId}/members", s.withQueryParameters(s.withUUIDPathParameters(s.handleCollectionChange, "collectionId")))
	router.HandleFunc("DELETE /api/v1/collections/{collectionId}/members/{listingId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleCollectionChange, "collectionId", "listingId")))
}
func (s *Server) handleCollectionList(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespaceId")
	page, err := s.parseAdminPage(r, "shared-collections", "", "id", map[string]string{"namespaceId": ns})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := collections.ListRequest{NamespaceID: ns, Limit: page.Limit}
	if len(page.Position) > 0 {
		if len(page.Position) != 1 {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "collection cursor is invalid"))
			return
		}
		q.AfterID = page.Position[0]
	}
	items, err := s.collections.List(r.Context(), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out, err := finishCursorPage(s, page, items, func(c collections.Collection) []string { return []string{c.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, out)
}
func (s *Server) handleCollectionGet(w http.ResponseWriter, r *http.Request) {
	item, err := s.collections.Get(r.Context(), r.PathValue("collectionId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setETag(w, item.Revision)
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, item)
}
func (s *Server) collectionReceipt(w http.ResponseWriter, r *http.Request, status int, item collections.Collection) {
	setETag(w, item.Revision)
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, status, feedbackReceipt{ID: item.ID, Revision: item.Revision})
}
func (s *Server) handleCollectionCreate(w http.ResponseWriter, r *http.Request) {
	var input collections.Input
	if err := decodeJSON(w, r, min(s.maxBodyBytes, collections.MaxBodyBytes), &input); err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := s.collections.Create(r.Context(), input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.collectionReceipt(w, r, 201, item)
}
func (s *Server) handleCollectionChange(w http.ResponseWriter, r *http.Request) {
	expected, err := expectedRevision(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	op := collections.Update
	patch := collections.Patch{}
	listing := r.PathValue("listingId")
	switch r.Method {
	case http.MethodPatch:
		var input struct {
			Name        strictOptionalString `json:"name,omitempty"`
			Description strictOptionalString `json:"description,omitempty"`
		}
		if err := decodeJSON(w, r, min(s.maxBodyBytes, collections.MaxBodyBytes), &input); err != nil {
			s.fail(w, r, err)
			return
		}
		if input.Name.Present {
			patch.Name = &input.Name.Value
		}
		if input.Description.Present {
			patch.Description = &input.Description.Value
		}
	case http.MethodPost:
		op = collections.Add
		var input struct {
			ListingID string `json:"listingId"`
		}
		if err := decodeJSON(w, r, min(s.maxBodyBytes, collections.MaxMemberBodyBytes), &input); err != nil {
			s.fail(w, r, err)
			return
		}
		listing = input.ListingID
	case http.MethodDelete:
		op = collections.Delete
		if listing != "" {
			op = collections.Remove
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "collection deletion does not accept a body"))
			return
		}
	}
	item, err := s.collections.Change(r.Context(), r.PathValue("collectionId"), expected, op, patch, listing)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if op == collections.Add {
		status = 201
	}
	s.collectionReceipt(w, r, status, item)
}
func (s *Server) handleCollectionMembers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("collectionId")
	snapshot, err := s.collections.Members(r.Context(), id, collections.MemberRequest{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// This binding is server-owned; it is not an accepted query parameter.
	parent := fmt.Sprintf("%s:%d:%s", id, snapshot.Collection.Revision, snapshot.Generation)
	page, err := s.parseAdminPage(r, "shared-collection-members", parent, "listing-id", nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := collections.MemberRequest{Limit: page.Limit, Revision: snapshot.Collection.Revision, Generation: snapshot.Generation}
	if len(page.Position) > 0 {
		if len(page.Position) != 1 {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "collection cursor is invalid"))
			return
		}
		q.AfterID = page.Position[0]
	}
	selected, err := s.collections.Members(r.Context(), id, q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out, err := finishCursorPage(s, page, selected.Items, func(item catalog.CatalogSummary) []string { return []string{item.Listing.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, out)
}
