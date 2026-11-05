// Unit tests exercise isolated validation and controlled dependencies.
package clientcompat_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

func TestHTTPFailurePreservesMetadataWithoutRetry(t *testing.T) {
	body := []byte(`{"type":"about:blank","title":"Unavailable","status":503,"code":"DEPENDENCY_UNAVAILABLE","detail":"Try later","requestId":"opaque/request=503"}`)
	must(t, component(t, "Problem").Validate(exactJSON(t, body)))
	stream := &trackedBody{Reader: bytes.NewReader(body)}
	calls := 0
	c, err := client.NewClientWithResponses("https://catalog.example", client.WithHTTPClient(boundedDoer{
		limit: responseLimit,
		next: doerFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Header.Get("Idempotency-Key") != "caller-command" || r.Method != http.MethodPost {
				t.Error("command identity missing")
			}
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{
				"Content-Type": {"application/problem+json"}, "Retry-After": {"7"}, "X-Request-Id": {"opaque/request=503"},
			}, Body: stream}, nil
		}),
	}))
	must(t, err)
	response, err := c.CreateNamespaceWithResponse(context.Background(), &client.CreateNamespaceParams{IdempotencyKey: "caller-command"}, client.CreateNamespaceJSONRequestBody{Slug: "example", DisplayName: "Example"})
	must(t, err) // HTTP errors can be successfully decoded; they are not successes.
	if response.StatusCode() != http.StatusServiceUnavailable || response.JSON201 != nil || response.ApplicationproblemJSONDefault == nil || response.ApplicationproblemJSONDefault.Code != "DEPENDENCY_UNAVAILABLE" {
		t.Fatal("HTTP failure misclassified")
	}
	if response.HTTPResponse.Header.Get("Retry-After") != "7" || response.HTTPResponse.Header.Get("X-Request-ID") != "opaque/request=503" || !bytes.Equal(response.Body, body) || !stream.closed || calls != 1 {
		t.Fatal("failure metadata/body changed, leaked or retried")
	}
}

func TestMalformedTypedResponseClosesBody(t *testing.T) {
	for _, body := range []string{`{"private":"unfinished`, `{"items":"not-an-array"}`, `{} {}`} {
		t.Run(body, func(t *testing.T) {
			stream := &trackedBody{Reader: strings.NewReader(body)}
			c, err := client.NewClientWithResponses("https://catalog.example", client.WithHTTPClient(boundedDoer{
				limit: responseLimit,
				next: doerFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: stream}, nil
				}),
			}))
			must(t, err)
			result, err := c.SearchCatalogWithResponse(context.Background(), client.SearchCatalogJSONRequestBody{})
			if err == nil || result != nil || !stream.closed {
				t.Fatalf("malformed response accepted or left open: %v", err)
			}
		})
	}
}

func TestRequestEditorFailureDoesNotSendCommand(t *testing.T) {
	calls := 0
	c, err := client.NewClient("https://catalog.example", client.WithHTTPClient(doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, io.ErrUnexpectedEOF
	})), client.WithRequestEditorFn(func(context.Context, *http.Request) error { return context.Canceled }))
	must(t, err)
	r, err := c.CreateNamespace(context.Background(), &client.CreateNamespaceParams{IdempotencyKey: "caller-command"}, client.CreateNamespaceJSONRequestBody{Slug: "example", DisplayName: "Example"})
	if err == nil {
		must(t, r.Body.Close())
		t.Fatal("failed credential editor accepted")
	}
	if calls != 0 {
		t.Fatal("command sent despite failed request editor")
	}
}
