// Unit tests exercise isolated validation and controlled dependencies.
package clientcompat_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

func TestCredentialEditorRefreshesEveryRequest(t *testing.T) {
	credential := "first-token"
	var authorizations []string
	edits := 0
	c, err := client.NewClient("https://catalog.example", client.WithHTTPClient(boundedDoer{
		limit: responseLimit,
		next: doerFunc(func(r *http.Request) (*http.Response, error) {
			authorizations = append(authorizations, r.Header.Get("Authorization"))
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}),
	}), client.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		edits++
		r.Header.Set("Authorization", "Bearer "+credential)
		return nil
	}))
	must(t, err)
	for _, token := range []string{"first-token", "rotated-token"} {
		credential = token
		response, err := c.GetCapabilities(context.Background())
		must(t, err)
		must(t, response.Body.Close())
	}
	if edits != 2 || len(authorizations) != 2 || authorizations[0] != "Bearer first-token" || authorizations[1] != "Bearer rotated-token" {
		t.Fatal("generated client retained credentials instead of calling the editor for each request")
	}
}
