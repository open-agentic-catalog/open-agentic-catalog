// Package controlclient sends credentials only to the operator-selected API
// origin and bounds untrusted responses for explicit CLI clients.

package controlclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const (
	MaxBody    = 8 << 20
	MaxHeaders = 32 << 10
	MaxToken   = 16 << 10
	MaxCA      = 1 << 20
)

type Envelope struct {
	Data json.RawMessage `json:"data"`
	Meta Metadata        `json:"meta"`
}

type Metadata struct {
	RequestID string `json:"requestId"`
	ETag      string `json:"etag,omitempty"`
}

// APIError deliberately contains only safe diagnostics, never a wrapped
// url.Error, raw response, endpoint, file path or transport credential.
type APIError struct {
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail"`
	RequestID string `json:"requestId,omitempty"`
}

// Error renders only the already sanitized diagnostic fields as compact JSON.
func (failure APIError) Error() string {
	encoded, _ := JSON(failure)
	return string(encoded)
}

// A session snapshots credentials once for an entire explicit CLI operation.
// It never follows redirects, uses environment proxies or accepts response URLs.
type Session struct {
	Client        *http.Client
	Origin, Token string
}

func (s *Session) Close() { s.Client.CloseIdleConnections() }
func New(ctx context.Context, options Options) (*Session, error) {
	if ctx.Err() != nil {
		return nil, safeerr.New("remote: request cancelled or deadline exceeded")
	}
	origin, err := Origin(options.BaseURL)
	if err != nil {
		return nil, err
	}
	token := options.Token
	if options.TokenFile != "" {
		value, err := ReadFile(options.TokenFile, MaxToken)
		if err != nil {
			return nil, safeerr.New("remote: token file must be a readable regular file of at most 16 KiB")
		}
		token = strings.TrimSpace(string(value))
	}
	if !ValidToken(token) {
		return nil, safeerr.New("remote: supply a nonempty bearer token of at most 16 KiB through CATALOG_REMOTE_TOKEN or a token file")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if options.CAFile != "" {
		pem, err := ReadFile(options.CAFile, MaxCA)
		if err != nil {
			return nil, safeerr.New("remote: CA file must be a readable regular file of at most 1 MiB")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, safeerr.New("remote: cannot load TLS roots")
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, safeerr.New("remote: CA file contains no usable certificates")
		}
		tlsConfig.RootCAs = roots
	}
	transport := &http.Transport{
		DialContext:     (&net.Dialer{Timeout: options.Timeout}).DialContext,
		TLSClientConfig: tlsConfig, TLSHandshakeTimeout: options.Timeout,
		ResponseHeaderTimeout: options.Timeout, MaxResponseHeaderBytes: MaxHeaders,
		DisableKeepAlives: true, DisableCompression: true,
	}
	client := &http.Client{Transport: transport, Timeout: options.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Session{Client: client, Origin: origin, Token: token}, nil
}

// NewRequest is the common construction boundary for selected API reads
// and commands. Callers supply code-owned routes, never metadata URLs.
func NewRequest(ctx context.Context, origin, token, method, path, accept string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, origin+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", accept)
	return request, nil
}

// Exchange performs one selected API request. Callers own the closed route and body.
func Exchange(ctx context.Context, client *http.Client, origin, token string, requestOptions Request) (Envelope, error) {
	var result Envelope
	method, body := requestOptions.Method, requestOptions.Body

	request, err := NewRequest(ctx, origin, token, method, requestOptions.Path, "application/json, application/problem+json", bytes.NewReader(body))
	if err != nil {
		return result, safeerr.New("remote: cannot construct request")
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	if requestOptions.IdempotencyKey != "" {
		request.Header.Set("Idempotency-Key", requestOptions.IdempotencyKey)
	}
	if requestOptions.IfMatch != "" {
		request.Header.Set("If-Match", requestOptions.IfMatch)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return result, safeerr.New("remote: request cancelled or deadline exceeded")
		}
		return result, safeerr.New("remote: request failed; check API origin, connectivity and TLS trust")
	}
	defer func() { _ = response.Body.Close() }()
	requestID := Diagnostic(response.Header.Get("X-Request-ID"), token, 256)
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return result, APIError{response.StatusCode, "redirect_refused", "API redirects are not followed", requestID}
	}
	limit := requestOptions.MaxResponseBytes
	if limit == 0 {
		limit = MaxBody
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return result, safeerr.New("remote: request cancelled or deadline exceeded")
	}
	if err != nil || int64(len(data)) > limit {
		return result, APIError{response.StatusCode, "invalid_response", "response unreadable or exceeds the configured byte limit; exact record reads support --max-response-bytes (maximum 67108864)", requestID}
	}
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	validJSON := utf8.Valid(data) && jsondoc.Validate(data) == nil && bytes.HasPrefix(bytes.TrimSpace(data), []byte("{"))
	expectedStatus := requestOptions.ExpectedStatus
	if expectedStatus == 0 {
		expectedStatus = http.StatusOK
	}

	if response.StatusCode != expectedStatus {
		return result, HTTPFailure(response.StatusCode, response.Header, data, token)
	}
	if mediaErr != nil || media != "application/json" || !validJSON {
		return result, APIError{response.StatusCode, "invalid_response", "expected an unambiguous JSON object", requestID}
	}
	// Success metadata is opaque protocol data, not a diagnostic to truncate
	// or redact. The transport already bounds the complete header block. Refuse
	// ambiguous, non-JSON-representable or credential-reflecting values instead
	// of returning an altered validator or correlation ID.
	for _, name := range []string{"X-Request-ID", "ETag"} {
		if values := response.Header.Values(name); len(values) > 1 || !utf8.ValidString(response.Header.Get(name)) || strings.Contains(response.Header.Get(name), token) {
			return result, APIError{response.StatusCode, "invalid_response", "response metadata is ambiguous or unsafe", requestID}
		}
	}
	result.Data = data
	result.Meta = Metadata{RequestID: response.Header.Get("X-Request-ID"), ETag: response.Header.Get("ETag")}
	return result, nil
}

// HTTPFailure is shared by JSON reads and exact artifact downloads.
// It preserves only bounded, token-redacted problem fields from valid JSON.
func HTTPFailure(status int, headers http.Header, data []byte, token string) APIError {
	failure := APIError{status, "http_error", "API returned an unsuccessful response", Diagnostic(headers.Get("X-Request-ID"), token, 256)}
	media, _, err := mime.ParseMediaType(headers.Get("Content-Type"))
	if err == nil && media == "application/problem+json" && utf8.Valid(data) && jsondoc.Validate(data) == nil && bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(data, &problem) == nil {
			if problem.Code != "" {
				failure.Code = Diagnostic(problem.Code, token, 128)
			}
			if problem.Detail != "" {
				failure.Detail = Diagnostic(problem.Detail, token, 1024)
			}
		}
	}
	return failure
}

