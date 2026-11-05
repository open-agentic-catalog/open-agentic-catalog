// Discovery host bindings normalize request authority and enforce tenant, audience
// and authentication policy before selecting snapshots.

package httpserver

import (
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// normalizeDiscoveryHost canonicalizes a request authority to its IP or DNS host while rejecting ambiguous authority syntax.
func normalizeDiscoveryHost(value string) (string, bool) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/\\@,\x00\r\n\t") {
		return "", false
	}
	if address := net.ParseIP(value); address != nil {
		return address.String(), true
	}
	host := value
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		address := net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
		if address == nil {
			return "", false
		}
		return address.String(), true
	}
	if strings.HasPrefix(value, "[") || strings.Count(value, ":") == 1 {
		parsedHost, port, err := net.SplitHostPort(value)
		if err == nil {
			parsedPort, parseErr := strconv.ParseUint(port, 10, 16)
			if parseErr != nil || parsedPort == 0 {
				return "", false
			}
			host = parsedHost
		} else if strings.Contains(value, ":") {
			return "", false
		}
	} else if strings.Contains(value, ":") {
		return "", false
	}
	if host == "" || strings.HasSuffix(host, ".") {
		return "", false
	}
	host = strings.ToLower(host)
	if address := net.ParseIP(host); address != nil {
		return address.String(), true
	}
	if strings.IndexFunc(host, func(character rune) bool { return character != '.' && (character < '0' || character > '9') }) == -1 {
		return "", false
	}
	if !validDiscoveryDNSName(host) {
		return "", false
	}
	return host, true
}

// validDiscoveryDNSName accepts only bounded lowercase ASCII DNS labels without edge hyphens.
func validDiscoveryDNSName(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

// authorizeDiscovery selects an approved host binding and checks its authentication/tenant policy before snapshot reads.
func (s *Server) authorizeDiscovery(writer http.ResponseWriter, request *http.Request) (DiscoveryHost, bool) {
	if request.Header.Get("Forwarded") != "" || request.Header.Get("X-Forwarded-Host") != "" || request.Header.Get("X-Original-Host") != "" {
		writeARDProblem(writer, request, catalog.E(catalog.CodeNotFound, "discovery host was not found"))
		return DiscoveryHost{}, false
	}
	host, ok := normalizeDiscoveryHost(request.Host)
	if !ok {
		writeARDProblem(writer, request, catalog.E(catalog.CodeNotFound, "discovery host was not found"))
		return DiscoveryHost{}, false
	}
	if request.URL.IsAbs() {
		urlHost, valid := normalizeDiscoveryHost(request.URL.Host)
		if !valid || urlHost != host {
			writeARDProblem(writer, request, catalog.E(catalog.CodeNotFound, "discovery host was not found"))
			return DiscoveryHost{}, false
		}
	}
	binding, exists := s.discovery[host]
	if !exists || binding.Mode == DiscoveryDisabled {
		writeARDProblem(writer, request, catalog.E(catalog.CodeNotFound, "discovery host was not found"))
		return DiscoveryHost{}, false
	}
	if binding.Mode == DiscoveryAnonymous {
		return binding, true
	}
	header := request.Header.Get("Authorization")
	scheme, token, valid := strings.Cut(header, " ")
	if !valid || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="open-agentic-catalog-discovery"`)
		writeARDProblem(writer, request, catalog.E(catalog.CodeUnauthenticated, "a valid Bearer token is required for this discovery host"))
		return DiscoveryHost{}, false
	}
	actor, err := s.authenticator.Authenticate(request.Context(), strings.TrimSpace(token))
	if err != nil {
		failure := authenticationFailure(err)
		if catalog.ErrorCodeOf(failure) == catalog.CodeUnauthenticated {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="open-agentic-catalog-discovery", error="invalid_token"`)
		}
		writeARDProblem(writer, request, failure)
		return DiscoveryHost{}, false
	}
	if actor.TenantID != binding.TenantID {
		writeARDProblem(writer, request, catalog.E(catalog.CodeForbidden, "the Bearer token tenant does not match this discovery host"))
		return DiscoveryHost{}, false
	}
	if s.enforceOAuthScopes && actor.Claims["scope:catalog.read"] != "true" {
		writer.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="catalog.read"`)
		writeARDProblem(writer, request, catalog.E(catalog.CodeForbidden, "OAuth scope catalog.read is required for this discovery host"))
		return DiscoveryHost{}, false
	}
	ctx := identity.WithPermissionContext(identity.WithActor(request.Context(), actor))
	if _, err := identity.RequirePermission(ctx, "discovery.read", ""); err != nil {
		writeARDProblem(writer, request, catalog.PermissionError("current discovery permission is required", err))
		return DiscoveryHost{}, false
	}
	*request = *request.WithContext(ctx)
	return binding, true
}

// publicationForControl requires an exact enabled host owned by the authenticated control tenant.
func (s *Server) publicationForControl(tenantID, host string) (*publication.Manager, error) {
	normalized, ok := normalizeDiscoveryHost(host)
	if !ok || normalized != host {
		return nil, catalog.E(catalog.CodeInvalidArgument, "host must be an exact configured discovery hostname")
	}
	binding, exists := s.discovery[host]
	if !exists || binding.Mode == DiscoveryDisabled || binding.TenantID != tenantID || binding.Publication == nil {
		return nil, catalog.E(catalog.CodeNotFound, "configured discovery host was not found")
	}
	return binding.Publication, nil
}

// setDiscoveryDocumentHeaders sets retained document identity and cache variation for the host's audience policy.
func setDiscoveryDocumentHeaders(writer http.ResponseWriter, binding DiscoveryHost, etag, digest, mediaType string, modifiedAt string) {
	writer.Header().Set("ETag", etag)
	writer.Header().Set("Content-Digest", digest)
	writer.Header().Set("Last-Modified", modifiedAt)
	writer.Header().Set("Content-Type", mediaType)
	writer.Header().Set("Link", publicDiscoveryLinks())
	if binding.Mode == DiscoveryAnonymous {
		writer.Header().Set("Cache-Control", "public, no-cache, no-transform")
		addVary(writer.Header(), "Accept-Encoding")
		return
	}
	writer.Header().Set("Cache-Control", "private, no-cache, no-transform")
	addVary(writer.Header(), "Authorization", "Accept-Encoding")
}

// addVary merges case-normalized response variation names deterministically without duplicates.
func addVary(header http.Header, names ...string) {
	seen := make(map[string]struct{})
	for _, value := range header.Values("Vary") {
		for _, name := range strings.Split(value, ",") {
			name = http.CanonicalHeaderKey(strings.TrimSpace(name))
			if name != "" {
				seen[name] = struct{}{}
			}
		}
	}
	for _, name := range names {
		name = http.CanonicalHeaderKey(strings.TrimSpace(name))
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	values := make([]string, 0, len(seen))
	for name := range seen {
		values = append(values, name)
	}
	slices.Sort(values)
	header.Set("Vary", strings.Join(values, ", "))
}
