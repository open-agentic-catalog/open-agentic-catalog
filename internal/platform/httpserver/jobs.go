// Job handlers expose bounded history and authorized get, cancel and retry
// operations without claiming or executing work.

package httpserver

import (
	"net/http"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

// handleJobsList returns bounded newest-first job projections without private worker fields.
func (s *Server) handleJobsList(writer http.ResponseWriter, request *http.Request) {
	query, err := parseQueryParameters(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	filters := map[string]string{}
	for _, name := range []string{"kind", "state", "parentJobId"} {
		if values, present := query[name]; present {
			if len(values) != 1 || values[0] == "" {
				s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, name+" must be one non-empty value"))
				return
			}
			filters[name] = values[0]
		}
	}
	pageRequest, err := s.parseAdminPage(request, "jobs", "", "createdAt desc,id desc", filters)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := jobs.ListRequest{Limit: pageRequest.Limit, Kind: filters["kind"], State: jobs.State(filters["state"]), ParentJobID: filters["parentJobId"]}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 2 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		position, parseErr := time.Parse(time.RFC3339Nano, pageRequest.Position[0])
		if parseErr != nil {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterCreatedAt, listRequest.AfterID = &position, pageRequest.Position[1]
	}
	items, err := s.jobs.List(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item jobs.View) []string { return []string{item.CreatedAt.UTC().Format(time.RFC3339Nano), item.ID} })
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleJobGet projects one authorized job through its public view contract.
func (s *Server) handleJobGet(writer http.ResponseWriter, request *http.Request) {
	item, err := s.jobs.Get(request.Context(), request.PathValue("jobId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handleJobCancel passes an explicit audit reason to cancellation and returns the resulting public state.
func (s *Server) handleJobCancel(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.jobs.Cancel(request.Context(), request.PathValue("jobId"), input.Reason)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, jobs.PublicView(item))
}

// handleJobRetry requests retry with an audit reason and returns the accepted job Location.
func (s *Server) handleJobRetry(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.jobs.Retry(request.Context(), request.PathValue("jobId"), input.Reason)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("Location", "/api/v1/jobs/"+item.ID)
	s.respondJSON(writer, request, http.StatusAccepted, jobs.PublicView(item))
}
