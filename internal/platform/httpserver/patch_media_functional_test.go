//go:build functional

package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// These commands accept closed JSON updates, not RFC 7396 merge-patch documents.
// Exercise the real router, authorization, command cache and retained objects.
func TestHTTPPatchMediaContractPreservesStateAndReplay(t *testing.T) {
	t.Parallel()
	server := newWebhookHTTPServer(t)
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		collection, schema, field, action, targetType string
		create, patch, nullPatch                      string
	}{
		{"namespaces", "Namespace", "displayName", "namespace.updated", "namespace",
			`{"slug":"patch-media","displayName":"Original","description":"Keep this description"}`,
			`{"displayName":"Updated","reason":"Update display metadata"}`,
			`{"displayName":null,"reason":"Reject null deletion"}`},
		{"publishers", "Publisher", "displayName", "publisher.updated", "publisher",
			`{"displayName":"Original","domains":["example.com"]}`,
			`{"displayName":"Updated","reason":"Update display metadata"}`,
			`{"displayName":null,"reason":"Reject null deletion"}`},
		{"webhooks", "Webhook", "name", "webhook.updated", "webhook_endpoint",
			`{"name":"Original","url":"https://hooks.example.com/catalog","eventTypes":["resource.created"]}`,
			`{"name":"Updated"}`,
			`{"name":null}`},
	} {
		t.Run(tc.collection, func(t *testing.T) {
			collection := "/api/v1/" + tc.collection
			created := requestJSON(t, server.URL, http.MethodPost, collection, "admin", tc.collection+"-create", "", json.RawMessage(tc.create))
			requireStatus(t, created, http.StatusCreated)
			path := created.header.Get("Location")
			if path == "" {
				t.Fatal("creation omitted Location")
			}
			read := func() httpResult {
				t.Helper()
				result := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
				requireStatus(t, result, http.StatusOK)
				return result
			}
			original := read()
			for _, invalid := range []struct{ name, body, media string }{
				{"merge-patch", tc.patch, "application/merge-patch+json"},
				{"null-deletion", tc.nullPatch, "application/json"},
			} {
				rejected := requestWithHeaders(t, server.URL, http.MethodPatch, path, []byte(invalid.body), map[string]string{
					"Authorization": "Bearer admin", "Content-Type": invalid.media,
					"Idempotency-Key": tc.collection + "-" + invalid.name, "If-Match": original.header.Get("ETag"),
				})
				problem := requireControlProblem(t, rejected, http.StatusBadRequest, catalog.CodeInvalidArgument)
				if invalid.name == "merge-patch" && problem.Detail != "request body requires Content-Type application/json" {
					t.Fatalf("wrong media rejection guard: %s", problem.Detail)
				}
				unchanged := read()
				if !bytes.Equal(unchanged.body, original.body) || unchanged.header.Get("ETag") != original.header.Get("ETag") {
					t.Fatalf("%s changed the retained object", invalid.name)
				}
			}
			update := func() httpResult {
				t.Helper()
				return requestJSON(t, server.URL, http.MethodPatch, path, "admin", tc.collection+"-update", original.header.Get("ETag"), json.RawMessage(tc.patch))
			}
			updated := update()
			requireStatus(t, updated, http.StatusOK)
			requireControlSchema(t, tc.schema, updated.body)
			var before, after map[string]json.RawMessage
			decodeResult(t, original, &before)
			decodeResult(t, updated, &after)
			if string(after[tc.field]) != `"Updated"` || string(after["revision"]) != "2" || updated.header.Get("ETag") != `"rev-2"` {
				t.Fatalf("JSON update omitted its new value or revision: %s", updated.body)
			}
			for field, value := range before {
				if field != tc.field && field != "revision" && field != "updatedAt" && !bytes.Equal(after[field], value) {
					t.Errorf("omitted field %s changed", field)
				}
			}
			replayed := update()
			requireStatus(t, replayed, http.StatusOK)
			if replayed.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(replayed.body, updated.body) || replayed.header.Get("ETag") != updated.header.Get("ETag") {
				t.Fatal("JSON replay did not preserve the original update")
			}
			retained := read()
			if !bytes.Equal(retained.body, updated.body) || retained.header.Get("ETag") != updated.header.Get("ETag") {
				t.Fatal("JSON update/replay changed retained state unexpectedly")
			}
			var object struct {
				ID string `json:"id"`
			}
			decodeResult(t, updated, &object)
			audits := requestJSON(t, server.URL, http.MethodGet, "/api/v1/audit-events?action="+tc.action+"&targetType="+tc.targetType+"&targetId="+object.ID+"&limit=10", "admin", "", "", nil)
			requireStatus(t, audits, http.StatusOK)
			var page cursorPage[json.RawMessage]
			decodeResult(t, audits, &page)
			if len(page.Items) != 1 {
				t.Fatalf("refusals and replay must not add update audit records: %d", len(page.Items))
			}
		})
	}
}
