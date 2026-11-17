//go:build functional

package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/collections"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestHTTPSharedCollectionContractsAndReplay(t *testing.T) {
	server := newConfiguredHTTPServer(t, nil)
	defer server.Close()
	namespaceReply := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "collection-ns", "", map[string]any{"slug": "collection-test", "displayName": "Collection test"})
	requireStatus(t, namespaceReply, 201)
	var ns catalog.Namespace
	decodeResult(t, namespaceReply, &ns)
	createBody := map[string]any{"namespaceId": ns.ID, "name": "Shared tools", "description": "Curated text"}
	created := requestJSON(t, server.URL, "POST", "/api/v1/collections", "contributor", "collection-create", "", createBody)
	requireStatus(t, created, 201)
	requireControlSchema(t, "CollectionReceipt", created.body)
	var receipt feedbackReceipt
	decodeResult(t, created, &receipt)
	path := "/api/v1/collections/" + receipt.ID
	replay := requestJSON(t, server.URL, "POST", "/api/v1/collections", "contributor", "collection-create", "", createBody)
	requireStatus(t, replay, 201)
	if !bytes.Equal(replay.body, created.body) || replay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("create did not replay exactly")
	}
	got := requestJSON(t, server.URL, "GET", path, "reader", "", "", nil)
	requireStatus(t, got, 200)
	requireControlSchema(t, "SharedCollection", got.body)
	if got.header.Get("Cache-Control") != "no-store" || got.header.Get("ETag") == "" {
		t.Fatal("private metadata validators", got.header)
	}
	requireStatus(t, requestJSON(t, server.URL, "GET", path, "other-admin", "", "", nil), 404)
	requireStatus(t, requestJSON(t, server.URL, "PATCH", path, "reader", "denied-edit", got.header.Get("ETag"), map[string]any{"name": "Denied"}), 403)
	listed := requestJSON(t, server.URL, "GET", "/api/v1/collections?namespaceId="+ns.ID+"&limit=1", "reader", "", "", nil)
	requireStatus(t, listed, 200)
	requireControlSchema(t, "SharedCollectionPage", listed.body)
	for i, body := range []string{`{}`, `{"name":null}`, `{"description":null}`, `{"Name":"wrong case"}`, `{"namespaceId":"` + ns.ID + `"}`, `{"name":"one","name":"two"}`, `{"name":"x","extra":true}`} {
		invalid := requestWithHeaders(t, server.URL, "PATCH", path, []byte(body), map[string]string{"Authorization": "Bearer contributor", "Content-Type": "application/json", "Idempotency-Key": fmt.Sprintf("invalid-collection-%d", i), "If-Match": got.header.Get("ETag")})
		requireControlProblem(t, invalid, 400, catalog.CodeInvalidArgument)
	}
	for _, suffix := range []string{"?generation=1", "?namespaceId=" + ns.ID, "?limit=0", "?limit=101", "?limit=1&limit=1"} {
		requireControlProblem(t, requestJSON(t, server.URL, "GET", path+"/members"+suffix, "reader", "", "", nil), 400, catalog.CodeInvalidArgument)
	}
	// Escaped surrogate pairs remain legal up to the code-point limits. This
	// legal document exceeds 16 KiB, so the separate 32 KiB cap matters.
	escaped := `{"namespaceId":"` + ns.ID + `","name":"` + strings.Repeat(`\ud83d\ude42`, 160) + `","description":"` + strings.Repeat(`\ud83d\ude42`, 2000) + `"}`
	requireControlSchema(t, "CreateCollection", []byte(escaped))
	maximal := requestWithHeaders(t, server.URL, "POST", "/api/v1/collections", []byte(escaped), map[string]string{"Authorization": "Bearer contributor", "Content-Type": "application/json", "Idempotency-Key": "escaped-max-collection"})
	requireStatus(t, maximal, 201)
	var maxReceipt feedbackReceipt
	decodeResult(t, maximal, &maxReceipt)
	maxRead := requestJSON(t, server.URL, "GET", "/api/v1/collections/"+maxReceipt.ID, "reader", "", "", nil)
	requireStatus(t, maxRead, 200)
	var maxCollection struct{ Name, Description string }
	decodeResult(t, maxRead, &maxCollection)
	if maxCollection.Name != strings.Repeat("🙂", 160) || maxCollection.Description != strings.Repeat("🙂", 2000) {
		t.Fatal("escaped Unicode values changed")
	}
	padded := []byte(`{"namespaceId":"` + ns.ID + `","name":" ` + strings.Repeat(`\ud83d\ude42`, 160) + ` ","description":" ` + strings.Repeat(`\ud83d\ude42`, 2000) + ` "}`)
	requireControlSchema(t, "CreateCollection", padded)
	requireStatus(t, requestWithHeaders(t, server.URL, "POST", "/api/v1/collections", padded, map[string]string{"Authorization": "Bearer contributor", "Content-Type": "application/json", "Idempotency-Key": "padded-max-collection"}), 201)
	oversizedMember := requestWithHeaders(t, server.URL, "POST", path+"/members", []byte(`{"listingId":"`+ns.ID+`"}`+strings.Repeat(" ", 1024)), map[string]string{"Authorization": "Bearer contributor", "Content-Type": "application/json", "Idempotency-Key": "oversized-member", "If-Match": got.header.Get("ETag")})
	requireControlProblem(t, oversizedMember, 400, catalog.CodeInvalidArgument)
	deleteBody := requestJSON(t, server.URL, "DELETE", path, "contributor", "delete-body", got.header.Get("ETag"), map[string]any{})
	requireControlProblem(t, deleteBody, 400, catalog.CodeInvalidArgument)
	for i, body := range []string{`{"namespaceId":"` + ns.ID + `","name":null}`, `{"namespaceId":"` + ns.ID + `","name":"Valid","description":null}`, `{"namespaceId":"` + ns.ID + `","name":"one","name":"two"}`, escaped + strings.Repeat(" ", 32<<10)} {
		invalid := requestWithHeaders(t, server.URL, "POST", "/api/v1/collections", []byte(body), map[string]string{"Authorization": "Bearer contributor", "Content-Type": "application/json", "Idempotency-Key": fmt.Sprintf("invalid-create-collection-%d", i)})
		requireControlProblem(t, invalid, 400, catalog.CodeInvalidArgument)
	}
	requireStatus(t, requestJSON(t, server.URL, "DELETE", path, "contributor", "no-etag", "", nil), 412)
	updated := requestJSON(t, server.URL, "PATCH", path, "contributor", "collection-edit", got.header.Get("ETag"), map[string]any{"description": ""})
	requireStatus(t, updated, 200)
	requireStatus(t, requestJSON(t, server.URL, "PATCH", path, "contributor", "collection-stale", got.header.Get("ETag"), map[string]any{"name": "Stale"}), 412)
	members := requestJSON(t, server.URL, "GET", path+"/members", "reader", "", "", nil)
	requireStatus(t, members, 200)
	requireControlSchema(t, "CollectionMemberPage", members.body)
	if members.header.Get("ETag") != "" || members.header.Get("Cache-Control") != "no-store" || strings.Contains(string(members.body), "memberCount") {
		t.Fatal("member-page disclosure/cache", string(members.body), members.header)
	}
	deleted := requestJSON(t, server.URL, "DELETE", path, "contributor", "collection-delete", updated.header.Get("ETag"), nil)
	requireStatus(t, deleted, 200)
	requireStatus(t, requestJSON(t, server.URL, "GET", path, "reader", "", "", nil), 404)
	deletedReplay := requestJSON(t, server.URL, "DELETE", path, "contributor", "collection-delete", updated.header.Get("ETag"), nil)
	requireStatus(t, deletedReplay, 200)
	if deletedReplay.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(deleted.body, deletedReplay.body) {
		t.Fatal("deleted parent prevented replay")
	}
	requireStatus(t, requestJSON(t, server.URL, "POST", "/api/v1/collections", "contributor", "collection-create", "", createBody), 201)
}

