// Webhook handlers manage tenant endpoints, test delivery, bounded attempt history
// and durable reconciliation checkpoints.

package httpserver

import (
	"net/http"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

// handleWebhookReconciliation returns authorized durable notification counters for receiver reconciliation.
func (s *Server) handleWebhookReconciliation(writer http.ResponseWriter, request *http.Request) {
	state, err := s.webhooks.NotificationState(request.Context())
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, state)
}

// handleWebhooksList pages endpoint metadata by name/ID with authorization-bound continuation.
func (s *Server) handleWebhooksList(writer http.ResponseWriter, request *http.Request) {
	filters, err := webhookPageFilters(request, "name", "status")
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	pageRequest, err := s.parseAdminPage(request, "webhooks", "", "name,id", filters)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := webhook.EndpointListRequest{Limit: pageRequest.Limit, Name: filters["name"], Status: webhook.Status(filters["status"])}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 2 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterName, listRequest.AfterID = pageRequest.Position[0], pageRequest.Position[1]
	}
	items, err := s.webhooks.List(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item webhook.Endpoint) []string { return []string{item.Name, item.ID} })
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleWebhookCreate returns the created endpoint and signing-secret result with its Location and revision ETag.
func (s *Server) handleWebhookCreate(writer http.ResponseWriter, request *http.Request) {
	var input webhook.CreateInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	result, err := s.webhooks.Create(request.Context(), input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, result.Endpoint.Revision)
	writer.Header().Set("Location", "/api/v1/webhooks/"+result.Endpoint.ID)
	s.respondJSON(writer, request, http.StatusCreated, result)
}

// handleWebhookGet reads one authorized endpoint's metadata and current revision ETag.
func (s *Server) handleWebhookGet(writer http.ResponseWriter, request *http.Request) {
	endpoint, err := s.webhooks.Get(request.Context(), request.PathValue("webhookId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, endpoint.Revision)
	s.respondJSON(writer, request, http.StatusOK, endpoint)
}

// handleWebhookPatch requires If-Match before applying closed endpoint settings.
func (s *Server) handleWebhookPatch(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	var input webhook.PatchInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	endpoint, err := s.webhooks.Update(request.Context(), request.PathValue("webhookId"), input, revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, endpoint.Revision)
	s.respondJSON(writer, request, http.StatusOK, endpoint)
}

// handleWebhookDelete disables the endpoint against its current revision and returns no content.
func (s *Server) handleWebhookDelete(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	if _, err := s.webhooks.Disable(request.Context(), request.PathValue("webhookId"), revision); err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// handleWebhookRotateSecret requires endpoint revision fencing and decodes optional key-rotation settings.
func (s *Server) handleWebhookRotateSecret(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	var input webhook.RotateInput
	if err := decodeOptionalEmptyObject(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	result, err := s.webhooks.RotateSecret(request.Context(), request.PathValue("webhookId"), revision, input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, result.Endpoint.Revision)
	s.respondJSON(writer, request, http.StatusOK, result)
}

// handleWebhookTest requests an explicit test delivery against the current endpoint revision.
func (s *Server) handleWebhookTest(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	if err := decodeOptionalEmptyObject(writer, request, s.maxBodyBytes, &struct{}{}); err != nil {
		s.fail(writer, request, err)
		return
	}
	delivery, err := s.webhooks.Test(request.Context(), request.PathValue("webhookId"), revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, delivery)
}

// handleWebhookDeliveries pages delivery evidence with continuation bound to the exact endpoint.
func (s *Server) handleWebhookDeliveries(writer http.ResponseWriter, request *http.Request) {
	endpointID := request.PathValue("webhookId")
	filters, err := webhookPageFilters(request, "deliveryId", "eventId", "state")
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	pageRequest, err := s.parseAdminPage(request, "webhook-deliveries", endpointID, "updatedAt desc,id desc", filters)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := webhook.DeliveryListRequest{EndpointID: endpointID, Limit: pageRequest.Limit, DeliveryID: filters["deliveryId"], EventID: filters["eventId"], State: webhook.DeliveryState(filters["state"])}
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
		listRequest.AfterUpdatedAt, listRequest.AfterID = &position, pageRequest.Position[1]
	}
	items, err := s.webhooks.ListDeliveries(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item webhook.Delivery) []string {
		return []string{item.UpdatedAt.UTC().Format(time.RFC3339Nano), item.ID}
	})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// webhookPageFilters rejects explicit empty filters and binds normalized UUIDs.
// Every supported filter participates in continuation binding.
func webhookPageFilters(request *http.Request, names ...string) (map[string]string, error) {
	query, err := parseQueryParameters(request)
	if err != nil {
		return nil, err
	}
	filters := make(map[string]string)
	for _, name := range names {
		if query.Has(name) {
			value := query.Get(name)
			if value == "" {
				return nil, catalog.E(catalog.CodeInvalidArgument, "webhook filters must not be empty")
			}
			if name == "deliveryId" || name == "eventId" {
				value = strings.ToLower(value)
			}
			filters[name] = value
		} else {
			// The parser allowlist requires keys even when no value is selected.
			filters[name] = ""
		}
	}
	return filters, nil
}
