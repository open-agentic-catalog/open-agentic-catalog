// Native capability selection uses bounded declarations from retained cards.
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"mime"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const (
	MaxNativeFilterValues     = 16
	MaxNativeFilterRunes      = 256
	MaxNativeFilterTerms      = 64
	MaxNativeFilterBytes      = 8 << 10
	MaxNativeCardBytes        = 16 << 20
	MaxNativeCardMembers      = 256
	MaxNativeDeclarationRunes = 512
	MaxNativeDeclarationBytes = 32 << 10
)

// NativeCapabilitiesFilter contains exact alternatives within each field.
// Different fields constrain one released primary card; arrays are not tuples.
type NativeCapabilitiesFilter struct {
	AgentSkillIDs           []string `json:"agentSkillIds,omitempty"`
	AgentSkillTags          []string `json:"agentSkillTags,omitempty"`
	AgentDefaultInputModes  []string `json:"agentDefaultInputModes,omitempty"`
	AgentDefaultOutputModes []string `json:"agentDefaultOutputModes,omitempty"`
	AgentStreaming          *bool    `json:"agentStreaming,omitempty"`
	MCPTransports           []string `json:"mcpTransports,omitempty"`
	MCPProtocolVersions     []string `json:"mcpProtocolVersions,omitempty"`
}

func (f *NativeCapabilitiesFilter) arrays() [][]string {
	return [][]string{f.AgentSkillIDs, f.AgentSkillTags, f.AgentDefaultInputModes, f.AgentDefaultOutputModes, f.MCPTransports, f.MCPProtocolVersions}
}

func (f *NativeCapabilitiesFilter) agent() bool {
	return len(f.AgentSkillIDs)+len(f.AgentSkillTags)+len(f.AgentDefaultInputModes)+len(f.AgentDefaultOutputModes) > 0 || f.AgentStreaming != nil
}

func (f *NativeCapabilitiesFilter) mcp() bool {
	return len(f.MCPTransports)+len(f.MCPProtocolVersions) > 0
}

// ValidateNativeCapabilities is shared by service and explicit client inputs.
// It neither reads declarations nor grants content permission.
func ValidateNativeCapabilities(query SearchQuery) error {
	f := query.NativeCapabilities
	if f == nil {
		return nil
	}
	if !f.agent() && !f.mcp() || f.agent() && f.mcp() {
		return E(CodeInvalidArgument, "nativeCapabilities requires declarations from one card kind")
	}
	if len(query.Origins) != 1 || query.Origins[0] != SearchResultLocal {
		return E(CodeInvalidArgument, "nativeCapabilities requires explicit local-only origins")
	}
	want := KindAgent
	if f.mcp() {
		want = KindMCPServer
	}
	if len(query.Kinds) > 1 || len(query.Kinds) == 1 && query.Kinds[0] != want || f.agent() && query.IncludeMCPTools || f.mcp() && query.AgentProtocol != "" {
		return E(CodeInvalidArgument, "nativeCapabilities conflicts with the selected kind or content filter")
	}
	count := 0
	for _, values := range f.arrays() {
		if values != nil && len(values) == 0 || len(values) > MaxNativeFilterValues {
			return E(CodeInvalidArgument, "native capability arrays require one to sixteen values")
		}
		count += len(values)
		seen := map[string]bool{}
		for _, value := range values {
			if !validTextLength(value, MaxNativeFilterRunes) || strings.TrimSpace(value) == "" || seen[value] {
				return E(CodeInvalidArgument, "native capability values must be distinct nonblank literals of at most 256 characters")
			}
			seen[value] = true
		}
	}
	encoded, err := json.Marshal(f)
	if err != nil || count > MaxNativeFilterTerms || len(encoded) > MaxNativeFilterBytes {
		return E(CodeInvalidArgument, "native capability filters exceed their aggregate term or encoded byte limit")
	}
	return nil
}

