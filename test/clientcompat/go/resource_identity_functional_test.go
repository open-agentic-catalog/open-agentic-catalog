//go:build functional

package clientcompat_test

import (
	"bytes"
	"context"
	"testing"

	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

// TestGeneratedPrivateResourceContract validates actual private response envelopes
// against OpenAPI, independently of the generated client's JSON decoding.
func TestGeneratedPrivateResourceContract(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.client("admin")
	r, err := c.CreateNamespace(ctx, &client.CreateNamespaceParams{IdempotencyKey: "opaque-namespace"}, client.CreateNamespaceJSONRequestBody{Slug: "opaque", DisplayName: "Private identity"})
	ns := decode[client.Namespace](t, f.result(r, err, 201))
	params := &client.CreateResourceParams{IdempotencyKey: "opaque-resource"}
	body := client.CreateResourceJSONRequestBody{NamespaceId: ns.Id, CanonicalIdentifier: " custom:private-e\u0301-🙂 ", Kind: client.ResourceKind("agent")}
	r, err = c.CreateResource(ctx, params, body)
	created := f.result(r, err, 201)
	resource := decode[client.Resource](t, created)
	etag := r.Header.Get("ETag")
	if resource.CanonicalIdentifier != "custom:private-e\u0301-🙂" || resource.NamespaceId != ns.Id || etag == "" {
		t.Fatal("private creation changed identity or omitted its validator")
	}
	r, err = c.CreateResource(ctx, params, body)
	if replay := f.result(r, err, 201); !bytes.Equal(replay, created) || r.Header.Get("Idempotency-Replayed") != "true" || r.Header.Get("ETag") != etag {
		t.Fatal("private creation replay changed")
	}
	r, err = c.GetResource(ctx, resource.Id)
	if exact := f.result(r, err, 200); !bytes.Equal(exact, created) || r.Header.Get("ETag") != etag {
		t.Fatal("private exact read changed")
	}
	r, err = c.ListResources(ctx, &client.ListResourcesParams{CanonicalIdentifier: ptr(resource.CanonicalIdentifier), Limit: ptr(1)})
	page := decode[client.ResourcePage](t, f.result(r, err, 200))
	if len(page.Items) != 1 || page.Items[0].Id != resource.Id || page.Items[0].CanonicalIdentifier != resource.CanonicalIdentifier || page.NextCursor != nil {
		t.Fatal("private collection response changed identity or duplicated a replay")
	}
}
