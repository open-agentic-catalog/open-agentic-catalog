//go:build functional

package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestScopedDirectoryHTTPPagesAndRevocation(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	namespaces := make([]catalog.Namespace, 0, 3)
	resources := make([]catalog.Resource, 0, 3)
	for i, slug := range []string{"aaa-hidden", "bbb-visible", "ccc-visible"} {
		created := requestJSON(t, server.URL, http.MethodPost, "/api/v1/namespaces", "admin", "namespace-"+slug, "", map[string]any{"slug": slug, "displayName": slug})
		requireStatus(t, created, 201)
		var ns catalog.Namespace
		decodeResult(t, created, &ns)
		namespaces = append(namespaces, ns)
		created = requestJSON(t, server.URL, http.MethodPost, "/api/v1/resources", "admin", "resource-"+slug, "", map[string]any{"namespaceId": ns.ID, "canonicalIdentifier": "urn:air:catalog.example.com:" + slug + ":resource", "kind": "skill"})
		requireStatus(t, created, 201)
		var resource catalog.Resource
		decodeResult(t, created, &resource)
		resources = append(resources, resource)
		if i > 0 {
			grant := requestJSON(t, server.URL, http.MethodPost, "/api/v1/role-assignments", "admin", "scoped-"+slug, "", map[string]any{"subject": "reader", "actorType": "human", "role": "reader", "namespaceId": ns.ID, "reason": "Qualify scoped directory transport"})
			requireStatus(t, grant, 201)
		}
	}
	assignments := requestJSON(t, server.URL, http.MethodGet, "/api/v1/role-assignments?limit=100", "admin", "", "", nil)
	requireStatus(t, assignments, 200)
	var grants cursorPage[identity.RoleAssignment]
	decodeResult(t, assignments, &grants)
	var scoped identity.RoleAssignment
	for _, grant := range grants.Items {
		if grant.Subject != "reader" {
			continue
		}
		if grant.NamespaceID == "" {
			v := requestJSON(t, server.URL, http.MethodPost, "/api/v1/role-assignments/"+grant.ID+":revoke", "admin", "revoke-global", fmt.Sprintf("\"rev-%d\"", grant.Revision), map[string]any{"reason": "Leave only scoped access"})
			requireStatus(t, v, 200)
		} else if grant.NamespaceID == namespaces[1].ID {
			scoped = grant
		}
	}
	if scoped.ID == "" {
		t.Fatal("scoped fixture grant missing")
	}
	summary := requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/resources", "reader", "", "", nil)
	requireStatus(t, summary, 200)
	requireControlSchema(t, "ResourceOverview", summary.body)
	var counts catalog.ResourceOverview
	decodeResult(t, summary, &counts)
	if counts.Total != (catalog.BoundedCount{Value: 2}) || counts.Kinds[2].Count != 2 {
		t.Fatal("HTTP summary disclosed hidden resources", counts)
	}
	first := requestJSON(t, server.URL, http.MethodGet, "/api/v1/namespaces?limit=1", "reader", "", "", nil)
	requireStatus(t, first, 200)
	requireControlSchema(t, "NamespacePage", first.body)
	var page cursorPage[catalog.Namespace]
	decodeResult(t, first, &page)
	if len(page.Items) != 1 || page.Items[0].ID != namespaces[1].ID || page.NextCursor == "" {
		t.Fatal("hidden namespace shortened first page", page)
	}
	last := requestJSON(t, server.URL, http.MethodGet, "/api/v1/namespaces?limit=1&cursor="+url.QueryEscape(page.NextCursor), "reader", "", "", nil)
	requireStatus(t, last, 200)
	var terminal cursorPage[catalog.Namespace]
	decodeResult(t, last, &terminal)
	if len(terminal.Items) != 1 || terminal.Items[0].ID != namespaces[2].ID || terminal.NextCursor != "" {
		t.Fatal("terminal scoped page", terminal)
	}
	listed := requestJSON(t, server.URL, http.MethodGet, "/api/v1/resources?limit=1", "reader", "", "", nil)
	requireStatus(t, listed, 200)
	requireControlSchema(t, "ResourcePage", listed.body)
	var resourcePage cursorPage[catalog.Resource]
	decodeResult(t, listed, &resourcePage)
	if len(resourcePage.Items) != 1 || resourcePage.Items[0].NamespaceID == namespaces[0].ID || resourcePage.NextCursor == "" {
		t.Fatal("resource scoped page", resourcePage)
	}
	resourceLast := requestJSON(t, server.URL, http.MethodGet, "/api/v1/resources?limit=1&cursor="+url.QueryEscape(resourcePage.NextCursor), "reader", "", "", nil)
	requireStatus(t, resourceLast, 200)
	var resourceTerminal cursorPage[catalog.Resource]
	decodeResult(t, resourceLast, &resourceTerminal)
	if len(resourceTerminal.Items) != 1 || resourceTerminal.Items[0].ID == resourcePage.Items[0].ID || resourceTerminal.Items[0].NamespaceID == namespaces[0].ID || resourceTerminal.NextCursor != "" {
		t.Fatal("resource continuation", resourceTerminal)
	}
	for _, item := range resources {
		query := url.Values{"canonicalIdentifier": {" " + item.CanonicalIdentifier + " "}, "kind": {"skill"}, "limit": {"1"}}
		reply := requestJSON(t, server.URL, http.MethodGet, "/api/v1/resources?"+query.Encode(), "reader", "", "", nil)
		requireStatus(t, reply, 200)
		requireControlSchema(t, "ResourcePage", reply.body)
		var exact cursorPage[catalog.Resource]
		decodeResult(t, reply, &exact)
		want := 1
		if item.NamespaceID == namespaces[0].ID {
			want = 0
		}
		if len(exact.Items) != want || exact.NextCursor != "" || want == 1 && exact.Items[0].ID != item.ID {
			t.Fatal("exact filter broadened scoped inventory", exact)
		}
	}
	for _, query := range []string{"canonicalIdentifier=", "canonicalIdentifier=%20%20", "canonicalIdentifier=x&canonicalIdentifier=y", "canonicalIdentifier=urn%3Aair%3Abad", "canonicalidentifier=x", "canonicalIdentifier=%00"} {
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/resources?"+query, "reader", "", "", nil), 400, catalog.CodeInvalidArgument)
	}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/resources/"+resources[0].ID, "reader", "", "", nil), 403, catalog.CodeForbidden)
	requireStatus(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/resources/"+resources[1].ID, "reader", "", "", nil), 200)
	removed := requestJSON(t, server.URL, http.MethodPost, "/api/v1/role-assignments/"+scoped.ID+":revoke", "admin", "revoke-scoped", fmt.Sprintf("\"rev-%d\"", scoped.Revision), map[string]any{"reason": "Invalidate private directory continuation"})
	requireStatus(t, removed, 200)
	summary = requestJSON(t, server.URL, http.MethodGet, "/api/v1/overview/resources", "reader", "", "", nil)
	requireStatus(t, summary, 200)
	decodeResult(t, summary, &counts)
	if counts.Total.Value != 1 {
		t.Fatal("summary retained revoked namespace", counts)
	}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/namespaces?limit=1&cursor="+url.QueryEscape(page.NextCursor), "reader", "", "", nil), 400, catalog.CodeInvalidArgument)
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/resources?limit=1&cursor="+url.QueryEscape(resourcePage.NextCursor), "reader", "", "", nil), 400, catalog.CodeInvalidArgument)
	refreshed := requestJSON(t, server.URL, http.MethodGet, "/api/v1/namespaces?limit=1", "reader", "", "", nil)
	requireStatus(t, refreshed, 200)
	var fresh cursorPage[catalog.Namespace]
	decodeResult(t, refreshed, &fresh)
	if len(fresh.Items) != 1 || fresh.Items[0].ID != namespaces[2].ID || fresh.NextCursor != "" {
		t.Fatal("fresh selection retained revoked scope", fresh)
	}
}
