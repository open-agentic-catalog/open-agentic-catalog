// Outbound routing selects approved tenant/origin authority and resolves
// revision-bound credentials without accepting client-created routes.

package fetcher

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const (
	PublicDisabled       = "disabled"
	PublicAllowlist      = "allowlist"
	PublicAnyPublicHTTPS = "any_public_https"
)

var (
	ErrPolicyChanged     = errors.New("federation egress policy changed after source approval")
	ErrCredentialChanged = errors.New("federation credential changed after validator approval")
)

type PublicRoute struct {
	Mode    string
	Origins []string
}

type PrivateRoute struct {
	Name               string
	Origins            []string
	AddressPrefixes    []netip.Prefix
	AssignedTenantIDs  []string
	TrustBundleFile    string
	TrustBundlePEM     []byte
	AllowTestAddresses bool
}

type RoutingPolicy struct {
	ID                    string
	Revision              int64
	Digest                string
	Public                PublicRoute
	PrivateRoutes         []PrivateRoute
	DeniedOrigins         []string
	DeniedAddressPrefixes []netip.Prefix
	AllowHTTPForTests     bool
	Timeout               time.Duration
	MaxResponseBytes      int64
	MaxRedirects          int
	UserAgent             string
}

type CredentialRequest struct {
	TenantID string
	Binding  string
	Origin   string
}

type ResolvedCredential struct {
	Revision     string
	Origin       string
	Headers      map[string]string
	Certificates []tls.Certificate
}

type CredentialResolver interface {
	Resolve(context.Context, CredentialRequest) (ResolvedCredential, error)
}

type route struct {
	origins            map[string]struct{}
	prefixes           []netip.Prefix
	tenants            map[string]struct{}
	rootCAs            *x509.CertPool
	allowTestAddresses bool
}

// Router selects an operator-defined public or tenant-assigned private route,
// then delegates the request to the hardened Fetcher boundary.
type Router struct {
	policy   RoutingPolicy
	public   route
	private  map[string]route
	denied   map[string]struct{}
	resolver CredentialResolver
}

