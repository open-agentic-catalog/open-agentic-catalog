// Embedding transport binds inference to one configured endpoint, without
// exposing the fetcher's general URL or header selection to its callers.
package fetcher

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// EmbeddingEndpoint performs bounded JSON POSTs to a single deployment destination.
type EmbeddingEndpoint struct {
	fetcher  *Fetcher
	endpoint string
}

// NewEmbeddingEndpoint isolates inference address permissions from other fetchers.
// Callers supply bounded policy and private trust; neither redirects nor proxies are used.
func NewEmbeddingEndpoint(policy Policy, endpoint string, allowHTTP bool) (*EmbeddingEndpoint, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("invalid embedding endpoint")
	}
	f, err := New(policy, nil)
	if err != nil {
		return nil, errors.New("invalid embedding transport policy")
	}
	// These private flags permit fixed-destination internal services only. Do not
	// enable the wider telemetry-header vocabulary for inference requests.
	f.telemetryHTTP = allowHTTP
	f.telemetryLocal = len(policy.AllowedAddressPrefixes) > 0
	if _, err = f.validateURL(endpoint); err != nil {
		return nil, errors.New("embedding endpoint violates outbound policy")
	}
	return &EmbeddingEndpoint{fetcher: f, endpoint: endpoint}, nil
}

// Post returns untrusted bytes/status without exposing URL-bearing transport errors.
func (e *EmbeddingEndpoint) Post(ctx context.Context, body []byte, token string) (Result, error) {
	if strings.ContainsAny(token, "\r\n\x00") {
		return Result{}, errors.New("invalid embedding credential")
	}
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	result, err := e.fetcher.Fetch(ctx, Request{URL: e.endpoint, Method: http.MethodPost, Body: body, ContentType: "application/json", Headers: headers, RejectRedirects: true, RedactQueryInErrors: true})
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, errors.New("embedding transport failed")
	}
	return result, nil
}

// Close releases idle sockets without retaining a process-global HTTP pool.
func (e *EmbeddingEndpoint) Close() { e.fetcher.client.CloseIdleConnections() }
