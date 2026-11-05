//go:build functional

// Functional tests exercise local HTTP or composed service workflows.
package httpserver

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
)

func TestHTTPFeedbackWorkflowAndPrivateReplay(t *testing.T) {
	s := newWebhookHTTPServer(t)
	defer s.Close()
	nsReply := requestJSON(t, s.URL, "POST", "/api/v1/namespaces", "admin", "feedback-namespace", "", map[string]any{"slug": "feedback", "displayName": "Feedback tests"})
	requireStatus(t, nsReply, 201)
	var ns catalog.Namespace
	decodeResult(t, nsReply, &ns)
	for _, kind := range []string{"agent", "mcp_server", "skill", "plugin"} {
		t.Run(kind, func(t *testing.T) {
			resourceReply := requestJSON(t, s.URL, "POST", "/api/v1/resources", "admin", "feedback-resource-"+kind, "", map[string]any{"namespaceId": ns.ID, "canonicalIdentifier": "urn:air:catalog.example.com:feedback:" + kind, "kind": kind})
			requireStatus(t, resourceReply, 201)
			var resource catalog.Resource
			decodeResult(t, resourceReply, &resource)
			payload := map[string]any{"resourceId": resource.ID, "category": "problem", "comment": "private comment <script>unsafe</script>"}
			for _, token := range []string{"", "other-admin"} {
				denied := requestJSON(t, s.URL, "POST", "/api/v1/feedback", token, "denied-"+kind+token, "", payload)
				if denied.status < 400 {
					t.Fatalf("token %q admitted", token)
				}
			}
			created := requestJSON(t, s.URL, "POST", "/api/v1/feedback", "reader", "feedback-"+kind, "", payload)
			requireStatus(t, created, 201)
			requireControlSchema(t, "FeedbackReceipt", created.body)
			if strings.Contains(string(created.body), "private comment") {
				t.Fatal("command retained text")
			}
			var receipt feedbackReceipt
			decodeResult(t, created, &receipt)
			path := "/api/v1/feedback/" + receipt.ID
			requireStatus(t, requestJSON(t, s.URL, "GET", path, "reader-two", "", "", nil), 404)
			get := requestJSON(t, s.URL, "GET", path, "reader", "", "", nil)
			requireStatus(t, get, 200)
			requireControlSchema(t, "Feedback", get.body)
			for _, token := range []string{"reader", "reader-two"} {
				requireStatus(t, requestJSON(t, s.URL, "GET", "/api/v1/feedback/inbox", token, "", "", nil), 403)
			}
			inbox := requestJSON(t, s.URL, "GET", "/api/v1/feedback/inbox?resourceId="+resource.ID, "admin", "", "", nil)
			requireStatus(t, inbox, 200)
			requireControlSchema(t, "FeedbackPage", inbox.body)
			address := requestJSON(t, s.URL, "POST", path+":address", "admin", "address-"+kind, get.header.Get("ETag"), map[string]any{"response": "private response"})
			requireStatus(t, address, 200)
			requireStatus(t, requestJSON(t, s.URL, "PATCH", path, "reader", "stale-"+kind, get.header.Get("ETag"), map[string]any{"category": "other", "comment": "stale"}), 412)
			edited := requestJSON(t, s.URL, "PATCH", path, "reader", "edit-"+kind, address.header.Get("ETag"), map[string]any{"category": "suggestion", "comment": "updated private text"})
			requireStatus(t, edited, 200)
			withdrawn := requestJSON(t, s.URL, "POST", path+":withdraw", "reader", "withdraw-"+kind, edited.header.Get("ETag"), map[string]any{})
			requireStatus(t, withdrawn, 200)
			get = requestJSON(t, s.URL, "GET", path, "reader", "", "", nil)
			requireStatus(t, get, 200)
			var item feedback.Record
			decodeResult(t, get, &item)
			if item.Comment != "" || item.Response != "" || item.State != "withdrawn" {
				t.Fatalf("terminal record: %#v", item)
			}
			replayed := requestJSON(t, s.URL, "POST", "/api/v1/feedback", "reader", "feedback-"+kind, "", payload)
			requireStatus(t, replayed, 201)
			if replayed.header.Get("Idempotency-Replayed") != "true" || strings.Contains(string(replayed.body), "private") {
				t.Fatal("unsafe or missing replay")
			}
			requireStatus(t, requestJSON(t, s.URL, "POST", path+":reopen", "admin", "reopen-terminal-"+kind, withdrawn.header.Get("ETag"), map[string]any{}), 409)
			current := requestJSON(t, s.URL, "GET", "/api/v1/resources/"+resource.ID, "reader", "", "", nil)
			requireStatus(t, current, 200)
			if current.header.Get("ETag") != resourceReply.header.Get("ETag") {
				t.Fatal("feedback changed resource ETag")
			}
		})
	}
}

