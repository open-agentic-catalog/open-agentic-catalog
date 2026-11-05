// Fixed-origin clients keep API, issuer and container-probe transports isolated.

package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// fixedTransport is for one operator-selected API or issuer origin, never a
// destination supplied by a browser, Catalog record or redirect. Trust pools
// and connections are not shared between API and issuer clients.
type fixedTransport struct {
	base     *http.Transport
	origin   *url.URL
	limit    int64
	jsonOnly bool
}

// ProbeReadiness is the shell-free container check of one fixed loopback target.
func ProbeReadiness(ctx context.Context) error {
	return probeReadiness(ctx, &url.URL{Scheme: "http", Host: "127.0.0.1:8080"})
}

// probeReadiness performs a bounded local health read; origin is injectable for tests.
func probeReadiness(ctx context.Context, origin *url.URL) error {
	client, err := newClient(origin, "", 4096, 2*time.Second, true)
	if err != nil {
		return safeerr.New("UI is not ready")
	}
	defer client.CloseIdleConnections()
	target := *origin
	target.Path = "/readyz"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return safeerr.New("UI is not ready")
	}
	response, err := client.Do(request)
	if err != nil {
		return safeerr.New("UI is not ready")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return safeerr.New("UI is not ready")
	}
	return nil
}

// CloseIdleConnections lets http.Client release this wrapper's underlying pool.
func (t fixedTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }

// apiRequest places a handler-selected path and query on the immutable API origin.
func (s *Server) apiRequest(ctx context.Context, method, path, query string, body io.Reader) (*http.Request, error) {
	target := *s.upstream
	target.Path = path
	target.RawQuery = query
	return http.NewRequestWithContext(ctx, method, target.String(), body) // #nosec G704 -- Only handler-selected paths and queries vary; the configured origin is preserved and fixedTransport checks it again before sending.
}

// newClient creates an origin-bound client with no redirect following or environment proxy.
// A supplied CA file replaces public roots for this client's private trust boundary.
func newClient(origin *url.URL, caFile string, limit int64, timeout time.Duration, jsonOnly bool) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		raw, err := readFile(caFile, 1<<20)
		if err != nil {
			return nil, safeerr.New("cannot read UI private trust")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(raw) {
			return nil, safeerr.New("UI private trust contains no certificates")
		}
		tlsConfig.RootCAs = roots
	}
	base := &http.Transport{TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 32, MaxIdleConnsPerHost: 16, MaxConnsPerHost: 64, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 32 << 10}
	return &http.Client{Transport: fixedTransport{base: base, origin: origin, limit: limit, jsonOnly: jsonOnly}, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// RoundTrip checks the destination and buffers a bounded response before exposing it.
// Issuer clients additionally reject malformed successful JSON responses.
func (t fixedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != t.origin.Scheme || r.URL.Host != t.origin.Host || r.URL.User != nil || r.URL.Fragment != "" {
		return nil, safeerr.New("UI destination is outside its configured origin")
	}
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, safeerr.New("UI upstream request failed")
	}
	// Retain upstream ownership before replacing the response body with a buffer.
	upstreamBody := response.Body
	defer func() { _ = upstreamBody.Close() }()
	raw, err := io.ReadAll(io.LimitReader(upstreamBody, t.limit+1))
	if err != nil || int64(len(raw)) > t.limit {
		return nil, safeerr.New("UI upstream response is unavailable or too large")
	}
	if t.jsonOnly && response.StatusCode >= 200 && response.StatusCode < 300 && jsondoc.Validate(raw) != nil {
		return nil, safeerr.New("UI issuer returned invalid JSON")
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))
	return response, nil
}
