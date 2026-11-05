package httpserver

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestServerCommandDispatchAdmission(t *testing.T) {
	t.Parallel()
	s := projectionServer(nil)
	for _, operation := range []struct {
		command, key string
		handler      http.HandlerFunc
		actions      []string
	}{
		{"revisionCommand", "revisionId", s.dispatchVersionRevisionCommand, []string{"validate"}},
		{"domainCommand", "domain", s.dispatchPublisherPublicationDomainCommand, []string{"approve", "revoke"}},
		{"submissionCommand", "submissionId", s.dispatchSubmissionCommand, []string{"approve", "request-changes", "reject"}},
		{"listingCommand", "listingId", s.dispatchListingCommand, []string{"publish"}},
		{"releaseCommand", "releaseId", s.dispatchReleaseCommand, []string{"deprecate", "retire", "supersede", "revoke"}},
		{"snapshotCommand", "snapshotId", s.dispatchPublicationCommand, []string{"activate", "rollback"}},
		{"assignmentCommand", "assignmentId", s.dispatchRoleAssignmentCommand, []string{"revoke"}},
		{"resourceCommand", "resourceId", s.dispatchResourceCommand, []string{"delete"}},
		{"sourceCommand", "sourceId", s.dispatchSourceCommand, []string{"sync", "test", "pause", "resume", "remove"}},
		{"jobCommand", "jobId", s.dispatchJobCommand, []string{"cancel", "retry"}},
		{"webhookCommand", "webhookId", s.dispatchWebhookCommand, []string{"rotate-secret", "test"}},
	} {
		for _, action := range append(operation.actions, "unknown") {
			for _, identifier := range []string{projectionItem, "invalid", ""} {
				if operation.key == "domain" && identifier == "invalid" {
					continue
				}
				t.Run(operation.command+"/"+action+"/"+identifier, func(t *testing.T) {
					if operation.key == "domain" && identifier != "" {
						identifier = "Publisher.Example."
					}
					r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin)), http.MethodPost, "/command", strings.NewReader(`{`))
					r.SetPathValue(operation.command, identifier+":"+action)
					r.Header.Set("If-Match", `"rev-1"`)
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					operation.handler(w, r)
					status, code := 400, catalog.CodeInvalidArgument
					if identifier == "" || action == "unknown" {
						status, code = 404, catalog.CodeNotFound
					}
					requireProjectionProblem(t, w, status, code)
					want := ""
					if identifier != "" && identifier != "invalid" && action != "unknown" {
						want = identifier
					}
					if r.PathValue(operation.key) != want {
						t.Fatalf("selected %s=%q want=%q", operation.key, r.PathValue(operation.key), want)
					}
					if want != "" && !strings.Contains(w.Body.String(), "request body is not unambiguous JSON") {
						t.Fatalf("dispatch did not reach closed-body admission: %s", w.Body.String())
					}
				})
			}
		}
	}
}
