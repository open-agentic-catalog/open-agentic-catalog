package catalogmcp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func bounded(value string, limit int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && !strings.ContainsRune(value, '\x00')
}
func digest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

// Decode only consumed fields, with case-sensitive names. Unknown API additions
// remain forward-compatible. Arguments use the separate closed decoder.
func consume(raw []byte, target any) error {
	if jsondoc.Validate(raw) != nil {
		return safeerr.New("invalid response")
	}
	return jsonv2.Unmarshal(raw, target, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false))
}

func output(value controlclient.Envelope, version string, toolBytes int, credential string) *mcp.CallToolResult {
	encoded, err := controlclient.JSON(value)
	if err != nil {
		return failure("invalid_response", "The projected response cannot be encoded.")
	}
	// Check decoded strings as well as raw bytes: escaped credentials must not
	// evade reflection protection. Do not log or include the offending value.
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return failure("invalid_response", "The projected response is invalid.")
		}
		if text, ok := token.(string); ok && credential != "" && strings.Contains(text, credential) {
			return failure("invalid_response", "The response reflects a configured credential.")
		}
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}, StructuredContent: json.RawMessage(encoded)}
	// SDK 1.8 adds server identity and resultType for the newest protocol.
	// Budget that complete form, without sending new fields to older clients.
	measured := *result
	measured.Meta = mcp.Meta{mcp.MetaKeyServerInfo: &mcp.Implementation{Name: serverName, Version: version}}
	serialized, err := json.Marshal(&measured)
	if err != nil || len(serialized)+len(`,"resultType":"complete"`) > toolBytes {
		return failure("output_limit", "The complete projected result exceeds the tool limit. Reduce the page size or narrow the filters; no items or cursor were returned.")
	}
	return result
}

type liveReference struct {
	SourceID               string `json:"sourceId"`
	Source                 string `json:"source"`
	SourceRevision         int64  `json:"sourceRevision"`
	PresentationGeneration int64  `json:"presentationGeneration"`
	URL                    string `json:"url,omitempty"`
	MediaType              string `json:"mediaType,omitempty"`
}

type resultProjection struct {
	ID             string                         `json:"id"`
	Type           catalog.SearchResultType       `json:"type"`
	Identifier     string                         `json:"identifier"`
	Version        string                         `json:"version,omitempty"`
	VersionPresent bool                           `json:"versionPresent"`
	Kind           catalog.Kind                   `json:"kind"`
	DisplayName    string                         `json:"displayName"`
	Description    string                         `json:"description,omitempty"`
	Tags           []string                       `json:"tags"`
	Publisher      string                         `json:"publisher,omitempty"`
	Score          int                            `json:"score"`
	Local          *catalog.CatalogSearchLocal    `json:"local,omitempty"`
	Indexed        *catalog.IndexedSearchEvidence `json:"indexed,omitempty"`
	Live           *liveReference                 `json:"live,omitempty"`
}