// Origin accepts a bounded credential-free HTTPS origin, allowing HTTP only for literal loopback addresses.
func Origin(value string) (string, error) {
	invalid := safeerr.New("remote: --base-url must be an HTTPS origin (HTTP only for literal loopback IPs), without credentials, path, query or fragment")
	if len(value) > 2048 {
		return "", invalid
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(value, "#") {
		return "", invalid
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", invalid
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return "", invalid
	}
	if parsed.Scheme == "http" {
		address, err := netip.ParseAddr(parsed.Hostname())
		if err != nil || !address.IsLoopback() || address.Zone() != "" {
			return "", invalid
		}
	} else if parsed.Scheme != "https" {
		return "", invalid
	}
	return parsed.String(), nil
}

// ValidToken requires a bounded bearer-compatible alphabet before constructing an Authorization header.
func ValidToken(token string) bool {
	if len(token) == 0 || len(token) > MaxToken {
		return false
	}
	for _, r := range token {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~+/=", r)) {
			return false
		}
	}
	return true
}

// Nonblocking open plus a regular-file check rejects FIFOs/devices, including
// a path replaced after validation. Symlinks to regular mounted secrets work.
func ReadFile(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 G703 -- Explicit operator-selected file, not an API or document path; regular-file and byte checks follow.
	if err != nil {
		return nil, safeerr.New("cannot open file")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, safeerr.New("invalid file")
	}
	value, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(value)) > limit {
		return nil, safeerr.New("invalid file size")
	}
	return value, nil
}

// Diagnostic redacts the current token and truncates diagnostic text by rune count.
func Diagnostic(value, token string, limit int) string {
	value = strings.ReplaceAll(value, token, "[redacted]")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return value
}

// Options holds operator-owned credentials and a fixed API origin. No runtime configuration is loaded.
type Options struct {
	BaseURL, TokenFile, CAFile, Token string
	Timeout                           time.Duration
}

