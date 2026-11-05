// Package catalogclient provides generated Catalog operations and a bounded,
// explicitly authenticated HTTP client. Use New for the maintained defaults.
package catalogclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultTimeout             = 30 * time.Second
	DefaultResponseBytes int64 = 8 << 20
	DefaultRequestBytes  int64 = 16 << 20
	DefaultHeaderBytes   int64 = 32 << 10
	// MaxBodyBytes prevents accidental unbounded buffering. A caller can select
	// a larger finite budget for a valid exact-record or artifact read.
	MaxBodyBytes int64 = 1 << 30
)

// Config contains client inputs, not Catalog service configuration. Zero budgets
// select the documented defaults. Credentials, proxies, cookies and login state
// are never inferred. Token and TokenProvider are mutually exclusive; omit both
// for anonymous discovery. TokenProvider runs once per request, after editors.
type Config struct {
	Origin        string
	Token         string
	TokenProvider func(context.Context) (string, error)
	RootCAs       *x509.CertPool
	Timeout       time.Duration
	ResponseBytes int64
	RequestBytes  int64
	HeaderBytes   int64
	// AllowLoopbackHTTP permits HTTP only to a literal loopback IP for local
	// development. HTTPS remains verified, including hostname checks.
	AllowLoopbackHTTP bool
}

// SDK exposes all generated raw and typed operations. Close releases idle
// connections; in-flight calls remain owned by their contexts.
type SDK struct {
	*ClientWithResponses
	transport *http.Transport
}

func (s *SDK) Close() { s.transport.CloseIdleConnections() }

// Error contains a fixed diagnostic code and safe response correlation. It
// retains no transport cause, URL, token or response body. HTTP non-success
// responses remain available in generated response branches, not Go errors.
type Error struct {
	Code       string
	StatusCode int
	RequestID  string
	contextErr error
}

func (e *Error) Error() string {
	code := "request_or_decode_failed"
	if e != nil {
		switch e.Code {
		case "canceled", "deadline_exceeded", "invalid_origin", "invalid_credentials", "invalid_budget", "invalid_response_limit", "origin_mismatch", "request_body_unreadable_or_over_limit", "credential_provider_failed", "invalid_response", "response_unreadable", "response_over_limit", "unsupported_content_encoding":
			code = e.Code
		}
	}
	return "Catalog SDK: " + code
}
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.contextErr
}

// safeError is also used by the two pinned generator template overrides. It
// prevents request-editor, marshaler and typed-decoder failures from exposing
// private values before or after the HTTP boundary.
func safeError(err error) error {
	if err == nil {
		return nil
	}
	return diagnostic(err)
}

func diagnostic(err error) *Error {
	var known *Error
	if errors.As(err, &known) && known != nil {
		return known
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: "canceled", contextErr: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: "deadline_exceeded", contextErr: context.DeadlineExceeded}
	}
	return &Error{Code: "request_or_decode_failed"}
}

func safeResponseError(err error, response *http.Response) error {
	if err == nil {
		return nil
	}
	correlated := *diagnostic(err)
	correlated.StatusCode = response.StatusCode
	correlated.RequestID = response.Header.Get("X-Request-ID")
	return &correlated
}

type responseLimitKey struct{}

// WithResponseLimit selects a finite body budget for one call. It does not
// change the client's default or truncate a response.
func WithResponseLimit(ctx context.Context, bytes int64) (context.Context, error) {
	if bytes < 1 || bytes > MaxBodyBytes {
		return nil, &Error{Code: "invalid_response_limit"}
	}
	return context.WithValue(ctx, responseLimitKey{}, bytes), nil
}