func projectResult(raw json.RawMessage) (resultProjection, error) {
	var input catalog.CatalogSearchResult
	bad := safeerr.New("invalid result")
	var required struct {
		VersionPresent *bool           `json:"versionPresent"`
		Score          *int            `json:"score"`
		Version        json.RawMessage `json:"version"`
	}
	if consume(raw, &input) != nil || consume(raw, &required) != nil || required.VersionPresent == nil || required.Score == nil || !input.Kind.Valid() || input.Tags == nil || input.ID == "" || input.Identifier == "" || !bounded(input.Identifier, 512) || !bounded(input.Version, 128) || input.DisplayName == "" || !bounded(input.DisplayName, 512) || !bounded(input.Description, 4000) || input.Score < 0 || input.Score > 100 || !input.VersionPresent && input.Version != "" {
		return resultProjection{}, bad
	}
	// A missing version can mean empty or absent according to versionPresent.
	// JSON null is neither: Go's legacy string decoder otherwise erases it.
	if len(required.Version) != 0 {
		var version string
		if jsondoc.Decode(required.Version, &version) != nil {
			return resultProjection{}, bad
		}
	}
	output := resultProjection{ID: input.ID, Type: input.Type, Identifier: input.Identifier, Version: input.Version, VersionPresent: input.VersionPresent, Kind: input.Kind, DisplayName: input.DisplayName, Description: input.Description, Tags: input.Tags, Publisher: input.Publisher, Score: input.Score, Local: input.Local, Indexed: input.Indexed}
	switch input.Type {
	case catalog.SearchResultLocal:
		if input.Local == nil || input.Indexed != nil || input.Live != nil || !validLocal(*input.Local) || input.ID != "local:"+input.Local.Release.ID || input.Identifier != input.Local.Resource.CanonicalIdentifier || input.Version != input.Local.Version.Version || !input.VersionPresent || input.Kind != input.Local.Resource.Kind {
			return resultProjection{}, bad
		}
	case catalog.SearchResultIndexed:
		id, err := catalog.ParseAIRIdentifier(input.Identifier)
		if err != nil || id.String() != input.Identifier || input.Local != nil || input.Live != nil || input.Indexed == nil || !validIndexed(*input.Indexed) {
			return resultProjection{}, bad
		}
	case catalog.SearchResultLive:
		id, err := catalog.ParseAIRIdentifier(input.Identifier)
		if err != nil || id.String() != input.Identifier || input.Local != nil || input.Indexed != nil || input.Live == nil || !identity.ValidTenantID(input.Live.SourceID) || input.Live.SourceRevision < 0 || input.Live.PresentationGeneration < 0 {
			return resultProjection{}, bad
		}
		var entry struct {
			URL       string `json:"url"`
			MediaType string `json:"type"`
		}
		if consume(input.Live.Entry, &entry) != nil {
			return resultProjection{}, bad
		}
		output.Live = &liveReference{SourceID: input.Live.SourceID, Source: input.Live.Source, SourceRevision: input.Live.SourceRevision, PresentationGeneration: input.Live.PresentationGeneration, URL: entry.URL, MediaType: entry.MediaType}
	default:
		return resultProjection{}, bad
	}
	return output, nil
}

func validLocal(local catalog.CatalogSearchLocal) bool {
	for _, id := range []string{local.Resource.ID, local.Resource.TenantID, local.Resource.NamespaceID, local.Version.ID, local.VersionRevision.ID, local.Listing.ID, local.Listing.NamespaceID, local.ListingRevision.ID, local.Release.ID} {
		if !identity.ValidTenantID(id) {
			return false
		}
	}
	return local.Resource.Kind.Valid() && local.Resource.CanonicalIdentifier != "" && local.Version.Version != "" && bounded(local.Version.Version, 128) && local.Release.VersionRevisionID == local.VersionRevision.ID && local.Release.ListingRevisionID == local.ListingRevision.ID && local.Resource.NamespaceID == local.Listing.NamespaceID
}