// NewRouter validates and compiles the complete routing topology up front so an
// individual request cannot introduce an origin, address range, or trust root.
func NewRouter(policy RoutingPolicy, resolver CredentialResolver) (*Router, error) {
	if !validRoutingName(policy.ID) || policy.Revision <= 0 || !validRoutingDigest(policy.Digest) {
		return nil, errors.New("federation routing policy identity is incomplete")
	}
	if policy.Timeout <= 0 || policy.MaxResponseBytes < 1024 || policy.MaxResponseBytes > 64<<20 || policy.MaxRedirects < 0 || policy.MaxRedirects > 10 {
		return nil, errors.New("federation routing policy budgets are invalid")
	}
	if len(policy.DeniedOrigins) > 256 || len(policy.DeniedAddressPrefixes) > 256 || len(policy.PrivateRoutes) > 32 {
		return nil, errors.New("federation routing policy exceeds its bounded topology")
	}
	if err := validateRoutingPrefixes(policy.DeniedAddressPrefixes, false, false); err != nil {
		return nil, fmt.Errorf("federation denied address policy is invalid: %w", err)
	}
	denied, err := normalizedOriginSet(policy.DeniedOrigins, policy.AllowHTTPForTests)
	if err != nil {
		return nil, fmt.Errorf("federation denied origin policy is invalid: %w", err)
	}
	router := &Router{policy: policy, private: make(map[string]route), denied: denied, resolver: resolver}
	switch policy.Public.Mode {
	case PublicDisabled:
		if len(policy.Public.Origins) != 0 {
			return nil, errors.New("disabled federation public policy cannot contain origins")
		}
		router.public.origins = map[string]struct{}{}
	case PublicAllowlist:
		if len(policy.Public.Origins) > 256 {
			return nil, errors.New("federation public policy exceeds its origin limit")
		}
		router.public.origins, err = normalizedOriginSet(policy.Public.Origins, false)
		if err != nil {
			return nil, fmt.Errorf("federation public origin policy is invalid: %w", err)
		}
	case PublicAnyPublicHTTPS:
		if len(policy.Public.Origins) != 0 {
			return nil, errors.New("any-public federation policy cannot contain an origin allowlist")
		}
		router.public.origins = nil
	default:
		return nil, errors.New("federation public routing mode is invalid")
	}
	totalTrustBytes := 0
	for _, profile := range policy.PrivateRoutes {
		if !validRoutingName(profile.Name) {
			return nil, errors.New("federation private routing profile name is invalid")
		}
		if _, exists := router.private[profile.Name]; exists {
			return nil, errors.New("federation private routing profile names must be unique")
		}
		if len(profile.Origins) == 0 || len(profile.Origins) > 64 || len(profile.AddressPrefixes) == 0 || len(profile.AddressPrefixes) > 64 || len(profile.AssignedTenantIDs) == 0 || len(profile.AssignedTenantIDs) > 128 {
			return nil, errors.New("federation private routing profile exceeds or omits a bounded assignment")
		}
		origins, originErr := normalizedOriginSet(profile.Origins, policy.AllowHTTPForTests)
		if originErr != nil {
			return nil, fmt.Errorf("federation private routing profile origins are invalid: %w", originErr)
		}
		if err := validateRoutingPrefixes(profile.AddressPrefixes, true, profile.AllowTestAddresses); err != nil {
			return nil, fmt.Errorf("federation private routing profile address ranges are invalid: %w", err)
		}
		tenants, tenantErr := uniqueStringSet(profile.AssignedTenantIDs)
		if tenantErr != nil {
			return nil, fmt.Errorf("federation private routing profile tenant assignments are invalid: %w", tenantErr)
		}
		configured := route{origins: origins, prefixes: append([]netip.Prefix(nil), profile.AddressPrefixes...), tenants: tenants, allowTestAddresses: profile.AllowTestAddresses}
		if profile.TrustBundleFile != "" && len(profile.TrustBundlePEM) != 0 {
			return nil, errors.New("federation private routing profile has conflicting trust-bundle inputs")
		}
		if profile.TrustBundleFile != "" || len(profile.TrustBundlePEM) != 0 {
			var raw []byte
			var err error
			if profile.TrustBundleFile != "" {
				raw, err = readBoundedFile(profile.TrustBundleFile, 4<<20)
				if err != nil {
					return nil, fmt.Errorf("read federation trust bundle: %w", err)
				}
			} else {
				if len(profile.TrustBundlePEM) > 4<<20 {
					return nil, errors.New("federation trust bundle exceeds the supported size")
				}
				raw = profile.TrustBundlePEM
			}
			totalTrustBytes += len(raw)
			if totalTrustBytes > 16<<20 {
				return nil, errors.New("federation trust bundles exceed the supported combined size")
			}
			roots, err := loadTrustBundle(raw)
			if err != nil {
				return nil, err
			}
			configured.rootCAs = roots
		}
		router.private[profile.Name] = configured
	}
	return router, nil
}

// normalizedOriginSet rejects non-origin URL parts and collisions after origin
// normalization rather than silently merging policy assignments.
func normalizedOriginSet(values []string, allowHTTP bool) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, errors.New("routing origins cannot contain a path, query, or fragment")
		}
		origin, err := NormalizeOrigin(value, allowHTTP)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[origin]; duplicate {
			return nil, errors.New("routing origins must be unique after normalization")
		}
		result[origin] = struct{}{}
	}
	return result, nil
}

// validRoutingName accepts bounded lowercase alphanumeric names with internal
// hyphens, excluding leading or trailing separators.
func validRoutingName(value string) bool {
	if value == "" || len(value) > 63 || !lowerAlphaNumeric(value[0]) {
		return false
	}
	for index, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' && index > 0 && index < len(value)-1 {
			continue
		}
		return false
	}
	return true
}

