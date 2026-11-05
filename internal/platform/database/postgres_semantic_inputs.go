// Semantic input selection uses current Catalog eligibility and bounded metadata
// pages. Source generation, membership accounting and projection share a snapshot.
package database

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// semanticControlInputsCTE shares indexed eligibility with reader Search without
// selecting API-only scoring fields. Local metadata is joined to current releases.
var semanticControlInputsCTE = ownerCollectionPredicates.Replace(indexedApprovedSourcesCTE + `, inputs AS (
 SELECT 'local:'||release.id::text AS item_key,revision.display_name,revision.description,
        revision.tags,revision.representative_queries
 FROM catalog_listings listing
 JOIN listing_releases release ON release.tenant_id=listing.tenant_id AND release.id=listing.current_release_id AND release.state IN ('published','deprecated')
 JOIN resources resource ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id AND resource.deleted_at IS NULL
 JOIN listing_revisions revision ON revision.tenant_id=release.tenant_id AND revision.id=release.listing_revision_id
 WHERE listing.tenant_id=$1
 UNION ALL
 SELECT 'observation:'||observation.id::text AS item_key,
        coalesce(nullif(observation.search_display_name,''),observation.identifier),
        observation.search_description,observation.search_tags,observation.search_representative_queries
 ` + indexedCurrentEligibleFromSQL + `)`)

// semanticPublicInputsCTE reads only the immutable active public document. The
// ordinal key is snapshot-local and never reused across publication identities.
const semanticPublicInputsCTE = `WITH inputs AS (
 SELECT 'entry:'||lpad(entry.ordinality::text,10,'0') AS item_key,
        coalesce(nullif(entry.value->>'displayName',''),entry.value->>'identifier','') AS display_name,
        coalesce(entry.value->>'description','') AS description,
        ARRAY(SELECT jsonb_array_elements_text(coalesce(entry.value->'tags','[]'::jsonb))) AS tags,
        ARRAY(SELECT jsonb_array_elements_text(coalesce(entry.value->'representativeQueries','[]'::jsonb))) AS representative_queries
 FROM publication_snapshots snapshot
 CROSS JOIN LATERAL jsonb_array_elements(snapshot.document#>'{ard,entries}') WITH ORDINALITY AS entry(value,ordinality)
 WHERE snapshot.tenant_id=$1 AND snapshot.id=$5 AND snapshot.state='active'
)`

// Inputs enumerates at most limit unprocessed members, ordered by an opaque key.
// The exact total includes already persisted and permanently rejected members.
func (s *PostgresSemanticStore) Inputs(ctx context.Context, g catalog.SemanticGeneration, after string, limit int) ([]catalog.SemanticInput, int64, error) {
	if err := validateSemanticGeneration(g, "input"); err != nil {
		return nil, 0, err
	}
	if limit < 1 || limit > 32 || len(after) > 1024 || !utf8.ValidString(after) || strings.ContainsRune(after, 0) {
		return nil, 0, catalog.E(catalog.CodeInvalidArgument, "invalid semantic input page")
	}
	type page struct {
		inputs []catalog.SemanticInput
		total  int64
	}
	value, err := withTenantTx(ctx, s.repository.pool, g.TenantID, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (page, error) {
		current, err := semanticSourceGeneration(ctx, tx, g.TenantID, g.Scope)
		if err != nil {
			return page{}, err
		}
		if current != g.SourceGeneration {
			return page{}, catalog.Wrap(catalog.CodeConflict, "semantic source generation changed", catalog.ErrSearchGenerationChanged)
		}
		statement := semanticControlInputsCTE
		args := []any{g.TenantID, g.ID, after, limit}
		if g.Scope != "control" {
			statement = semanticPublicInputsCTE
			args = append(args, strings.TrimPrefix(g.Scope, "public:"))
		}
		statement += `, selected AS (
 SELECT item_key,display_name,description,tags,representative_queries FROM inputs
 WHERE item_key>$3 COLLATE "C" AND NOT EXISTS (
   SELECT 1 FROM semantic_members member WHERE member.tenant_id=$1 AND member.generation_id=$2 AND member.item_key=inputs.item_key)
 ORDER BY item_key COLLATE "C" LIMIT $4
)
SELECT total.count,selected.item_key,selected.display_name,selected.description,selected.tags,selected.representative_queries
FROM (SELECT count(*) FROM inputs) total LEFT JOIN selected ON true ORDER BY selected.item_key COLLATE "C"`
		rows, err := tx.Query(ctx, statement, args...)
		if err != nil {
			return page{}, translateError("enumerate semantic metadata", err)
		}
		defer rows.Close()
		result := page{inputs: []catalog.SemanticInput{}}
		for rows.Next() {
			var key, name, description *string
			var tags, queries []string
			if err := rows.Scan(&result.total, &key, &name, &description, &tags, &queries); err != nil {
				return page{}, err
			}
			if key == nil {
				continue
			}
			if name == nil || description == nil {
				return page{}, catalog.E(catalog.CodeInternal, "incomplete semantic metadata")
			}
			result.inputs = append(result.inputs, catalog.SemanticInput{Key: *key, Document: catalog.SemanticDocument{DisplayName: *name, Description: *description, Tags: tags, RepresentativeQueries: queries}})
		}
		return result, rows.Err()
	})
	return value.inputs, value.total, err
}

// KnownInputs returns only existence, never vector contents, for one bounded
// same-tenant/profile/disclosure batch. A foreign key rechecks reuse at commit.
func (s *PostgresSemanticStore) KnownInputs(ctx context.Context, g catalog.SemanticGeneration, digests []string) (map[string]bool, error) {
	if err := validateSemanticGeneration(g, "reuse"); err != nil {
		return nil, err
	}
	if len(digests) > 32 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "semantic reuse batch exceeds bound")
	}
	for _, digest := range digests {
		if !validSemanticDigest(digest) {
			return nil, catalog.E(catalog.CodeInvalidArgument, "invalid semantic input digest")
		}
	}
	if len(digests) == 0 {
		return map[string]bool{}, nil
	}
	disclosure := "control"
	if g.Scope != "control" {
		disclosure = "public"
	}
	return withTenantTx(ctx, s.repository.pool, g.TenantID, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (map[string]bool, error) {
		rows, err := tx.Query(ctx, `SELECT input_digest FROM semantic_vectors WHERE tenant_id=$1 AND profile=$2 AND disclosure=$3 AND dimensions=$4 AND input_digest=ANY($5::text[]) LIMIT 32`, g.TenantID, g.Profile, disclosure, g.Dimensions, digests)
		if err != nil {
			return nil, translateError("find reusable semantic inputs", err)
		}
		defer rows.Close()
		result := make(map[string]bool, len(digests))
		for rows.Next() {
			var digest string
			if err := rows.Scan(&digest); err != nil {
				return nil, err
			}
			result[digest] = true
		}
		return result, rows.Err()
	})
}
