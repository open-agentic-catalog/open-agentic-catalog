// Private native selectors constrain released-card selection before hydration.
package database

import (
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// localCardQuery preserves the original Agent protocol path for ordinary calls.
// Combined selectors inspect the same primary representation, not two cards.
func localCardQuery(statement string, parameters []any, query catalog.SearchQuery) (string, []any) {
	if query.NativeCapabilities == nil {
		return agentProtocolQuery(statement, parameters, query)
	}
	f := query.NativeCapabilities
	facts := `(CASE WHEN native_rep.document_blob_id IS NOT NULL THEN native_doc.native_capabilities ELSE native_rep.native_capabilities END)`
	conditions := []string{facts + `->>'unavailable' IS DISTINCT FROM 'true'`}
	fields := []struct {
		name   string
		values []string
	}{
		{"agentSkillIds", f.AgentSkillIDs}, {"agentSkillTags", f.AgentSkillTags},
		{"agentDefaultInputModes", f.AgentDefaultInputModes}, {"agentDefaultOutputModes", f.AgentDefaultOutputModes},
		{"mcpTransports", f.MCPTransports}, {"mcpProtocolVersions", f.MCPProtocolVersions},
	}
	bind := func(value any) string {
		parameters = append(parameters, value)
		return "$" + strconv.Itoa(len(parameters))
	}
	for _, field := range fields {
		if len(field.values) > 0 {
			conditions = append(conditions, `(`+facts+`->'`+field.name+`') ?| `+bind(field.values)+`::text[]`)
		}
	}
	if f.AgentStreaming != nil {
		conditions = append(conditions, facts+` @> jsonb_build_object('agentStreaming',`+bind(*f.AgentStreaming)+`::boolean)`)
	}
	kind, profile := catalog.KindMCPServer, "mcp-server-card-v1-experimental-526201b"
	if len(f.AgentSkillIDs)+len(f.AgentSkillTags)+len(f.AgentDefaultInputModes)+len(f.AgentDefaultOutputModes) > 0 || f.AgentStreaming != nil {
		kind, profile = catalog.KindAgent, "a2a-agent-card-1.0.1-3303592"
	}
	if query.AgentProtocol != "" {
		conditions = append(conditions, `(native_rep.agent_protocol_bindings @> ARRAY[`+bind(query.AgentProtocol)+`::text] OR native_doc.agent_protocol_bindings @> ARRAY[$`+strconv.Itoa(len(parameters))+`::text])`)
	}
	predicate := `CASE WHEN ` + collectionPermissionSQL("resource.read_content", "listing.namespace_id") + ` THEN resource.kind='` + string(kind) + `' AND EXISTS (
 SELECT 1 FROM representations native_rep
 LEFT JOIN document_blobs native_doc ON native_doc.tenant_id=native_rep.tenant_id AND native_doc.id=native_rep.document_blob_id
 WHERE native_rep.tenant_id=release.tenant_id AND native_rep.version_revision_id=release.version_revision_id
 AND native_rep.role='primary' AND native_rep.validation_state='valid' AND native_rep.profile_id='` + profile + `'
 AND ` + strings.Join(conditions, " AND ") + `) ELSE false END AND `
	return strings.Replace(statement, "WHERE listing.tenant_id=$1 AND ", "WHERE listing.tenant_id=$1 AND "+predicate, 1), parameters
}