// lowerAlphaNumeric recognizes the ASCII characters permitted at a routing name's
// boundary.
func lowerAlphaNumeric(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

// validRoutingDigest requires canonical lowercase SHA-256 text so
// equivalent-looking digest spellings cannot bypass revision checks.
func validRoutingDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

// PolicyDigest returns the exact active policy identity used to bind inspection
// and subsequent retrieval.
func (r *Router) PolicyDigest() string { return r.policy.Digest }

// Fetch binds the selected network profile and optional credential to the exact
// request origin before any connection is attempted.
func (r *Router) Fetch(ctx context.Context, request Request) (Result, error) {
	if request.ExpectedPolicyDigest != "" && request.ExpectedPolicyDigest != r.policy.Digest {
		return Result{}, ErrPolicyChanged
	}
	selected := r.public
	if request.NetworkProfile != "" {
		var ok bool
		selected, ok = r.private[request.NetworkProfile]
		if !ok {
			return Result{}, fmt.Errorf("%w: selected federation network profile is unavailable", ErrPolicyDenied)
		}
		if _, assigned := selected.tenants[request.TenantID]; !assigned {
			return Result{}, fmt.Errorf("%w: selected federation network profile is not assigned to the tenant", ErrPolicyDenied)
		}
	}
	allowHTTP := r.policy.AllowHTTPForTests && selected.origins != nil
	origin, err := NormalizeOrigin(request.URL, allowHTTP)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrInvalidTarget, err)
	}
	if _, denied := r.denied[origin]; denied {
		return Result{}, fmt.Errorf("%w: source origin is denied by egress policy", ErrPolicyDenied)
	}
	if selected.origins != nil {
		if _, allowed := selected.origins[origin]; !allowed {
			return Result{}, fmt.Errorf("%w: source origin is outside the selected network profile", ErrPolicyDenied)
		}
	}
	policy := Policy{
		Timeout:                r.policy.Timeout,
		MaxResponseBytes:       r.policy.MaxResponseBytes,
		MaxRedirects:           r.policy.MaxRedirects,
		AllowHTTPForTests:      allowHTTP,
		AllowTestAddresses:     selected.allowTestAddresses,
		AllowedOrigins:         selected.origins,
		DeniedOrigins:          r.denied,
		AllowedAddressPrefixes: append([]netip.Prefix(nil), selected.prefixes...),
		DeniedAddressPrefixes:  append([]netip.Prefix(nil), r.policy.DeniedAddressPrefixes...),
		RootCAs:                selected.rootCAs,
		UserAgent:              r.policy.UserAgent,
	}
	outbound, err := New(policy, nil)
	if err != nil {
		return Result{}, err
	}
	if _, err := outbound.validateURL(request.URL); err != nil {
		outbound.client.CloseIdleConnections()
		return Result{}, err
	}
	credentialRevision := ""
	if request.CredentialBinding != "" {
		outbound.client.CloseIdleConnections()
		if r.resolver == nil {
			return Result{}, errors.New("source credential resolution is unavailable")
		}
		if request.TenantID == "" {
			return Result{}, errors.New("source credential resolution requires a tenant")
		}
		credential, resolveErr := r.resolver.Resolve(ctx, CredentialRequest{TenantID: request.TenantID, Binding: request.CredentialBinding, Origin: origin})
		if resolveErr != nil {
			return Result{}, fmt.Errorf("resolve source credential: %w", resolveErr)
		}
		if credential.Origin != origin {
			return Result{}, fmt.Errorf("%w: resolved source credential is bound to a different origin", ErrPolicyDenied)
		}
		if credential.Revision == "" || strings.TrimSpace(credential.Revision) != credential.Revision || len(credential.Revision) > 128 || strings.IndexFunc(credential.Revision, unicode.IsControl) >= 0 {
			return Result{}, fmt.Errorf("%w: resolved source credential identity is invalid", ErrPolicyDenied)
		}
		if request.ExpectedCredentialRevision != "" && credential.Revision != request.ExpectedCredentialRevision {
			return Result{}, ErrCredentialChanged
		}
		if len(credential.Headers) > 1 || len(credential.Certificates) > 1 || len(credential.Headers) == 0 && len(credential.Certificates) == 0 || len(credential.Headers) != 0 && len(credential.Certificates) != 0 {
			return Result{}, fmt.Errorf("%w: resolved source credential shape is invalid", ErrPolicyDenied)
		}
		request.Headers = cloneHeaders(request.Headers)
		for name, value := range credential.Headers {
			canonical := http.CanonicalHeaderKey(name)
			if canonical != "Authorization" && canonical != "X-Api-Key" {
				return Result{}, fmt.Errorf("%w: resolved source credential header is unsupported", ErrPolicyDenied)
			}
			if headerExists(request.Headers, name) {
				return Result{}, fmt.Errorf("%w: resolved source credential conflicts with an outbound request header", ErrPolicyDenied)
			}
			request.Headers[name] = value
		}
		policy.Certificates = append([]tls.Certificate(nil), credential.Certificates...)
		request.RejectOriginChanges = true
		credentialRevision = credential.Revision
		outbound, err = New(policy, nil)
		if err != nil {
			return Result{}, err
		}
	}
	defer outbound.client.CloseIdleConnections()
	result, err := outbound.Fetch(ctx, request)
	if err != nil {
		return Result{}, err
	}
	result.PolicyDigest = r.policy.Digest
	result.PolicyID = r.policy.ID
	result.PolicyRevision = r.policy.Revision
	result.NetworkProfile = request.NetworkProfile
	result.CredentialRevision = credentialRevision
	return result, nil
}