// New creates the maintained client. Generated NewClient/NewClientWithResponses
// and WithHTTPClient remain low-level escape hatches; they do not provide these
// transport guarantees by themselves.
func New(config Config) (*SDK, error) {
	origin, err := url.Parse(config.Origin)
	if err != nil || origin == nil || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.ForceQuery || strings.Contains(config.Origin, "#") || (origin.Path != "" && origin.Path != "/") || origin.Opaque != "" {
		return nil, &Error{Code: "invalid_origin"}
	}
	if origin.Scheme != "https" {
		ip, parseErr := netip.ParseAddr(origin.Hostname())
		if origin.Scheme != "http" || !config.AllowLoopbackHTTP || parseErr != nil || !ip.IsLoopback() {
			return nil, &Error{Code: "invalid_origin"}
		}
	}
	if strings.HasSuffix(origin.Host, ":") {
		return nil, &Error{Code: "invalid_origin"}
	}
	if origin.Port() != "" {
		port, err := strconv.ParseUint(origin.Port(), 10, 16)
		if err != nil || port == 0 {
			return nil, &Error{Code: "invalid_origin"}
		}
	}
	if config.Token != "" && config.TokenProvider != nil || !validToken(config.Token) {
		return nil, &Error{Code: "invalid_credentials"}
	}
	if config.Timeout == 0 {
		config.Timeout = DefaultTimeout
	}
	if config.ResponseBytes == 0 {
		config.ResponseBytes = DefaultResponseBytes
	}
	if config.RequestBytes == 0 {
		config.RequestBytes = DefaultRequestBytes
	}
	if config.HeaderBytes == 0 {
		config.HeaderBytes = DefaultHeaderBytes
	}
	if config.Timeout < 0 || config.ResponseBytes < 1 || config.ResponseBytes > MaxBodyBytes || config.RequestBytes < 1 || config.RequestBytes > MaxBodyBytes || config.HeaderBytes < 1 || config.HeaderBytes > MaxBodyBytes {
		return nil, &Error{Code: "invalid_budget"}
	}
	var roots *x509.CertPool
	if config.RootCAs != nil {
		roots = config.RootCAs.Clone()
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: config.Timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout:    config.Timeout,
		ResponseHeaderTimeout:  config.Timeout,
		MaxResponseHeaderBytes: config.HeaderBytes,
		DisableCompression:     true,
		MaxIdleConns:           16, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second,
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	boundary := &boundedClient{config: config, origin: origin, next: client}
	generated, err := NewClientWithResponses(origin.String(), WithHTTPClient(boundary))
	if err != nil {
		transport.CloseIdleConnections()
		return nil, safeError(err)
	}
	return &SDK{ClientWithResponses: generated, transport: transport}, nil
}

func validToken(token string) bool {
	if len(token) > 32<<10 {
		return false
	}
	for _, c := range []byte(token) {
		if c <= 32 || c >= 127 {
			return false
		}
	}
	return true
}

type boundedClient struct {
	config Config
	origin *url.URL
	next   HttpRequestDoer
}

func (c *boundedClient) Do(original *http.Request) (*http.Response, error) {
	if original.URL == nil || original.URL.User != nil || original.URL.Opaque != "" || original.URL.Scheme != c.origin.Scheme || !strings.EqualFold(original.URL.Host, c.origin.Host) || original.URL.Fragment != "" || (original.Host != "" && !strings.EqualFold(original.Host, c.origin.Host)) {
		if original.Body != nil {
			_ = original.Body.Close()
		}
		return nil, &Error{Code: "origin_mismatch"}
	}
	ctx, cancel := context.WithTimeout(original.Context(), c.config.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		if original.Body != nil {
			_ = original.Body.Close()
		}
		return nil, safeError(err)
	}
	req := original.Clone(ctx)
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if req.Body != nil {
		data, readErr := io.ReadAll(io.LimitReader(req.Body, c.config.RequestBytes+1))
		closeErr := req.Body.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) > c.config.RequestBytes {
			if ctx.Err() != nil {
				return nil, safeError(ctx.Err())
			}
			return nil, &Error{Code: "request_body_unreadable_or_over_limit"}
		}
		req.Body = io.NopCloser(bytes.NewReader(data))
		req.ContentLength = int64(len(data))
	}
	if err := ctx.Err(); err != nil {
		return nil, safeError(err)
	}
	// A non-rewindable body disables Transport's Idempotency-Key replay path.
	// For an empty mutation, use a streaming empty body (ContentLength=-1), not
	// NoBody: isReplayable otherwise treats its Idempotency-Key as retry consent.
	if req.Method != http.MethodGet && req.Method != http.MethodHead && req.Method != http.MethodOptions && req.Method != http.MethodTrace {
		req.GetBody = nil
		if req.Body == nil || req.ContentLength == 0 {
			req.Body = io.NopCloser(strings.NewReader(""))
			req.ContentLength = -1
		}
	}
	token := c.config.Token
	if c.config.TokenProvider != nil {
		var err error
		token, err = c.config.TokenProvider(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, safeError(ctx.Err())
			}
			return nil, &Error{Code: "credential_provider_failed"}
		}
	}
	if !validToken(token) {
		return nil, &Error{Code: "invalid_credentials"}
	}
	if err := ctx.Err(); err != nil {
		return nil, safeError(err)
	}
	// Editors can assign noncanonical map keys. Header.Del only removes the
	// canonical spelling, so remove every case variant before setting our values.
	for name := range req.Header {
		switch strings.ToLower(name) {
		case "authorization", "cookie", "proxy-authorization", "accept-encoding":
			delete(req.Header, name)
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept-Encoding", "identity")
	response, err := c.next.Do(req)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, safeError(err)
	}
	if response == nil || response.Body == nil {
		return nil, &Error{Code: "invalid_response"}
	}
	limit := c.config.ResponseBytes
	if override, ok := ctx.Value(responseLimitKey{}).(int64); ok {
		limit = override
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	closeErr := response.Body.Close()
	failure := func(code string) (*http.Response, error) {
		return nil, &Error{Code: code, StatusCode: response.StatusCode, RequestID: response.Header.Get("X-Request-ID")}
	}
	if readErr != nil || closeErr != nil {
		if ctx.Err() != nil {
			return nil, safeResponseError(ctx.Err(), response)
		}
		return failure("response_unreadable")
	}
	if int64(len(data)) > limit {
		return failure("response_over_limit")
	}
	// Compression is not negotiated. Refuse unexpected encodings rather than
	// pass compressed bytes to generated JSON decoders or hide expansion costs.
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return failure("unsupported_content_encoding")
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response, nil
}
