//go:build functional

package httpserver

import (
	"context"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHTTPPersonalLiteralLabelAndBoundContinuation(t *testing.T) {
	serviceClock := clock.NewFake(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	s := newConfiguredHTTPServer(t, func(d *Dependencies) { d.Now = serviceClock.Now })
	defer s.Close()
	want := []string{}
	for i, label := range []string{"First unrelated", "Exact", "exact", " Exact ", "café", "cafe\u0301", "%_", "Exact"} {
		serviceClock.Advance(time.Millisecond)
		created := requestJSON(t, s.URL, "POST", "/api/v1/personal/items", "reader", fmt.Sprintf("label-%d", i), "", map[string]any{"type": "search", "label": label, "search": map[string]any{"text": "private"}})
		requireStatus(t, created, 201)
		var receipt feedbackReceipt
		decodeResult(t, created, &receipt)
		if label == "Exact" {
			want = append(want, receipt.ID)
		}
	}
	type libraryPage struct {
		Items      []personal.Item `json:"items"`
		NextCursor string          `json:"nextCursor"`
	}
	path := "/api/v1/personal/items?type=search&label=Exact&limit=1"
	first := requestJSON(t, s.URL, "GET", path, "reader", "", "", nil)
	requireStatus(t, first, 200)
	requireControlSchema(t, "PersonalPage", first.body)
	var page libraryPage
	decodeResult(t, first, &page)
	if len(page.Items) != 1 || page.Items[0].ID != want[0] || page.NextCursor == "" || first.header.Get("Cache-Control") != "no-store" {
		t.Fatal("filtered first page", page)
	}
	next := path + "&cursor=" + url.QueryEscape(page.NextCursor)
	second := requestJSON(t, s.URL, "GET", next, "reader", "", "", nil)
	requireStatus(t, second, 200)
	page = libraryPage{}
	decodeResult(t, second, &page)
	if len(page.Items) != 1 || page.Items[0].ID != want[1] || page.NextCursor != "" {
		t.Fatal("filtered second page", page)
	}
	for _, test := range []struct{ path, token string }{
		{strings.Replace(next, "label=Exact", "label=exact", 1), "reader"},
		{strings.Replace(next, "&label=Exact", "", 1), "reader"},
		{strings.Replace(next, "type=search", "type=resource", 1), "reader"},
		{next, "reader-two"},
		{next, "other-admin"},
	} {
		requireStatus(t, requestJSON(t, s.URL, "GET", test.path, test.token, "", "", nil), 400)
	}
	for _, label := range []string{"exact", " Exact ", "café", "cafe\u0301", "%_"} {
		r := requestJSON(t, s.URL, "GET", "/api/v1/personal/items?limit=1&label="+url.QueryEscape(label), "reader", "", "", nil)
		requireStatus(t, r, 200)
		page = libraryPage{}
		decodeResult(t, r, &page)
		if len(page.Items) != 1 || page.Items[0].Label != label || page.NextCursor != "" {
			t.Fatal("literal label changed", label, page)
		}
	}
	for _, label := range []string{"", "   ", "\n", strings.Repeat("🙂", 121)} {
		requireStatus(t, requestJSON(t, s.URL, "GET", "/api/v1/personal/items?label="+url.QueryEscape(label), "reader", "", "", nil), 400)
	}
	requireStatus(t, requestJSON(t, s.URL, "GET", "/api/v1/personal/items?label=Exact&label=Exact", "reader", "", "", nil), 400)
	for _, token := range []string{"admin", "reader-two", "other-admin"} {
		r := requestJSON(t, s.URL, "GET", path, token, "", "", nil)
		requireStatus(t, r, 200)
		decodeResult(t, r, &page)
		if len(page.Items) != 0 || page.NextCursor != "" {
			t.Fatal("label escaped owner", token, page)
		}
	}
}

func TestHTTPPersonalLibraryReplayRevisionAndCursorIsolation(t *testing.T) {
	s := newWebhookHTTPServer(t)
	defer s.Close()
	body := map[string]any{"type": "search", "label": "Private label", "search": map[string]any{"text": "private-query-sentinel", "origins": []string{"local"}}}
	create := requestJSON(t, s.URL, "POST", "/api/v1/personal/items", "reader", "personal-create", "", body)
	requireStatus(t, create, 201)
	requireControlSchema(t, "PersonalReceipt", create.body)
	if strings.Contains(string(create.body), "private-query-sentinel") {
		t.Fatal("query in command receipt")
	}
	var receipt feedbackReceipt
	decodeResult(t, create, &receipt)
	path := "/api/v1/personal/items/" + receipt.ID
	replay := requestJSON(t, s.URL, "POST", "/api/v1/personal/items", "reader", "personal-create", "", body)
	requireStatus(t, replay, 201)
	if replay.header.Get("Idempotency-Replayed") != "true" || string(replay.body) != string(create.body) {
		t.Fatal("missing exact replay")
	}
	get := requestJSON(t, s.URL, "GET", path, "reader", "", "", nil)
	requireStatus(t, get, 200)
	requireControlSchema(t, "PersonalItem", get.body)
	var item personal.Item
	decodeResult(t, get, &item)
	if item.Search.Text != "private-query-sentinel" || get.header.Get("Cache-Control") != "no-store" {
		t.Fatal(item)
	}
	for _, token := range []string{"admin", "reader-two", "other-admin"} {
		requireStatus(t, requestJSON(t, s.URL, "GET", path, token, "", "", nil), 404)
	}
	body["label"] = "Renamed"
	updated := requestJSON(t, s.URL, "PATCH", path, "reader", "personal-rename", get.header.Get("ETag"), body)
	requireStatus(t, updated, 200)
	requireStatus(t, requestJSON(t, s.URL, "PATCH", path, "reader", "personal-stale", get.header.Get("ETag"), body), 412)
	requireStatus(t, requestJSON(t, s.URL, "POST", path+"/resolve", "reader", "", "", map[string]any{}), 400) // named search is never executed by resolve
	for i := range 2 {
		requireStatus(t, requestJSON(t, s.URL, "POST", "/api/v1/personal/items", "reader", fmt.Sprintf("personal-page-%d", i), "", body), 201)
	}
	first := requestJSON(t, s.URL, "GET", "/api/v1/personal/items?limit=1", "reader", "", "", nil)
	requireStatus(t, first, 200)
	requireControlSchema(t, "PersonalPage", first.body)
	var page struct {
		Items      []personal.Item `json:"items"`
		NextCursor string          `json:"nextCursor"`
	}
	decodeResult(t, first, &page)
	if len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatal(page)
	}
	next := "/api/v1/personal/items?limit=1&cursor=" + page.NextCursor
	requireStatus(t, requestJSON(t, s.URL, "GET", next, "reader", "", "", nil), 200)
	for _, test := range []struct{ path, token string }{{next, "reader-two"}, {next + "&type=search", "reader"}, {next + "tampered", "reader"}} {
		requireStatus(t, requestJSON(t, s.URL, "GET", test.path, test.token, "", "", nil), 400)
	}
	requireStatus(t, requestJSON(t, s.URL, "DELETE", path, "reader", "personal-remove", updated.header.Get("ETag"), nil), 200)
	requireStatus(t, requestJSON(t, s.URL, "GET", path, "reader", "", "", nil), 404)
}
func TestHTTPPersonalClosedBodiesAndBounds(t *testing.T) {
	s := newWebhookHTTPServer(t)
	defer s.Close()
	for i, body := range []map[string]any{{"type": "search", "label": "Bad", "search": map[string]any{"text": "query", "cursor": "private"}}, {"type": "search", "label": "Bad", "search": map[string]any{"text": "query"}, "membershipId": "forged"}, {"type": "resource", "label": "Bad", "reference": map[string]any{"origin": "foreign", "sourceId": "https://arbitrary.example"}}, {"type": "search", "label": strings.Repeat("x", 121), "search": map[string]any{"text": "query"}}} {
		requireStatus(t, requestJSON(t, s.URL, "POST", "/api/v1/personal/items", "reader", fmt.Sprintf("bad-personal-%d", i), "", body), 400)
	}
	for _, path := range []string{"/api/v1/personal/items?limit=101", "/api/v1/personal/items?owner=forged", "/api/v1/me/work?queue=unknown", "/api/v1/me/work?limit=101"} {
		requireStatus(t, requestJSON(t, s.URL, "GET", path, "reader", "", "", nil), 400)
	}
}

func TestHTTPPersonalBackdatedRemovalPreservesPreference(t *testing.T) {
	serviceClock := clock.NewFake(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	s := newConfiguredHTTPServer(t, func(d *Dependencies) { d.Now = serviceClock.Now })
	defer s.Close()
	body := map[string]any{"type": "search", "label": "Private search", "search": map[string]any{"text": "private-clock-sentinel", "origins": []string{"local"}}}
	created := requestJSON(t, s.URL, "POST", "/api/v1/personal/items", "reader", "personal-clock-create", "", body)
	requireStatus(t, created, 201)
	var receipt feedbackReceipt
	decodeResult(t, created, &receipt)
	path := "/api/v1/personal/items/" + receipt.ID
	serviceClock.Advance(-time.Second)
	refused := requestJSON(t, s.URL, "DELETE", path, "reader", "personal-clock-refused", created.header.Get("ETag"), nil)
	requireStatus(t, refused, 409)
	var problem Problem
	decodeResult(t, refused, &problem)
	if problem.Code != catalog.CodeConflict || problem.Detail != "saved item transition time is before its retained update" || strings.Contains(string(refused.body), "private-clock-sentinel") {
		t.Fatal("backdated refusal lost its safe classification", problem)
	}
	retained := requestJSON(t, s.URL, "GET", path, "reader", "", "", nil)
	requireStatus(t, retained, 200)
	var item personal.Item
	decodeResult(t, retained, &item)
	if item.Revision != 1 || item.Label != "Private search" || retained.header.Get("ETag") != created.header.Get("ETag") {
		t.Fatal("refused removal changed saved preference", item)
	}
	serviceClock.Advance(2 * time.Second)
	// A completed refusal remains the same outcome after clock recovery. Only
	// a newly confirmed command may attempt the mutation again.
	replayed := requestJSON(t, s.URL, "DELETE", path, "reader", "personal-clock-refused", retained.header.Get("ETag"), nil)
	requireStatus(t, replayed, 409)
	if replayed.header.Get("Idempotency-Replayed") != "true" || string(replayed.body) != string(refused.body) {
		t.Fatal("clock recovery silently repeated a refused command")
	}
	requireStatus(t, requestJSON(t, s.URL, "GET", path, "reader", "", "", nil), 200)
	requireStatus(t, requestJSON(t, s.URL, "DELETE", path, "reader", "personal-clock-forward", retained.header.Get("ETag"), nil), 200)
	requireStatus(t, requestJSON(t, s.URL, "GET", path, "reader", "", "", nil), 404)
}

func TestHTTPPersonalScopeProjectionAndAdmission(t *testing.T) {
	s := newConfiguredHTTPServer(t, func(d *Dependencies) {
		original := d.Authenticator
		d.EnforceOAuthScopes = true
		d.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			missing := token == "read-only"
			a, e := original.Authenticate(ctx, "reader")
			a.Claims = map[string]string{"scope:catalog.read": "true"}
			if !missing {
				a.Claims["scope:personal.write"] = "true"
			}
			return a, e
		})
	})
	defer s.Close()
	for _, token := range []string{"read-only", "full"} {
		self := requestJSON(t, s.URL, "GET", "/api/v1/me", token, "", "", nil)
		requireStatus(t, self, 200)
		requireControlSchema(t, "CurrentAccess", self.body)
		var access selfResponse
		decodeResult(t, self, &access)
		if !slices.Contains(access.Permissions, "library.own") || slices.Contains(access.Scopes, "personal.write") != (token == "full") {
			t.Fatal(access)
		}
	}
	input := map[string]any{"type": "search", "label": "Scoped search", "search": map[string]any{"text": "private"}}
	denied := requestJSON(t, s.URL, "POST", "/api/v1/personal/items", "read-only", "scope-private", "", input)
	requireStatus(t, denied, 403)
	if !strings.Contains(denied.header.Get("WWW-Authenticate"), "personal.write") {
		t.Fatal(denied.header)
	}
	requireStatus(t, requestJSON(t, s.URL, "POST", "/api/v1/personal/items", "full", "scope-private", "", input), 201)
	requireStatus(t, requestJSON(t, s.URL, "POST", "/api/v1/personal/items", "read-only", "scope-private", "", input), 403)
}

