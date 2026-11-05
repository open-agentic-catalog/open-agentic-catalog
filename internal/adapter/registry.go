// Package adapter validates native Agent, MCP Server, Skill, and Plugin
// representations. [Registry] selects pinned profiles and returns findings,
// normalized metadata and retainable package evidence. Archive inspection
// enforces bounded paths and expansion without extracting or executing files;
// structural acceptance remains separate from trust and local approval.
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

const (
	ProfileA2AAgentCard  = "a2a-agent-card-1.0.1-3303592"
	ProfilePrivateAgent  = "open-agentic-catalog-agent-descriptor-1"
	ProfileMCPServerCard = "mcp-server-card-v1-experimental-526201b"
	ProfileMCPRegistry   = "mcp-registry-server-1.8.1-f52dc85"
	ProfilePrivateMCP    = "open-agentic-catalog-mcp-registration-1"
	ProfileAgentSkills   = "agent-skills-69ef37e"
	ProfileUnknown       = "unknown-observation-1"
)

const (
	MediaA2AAgentCard     = "application/a2a-agent-card+json"
	MediaPrivateAgent     = "application/vnd.open-agentic-catalog.agent+json"
	MediaMCPServerCard    = "application/mcp-server-card+json"
	MediaMCPRegistry      = "application/vnd.mcp.server+json"
	MediaPrivateMCP       = "application/vnd.open-agentic-catalog.mcp-registration+json"
	MediaSkillMarkdown    = "application/agent-skills+md"
	MediaSkillMarkdownARD = `text/markdown; profile="urn:air:agent-skills"`
	MediaSkillJSON        = "application/agent-skills+json"
	MediaSkillZIP         = catalog.SkillPackageMediaZIP
	MediaSkillGZIP        = catalog.SkillPackageMediaGZIP
)

// Registry dispatches a representation to the validator for its declared
// Catalog kind and media type without inferring trust or approval.
type Registry struct{ limits catalog.DiagnosticLimits }

// NewRegistry returns pinned validators with the default diagnostic budget.
func NewRegistry() Registry { return Registry{limits: catalog.DefaultDiagnosticLimits()} }

// NewRegistryWithLimits binds one immutable diagnostic policy to the validators.
func NewRegistryWithLimits(limits catalog.DiagnosticLimits) (Registry, error) {
	if err := limits.Validate(); err != nil {
		return Registry{}, err
	}
	return Registry{limits: limits}, nil
}

// Validate dispatches by resource kind and declared media type, returning
// findings instead of executing or installing a representation.
func (r Registry) Validate(ctx context.Context, kind catalog.Kind, input catalog.RepresentationInput) (result catalog.ValidationResult) {
	limits := r.limits
	if limits == (catalog.DiagnosticLimits{}) {
		limits = catalog.DefaultDiagnosticLimits()
	}
	defer func() {
		report := catalog.NewFindingCollector(limits)
		report.AddAll("", result.Findings)
		result.Findings = report.Findings()
	}()
	mediaType := normalizedMediaType(input.MediaType)
	switch kind {
	case catalog.KindPlugin:
		return InspectPlugin(ctx, mediaType, input.PackageData, limits)
	case catalog.KindAgent:
		return validateAgent(mediaType, input, limits)
	case catalog.KindMCPServer:
		return validateMCP(mediaType, input, limits)
	case catalog.KindSkill:
		return validateSkill(ctx, mediaType, input)
	default:
		return invalid(ProfileUnknown, "UNSUPPORTED_RESOURCE_KIND", "The resource kind is not supported.")
	}
}

// IsSkillMarkdownMediaType recognizes the ARD Agent Skills media type and the
// AI Catalog compatibility alias retained for existing producers.
func IsSkillMarkdownMediaType(value string) bool {
	mediaType := normalizedMediaType(value)
	return mediaType == MediaSkillMarkdown || mediaType == MediaSkillMarkdownARD
}

// DiscoveryMediaType returns the media type emitted by the ARD publication
// profile. AI Catalog's +md alias remains accepted on ingestion, but ARD
// publishes Markdown through its profile-qualified text/markdown type.
func DiscoveryMediaType(value string) string {
	if IsSkillMarkdownMediaType(value) {
		return MediaSkillMarkdownARD
	}
	mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return strings.TrimSpace(value)
	}
	formatted := mime.FormatMediaType(mediaType, parameters)
	if formatted == "" {
		return strings.TrimSpace(value)
	}
	return formatted
}

