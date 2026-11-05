// Package fetcher provides the hardened boundary for untrusted outbound HTTP.
// URL normalization, DNS/IP admission, redirects, deadlines, and response limits
// share one transport boundary. Routing adds deployment-approved tenant/origin
// assignments; telemetry uses a separate client bound to one collector endpoint.
package fetcher

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"golang.org/x/net/idna"
)

// Resolver supplies DNS candidates for the fetcher's address-policy checks.
// Resolution alone does not authorize a destination.
type Resolver interface {
	// LookupNetIP resolves a host for the requested IP network and honors cancellation.
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// Policy defines one outbound HTTP safety envelope.
// New retains references to its maps, slices and trust material; callers must
// not mutate them while the fetcher is in use.
type Policy struct {
	Timeout                time.Duration
	MaxResponseBytes       int64
	MaxRedirects           int
	AllowHTTPForTests      bool
	AllowTestAddresses     bool
	AllowedOrigins         map[string]struct{}
	DeniedOrigins          map[string]struct{}
	AllowedAddressPrefixes []netip.Prefix
	DeniedAddressPrefixes  []netip.Prefix
	RootCAs                *x509.CertPool
	Certificates           []tls.Certificate
	UserAgent              string
}

// Request describes one outbound operation. Routing fields select only
// deployment-approved authority; they are not permission to expand policy.
// Headers and Body may contain secrets and must not be logged.
type Request struct {
	// IdentityEncoding preserves exact archive bytes rather than HTTP decoding.
	IdentityEncoding           bool
	URL                        string
	Method                     string
	Body                       []byte
	ContentType                string
	Accept                     []string
	ETag                       string
	LastModified               string
	Headers                    map[string]string
	RejectRedirects            bool
	RejectOriginChanges        bool
	TenantID                   string
	NetworkProfile             string
	CredentialBinding          string
	ExpectedPolicyDigest       string
	ExpectedCredentialRevision string
	MaxResponseBytes           int64
	RedactQueryInErrors        bool
}

// Result retains bounded but untrusted remote bytes and retrieval metadata.
// URLs, validators and headers may be tenant-private even when JSON-visible;
// callers must construct reviewed API/audit projections instead of exposing it.
type Result struct {
	RequestedURL       string        `json:"requestedUrl"`
	FinalURL           string        `json:"finalUrl"`
	Status             int           `json:"status"`
	MediaType          string        `json:"mediaType"`
	ETag               string        `json:"etag,omitempty"`
	LastModified       string        `json:"lastModified,omitempty"`
	CacheControl       string        `json:"cacheControl,omitempty"`
	Vary               []string      `json:"vary,omitempty"`
	RetryAfter         string        `json:"retryAfter,omitempty"`
	Links              []string      `json:"links,omitempty"`
	Body               []byte        `json:"-"`
	BodyDigest         string        `json:"bodyDigest"`
	PolicyID           string        `json:"policyId,omitempty"`
	PolicyRevision     int64         `json:"policyRevision,omitempty"`
	PolicyDigest       string        `json:"policyDigest,omitempty"`
	NetworkProfile     string        `json:"networkProfile,omitempty"`
	CredentialRevision string        `json:"credentialRevision,omitempty"`
	FetchedAt          time.Time     `json:"fetchedAt"`
	Duration           time.Duration `json:"duration"`
}

// Fetcher performs one bounded request through URL, DNS, redirect, address,
// header, TLS, and response-size validation.
type Fetcher struct {
	telemetryHTTP    bool
	telemetryLocal   bool
	telemetryHeaders bool
	policy           Policy
	resolver         Resolver
	client           *http.Client
}

var (
	// ErrInvalidTarget identifies a structurally invalid outbound target. A
	// caller can reject it as malformed input before recording an outbound
	// attempt, without inspecting error text.
	ErrInvalidTarget = errors.New("outbound target is invalid")
	// ErrPolicyDenied identifies a target rejected by the hardened egress
	// boundary before or during connection establishment.
	ErrPolicyDenied = errors.New("outbound target is denied by policy")
)

var deniedSpecialUsePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

// New validates transport budgets and uses the system resolver when nil.
// The transport deliberately ignores ambient proxy environment variables so
// connections cannot bypass its address validation and pinning.
func New(policy Policy, resolver Resolver) (*Fetcher, error) {
	if policy.Timeout <= 0 || policy.MaxResponseBytes < 1024 || policy.MaxResponseBytes > 64<<20 || policy.MaxRedirects < 0 || policy.MaxRedirects > 10 {
		return nil, errors.New("fetcher policy budgets are invalid")
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if policy.UserAgent == "" {
		policy.UserAgent = "open-agentic-catalog/1.0"
	}
	fetcher := &Fetcher{policy: policy, resolver: resolver}
	transport := &http.Transport{
		Proxy:                  nil,
		DisableCompression:     false,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           32,
		MaxIdleConnsPerHost:    4,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    min(policy.Timeout, 5*time.Second),
		ResponseHeaderTimeout:  min(policy.Timeout, 10*time.Second),
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: policy.RootCAs, Certificates: append([]tls.Certificate(nil), policy.Certificates...)},
	}
	transport.DialContext = fetcher.dialContext
	fetcher.client = &http.Client{Transport: transport, Timeout: policy.Timeout}
	fetcher.client.CheckRedirect = fetcher.checkRedirect
	return fetcher, nil
}

// Fetch returns bounded response bytes and retrieval metadata within Policy.
// A nil error does not imply a successful HTTP status or valid domain content;
// callers must check both. Redirects and dialed addresses remain policy-bound.
func (f *Fetcher) Fetch(ctx context.Context, request Request) (Result, error) {
	ctx, endTrace := catalog.StartTrace(ctx, "HTTP outbound", catalog.TraceClient, "")
	traceOutcome := "error"
	defer func() { endTrace(traceOutcome) }()
	parsed, err := f.validateURL(request.URL)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, f.policy.Timeout)
	defer cancel()
	method := request.Method
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost {
		return Result{}, errors.New("fetcher supports GET and POST only")
	}
	if int64(len(request.Body)) > f.policy.MaxResponseBytes {
		return Result{}, errors.New("outbound request body exceeds the configured size limit")
	}
	maxResponseBytes := f.policy.MaxResponseBytes
	if request.MaxResponseBytes != 0 {
		if request.MaxResponseBytes < 1024 {
			return Result{}, errors.New("outbound response size limit is invalid")
		}
		maxResponseBytes = min(maxResponseBytes, request.MaxResponseBytes)
	}
	var requestBody io.Reader
	if len(request.Body) > 0 {
		requestBody = strings.NewReader(string(request.Body))
	}
	if request.RejectRedirects {
		ctx = context.WithValue(ctx, redirectPolicyContextKey{}, true)
	}
	if request.RejectOriginChanges {
		ctx = context.WithValue(ctx, originChangePolicyContextKey{}, true)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, parsed.String(), requestBody)
	if err != nil {
		return Result{}, fmt.Errorf("create fetch request: %w", err)
	}
	httpRequest.Header.Set("User-Agent", f.policy.UserAgent)
	httpRequest.Header.Set("Accept", strings.Join(request.Accept, ", "))
	if request.IdentityEncoding {
		httpRequest.Header.Set("Accept-Encoding", "identity")
	}
	if len(request.Body) > 0 {
		contentType := request.ContentType
		if contentType == "" {
			contentType = "application/json"
		}
		httpRequest.Header.Set("Content-Type", contentType)
	}
	if request.ETag != "" {
		httpRequest.Header.Set("If-None-Match", request.ETag)
	}
	if request.LastModified != "" {
		httpRequest.Header.Set("If-Modified-Since", request.LastModified)
	}
	for name, value := range request.Headers {
		canonical := http.CanonicalHeaderKey(name)
		if !allowedRequestHeader(canonical) && !(f.telemetryHeaders && ValidTelemetryHeader(canonical)) {
			return Result{}, fmt.Errorf("request header %q is not allowed", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return Result{}, fmt.Errorf("request header %q is invalid", name)
		}
		httpRequest.Header.Set(canonical, value)
	}
	started := time.Now()
	response, err := f.client.Do(httpRequest)
	if err != nil {
		if request.RedactQueryInErrors {
			err = redactedTransportError{cause: err}
		}
		return Result{}, fmt.Errorf("fetch %s: %w", redactedRequestURL(parsed, request.RedactQueryInErrors), err)
	}
	defer func() { _ = response.Body.Close() }()
	contentEncoding := strings.TrimSpace(strings.Join(response.Header.Values("Content-Encoding"), ","))
	if request.IdentityEncoding && (response.Uncompressed || contentEncoding != "" && !strings.EqualFold(contentEncoding, "identity")) {
		return Result{}, errors.New("archive response uses unsupported HTTP content encoding")
	}
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Result{}, fmt.Errorf("read fetched body: %w", err)
	}
	if int64(len(body)) > maxResponseBytes {
		return Result{}, errors.New("fetched body exceeds the configured decoded size limit")
	}
	mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if mediaType == "" {
		mediaType = http.DetectContentType(body)
	}
	sum := sha256.Sum256(body)
	traceOutcome = "ok"
	if response.StatusCode >= 400 {
		traceOutcome = "error"
	}
	return Result{RequestedURL: request.URL, FinalURL: response.Request.URL.String(), Status: response.StatusCode, MediaType: strings.ToLower(mediaType), ETag: response.Header.Get("ETag"), LastModified: response.Header.Get("Last-Modified"), CacheControl: response.Header.Get("Cache-Control"), Vary: append([]string(nil), response.Header.Values("Vary")...), RetryAfter: response.Header.Get("Retry-After"), Links: append([]string(nil), response.Header.Values("Link")...), Body: body, BodyDigest: "sha256:" + hex.EncodeToString(sum[:]), FetchedAt: time.Now().UTC(), Duration: time.Since(started)}, nil
}

