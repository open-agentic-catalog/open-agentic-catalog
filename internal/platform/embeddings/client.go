// Package embeddings implements the bounded OpenAI-compatible inference subset.
// It accepts one startup-owned model, normalizes finite vectors and returns only
// fixed error categories. Model text, credentials and response bodies are never logged.
package embeddings

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

var (
	// ErrUnavailable permits bounded retry or explicit lexical fallback.
	ErrUnavailable = catalog.ErrEmbeddingUnavailable
	// ErrRejected identifies permanent input rejection, not credential failure.
	ErrRejected = catalog.ErrEmbeddingRejected
	// ErrAuthentication requires credential repair rather than discarding inputs.
	ErrAuthentication = catalog.ErrEmbeddingAuthentication
	// ErrInvalidResponse never exposes provider data and prevents storing invalid vectors.
	ErrInvalidResponse = catalog.ErrEmbeddingInvalidResponse
)

// inferenceEndpoint keeps client protocol decisions separate from the existing
// endpoint-bound fetcher, which owns HTTP policy and connection cleanup.
type inferenceEndpoint interface {
	Post(context.Context, []byte, string) (fetcher.Result, error)
	Close()
}

// admissionError preserves cancellation identity without treating local capacity
// or an already expired caller as evidence about the configured provider.
type admissionError struct{ error }

func (e admissionError) Unwrap() error { return e.error }

// Client owns separate query/indexing concurrency limits and an endpoint-bound pool.
type Client struct {
	observer func(string, string, time.Duration)
	state    atomic.Int32
	settings config.Semantic
	endpoint inferenceEndpoint
	token    string
	queries  chan struct{}
	batches  chan struct{}
}

// NewObserved installs fixed-outcome instrumentation before sharing the client.
func NewObserved(s config.Semantic, observer func(string, string, time.Duration)) (*Client, error) {
	client, err := New(s)
	if client != nil {
		client.observer = observer
	}
	return client, err
}

// State reports the last observed inference outcome without making a probe.
// A rejected input is not a deployment outage; no request means unknown.
func (c *Client) State() string {
	switch c.state.Load() {
	case 1:
		return "healthy"
	case 2:
		return "degraded"
	default:
		return "unknown"
	}
}

// observe excludes metadata and dependency error text from the metric boundary.
func (c *Client) observe(operation string, started time.Time, err error) {
	outcome := "success"
	var admission admissionError
	switch {
	case errors.As(err, &admission):
		outcome = "admission"
	case err == nil:
		c.state.Store(1)
	case errors.Is(err, ErrRejected):
		outcome = "rejected"
	case errors.Is(err, context.Canceled):
		outcome = "cancelled"
	case errors.Is(err, ErrAuthentication):
		outcome = "authentication"
		c.state.Store(2)
	case errors.Is(err, ErrInvalidResponse):
		outcome = "invalid_response"
		c.state.Store(2)
	default:
		outcome = "unavailable"
		c.state.Store(2)
	}
	if c.observer != nil {
		c.observer(operation, outcome, time.Since(started))
	}
}

// New loads bounded startup inputs without contacting inference. Disabled mode
// returns nil and reads no files. Configuration errors are not provider outages.
func New(s config.Semantic) (*Client, error) {
	if !s.Enabled {
		return nil, nil //nolint:nilnil // Disabled optional providers own no resources.
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	var token string
	if s.TokenFile != "" {
		raw, err := readInput(s.TokenFile, 8192)
		if err != nil {
			return nil, err
		}
		token = strings.TrimSpace(string(raw))
		if token == "" || strings.IndexFunc(token, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
			return nil, errors.New("invalid embedding credential file")
		}
	}
	var roots *x509.CertPool
	if s.CAFile != "" {
		raw, err := readInput(s.CAFile, 1<<20)
		if err != nil {
			return nil, err
		}
		roots, err = x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid embedding trust bundle")
		}
	}
	prefixes := make([]netip.Prefix, len(s.AllowedAddressPrefixes))
	for i, v := range s.AllowedAddressPrefixes {
		prefixes[i] = netip.MustParsePrefix(v)
	}
	endpoint, err := fetcher.NewEmbeddingEndpoint(fetcher.Policy{Timeout: max(s.QueryTimeout.Duration, s.BatchTimeout.Duration), MaxResponseBytes: 8 << 20, AllowedAddressPrefixes: prefixes, RootCAs: roots, UserAgent: "open-agentic-catalog-embeddings"}, s.Endpoint, s.AllowHTTP)
	if err != nil {
		return nil, err
	}
	s.ExcludedTenants = append([]string(nil), s.ExcludedTenants...)
	s.AllowedAddressPrefixes = append([]string(nil), s.AllowedAddressPrefixes...)
	return &Client{settings: s, endpoint: endpoint, token: token, queries: make(chan struct{}, s.QueryConcurrency), batches: make(chan struct{}, s.BatchConcurrency)}, nil
}

