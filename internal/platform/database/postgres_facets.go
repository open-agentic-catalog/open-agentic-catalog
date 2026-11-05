// Explore facets share one local matching projection for exact totals and
// requested buckets, with code-owned SQL and bound inputs.

package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// catalogFacetStatement shares matching across the exact total and requested
// buckets. Only requested value arrays enter the materialized set: no scores,
// documents, vectors or unrequested trust-evidence lookups. SQL expressions
// come from a closed field mapping; request values and limits remain bound.
func catalogFacetStatement(query catalog.SearchQuery, facets []catalog.FacetRequest) (string, []any, error) {
	return catalogFacetStatementWithFilter(query, facets, catalogSearchBaseCTE, catalogSearchFilterSQL, 11)
}

// catalogFacetStatementWithFilter reuses exact bucket accounting for lexical
// and hybrid matching. All SQL fragments are code-owned, not caller input.
func catalogFacetStatementWithFilter(query catalog.SearchQuery, facets []catalog.FacetRequest, base, filter string, firstParameter int) (string, []any, error) {
	columns := make([]string, 0, len(facets))
	parts := make([]string, 0, 2*len(facets))
	selections := make([]string, 0, len(facets))
	parameters := make([]any, 0, 2*len(facets))
	for index, facet := range facets {
		values, ok := catalogFacetValuesSQL(facet.Field)
		if !ok {
			return "", nil, catalog.E(catalog.CodeInvalidArgument, "facet field is not supported")
		}
		limit := facet.Limit
		if limit <= 0 {
			limit = 20
		}
		minimumParameter, limitParameter := firstParameter+len(parameters), firstParameter+1+len(parameters)
		parameters = append(parameters, max(1, facet.MinCount), limit)
		columns = append(columns, fmt.Sprintf("%s AS facet_%d", values, index))
		parts = append(parts, fmt.Sprintf(`,
facet_%[1]d_buckets AS (
  SELECT value,count(*)::integer AS count
    FROM matched CROSS JOIN LATERAL unnest(facet_%[1]d) value
   WHERE value<>'' GROUP BY value HAVING count(*) >= $%[2]d
), facet_%[1]d_ranked AS (
  SELECT value,count,row_number() OVER (ORDER BY count DESC,value COLLATE "C") AS position
    FROM facet_%[1]d_buckets
)`, index, minimumParameter))
		selections = append(selections, fmt.Sprintf(`SELECT %[1]d AS facet,value,count,
       (SELECT coalesce(sum(omitted.count),0)::integer FROM facet_%[1]d_ranked omitted WHERE omitted.position>$%[2]d) AS other_count
  FROM facet_%[1]d_ranked WHERE position<=$%[2]d`, index, limitParameter))
	}
	if len(columns) == 0 {
		columns = append(columns, "1 AS unused")
		selections = append(selections, "SELECT NULL::integer AS facet,NULL::text AS value,NULL::integer AS count,NULL::integer AS other_count WHERE false")
	}
	if query.Text == "" {
		filter = strings.ReplaceAll(filter, "$2=''", "true")
	} else {
		filter = strings.ReplaceAll(filter, "$2=''", "false")
	}
	sql := base + ", matched AS MATERIALIZED (\n  SELECT " + strings.Join(columns, ",") + filter + ")" + strings.Join(parts, "") + `,
selected_buckets AS (` + strings.Join(selections, "\nUNION ALL\n") + `)
SELECT totals.total,bucket.facet,bucket.value,bucket.count,bucket.other_count
  FROM (SELECT count(*) AS total FROM matched) totals
  LEFT JOIN selected_buckets bucket ON true
 ORDER BY bucket.facet,bucket.count DESC,bucket.value COLLATE "C"`
	return sql, parameters, nil
}

// ExploreCatalog computes exact totals and bounded buckets in one snapshot and
// one selection statement; matching Catalog rows never enter Go memory.
func (p *PostgresRepository) ExploreCatalog(ctx context.Context, tenantID string, query catalog.SearchQuery, facets []catalog.FacetRequest) (catalog.ExploreResponse, error) {
	return withTenantCollectionRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.ExploreResponse, error) {
		return exploreCatalogTx(ctx, tx, tenantID, query, facets)
	})
}

// exploreCatalogTx executes the bounded facet projection in the caller's snapshot
// and rejects incomplete totals or bucket rows.
func exploreCatalogTx(ctx context.Context, tx pgx.Tx, tenantID string, query catalog.SearchQuery, facets []catalog.FacetRequest) (catalog.ExploreResponse, error) {
	sql, limits, err := catalogFacetStatement(query, facets)
	if err != nil {
		return catalog.ExploreResponse{}, err
	}
	parameters := append([]any{pgx.QueryExecModeExec}, catalogSearchParameters(tenantID, query)...)
	parameters = append(parameters, limits...)
	rows, err := tx.Query(ctx, collectionStatement(ctx, sql), parameters...)
	if err != nil {
		return catalog.ExploreResponse{}, translateError("query catalog facets", err)
	}
	defer rows.Close()
	return readCatalogFacets(rows, facets)
}

// readCatalogFacets validates one bounded facet projection, including the empty
// total row. It never loads matching Catalog records to aggregate in Go.
func readCatalogFacets(rows pgx.Rows, facets []catalog.FacetRequest) (catalog.ExploreResponse, error) {
	response := catalog.ExploreResponse{Facets: make(map[string]catalog.FacetResult, len(facets))}
	for _, facet := range facets {
		response.Facets[facet.Field] = catalog.FacetResult{Buckets: []catalog.FacetBucket{}}
	}
	hasTotal := false
	for rows.Next() {
		var index, count, other *int
		var value *string
		if err := rows.Scan(&response.Matched, &index, &value, &count, &other); err != nil {
			return catalog.ExploreResponse{}, translateError("scan catalog facet", err)
		}
		hasTotal = true
		if index == nil && value == nil && count == nil && other == nil {
			continue // Exact total with no buckets, including a min-count exclusion.
		}
		if index == nil || *index < 0 || *index >= len(facets) || value == nil || count == nil || other == nil {
			return catalog.ExploreResponse{}, catalog.E(catalog.CodeInternal, "incomplete catalog facet selection")
		}
		field := facets[*index].Field
		result := response.Facets[field]
		result.Buckets = append(result.Buckets, catalog.FacetBucket{Value: *value, Count: *count})
		result.OtherCount = *other
		response.Facets[field] = result
	}
	if err := rows.Err(); err != nil {
		return catalog.ExploreResponse{}, translateError("iterate catalog facets", err)
	}
	if !hasTotal {
		return catalog.ExploreResponse{}, catalog.E(catalog.CodeInternal, "catalog facet total is missing")
	}
	return response, nil
}

// catalogFacetValuesSQL maps supported facet names to code-owned array
// expressions; unknown names never become SQL identifiers.
func catalogFacetValuesSQL(field string) (string, bool) {
	switch field {
	case "kind":
		return "ARRAY[kind::text]", true
	case "visibility":
		return "ARRAY[visibility::text]", true
	case "publisher":
		return "ARRAY[publisher_id::text]", true
	case "namespace":
		return "ARRAY[namespace_id::text]", true
	case "trustState":
		return "ARRAY[trust_state]", true
	case "source":
		return "ARRAY['local'::text]", true
	case "tag":
		return "tags", true
	case "category":
		return "categories", true
	default:
		return "", false
	}
}