type redactedTransportError struct{ cause error }

// Error returns a fixed message so formatting a transport error cannot reveal its
// URL or query.
func (redactedTransportError) Error() string { return "outbound request failed" }

// Unwrap preserves the internal cause for classification; callers must not expose
// that cause in public diagnostics.
func (e redactedTransportError) Unwrap() error { return e.cause }

// redactedRequestURL copies the URL before optionally replacing its query,
// preserving the request used for transport.
func redactedRequestURL(parsed *url.URL, hideQuery bool) string {
	redacted := *parsed
	if hideQuery && redacted.RawQuery != "" {
		redacted.RawQuery = "redacted"
		redacted.ForceQuery = false
	}
	return redacted.Redacted()
}

// allowedRequestHeader accepts only canonical credential, webhook-signature and
// CloudEvent headers used by maintained callers.
func allowedRequestHeader(name string) bool {
	switch name {
	case "Authorization", "X-Api-Key", "X-Catalog-Webhook-Id", "X-Catalog-Delivery-Id", "X-Catalog-Timestamp", "X-Catalog-Signature", "X-Catalog-Previous-Signature", "Ce-Specversion", "Ce-Id", "Ce-Type", "Ce-Source", "Ce-Subject", "Ce-Dataschema":
		return true
	default:
		return false
	}
}

