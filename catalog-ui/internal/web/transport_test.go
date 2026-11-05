// Transport units use registered handlers and deny socket access.
package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
)

func newFixedTransportFixture(t *testing.T, next hostRoundTripFunc) fixedTransport {
	t.Helper()
	denyDial := func(context.Context, string, string) (net.Conn, error) {
		t.Error("unit transport attempted a socket dial")
		return nil, errors.New("socket access is forbidden")
	}
	base := &http.Transport{DialContext: denyDial, DialTLSContext: denyDial}
	base.RegisterProtocol("https", next)
	t.Cleanup(base.CloseIdleConnections)
	return fixedTransport{base: base, origin: &url.URL{Scheme: "https", Host: "api.example.test:8443"}, limit: 16}
}

type transportResponseBody struct {
	reader            io.Reader
	bytesRead, closes int
}

func (b *transportResponseBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.bytesRead += n
	return n, err
}

func (b *transportResponseBody) Close() error {
	b.closes++
	return nil
}

func TestFixedTransportRejectsForeignDestinationsBeforeDelegation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, target string }{
		{"scheme", "http://api.example.test:8443/metadata"},
		{"host", "https://other.example.test:8443/metadata"},
		{"port", "https://api.example.test/metadata"},
		{"credentials", "https://user:password@api.example.test:8443/metadata"},
		{"empty userinfo", "https://@api.example.test:8443/metadata"},
		{"fragment", "https://api.example.test:8443/metadata#fragment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			transport := newFixedTransportFixture(t, func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected delegation")
			})
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, test.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if response != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if response != nil || err == nil || err.Error() != "UI destination is outside its configured origin" || calls != 0 {
				t.Fatalf("destination guard returned response=%v error=%v calls=%d", response, err, calls)
			}
		})
	}
}

func TestFixedTransportSanitizesUpstreamFailure(t *testing.T) {
	t.Parallel()
	calls := 0
	transport := newFixedTransportFixture(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("private upstream connection diagnostic")
	})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, transport.origin.String()+"/metadata", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if response != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if response != nil || err == nil || err.Error() != "UI upstream request failed" || calls != 1 {
		t.Fatalf("upstream failure returned response=%v error=%v calls=%d", response, err, calls)
	}
}

func TestFixedTransportResponseBoundsAndBodyOwnership(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, raw, wantError string
		status               int
		jsonOnly, readError  bool
	}{
		{name: "API exact byte limit", raw: strings.Repeat("é", 8), status: http.StatusOK},
		{name: "API empty response", status: http.StatusNoContent},
		{name: "API limit plus one", raw: strings.Repeat("x", 17), status: http.StatusOK, wantError: "UI upstream response is unavailable or too large"},
		{name: "API bounded read", raw: strings.Repeat("x", 64), status: http.StatusOK, wantError: "UI upstream response is unavailable or too large"},
		{name: "issuer exact limit", raw: `{"ok":true}` + "     ", status: http.StatusOK, jsonOnly: true},
		{name: "issuer malformed JSON", raw: "<private>", status: http.StatusOK, jsonOnly: true, wantError: "UI issuer returned invalid JSON"},
		{name: "issuer duplicate JSON member", raw: `{"x":1,"x":2}`, status: http.StatusOK, jsonOnly: true, wantError: "UI issuer returned invalid JSON"},
		{name: "issuer empty success", status: http.StatusNoContent, jsonOnly: true, wantError: "UI issuer returned invalid JSON"},
		{name: "issuer upper success status", raw: "private", status: 299, jsonOnly: true, wantError: "UI issuer returned invalid JSON"},
		{name: "issuer first redirect status", raw: "redirect", status: http.StatusMultipleChoices, jsonOnly: true},
		{name: "issuer error body", raw: "unavailable", status: http.StatusServiceUnavailable, jsonOnly: true},
		{name: "issuer redirect body", raw: "redirect", status: http.StatusFound, jsonOnly: true},
		{name: "issuer error body limit", raw: "null" + strings.Repeat(" ", 13), status: http.StatusServiceUnavailable, jsonOnly: true, wantError: "UI upstream response is unavailable or too large"},
		{name: "body read failure", raw: "partial", status: http.StatusOK, readError: true, wantError: "UI upstream response is unavailable or too large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &transportResponseBody{reader: strings.NewReader(test.raw)}
			if test.readError {
				body.reader = io.MultiReader(body.reader, iotest.ErrReader(errors.New("private body read diagnostic")))
			}
			calls := 0
			transport := newFixedTransportFixture(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.String() != "https://api.example.test:8443/metadata?view=raw" {
					t.Fatal("transport changed the fixed destination, path or query")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Etag": {`"upstream"`}}, Body: body, ContentLength: -1}, nil
			})
			transport.jsonOnly = test.jsonOnly
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, transport.origin.String()+"/metadata?view=raw", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if response != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if calls != 1 || body.closes != 1 || body.bytesRead != min(len(test.raw), 17) {
				t.Fatalf("response ownership/bounds: calls=%d closes=%d bytes=%d", calls, body.closes, body.bytesRead)
			}
			if test.wantError != "" {
				if response != nil || err == nil || err.Error() != test.wantError {
					t.Fatalf("rejected response=%v error=%v, want %q", response, err, test.wantError)
				}
				return
			}
			if err != nil || response == nil {
				t.Fatalf("buffered response=%v error=%v", response, err)
			}
			if response.Body == body || response.StatusCode != test.status || response.Header.Get("ETag") != `"upstream"` || response.ContentLength != int64(len(test.raw)) {
				t.Fatal("buffered response lost its body, status, headers or measured length")
			}
			raw, err := io.ReadAll(response.Body)
			if err != nil || string(raw) != test.raw {
				t.Fatalf("buffered response body=%q error=%v", raw, err)
			}
			if err := response.Body.Close(); err != nil || body.closes != 1 {
				t.Fatalf("closing buffered response affected upstream ownership: closes=%d error=%v", body.closes, err)
			}
		})
	}
}
