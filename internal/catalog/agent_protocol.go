// Agent protocol selection reads only validated primary retained declarations.
package catalog

import (
	"context"
	"encoding/json"
	"mime"
	"slices"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// declaredAgentProtocols projects exact interface bindings without interpreting
// protocol versions or contacting an Agent. Callers first admit content access.
func declaredAgentProtocols(mediaType string, raw []byte) []string {
	media, _, err := mime.ParseMediaType(mediaType)
	if err != nil || media != "application/a2a-agent-card+json" {
		return nil
	}
	type agentInterface struct {
		Binding      string `json:"protocolBinding"`
		ProtoBinding string `json:"protocol_binding"`
	}
	var card struct {
		Interfaces      []agentInterface `json:"supportedInterfaces"`
		ProtoInterfaces []agentInterface `json:"supported_interfaces"`
	}
	if json.Unmarshal(raw, &card) != nil {
		return nil
	}
	if card.Interfaces == nil {
		card.Interfaces = card.ProtoInterfaces
	}
	var bindings []string
	seen := map[string]bool{}
	for _, item := range card.Interfaces {
		if item.Binding == "" {
			item.Binding = item.ProtoBinding
		}
		if item.Binding != "" && !seen[item.Binding] {
			seen[item.Binding] = true
			bindings = append(bindings, item.Binding)
		}
	}
	return bindings
}

// agentProtocolRecordsLocked mirrors SQL preselection for development memory
// stores. Private declaration bytes never enter compact results or embeddings.
func (m *MemoryRepository) agentProtocolRecordsLocked(ctx context.Context, tenant string, records []CatalogSummary, binding string) ([]CatalogSummary, error) {
	if binding == "" {
		return records, nil
	}
	selected := make([]CatalogSummary, 0, len(records))
	for _, record := range records {
		if record.Resource.Kind != KindAgent {
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
		for _, representation := range revision.Representations {
			if representation.Role != RepresentationPrimary || representation.ValidationState != "valid" || representation.ProfileID != "a2a-agent-card-1.0.1-3303592" {
				continue
			}
			raw := representation.Data
			if representation.ArtifactID != "" {
				raw = m.documents[tenant+"/"+representation.ArtifactID].Content
			}
			if slices.Contains(declaredAgentProtocols(representation.MediaType, raw), binding) {
				selected = append(selected, record)
				break
			}
		}
	}
	return selected, nil
}