// validateURL normalizes the target and checks syntax, credentials, literal
// addresses and origin policy before any DNS lookup.
func (f *Fetcher) validateURL(value string) (*url.URL, error) {
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") {
		return nil, fmt.Errorf("%w: source URL is empty, too long, or contains control characters", ErrInvalidTarget)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("%w: source URL must be absolute and cannot contain user info or a fragment", ErrInvalidTarget)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "https" && (!(f.policy.AllowHTTPForTests || f.telemetryHTTP) || parsed.Scheme != "http") {
		return nil, fmt.Errorf("%w: source URL must use HTTPS", ErrInvalidTarget)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("%w: source URL query is invalid", ErrInvalidTarget)
	}
	for name := range query {
		if secretQueryParameter(name) {
			return nil, fmt.Errorf("%w: source URL cannot contain credentials or signatures in its query", ErrInvalidTarget)
		}
	}
	host, err := normalizeHostname(parsed.Hostname())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTarget, err)
	}
	port := parsed.Port()
	if literal, err := netip.ParseAddr(host); err == nil && !f.addressAllowed(literal.Unmap()) {
		return nil, fmt.Errorf("%w: source address is denied by egress policy", ErrPolicyDenied)
	}
	parsed.Host = host
	if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	}
	if port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return nil, fmt.Errorf("%w: source port is invalid", ErrInvalidTarget)
		}
		port = strconv.FormatUint(number, 10)
		parsed.Host = net.JoinHostPort(host, port)
	}
	if len(parsed.String()) > 2048 {
		return nil, fmt.Errorf("%w: source URL exceeds the normalized size limit", ErrInvalidTarget)
	}
	origin, err := NormalizeOrigin(parsed.String(), f.policy.AllowHTTPForTests || f.telemetryHTTP)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTarget, err)
	}
	if _, denied := f.policy.DeniedOrigins[origin]; denied {
		return nil, fmt.Errorf("%w: source origin is denied by egress policy", ErrPolicyDenied)
	}
	if f.policy.AllowedOrigins != nil {
		if _, allowed := f.policy.AllowedOrigins[origin]; !allowed {
			return nil, fmt.Errorf("%w: source origin is outside the selected network profile", ErrPolicyDenied)
		}
	}
	return parsed, nil
}

