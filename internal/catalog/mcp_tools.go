// Private MCP tool declarations remain immutable revision content. Discovery
// reads declared metadata, never runtime capabilities or executable schemas.
package catalog

import (
	"context"
	"encoding/json"
	"mime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const (
	MCPToolsMediaType     = "application/vnd.open-agentic-catalog.mcp-tools+json"
	MCPToolsProfile       = "open-agentic-catalog-mcp-tools-1"
	MCPToolsSearchProfile = "core-mcp-tools-v1"
	MaxMCPTools           = 64
	MaxMCPToolsBytes      = 64 << 10
	MaxMCPToolMatches     = 3
	MaxMCPToolSnippet     = 256
)

// MCPTool is an author declaration, without schemas, URLs or execution inputs.
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// MCPToolDeclaration distinguishes an explicitly empty list from no attachment.
type MCPToolDeclaration struct {
	Tools []MCPTool `json:"tools"`
}

// MCPToolMatch is bounded display data from the exact released declaration.
type MCPToolMatch struct {
	RepresentationID string           `json:"representationId"`
	ContentDigest    string           `json:"contentDigest"`
	MatchedToolCount int              `json:"matchedToolCount"`
	Tools            []MCPToolSnippet `json:"tools"`
	Truncated        bool             `json:"truncated"`
	Score            int              `json:"-"`
}

// MCPToolSnippet preserves exact names and a bounded plain-text description.
type MCPToolSnippet struct {
	Name                 string `json:"name"`
	Description          string `json:"description,omitempty"`
	DescriptionTruncated bool   `json:"descriptionTruncated"`
}

// IsMCPToolsMediaType recognizes the service-owned media type, including MIME
// case/parameters, without confusing it with pinned upstream MCP profiles.
func IsMCPToolsMediaType(value string) bool {
	media, _, err := mime.ParseMediaType(value)
	return err == nil && media == MCPToolsMediaType
}

// ParseMCPTools enforces closed JSON, finite content and case-sensitive unique
// nonblank names. Invalid content is rejected as a whole, never truncated.
func ParseMCPTools(raw []byte) (MCPToolDeclaration, error) {
	var value MCPToolDeclaration
	if len(raw) > MaxMCPToolsBytes || jsondoc.Decode(raw, &value) != nil || value.Tools == nil || len(value.Tools) > MaxMCPTools {
		return MCPToolDeclaration{}, E(CodeInvalidArgument, "tool declaration requires closed JSON with tools and at most 64 entries in 65536 bytes")
	}
	seen := make(map[string]bool, len(value.Tools))
	for _, tool := range value.Tools {
		if !validTextLength(tool.Name, 128) || strings.TrimSpace(tool.Name) == "" || !validTextLength(tool.Description, 1024) || seen[tool.Name] {
			return MCPToolDeclaration{}, E(CodeInvalidArgument, "tool names must be unique nonblank strings up to 128 characters; descriptions allow up to 1024 characters; NUL is forbidden")
		}
		seen[tool.Name] = true
	}
	return value, nil
}

// MatchMCPTools uses the ordinary lexical scorer independently for each tool.
// Terms may span that tool's name/description, not different tools. Parent
// scoring takes the best match rather than rewarding inventory size.
func MatchMCPTools(tools []MCPTool, query string, representationID, digest string) *MCPToolMatch {
	type scored struct {
		tool  MCPTool
		score int
	}
	matched := make([]scored, 0, len(tools))
	for _, tool := range tools {
		score := CoreLexicalScore(SearchDocument{DisplayName: tool.Name, Description: tool.Description}, query)
		if score > 0 {
			matched = append(matched, scored{tool, score})
		}
	}
	if len(matched) == 0 {
		return nil
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].score != matched[j].score {
			return matched[i].score > matched[j].score
		}
		return matched[i].tool.Name < matched[j].tool.Name
	})
	result := &MCPToolMatch{RepresentationID: representationID, ContentDigest: digest, MatchedToolCount: len(matched), Score: matched[0].score, Truncated: len(matched) > MaxMCPToolMatches}
	for _, item := range matched[:min(len(matched), MaxMCPToolMatches)] {
		snippet := MCPToolSnippet{Name: item.tool.Name, Description: item.tool.Description}
		if utf8.RuneCountInString(snippet.Description) > MaxMCPToolSnippet {
			snippet.Description = string([]rune(snippet.Description)[:MaxMCPToolSnippet])
			snippet.DescriptionTruncated = true
		}
		result.Tools = append(result.Tools, snippet)
	}
	return result
}

// mcpToolRecordsLocked admits current content authority before inspecting any
// private declaration. Listing-only matches remain available without that grant.
func (m *MemoryRepository) mcpToolRecordsLocked(ctx context.Context, tenant string, records []CatalogSummary, query SearchQuery) ([]CatalogSummary, error) {
	if !query.IncludeMCPTools {
		return records, nil
	}
	selected := make([]CatalogSummary, 0, len(records))
	for _, record := range records {
		if record.Resource.Kind != KindMCPServer {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.read_content", record.Listing.NamespaceID)
		if err != nil {
			return nil, err
		}
		if allowed {
			revision, exists := m.versionRevisions[record.VersionRevision.ID]
			if exists && revision.TenantID == tenant {
				for _, rep := range revision.Representations {
					if rep.Role != RepresentationMetadata || rep.ProfileID != MCPToolsProfile || rep.ValidationState != "valid" || !IsMCPToolsMediaType(rep.MediaType) {
						continue
					}
					raw := rep.Data
					if rep.ArtifactID != "" {
						raw = m.documents[tenant+"/"+rep.ArtifactID].Content
					}
					declaration, err := ParseMCPTools(raw)
					if err == nil {
						record.ToolMatch = MatchMCPTools(declaration.Tools, query.Text, rep.ID, rep.ContentDigest)
					}
					break
				}
			}
		}
		selected = append(selected, record)
	}
	return selected, nil
}

// validateMCPToolInputs admits exactly one local private metadata attachment.
// It runs before any external retrieval or revision write.
func validateMCPToolInputs(kind Kind, inputs []RepresentationInput) error {
	count := 0
	for _, input := range inputs {
		if !IsMCPToolsMediaType(input.MediaType) {
			continue
		}
		count++
		if count > 1 || kind != KindMCPServer || input.Role != "" && input.Role != RepresentationMetadata || input.URL != "" || len(input.PackageData) > 0 || input.ProfileID != "" && input.ProfileID != MCPToolsProfile {
			return E(CodeInvalidArgument, "one private local MCP tool metadata declaration is allowed per MCP revision")
		}
		raw := input.Data
		if input.Content != "" {
			raw = json.RawMessage(input.Content)
		}
		if _, err := ParseMCPTools(raw); err != nil {
			return err
		}
		// Inline JSON is retained in canonical form. Escaping can expand an
		// otherwise valid raw declaration beyond its independent retained-byte
		// limit; reject that before an earlier primary can trigger retrieval.
		if len(input.Data) > 0 {
			canonical, err := canonicalJSON(input.Data)
			if err != nil || len(canonical) > MaxMCPToolsBytes {
				return E(CodeInvalidArgument, "canonical tool declaration exceeds 65536 bytes")
			}
		}
	}
	return nil
}

// coreProfile binds the explicit private lexical population into cursor/ranking identity.
func coreProfile(query SearchQuery) string {
	if query.IncludeMCPTools {
		return MCPToolsSearchProfile
	}
	return CoreSearchProfile
}
