//go:build functional

package clientcompat_test

import (
	"context"
	"testing"

	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

func TestGeneratedDirectoryFilters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.client("admin")
	r, err := c.CreateNamespace(ctx, &client.CreateNamespaceParams{IdempotencyKey: "exact-namespace"}, client.CreateNamespaceJSONRequestBody{Slug: "tools--one", DisplayName: "Mixed 日本語"})
	ns := decode[client.Namespace](t, f.result(r, err, 201))
	r, err = c.ListNamespaces(ctx, &client.ListNamespacesParams{Limit: ptr(1), Slug: ptr(" TOOLS--ONE "), DisplayName: ptr("Mixed 日本語")})
	page := decode[client.NamespacePage](t, f.result(r, err, 200))
	if len(page.Items) != 1 || page.Items[0].Id != ns.Id || page.NextCursor != nil {
		t.Fatal(page)
	}
	r, err = c.ListNamespaces(ctx, &client.ListNamespacesParams{Slug: ptr("tools--one"), DisplayName: ptr("mixed 日本語")})
	if page := decode[client.NamespacePage](t, f.result(r, err, 200)); len(page.Items) != 0 {
		t.Fatal(page)
	}
	r, err = c.CreatePublisher(ctx, &client.CreatePublisherParams{IdempotencyKey: "exact-publisher"}, client.CreatePublisherJSONRequestBody{DisplayName: "Mixed publisher", Domains: []string{"declared.example"}})
	pub := decode[client.Publisher](t, f.result(r, err, 201))
	r, err = c.ListPublishers(ctx, &client.ListPublishersParams{Limit: ptr(1), DisplayName: ptr("Mixed publisher"), DeclaredDomain: ptr(" DECLARED.Example. ")})
	pubs := decode[client.PublisherPage](t, f.result(r, err, 200))
	if len(pubs.Items) != 1 || pubs.Items[0].Id != pub.Id || pubs.NextCursor != nil {
		t.Fatal(pubs)
	}
}
