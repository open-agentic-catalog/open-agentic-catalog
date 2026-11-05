package main

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func readExportSelection(ctx context.Context, path string, limit int) (exportSelection, error) {
	var selection exportSelection
	root, name, err := openImportRoot(path)
	if err != nil {
		return selection, safeerr.New("export: selection must be a regular local file without symlinks")
	}
	defer func() { _ = root.Close() }()
	raw, err := readImportAsset(ctx, root, name, exportSelectionLimit)
	if err != nil || jsondoc.Decode(raw, &selection) != nil || selection.Format != 1 || selection.Origin == "" || selection.Items == nil || len(selection.Items) > 64 {
		return selection, safeerr.New("export: invalid or oversized selection file")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields["complete"] == nil {
		return selection, safeerr.New("export: selection requires an explicit completion status")
	}
	origin, err := remoteOrigin(selection.Origin)
	if err != nil || origin != selection.Origin || selection.Complete && selection.StopReason != "exhausted" || !selection.Complete && selection.StopReason != "record_limit" && selection.StopReason != "page_limit" && selection.StopReason != "server_partial" {
		return selection, safeerr.New("export: invalid selection origin or completion status")
	}
	selection.Items, err = deduplicateExportSelection(selection.Items, limit)
	return selection, err
}

func deduplicateExportSelection(items []exportSelectionItem, limit int) ([]exportSelectionItem, error) {
	result := make([]exportSelectionItem, 0, len(items))
	seen := map[string]exportSelectionItem{}
	for _, item := range items {
		item.ReleaseID, item.RepresentationID = strings.ToLower(item.ReleaseID), strings.ToLower(item.RepresentationID)
		if !identity.ValidTenantID(item.ReleaseID) || item.RepresentationID != "" && !identity.ValidTenantID(item.RepresentationID) {
			return nil, safeerr.New("export: selection requires release and optional representation UUIDs")
		}
		// Frozen preview identities are all-or-none. Never accept a half-bound
		// release whose fields could silently be ignored during download.
		if item != (exportSelectionItem{ReleaseID: item.ReleaseID, RepresentationID: item.RepresentationID}) {
			for _, id := range []string{item.TenantID, item.ResourceID, item.VersionID, item.VersionRevisionID, item.ListingID, item.ListingRevisionID} {
				if !identity.ValidTenantID(id) || id != strings.ToLower(id) {
					return nil, safeerr.New("export: preview selection has incomplete identities")
				}
			}
			if !item.Kind.Valid() || !utf8.ValidString(item.Version) || utf8.RuneCountInString(item.Version) < 1 || utf8.RuneCountInString(item.Version) > 128 {
				return nil, safeerr.New("export: preview selection has an invalid kind or version")
			}
		}
		if previous, ok := seen[item.ReleaseID]; ok {
			if previous != item {
				return nil, safeerr.New("export: conflicting selections for the same release")
			}
			continue
		}
		seen[item.ReleaseID] = item
		result = append(result, item)
		if len(result) > limit {
			return nil, safeerr.New("export: selection exceeds the configured record limit")
		}
	}
	return result, nil
}

func previewExport(ctx context.Context, session *remoteSession, o exportBatchOptions) (exportSelection, error) {
	selection := exportSelection{Format: 1, Origin: session.origin, Complete: false, StopReason: "page_limit", Items: []exportSelectionItem{}}
	cursors := map[string]bool{"": true}
	cursor := ""
	for page := 0; page < o.maxPages; page++ {
		options := o.remote
		if o.query == "search" {
			query := o.search
			query.Cursor = cursor
			options.path, options.search = "/api/v1/catalog/search", &query
		} else {
			parameters := url.Values{"limit": {strconv.Itoa(o.search.Limit)}}
			for key, value := range map[string]string{"cursor": cursor, "namespaceId": o.search.NamespaceID, "visibility": string(o.search.Visibility)} {
				if value != "" {
					parameters.Set(key, value)
				}
			}
			if len(o.search.Kinds) == 1 {
				parameters.Set("kind", string(o.search.Kinds[0]))
			}
			options.path = "/api/v1/catalog/resources?" + parameters.Encode()
		}
		reply, err := exchangeRemote(ctx, session.client, options, session.origin, session.token)
		if err != nil {
			return selection, err
		}
		items, next, partial, err := decodeExportPage(reply.Data, o.query, o.search.Limit)
		if err != nil {
			return selection, err
		}
		for _, item := range items {
			all, err := deduplicateExportSelection(append(append([]exportSelectionItem{}, selection.Items...), item), 65)
			if err != nil {
				return selection, err
			}
			if len(all) > o.maxRecords {
				selection.StopReason = "record_limit"
				return selection, nil
			}
			selection.Items = all
		}
		if partial {
			selection.StopReason = "server_partial"
			return selection, nil
		}
		if next == "" {
			selection.Complete, selection.StopReason = true, "exhausted"
			return selection, nil
		}
		if cursors[next] {
			return selection, safeerr.New("export: repeated continuation cursor; no complete preview saved")
		}
		cursors[next], cursor = true, next
		if len(selection.Items) == o.maxRecords {
			selection.StopReason = "record_limit"
			return selection, nil
		}
	}
	return selection, nil
}

// Inspect only identities consumed by export. Evidence, ratings and display
// additions cannot change or block an otherwise valid release selection.
type exportPageLocal struct {
	Resource        catalog.CatalogResourceSummary `json:"resource"`
	Version         catalog.CatalogVersionSummary  `json:"version"`
	VersionRevision catalog.CatalogRevisionSummary `json:"versionRevision"`
	Listing         catalog.CatalogListingSummary  `json:"listing"`
	ListingRevision struct {
		ID string `json:"id"`
	} `json:"listingRevision"`
	Release struct {
		ID                string `json:"id"`
		VersionRevisionID string `json:"versionRevisionId"`
		ListingRevisionID string `json:"listingRevisionId"`
	} `json:"release"`
	Source string `json:"source"`
}

func decodeExportPage(raw []byte, mode string, limit int) ([]exportSelectionItem, string, bool, error) {
	var page struct {
		Items      []json.RawMessage `json:"items"`
		NextCursor json.RawMessage   `json:"nextCursor"`
		Partial    *bool             `json:"partial"`
	}
	fail := safeerr.New("export: invalid local selection page")
	if jsondoc.Validate(raw) != nil || jsonv2.Unmarshal(raw, &page, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)) != nil || page.Items == nil || len(page.Items) > limit {
		return nil, "", false, fail
	}
	// An omitted cursor ends the page. Explicit null must not silently become
	// an empty string and turn malformed continuation into a complete preview.
	var next string
	if len(page.NextCursor) != 0 && jsondoc.Decode(page.NextCursor, &next) != nil || len(next) > catalog.MaxCatalogCursorBytes || !utf8.ValidString(next) || strings.ContainsRune(next, '\x00') {
		return nil, "", false, fail
	}
	if mode == "search" && page.Partial == nil {
		return nil, "", false, fail
	}
	items := []exportSelectionItem{}
	for _, raw := range page.Items {
		var summary exportPageLocal
		if mode == "search" {
			var result struct {
				ID             string                   `json:"id"`
				Type           catalog.SearchResultType `json:"type"`
				Kind           catalog.Kind             `json:"kind"`
				Version        string                   `json:"version"`
				VersionPresent bool                     `json:"versionPresent"`
				Local          *exportPageLocal         `json:"local"`
				Indexed        json.RawMessage          `json:"indexed"`
				Live           json.RawMessage          `json:"live"`
			}
			if jsonv2.Unmarshal(raw, &result, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)) != nil || result.Type != catalog.SearchResultLocal || result.Local == nil || len(result.Indexed) != 0 || len(result.Live) != 0 || result.ID != "local:"+result.Local.Release.ID || result.Kind != result.Local.Resource.Kind || result.Version != result.Local.Version.Version || !result.VersionPresent {
				return nil, "", false, fail
			}
			summary = *result.Local
		} else if jsonv2.Unmarshal(raw, &summary, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)) != nil || summary.Source != "local" {
			return nil, "", false, fail
		}
		if summary.Release.VersionRevisionID != summary.VersionRevision.ID || summary.Release.ListingRevisionID != summary.ListingRevision.ID {
			return nil, "", false, fail
		}
		items = append(items, exportSelectionItem{ReleaseID: summary.Release.ID, TenantID: summary.Resource.TenantID, ResourceID: summary.Resource.ID, VersionID: summary.Version.ID, VersionRevisionID: summary.VersionRevision.ID, ListingID: summary.Listing.ID, ListingRevisionID: summary.ListingRevision.ID, Kind: summary.Resource.Kind, Version: summary.Version.Version})
	}
	items, err := deduplicateExportSelection(items, limit)
	return items, next, page.Partial != nil && *page.Partial, err
}