// Request holds code-selected HTTP inputs; it is not a model-controlled URL interface.
type Request struct {
	Method, Path, IdempotencyKey, IfMatch string
	Body                                  json.RawMessage
	MaxResponseBytes                      int64
	ExpectedStatus                        int
}

type Search struct {
	Origins     []catalog.SearchResultType `json:"origins,omitempty"`
	NamespaceID string                     `json:"namespaceId,omitempty"`
	License     string                     `json:"license,omitempty"`
	Categories  []string                   `json:"categories,omitempty"`
	Text        string                     `json:"text,omitempty"`
	Kinds       []catalog.Kind             `json:"kinds,omitempty"`
	Tags        []string                   `json:"tags,omitempty"`
	Publisher   string                     `json:"publisher,omitempty"`
	Visibility  catalog.Visibility         `json:"visibility,omitempty"`
	Limit       int                        `json:"limit"`
	Cursor      string                     `json:"cursor,omitempty"`
}

// Validate enforces server-compatible bounds while preserving cursor/filter values.
func (query Search) Validate() error {
	// Text, publisher and cursor retain their server byte ceilings; tags count
	// Unicode code points. Validate without rewriting transmitted inputs.
	bounded := func(value string, limit int) bool {
		return utf8.ValidString(value) && len(value) <= limit && !strings.ContainsRune(value, '\x00')
	}
	if query.Limit < 1 || query.Limit > 100 || !bounded(query.Text, 500) || !bounded(query.Publisher, 256) || !bounded(query.Cursor, catalog.MaxCatalogCursorBytes) || query.Visibility != "" && !query.Visibility.Valid() {
		return safeerr.New("remote: invalid Search filter, cursor or page limit")
	}
	if !utf8.ValidString(query.License) || utf8.RuneCountInString(query.License) > catalog.MaxCompactLicenseCharacters || strings.ContainsRune(query.License, '\x00') || query.License != "" && strings.TrimSpace(query.License) == "" || len(query.Categories) > 16 {
		return safeerr.New("remote: invalid license or category declaration filter")
	}
	for _, category := range query.Categories {
		if !utf8.ValidString(category) || utf8.RuneCountInString(category) > 80 || strings.ContainsRune(category, '\x00') || strings.TrimSpace(category) == "" {
			return safeerr.New("remote: category requires one to 80 Unicode characters without NUL")
		}
	}
	seenOrigins := make(map[catalog.SearchResultType]bool)
	for _, origin := range query.Origins {
		if origin != catalog.SearchResultLocal && origin != catalog.SearchResultIndexed && origin != catalog.SearchResultLive || seenOrigins[origin] {
			return safeerr.New("remote: origins must be distinct local, indexed or live values")
		}
		seenOrigins[origin] = true
	}
	if query.NamespaceID != "" && (!identity.ValidTenantID(query.NamespaceID) || len(query.Origins) > 0 && (len(query.Origins) != 1 || query.Origins[0] != catalog.SearchResultLocal)) {
		return safeerr.New("remote: a namespace requires a UUID and local origin only")
	}
	seen := make(map[catalog.Kind]bool)
	for _, kind := range query.Kinds {
		if !kind.Valid() || seen[kind] {
			return safeerr.New("remote: kinds must be distinct agent, mcp_server, skill or plugin values")
		}
		seen[kind] = true
	}
	for _, tag := range query.Tags {
		if !utf8.ValidString(tag) || utf8.RuneCountInString(strings.TrimSpace(tag)) > 80 || strings.ContainsRune(tag, '\x00') {
			return safeerr.New("remote: tag exceeds 80 Unicode characters or contains invalid text")
		}
	}
	return nil
}

// JSON escapes non-ASCII code points as JSON escapes as well as the
// standard ASCII controls. This prevents terminal/bidi interpretation while
// preserving decoded Unicode, raw numeric values and extension members.
func JSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var safe strings.Builder
	for _, r := range string(encoded) {
		switch {
		case r < 0x7f:
			safe.WriteRune(r)
		case r <= 0xffff:
			fmt.Fprintf(&safe, `\u%04x`, r)
		default:
			r -= 0x10000
			fmt.Fprintf(&safe, `\u%04x\u%04x`, 0xd800+r>>10, 0xdc00+r&0x3ff)
		}
	}
	return []byte(safe.String()), nil
}
