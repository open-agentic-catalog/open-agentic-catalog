// Unit tests exercise isolated validation and controlled dependencies.
package clientcompat_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"testing"
	"time"

	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

const responseLimit = 8 << 20

// boundedDoer is a test-owned example of the boundary required before generated
// WithResponse parsers. It is deliberately not a new production SDK transport.
type boundedDoer struct {
	next  client.HttpRequestDoer
	limit int64
}

func (d boundedDoer) Do(req *http.Request) (*http.Response, error) {
	response, err := d.next.Do(req)
	if err != nil {
		return nil, errors.New("Catalog request failed")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, d.limit+1))
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil || int64(len(data)) > d.limit {
		return nil, errors.New("Catalog response unreadable or over limit")
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response, nil
}

type doerFunc func(*http.Request) (*http.Response, error)

func (fn doerFunc) Do(req *http.Request) (*http.Response, error) { return fn(req) }

func explicitHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:           nil,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			// Keep this small test consumer single-attempt: net/http can retry
			// replayable commands with Idempotency-Key on reused connections.
			DisableKeepAlives:      true,
			DisableCompression:     true,
			MaxResponseHeaderBytes: 32 << 10,
			ResponseHeaderTimeout:  time.Second,
		},
	}
}

// expectedJSON checks status/media type before a caller treats decoding as success.
// Error text intentionally excludes response bodies and transport details.
func expectedJSON(response *http.Response, data []byte, status int) error {
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != status || err != nil || media != "application/json" || !json.Valid(data) || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return errors.New("Unexpected Catalog response")
	}
	return nil
}

type trackedBody struct {
	io.Reader
	closed     bool
	read       int
	closeError bool
}

func (body *trackedBody) Read(data []byte) (int, error) {
	n, err := body.Reader.Read(data)
	body.read += n
	return n, err
}

func (body *trackedBody) Close() error {
	body.closed = true
	if body.closeError {
		return errors.New("private close detail")
	}
	return nil
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("private body detail") }

func TestBoundBeforeGeneratedParser(t *testing.T) {
	for _, tc := range []struct {
		name       string
		length     int
		broken     bool
		closeError bool
		fail       bool
	}{
		{"exact", 32, false, false, false}, {"overflow", 4096, false, false, true}, {"read-error", 0, true, false, true}, {"close-error", 32, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader io.Reader = strings.NewReader(`{}` + strings.Repeat(" ", max(tc.length-2, 0)))
			if tc.broken {
				reader = brokenReader{}
			}
			body := &trackedBody{Reader: reader, closeError: tc.closeError}
			calls := 0
			doer := boundedDoer{limit: 32, next: doerFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
			})}
			c, err := client.NewClientWithResponses("https://catalog.example", client.WithHTTPClient(doer))
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.GetCapabilitiesWithResponse(context.Background())
			if (err != nil) != tc.fail || !body.closed || calls != 1 {
				t.Fatalf("failure=%v closed=%v calls=%d", err, body.closed, calls)
			}
			if body.read > 33 || (!tc.broken && body.read != min(tc.length, 33)) {
				t.Fatalf("bounded read consumed %d bytes", body.read)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("private cause exposed")
			}
		})
	}
}

func TestStatusMediaAndProblemHandling(t *testing.T) {
	for _, tc := range []struct {
		status      int
		media, body string
		accept      bool
	}{
		{200, "application/json", `{}`, true},
		{200, "application/json; charset=utf-8", " \n{}", true},
		{201, "application/json", `{}`, false},
		{200, "text/plain", `{}`, false},
		{200, "application/json; charset=", `{}`, false},
		{200, "", `{}`, false},
		{200, "application/json", `null`, false},
		{200, "application/json", `[]`, false},
		{200, "application/json", `{} {}`, false},
		{200, "application/json", ``, false},
	} {
		err := expectedJSON(&http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.media}}}, []byte(tc.body), 200)
		if (err == nil) != tc.accept {
			t.Fatalf("unexpected acceptance: %+v", tc)
		}
	}
	response, err := client.ParseGetCapabilitiesResponse(&http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": {"application/problem+json"}, "X-Request-Id": {"failure-id"}}, Body: io.NopCloser(strings.NewReader(`{"type":"about:blank","title":"Unauthorized","status":401,"code":"UNAUTHENTICATED","detail":"Authentication required","requestId":"failure-id"}`))})
	if err != nil || response.ApplicationproblemJSONDefault == nil || response.JSON200 != nil || response.StatusCode() != 401 {
		t.Fatalf("Problem branch: %+v %v", response, err)
	}
}

func TestGeneratedRequestEncoding(t *testing.T) {
	cursor := "opaque+value/with=&? space"
	r, err := client.NewListResourcesRequest("https://catalog.example", &client.ListResourcesParams{Cursor: &cursor, Kind: ptr(client.ResourceKind("plugin"))})
	must(t, err)
	if r.URL.Path != "/api/v1/resources" || r.URL.Query().Get("cursor") != cursor || r.URL.Query().Get("kind") != "plugin" || len(r.URL.Query()) != 2 {
		t.Fatalf("query encoding changed: %s", r.URL)
	}
	// UUID path parameters cannot contain arbitrary path delimiters; generated
	// action builders must keep the literal colon and opaque revision headers.
	id := decode[client.ResourceId](t, []byte(`"00000000-0000-7000-8000-000000000001"`))
	r, err = client.NewDeleteResourceRequest("https://catalog.example", id, &client.DeleteResourceParams{IdempotencyKey: "caller-key", IfMatch: `"opaque-etag"`}, client.DeleteResourceJSONRequestBody{Reason: "Remove draft"})
	must(t, err)
	defer func() { must(t, r.Body.Close()) }()
	if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/v1/resources/"+id.String()+":delete" || r.Header.Get("If-Match") != `"opaque-etag"` || r.Header.Get("Idempotency-Key") != "caller-key" || r.Header.Get("Content-Type") != "application/json" {
		t.Fatal("action path or command headers changed")
	}
}