func validIndexed(e catalog.IndexedSearchEvidence) bool {
	if e.PresentationSourceID != "" && !identity.ValidTenantID(e.PresentationSourceID) || !strings.HasPrefix(e.ObservationGroupID, "observation-group-v1:") || !digest(strings.TrimPrefix(e.ObservationGroupID, "observation-group-v1:")) || e.PresentationSourceRevision < 0 || e.PresentationGeneration < 0 || !digest(e.NormalizedEntryDigest) || len(e.SourceIDs) > 16 || e.SourceCount < 1 || e.SourceCount < len(e.SourceIDs) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range e.SourceIDs {
		if !identity.ValidTenantID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func project(name string, raw json.RawMessage, selected selection) (json.RawMessage, error) {
	bad := safeerr.New("invalid response")
	switch name {
	case "catalog_control_capabilities":
		var fields map[string]json.RawMessage
		var required struct {
			ResourceKinds []catalog.Kind             `json:"resourceKinds"`
			Search        map[string]json.RawMessage `json:"search"`
			Limits        map[string]json.RawMessage `json:"limits"`
		}
		if consume(raw, &fields) != nil || consume(raw, &required) != nil || len(required.ResourceKinds) != 4 || required.Search == nil || required.Limits == nil {
			return nil, bad
		}
		seen := map[catalog.Kind]bool{}
		for _, kind := range required.ResourceKinds {
			if !kind.Valid() || seen[kind] {
				return nil, bad
			}
			seen[kind] = true
		}
		output := map[string]json.RawMessage{}
		for _, key := range []string{"resourceKinds", "profiles", "operations", "governance", "search", "optionalCapabilities", "limits", "profileBundle", "degradations"} {
			if value, ok := fields[key]; ok {
				if bytes.Equal(value, []byte("null")) {
					return nil, bad
				}
				output[key] = value
			}
		}
		if output["search"] == nil || output["limits"] == nil {
			return nil, bad
		}
		return json.Marshal(output)
	case "catalog_control_search":
		var page struct {
			Items          []json.RawMessage                 `json:"items"`
			NextCursor     json.RawMessage                   `json:"nextCursor"`
			DurableTotal   *int                              `json:"durableTotal"`
			LiveCount      *int                              `json:"liveCount"`
			Profile        string                            `json:"profile"`
			Generation     string                            `json:"generation"`
			Tier           string                            `json:"tier"`
			FallbackReason string                            `json:"fallbackReason,omitempty"`
			PartialIndex   bool                              `json:"partialIndex,omitempty"`
			Partial        *bool                             `json:"partial"`
			Diagnostics    []catalog.CatalogSearchDiagnostic `json:"diagnostics,omitempty"`
		}
		if consume(raw, &page) != nil || page.Items == nil || len(page.Items) > selected.limit || page.DurableTotal == nil || *page.DurableTotal < 0 || page.LiveCount == nil || *page.LiveCount < 0 || *page.LiveCount > 50 || page.Partial == nil || page.Tier != "core" && page.Tier != "hybrid" || page.Profile != "core-lexical-v1" && !digest(page.Profile) || !digest(page.Generation) || len(page.Diagnostics) > 33 {
			return nil, bad
		}
		var cursor string
		if len(page.NextCursor) > 0 && jsondoc.Decode(page.NextCursor, &cursor) != nil || len(cursor) > catalog.MaxCatalogCursorBytes || !bounded(cursor, catalog.MaxCatalogCursorBytes) {
			return nil, bad
		}
		items := make([]resultProjection, 0, len(page.Items))
		for _, item := range page.Items {
			result, err := projectResult(item)
			if err != nil {
				return nil, bad
			}
			allowed := false
			for _, origin := range selected.origins {
				allowed = allowed || origin == result.Type
			}
			if !allowed {
				return nil, bad
			}
			items = append(items, result)
		}
		// Replace only item bodies. Preserve all page-level API fields, including
		// partial diagnostics, exact continuation and future compatible additions.
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return nil, bad
		}
		encoded, err := json.Marshal(items)
		if err != nil {
			return nil, bad
		}
		fields["items"] = encoded
		return json.Marshal(fields)
	case "catalog_control_indexed_record":
		result, err := projectResult(raw)
		if err != nil || result.Type != catalog.SearchResultIndexed || result.Identifier != selected.identifier || result.VersionPresent != selected.present || result.Version != selected.version {
			return nil, bad
		}
		return json.Marshal(result)
	case "catalog_control_explore":
		return projectExplore(raw, selected.explore)
	case "catalog_control_access":
		return projectAccess(raw, selected.access)
	case "catalog_control_release":
		return projectRelease(raw, selected.release)
	default:
		return nil, bad
	}
}

// Retained digests remain references. Native content, extensions, findings and
// Skill/Plugin instructions are deliberately excluded from this model context.
func projectRelease(raw json.RawMessage, release string) (json.RawMessage, error) {
	var record catalog.CatalogRecord
	bad := safeerr.New("invalid release")
	if consume(raw, &record) != nil || record.Source != "local" || !strings.EqualFold(record.Release.ID, release) {
		return nil, bad
	}
	summary := record.Summary()
	if !validLocal(summary.SearchLocal()) || record.Version.ResourceID != record.Resource.ID || record.VersionRevision.ResourceVersionID != record.Version.ID {
		return nil, bad
	}
	type reference struct {
		ID         string                     `json:"id"`
		Role       catalog.RepresentationRole `json:"role"`
		MediaType  string                     `json:"mediaType"`
		ProfileID  string                     `json:"profileId"`
		Digest     string                     `json:"contentDigest"`
		Size       int64                      `json:"size"`
		ArtifactID string                     `json:"artifactId,omitempty"`
		URL        string                     `json:"url,omitempty"`
	}
	refs := make([]reference, 0, len(record.VersionRevision.Representations))
	if len(record.VersionRevision.Representations) == 0 || len(record.VersionRevision.Representations) > 16 {
		return nil, bad
	}
	for _, rep := range record.VersionRevision.Representations {
		if !identity.ValidTenantID(rep.ID) || !digest(rep.ContentDigest) || rep.Size < 0 || rep.MediaType == "" || rep.ProfileID == "" {
			return nil, bad
		}
		refs = append(refs, reference{rep.ID, rep.Role, rep.MediaType, rep.ProfileID, rep.ContentDigest, rep.Size, rep.ArtifactID, rep.URL})
	}
	return json.Marshal(struct {
		catalog.CatalogSummary
		Representations []reference `json:"representations"`
	}{summary, refs})
}
