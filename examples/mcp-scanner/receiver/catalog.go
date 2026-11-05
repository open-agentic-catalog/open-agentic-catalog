package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

type catalogReader interface {
	GetCatalogRecordWithResponse(context.Context, sdk.CatalogRecordId, ...sdk.RequestEditorFn) (*sdk.GetCatalogRecordResponse, error)
	BrowseCatalogWithResponse(context.Context, *sdk.BrowseCatalogParams, ...sdk.RequestEditorFn) (*sdk.BrowseCatalogResponse, error)
	GetWebhookReconciliationWithResponse(context.Context, ...sdk.RequestEditorFn) (*sdk.GetWebhookReconciliationResponse, error)
}

type reference struct {
	TenantID             string `json:"tenantId"`
	NamespaceID          string `json:"namespaceId"`
	ResourceID           string `json:"resourceId"`
	ReleaseID            string `json:"releaseId"`
	ListingRevisionID    string `json:"listingRevisionId"`
	VersionRevisionID    string `json:"versionRevisionId"`
	ListingDigest        string `json:"listingDigest"`
	ContentSetDigest     string `json:"contentSetDigest"`
	RepresentationID     string `json:"representationId"`
	RepresentationDigest string `json:"representationDigest"`
	Endpoint             string `json:"endpoint"`
}

func readRecord(ctx context.Context, c catalogReader, id, tenant string, configs []mapping, now time.Time) (reference, *mapping, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return reference{}, nil, failed("invalid_release", false)
	}
	r, err := c.GetCatalogRecordWithResponse(ctx, u)
	if err != nil || r == nil {
		return reference{}, nil, failed("catalog_unavailable", true)
	}
	if r.StatusCode() == 401 || r.StatusCode() == 403 {
		return reference{}, nil, failed("catalog_access_denied", false)
	}
	if r.StatusCode() == 404 {
		return reference{}, nil, failed("catalog_record_unavailable", false)
	}
	if r.StatusCode() != http.StatusOK || r.JSON200 == nil {
		return reference{}, nil, failed("catalog_read_failed", r.StatusCode() >= 500 || r.StatusCode() == 429)
	}
	if uniqueJSON(r.Body) != nil {
		return reference{}, nil, failed("invalid_catalog_record", false)
	}
	return validateRecord(*r.JSON200, id, tenant, configs, now)
}

func validateRecord(r sdk.CatalogRecord, id, tenant string, configs []mapping, now time.Time) (reference, *mapping, error) {
	bad := failed("ineligible_catalog_record", false)
	for _, value := range []uuid.UUID{r.Resource.Id, r.Release.Id, r.Listing.Id, r.ListingRevision.Id, r.Version.Id, r.VersionRevision.Id} {
		if value == uuid.Nil {
			return reference{}, nil, bad
		}
	}
	if r.Source != "local" || r.Resource.Kind != "mcp_server" || r.Resource.TenantId.String() != tenant || r.Release.TenantId.String() != tenant || r.Version.TenantId.String() != tenant || r.VersionRevision.TenantId.String() != tenant || r.Listing.TenantId.String() != tenant || r.ListingRevision.TenantId.String() != tenant {
		return reference{}, nil, bad
	}
	if r.Release.Id.String() != id || r.Listing.CurrentReleaseId == nil || *r.Listing.CurrentReleaseId != r.Release.Id || r.Release.ListingId != r.Listing.Id || r.Release.ListingRevisionId != r.ListingRevision.Id || r.Release.VersionRevisionId != r.VersionRevision.Id || r.Listing.ResourceId != r.Resource.Id || r.Listing.NamespaceId != r.Resource.NamespaceId || r.Version.ResourceId != r.Resource.Id || r.ListingRevision.ListingId != r.Listing.Id || r.ListingRevision.VersionRevisionId != r.VersionRevision.Id || r.VersionRevision.ResourceVersionId != r.Version.Id {
		return reference{}, nil, bad
	}
	if r.Release.State != "published" && r.Release.State != "deprecated" || r.Release.SunsetAt != nil && !r.Release.SunsetAt.After(now) || !digestPattern.MatchString(r.ListingRevision.Digest) || !digestPattern.MatchString(r.VersionRevision.ContentSetDigest) {
		return reference{}, nil, bad
	}
	var selected *mapping
	for i := range configs {
		if configs[i].ResourceID == r.Resource.Id.String() && configs[i].NamespaceID == r.Resource.NamespaceId.String() {
			selected = &configs[i]
			break
		}
	}
	if selected == nil {
		return reference{}, nil, failed("unmapped_resource", false)
	}
	var found *sdk.Representation
	for i := range r.VersionRevision.Representations {
		p := &r.VersionRevision.Representations[i]
		if p.Role != "primary" || p.MediaType != "application/mcp-server-card+json" && p.MediaType != "application/vnd.mcp.server+json" {
			continue
		}
		if p.Data == nil || p.Id == uuid.Nil || !digestPattern.MatchString(p.ContentDigest) || p.ValidationState != "valid" {
			return reference{}, nil, bad
		}
		remotes, ok := (*p.Data)["remotes"].([]any)
		if !ok {
			return reference{}, nil, bad
		}
		matches := 0
		for _, item := range remotes {
			remote, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if remote["type"] == "streamable-http" && remote["url"] == selected.Endpoint {
				matches++
			}
		}
		if matches > 0 {
			if found != nil || matches != 1 {
				return reference{}, nil, bad
			}
			found = p
		}
	}
	if found == nil {
		return reference{}, nil, failed("endpoint_mapping_mismatch", false)
	}
	return reference{TenantID: tenant, NamespaceID: selected.NamespaceID, ResourceID: selected.ResourceID, ReleaseID: id, ListingRevisionID: r.ListingRevision.Id.String(), VersionRevisionID: r.VersionRevision.Id.String(), ListingDigest: r.ListingRevision.Digest, ContentSetDigest: r.VersionRevision.ContentSetDigest, RepresentationID: found.Id.String(), RepresentationDigest: found.ContentDigest, Endpoint: selected.Endpoint}, selected, nil
}