// headerExists detects caller/resolver header collisions case-insensitively before
// credentials are attached.
func headerExists(headers map[string]string, wanted string) bool {
	for name := range headers {
		if strings.EqualFold(name, wanted) {
			return true
		}
	}
	return false
}

// cloneHeaders returns an independently mutable map so credential injection cannot
// change the caller's request headers.
func cloneHeaders(headers map[string]string) map[string]string {
	cloned := make(map[string]string, len(headers)+1)
	for name, value := range headers {
		cloned[name] = value
	}
	return cloned
}

// uniqueStringSet rejects empty or repeated assignments before constructing route
// membership indexes.
func uniqueStringSet(values []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return nil, errors.New("assignment cannot be empty")
		}
		if _, exists := result[value]; exists {
			return nil, errors.New("assignments must be unique")
		}
		result[value] = struct{}{}
	}
	return result, nil
}

// validateRoutingPrefixes rejects duplicate, noncanonical, default and
// mapped-address ranges, requiring private or explicit test-loopback containment
// when requested.
func validateRoutingPrefixes(prefixes []netip.Prefix, requirePrivate, allowTestAddresses bool) error {
	seen := make(map[netip.Prefix]struct{}, len(prefixes))
	for _, prefix := range prefixes {
		if !prefix.IsValid() || prefix != prefix.Masked() || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return errors.New("address ranges must be canonical non-default prefixes")
		}
		if _, duplicate := seen[prefix]; duplicate {
			return errors.New("address ranges must be unique")
		}
		seen[prefix] = struct{}{}
		if requirePrivate && !routingPrivatePrefix(prefix) && (!allowTestAddresses || !routingLoopbackPrefix(prefix)) {
			return errors.New("private profile address ranges must use private address space")
		}
	}
	return nil
}

