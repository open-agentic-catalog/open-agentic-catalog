// Native protocol facts are selected under content authority before any search
// ranking, count, facet, page or summary hydration.
package database

import (
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// agentProtocolQuery adds one bound literal to the existing local selection.
// Ordinary queries retain their existing statements and prepared parameter shape.
func agentProtocolQuery(statement string, parameters []any, query catalog.SearchQuery) (string, []any) {
	if query.AgentProtocol == "" {
		return statement, parameters
	}
	predicate := `CASE WHEN ` + collectionPermissionSQL("resource.read_content", "listing.namespace_id") + ` THEN resource.kind='agent' AND EXISTS (
 SELECT 1 FROM representations agent_rep
 LEFT JOIN document_blobs agent_doc ON agent_doc.tenant_id=agent_rep.tenant_id AND agent_doc.id=agent_rep.document_blob_id
 WHERE agent_rep.tenant_id=release.tenant_id AND agent_rep.version_revision_id=release.version_revision_id
 AND agent_rep.role='primary' AND agent_rep.validation_state='valid' AND agent_rep.profile_id='a2a-agent-card-1.0.1-3303592'
 AND (agent_rep.agent_protocol_bindings @> ARRAY[$` + strconv.Itoa(len(parameters)+1) + `::text] OR agent_doc.agent_protocol_bindings @> ARRAY[$` + strconv.Itoa(len(parameters)+1) + `::text])) ELSE false END AND `
	statement = strings.Replace(statement, "WHERE listing.tenant_id=$1 AND ", "WHERE listing.tenant_id=$1 AND "+predicate, 1)
	return statement, append(parameters, query.AgentProtocol)
}
