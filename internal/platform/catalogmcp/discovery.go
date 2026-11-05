package catalogmcp

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

var facetFields = []string{"kind", "visibility", "publisher", "tag", "category", "namespace", "trustState", "source"}

func parseExplore(raw json.RawMessage) (catalog.ExploreRequest, error) {
	bad := safeerr.New("invalid explore arguments")
	var input struct {
		Query  json.RawMessage `json:"query"`
		Facets []struct {
			Field    string `json:"field"`
			Limit    *int   `json:"limit,omitempty"`
			MinCount int    `json:"minCount,omitempty"`
		} `json:"facets"`
	}
	if jsondoc.Decode(raw, &input) != nil || len(input.Facets) < 1 || len(input.Facets) > 10 {
		return catalog.ExploreRequest{}, bad
	}
	// Search owns the identical filter checks. Explicitly exclude pagination
	// and origins before using it: Explore covers local releases only.
	query := json.RawMessage(`{}`)
	if len(input.Query) != 0 {
		query = input.Query
	}
	var fields map[string]json.RawMessage
	if jsondoc.Validate(query) != nil || json.Unmarshal(query, &fields) != nil || fields == nil {
		return catalog.ExploreRequest{}, bad
	}
	for _, name := range []string{"origins", "limit", "cursor"} {
		if fields[name] != nil {
			return catalog.ExploreRequest{}, bad
		}
	}
	selected, err := toolRequest("catalog_control_search", query)
	if err != nil {
		return catalog.ExploreRequest{}, bad
	}
	result := catalog.ExploreRequest{Query: selected.query, Facets: make([]catalog.FacetRequest, 0, len(input.Facets))}
	result.Query.Limit, result.Query.Origins = 0, nil
	seen := map[string]bool{}
	for _, f := range input.Facets {
		limit := 20
		if f.Limit != nil {
			limit = *f.Limit
		}
		if !slices.Contains(facetFields, f.Field) || seen[f.Field] || limit < 1 || limit > 100 || f.MinCount < 0 {
			return catalog.ExploreRequest{}, bad
		}
		seen[f.Field] = true
		result.Facets = append(result.Facets, catalog.FacetRequest{Field: f.Field, Limit: limit, MinCount: f.MinCount})
	}
	return result, nil
}

func projectExplore(raw json.RawMessage, request catalog.ExploreRequest) (json.RawMessage, error) {
	bad := safeerr.New("invalid explore response")
	var input struct {
		Matched *int `json:"matched"`
		Facets  map[string]struct {
			Buckets []struct {
				Value *string `json:"value"`
				Count *int    `json:"count"`
			} `json:"buckets"`
			OtherCount json.RawMessage `json:"otherCount"`
		} `json:"facets"`
		Tier           string `json:"tier"`
		Profile        string `json:"profile"`
		FallbackReason string `json:"fallbackReason"`
		PartialIndex   bool   `json:"partialIndex"`
	}
	if consume(raw, &input) != nil || input.Matched == nil || *input.Matched < 0 || len(input.Facets) != len(request.Facets) {
		return nil, bad
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, bad
	}
	for _, name := range []string{"tier", "profile", "fallbackReason", "partialIndex"} {
		if string(fields[name]) == "null" {
			return nil, bad
		}
	}
	if fields["tier"] != nil && input.Tier != "core" && input.Tier != "hybrid" || fields["profile"] != nil && input.Profile != catalog.CoreSearchProfile && !digest(input.Profile) {
		return nil, bad
	}
	result := catalog.ExploreResponse{Matched: *input.Matched, Facets: map[string]catalog.FacetResult{}, Tier: input.Tier, Profile: input.Profile, FallbackReason: input.FallbackReason, PartialIndex: input.PartialIndex}
	if fields["fallbackReason"] != nil && !slices.Contains([]string{"indexing", "provider_unavailable", "index_changed", "work_limit"}, input.FallbackReason) {
		return nil, bad
	}
	for _, f := range request.Facets {
		value, ok := input.Facets[f.Field]
		if !ok || value.Buckets == nil || len(value.Buckets) > f.Limit {
			return nil, bad
		}
		projected := catalog.FacetResult{Buckets: []catalog.FacetBucket{}}
		if len(value.OtherCount) != 0 && (jsondoc.Decode(value.OtherCount, &projected.OtherCount) != nil || projected.OtherCount < 1) {
			return nil, bad
		}
		seen := map[string]bool{}
		for _, bucket := range value.Buckets {
			if bucket.Value == nil || *bucket.Value == "" || !bounded(*bucket.Value, 512) || seen[*bucket.Value] || bucket.Count == nil || *bucket.Count < max(1, f.MinCount) || *bucket.Count > *input.Matched {
				return nil, bad
			}
			seen[*bucket.Value] = true
			projected.Buckets = append(projected.Buckets, catalog.FacetBucket{Value: *bucket.Value, Count: *bucket.Count})
		}
		result.Facets[f.Field] = projected
	}
	return json.Marshal(result)
}

