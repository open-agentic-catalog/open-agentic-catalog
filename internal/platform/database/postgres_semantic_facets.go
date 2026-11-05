// Hybrid Explore retains the local-only facet contract and complete SQL totals.
package database

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Explore matches lexical OR semantic local records under current generation
// fencing; indexed and live observations never enter local facet counts.
func (s *PostgresSemanticStore) Explore(ctx context.Context, request catalog.SemanticSearchRequest, facets []catalog.FacetRequest) (catalog.ExploreResponse, error) {
	g := request.Generation
	if err := validateSemanticGeneration(g, "explore"); err != nil {
		return catalog.ExploreResponse{}, err
	}
	if g.Scope != "control" || len(facets) == 0 || len(facets) > 10 || math.IsNaN(request.Cutoff) || math.IsInf(request.Cutoff, 0) || request.Cutoff < -1 || request.Cutoff > 1 {
		return catalog.ExploreResponse{}, catalog.E(catalog.CodeInvalidArgument, "invalid hybrid Explore request")
	}
	if err := validateSemanticMembers([]catalog.SemanticMember{{Key: "query", InputDigest: g.Profile, Vector: request.Vector}}, g.Dimensions); err != nil {
		return catalog.ExploreResponse{}, err
	}
	return withTenantCollectionRead(ctx, s.repository.pool, g.TenantID, func(tx pgx.Tx) (catalog.ExploreResponse, error) {
		current, err := readCatalogSearchGeneration(ctx, tx, g.TenantID)
		if err != nil {
			return catalog.ExploreResponse{}, err
		}
		if current != g.SourceGeneration {
			return catalog.ExploreResponse{}, catalog.Wrap(catalog.CodeConflict, "hybrid index changed; repeat the query", catalog.ErrSearchGenerationChanged)
		}
		var ready bool
		if err := tx.QueryRow(ctx, `SELECT state='ready' AND profile=$3 AND scope='control' AND source_generation=$4 AND dimensions=$5 FROM semantic_generations WHERE tenant_id=$1 AND id=$2`, g.TenantID, g.ID, g.Profile, g.SourceGeneration, g.Dimensions).Scan(&ready); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return catalog.ExploreResponse{}, catalog.Wrap(catalog.CodeConflict, "semantic generation changed; repeat the query", catalog.ErrSearchGenerationChanged)
			}
			return catalog.ExploreResponse{}, translateError("read hybrid facet generation", err)
		}
		if !ready {
			return catalog.ExploreResponse{}, catalog.Wrap(catalog.CodeConflict, "semantic index is not ready", catalog.ErrSearchGenerationChanged)
		}
		base := strings.Replace(catalogSearchBaseCTE, "WITH records AS (", "WITH lexical_records AS (", 1) + `, records AS (
SELECT record.kind,record.namespace_id,record.tags,record.categories,record.license,record.publisher_id,record.visibility,record.trust_state,record.canonical_identifier,record.document_vector,
 CASE WHEN vector.input_digest IS NOT NULL THEN 1-` + pgx.Identifier{s.vectorSchema, "cosine_distance"}.Sanitize() + `(vector.embedding,$13::real[]::` + pgx.Identifier{s.vectorSchema, "vector"}.Sanitize() + `) END AS similarity
FROM lexical_records record LEFT JOIN semantic_members member ON member.tenant_id=$1 AND member.generation_id=$11 AND member.profile=$12 AND member.item_key=record.result_id
LEFT JOIN semantic_vectors vector ON vector.tenant_id=member.tenant_id AND vector.profile=member.profile AND vector.disclosure=member.disclosure AND vector.input_digest=member.input_digest AND vector.dimensions=member.dimensions)`
		filter := strings.Replace(catalogSearchFilterSQL, "AND ($2='' OR", "AND (similarity>=$14 OR $2='' OR", 1)
		statement, limits, err := catalogFacetStatementWithFilter(request.Query, facets, base, filter, 15)
		if err != nil {
			return catalog.ExploreResponse{}, err
		}
		// Keep the same parameter-specific planning boundary as core facets
		// and hybrid Search; no session or server-wide planner setting changes.
		parameters := append([]any{pgx.QueryExecModeExec}, catalogSearchParameters(g.TenantID, request.Query)...)
		parameters = append(parameters, g.ID, g.Profile, request.Vector, request.Cutoff)
		parameters = append(parameters, limits...)
		rows, err := tx.Query(ctx, collectionStatement(ctx, statement), parameters...)
		if err != nil {
			return catalog.ExploreResponse{}, translateError("query hybrid facets", err)
		}
		defer rows.Close()
		response, err := readCatalogFacets(rows, facets)
		if err != nil {
			return catalog.ExploreResponse{}, err
		}
		if response.Matched > 100000 {
			return catalog.ExploreResponse{}, catalog.E(catalog.CodeWorkLimitExceeded, "hybrid matching limit exceeded; narrow the search")
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM semantic_members WHERE tenant_id=$1 AND generation_id=$2 AND failure<>'')`, g.TenantID, g.ID).Scan(&response.PartialIndex); err != nil {
			return catalog.ExploreResponse{}, translateError("read hybrid facet failures", err)
		}
		return response, nil
	})
}