// NormalizeOrigin returns the exact lower-case scheme, normalized ASCII host,
// and effective port used for federation policy comparisons.
func NormalizeOrigin(value string, allowHTTP bool) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("origin must be an absolute HTTP or HTTPS URL without user information")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && (!allowHTTP || scheme != "http") {
		return "", errors.New("origin must use HTTPS")
	}
	host, err := normalizeHostname(parsed.Hostname())
	if err != nil {
		return "", err
	}
	port := parsed.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	} else {
		number, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || number == 0 {
			return "", errors.New("origin port is invalid")
		}
		port = strconv.FormatUint(number, 10)
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

// normalizeHostname canonicalizes IP literals and IDNA names while rejecting
// trailing-dot and escaped host aliases.
func normalizeHostname(value string) (string, error) {
	if value == "" || strings.HasSuffix(value, ".") || strings.ContainsAny(value, " \\/%") {
		return "", errors.New("source hostname is invalid")
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Unmap().String(), nil
	}
	value, err := idna.Lookup.ToASCII(value)
	if err != nil {
		return "", errors.New("source hostname is not valid IDNA")
	}
	value = strings.ToLower(value)
	if value == "" || len(value) > 253 || !isASCII(value) {
		return "", errors.New("source hostname is invalid; use its normalized ASCII form")
	}
	return value, nil
}

// dialContext checks every DNS answer against egress policy before dialing
// validated IP literals, preventing a second hostname lookup.
func (f *Fetcher) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse upstream address: %w", err)
	}
	addresses, err := f.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve upstream hostname: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("resolve upstream hostname: no addresses returned")
	}
	for _, candidate := range addresses {
		if !f.addressAllowed(candidate.Unmap()) {
			return nil, fmt.Errorf("%w: resolved address %s is denied by egress policy", ErrPolicyDenied, candidate)
		}
	}
	dialer := net.Dialer{Timeout: min(f.policy.Timeout, 5*time.Second), KeepAlive: 30 * time.Second}
	var lastErr error
	for _, candidate := range addresses {
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("dial upstream: %w", lastErr)
}