type accessProjection struct {
	TenantID                string                               `json:"tenantId"`
	Permissions             []identity.Permission                `json:"permissions"`
	AvailablePermissions    []identity.Permission                `json:"availablePermissions"`
	AvailablePermissionsUse string                               `json:"availablePermissionsUse"`
	AuthorizationRevision   int64                                `json:"authorizationRevision"`
	NamespaceGrants         []identity.NamespacePermissionAccess `json:"namespaceGrants"`
	NextCursor              string                               `json:"nextCursor,omitempty"`
	ScopeEnforcement        bool                                 `json:"scopeEnforcement"`
	Scopes                  []string                             `json:"scopes"`
}

func projectAccess(raw json.RawMessage, request AccessRequest) (json.RawMessage, error) {
	bad := safeerr.New("invalid access response")
	var input struct {
		TenantID              string                               `json:"tenantId"`
		Permissions           []identity.Permission                `json:"permissions"`
		AvailablePermissions  []identity.Permission                `json:"availablePermissions"`
		AuthorizationRevision *int64                               `json:"authorizationRevision"`
		NamespaceGrants       []identity.NamespacePermissionAccess `json:"namespaceGrants"`
		NextCursor            json.RawMessage                      `json:"nextCursor"`
		ScopeEnforcement      *bool                                `json:"scopeEnforcement"`
		Scopes                []string                             `json:"scopes"`
	}
	if consume(raw, &input) != nil || !identity.ValidTenantID(input.TenantID) || !validPermissions(input.Permissions) || !validPermissions(input.AvailablePermissions) || input.AuthorizationRevision == nil || *input.AuthorizationRevision < 1 || input.NamespaceGrants == nil || len(input.NamespaceGrants) > request.Limit || input.ScopeEnforcement == nil || input.Scopes == nil || len(input.Scopes) > 32 {
		return nil, bad
	}
	var cursor string
	if len(input.NextCursor) != 0 && jsondoc.Decode(input.NextCursor, &cursor) != nil || len(cursor) > 2048 || !bounded(cursor, 2048) {
		return nil, bad
	}
	seen := map[string]bool{}
	for _, grant := range input.NamespaceGrants {
		if !identity.ValidTenantID(grant.NamespaceID) || seen[grant.NamespaceID] || !validPermissions(grant.Permissions) || request.NamespaceID != "" && !strings.EqualFold(request.NamespaceID, grant.NamespaceID) {
			return nil, bad
		}
		seen[grant.NamespaceID] = true
	}
	seen = map[string]bool{}
	for _, scope := range input.Scopes {
		if scope == "" || !bounded(scope, 128) || seen[scope] {
			return nil, bad
		}
		seen[scope] = true
	}
	return json.Marshal(accessProjection{input.TenantID, input.Permissions, input.AvailablePermissions, "navigation_only", *input.AuthorizationRevision, input.NamespaceGrants, cursor, *input.ScopeEnforcement, input.Scopes})
}

func validPermissions(values []identity.Permission) bool {
	if values == nil || len(values) > len(identity.Permissions()) {
		return false
	}
	seen := map[identity.Permission]bool{}
	for _, value := range values {
		if seen[value] || identity.ValidatePermissionChecks([]identity.PermissionCheck{{Permission: value}}) != nil {
			return false
		}
		seen[value] = true
	}
	return true
}

// Compile-time checks bind both implementations to the same concrete reads.
var _ Reader = remoteReader{}
