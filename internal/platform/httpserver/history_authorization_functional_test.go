//go:build functional

package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestHTTPHistoryOnlyGrantBindsParentsAndCurrentAuthority(t *testing.T) {
	// The shared fixture owns a fixed service clock and a real loopback listener.
	server := newWebhookHTTPServer(t)
	t.Cleanup(server.Close)
	created := requestJSON(t, server.URL, http.MethodPost, "/api/v1/namespaces", "admin", "history-namespace", "", map[string]string{"slug": "history", "displayName": "History"})
	requireStatus(t, created, http.StatusCreated)
	var namespace catalog.Namespace
	decodeResult(t, created, &namespace)
	resources := make([]catalog.Resource, 2)
	versions := make([]versionEvidenceResponse, 0, 3)
	const privateText = "history-private-representation-sentinel"
	representations := []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: "application/vnd.open-agentic-catalog.agent+json", Data: json.RawMessage(`{"name":"` + privateText + `","framework":"custom"}`)}}
	for i := range resources {
		created = requestJSON(t, server.URL, http.MethodPost, "/api/v1/resources", "admin", fmt.Sprintf("history-resource-%d", i), "", catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: fmt.Sprintf("urn:air:catalog.example.com:history:resource-%d", i), Kind: catalog.KindAgent})
		requireStatus(t, created, http.StatusCreated)
		decodeResult(t, created, &resources[i])
		count := 1
		if i == 0 {
			count = 2
		}
		for j := range count {
			created = requestJSON(t, server.URL, http.MethodPost, "/api/v1/resources/"+resources[i].ID+"/versions", "admin", fmt.Sprintf("history-version-%d-%d", i, j), "", catalog.CreateVersionInput{Version: fmt.Sprintf("1.%d.0", j), Representations: representations})
			requireStatus(t, created, http.StatusCreated)
			var version versionEvidenceResponse
			decodeResult(t, created, &version)
			versions = append(versions, version)
		}
	}
	nextRepresentations := slices.Clone(representations)
	nextRepresentations[0].Data = json.RawMessage(`{"name":"` + privateText + `","framework":"custom","description":"Second immutable draft"}`)
	created = requestJSON(t, server.URL, http.MethodPost, "/api/v1/resource-versions/"+versions[0].Version.ID+"/revisions", "admin", "history-second-revision", `"rev-1"`, map[string]any{"representations": nextRepresentations})
	requireStatus(t, created, http.StatusCreated)
	var nextRevision versionEvidenceResponse
	decodeResult(t, created, &nextRevision)
	content := requestJSON(t, server.URL, http.MethodGet, "/api/v1/version-revisions/"+nextRevision.Revision.ID, "admin", "", "", nil)
	requireStatus(t, content, http.StatusOK)
	if !bytes.Contains(content.body, []byte(privateText)) {
		t.Fatal("private-history fixture did not retain the content control")
	}
	created = requestJSON(t, server.URL, http.MethodPost, "/api/v1/namespaces", "admin", "history-outside-namespace", "", map[string]string{"slug": "outside-history", "displayName": "Outside history grant"})
	requireStatus(t, created, http.StatusCreated)
	var outsideNamespace catalog.Namespace
	decodeResult(t, created, &outsideNamespace)
	created = requestJSON(t, server.URL, http.MethodPost, "/api/v1/resources", "admin", "history-outside-resource", "", catalog.CreateResourceInput{NamespaceID: outsideNamespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:outside-history:resource", Kind: catalog.KindAgent})
	requireStatus(t, created, http.StatusCreated)
	var outsideResource catalog.Resource
	decodeResult(t, created, &outsideResource)
	created = requestJSON(t, server.URL, http.MethodPost, "/api/v1/resources/"+outsideResource.ID+"/versions", "admin", "history-outside-version", "", catalog.CreateVersionInput{Version: "1.0.0", Representations: representations})
	requireStatus(t, created, http.StatusCreated)
	var outsideVersion versionEvidenceResponse
	decodeResult(t, created, &outsideVersion)
	if outsideNamespace.ID == namespace.ID || outsideResource.NamespaceID != outsideNamespace.ID || outsideVersion.Version.ResourceID != outsideResource.ID {
		t.Fatal("outside history control did not retain its distinct canonical namespace")
	}

	self := requestJSON(t, server.URL, http.MethodGet, "/api/v1/me", "reader", "", "", nil)
	requireStatus(t, self, http.StatusOK)
	var member selfResponse
	decodeResult(t, self, &member)
	assignments := requestJSON(t, server.URL, http.MethodGet, "/api/v1/role-assignments?limit=100", "admin", "", "", nil)
	requireStatus(t, assignments, http.StatusOK)
	var broad cursorPage[identity.RoleAssignment]
	decodeResult(t, assignments, &broad)
	revoked := 0
	for _, grant := range broad.Items {
		if grant.Subject == "reader" && grant.NamespaceID == "" {
			requireStatus(t, requestJSON(t, server.URL, http.MethodPost, "/api/v1/role-assignments/"+grant.ID+":revoke", "admin", "history-remove-broad-"+grant.ID, fmt.Sprintf(`"rev-%d"`, grant.Revision), map[string]string{"reason": "Keep only exact history access"}), http.StatusOK)
			revoked++
		}
	}
	if revoked != 1 || broad.NextCursor != "" {
		t.Fatal("history-only control did not remove its complete seeded grant")
	}
	roleReply := requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/roles", "admin", "history-only-role", "", map[string]any{"name": "Exact history reader", "permissions": []string{"resource.read_history"}, "reason": "Qualify bounded history without content access"})
	requireStatus(t, roleReply, http.StatusCreated)
	var role identity.RoleDefinition
	decodeResult(t, roleReply, &role)
	granted := requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/assignments", "admin", "history-only-grant", "", map[string]string{"membershipId": member.MembershipID, "roleDefinitionId": role.ID, "namespaceId": namespace.ID, "reason": "Grant only this namespace history"})
	requireStatus(t, granted, http.StatusCreated)
	var grant identity.AccessAssignment
	decodeResult(t, granted, &grant)
	for _, path := range []string{"/api/v1/resources/" + resources[0].ID, "/api/v1/version-revisions/" + nextRevision.Revision.ID} {
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, path, "reader", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	}
	auditCount := func() int {
		t.Helper()
		reply := requestJSON(t, server.URL, http.MethodGet, "/api/v1/audit-events?action="+audit.ActionVersionRevisionRead+"&limit=100", "admin", "", "", nil)
		requireStatus(t, reply, http.StatusOK)
		var page cursorPage[audit.Record]
		decodeResult(t, reply, &page)
		if page.NextCursor != "" {
			t.Fatal("private-read audit control exceeded its fixture bound")
		}
		return len(page.Items)
	}
	before := auditCount()
	if before != 1 {
		t.Fatal("private-read control did not retain exactly one audit")
	}

	histories := []struct {
		path, other, schema string
		outside, outsideID  string
		ids                 []string
		rows                [2]json.RawMessage
		continuation        string
	}{
		{path: "/api/v1/resources/" + resources[0].ID + "/versions", other: "/api/v1/resources/" + resources[1].ID + "/versions", schema: "ResourceVersion", outside: "/api/v1/resources/" + outsideResource.ID + "/versions", outsideID: outsideVersion.Version.ID, ids: []string{versions[0].Version.ID, versions[1].Version.ID}},
		{path: "/api/v1/resource-versions/" + versions[0].Version.ID + "/revisions", other: "/api/v1/resource-versions/" + versions[2].Version.ID + "/revisions", schema: "RevisionSummary", outside: "/api/v1/resource-versions/" + outsideVersion.Version.ID + "/revisions", outsideID: outsideVersion.Revision.ID, ids: []string{versions[0].Revision.ID, nextRevision.Revision.ID}},
	}
	for i := range histories {
		history := &histories[i]
		slices.Sort(history.ids) // Contract order is UUID order, not creation time.
		outside := requestJSON(t, server.URL, http.MethodGet, history.outside+"?limit=1", "admin", "", "", nil)
		requireStatus(t, outside, http.StatusOK)
		var outsidePage cursorPage[struct{ ID string }]
		decodeResult(t, outside, &outsidePage)
		if len(outsidePage.Items) != 1 || outsidePage.Items[0].ID != history.outsideID || outsidePage.NextCursor != "" {
			t.Fatal("outside history parent did not retain its known immutable child")
		}
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, history.outside+"?limit=1", "reader", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
		for _, control := range []struct{ path, token string }{{history.other, "reader"}, {history.path, "reader-two"}} {
			requireStatus(t, requestJSON(t, server.URL, http.MethodGet, control.path+"?limit=1", control.token, "", "", nil), http.StatusOK)
		}
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, history.path+"?limit=1", "other-admin", "", "", nil), http.StatusNotFound, catalog.CodeNotFound)
		first := requestJSON(t, server.URL, http.MethodGet, history.path+"?limit=1", "reader", "", "", nil)
		requireStatus(t, first, http.StatusOK)
		var page cursorPage[json.RawMessage]
		decodeResult(t, first, &page)
		if len(page.Items) != 1 || page.NextCursor == "" {
			t.Fatal("bounded history did not expose its second immutable row")
		}
		var item struct{ ID string }
		decodeResult(t, httpResult{body: page.Items[0]}, &item)
		requireControlSchema(t, history.schema, page.Items[0])
		if item.ID != history.ids[0] || bytes.Contains(first.body, []byte(privateText)) {
			t.Fatal("first history page lost UUID order or disclosed retained content")
		}
		history.rows[0] = bytes.Clone(page.Items[0])
		cursor := page.NextCursor
		history.continuation = history.path + "?limit=1&cursor=" + url.QueryEscape(cursor)
		last := requestJSON(t, server.URL, http.MethodGet, history.continuation, "reader", "", "", nil)
		requireStatus(t, last, http.StatusOK)
		page = cursorPage[json.RawMessage]{}
		decodeResult(t, last, &page)
		if len(page.Items) != 1 || page.NextCursor != "" {
			t.Fatal("history continuation omitted or duplicated a retained row")
		}
		decodeResult(t, httpResult{body: page.Items[0]}, &item)
		requireControlSchema(t, history.schema, page.Items[0])
		if item.ID != history.ids[1] || bytes.Contains(last.body, []byte(privateText)) {
			t.Fatal("last history page lost UUID order or disclosed retained content")
		}
		history.rows[1] = bytes.Clone(page.Items[0])
		for _, wrong := range []struct{ path, token string }{
			{history.other + "?limit=1&cursor=" + url.QueryEscape(cursor), "reader"},
			{history.continuation, "reader-two"},
			{history.continuation, "other-admin"},
		} {
			refused := requestJSON(t, server.URL, http.MethodGet, wrong.path, wrong.token, "", "", nil)
			requireControlProblem(t, refused, http.StatusBadRequest, catalog.CodeInvalidArgument)
			if bytes.Contains(refused.body, []byte(privateText)) {
				t.Fatal("rebound cursor disclosed private content")
			}
		}
		replayed := requestJSON(t, server.URL, http.MethodGet, history.continuation, "reader", "", "", nil)
		requireStatus(t, replayed, http.StatusOK)
		if !bytes.Equal(replayed.body, last.body) {
			t.Fatal("refused rebinding changed the original history continuation")
		}
	}
	requireStatus(t, requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/assignments/"+grant.ID+":revoke", "admin", "history-revoke-scoped", granted.header.Get("ETag"), map[string]string{"reason": "Withdraw exact history authority"}), http.StatusOK)
	for _, history := range histories {
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, history.continuation, "reader", "", "", nil), http.StatusBadRequest, catalog.CodeInvalidArgument)
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, history.path+"?limit=1", "reader", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
		retained := requestJSON(t, server.URL, http.MethodGet, history.path+"?limit=100", "admin", "", "", nil)
		requireStatus(t, retained, http.StatusOK)
		var page cursorPage[json.RawMessage]
		decodeResult(t, retained, &page)
		if len(page.Items) != 2 || !bytes.Equal(page.Items[0], history.rows[0]) || !bytes.Equal(page.Items[1], history.rows[1]) || page.NextCursor != "" {
			t.Fatal("read refusal or authority change rewrote immutable history")
		}
	}
	if auditCount() != before {
		t.Fatal("metadata history reads manufactured private content-read audit")
	}
}
