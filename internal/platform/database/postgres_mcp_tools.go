// Declared tool matching stays in the current authorized SQL population. Only
// selected pages hydrate bounded private match details.
package database

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// mcpToolScoreSQL mirrors CoreLexicalScore for one name/description pair.
const mcpToolScoreSQL = `CASE WHEN (catalog_core_lexical_vector_v1(tool->>'name') || catalog_core_lexical_vector_v1(coalesce(tool->>'description',''))) @@ catalog_core_lexical_query_v1($2)
 THEN 40 + CASE WHEN catalog_core_lexical_vector_v1(tool->>'name') @@ catalog_core_lexical_query_v1($2) THEN 20 ELSE 0 END
 + CASE WHEN catalog_core_lexical_vector_v1(coalesce(tool->>'description','')) @@ catalog_core_lexical_query_v1($2) THEN 10 ELSE 0 END ELSE 0 END`

// mcpToolsBaseCTE adds only a scalar score to the narrow matching projection.
// CASE keeps all private inventory inspection behind current content authority.
func mcpToolsBaseCTE() string {
	score := `CASE WHEN ` + collectionPermissionSQL("resource.read_content", "listing.namespace_id") + ` THEN coalesce((
 SELECT max(` + mcpToolScoreSQL + `) FROM representations tool_rep
 LEFT JOIN document_blobs tool_doc ON tool_doc.tenant_id=tool_rep.tenant_id AND tool_doc.id=tool_rep.document_blob_id
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN tool_rep.document_blob_id IS NULL THEN tool_rep.mcp_tools ELSE coalesce(tool_doc.mcp_tools,'[]'::jsonb) END) tool
 WHERE tool_rep.tenant_id=release.tenant_id AND tool_rep.version_revision_id=release.version_revision_id
 AND tool_rep.role='metadata' AND tool_rep.profile_id='open-agentic-catalog-mcp-tools-1' AND tool_rep.validation_state='valid'
 AND (catalog_mcp_tools_vector_v1(tool_rep.mcp_tools) @@ catalog_core_lexical_query_v1($2) OR catalog_mcp_tools_vector_v1(coalesce(tool_doc.mcp_tools,'[]'::jsonb)) @@ catalog_core_lexical_query_v1($2))
 ),0) ELSE 0 END AS declared_tool_score`
	return strings.Replace(catalogSearchBaseCTE, "revision.search_queries_vector AS queries_vector", "revision.search_queries_vector AS queries_vector, "+score, 1)
}

func mcpToolsFilterSQL() string {
	return strings.Replace(strings.Replace(catalogSearchFilterSQL, "WHERE (coalesce", "WHERE kind='mcp_server' AND (coalesce", 1), "OR document_vector @@ (SELECT catalog_core_lexical_query_v1($2)))", "OR document_vector @@ (SELECT catalog_core_lexical_query_v1($2)) OR declared_tool_score>0)", 1)
}

// mcpToolsSearchStatements preserves ordinary exact identifier/Listing scoring;
// private tools contribute the best single-tool score, never an inventory sum.
func mcpToolsSearchStatements() (string, string) {
	records := strings.Replace(catalogSearchRecordsCTE, catalogSearchBaseCTE, mcpToolsBaseCTE(), 1)
	records = strings.Replace(records, "         CASE\n", "         greatest(declared_tool_score, CASE\n", 1)
	records = strings.Replace(records, "END::integer AS score", "END)::integer AS score", 1)
	records = strings.Replace(records, catalogSearchFilterSQL, mcpToolsFilterSQL(), 1)
	page := strings.NewReplacer("matched AS (", "matched AS MATERIALIZED (", "SELECT release_id,kind,namespace_id,sort_name,tags,categories,publisher_id,visibility,trust_state,result_id,", "SELECT release_id,sort_name,result_id,").Replace(records)
	page += catalogSearchPageTailSQL
	return records, page
}

// readMCPToolMatches hydrates at most one bounded declaration per selected page
// member, in the same policy/generation snapshot as totals and selection.
func readMCPToolMatches(ctx context.Context, tx pgx.Tx, tenant string, ids []string, text string) (map[string]*catalog.MCPToolMatch, error) {
	result := make(map[string]*catalog.MCPToolMatch, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	if len(ids) > catalog.MaxCatalogPageSize+2 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "tool detail page exceeds its bound")
	}
	rows, err := tx.Query(ctx, collectionStatement(ctx, `SELECT release.id::text,rep.id::text,rep.content_digest,
 CASE WHEN rep.document_blob_id IS NULL THEN rep.mcp_tools ELSE doc.mcp_tools END
 FROM catalog_listings listing JOIN listing_releases release ON release.tenant_id=listing.tenant_id AND release.id=listing.current_release_id AND release.state IN ('published','deprecated')
 JOIN representations rep ON rep.tenant_id=release.tenant_id AND rep.version_revision_id=release.version_revision_id
 LEFT JOIN document_blobs doc ON doc.tenant_id=rep.tenant_id AND doc.id=rep.document_blob_id
 WHERE listing.tenant_id=$1 AND release.id=ANY($2::uuid[]) AND `+collectionPermissionSQL("resource.read", "listing.namespace_id")+` AND `+collectionPermissionSQL("resource.read_content", "listing.namespace_id")+`
 AND rep.role='metadata' AND rep.profile_id='open-agentic-catalog-mcp-tools-1' AND rep.validation_state='valid'`), tenant, ids)
	if err != nil {
		return nil, translateError("read bounded tool matches", err)
	}
	defer rows.Close()
	for rows.Next() {
		var releaseID, representationID, digest string
		var raw []byte
		if err := rows.Scan(&releaseID, &representationID, &digest, &raw); err != nil {
			return nil, translateError("decode tool matches", err)
		}
		var tools []catalog.MCPTool
		if err := json.Unmarshal(raw, &tools); err != nil || len(tools) > catalog.MaxMCPTools {
			return nil, catalog.E(catalog.CodeInternal, "invalid derived tool declaration")
		}
		result[releaseID] = catalog.MatchMCPTools(tools, text, representationID, digest)
	}
	return result, translateError("iterate tool matches", rows.Err())
}