// normalizedMediaType canonicalizes the base type while preserving the
// recognized profile-qualified Skill Markdown type.
func normalizedMediaType(value string) string {
	mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(value))
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType == "text/markdown" && strings.EqualFold(parameters["profile"], "urn:air:agent-skills") {
		return MediaSkillMarkdownARD
	}
	return mediaType
}

// validateAgent applies the declared A2A or private Agent profile and extracts
// bounded descriptive facts.
func validateAgent(mediaType string, input catalog.RepresentationInput, limits catalog.DiagnosticLimits) catalog.ValidationResult {
	switch mediaType {
	case MediaA2AAgentCard:
		object, findings := decodeObject(input.Data)
		if object == nil {
			return catalog.ValidationResult{ProfileID: ProfileA2AAgentCard, Findings: findings}
		}
		card, protocolFindings := validateA2AAgentCard(input.Data, limits)
		findings = append(findings, protocolFindings...)
		if card == nil {
			return catalog.ValidationResult{ProfileID: ProfileA2AAgentCard, Findings: findings}
		}
		return catalog.ValidationResult{ProfileID: ProfileA2AAgentCard, NormalizedFacts: map[string]any{"name": card.GetName(), "description": card.GetDescription(), "interfaces": len(card.GetSupportedInterfaces()), "version": card.GetVersion()}, Findings: findings}
	case MediaPrivateAgent:
		object, findings := decodeObject(input.Data)
		if object == nil {
			return catalog.ValidationResult{ProfileID: ProfilePrivateAgent, Findings: findings}
		}
		if stringField(object, "name") == "" || stringField(object, "framework") == "" {
			findings = append(findings, errorFinding("PRIVATE_AGENT_FIELDS_REQUIRED", "", "A private Agent descriptor needs name and framework."))
		}
		return catalog.ValidationResult{ProfileID: ProfilePrivateAgent, NormalizedFacts: map[string]any{"name": stringField(object, "name"), "framework": stringField(object, "framework")}, Findings: findings}
	default:
		return invalid(ProfileUnknown, "AGENT_MEDIA_TYPE_UNSUPPORTED", "This media type is not supported as a governed Agent representation.")
	}
}