// normalized copies selectors before sorting, without changing literal values.
func (f *NativeCapabilitiesFilter) normalized() *NativeCapabilitiesFilter {
	cloned := f.Clone()
	if cloned == nil {
		return nil
	}
	for _, values := range [][]string{cloned.AgentSkillIDs, cloned.AgentSkillTags, cloned.AgentDefaultInputModes, cloned.AgentDefaultOutputModes, cloned.MCPTransports, cloned.MCPProtocolVersions} {
		sort.Strings(values)
	}
	return cloned
}

// Clone gives saved records and callers independent arrays and boolean presence.
func (f *NativeCapabilitiesFilter) Clone() *NativeCapabilitiesFilter {
	if f == nil {
		return nil
	}
	cloned := *f
	arrays := []*[]string{&cloned.AgentSkillIDs, &cloned.AgentSkillTags, &cloned.AgentDefaultInputModes, &cloned.AgentDefaultOutputModes, &cloned.MCPTransports, &cloned.MCPProtocolVersions}
	for _, values := range arrays {
		if *values != nil {
			*values = slices.Clone(*values)
		}
	}
	if f.AgentStreaming != nil {
		value := *f.AgentStreaming
		cloned.AgentStreaming = &value
	}
	return &cloned
}

// NativeCapabilityProjection is derived, private and replaceable. Unavailable
// evidence never supplies partial matches; it does not change retained bytes.
type NativeCapabilityProjection struct {
	NativeCapabilitiesFilter
	Unavailable bool `json:"unavailable,omitempty"`
}

// ProjectNativeCapabilities inspects only supported literal card members. Raw
// values avoid coercing unselected exact numbers or escaped characters.
func ProjectNativeCapabilities(media string, raw []byte) NativeCapabilityProjection {
	media, _, err := mime.ParseMediaType(media)
	if err != nil || media != "application/a2a-agent-card+json" && media != "application/mcp-server-card+json" {
		return NativeCapabilityProjection{}
	}
	unavailable := NativeCapabilityProjection{Unavailable: true}
	if len(raw) == 0 || len(raw) > MaxNativeCardBytes {
		return unavailable
	}
	var card map[string]json.RawMessage
	if jsonv2.Unmarshal(raw, &card) != nil || card == nil {
		return unavailable
	}
	var out NativeCapabilityProjection
	total := 0
	add := func(target *[]string, value json.RawMessage) bool {
		var text string
		if json.Unmarshal(value, &text) != nil || bytes.Equal(value, []byte("null")) || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || utf8.RuneCountInString(text) > MaxNativeDeclarationRunes {
			return false
		}
		total += len(text)
		if total > MaxNativeDeclarationBytes || len(*target) >= MaxNativeCardMembers {
			return false
		}
		*target = append(*target, text)
		return true
	}
	array := func(value json.RawMessage, target *[]string) bool {
		return nativeMembers(value, func(value json.RawMessage) bool { return add(target, value) })
	}
	if media == "application/a2a-agent-card+json" {
		if !nativeMembers(card["skills"], func(raw json.RawMessage) bool {
			var skill map[string]json.RawMessage
			if jsonv2.Unmarshal(raw, &skill) != nil || skill == nil {
				return false
			}
			if id := skill["id"]; len(id) > 0 && !bytes.Equal(id, []byte("null")) && !add(&out.AgentSkillIDs, id) {
				return false
			}
			return array(skill["tags"], &out.AgentSkillTags)
		}) {
			return unavailable
		}
		input := card["defaultInputModes"]
		if input == nil {
			input = card["default_input_modes"]
		}
		output := card["defaultOutputModes"]
		if output == nil {
			output = card["default_output_modes"]
		}
		if !array(input, &out.AgentDefaultInputModes) || !array(output, &out.AgentDefaultOutputModes) {
			return unavailable
		}
		if raw := card["capabilities"]; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			var capabilities map[string]json.RawMessage
			if jsonv2.Unmarshal(raw, &capabilities) != nil || capabilities == nil {
				return unavailable
			}
			if value := capabilities["streaming"]; len(value) > 0 && !bytes.Equal(value, []byte("null")) {
				var streaming bool
				if json.Unmarshal(value, &streaming) != nil {
					return unavailable
				}
				out.AgentStreaming = &streaming
			}
		}
	} else if !nativeMembers(card["remotes"], func(raw json.RawMessage) bool {
		var remote map[string]json.RawMessage
		if jsonv2.Unmarshal(raw, &remote) != nil || remote == nil {
			return false
		}
		if value := remote["type"]; len(value) > 0 && !bytes.Equal(value, []byte("null")) && !add(&out.MCPTransports, value) {
			return false
		}
		return array(remote["supportedProtocolVersions"], &out.MCPProtocolVersions)
	}) {
		return unavailable
	}
	out.NativeCapabilitiesFilter = *out.NativeCapabilitiesFilter.normalized()
	arrays := []*[]string{&out.AgentSkillIDs, &out.AgentSkillTags, &out.AgentDefaultInputModes, &out.AgentDefaultOutputModes, &out.MCPTransports, &out.MCPProtocolVersions}
	for _, values := range arrays {
		*values = slices.Compact(*values)
	}
	return out
}

