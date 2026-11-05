// Feedback routes expose private current reads and text-free command receipts.
package httpserver

import (
	"net/http"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// feedbackRoutes registers explicit own and administrator operations.
func (s *Server) feedbackRoutes(router *http.ServeMux) {
	if s.feedback == nil {
		return
	}
	router.HandleFunc("GET /api/v1/overview/feedback", s.withQueryParameters(s.handleOverviewFeedback))
	router.HandleFunc("GET /api/v1/feedback", s.withQueryParameters(s.handleFeedbackList, "resourceId", "category", "state", "cursor", "limit"))
	router.HandleFunc("GET /api/v1/feedback/inbox", s.withQueryParameters(s.handleFeedbackList, "resourceId", "category", "state", "cursor", "limit"))
	router.HandleFunc("POST /api/v1/feedback", s.withQueryParameters(s.handleFeedbackCreate))
	router.HandleFunc("GET /api/v1/feedback/{feedbackId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleFeedbackGet, "feedbackId")))
	router.HandleFunc("PATCH /api/v1/feedback/{feedbackId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleFeedbackEdit, "feedbackId")))
	router.HandleFunc("POST /api/v1/feedback/{feedbackCommand}", s.withQueryParameters(s.handleFeedbackCommand))
}

// handleFeedbackList binds filters and authorization to a signed continuation.
func (s *Server) handleFeedbackList(w http.ResponseWriter, r *http.Request) {
	inbox := r.URL.Path == "/api/v1/feedback/inbox"
	filters := map[string]string{"resourceId": r.URL.Query().Get("resourceId"), "category": r.URL.Query().Get("category"), "state": r.URL.Query().Get("state")}
	purpose := "feedback-own"
	if inbox {
		purpose = "feedback-inbox"
	}
	p, err := s.parseAdminPage(r, purpose, "", "id", filters)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	input := feedback.ListInput{ResourceID: filters["resourceId"], Category: filters["category"], State: filters["state"], Limit: p.Limit}
	if len(p.Position) > 0 {
		if len(p.Position) != 1 {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		input.AfterID = p.Position[0]
	}
	items, err := s.feedback.List(r.Context(), input, inbox)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := finishCursorPage(s, p, items, func(item feedback.Record) []string { return []string{item.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, http.StatusOK, page)
}

// handleFeedbackGet returns authorized current content, never a replay receipt.
func (s *Server) handleFeedbackGet(w http.ResponseWriter, r *http.Request) {
	item, err := s.feedback.Get(r.Context(), r.PathValue("feedbackId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setETag(w, item.Revision)
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, http.StatusOK, item)
}

// feedbackReceipt excludes private text from durable idempotency responses.
type feedbackReceipt struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}

// respondFeedback sends only the original command identity and revision.
func (s *Server) respondFeedback(w http.ResponseWriter, r *http.Request, item feedback.Record, status int) {
	setETag(w, item.Revision)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/api/v1/feedback/"+item.ID)
	s.respondJSON(w, r, status, feedbackReceipt{item.ID, item.Revision})
}

// handleFeedbackCreate derives membership and retained context in the service.
func (s *Server) handleFeedbackCreate(w http.ResponseWriter, r *http.Request) {
	var input feedback.CreateInput
	if err := decodeJSON(w, r, min(s.maxBodyBytes, feedback.MaxBodyBytes), &input); err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := s.feedback.Create(r.Context(), input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondFeedback(w, r, item, http.StatusCreated)
}

// handleFeedbackEdit uses the feedback ETag, not the parent Resource revision.
func (s *Server) handleFeedbackEdit(w http.ResponseWriter, r *http.Request) {
	expected, err := expectedRevision(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var input feedback.EditInput
	if err := decodeJSON(w, r, min(s.maxBodyBytes, feedback.MaxBodyBytes), &input); err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := s.feedback.Change(r.Context(), r.PathValue("feedbackId"), expected, "edit", input, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondFeedback(w, r, item, http.StatusOK)
}

// handleFeedbackCommand separates admin actions from submitter withdrawal.
func (s *Server) handleFeedbackCommand(w http.ResponseWriter, r *http.Request) {
	id, action, ok := strings.Cut(r.PathValue("feedbackCommand"), ":")
	if ok && !identity.ValidTenantID(id) {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "feedbackId must be a UUID"))
		return
	}
	if !ok || (action != "address" && action != "reopen" && action != "remove" && action != "withdraw") {
		s.fail(w, r, catalog.E(catalog.CodeNotFound, "feedback command was not found"))
		return
	}
	expected, err := expectedRevision(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	response := ""
	if action == "address" {
		var input struct {
			Response string `json:"response"`
		}
		if err := decodeJSON(w, r, min(s.maxBodyBytes, feedback.MaxBodyBytes), &input); err != nil {
			s.fail(w, r, err)
			return
		}
		response = input.Response
	} else {
		var input struct{}
		if err := decodeJSON(w, r, min(s.maxBodyBytes, feedback.MaxBodyBytes), &input); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	item, err := s.feedback.Change(r.Context(), id, expected, action, feedback.EditInput{}, response)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondFeedback(w, r, item, http.StatusOK)
}