// secretQueryParameter rejects recognized credential and signature keys after case
// and separator normalization.
func secretQueryParameter(name string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.TrimSpace(name)))
	switch normalized {
	case "accesstoken", "apikey", "authorization", "credential", "password", "secret", "sig", "signature", "token", "xamzcredential", "xamzsecuritytoken", "xamzsignature":
		return true
	default:
		return false
	}
}

// checkRedirect reapplies URL policy and caller redirect restrictions at every
// hop; permitted origin changes strip bearer and API-key headers.
func (f *Fetcher) checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) > f.policy.MaxRedirects {
		return fmt.Errorf("%w: redirect limit exceeded", ErrPolicyDenied)
	}
	if _, err := f.validateURL(request.URL.String()); err != nil {
		// A redirect target is remote input received after the original request
		// crossed the outbound boundary. Classify every rejected redirect as a
		// policy denial so callers retain the attempt's bounded outcome rather
		// than treating it as malformed administrator input.
		return fmt.Errorf("%w: redirect target is invalid or denied", ErrPolicyDenied)
	}
	if len(via) > 0 && redirectsRejected(via[0]) {
		return fmt.Errorf("%w: redirect denied for origin-bound request", ErrPolicyDenied)
	}
	if len(via) > 0 && !sameOrigin(via[len(via)-1].URL, request.URL) {
		if originChangesRejected(via[0]) {
			return fmt.Errorf("%w: origin-changing redirect denied for credential-bound request", ErrPolicyDenied)
		}
		request.Header.Del("Authorization")
		request.Header.Del("X-Api-Key")
	}
	return nil
}

type redirectPolicyContextKey struct{}
type originChangePolicyContextKey struct{}

// redirectsRejected reads the original request's explicit prohibition on all
// redirects.
func redirectsRejected(request *http.Request) bool {
	rejected, _ := request.Context().Value(redirectPolicyContextKey{}).(bool)
	return rejected
}

// originChangesRejected reads the credential-bound request's restriction against
// cross-origin redirects.
func originChangesRejected(request *http.Request) bool {
	rejected, _ := request.Context().Value(originChangePolicyContextKey{}).(bool)
	return rejected
}

// sameOrigin compares normalized request schemes, hosts and effective ports
// without treating a default-port spelling as a new origin.
func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Hostname(), right.Hostname()) && effectivePort(left) == effectivePort(right)
}

// effectivePort supplies HTTP(S) defaults and normalizes numeric port spelling for
// redirect origin comparison.
func effectivePort(value *url.URL) string {
	if port := value.Port(); port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return port
		}
		return strconv.FormatUint(number, 10)
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(value.Scheme, "http") {
		return "80"
	}
	return ""
}

// addressAllowed applies explicit and special-use denials before allowlists;
// private and non-global addresses require their documented explicit permission.
func (f *Fetcher) addressAllowed(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() {
		return false
	}
	for _, prefix := range f.policy.DeniedAddressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	for _, prefix := range deniedSpecialUsePrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	if !address.IsGlobalUnicast() {
		if !(f.policy.AllowTestAddresses || f.telemetryLocal) || !addressInPrefixes(address, f.policy.AllowedAddressPrefixes) {
			return false
		}
	}
	if len(f.policy.AllowedAddressPrefixes) > 0 {
		return addressInPrefixes(address, f.policy.AllowedAddressPrefixes)
	}
	if address.IsPrivate() {
		return false
	}
	return true
}

// addressInPrefixes checks membership against the already normalized policy
// ranges.
func addressInPrefixes(address netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// isASCII verifies that IDNA normalization left no non-ASCII code points.
func isASCII(value string) bool {
	for _, r := range value {
		if r > 127 {
			return false
		}
	}
	return true
}