// nativeMembers stops before decoding an unbounded selected array. Missing and
// null declarations contribute no values, while wrong shapes are unavailable.
func nativeMembers(raw json.RawMessage, visit func(json.RawMessage) bool) bool {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return false
	}
	count := 0
	for decoder.More() {
		count++
		if count > MaxNativeCardMembers {
			return false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || !visit(value) {
			return false
		}
	}
	_, err = decoder.Token()
	return err == nil
}

func (f *NativeCapabilitiesFilter) matches(facts NativeCapabilityProjection) bool {
	if facts.Unavailable {
		return false
	}
	for i, selected := range f.arrays() {
		if len(selected) == 0 {
			continue
		}
		found := false
		for _, value := range selected {
			if slices.Contains(facts.arrays()[i], value) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return f.AgentStreaming == nil || facts.AgentStreaming != nil && *f.AgentStreaming == *facts.AgentStreaming
}

// nativeCapabilityRecordsLocked mirrors SQL preselection under current content
// authority. One exact primary card must satisfy all native selectors.
func (m *MemoryRepository) nativeCapabilityRecordsLocked(ctx context.Context, tenant string, records []CatalogSummary, query SearchQuery) ([]CatalogSummary, error) {
	if query.NativeCapabilities == nil {
		return records, nil
	}
	selected := make([]CatalogSummary, 0, len(records))
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if query.NativeCapabilities.agent() && record.Resource.Kind != KindAgent || query.NativeCapabilities.mcp() && record.Resource.Kind != KindMCPServer {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.read_content", record.Listing.NamespaceID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		revision, exists := m.versionRevisions[record.VersionRevision.ID]
		if !exists || revision.TenantID != tenant {
			continue
		}
		for _, rep := range revision.Representations {
			if rep.Role != RepresentationPrimary || rep.ValidationState != "valid" {
				continue
			}
			if record.Resource.Kind == KindAgent && rep.ProfileID != "a2a-agent-card-1.0.1-3303592" || record.Resource.Kind == KindMCPServer && rep.ProfileID != "mcp-server-card-v1-experimental-526201b" || record.Resource.Kind != KindAgent && record.Resource.Kind != KindMCPServer {
				continue
			}
			raw := rep.Data
			if rep.ArtifactID != "" {
				raw = m.documents[tenant+"/"+rep.ArtifactID].Content
			}
			if query.AgentProtocol != "" && !slices.Contains(declaredAgentProtocols(rep.MediaType, raw), query.AgentProtocol) {
				continue
			}
			facts := ProjectNativeCapabilities(rep.MediaType, raw)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if query.NativeCapabilities.matches(facts) {
				selected = append(selected, record)
				break
			}
		}
	}
	return selected, nil
}