// validateMCP distinguishes Server Card, Registry and private registration
// profiles and validates declared remote references.
func validateMCP(mediaType string, input catalog.RepresentationInput, limits catalog.DiagnosticLimits) catalog.ValidationResult {
	switch mediaType {
	case MediaMCPServerCard:
		object, initialFindings := decodeObject(input.Data)
		report := catalog.NewFindingCollector(limits)
		report.AddAll("", initialFindings)
		if object == nil {
			return catalog.ValidationResult{ProfileID: ProfileMCPServerCard, Findings: report.Findings()}
		}
		if err := standards.ValidateMCPServerCard(input.Data); err != nil {
			report.Add(errorFinding("MCP_SERVER_CARD_SCHEMA_INVALID", "", err.Error()))
		}
		if stringField(object, "version") == "" {
			report.Add(errorFinding("MCP_SERVER_CARD_VERSION_REQUIRED", "/version", "MCP Server Card version must be a non-empty string."))
		}
		remotes, _ := object["remotes"].([]any)
		if len(remotes) > 0 {
			for index, raw := range remotes {
				if report.Exhausted() {
					break
				}
				remote, valid := raw.(map[string]any)
				if !valid || !validMCPRemoteReference(stringField(remote, "url")) {
					report.Add(errorFinding("MCP_CARD_REMOTE_INVALID", fmt.Sprintf("/remotes/%d/url", index), "Each remote endpoint must be a declared URL template or a safe absolute HTTPS URL."))
				}
			}
		}
		report.Add(catalog.Finding{Code: "MCP_SERVER_CARD_EXPERIMENTAL", Severity: catalog.SeverityWarning, Message: "This representation uses the pinned experimental MCP Server Card profile."})
		return catalog.ValidationResult{ProfileID: ProfileMCPServerCard, NormalizedFacts: map[string]any{"name": stringField(object, "name"), "description": stringField(object, "description"), "version": stringField(object, "version"), "remoteCount": len(remotes), "experimental": true}, Findings: report.Findings()}
	case MediaMCPRegistry:
		object, initialFindings := decodeObject(input.Data)
		report := catalog.NewFindingCollector(limits)
		report.AddAll("", initialFindings)
		if object == nil {
			return catalog.ValidationResult{ProfileID: ProfileMCPRegistry, Findings: report.Findings()}
		}
		if err := standards.ValidateMCPRegistryServer(input.Data); err != nil {
			report.Add(errorFinding("MCP_REGISTRY_SCHEMA_INVALID", "", err.Error()))
		}
		name := stringField(object, "name")
		version := stringField(object, "version")
		if version == "" {
			report.Add(errorFinding("MCP_REGISTRY_VERSION_REQUIRED", "/version", "MCP Registry version must be a non-empty string."))
		}
		packages, _ := object["packages"].([]any)
		remotes, _ := object["remotes"].([]any)
		if len(packages) == 0 && len(remotes) == 0 {
			report.Add(catalog.Finding{Code: "MCP_DISTRIBUTION_NOT_DECLARED", Severity: catalog.SeverityWarning, Message: "MCP Registry metadata has no package or remote distribution."})
		}
		for index, raw := range remotes {
			if report.Exhausted() {
				break
			}
			remote, valid := raw.(map[string]any)
			if !valid || !catalog.ValidExternalHTTPSURL(stringField(remote, "url")) {
				report.Add(errorFinding("MCP_REGISTRY_REMOTE_INVALID", fmt.Sprintf("/remotes/%d/url", index), "Each MCP Registry remote must be a safe absolute HTTPS URL."))
			}
		}
		report.Add(catalog.Finding{Code: "MCP_REGISTRY_NOT_SERVER_CARD", Severity: catalog.SeverityInfo, Message: "MCP Registry metadata is a distribution representation and is not published as an MCP Server Card."})
		return catalog.ValidationResult{ProfileID: ProfileMCPRegistry, NormalizedFacts: map[string]any{"name": name, "version": version, "packages": len(packages), "remotes": len(remotes)}, Findings: report.Findings()}
	case MediaPrivateMCP:
		object, initialFindings := decodeObject(input.Data)
		report := catalog.NewFindingCollector(limits)
		report.AddAll("", initialFindings)
		if object == nil {
			return catalog.ValidationResult{ProfileID: ProfilePrivateMCP, Findings: report.Findings()}
		}
		if stringField(object, "name") == "" || stringField(object, "transport") == "" {
			report.Add(errorFinding("PRIVATE_MCP_FIELDS_REQUIRED", "", "Private MCP registration metadata needs name and transport."))
		}
		return catalog.ValidationResult{ProfileID: ProfilePrivateMCP, NormalizedFacts: map[string]any{"name": stringField(object, "name"), "transport": stringField(object, "transport")}, Findings: report.Findings()}
	default:
		return invalid(ProfileUnknown, "MCP_MEDIA_TYPE_UNSUPPORTED", "This media type is not supported as a governed MCP Server representation.")
	}
}

// validMCPRemoteReference accepts supported URL templates or safe absolute
// HTTPS endpoint declarations without fetching them.
func validMCPRemoteReference(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "{") || catalog.ValidExternalHTTPSURL(value)
}

