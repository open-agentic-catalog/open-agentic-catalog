package httpserver

import (
	"net/http"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// activityFamily accepts only the matched ServeMux pattern, never a raw URL.
// Worker operations do not call this API-listener classifier.
func activityFamily(method, pattern string) observability.HTTPFamily {
	if pattern == "/mcp" {
		if method == http.MethodPost || method == http.MethodOptions {
			return observability.FamilyHTTPMCP
		}
		return observability.FamilyAuxiliary
	}
	if pattern == mcpMetadataPath {
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			return observability.FamilyHTTPMCP
		}
		return observability.FamilyAuxiliary
	}
	registeredMethod, path, ok := strings.Cut(pattern, " ")
	if !ok || (registeredMethod != method && !(registeredMethod == http.MethodGet && method == http.MethodHead)) {
		return observability.FamilyAuxiliary
	}
	switch pattern {
	case "POST /api/v1/catalog/search":
		return observability.FamilyControlSearch
	case "POST /api/v1/catalog/explore":
		return observability.FamilyControlExplore
	case "POST /api/v1/observations:search":
		return observability.FamilyObservationSearch
	case "POST /api/v1/federation/sources:live-search":
		return observability.FamilyAdministrativeLiveSearch
	case "POST /ard/v1/search":
		return observability.FamilyARDSearch
	case "POST /ard/v1/explore":
		return observability.FamilyARDExplore
	case "GET /ard/v1/agents":
		return observability.FamilyARDList
	case "GET /.well-known/ard.json", "GET /.well-known/ai-catalog.json", "GET /public/v1/artifacts/{artifactId}":
		return observability.FamilyDiscoveryDocuments
	case "GET /livez", "GET /readyz", "GET /metrics", "GET /version", "GET /openapi/control-v1.yaml", "GET /api/v1/service-information", "GET /api/v1/capabilities":
		return observability.FamilyPlatform
	case "GET /api/v1/service-activity":
		return observability.FamilyObserver
	case "POST /api/v1/access/capabilities", "POST /api/v1/artwork/read", "POST /api/v1/ratings/read", "POST /api/v1/personal/items/{itemId}/resolve":
		return observability.FamilyControlRead
	}
	if strings.HasPrefix(path, "/api/v1/") {
		switch registeredMethod {
		case http.MethodGet:
			return observability.FamilyControlRead
		case http.MethodPost, http.MethodPatch, http.MethodDelete:
			return observability.FamilyControlCommand
		}
	}
	return observability.FamilyAuxiliary
}