// routingPrivatePrefix requires the entire address range to fit RFC 1918 or IPv6
// unique-local space.
func routingPrivatePrefix(prefix netip.Prefix) bool {
	privateRanges := [...]netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("fc00::/7"),
	}
	for _, privateRange := range privateRanges {
		if prefix.Addr().BitLen() == privateRange.Addr().BitLen() && prefix.Bits() >= privateRange.Bits() && privateRange.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

// routingLoopbackPrefix requires complete IPv4 or IPv6 loopback containment for
// explicitly allowed test routes.
func routingLoopbackPrefix(prefix netip.Prefix) bool {
	loopbackRanges := [...]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	for _, loopbackRange := range loopbackRanges {
		if prefix.Addr().BitLen() == loopbackRange.Addr().BitLen() && prefix.Bits() >= loopbackRange.Bits() && loopbackRange.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

// loadTrustBundle adds deployment PEM certificates to a local root pool, using an
// empty pool if system roots are unavailable.
func loadTrustBundle(raw []byte) (*x509.CertPool, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(raw) {
		return nil, errors.New("federation trust bundle contains no certificates")
	}
	return roots, nil
}

// FileCredentialResolver resolves opaque bindings from a deployment-mounted
// directory; callers never receive or persist the binding metadata itself.
type FileCredentialResolver struct {
	directory string
}

// NewFileCredentialResolver requires an existing directory addressed by an
// absolute clean path; it reads individual bindings only when resolving requests.
func NewFileCredentialResolver(directory string) (*FileCredentialResolver, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, errors.New("credential resolver directory must be an absolute clean path")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("credential resolver directory is unavailable")
	}
	return &FileCredentialResolver{directory: directory}, nil
}

type credentialMetadata struct {
	SchemaVersion   int    `json:"schemaVersion"`
	TenantID        string `json:"tenantId"`
	Revision        string `json:"revision"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
	Origin          string `json:"origin"`
	Type            string `json:"type"`
	SecretFile      string `json:"secretFile,omitempty"`
	CertificateFile string `json:"certificateFile,omitempty"`
	PrivateKeyFile  string `json:"privateKeyFile,omitempty"`
}

// Resolve enforces tenant and origin binding before reading one bounded secret
// or certificate pair from the configured directory.
func (r *FileCredentialResolver) Resolve(ctx context.Context, request CredentialRequest) (ResolvedCredential, error) {
	if err := ctx.Err(); err != nil {
		return ResolvedCredential{}, err
	}
	if !safeCredentialName(request.Binding) {
		return ResolvedCredential{}, errors.New("credential binding reference is invalid")
	}
	metadataPath := filepath.Join(r.directory, request.Binding+".json")
	raw, err := readBoundedFile(metadataPath, 64<<10)
	if err != nil {
		return ResolvedCredential{}, errors.New("credential binding is unavailable")
	}
	var metadata credentialMetadata
	if err := jsondoc.Decode(raw, &metadata); err != nil {
		return ResolvedCredential{}, errors.New("credential binding metadata is invalid")
	}
	if metadata.SchemaVersion != 1 || metadata.TenantID != request.TenantID || metadata.Revision == "" || strings.TrimSpace(metadata.Revision) != metadata.Revision || len(metadata.Revision) > 128 || strings.IndexFunc(metadata.Revision, unicode.IsControl) >= 0 {
		return ResolvedCredential{}, errors.New("credential binding identity is invalid")
	}
	if metadata.ExpiresAt != "" {
		expiresAt, parseErr := time.Parse(time.RFC3339Nano, metadata.ExpiresAt)
		if parseErr != nil || !expiresAt.After(time.Now()) {
			return ResolvedCredential{}, errors.New("credential binding has expired")
		}
	}
	parsedOrigin, parseErr := url.Parse(metadata.Origin)
	if parseErr != nil || parsedOrigin.Path != "" && parsedOrigin.Path != "/" || parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" {
		return ResolvedCredential{}, errors.New("credential binding origin does not match the request")
	}
	origin, err := NormalizeOrigin(metadata.Origin, strings.HasPrefix(request.Origin, "http://"))
	if err != nil || origin != request.Origin {
		return ResolvedCredential{}, errors.New("credential binding origin does not match the request")
	}
	resolved := ResolvedCredential{Revision: metadata.Revision, Origin: origin}
	switch metadata.Type {
	case "bearer":
		if metadata.CertificateFile != "" || metadata.PrivateKeyFile != "" {
			return ResolvedCredential{}, errors.New("credential binding metadata is invalid")
		}
		secret, readErr := r.readSecret(metadata.SecretFile)
		if readErr != nil {
			return ResolvedCredential{}, readErr
		}
		resolved.Headers = map[string]string{"Authorization": "Bearer " + secret}
	case "api_key":
		if metadata.CertificateFile != "" || metadata.PrivateKeyFile != "" {
			return ResolvedCredential{}, errors.New("credential binding metadata is invalid")
		}
		secret, readErr := r.readSecret(metadata.SecretFile)
		if readErr != nil {
			return ResolvedCredential{}, readErr
		}
		resolved.Headers = map[string]string{"X-Api-Key": secret}
	case "mtls":
		if metadata.SecretFile != "" || !safeCredentialName(metadata.CertificateFile) || !safeCredentialName(metadata.PrivateKeyFile) {
			return ResolvedCredential{}, errors.New("credential binding metadata is invalid")
		}
		certificate, readErr := readBoundedFile(filepath.Join(r.directory, metadata.CertificateFile), 1<<20)
		if readErr != nil {
			return ResolvedCredential{}, errors.New("credential certificate is unavailable")
		}
		privateKey, readErr := readBoundedFile(filepath.Join(r.directory, metadata.PrivateKeyFile), 1<<20)
		if readErr != nil {
			return ResolvedCredential{}, errors.New("credential private key is unavailable")
		}
		pair, pairErr := tls.X509KeyPair(certificate, privateKey)
		if pairErr != nil {
			return ResolvedCredential{}, errors.New("credential client certificate is invalid")
		}
		resolved.Certificates = []tls.Certificate{pair}
	default:
		return ResolvedCredential{}, errors.New("credential binding type is unsupported")
	}
	if err := ctx.Err(); err != nil {
		return ResolvedCredential{}, err
	}
	if err := verifyCredentialMetadata(metadataPath, raw); err != nil {
		return ResolvedCredential{}, err
	}
	return resolved, nil
}

// verifyCredentialMetadata rereads exact metadata bytes after secret loading and
// rejects a binding changed during resolution.
func verifyCredentialMetadata(filename string, expected []byte) error {
	current, err := readBoundedFile(filename, 64<<10)
	if err != nil || !bytes.Equal(expected, current) {
		return errors.New("credential binding changed while resolving")
	}
	return nil
}

// readSecret accepts a bounded validated leaf file, strips one terminal LF, and
// rejects empty, invalid UTF-8 or control-containing credentials.
func (r *FileCredentialResolver) readSecret(filename string) (string, error) {
	if !safeCredentialName(filename) {
		return "", errors.New("credential secret reference is invalid")
	}
	raw, err := readBoundedFile(filepath.Join(r.directory, filename), 64<<10)
	if err != nil {
		return "", errors.New("credential secret is unavailable")
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	if len(raw) == 0 || !utf8.Valid(raw) || bytes.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", errors.New("credential secret is invalid")
	}
	return string(raw), nil
}

// safeCredentialName confines references to bounded ASCII leaf names, rejecting
// path separators and dot-directory entries.
func safeCredentialName(value string) bool {
	if value == "" || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// readBoundedFile reads maximum plus one byte to detect overflow and always closes
// the deployment-selected file.
func readBoundedFile(filename string, maximum int64) ([]byte, error) {
	file, err := os.Open(filename) // #nosec G304 -- deployment policy selects the mounted path and leaf names are validated.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, errors.New("mounted file exceeds the supported size")
	}
	return raw, nil
}