func TestHTTPSharedCollectionMemberLifecycleAndCursor(t *testing.T) {
	server := newConfiguredHTTPServer(t, nil)
	defer server.Close()
	nsReply := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "collection-member-ns", "", map[string]any{"slug": "collection-members", "displayName": "Collection members"})
	requireStatus(t, nsReply, 201)
	var ns catalog.Namespace
	decodeResult(t, nsReply, &ns)
	publisherReply := requestJSON(t, server.URL, "POST", "/api/v1/publishers", "admin", "collection-publisher", "", map[string]any{"displayName": "Collection publisher", "domains": []string{"catalog.example.com"}})
	requireStatus(t, publisherReply, 201)
	var publisher catalog.Publisher
	decodeResult(t, publisherReply, &publisher)
	var listingIDs []string
	for n := 0; n < 2; n++ {
		prefix := fmt.Sprintf("collection-member-%d", n)
		resourceReply := requestJSON(t, server.URL, "POST", "/api/v1/resources", "contributor", prefix+"-resource", "", map[string]any{"namespaceId": ns.ID, "kind": "skill", "canonicalIdentifier": fmt.Sprintf("urn:air:catalog.example.com:collection-members:skill-%d", n)})
		requireStatus(t, resourceReply, 201)
		var resource catalog.Resource
		decodeResult(t, resourceReply, &resource)
		versionReply := requestJSON(t, server.URL, "POST", "/api/v1/resources/"+resource.ID+"/versions", "contributor", prefix+"-version", "", map[string]any{"version": "1.0.0", "representations": []map[string]any{{"role": "primary", "mediaType": adapter.MediaSkillMarkdown, "content": "---\nname: collection-fixture\ndescription: Read metadata only.\n---\n# Instructions\nRead metadata.\n"}}})
		requireStatus(t, versionReply, 201)
		var version struct {
			Revision catalog.VersionRevision `json:"revision"`
		}
		decodeResult(t, versionReply, &version)
		listingReply := requestJSON(t, server.URL, "POST", "/api/v1/catalog-listings", "contributor", prefix+"-listing", "", map[string]any{"namespaceId": ns.ID, "resourceId": resource.ID})
		requireStatus(t, listingReply, 201)
		var listing catalog.CatalogListing
		decodeResult(t, listingReply, &listing)
		revised := requestJSON(t, server.URL, "POST", "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", prefix+"-display", listingReply.header.Get("ETag"), map[string]any{"versionRevisionId": version.Revision.ID, "displayName": prefix, "description": "Collection fixture", "publisherId": publisher.ID, "visibility": "tenant"})
		requireStatus(t, revised, 201)
		submitted := requestJSON(t, server.URL, "POST", "/api/v1/catalog-listings/"+listing.ID+"/submissions", "contributor", prefix+"-submit", "", map[string]any{})
		requireStatus(t, submitted, 201)
		var submission catalog.ReviewSubmission
		decodeResult(t, submitted, &submission)
		approved := requestJSON(t, server.URL, "POST", "/api/v1/review-submissions/"+submission.ID+":approve", "admin", prefix+"-approve", submitted.header.Get("ETag"), map[string]any{"reason": "Review collection fixture"})
		requireStatus(t, approved, 200)
		published := requestJSON(t, server.URL, "POST", "/api/v1/catalog-listings/"+listing.ID+":publish", "admin", prefix+"-publish", revised.header.Get("ETag"), map[string]any{"submissionId": submission.ID})
		requireStatus(t, published, 201)
		listingIDs = append(listingIDs, listing.ID)
	}
	ownerReply := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "collection-owner-scope", "", map[string]any{"slug": "collection-owner", "displayName": "Collection owner"})
	requireStatus(t, ownerReply, 201)
	var owner catalog.Namespace
	decodeResult(t, ownerReply, &owner)
	created := requestJSON(t, server.URL, "POST", "/api/v1/collections", "contributor", "member-collection", "", map[string]any{"namespaceId": owner.ID, "name": "Reader collection"})
	requireStatus(t, created, 201)
	var receipt feedbackReceipt
	decodeResult(t, created, &receipt)
	path := "/api/v1/collections/" + receipt.ID
	etag := created.header.Get("ETag")
	for n, listing := range listingIDs {
		added := requestJSON(t, server.URL, "POST", path+"/members", "contributor", fmt.Sprintf("member-add-%d", n), etag, map[string]any{"listingId": listing})
		requireStatus(t, added, 201)
		etag = added.header.Get("ETag")
	}
	duplicate := requestJSON(t, server.URL, "POST", path+"/members", "contributor", "duplicate-member", etag, map[string]any{"listingId": listingIDs[0]})
	requireStatus(t, duplicate, 409)
	// Every operation has valid existing parents, inputs and current revisions.
	// Authentication must fail before disclosure or any command can execute.
	for n, command := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/collections", nil},
		{"POST", "/api/v1/collections", map[string]any{"namespaceId": owner.ID, "name": "Unauthenticated collection"}},
		{"GET", path, nil},
		{"PATCH", path, map[string]any{"name": "Unauthenticated edit"}},
		{"DELETE", path, nil},
		{"GET", path + "/members", nil},
		{"POST", path + "/members", map[string]any{"listingId": listingIDs[0]}},
		{"DELETE", path + "/members/" + listingIDs[0], nil},
	} {
		refused := requestJSON(t, server.URL, command.method, command.path, "", fmt.Sprintf("anonymous-collection-%d", n), etag, command.body)
		requireControlProblem(t, refused, 401, catalog.CodeUnauthenticated)
		if refused.header.Get("ETag") != "" || refused.header.Get("Idempotency-Replayed") != "" {
			t.Fatal("unauthenticated request exposed a retained validator or receipt", refused.header)
		}
	}
	unchanged := requestJSON(t, server.URL, "GET", path, "reader", "", "", nil)
	requireStatus(t, unchanged, 200)
	var unchangedCollection collections.Collection
	decodeResult(t, unchanged, &unchangedCollection)
	if unchanged.header.Get("ETag") != etag || unchangedCollection.Name != "Reader collection" {
		t.Fatal("unauthenticated command changed collection metadata", unchangedCollection)
	}
	first := requestJSON(t, server.URL, "GET", path+"/members?limit=1", "reader", "", "", nil)
	requireStatus(t, first, 200)
	requireControlSchema(t, "CollectionMemberPage", first.body)
	var page cursorPage[catalog.CatalogSummary]
	decodeResult(t, first, &page)
	ordered := slices.Clone(listingIDs)
	slices.Sort(ordered)
	if len(page.Items) != 1 || page.Items[0].Listing.ID != ordered[0] || page.NextCursor == "" {
		t.Fatal("missing page", page)
	}
	continuation := path + "/members?limit=1&cursor=" + url.QueryEscape(page.NextCursor)
	second := requestJSON(t, server.URL, "GET", continuation, "reader", "", "", nil)
	requireStatus(t, second, 200)
	requireControlSchema(t, "CollectionMemberPage", second.body)
	var last cursorPage[catalog.CatalogSummary]
	decodeResult(t, second, &last)
	if len(last.Items) != 1 || last.Items[0].Listing.ID != ordered[1] || last.NextCursor != "" {
		t.Fatal("member continuation repeated, omitted or misordered an entry", last)
	}
	// A metadata ETag cannot validate member results, which follow releases and
	// current access independently of the collection's own revision.
	conditional := requestWithHeaders(t, server.URL, "GET", path+"/members?limit=1", nil, map[string]string{"Authorization": "Bearer reader", "If-None-Match": etag})
	requireStatus(t, conditional, 200)
	requireControlSchema(t, "CollectionMemberPage", conditional.body)
	if conditional.header.Get("ETag") != "" || conditional.header.Get("Cache-Control") != "no-store" {
		t.Fatal("parent metadata ETag incorrectly qualified member results", conditional.header)
	}
	// Both principals and parents have successful read controls. Continuations
	// must still refuse another identity, collection or page size.
	requireStatus(t, requestJSON(t, server.URL, "GET", path+"/members?limit=1", "contributor", "", "", nil), 200)
	requireControlProblem(t, requestJSON(t, server.URL, "GET", continuation, "contributor", "", "", nil), 400, catalog.CodeInvalidArgument)
	requireControlProblem(t, requestJSON(t, server.URL, "GET", path+"/members?limit=2&cursor="+url.QueryEscape(page.NextCursor), "reader", "", "", nil), 400, catalog.CodeInvalidArgument)
	otherCreated := requestJSON(t, server.URL, "POST", "/api/v1/collections", "contributor", "other-member-collection", "", map[string]any{"namespaceId": owner.ID, "name": "Other collection"})
	requireStatus(t, otherCreated, 201)
	var otherReceipt feedbackReceipt
	decodeResult(t, otherCreated, &otherReceipt)
	otherETag := otherCreated.header.Get("ETag")
	for n, listing := range listingIDs {
		added := requestJSON(t, server.URL, "POST", "/api/v1/collections/"+otherReceipt.ID+"/members", "contributor", fmt.Sprintf("other-member-add-%d", n), otherETag, map[string]any{"listingId": listing})
		requireStatus(t, added, 201)
		otherETag = added.header.Get("ETag")
	}
	if otherETag != etag {
		t.Fatal("parent cursor control must have the same revision and members")
	}
	otherPath := "/api/v1/collections/" + otherReceipt.ID + "/members?limit=1"
	requireStatus(t, requestJSON(t, server.URL, "GET", otherPath, "reader", "", "", nil), 200)
	requireControlProblem(t, requestJSON(t, server.URL, "GET", otherPath+"&cursor="+url.QueryEscape(page.NextCursor), "reader", "", "", nil), 400, catalog.CodeInvalidArgument)
	removed := requestJSON(t, server.URL, "DELETE", path+"/members/"+listingIDs[0], "contributor", "remove-member", etag, nil)
	requireStatus(t, removed, 200)
	requireStatus(t, requestJSON(t, server.URL, "GET", continuation, "reader", "", "", nil), 400)
	replay := requestJSON(t, server.URL, "DELETE", path+"/members/"+listingIDs[0], "contributor", "remove-member", etag, nil)
	requireStatus(t, replay, 200)
	if !bytes.Equal(replay.body, removed.body) || replay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("member removal replay changed")
	}
	// The source remains a normal current Catalog entry after curation removal.
	requireStatus(t, requestJSON(t, server.URL, "GET", "/api/v1/catalog/resources/"+page.Items[0].Release.ID, "reader", "", "", nil), 200)
	// Restrict the original curator to its owner namespace after successful adds.
	grant := requestJSON(t, server.URL, "POST", "/api/v1/role-assignments", "admin", "collection-scoped-manager", "", map[string]any{"subject": "contributor", "actorType": "human", "role": "contributor", "namespaceId": owner.ID, "reason": "Qualify owner scope without target read"})
	requireStatus(t, grant, 201)
	grantsReply := requestJSON(t, server.URL, "GET", "/api/v1/role-assignments?limit=100", "admin", "", "", nil)
	requireStatus(t, grantsReply, 200)
	var grants cursorPage[identity.RoleAssignment]
	decodeResult(t, grantsReply, &grants)
	removedGlobals := 0
	for _, assignment := range grants.Items {
		if assignment.Subject == "contributor" && assignment.NamespaceID == "" {
			requireStatus(t, requestJSON(t, server.URL, "POST", "/api/v1/role-assignments/"+assignment.ID+":revoke", "admin", "restrict-"+assignment.ID, fmt.Sprintf(`"rev-%d"`, assignment.Revision), map[string]any{"reason": "Remove global fixture grant"}), 200)
			removedGlobals++
		}
	}
	if removedGlobals == 0 {
		t.Fatal("scope restriction fixture did not revoke a global assignment")
	}
	hidden := requestJSON(t, server.URL, "GET", path+"/members", "contributor", "", "", nil)
	requireStatus(t, hidden, 200)
	var hiddenPage cursorPage[catalog.CatalogSummary]
	decodeResult(t, hidden, &hiddenPage)
	if len(hiddenPage.Items) != 0 {
		t.Fatal("collection authority disclosed source namespace", hiddenPage)
	}
	// Existing add receipts retain the original target-read requirement.
	deniedReplay := requestJSON(t, server.URL, "POST", path+"/members", "contributor", "member-add-0", created.header.Get("ETag"), map[string]any{"listingId": listingIDs[0]})
	requireStatus(t, deniedReplay, 403)
	if deniedReplay.header.Get("Idempotency-Replayed") != "" || deniedReplay.header.Get("ETag") != "" {
		t.Fatal("denied replay exposed retained receipt")
	}
	private := requestJSON(t, server.URL, "POST", path+"/members", "contributor", "private-add", removed.header.Get("ETag"), map[string]any{"listingId": listingIDs[0]})
	requireControlProblem(t, private, 404, catalog.CodeNotFound)
	absent := requestJSON(t, server.URL, "POST", path+"/members", "contributor", "absent-add", removed.header.Get("ETag"), map[string]any{"listingId": "00000000-0000-7000-8000-000000000999"})
	requireControlProblem(t, absent, 404, catalog.CodeNotFound)
	var privateProblem, absentProblem map[string]any
	decodeResult(t, private, &privateProblem)
	decodeResult(t, absent, &absentProblem)
	if privateProblem["detail"] != absentProblem["detail"] {
		t.Fatal("add disclosed private target existence")
	}
	staleRemoved := requestJSON(t, server.URL, "DELETE", path+"/members/"+listingIDs[1], "contributor", "known-hidden-remove", removed.header.Get("ETag"), nil)
	requireStatus(t, staleRemoved, 200)
	requireStatus(t, requestJSON(t, server.URL, "DELETE", path, "contributor", "scoped-delete", staleRemoved.header.Get("ETag"), nil), 200)
	// Removing a known target never acquires target read, even on parentless replay.
	parentless := requestJSON(t, server.URL, "DELETE", path+"/members/"+listingIDs[1], "contributor", "known-hidden-remove", removed.header.Get("ETag"), nil)
	requireStatus(t, parentless, 200)
	if !bytes.Equal(parentless.body, staleRemoved.body) {
		t.Fatal("parentless removal replay changed receipt")
	}

}