// Close releases the process-owned pool. Active requests retain their deadlines.
func (c *Client) Close() {
	if c != nil {
		c.endpoint.Close()
	}
}

// Query embeds one query under its separate deadline and concurrency budget.
func (c *Client) Query(ctx context.Context, text string) (vector []float32, resultErr error) {
	started := time.Now()
	defer func() { c.observe("query", started, resultErr) }()
	ctx, cancel := context.WithTimeout(ctx, c.settings.QueryTimeout.Duration)
	defer cancel()
	v, err := c.embed(ctx, []string{text}, c.settings.QueryPrefix, c.queries)
	if err != nil {
		return nil, err
	}
	return v[0], nil
}

// Documents embeds one bounded metadata batch; callers own durable Job retries.
func (c *Client) Documents(ctx context.Context, texts []string) (vectors [][]float32, resultErr error) {
	started := time.Now()
	defer func() { c.observe("batch", started, resultErr) }()
	ctx, cancel := context.WithTimeout(ctx, c.settings.BatchTimeout.Duration)
	defer cancel()
	return c.embed(ctx, texts, c.settings.DocumentPrefix, c.batches)
}

// embed bounds allocation and validates every response index before publishing
// any vector. Additive provider-envelope fields are allowed, duplicate keys are not.
func (c *Client) embed(ctx context.Context, texts []string, prefix string, limit chan struct{}) ([][]float32, error) {
	if len(texts) == 0 || len(texts) > c.settings.BatchSize {
		return nil, ErrRejected
	}
	input := make([]string, len(texts))
	for i, text := range texts {
		if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || len(text)+len(prefix) > c.settings.MaxInputBytes {
			return nil, ErrRejected
		}
		input[i] = prefix + text
	}
	select {
	case limit <- struct{}{}:
		defer func() { <-limit }()
	case <-ctx.Done():
		return nil, admissionError{ctx.Err()}
	}
	if err := ctx.Err(); err != nil {
		return nil, admissionError{err}
	}
	request := struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Encoding   string   `json:"encoding_format"`
		Dimensions int      `json:"dimensions,omitempty"`
	}{c.settings.Model, input, "float", c.settings.RequestedDimensions}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, ErrRejected
	}
	response, err := c.endpoint.Post(ctx, raw, c.token)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	if response.Status == 429 || response.Status >= 500 {
		if response.Status == 429 || response.Status == 503 {
			if delay := fetcher.RetryAfterDelay(response.RetryAfter, response.FetchedAt); delay > 0 {
				return nil, &catalog.EmbeddingRetryError{RetryAfter: delay}
			}
		}
		return nil, ErrUnavailable
	}
	if response.Status == 401 || response.Status == 403 {
		return nil, ErrAuthentication
	}
	if response.Status == 400 || response.Status == 413 || response.Status == 422 {
		return nil, ErrRejected
	}
	if response.Status != 200 {
		return nil, ErrInvalidResponse
	}
	if response.MediaType != "application/json" || jsondoc.Validate(response.Body) != nil {
		return nil, ErrInvalidResponse
	}
	var decoded struct {
		Model string `json:"model"`
		Data  []struct {
			Index     *int       `json:"index"`
			Embedding []*float64 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(response.Body, &decoded) != nil || len(decoded.Data) != len(input) {
		return nil, ErrInvalidResponse
	}
	expected := c.settings.ResponseModel
	if expected == "" {
		expected = c.settings.Model
	}
	if decoded.Model != expected {
		return nil, ErrInvalidResponse
	}
	vectors := make([][]float32, len(input))
	for _, entry := range decoded.Data {
		if entry.Index == nil || *entry.Index < 0 || *entry.Index >= len(input) || vectors[*entry.Index] != nil || len(entry.Embedding) != c.settings.Dimensions {
			return nil, ErrInvalidResponse
		}
		var norm float64
		for _, component := range entry.Embedding {
			if component == nil {
				return nil, ErrInvalidResponse
			}
			value := *component
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > math.MaxFloat32 {
				return nil, ErrInvalidResponse
			}
			norm += value * value
		}
		if norm <= 0 || math.IsInf(norm, 0) {
			return nil, ErrInvalidResponse
		}
		norm = math.Sqrt(norm)
		vector := make([]float32, len(entry.Embedding))
		for i, value := range entry.Embedding {
			vector[i] = float32(*value / norm)
		}
		vectors[*entry.Index] = vector
	}
	return vectors, nil
}

// readInput accepts projected regular files, bounds reads and hides private paths.
func readInput(name string, limit int64) ([]byte, error) {
	info, err := os.Stat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("embedding input must be a bounded regular file")
	}
	f, err := os.Open(name) // #nosec G304 -- Operator-configured startup input, never a request path.
	if err != nil {
		return nil, errors.New("cannot read embedding input")
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("embedding input must be a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("cannot read bounded embedding input")
	}
	return raw, nil
}