// validateSkill dispatches Markdown, JSON and archive Skill representations to
// their corresponding bounded validators.
func validateSkill(ctx context.Context, mediaType string, input catalog.RepresentationInput) catalog.ValidationResult {
	switch mediaType {
	case MediaSkillMarkdown, MediaSkillMarkdownARD:
		if input.Content == "" {
			return invalid(ProfileAgentSkills, "SKILL_CONTENT_REQUIRED", "An Agent Skill Markdown representation needs SKILL.md content.")
		}
		frontmatter, findings := parseSkillFrontmatter(input.Content)
		return catalog.ValidationResult{ProfileID: ProfileAgentSkills, NormalizedFacts: map[string]any{"name": frontmatter["name"], "description": frontmatter["description"]}, Findings: findings}
	case MediaSkillZIP, MediaSkillGZIP:
		inspection, facts, findings := inspectSkillPackage(ctx, mediaType, input.PackageData, defaultSkillPackageLimits)
		result := catalog.ValidationResult{ProfileID: ProfileAgentSkills, NormalizedFacts: facts, Findings: findings, Package: inspection}
		if input.Handoff != nil {
			safeHandoff, safeErr := catalog.NormalizeSkillHandoff(*input.Handoff, "")
			if safeErr == nil {
				result.Handoff = &safeHandoff
			}
			expectedRoot := ""
			if inspection.StructureState == catalog.PackageAssessmentPassed {
				expectedRoot = inspection.RootDirectory
			}
			handoff, err := catalog.NormalizeSkillHandoff(*input.Handoff, expectedRoot)
			if err != nil {
				result.Findings = append(result.Findings, errorFinding("SKILL_HANDOFF_INVALID", "/handoff", err.Error()))
			} else {
				result.Handoff = &handoff
			}
		}
		return result
	case MediaSkillJSON:
		object, findings := decodeObject(input.Data)
		if object == nil {
			return catalog.ValidationResult{ProfileID: ProfileAgentSkills, Findings: findings}
		}
		if stringField(object, "name") == "" || stringField(object, "description") == "" {
			findings = append(findings, errorFinding("SKILL_METADATA_REQUIRED", "", "Skill metadata needs name and description."))
		}
		findings = append(findings, catalog.Finding{Code: "SKILL_JSON_NOT_INSTALLABLE", Severity: catalog.SeverityInfo, Message: "JSON metadata is discoverable but is not treated as an installable package."})
		return catalog.ValidationResult{ProfileID: ProfileAgentSkills, NormalizedFacts: map[string]any{"name": stringField(object, "name"), "description": stringField(object, "description")}, Findings: findings}
	default:
		return invalid(ProfileUnknown, "SKILL_MEDIA_TYPE_UNSUPPORTED", "This media type is not supported as a governed Skill representation.")
	}
}

// decodeObject rejects duplicate keys, nonobject values and trailing input
// while preserving JSON number spelling.
func decodeObject(raw json.RawMessage) (map[string]any, []catalog.Finding) {
	if len(raw) == 0 {
		return nil, []catalog.Finding{errorFinding("JSON_OBJECT_REQUIRED", "", "This representation requires inline JSON object data.")}
	}
	if err := jsondoc.Validate(raw); err != nil {
		if errors.Is(err, jsondoc.ErrTrailingValue) {
			return nil, []catalog.Finding{errorFinding("JSON_TRAILING_DATA", "", "The representation has trailing JSON data.")}
		}
		return nil, []catalog.Finding{errorFinding("JSON_OBJECT_INVALID", "", "The representation is not an unambiguous JSON object.")}
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, []catalog.Finding{errorFinding("JSON_OBJECT_INVALID", "", "The representation is not a valid JSON object.")}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, []catalog.Finding{errorFinding("JSON_TRAILING_DATA", "", "The representation has trailing JSON data.")}
	}
	return object, nil
}

type skillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility *string           `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
	AllowedTools  string            `yaml:"allowed-tools,omitempty"`
}

// parseSkillFrontmatter validates bounded UTF-8 Markdown with closed, exactly
// typed YAML frontmatter and returns normalized display facts.
func parseSkillFrontmatter(content string) (map[string]string, []catalog.Finding) {
	findings := make([]catalog.Finding, 0)
	result := make(map[string]string)
	if !utf8.ValidString(content) || strings.ContainsRune(content, '\x00') {
		return result, []catalog.Finding{errorFinding("SKILL_MARKDOWN_INVALID", "", "SKILL.md must be valid UTF-8 text without NUL characters.")}
	}
	if len(content) > 500_000 {
		return result, []catalog.Finding{errorFinding("SKILL_MARKDOWN_TOO_LARGE", "", "SKILL.md exceeds the supported size.")}
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return result, []catalog.Finding{errorFinding("SKILL_FRONTMATTER_REQUIRED", "", "SKILL.md must begin with YAML frontmatter.")}
	}
	frontmatterEnd := -1
	remainder := content[4:]
	for offset := 0; offset <= len(remainder); {
		lineEnd := strings.IndexByte(remainder[offset:], '\n')
		if lineEnd < 0 {
			if remainder[offset:] == "---" {
				frontmatterEnd = 4 + offset
			}
			break
		}
		lineEnd += offset
		if remainder[offset:lineEnd] == "---" {
			frontmatterEnd = 4 + offset
			break
		}
		offset = lineEnd + 1
	}
	if frontmatterEnd < 0 {
		return result, []catalog.Finding{errorFinding("SKILL_FRONTMATTER_UNCLOSED", "", "SKILL.md frontmatter is not closed.")}
	}
	frontmatterText := content[4:frontmatterEnd]
	if !skillFrontmatterHasExactTypes(frontmatterText) {
		return result, []catalog.Finding{errorFinding("SKILL_FRONTMATTER_INVALID", "/frontmatter", "SKILL.md frontmatter fields must use the exact types defined by Agent Skills.")}
	}
	decoder := yaml.NewDecoder(strings.NewReader(frontmatterText))
	decoder.KnownFields(true)
	var frontmatter skillFrontmatter
	if err := decoder.Decode(&frontmatter); err != nil {
		return result, []catalog.Finding{errorFinding("SKILL_FRONTMATTER_INVALID", "/frontmatter", "SKILL.md frontmatter must be valid YAML using only fields defined by Agent Skills.")}
	}
	// Validate decoded content, not a trimmed substitute. In particular, YAML
	// block-scalar newlines count toward character limits. Display facts retain
	// their existing description normalization after checking the original value.
	result["name"] = frontmatter.Name
	result["description"] = strings.TrimSpace(frontmatter.Description)
	result["license"] = frontmatter.License
	if frontmatter.Compatibility != nil {
		result["compatibility"] = *frontmatter.Compatibility
	}
	result["allowed-tools"] = frontmatter.AllowedTools
	if !catalog.ValidSkillName(result["name"]) {
		findings = append(findings, errorFinding("SKILL_NAME_INVALID", "/frontmatter/name", "Skill name must use lower-case words separated by hyphens."))
	}
	if result["description"] == "" || utf8.RuneCountInString(frontmatter.Description) > 1024 {
		findings = append(findings, errorFinding("SKILL_DESCRIPTION_INVALID", "/frontmatter/description", "Skill description is required and must be at most 1024 characters."))
	}
	// A missing optional field is valid; an explicitly empty string is not.
	if frontmatter.Compatibility != nil && (strings.TrimSpace(*frontmatter.Compatibility) == "" || utf8.RuneCountInString(*frontmatter.Compatibility) > 500) {
		findings = append(findings, errorFinding("SKILL_COMPATIBILITY_INVALID", "/frontmatter/compatibility", "Skill compatibility must be 1 to 500 characters when present."))
	}
	return result, findings
}

// skillFrontmatterHasExactTypes rejects coercible scalar types and aliases
// before decoding the pinned Skill fields.
func skillFrontmatterHasExactTypes(frontmatterText string) bool {
	var document yaml.Node
	if err := yaml.NewDecoder(strings.NewReader(frontmatterText)).Decode(&document); err != nil || len(document.Content) != 1 {
		return false
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode || containsYAMLAlias(root) {
		return false
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		key := root.Content[index]
		value := root.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return false
		}
		switch key.Value {
		case "name", "description", "license", "compatibility", "allowed-tools":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return false
			}
		case "metadata":
			if value.Kind != yaml.MappingNode {
				return false
			}
			for metadataIndex := 0; metadataIndex+1 < len(value.Content); metadataIndex += 2 {
				metadataKey := value.Content[metadataIndex]
				metadataValue := value.Content[metadataIndex+1]
				if metadataKey.Kind != yaml.ScalarNode || metadataKey.Tag != "!!str" || metadataValue.Kind != yaml.ScalarNode || metadataValue.Tag != "!!str" {
					return false
				}
			}
		}
	}
	return true
}

// containsYAMLAlias reports any alias or anchor in the YAML tree.
func containsYAMLAlias(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return true
	}
	for _, child := range node.Content {
		if containsYAMLAlias(child) {
			return true
		}
	}
	return false
}

// stringField returns a trimmed JSON string member, or empty for missing and
// nonstring values.
func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return strings.TrimSpace(value)
}

// invalid returns a profile-bound validation result containing one root error
// finding.
func invalid(profile, code, message string) catalog.ValidationResult {
	return catalog.ValidationResult{ProfileID: profile, Findings: []catalog.Finding{errorFinding(code, "", message)}}
}

// errorFinding attaches a stable error code and source path to a safe
// validation message.
func errorFinding(code, path, message string) catalog.Finding {
	return catalog.Finding{Code: code, Severity: catalog.SeverityError, Path: path, Message: message}
}
