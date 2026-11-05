//go:build functional

package httpserver

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestDirectoryExactFiltersHTTP(t *testing.T) {
	s := newWebhookHTTPServer(t)
	defer s.Close()
	namespaces := make([]catalog.Namespace, 0, 3)
	publishers := make([]catalog.Publisher, 0, 3)
	for i, slug := range []string{"aaa", "bbb", "ccc"} {
		reply := requestJSON(t, s.URL, "POST", "/api/v1/namespaces", "admin", slug, "", map[string]any{"slug": slug, "displayName": "Shared 日本語"})
		requireStatus(t, reply, 201)
		var ns catalog.Namespace
		decodeResult(t, reply, &ns)
		namespaces = append(namespaces, ns)
		reply = requestJSON(t, s.URL, "POST", "/api/v1/publishers", "admin", "pub-"+slug, "", map[string]any{"displayName": "Shared 日本語", "domains": []string{slug + ".example"}})
		requireStatus(t, reply, 201)
		var pub catalog.Publisher
		decodeResult(t, reply, &pub)
		publishers = append(publishers, pub)
		if i > 0 {
			requireStatus(t, requestJSON(t, s.URL, "POST", "/api/v1/role-assignments", "admin", "grant-"+slug, "", map[string]any{"subject": "reader", "actorType": "human", "role": "reader", "namespaceId": ns.ID, "reason": "Owned exact directory visibility control"}), 201)
		}
	}
	for _, tc := range []struct{ path, filter, value, other string }{
		{"namespaces", "slug", " BBB ", "ccc"},
		{"publishers", "declaredDomain", " BBB.Example. ", "ccc.example"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			q := url.Values{"displayName": {"Shared 日本語"}, tc.filter: {tc.value}, "limit": {"1"}}
			reply := requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+q.Encode(), "admin", "", "", nil)
			requireStatus(t, reply, 200)
			if tc.path == "namespaces" {
				var page cursorPage[catalog.Namespace]
				decodeResult(t, reply, &page)
				if len(page.Items) != 1 || page.Items[0].ID != namespaces[1].ID || page.NextCursor != "" {
					t.Fatal(page)
				}
			} else {
				var page cursorPage[catalog.Publisher]
				decodeResult(t, reply, &page)
				if len(page.Items) != 1 || page.Items[0].ID != publishers[1].ID || page.NextCursor != "" {
					t.Fatal(page)
				}
			}
			q.Set("displayName", "shared 日本語")
			reply = requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+q.Encode(), "admin", "", "", nil)
			requireStatus(t, reply, 200)
			if !strings.Contains(string(reply.body), `"items":[]`) {
				t.Fatal(string(reply.body))
			}
			q = url.Values{"displayName": {"Shared 日本語"}, "limit": {"1"}}
			reply = requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+q.Encode(), "admin", "", "", nil)
			requireStatus(t, reply, 200)
			var page cursorPage[map[string]any]
			decodeResult(t, reply, &page)
			if len(page.Items) != 1 || page.NextCursor == "" {
				t.Fatal(page)
			}
			q.Set("cursor", page.NextCursor)
			reply = requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+q.Encode(), "admin", "", "", nil)
			requireStatus(t, reply, 200)
			var next cursorPage[map[string]any]
			decodeResult(t, reply, &next)
			if len(next.Items) != 1 || next.Items[0]["id"] == page.Items[0]["id"] {
				t.Fatal(next)
			}
			if next.NextCursor == "" {
				t.Fatal("three matching rows must retain the final continuation")
			}
			q.Set("cursor", next.NextCursor)
			reply = requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+q.Encode(), "admin", "", "", nil)
			requireStatus(t, reply, 200)
			var last cursorPage[map[string]any]
			decodeResult(t, reply, &last)
			if len(last.Items) != 1 || last.NextCursor != "" {
				t.Fatal(last)
			}
			wantIDs := []string{namespaces[0].ID, namespaces[1].ID, namespaces[2].ID}
			if tc.path == "publishers" {
				wantIDs = []string{publishers[0].ID, publishers[1].ID, publishers[2].ID}
				slices.Sort(wantIDs)
			}
			gotIDs := []any{page.Items[0]["id"], next.Items[0]["id"], last.Items[0]["id"]}
			for i, wantID := range wantIDs {
				if gotIDs[i] != wantID {
					t.Fatalf("filtered page order/identity: got %v want %v", gotIDs, wantIDs)
				}
			}
			q.Set("cursor", page.NextCursor)
			// A matching control preceded every refusal. Exact filter removal,
			// case changes, tenant changes and added criteria cannot reuse it.
			requireStatus(t, requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+q.Encode(), "other-admin", "", "", nil), 400)
			q.Del("displayName")
			requireStatus(t, requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+q.Encode(), "admin", "", "", nil), 400)
			q.Set("displayName", "shared 日本語")
			requireStatus(t, requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+q.Encode(), "admin", "", "", nil), 400)
			q.Set("displayName", "Shared 日本語")
			q.Set(tc.filter, tc.other)
			requireStatus(t, requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+q.Encode(), "admin", "", "", nil), 400)
			q.Del("cursor")
			reply = requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+q.Encode(), "other-admin", "", "", nil)
			requireStatus(t, reply, 200)
			if !strings.Contains(string(reply.body), `"items":[]`) {
				t.Fatal(string(reply.body))
			}
			for _, invalid := range []string{tc.filter + "=", tc.filter + "=one&" + tc.filter + "=one", "displayName=", "DisplayName=Shared", tc.filter + "=" + url.QueryEscape(" "), "displayName=" + url.QueryEscape(strings.Repeat("界", 161))} {
				requireStatus(t, requestJSON(t, s.URL, "GET", "/api/v1/"+tc.path+"?"+invalid, "admin", "", "", nil), 400)
			}
		})
	}
	grantsReply := requestJSON(t, s.URL, "GET", "/api/v1/role-assignments?limit=100", "admin", "", "", nil)
	requireStatus(t, grantsReply, 200)
	var grants cursorPage[identity.RoleAssignment]
	decodeResult(t, grantsReply, &grants)
	for _, grant := range grants.Items {
		if grant.Subject == "reader" && grant.NamespaceID == "" {
			requireStatus(t, requestJSON(t, s.URL, "POST", "/api/v1/role-assignments/"+grant.ID+":revoke", "admin", "revoke-global-"+grant.ID, fmt.Sprintf("\"rev-%d\"", grant.Revision), map[string]any{"reason": "Leave only scoped directory access"}), 200)
		}
	}
	// Hidden matching rows cannot consume a visible caller's page or leak
	// through a filter. The last visible duplicate still gets a continuation.
	reply := requestJSON(t, s.URL, "GET", "/api/v1/namespaces?displayName="+url.QueryEscape("Shared 日本語")+"&limit=1", "reader", "", "", nil)
	requireStatus(t, reply, 200)
	var page cursorPage[catalog.Namespace]
	decodeResult(t, reply, &page)
	if len(page.Items) != 1 || page.Items[0].ID != namespaces[1].ID || page.NextCursor == "" {
		t.Fatal(page)
	}
	reply = requestJSON(t, s.URL, "GET", "/api/v1/namespaces?slug=aaa&limit=1", "reader", "", "", nil)
	requireStatus(t, reply, 200)
	decodeResult(t, reply, &page)
	if len(page.Items) != 0 {
		t.Fatal(page)
	}
	for _, invalid := range []string{"slug=-bad", "slug=bad_name", "declaredDomain=https%3A%2F%2Fexample.com"} {
		path := "namespaces"
		if strings.HasPrefix(invalid, "declared") {
			path = "publishers"
		}
		requireStatus(t, requestJSON(t, s.URL, "GET", fmt.Sprintf("/api/v1/%s?%s", path, invalid), "admin", "", "", nil), 400)
	}
}

func TestDirectoryFilterOAuthCeilings(t *testing.T) {
	s := newConfiguredHTTPServer(t, func(d *Dependencies) {
		original := d.Authenticator
		d.EnforceOAuthScopes = true
		d.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			actor, err := original.Authenticate(ctx, "admin")
			actor.Claims = map[string]string{}
			if token == "valid" {
				actor.Claims["scope:catalog.read"] = "true"
				actor.Claims["scope:publishers.read"] = "true"
			}
			return actor, err
		})
	})
	defer s.Close()
	for _, path := range []string{"/api/v1/namespaces?slug=tools", "/api/v1/publishers?declaredDomain=catalog.example"} {
		requireStatus(t, requestJSON(t, s.URL, "GET", path, "valid", "", "", nil), 200)
		requireStatus(t, requestJSON(t, s.URL, "GET", path, "no-scopes", "", "", nil), 403)
	}
}
