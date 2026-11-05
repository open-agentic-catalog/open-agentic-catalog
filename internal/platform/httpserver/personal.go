package httpserver

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"net/http"
)

type personalAuthority struct {
	local      *catalog.Service
	federation *federation.Manager
}

func (a personalAuthority) Admit(ctx context.Context, ref personal.Reference) error {
	if ref.Origin == "local" {
		r, err := a.local.GetCatalogRecord(ctx, ref.ReleaseID)
		if err != nil {
			return err
		}
		if r.VersionRevision.ID != ref.VersionRevisionID {
			return catalog.E(catalog.CodePreconditionFailed, "saved release revision does not match")
		}
		return nil
	}
	if a.federation == nil {
		return catalog.E(catalog.CodeDependencyUnavailable, "saved source validation is unavailable")
	}
	_, err := a.federation.ArtworkSourceContext(ctx, federation.ArtworkTarget{SourceID: ref.SourceID, PresentationGeneration: ref.PresentationGeneration, Identifier: ref.Identifier, VersionPresent: *ref.VersionPresent, Version: ref.Version, Kind: ref.Kind}, 0, false)
	return err
}
func (s *Server) personalRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /api/v1/me/work", s.withQueryParameters(s.handlePersonalWork, "queue", "limit", "cursor"))
	if s.personal == nil {
		return
	}
	router.HandleFunc("GET /api/v1/personal/items", s.withQueryParameters(s.handlePersonalList, "type", "label", "limit", "cursor"))
	router.HandleFunc("POST /api/v1/personal/items", s.withQueryParameters(s.handlePersonalSave))
	router.HandleFunc("GET /api/v1/personal/items/{itemId}", s.withQueryParameters(s.withUUIDPathParameters(s.handlePersonalGet, "itemId")))
	router.HandleFunc("PATCH /api/v1/personal/items/{itemId}", s.withQueryParameters(s.withUUIDPathParameters(s.handlePersonalSave, "itemId")))
	router.HandleFunc("DELETE /api/v1/personal/items/{itemId}", s.withQueryParameters(s.withUUIDPathParameters(s.handlePersonalSave, "itemId")))
	router.HandleFunc("POST /api/v1/personal/items/{itemId}/resolve", s.withQueryParameters(s.withUUIDPathParameters(s.handlePersonalResolve, "itemId")))
}
func (s *Server) handlePersonalWork(w http.ResponseWriter, r *http.Request) {
	queue := r.URL.Query().Get("queue")
	if queue == "" {
		queue = "drafts"
	}
	page, err := s.parseAdminPage(r, "personal-work", "", "id", map[string]string{"queue": queue})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := catalog.PersonalWorkRequest{Queue: queue, Limit: page.Limit}
	if len(page.Position) > 0 {
		if len(page.Position) != 1 {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "work cursor is invalid"))
			return
		}
		q.AfterID = page.Position[0]
	}
	items, err := s.catalog.ListPersonalWork(r.Context(), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := finishCursorPage(s, page, items, func(i catalog.PersonalWorkItem) []string { return []string{i.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, result)
}
func (s *Server) handlePersonalList(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("type")
	filters := map[string]string{"type": kind}
	var label *string
	if r.URL.Query().Has("label") {
		value := r.URL.Query().Get("label")
		label = &value
		filters["label"] = value
	}
	page, err := s.parseAdminPage(r, "personal-library", "", "id", filters)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := personal.ListRequest{Type: kind, Label: label, Limit: page.Limit}
	if len(page.Position) > 0 {
		if len(page.Position) != 1 {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "saved cursor is invalid"))
			return
		}
		q.AfterID = page.Position[0]
	}
	items, err := s.personal.List(r.Context(), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := finishCursorPage(s, page, items, func(i personal.Item) []string { return []string{i.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, result)
}
func (s *Server) handlePersonalGet(w http.ResponseWriter, r *http.Request) {
	i, err := s.personal.Get(r.Context(), r.PathValue("itemId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setETag(w, i.Revision)
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, i)
}
func (s *Server) handlePersonalSave(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("itemId")
	expected := int64(0)
	var err error
	if id != "" {
		expected, err = expectedRevision(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	var input personal.Input
	remove := r.Method == http.MethodDelete
	if remove && (r.ContentLength != 0 || len(r.TransferEncoding) > 0) {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "saved item removal does not accept a body"))
		return
	}
	if !remove {
		if err := decodeJSON(w, r, min(s.maxBodyBytes, personal.MaxBodyBytes), &input); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	item, err := s.personal.Save(r.Context(), id, expected, input, remove)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == "" {
		status = 201
	}
	setETag(w, item.Revision)
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, status, feedbackReceipt{ID: item.ID, Revision: item.Revision})
}

// Resolve is a read without outbound discovery. It never substitutes an identity.
func (s *Server) handlePersonalResolve(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := decodeJSON(w, r, 1024, &input); err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := s.personal.Get(r.Context(), r.PathValue("itemId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if item.Reference == nil {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "only saved resources can be resolved"))
		return
	}
	ref := *item.Reference
	authority := personalAuthority{s.catalog, s.federation}
	var resolved *catalog.CatalogSearchResult
	err = authority.Admit(r.Context(), ref)
	if err == nil && ref.Origin == "foreign" {
		var value catalog.CatalogSearchResult
		value, err = s.catalogSearch.GetIndexed(r.Context(), catalog.IndexedIdentity{Identifier: ref.Identifier, Version: ref.Version, VersionPresent: *ref.VersionPresent})
		if err == nil && (value.Indexed == nil || value.Indexed.PresentationSourceID != ref.SourceID || value.Indexed.PresentationGeneration != ref.PresentationGeneration || value.Kind != ref.Kind) {
			err = catalog.E(catalog.CodeNotFound, "saved contribution is unavailable")
		}
		if err == nil {
			resolved = &value
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		code := catalog.ErrorCodeOf(err)
		if code == catalog.CodeForbidden || code == catalog.CodeNotFound || code == catalog.CodePreconditionFailed {
			s.respondJSON(w, r, 200, struct {
				Status string `json:"status"`
			}{"unavailable"})
			return
		}
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, 200, struct {
		Status    string                       `json:"status"`
		Reference personal.Reference           `json:"reference"`
		Indexed   *catalog.CatalogSearchResult `json:"indexed,omitempty"`
	}{"available", ref, resolved})
}