func TestHTTPMyWorkBoundedPagesAndPrivateContinuations(t *testing.T) {
	s := newWebhookHTTPServer(t)
	defer s.Close()
	ns := requestJSON(t, s.URL, "POST", "/api/v1/namespaces", "admin", "my-work-ns", "", map[string]any{"slug": "my-work", "displayName": "My work"})
	requireStatus(t, ns, 201)
	var namespace catalog.Namespace
	decodeResult(t, ns, &namespace)
	for i := range 3 {
		requireStatus(t, requestJSON(t, s.URL, "POST", "/api/v1/resources", "contributor", fmt.Sprintf("work-resource-%d", i), "", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": fmt.Sprintf("urn:air:catalog.example.com:my-work:item-%d", i), "kind": "agent"}), 201)
	}
	first := requestJSON(t, s.URL, "GET", "/api/v1/me/work?queue=drafts&limit=1", "contributor", "", "", nil)
	requireStatus(t, first, 200)
	requireControlSchema(t, "PersonalWorkPage", first.body)
	var page struct {
		Items      []catalog.PersonalWorkItem `json:"items"`
		NextCursor string                     `json:"nextCursor"`
	}
	decodeResult(t, first, &page)
	if len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatal(page)
	}
	next := "/api/v1/me/work?queue=drafts&limit=1&cursor=" + page.NextCursor
	requireStatus(t, requestJSON(t, s.URL, "GET", next, "contributor", "", "", nil), 200)
	for _, test := range []struct{ path, token string }{{next, "admin"}, {next, "reader-two"}, {strings.Replace(next, "queue=drafts", "queue=approved", 1), "contributor"}, {next + "tampered", "contributor"}} {
		requireStatus(t, requestJSON(t, s.URL, "GET", test.path, test.token, "", "", nil), 400)
	}
	empty := requestJSON(t, s.URL, "GET", "/api/v1/me/work?queue=drafts", "admin", "", "", nil)
	requireStatus(t, empty, 200)
	decodeResult(t, empty, &page)
	if len(page.Items) != 0 {
		t.Fatal("admin adopted author's work", page)
	}
}

func qualifyHTTPPersonalExactReference(t *testing.T, base, actor string, ref personal.Reference) {
	t.Helper()
	input := map[string]any{"type": "resource", "label": "Exact saved resource", "reference": ref}
	created := requestJSON(t, base, "POST", "/api/v1/personal/items", actor, "personal-exact-"+ref.Origin, "", input)
	requireStatus(t, created, 201)
	var receipt feedbackReceipt
	decodeResult(t, created, &receipt)
	path := "/api/v1/personal/items/" + receipt.ID
	check := requestJSON(t, base, "POST", path+"/resolve", actor, "", "", map[string]any{})
	requireStatus(t, check, 200)
	requireControlSchema(t, "PersonalResolution", check.body)
	var resolved struct {
		Status    string             `json:"status"`
		Reference personal.Reference `json:"reference"`
	}
	decodeResult(t, check, &resolved)
	if resolved.Status != "available" || resolved.Reference.Origin != ref.Origin || resolved.Reference.ReleaseID != ref.ReleaseID || resolved.Reference.SourceID != ref.SourceID || resolved.Reference.PresentationGeneration != ref.PresentationGeneration {
		t.Fatal("exact reference changed", resolved)
	}
	loaded := requestJSON(t, base, "GET", path, actor, "", "", nil)
	requireStatus(t, loaded, 200)
	if ref.Origin == "foreign" {
		bad := ref
		bad.PresentationGeneration++
		input["reference"] = bad
		requireStatus(t, requestJSON(t, base, "PATCH", path, actor, "personal-stale-generation", loaded.header.Get("ETag"), input), 412)
		input["reference"] = ref
	}
	requireStatus(t, requestJSON(t, base, "DELETE", path, actor, "personal-body-"+ref.Origin, loaded.header.Get("ETag"), map[string]any{"owner": "forged"}), 400)
	requireStatus(t, requestJSON(t, base, "DELETE", path, actor, "personal-delete-"+ref.Origin, loaded.header.Get("ETag"), nil), 200)
}