// A signed own-list continuation cannot be moved to another caller, purpose or filter.
func TestFeedbackCursorBinding(t *testing.T) {
	s := newWebhookHTTPServer(t)
	defer s.Close()
	ns := requestJSON(t, s.URL, "POST", "/api/v1/namespaces", "admin", "cursor-ns", "", map[string]any{"slug": "feedback-cursor", "displayName": "Feedback cursor"})
	requireStatus(t, ns, 201)
	var namespace catalog.Namespace
	decodeResult(t, ns, &namespace)
	created := requestJSON(t, s.URL, "POST", "/api/v1/resources", "admin", "cursor-resource", "", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:catalog.example.com:feedback:cursor", "kind": "agent"})
	requireStatus(t, created, 201)
	var resource catalog.Resource
	decodeResult(t, created, &resource)
	for i := range 2 {
		requireStatus(t, requestJSON(t, s.URL, "POST", "/api/v1/feedback", "reader", fmt.Sprintf("cursor-feedback-%d", i), "", map[string]any{"resourceId": resource.ID, "category": "other", "comment": "Private cursor fixture"}), 201)
	}
	first := requestJSON(t, s.URL, "GET", "/api/v1/feedback?limit=1", "reader", "", "", nil)
	requireStatus(t, first, 200)
	var page struct {
		Items      []feedback.Record `json:"items"`
		NextCursor string            `json:"nextCursor"`
	}
	decodeResult(t, first, &page)
	if len(page.Items) != 1 || page.NextCursor == "" || first.header.Get("Cache-Control") != "no-store" {
		t.Fatal("missing bounded private continuation")
	}
	base := "/api/v1/feedback?limit=1&cursor=" + page.NextCursor
	requireStatus(t, requestJSON(t, s.URL, "GET", base, "reader", "", "", nil), 200)
	for _, test := range []struct{ path, token string }{{base, "reader-two"}, {base + "&category=other", "reader"}, {"/api/v1/feedback/inbox?limit=1&cursor=" + page.NextCursor, "admin"}, {base + "tampered", "reader"}} {
		requireStatus(t, requestJSON(t, s.URL, "GET", test.path, test.token, "", "", nil), 400)
	}
}

// Ensure shared middleware rejects unknown query inputs before touching the service.
func TestFeedbackQueryAndBodyRejection(t *testing.T) {
	s := newWebhookHTTPServer(t)
	defer s.Close()
	for _, body := range []map[string]any{{"resourceId": "bad", "category": "problem", "comment": "text"}, {"resourceId": "00000000-0000-7000-8000-000000000999", "category": "problem", "comment": "text", "membershipId": "forged"}} {
		r := requestJSON(t, s.URL, http.MethodPost, "/api/v1/feedback", "reader", fmt.Sprint(body), "", body)
		requireStatus(t, r, 400)
	}
	requireStatus(t, requestJSON(t, s.URL, "GET", "/api/v1/feedback?membershipId=another", "reader", "", "", nil), 400)
}