func TestHTTPSharedCollectionConfiguredBodyCeiling(t *testing.T) {
	server := newConfiguredHTTPServer(t, func(d *Dependencies) { d.MaxBodyBytes = 128 })
	defer server.Close()
	nsReply := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "small-ceiling-ns", "", map[string]any{"slug": "small", "displayName": "Small"})
	requireStatus(t, nsReply, 201)
	var ns catalog.Namespace
	decodeResult(t, nsReply, &ns)
	refused := requestJSON(t, server.URL, "POST", "/api/v1/collections", "contributor", "small-ceiling-create", "", map[string]any{"namespaceId": ns.ID, "name": "Small", "description": strings.Repeat("x", 128)})
	requireControlProblem(t, refused, 400, catalog.CodeInvalidArgument)
	page := requestJSON(t, server.URL, "GET", "/api/v1/collections", "reader", "", "", nil)
	requireStatus(t, page, 200)
	var result struct {
		Items []json.RawMessage `json:"items"`
	}
	decodeResult(t, page, &result)
	if len(result.Items) != 0 {
		t.Fatal("over-limit command changed state")
	}
}

func TestHTTPSharedCollectionDirectoryContinuationAndBinding(t *testing.T) {
	server := newConfiguredHTTPServer(t, nil)
	defer server.Close()
	namespaces := make([]string, 0, 2)
	created := map[string]bool{}
	for n := range 2 {
		reply := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", fmt.Sprintf("directory-ns-%d", n), "", map[string]any{"slug": fmt.Sprintf("directory-%d", n), "displayName": "Directory owner"})
		requireStatus(t, reply, 201)
		var namespace catalog.Namespace
		decodeResult(t, reply, &namespace)
		namespaces = append(namespaces, namespace.ID)
	}
	for n := range 4 {
		reply := requestJSON(t, server.URL, "POST", "/api/v1/collections", "contributor", fmt.Sprintf("directory-create-%d", n), "", map[string]any{"namespaceId": namespaces[n%2], "name": "Same collection name"})
		requireStatus(t, reply, 201)
		var receipt feedbackReceipt
		decodeResult(t, reply, &receipt)
		created[receipt.ID] = true
	}
	firstCursor, last, cursor := "", "", ""
	for n := range 4 {
		endpoint := "/api/v1/collections?limit=1"
		if cursor != "" {
			endpoint += "&cursor=" + url.QueryEscape(cursor)
		}
		reply := requestJSON(t, server.URL, "GET", endpoint, "reader", "", "", nil)
		requireStatus(t, reply, 200)
		requireControlSchema(t, "SharedCollectionPage", reply.body)
		var page cursorPage[collections.Collection]
		decodeResult(t, reply, &page)
		if len(page.Items) != 1 || !created[page.Items[0].ID] || page.Items[0].ID <= last || (page.NextCursor != "") != (n < 3) {
			t.Fatal("directory lost, repeated or misordered an item", page)
		}
		delete(created, page.Items[0].ID)
		last, cursor = page.Items[0].ID, page.NextCursor
		if n == 0 {
			firstCursor = cursor
		}
	}
	if len(created) != 0 {
		t.Fatal("incomplete directory", created)
	}
	filtered := requestJSON(t, server.URL, "GET", "/api/v1/collections?namespaceId="+namespaces[0]+"&limit=100", "reader", "", "", nil)
	requireStatus(t, filtered, 200)
	var scoped cursorPage[collections.Collection]
	decodeResult(t, filtered, &scoped)
	if len(scoped.Items) != 2 {
		t.Fatal(scoped)
	}
	for _, item := range scoped.Items {
		if item.NamespaceID != namespaces[0] {
			t.Fatal("filter returned another owner", item)
		}
	}
	for _, query := range []string{"limit=0", "namespaceId=bad", "cursor=invalid", "limit=1&namespaceId=" + namespaces[0] + "&cursor=" + url.QueryEscape(firstCursor)} {
		requireControlProblem(t, requestJSON(t, server.URL, "GET", "/api/v1/collections?"+query, "reader", "", "", nil), 400, catalog.CodeInvalidArgument)
	}
	requireControlProblem(t, requestJSON(t, server.URL, "GET", "/api/v1/collections?limit=1&cursor="+url.QueryEscape(firstCursor), "other-admin", "", "", nil), 400, catalog.CodeInvalidArgument)
	requireStatus(t, requestJSON(t, server.URL, "GET", "/api/v1/collections/00000000-0000-7000-8000-000000000999/members", "reader", "", "", nil), 404)
}
