// Telemetry transport adapts the exporter to a single endpoint; collector-only
// header and address permissions remain isolated from resource retrieval.

package fetcher

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// NewTelemetryClient adapts the hardened fetcher to the standard OTLP exporter.
// It authorizes only one operator-selected URL, without redirects or proxies.
// Explicit local addressing and HTTP are private to this client, not federation.
func NewTelemetryClient(policy Policy, endpoint string, allowHTTP bool) (*http.Client, error) {
	f, err := New(policy, nil)
	if err != nil {
		return nil, err
	}
	f.telemetryHTTP = allowHTTP
	f.telemetryHeaders = true
	f.telemetryLocal = len(policy.AllowedAddressPrefixes) > 0
	_, err = f.validateURL(endpoint)
	if err != nil {
		return nil, errors.New("telemetry endpoint violates outbound policy")
	}
	// Compare the SDK request with the supplied spelling. Fetch performs the
	// same validated hostname/port normalization before the actual connection.
	return &http.Client{Timeout: policy.Timeout, Transport: telemetryTransport{fetcher: f, endpoint: endpoint}}, nil
}

type telemetryTransport struct {
	fetcher  *Fetcher
	endpoint string
}

// CloseIdleConnections releases the underlying fetcher's idle collector sockets.
func (t telemetryTransport) CloseIdleConnections() { t.fetcher.client.CloseIdleConnections() }

// ValidTelemetryHeader excludes routing, framing, compression and propagation
// headers while allowing deployment-selected authentication/routing credentials.
func ValidTelemetryHeader(name string) bool {
	if len(name) > 64 || !httpguts.ValidHeaderFieldName(name) {
		return false
	}
	switch strings.ToLower(name) {
	case "host", "content-type", "content-length", "content-encoding", "accept", "accept-encoding", "user-agent", "connection", "upgrade", "te", "trailer", "transfer-encoding", "proxy-authorization", "proxy-authenticate", "cookie", "set-cookie", "traceparent", "tracestate", "baggage":
		return false
	}
	return !strings.HasPrefix(strings.ToLower(name), "proxy-") && !strings.HasPrefix(strings.ToLower(name), "sec-")
}

// RoundTrip accepts only a POST to the exact configured endpoint, bounds request
// and response bytes, and replaces transport failures with secret-free errors.
// It consumes and closes the exporter request body.
func (t telemetryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer func() { _ = request.Body.Close() }()
	}
	if request.Method != http.MethodPost || request.URL.String() != t.endpoint {
		return nil, errors.New("telemetry request target is not permitted")
	}
	var raw []byte
	if request.Body != nil {
		var err error
		raw, err = io.ReadAll(io.LimitReader(request.Body, t.fetcher.policy.MaxResponseBytes+1))
		if err != nil || int64(len(raw)) > t.fetcher.policy.MaxResponseBytes {
			return nil, errors.New("telemetry request exceeds its bound")
		}
	}
	headers := make(map[string]string)
	for key, values := range request.Header {
		if key == "Content-Type" || key == "Content-Length" || key == "User-Agent" {
			continue
		}
		if len(values) != 1 {
			return nil, errors.New("telemetry request header is invalid")
		}
		headers[key] = values[0]
	}
	result, err := t.fetcher.Fetch(request.Context(), Request{URL: t.endpoint, Method: http.MethodPost, Body: raw, ContentType: request.Header.Get("Content-Type"), Headers: headers, RejectRedirects: true, MaxResponseBytes: 64 << 10, RedactQueryInErrors: true})
	if err != nil {
		return nil, errors.New("telemetry transport failed")
	}
	return &http.Response{StatusCode: result.Status, Header: http.Header{"Content-Type": {result.MediaType}, "Retry-After": {result.RetryAfter}}, Body: io.NopCloser(bytes.NewReader(result.Body)), ContentLength: int64(len(result.Body)), Request: request}, nil
}
