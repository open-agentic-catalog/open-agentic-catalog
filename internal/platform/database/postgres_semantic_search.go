// Hybrid retrieval shares one repeatable-read snapshot for generation fencing,
// exact matching, rank/page selection and bounded result hydration.
package database

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// Search performs control-plane hybrid retrieval without provider calls. A
// changed metadata population is a restart, never permission to use stale hits.
func (s *PostgresSemanticStore) Search(ctx context.Context, request catalog.SemanticSearchRequest) (catalog.SemanticSearchPage, error) {
	g := request.Generation
	if err := validateSemanticGeneration(g, "search"); err != nil {
		return catalog.SemanticSearchPage{}, err
	}
	if g.Scope != "control" || request.Query.Limit < 1 || request.Query.Limit > 100 || request.Query.Text == "" || math.IsNaN(request.Cutoff) || math.IsInf(request.Cutoff, 0) || request.Cutoff < -1 || request.Cutoff > 1 {
		return catalog.SemanticSearchPage{}, catalog.E(catalog.CodeInvalidArgument, "invalid hybrid search bounds")
	}
	if err := validateSemanticMembers([]catalog.SemanticMember{{Key: "query", InputDigest: g.Profile, Vector: request.Vector}}, g.Dimensions); err != nil {
		return catalog.SemanticSearchPage{}, err
	}
	return withTenantCollectionRead(ctx, s.repository.pool, g.TenantID, func(tx pgx.Tx) (catalog.SemanticSearchPage, error) {
		current, err := readCatalogSearchGeneration(ctx, tx, g.TenantID)
		if err != nil {
			return catalog.SemanticSearchPage{}, err
		}
		if current != g.SourceGeneration {
			return catalog.SemanticSearchPage{}, catalog.Wrap(catalog.CodeConflict, "hybrid search generation changed; restart the search", catalog.ErrSearchGenerationChanged)
		}
		var ready bool
		if err := tx.QueryRow(ctx, `SELECT state='ready' AND profile=$3 AND scope=$4 AND source_generation=$5 AND dimensions=$6 FROM semantic_generations WHERE tenant_id=$1 AND id=$2`, g.TenantID, g.ID, g.Profile, g.Scope, g.SourceGeneration, g.Dimensions).Scan(&ready); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return catalog.SemanticSearchPage{}, catalog.Wrap(catalog.CodeConflict, "semantic generation changed; restart the search", catalog.ErrSearchGenerationChanged)
			}
			return catalog.SemanticSearchPage{}, translateError("check hybrid readiness", err)
		}
		if !ready {
			return catalog.SemanticSearchPage{}, catalog.Wrap(catalog.CodeConflict, "semantic index is not ready", catalog.ErrSearchGenerationChanged)
		}
		kinds := make([]string, 0, len(request.Query.Kinds))
		for _, kind := range request.Query.Kinds {
			kinds = append(kinds, string(kind))
		}
		position := catalog.SemanticSearchPosition{}
		if request.After != nil {
			position = *request.After
		}
		statement := strings.NewReplacer("@cosine@", pgx.Identifier{s.vectorSchema, "cosine_distance"}.Sanitize(), "@vector@", pgx.Identifier{s.vectorSchema, "vector"}.Sanitize()).Replace(semanticSearchInputsSQL + semanticSearchRanksSQL)
		if !request.Query.IncludesOrigin(catalog.SearchResultLocal) {
			statement = strings.Replace(statement, "AND NOT (observation.version_present AND EXISTS (", "AND NOT (false AND observation.version_present AND EXISTS (", 1)
		}
		// As with core search, filter selectivity needs request-specific plans.
		// The 1k pilot exposed a much slower generic cached hybrid plan. Exec
		// retains bound extended-protocol parameters, not SQL interpolation.
		parameters := []any{g.TenantID, request.Query.Text, catalog.CatalogSearchExactIdentifier(request.Query.Text), kinds, append([]string{}, request.Query.Tags...), request.Query.Publisher, request.Query.Visibility, g.ID, g.Profile, request.Vector, request.Cutoff, request.LocalOnly, request.After != nil, position.Rank, position.SortName, position.ID, request.Query.Limit + 1, request.Query.NamespaceID, request.Query.IncludesOrigin(catalog.SearchResultLocal), request.Query.IncludesOrigin(catalog.SearchResultIndexed), request.Query.License, append([]string{}, request.Query.Categories...)}
		if request.IdentityProbes != nil {
			probes, err := identityProbeParameters(request.IdentityProbes)
			if err != nil {
				return catalog.SemanticSearchPage{}, err
			}
			// Reuse the exact hybrid matching population, including semantic-only
			// matches. Select at most 50 identities without hydrating documents.
			statement = strings.Split(statement, "SELECT totals.total,")[0] + `SELECT DISTINCT matching.identifier,coalesce(version.version,matching.version),matching.release_id<>'' OR matching.version_present
 FROM matching
 LEFT JOIN listing_releases release ON release.tenant_id=$1 AND release.id::text=matching.release_id
 LEFT JOIN resource_version_revisions revision ON revision.tenant_id=$1 AND revision.id=release.version_revision_id
 LEFT JOIN resource_versions version ON version.tenant_id=$1 AND version.id=revision.resource_version_id
 WHERE (matching.identifier,coalesce(version.version,matching.version),matching.release_id<>'' OR matching.version_present)
 IN (SELECT identifier,version,present FROM unnest($23::text[],$24::text[],$25::boolean[]) probe(identifier,version,present)) LIMIT 51`
			matches, err := readLiveMatches(ctx, tx, collectionStatement(ctx, statement), append([]any{pgx.QueryExecModeExec}, append(parameters, probes...)...))
			return catalog.SemanticSearchPage{Matches: matches}, err
		}
		rows, err := tx.Query(ctx, collectionStatement(ctx, statement), append([]any{pgx.QueryExecModeExec}, parameters...)...)
		if err != nil {
			return catalog.SemanticSearchPage{}, translateError("select hybrid search page", err)
		}
		page := catalog.SemanticSearchPage{Items: []catalog.SemanticSearchItem{}}
		ids := []string{}
		for rows.Next() {
			var id, release, identifier, version, kind, digest, name, description, publisher, sortName *string
			var versionPresent *bool
			var rank *int64
			var score *int
			var tags []string
			var evidence catalog.IndexedSearchEvidence
			if err := rows.Scan(&page.Total, &id, &release, &identifier, &version, &versionPresent, &kind, &digest, &name, &description, &tags, &publisher, &rank, &score, &sortName, &evidence.Conflict, &evidence.SameSourceConflict, &evidence.SourceCount, &evidence.SourceIDs, &evidence.PresentationSourceID, &evidence.PresentationGeneration, &evidence.PresentationSourceRevision); err != nil {
				rows.Close()
				return catalog.SemanticSearchPage{}, translateError("read hybrid search page", err)
			}
			if page.Total > 100000 {
				rows.Close()
				return catalog.SemanticSearchPage{}, catalog.E(catalog.CodeWorkLimitExceeded, "hybrid matching limit exceeded; narrow the search")
			}
			if id == nil {
				continue
			}
			if release == nil || identifier == nil || version == nil || versionPresent == nil || kind == nil || digest == nil || name == nil || description == nil || publisher == nil || rank == nil || score == nil || sortName == nil {
				rows.Close()
				return catalog.SemanticSearchPage{}, catalog.E(catalog.CodeInternal, "incomplete hybrid page")
			}
			item := catalog.SemanticSearchItem{Position: catalog.SemanticSearchPosition{Rank: *rank, SortName: *sortName, ID: *id}, Result: catalog.CatalogSearchResult{ID: *id, Identifier: *identifier, Version: *version, VersionPresent: *versionPresent, Kind: catalog.Kind(*kind), DisplayName: *name, Description: *description, Tags: tags, Publisher: *publisher, Score: *score}}
			if *release != "" {
				item.Result.Type = catalog.SearchResultLocal
			} else {
				item.Result.Type = catalog.SearchResultIndexed
				evidence.NormalizedEntryDigest = *digest
				evidence.SourcesTruncated = evidence.SourceCount > len(evidence.SourceIDs)
				evidence.ObservationGroupID, err = federation.ObservationGroupID(*identifier, *version, *versionPresent)
				if err != nil {
					rows.Close()
					return catalog.SemanticSearchPage{}, err
				}
				item.Result.Indexed = &evidence
			}
			page.Items = append(page.Items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return catalog.SemanticSearchPage{}, translateError("iterate hybrid page", err)
		}
		rows.Close()
		page.HasMore = len(page.Items) > request.Query.Limit
		if page.HasMore {
			page.Items = page.Items[:request.Query.Limit]
		}
		for _, item := range page.Items {
			if item.Result.Type == catalog.SearchResultLocal {
				ids = append(ids, strings.TrimPrefix(item.Result.ID, "local:"))
			}
		}
		locals, err := readSearchRecords(ctx, tx, g.TenantID, ids)
		if err != nil {
			return catalog.SemanticSearchPage{}, err
		}
		for i := range page.Items {
			item := &page.Items[i]
			if item.Result.Type == catalog.SearchResultLocal {
				item.Local = locals[strings.TrimPrefix(item.Result.ID, "local:")]
				if item.Local == nil {
					return catalog.SemanticSearchPage{}, catalog.E(catalog.CodeInternal, "missing hybrid local record")
				}
				item.Local.RelevanceScore = item.Result.Score
			}
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM semantic_members WHERE tenant_id=$1 AND generation_id=$2 AND failure<>'')`, g.TenantID, g.ID).Scan(&page.PartialIndex); err != nil {
			return catalog.SemanticSearchPage{}, translateError("read hybrid index failures", err)
		}
		return page, nil
	})
}