// discover completes the bounded traversal before scheduling or withdrawing work.
func discover(ctx context.Context, c catalogReader, configs []mapping, tenant string) (map[string]string, error) {
	spaces := map[string]bool{}
	wanted := map[string]mapping{}
	for _, m := range configs {
		spaces[m.NamespaceID] = true
		wanted[m.ResourceID] = m
	}
	ordered := make([]string, 0, len(spaces))
	for s := range spaces {
		ordered = append(ordered, s)
	}
	sort.Strings(ordered)
	out := map[string]string{}
	pages := 0
	for _, space := range ordered {
		var cursor *sdk.Cursor
		seen := map[string]bool{}
		for {
			pages++
			if pages > maxPages {
				return nil, failed("catalog_page_limit", false)
			}
			ns := uuid.MustParse(space)
			limit := sdk.Limit(32)
			kind := sdk.ResourceKind("mcp_server")
			r, err := c.BrowseCatalogWithResponse(ctx, &sdk.BrowseCatalogParams{NamespaceId: &ns, Limit: &limit, Kind: &kind, Cursor: cursor})
			if err != nil || r == nil {
				return nil, failed("catalog_unavailable", true)
			}
			if r.StatusCode() != 200 || r.JSON200 == nil || uniqueJSON(r.Body) != nil {
				return nil, failed("catalog_browse_failed", r.StatusCode() >= 500 || r.StatusCode() == 429)
			}
			if r.JSON200.Items == nil || len(r.JSON200.Items) > 32 {
				return nil, failed("invalid_catalog_page", false)
			}
			var rawPage struct {
				Total *int            `json:"total"`
				Next  json.RawMessage `json:"nextCursor"`
			}
			if json.Unmarshal(r.Body, &rawPage) != nil || rawPage.Total == nil || *rawPage.Total < len(r.JSON200.Items) || len(rawPage.Next) > 0 && rawPage.Next[0] != '"' {
				return nil, failed("invalid_catalog_page", false)
			}
			for _, item := range r.JSON200.Items {
				if item.Source != "local" || item.Resource.TenantId.String() != tenant || item.Resource.Kind != "mcp_server" || item.Resource.NamespaceId.String() != space {
					return nil, failed("invalid_catalog_page", false)
				}
				id := item.Resource.Id.String()
				if _, ok := wanted[id]; !ok {
					continue
				}
				if _, ok := out[id]; ok {
					return nil, failed("duplicate_catalog_resource", false)
				}
				out[id] = item.Release.Id.String()
			}
			cursor = r.JSON200.NextCursor
			if cursor == nil {
				break
			}
			if *cursor == "" || len(*cursor) > 4096 || seen[*cursor] {
				return nil, failed("invalid_catalog_cursor", false)
			}
			seen[*cursor] = true
		}
	}
	return out, nil
}

func readLoss(ctx context.Context, c catalogReader) ([2]int64, error) {
	r, err := c.GetWebhookReconciliationWithResponse(ctx)
	if err != nil || r == nil || r.StatusCode() != 200 || r.JSON200 == nil || uniqueJSON(r.Body) != nil {
		return [2]int64{}, failed("reconciliation_counters_unavailable", true)
	}
	// Decode required counts from the raw object so missing/null counts cannot
	// masquerade as a successful zero observation in generated Go zero values.
	var v struct {
		Capacity *int64 `json:"capacityLossCount"`
		Delivery *int64 `json:"deliveryLossCount"`
		Pending  *int64 `json:"pendingEvents"`
		Limit    *int64 `json:"pendingEventLimit"`
	}
	if json.Unmarshal(r.Body, &v) != nil || v.Capacity == nil || v.Delivery == nil || v.Pending == nil || v.Limit == nil || *v.Capacity < 0 || *v.Delivery < 0 || *v.Pending < 0 || *v.Pending > 1000 || *v.Limit != 1000 {
		return [2]int64{}, failed("invalid_reconciliation_counters", false)
	}
	return [2]int64{*v.Capacity, *v.Delivery}, nil
}
