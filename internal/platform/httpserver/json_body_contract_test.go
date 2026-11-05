// Unit tests exercise isolated validation and controlled dependencies.
package httpserver

import (
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestJSONEnvelopePreservesExplicitStandardsPayload(t *testing.T) {
	t.Parallel()
	const body = `{"document":{"futureMember":[null,42,{"extension":true}]}}`
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/command", strings.NewReader(body))
	request.Header.Set("Content-Type", "Application/JSON; charset=utf-8")
	var input struct {
		Document json.RawMessage `json:"document"`
	}
	if err := decodeJSON(httptest.NewRecorder(), request, 256, &input); err != nil || string(input.Document) != `{"futureMember":[null,42,{"extension":true}]}` {
		t.Fatalf("explicit standards payload changed: %s, %v", input.Document, err)
	}
}

func TestControlJSONHandlersRejectInvalidEnvelopesBeforeDomainHandoff(t *testing.T) {
	t.Parallel()
	// Deliberately leave domain services unwired: an invalid envelope must be
	// rejected at the handler, not handed off to a service or external sender.
	server := &Server{logger: slog.New(slog.DiscardHandler), maxBodyBytes: 256}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}})
	for _, operation := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"namespace create", server.handleNamespacesCreate},
		{"namespace update", server.handleNamespacePatch},
		{"publisher create", server.handlePublishersCreate},
		{"publisher update", server.handlePublisherPatch},
		{"publisher domain approve", server.handlePublisherPublicationDomainApprove},
		{"publisher domain revoke", server.handlePublisherPublicationDomainRevoke},
		{"resource create", server.handleResourcesCreate},
		{"resource delete", server.handleResourceDelete},
		{"version create", server.handleVersionCreate},
		{"version revise", server.handleVersionRevisionCreate},
		{"version validate", server.handleVersionRevisionValidate},
		{"listing create", server.handleListingCreate},
		{"listing revise", server.handleListingRevisionCreate},
		{"submission create", server.handleSubmissionCreate},
		{"submission approve", server.handleSubmissionApprove},
		{"listing publish", server.handleListingPublish},
		{"release retire", server.handleReleaseRetire},
		{"catalog search", server.handleCatalogSearch},
		{"catalog explore", server.handleCatalogExplore},
		{"audit export", server.handleAuditExport},
		{"source inspect", server.handleSourceInspect},
		{"source connect", server.handleSourceConnect},
		{"source preview", server.handleSourceChangePreview},
		{"source update", server.handleSourcePatch},
		{"source test", server.handleSourceTest},
		{"source synchronize", server.handleSourceSync},
		{"source pause", server.handleSourcePause},
		{"source resume", server.handleSourceResume},
		{"source remove", server.handleSourceRemove},
		{"observation search", server.handleObservationSearch},
		{"live search", server.handleLiveFederationSearch},
		{"feedback create", server.handleFeedbackCreate},
		{"feedback edit", server.handleFeedbackEdit},
		{"feedback address", func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("feedbackCommand", projectionItem+":address")
			server.handleFeedbackCommand(w, r)
		}},
		{"feedback reopen", func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("feedbackCommand", projectionItem+":reopen")
			server.handleFeedbackCommand(w, r)
		}},
		{"membership transition", func(w http.ResponseWriter, r *http.Request) {
			server.handleMembershipTransition(w, r, identity.MembershipSuspend)
		}},
		{"role grant", server.handleRoleAssignmentCreate},
		{"role revoke", server.handleRoleAssignmentDelete},
		{"job cancel", server.handleJobCancel},
		{"job retry", server.handleJobRetry},
		{"publication build", server.handlePublicationBuild},
		{"publication activate", server.handlePublicationActivate},
		{"publication withdraw", server.handlePublicationUnpublish},
		{"publication rollback", server.handlePublicationRollback},
		{"webhook create", server.handleWebhookCreate},
		{"webhook update", server.handleWebhookPatch},
		{"webhook rotate", server.handleWebhookRotateSecret},
		{"webhook test", server.handleWebhookTest},
	} {
		for _, body := range []string{"{", "null"} {
			t.Run(operation.name+"/"+body, func(t *testing.T) {
				request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/command", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("If-Match", `"rev-1"`)
				response := httptest.NewRecorder()
				operation.handler(response, request)
				var problem struct {
					Code   catalog.ErrorCode `json:"code"`
					Detail string            `json:"detail"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
					t.Fatal(err)
				}
				want := "request body is not unambiguous JSON"
				if body == "null" {
					want = "request body must be a JSON object"
				}
				if response.Code != http.StatusBadRequest || problem.Code != catalog.CodeInvalidArgument || problem.Detail != want {
					t.Fatalf("wrong rejection boundary: status=%d body=%s", response.Code, response.Body)
				}
			})
		}
	}
}
