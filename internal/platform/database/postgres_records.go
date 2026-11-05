// Local discovery selects current published records through SQL lexical, filter
// and page projections before bounded hydration.

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Local vectors/sort keys are generated from immutable metadata on write.
// Positionless vector union preserves cross-field matching and field weights.
// Query normalization stays statement-local, including generic prepared plans.
// Trust is a scalar projection: only trust facets need it for every match;
// ordinary counts/pages leave current-evidence reads to bounded hydration.
var catalogSearchBaseCTE = `
WITH records AS (
  SELECT release.id AS release_id,
         resource.canonical_identifier,
         resource.kind,
         listing.namespace_id,
         revision.display_name,
         revision.search_sort_name COLLATE "C" AS sort_name,
         revision.description,
         revision.tags,
         revision.categories,
         revision.license,
         revision.representative_queries,
         revision.publisher_id,
         revision.visibility,
         (SELECT coalesce(evidence.trust->>'state','not_assessed')
            FROM resource_version_revisions version_revision
            JOIN evidence_sets evidence ON evidence.tenant_id=version_revision.tenant_id AND evidence.id=coalesce(
              (SELECT run.evidence_set_id FROM version_validation_runs run
                WHERE run.tenant_id=version_revision.tenant_id AND run.version_revision_id=version_revision.id
                ORDER BY run.run_number DESC LIMIT 1),
              version_revision.evidence_set_id)
           WHERE version_revision.tenant_id=release.tenant_id AND version_revision.id=release.version_revision_id) AS trust_state,
         'local:' || release.id::text AS result_id,
         (resource.search_identifier_vector || revision.search_display_vector || revision.search_description_vector || revision.search_tags_vector || revision.search_queries_vector) AS document_vector,
         resource.search_identifier_vector AS identifier_vector,
         revision.search_display_vector AS display_vector,
         revision.search_description_vector AS description_vector,
         revision.search_tags_vector AS tags_vector,
         revision.search_queries_vector AS queries_vector
    FROM catalog_listings listing
    JOIN listing_releases release
      ON release.tenant_id=listing.tenant_id AND release.id=listing.current_release_id
     AND release.state IN ('published','deprecated')
    JOIN resources resource
      ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id AND resource.deleted_at IS NULL
    JOIN listing_revisions revision
      ON revision.tenant_id=release.tenant_id AND revision.id=release.listing_revision_id
   WHERE listing.tenant_id=$1 AND ` + collectionPermissionSQL("resource.read", "listing.namespace_id") + `
)`

const catalogSearchFilterSQL = `
    FROM records
   WHERE (coalesce(cardinality($4::text[]),0)=0 OR kind=ANY($4::text[]))
     AND (coalesce(cardinality($5::text[]),0)=0 OR tags @> $5::text[])
     AND ($6='' OR publisher_id::text=$6)
     AND ($7='' OR visibility=$7)
     AND ($8='' OR namespace_id::text=$8)
     AND ($9='' OR license=$9)
     AND (coalesce(cardinality($10::text[]),0)=0 OR categories @> $10::text[])
     AND ($2='' OR ($3<>'' AND canonical_identifier=$3) OR document_vector @@ (SELECT catalog_core_lexical_query_v1($2)))
`

var catalogSearchRecordsCTE = catalogSearchBaseCTE + `, matched AS (
  SELECT release_id,kind,namespace_id,sort_name,tags,categories,publisher_id,visibility,trust_state,result_id,
         CASE
           WHEN $2='' THEN 0
           WHEN $3<>'' AND canonical_identifier=$3 THEN 100
           ELSE least(99,40
             + CASE WHEN display_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 20 ELSE 0 END
             + CASE WHEN queries_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 15 ELSE 0 END
             + CASE WHEN description_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 10 ELSE 0 END
             + CASE WHEN tags_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 5 ELSE 0 END
             + CASE WHEN identifier_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 5 ELSE 0 END)
         END::integer AS score` + catalogSearchFilterSQL + `) `

// Count and page share a narrow evaluated match set. This avoids joining and
// testing all eligible listings twice. No documents, vectors or trust evidence
// enter the materialized set. The outer join retains totals for empty and
// terminal pages; a zero total does not execute page selection.
var catalogSearchPageSQL = strings.NewReplacer(
	"matched AS (", "matched AS MATERIALIZED (",
	"SELECT release_id,kind,namespace_id,sort_name,tags,categories,publisher_id,visibility,trust_state,result_id,", "SELECT release_id,sort_name,result_id,",
).Replace(catalogSearchRecordsCTE) + `
SELECT totals.total,page.release_id::text,page.score
  FROM (SELECT count(*) AS total FROM matched) totals
  LEFT JOIN LATERAL (
    SELECT release_id,score,sort_name,result_id FROM matched
     WHERE totals.total>0 AND ($11=false OR score<$12 OR (score=$12 AND (sort_name>$13 COLLATE "C" OR (sort_name=$13 COLLATE "C" AND result_id>$14))))
     ORDER BY score DESC,sort_name COLLATE "C",result_id LIMIT $15
  ) page ON true
 ORDER BY page.score DESC,page.sort_name COLLATE "C",page.result_id`

// Text presence is a known query shape. All values and filters stay bound;
// normal execution uses request-specific planning rather than a filter matrix.
var (
	catalogSearchEmptyRecordsCTE = strings.ReplaceAll(catalogSearchRecordsCTE, "$2=''", "true")
	catalogSearchEmptyPageSQL    = strings.ReplaceAll(catalogSearchPageSQL, "$2=''", "true")
	catalogSearchTextRecordsCTE  = strings.ReplaceAll(catalogSearchRecordsCTE, "$2=''", "false")
	catalogSearchTextPageSQL     = strings.ReplaceAll(catalogSearchPageSQL, "$2=''", "false")
)

// catalogSearchStatements selects the prebuilt text-present or text-absent
// query shape; the search text and all filter values remain bound parameters.
func catalogSearchStatements(query catalog.SearchQuery) (records, page string) {
	if query.Text == "" {
		return catalogSearchEmptyRecordsCTE, catalogSearchEmptyPageSQL
	}
	return catalogSearchTextRecordsCTE, catalogSearchTextPageSQL
}

// PublicationRecords applies publication visibility and administrator-approved
// publisher authority before hydrating any bounded representation documents.
func (p *PostgresRepository) PublicationRecords(ctx context.Context, tenantID string, request catalog.PublicationRecordRequest) (catalog.PublicationRecordPage, error) {
	if request.Limit < 1 || request.Limit > catalog.MaxPublicationRecords {
		return catalog.PublicationRecordPage{}, catalog.E(catalog.CodeInvalidArgument, "publication record limit must be positive")
	}
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.PublicationRecordPage, error) {
		rows, err := tx.Query(ctx, `
      SELECT r.id,r.tenant_id,r.namespace_id,r.canonical_identifier,r.kind,r.created_by,r.revision,r.created_at,r.updated_at,
             v.id,v.tenant_id,v.resource_id,v.version,coalesce(v.candidate_revision_id::text,''),coalesce(v.sealed_revision_id::text,''),v.revision,v.created_at,v.updated_at,
             l.id,l.tenant_id,l.namespace_id,l.resource_id,coalesce(l.candidate_revision_id::text,''),coalesce(l.current_release_id::text,''),l.revision,l.created_at,l.updated_at,
             lr.id,lr.tenant_id,lr.listing_id,lr.submission_id,lr.version_revision_id,lr.listing_revision_id,lr.state,coalesce(lr.replacement_release_id::text,''),lr.disposition_reason,lr.sunset_at,lr.activated_by,lr.revision,lr.activated_at,lr.updated_at
        FROM catalog_listings l
        JOIN listing_releases lr ON lr.tenant_id=l.tenant_id AND lr.id=l.current_release_id AND lr.state IN ('published','deprecated')
        JOIN resource_version_revisions vr ON vr.tenant_id=lr.tenant_id AND vr.id=lr.version_revision_id
        JOIN resource_versions v ON v.tenant_id=vr.tenant_id AND v.id=vr.resource_version_id
        JOIN resources r ON r.tenant_id=l.tenant_id AND r.id=l.resource_id AND r.deleted_at IS NULL
        JOIN listing_revisions lrev ON lrev.tenant_id=lr.tenant_id AND lrev.id=lr.listing_revision_id
        JOIN publishers publisher ON publisher.tenant_id=lrev.tenant_id AND publisher.id=lrev.publisher_id
       WHERE l.tenant_id=$1
         AND (($2 AND lrev.visibility IN ('public','tenant')) OR (NOT $2 AND lrev.visibility='public'))
         AND lower(split_part(r.canonical_identifier,':',3))=ANY(publisher.approved_publication_domains)
       ORDER BY r.canonical_identifier COLLATE "C",v.version COLLATE "C",lr.id
       LIMIT $3`, tenantID, request.IncludeTenant, request.Limit+1)
		if err != nil {
			return catalog.PublicationRecordPage{}, translateError("select bounded publication records", err)
		}
		items := make([]catalog.CatalogRecord, 0, request.Limit+1)
		for rows.Next() {
			var item catalog.CatalogRecord
			if err := rows.Scan(
				&item.Resource.ID, &item.Resource.TenantID, &item.Resource.NamespaceID, &item.Resource.CanonicalIdentifier, &item.Resource.Kind, &item.Resource.CreatedBy, &item.Resource.Revision, &item.Resource.CreatedAt, &item.Resource.UpdatedAt,
				&item.Version.ID, &item.Version.TenantID, &item.Version.ResourceID, &item.Version.Version, &item.Version.CandidateRevisionID, &item.Version.SealedRevisionID, &item.Version.Revision, &item.Version.CreatedAt, &item.Version.UpdatedAt,
				&item.Listing.ID, &item.Listing.TenantID, &item.Listing.NamespaceID, &item.Listing.ResourceID, &item.Listing.CandidateRevisionID, &item.Listing.CurrentReleaseID, &item.Listing.Revision, &item.Listing.CreatedAt, &item.Listing.UpdatedAt,
				&item.Release.ID, &item.Release.TenantID, &item.Release.ListingID, &item.Release.SubmissionID, &item.Release.VersionRevisionID, &item.Release.ListingRevisionID, &item.Release.State, &item.Release.ReplacementReleaseID, &item.Release.DispositionReason, &item.Release.SunsetAt, &item.Release.ActivatedBy, &item.Release.Revision, &item.Release.ActivatedAt, &item.Release.UpdatedAt,
			); err != nil {
				rows.Close()
				return catalog.PublicationRecordPage{}, translateError("scan bounded publication record", err)
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return catalog.PublicationRecordPage{}, translateError("iterate bounded publication records", err)
		}
		rows.Close()
		if len(items) > request.Limit {
			return catalog.PublicationRecordPage{Overflow: true}, nil
		}
		projectionBytes := 0
		for index := range items {
			versionRevision, evidence, readErr := readVersionRevision(ctx, tx, tenantID, items[index].Release.VersionRevisionID)
			if readErr != nil {
				return catalog.PublicationRecordPage{}, readErr
			}
			listingRevision, readErr := readPublicationListingRevision(ctx, tx, tenantID, items[index].Release.ListingRevisionID)
			if readErr != nil {
				return catalog.PublicationRecordPage{}, readErr
			}
			items[index].VersionRevision = versionRevision
			items[index].ListingRevision = listingRevision
			items[index].Evidence = evidence
			items[index].Source = "local"
			raw, marshalErr := json.Marshal(items[index])
			if marshalErr != nil {
				return catalog.PublicationRecordPage{}, catalog.Wrap(catalog.CodeInternal, "measure publication record", marshalErr)
			}
			projectionBytes += len(raw)
			if projectionBytes > catalog.MaxPublicationProjectionBytes {
				return catalog.PublicationRecordPage{}, catalog.E(catalog.CodeWorkLimitExceeded, "publication record projection exceeds the supported byte limit")
			}
		}
		return catalog.PublicationRecordPage{Records: items}, nil
	})
}

// PublishedReleaseIDsByVersionRevision selects current published or deprecated
// release IDs with limit+1. Overflow returns nil IDs and true so callers cannot
// mistake a partial set for complete withdrawal scope.
func (p *PostgresRepository) PublishedReleaseIDsByVersionRevision(ctx context.Context, tenantID, revisionID string, limit int) ([]string, bool, error) {
	if limit < 1 {
		return nil, false, catalog.E(catalog.CodeInvalidArgument, "release lookup limit must be positive")
	}
	type result struct {
		ids      []string
		overflow bool
	}
	selected, err := withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (result, error) {
		rows, err := tx.Query(ctx, `
      SELECT release.id::text
        FROM catalog_listings listing
        JOIN listing_releases release
          ON release.tenant_id=listing.tenant_id AND release.id=listing.current_release_id
       WHERE listing.tenant_id=$1 AND release.version_revision_id=$2
         AND release.state IN ('published','deprecated')
       ORDER BY release.id LIMIT $3`, tenantID, revisionID, limit+1)
		if err != nil {
			return result{}, translateError("select releases for version revision", err)
		}
		defer rows.Close()
		ids := make([]string, 0, limit+1)
		for rows.Next() {
			var idValue string
			if err := rows.Scan(&idValue); err != nil {
				return result{}, translateError("scan release for version revision", err)
			}
			ids = append(ids, idValue)
		}
		if err := rows.Err(); err != nil {
			return result{}, translateError("iterate releases for version revision", err)
		}
		overflow := len(ids) > limit
		if overflow {
			ids = nil
		}
		return result{ids: ids, overflow: overflow}, nil
	})
	return selected.ids, selected.overflow, err
}

// SearchCatalog filters, scores, orders, counts, and bounds local published
// records in PostgreSQL under one repeatable-read generation.
func (p *PostgresRepository) SearchCatalog(ctx context.Context, tenantID string, request catalog.CatalogSearchRepositoryRequest) (catalog.CatalogSearchRepositoryPage, error) {
	return withTenantCollectionRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.CatalogSearchRepositoryPage, error) {
		return searchCatalogTx(ctx, tx, tenantID, request)
	})
}

// searchCatalogTx validates the cursor generation, obtains an exact total and
// limit+1 ID page in SQL, then hydrates only selected records within the caller's
// snapshot.
func searchCatalogTx(ctx context.Context, tx pgx.Tx, tenantID string, request catalog.CatalogSearchRepositoryRequest) (catalog.CatalogSearchRepositoryPage, error) {
	generation, err := readCatalogSearchGeneration(ctx, tx, tenantID)
	if err != nil {
		return catalog.CatalogSearchRepositoryPage{}, err
	}
	if request.ExpectedGeneration != "" && request.ExpectedGeneration != generation {
		return catalog.CatalogSearchRepositoryPage{}, catalog.Wrap(catalog.CodeInvalidArgument, "catalog cursor is stale", catalog.ErrSearchGenerationChanged)
	}
	if !request.Query.IncludesOrigin(catalog.SearchResultLocal) {
		return catalog.CatalogSearchRepositoryPage{Items: []catalog.CatalogSummary{}, Generation: generation}, nil
	}
	if request.IdentityProbes != nil {
		return localLiveMatches(ctx, tx, tenantID, request, generation)
	}
	parameters := catalogSearchParameters(tenantID, request.Query)
	_, pageSQL := catalogSearchStatements(request.Query)
	after := request.After
	afterPresent := after != nil
	var afterScore any = 0
	afterName, afterID := "", ""
	if after != nil {
		afterScore, afterName, afterID = after.Score, after.DisplayName, after.ResultID
	}
	if request.Query.Order != "" {
		pageSQL = localColumnOrderSQL(pageSQL, request.Query.Order)
		afterScore = ""
		if after != nil {
			afterScore = after.SortValue
		}
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 20
	}
	// Selectivity varies widely with text and filters. Extended-protocol Exec
	// binds these built-in parameter types in one round trip, without retaining
	// a named plan that auto planning can replace with a poor generic plan.
	// This is not simple-protocol interpolation or a session-wide setting.
	pageParameters := append([]any{pgx.QueryExecModeExec}, parameters...)
	pageParameters = append(pageParameters, afterPresent, afterScore, afterName, afterID, limit+1)
	rows, err := tx.Query(ctx, collectionStatement(ctx, pageSQL), pageParameters...)
	if err != nil {
		return catalog.CatalogSearchRepositoryPage{}, translateError("query bounded catalog search", err)
	}
	type selectedRecord struct {
		id    string
		score int
	}
	selected := make([]selectedRecord, 0, limit+1)
	var total int
	hasTotal := false
	for rows.Next() {
		var releaseID *string
		var score *int
		if err := rows.Scan(&total, &releaseID, &score); err != nil {
			rows.Close()
			return catalog.CatalogSearchRepositoryPage{}, translateError("scan bounded catalog search", err)
		}
		hasTotal = true
		if releaseID == nil && score == nil {
			continue // Exact total with no selected page, including terminal cursors.
		}
		if releaseID == nil || score == nil {
			rows.Close()
			return catalog.CatalogSearchRepositoryPage{}, catalog.E(catalog.CodeInternal, "incomplete catalog search selection")
		}
		selected = append(selected, selectedRecord{id: *releaseID, score: *score})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return catalog.CatalogSearchRepositoryPage{}, translateError("iterate bounded catalog search", err)
	}
	rows.Close()
	if !hasTotal {
		return catalog.CatalogSearchRepositoryPage{}, catalog.E(catalog.CodeInternal, "catalog search total is missing")
	}
	hasMore := len(selected) > limit
	if hasMore {
		selected = selected[:limit]
	}
	releaseIDs := make([]string, len(selected))
	for index, value := range selected {
		releaseIDs[index] = value.id
	}
	records, err := readSearchRecords(ctx, tx, tenantID, releaseIDs)
	if err != nil {
		return catalog.CatalogSearchRepositoryPage{}, err
	}
	items := make([]catalog.CatalogSummary, 0, len(selected))
	for _, selectedRecord := range selected {
		record := *records[selectedRecord.id]
		record.RelevanceScore = selectedRecord.score
		items = append(items, record)
	}
	return catalog.CatalogSearchRepositoryPage{Items: items, Total: total, Generation: generation, HasMore: hasMore}, nil
}

// GetCatalogRecord hydrates one tenant-bound current published or deprecated
// release and its immutable evidence; noncurrent or deleted records are not found.
func (p *PostgresRepository) GetCatalogRecord(ctx context.Context, tenantID, releaseID string) (catalog.CatalogRecord, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) (catalog.CatalogRecord, error) {
		return readCurrentCatalogRecord(ctx, tx, tenantID, releaseID)
	})
}

// catalogSearchParameters binds tenant and search filters in the fixed order
// shared by record and facet statements.
func catalogSearchParameters(tenantID string, query catalog.SearchQuery) []any {
	exactIdentifier := catalog.CatalogSearchExactIdentifier(query.Text)
	kinds := make([]string, 0, len(query.Kinds))
	for _, kind := range query.Kinds {
		kinds = append(kinds, string(kind))
	}
	tags := append([]string{}, query.Tags...)
	return []any{tenantID, query.Text, exactIdentifier, kinds, tags, query.Publisher, string(query.Visibility), query.NamespaceID, query.License, append([]string{}, query.Categories...)}
}

// readCatalogSearchGeneration returns the tenant generation used to bind cursors
// and fails closed when an observation search projection requires repair.
func readCatalogSearchGeneration(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	var generation int64
	var needsRepair bool
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT generation FROM catalog_search_generations WHERE tenant_id=$1),0),
        EXISTS (SELECT 1 FROM observations WHERE tenant_id=$1 AND normalized_entry_digest IS NOT NULL
          AND (search_display_name IS NULL OR search_description IS NULL OR search_tags IS NULL
            OR search_representative_queries IS NULL OR search_publisher IS NULL OR search_vector IS NULL OR search_vector_version<>2 OR search_filter_version<>1)
          LIMIT 1)`, tenantID).Scan(&generation, &needsRepair); err != nil {
		return "", translateError("read catalog search generation", err)
	}
	if needsRepair {
		return "", catalog.E(catalog.CodeDependencyUnavailable, "catalog search projection repair is required")
	}
	return strconv.FormatInt(generation, 10), nil
}

const currentCatalogRecordReadSQL = `
      SELECT r.id,r.tenant_id,r.namespace_id,r.canonical_identifier,r.kind,r.created_by,r.revision,r.created_at,r.updated_at,
             v.id,v.tenant_id,v.resource_id,v.version,coalesce(v.candidate_revision_id::text,''),coalesce(v.sealed_revision_id::text,''),v.revision,v.created_at,v.updated_at,
             l.id,l.tenant_id,l.namespace_id,l.resource_id,coalesce(l.candidate_revision_id::text,''),coalesce(l.current_release_id::text,''),l.revision,l.created_at,l.updated_at,
             lr.id,lr.tenant_id,lr.listing_id,lr.submission_id,lr.version_revision_id,lr.listing_revision_id,lr.state,coalesce(lr.replacement_release_id::text,''),lr.disposition_reason,lr.sunset_at,lr.activated_by,lr.revision,lr.activated_at,lr.updated_at,coalesce(publisher.display_name,''),coalesce(namespace.display_name,'')
        FROM listing_releases lr
        JOIN catalog_listings l
          ON l.tenant_id=lr.tenant_id AND l.current_release_id=lr.id
        JOIN resource_version_revisions vr
          ON vr.tenant_id=lr.tenant_id AND vr.id=lr.version_revision_id
        JOIN resource_versions v
          ON v.tenant_id=vr.tenant_id AND v.id=vr.resource_version_id
        JOIN resources r
          ON r.tenant_id=l.tenant_id AND r.id=l.resource_id AND r.deleted_at IS NULL
        JOIN listing_revisions display ON display.tenant_id=lr.tenant_id AND display.id=lr.listing_revision_id
        LEFT JOIN publishers publisher ON publisher.tenant_id=display.tenant_id AND publisher.id=display.publisher_id
        LEFT JOIN namespaces namespace ON namespace.tenant_id=l.tenant_id AND namespace.id=l.namespace_id
       WHERE lr.tenant_id=$1 AND lr.id=ANY($2::uuid[]) AND lr.state IN ('published','deprecated')`

// scanCurrentCatalogRecord decodes joined resource, version, listing and release
// metadata before immutable revisions are loaded.
func scanCurrentCatalogRecord(row pgx.Row) (*catalog.CatalogRecord, error) {
	item := new(catalog.CatalogRecord)
	item.Display = new(catalog.CatalogPresentation)
	err := row.Scan(
		&item.Resource.ID, &item.Resource.TenantID, &item.Resource.NamespaceID, &item.Resource.CanonicalIdentifier, &item.Resource.Kind, &item.Resource.CreatedBy, &item.Resource.Revision, &item.Resource.CreatedAt, &item.Resource.UpdatedAt,
		&item.Version.ID, &item.Version.TenantID, &item.Version.ResourceID, &item.Version.Version, &item.Version.CandidateRevisionID, &item.Version.SealedRevisionID, &item.Version.Revision, &item.Version.CreatedAt, &item.Version.UpdatedAt,
		&item.Listing.ID, &item.Listing.TenantID, &item.Listing.NamespaceID, &item.Listing.ResourceID, &item.Listing.CandidateRevisionID, &item.Listing.CurrentReleaseID, &item.Listing.Revision, &item.Listing.CreatedAt, &item.Listing.UpdatedAt,
		&item.Release.ID, &item.Release.TenantID, &item.Release.ListingID, &item.Release.SubmissionID, &item.Release.VersionRevisionID, &item.Release.ListingRevisionID, &item.Release.State, &item.Release.ReplacementReleaseID, &item.Release.DispositionReason, &item.Release.SunsetAt, &item.Release.ActivatedBy, &item.Release.Revision, &item.Release.ActivatedAt, &item.Release.UpdatedAt, &item.Display.PublisherName, &item.Display.NamespaceName,
	)
	if err != nil {
		return nil, translateError("read catalog record", err)
	}

	return item, nil
}

// readCurrentCatalogRecord loads current local release metadata and its exact
// retained revisions and validation evidence in the caller's transaction.
func readCurrentCatalogRecord(ctx context.Context, tx pgx.Tx, tenantID, releaseID string) (catalog.CatalogRecord, error) {
	item, err := scanCurrentCatalogRecord(tx.QueryRow(ctx, currentCatalogRecordReadSQL, tenantID, []string{releaseID}))
	if err != nil {
		return catalog.CatalogRecord{}, err
	}
	versionRevision, evidence, err := readVersionRevision(ctx, tx, tenantID, item.Release.VersionRevisionID)
	if err != nil {
		return catalog.CatalogRecord{}, err
	}
	listingRevision, err := readListingRevision(ctx, tx, tenantID, item.Release.ListingRevisionID)
	if err != nil {
		return catalog.CatalogRecord{}, err
	}
	item.VersionRevision = versionRevision
	item.ListingRevision = listingRevision
	item.Display = catalog.NewCatalogPresentation(item.Display.PublisherName, item.Display.NamespaceName, listingRevision.License)
	item.Evidence = evidence
	item.Source = "local"
	return *item, nil
}

// AuditEvents returns a SQL-limited tenant page in descending occurrence/ID order.
// Oversized retained records fail the read rather than loading unbounded details
// or returning truncated evidence.
func (p *PostgresRepository) AuditEvents(ctx context.Context, tenantID string, request catalog.AuditEventListRequest) ([]catalog.AuditEvent, error) {
	return withTenantRead(ctx, p.pool, tenantID, func(tx pgx.Tx) ([]catalog.AuditEvent, error) {
		rows, err := tx.Query(ctx, `
			SELECT format_version,id,tenant_id,
			       CASE WHEN bounds.estimated_record_bytes<=$12 THEN actor ELSE '' END,
			       CASE WHEN bounds.estimated_record_bytes<=$12 THEN action ELSE '' END,
			       CASE WHEN bounds.estimated_record_bytes<=$12 THEN target_type ELSE '' END,
			       CASE WHEN bounds.estimated_record_bytes<=$12 THEN target_id ELSE '' END,
			       CASE WHEN bounds.estimated_record_bytes<=$12 THEN reason ELSE '' END,
			       CASE WHEN bounds.estimated_record_bytes<=$12 THEN request_id ELSE '' END,
			       CASE WHEN bounds.estimated_record_bytes<=$12 THEN before_digest ELSE '' END,
			       CASE WHEN bounds.estimated_record_bytes<=$12 THEN after_digest ELSE '' END,
			       CASE WHEN bounds.estimated_record_bytes<=$12 THEN details END,
			       occurred_at
			FROM audit_events
			CROSS JOIN LATERAL (
			  SELECT 512::bigint + octet_length(actor) + octet_length(action) + octet_length(target_type) + octet_length(target_id) +
			         octet_length(reason) + octet_length(request_id) + octet_length(before_digest) + octet_length(after_digest) +
			         octet_length(details::text) AS estimated_record_bytes
			) bounds
			WHERE tenant_id=$1 AND ($2::timestamptz IS NULL OR occurred_at<$2 OR (occurred_at=$2 AND id<nullif($3,'')::uuid))
			  AND ($4='' OR (octet_length(actor)<=512 AND actor=$4))
			  AND ($5='' OR (octet_length(action)<=160 AND action=$5))
			  AND ($6='' OR (request_id<>'' AND octet_length(request_id)<=128 AND request_id=$6))
			  AND ($7='' OR (octet_length(target_type)<=160 AND octet_length(target_id)<=512 AND target_type=$7 AND target_id=$8))
			  AND ($9::timestamptz IS NULL OR occurred_at >= $9)
			  AND ($10::timestamptz IS NULL OR occurred_at < $10)
			ORDER BY occurred_at DESC,id DESC LIMIT $11`, tenantID, request.AfterOccurredAt, request.AfterID, request.Filters.Actor, request.Filters.Action, request.Filters.RequestID, request.Filters.TargetType, request.Filters.TargetID, request.Filters.OccurredFrom, request.Filters.OccurredBefore, request.Limit, audit.MaxEncodedRecordBytes)
		if err != nil {
			return nil, translateError("list audit events", err)
		}
		defer rows.Close()
		items := make([]catalog.AuditEvent, 0)
		for rows.Next() {
			var item catalog.AuditEvent
			var details []byte
			if err := rows.Scan(&item.FormatVersion, &item.ID, &item.TenantID, &item.Actor, &item.Action, &item.TargetType, &item.TargetID, &item.Reason, &item.RequestID, &item.BeforeDigest, &item.AfterDigest, &details, &item.At); err != nil {
				return nil, translateError("scan audit event", err)
			}
			item.At = item.At.UTC().Truncate(time.Microsecond)
			if details == nil {
				return nil, catalog.E(catalog.CodeInternal, "version-1 audit event exceeds its database record invariant")
			}
			decoder := json.NewDecoder(bytes.NewReader(details))
			decoder.UseNumber()
			if err := decoder.Decode(&item.Details); err != nil {
				return nil, catalog.Wrap(catalog.CodeInternal, "decode audit details", err)
			}
			items = append(items, item)
		}
		return items, translateError("iterate audit events", rows.Err())
	})
}

// AppendAuditEvent validates and persists audit-only evidence in a tenant
// transaction, joining an active command transaction when present.
func (p *PostgresRepository) AppendAuditEvent(ctx context.Context, event catalog.AuditEvent) error {
	_, err := withTenantTx(ctx, p.pool, event.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
		return struct{}{}, insertAuditEvent(ctx, tx, event)
	})
	return err
}

// AppendEvidence atomically persists audit evidence and an optional validated
// outbox pair; nil outbox preserves audit-only actions.
func (p *PostgresRepository) AppendEvidence(ctx context.Context, event catalog.AuditEvent, outbox *catalog.OutboxEvent) error {
	_, err := withTenantTx(ctx, p.pool, event.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
		if outbox != nil {
			if err := validateEvidencePair(event, *outbox); err != nil {
				return struct{}{}, err
			}
		}
		if err := insertAuditEvent(ctx, tx, event); err != nil {
			return struct{}{}, err
		}
		if outbox == nil {
			return struct{}{}, nil
		}
		if err := insertOutboxEvent(ctx, tx, *outbox); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

// ExportAuditEvents builds a deterministic bounded range and records access to
// that sensitive evidence in the same repeatable-read transaction.
func (p *PostgresRepository) ExportAuditEvents(ctx context.Context, tenantID string, request catalog.AuditExportRequest) (catalog.AuditExport, error) {
	return withTenantTx(ctx, p.pool, tenantID, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) (catalog.AuditExport, error) {
		items, err := selectAuditExportRecords(ctx, tx, tenantID, request.Filters)
		if err != nil {
			return catalog.AuditExport{}, err
		}
		export, _, err := audit.BuildExport(tenantID, request.Filters, request.Cutoff, request.GeneratedAt, items)
		if err != nil {
			return catalog.AuditExport{}, catalog.TranslateAuditExportError(err)
		}
		request.Access = catalog.CompleteAuditExportAccess(request.Access, export.Manifest)
		if err := insertAuditEvent(ctx, tx, request.Access); err != nil {
			return catalog.AuditExport{}, err
		}
		return export, nil
	})
}

// selectAuditExportRecords checks row and record-size bounds before hydration,
// then enforces encoded response bytes while reading a deterministic ascending
// range.
func selectAuditExportRecords(ctx context.Context, tx pgx.Tx, tenantID string, filters audit.Query) ([]catalog.AuditEvent, error) {
	metadataRows, err := tx.Query(ctx, `
		SELECT id::text,occurred_at,format_version,
		       512::bigint + octet_length(actor) + octet_length(action) + octet_length(target_type) + octet_length(target_id) +
		       octet_length(reason) + octet_length(request_id) + octet_length(before_digest) + octet_length(after_digest) +
		       octet_length(details::text)
		  FROM audit_events
		 WHERE tenant_id=$1
		   AND ($2='' OR (octet_length(actor)<=512 AND actor=$2))
		   AND ($3='' OR (octet_length(action)<=160 AND action=$3))
		   AND ($4='' OR (request_id<>'' AND octet_length(request_id)<=128 AND request_id=$4))
		   AND ($5='' OR (octet_length(target_type)<=160 AND octet_length(target_id)<=512 AND target_type=$5 AND target_id=$6))
		   AND occurred_at >= $7 AND occurred_at < $8
		 ORDER BY occurred_at ASC,id ASC
		 LIMIT $9`, tenantID, filters.Actor, filters.Action, filters.RequestID, filters.TargetType, filters.TargetID, filters.OccurredFrom, filters.OccurredBefore, audit.MaxExportRows+1)
	if err != nil {
		return nil, translateError("inspect audit export bounds", err)
	}
	metadataCount := 0
	for metadataRows.Next() {
		var id string
		var occurredAt time.Time
		var formatVersion *int
		var conservativeEncodedBytes int64
		if err := metadataRows.Scan(&id, &occurredAt, &formatVersion, &conservativeEncodedBytes); err != nil {
			metadataRows.Close()
			return nil, translateError("scan audit export bounds", err)
		}
		metadataCount++
		if formatVersion == nil || *formatVersion != audit.RecordFormatV1 || conservativeEncodedBytes > audit.MaxEncodedRecordBytes {

			metadataRows.Close()
			return nil, catalog.E(catalog.CodeInternal, "version-1 audit event exceeds its database record invariant")
		}
	}
	if err := metadataRows.Err(); err != nil {
		metadataRows.Close()
		return nil, translateError("iterate audit export bounds", err)
	}
	metadataRows.Close()
	if metadataCount > audit.MaxExportRows {
		return nil, catalog.TranslateAuditExportError(&audit.RangeTooLargeError{Reason: "row limit exceeded"})
	}

	rows, err := tx.Query(ctx, `
		SELECT format_version,id,tenant_id,actor,action,target_type,target_id,reason,request_id,before_digest,after_digest,details,occurred_at
		  FROM audit_events
		 WHERE tenant_id=$1
		   AND ($2='' OR (octet_length(actor)<=512 AND actor=$2))
		   AND ($3='' OR (octet_length(action)<=160 AND action=$3))
		   AND ($4='' OR (request_id<>'' AND octet_length(request_id)<=128 AND request_id=$4))
		   AND ($5='' OR (octet_length(target_type)<=160 AND octet_length(target_id)<=512 AND target_type=$5 AND target_id=$6))
		   AND occurred_at >= $7 AND occurred_at < $8
		 ORDER BY occurred_at ASC,id ASC
		 LIMIT $9`, tenantID, filters.Actor, filters.Action, filters.RequestID, filters.TargetType, filters.TargetID, filters.OccurredFrom, filters.OccurredBefore, audit.MaxExportRows+1)
	if err != nil {
		return nil, translateError("select audit export", err)
	}
	items := make([]catalog.AuditEvent, 0, metadataCount)
	encodedEventBytes := 2 // Opening and closing array brackets.
	for rows.Next() {
		item, scanErr := scanAuditEvent(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		encoded, encodeErr := json.Marshal(item)
		if encodeErr != nil {
			rows.Close()
			return nil, catalog.Wrap(catalog.CodeInternal, "encode selected audit event", encodeErr)
		}
		if len(encoded) > audit.MaxEncodedRecordBytes {
			rows.Close()
			return nil, catalog.E(catalog.CodeInternal, "version-1 audit event exceeds its database record invariant")
		}
		if len(items) > 0 {
			encodedEventBytes++
		}
		encodedEventBytes += len(encoded)
		if encodedEventBytes > audit.MaxExportResponseBytes {
			rows.Close()
			return nil, catalog.TranslateAuditExportError(&audit.RangeTooLargeError{Reason: "encoded response limit exceeded"})
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, translateError("iterate audit export", err)
	}
	rows.Close()
	return items, nil
}

// scanAuditEvent preserves numeric audit details and normalizes timestamps to UTC
// microseconds for deterministic export encoding.
func scanAuditEvent(row scanner) (catalog.AuditEvent, error) {
	var item catalog.AuditEvent
	var details []byte
	if err := row.Scan(&item.FormatVersion, &item.ID, &item.TenantID, &item.Actor, &item.Action, &item.TargetType, &item.TargetID, &item.Reason, &item.RequestID, &item.BeforeDigest, &item.AfterDigest, &details, &item.At); err != nil {
		return catalog.AuditEvent{}, translateError("scan audit event", err)
	}
	item.At = item.At.UTC().Truncate(time.Microsecond)
	decoder := json.NewDecoder(bytes.NewReader(details))
	decoder.UseNumber()
	if err := decoder.Decode(&item.Details); err != nil {
		return catalog.AuditEvent{}, catalog.Wrap(catalog.CodeInternal, "decode audit details", err)
	}
	return item, nil
}

// ClaimOutbox leases a SQL-limited cross-tenant worker batch of due unpublished
// events using SKIP LOCKED. Follow-up operations must use each event's tenant and
// the same owner.
func (p *PostgresRepository) ClaimOutbox(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]catalog.OutboxEvent, error) {
	rows, err := p.pool.Query(ctx, `
    WITH candidates AS (
      SELECT tenant_id,id FROM outbox_events
       WHERE published_at IS NULL AND (dispatch_after IS NULL OR dispatch_after <= $2) AND (lease_expires_at IS NULL OR lease_expires_at <= $2)
       ORDER BY coalesce(dispatch_after,created_at),id FOR UPDATE SKIP LOCKED LIMIT $1
    )
    UPDATE outbox_events event SET lease_owner=$3,lease_expires_at=$4
      FROM candidates WHERE event.tenant_id=candidates.tenant_id AND event.id=candidates.id
    RETURNING event.id,event.tenant_id,event.event_type,event.subject,event.data,event.is_internal,event.created_at,event.published_at,event.lease_owner,event.lease_expires_at,event.dispatch_after,coalesce(event.causal_trace,'')`, limit, now, owner, now.Add(lease))
	if err != nil {
		return nil, translateError("read outbox batch", err)
	}
	defer rows.Close()
	items := make([]catalog.OutboxEvent, 0)
	for rows.Next() {
		var item catalog.OutboxEvent
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Type, &item.Subject, &item.Data, &item.Internal, &item.CreatedAt, &item.PublishedAt, &item.LeaseOwner, &item.LeaseExpiresAt, &item.DispatchAfter, &item.CausalTrace); err != nil {
			return nil, translateError("scan outbox event", err)
		}
		items = append(items, item)
	}
	return items, translateError("iterate outbox events", rows.Err())
}

// MarkOutboxPublished completes one tenant-bound unpublished event only while the
// named owner's lease is unexpired at publishedAt; lost ownership returns
// conflict.
func (p *PostgresRepository) MarkOutboxPublished(ctx context.Context, tenantID, eventID, owner string, publishedAt time.Time) error {
	command, err := p.pool.Exec(ctx, `UPDATE outbox_events SET published_at=$4,lease_owner=NULL,lease_expires_at=NULL WHERE tenant_id=$1 AND id=$2 AND published_at IS NULL AND lease_owner=$3 AND lease_expires_at>$4`, tenantID, eventID, owner, publishedAt)
	if err != nil {
		return translateError("mark outbox event published", err)
	}
	if command.RowsAffected() == 0 {
		return catalog.E(catalog.CodeConflict, "outbox event lease is not owned or has expired")
	}
	return nil
}

// RenewOutbox extends one tenant-bound unpublished lease from now, returning
// conflict if ownership was lost or the prior lease expired.
func (p *PostgresRepository) RenewOutbox(ctx context.Context, tenantID, eventID, owner string, now time.Time, lease time.Duration) error {
	command, err := p.pool.Exec(ctx, `UPDATE outbox_events SET lease_expires_at=$5 WHERE tenant_id=$1 AND id=$2 AND published_at IS NULL AND lease_owner=$3 AND lease_expires_at>$4`, tenantID, eventID, owner, now, now.Add(lease))
	if err != nil {
		return translateError("renew outbox event lease", err)
	}
	if command.RowsAffected() == 0 {
		return catalog.E(catalog.CodeConflict, "outbox event lease is not owned or has expired")
	}
	return nil
}

// ReleaseOutbox clears an unpublished event's lease only for its current owner;
// expiry alone does not prevent voluntary release.
func (p *PostgresRepository) ReleaseOutbox(ctx context.Context, tenantID, eventID, owner string) error {
	command, err := p.pool.Exec(ctx, `UPDATE outbox_events SET lease_owner=NULL,lease_expires_at=NULL WHERE tenant_id=$1 AND id=$2 AND published_at IS NULL AND lease_owner=$3`, tenantID, eventID, owner)
	if err != nil {
		return translateError("release outbox event", err)
	}
	if command.RowsAffected() == 0 {
		return catalog.E(catalog.CodeConflict, "outbox event lease is not owned")
	}
	return nil
}

// DeferOutbox releases an owned, unexpired event until a strictly future time no
// more than one hour away, preserving durable retry without marking publication.
func (p *PostgresRepository) DeferOutbox(ctx context.Context, tenantID, eventID, owner string, now, notBefore time.Time) error {
	if !notBefore.After(now) || notBefore.After(now.Add(time.Hour)) {
		return catalog.E(catalog.CodeInvalidArgument, "outbox deferral must be within one hour")
	}
	command, err := p.pool.Exec(ctx, `UPDATE outbox_events SET dispatch_after=$6,lease_owner=NULL,lease_expires_at=NULL
WHERE tenant_id=$1 AND id=$2 AND published_at IS NULL AND lease_owner=$3 AND lease_expires_at>$4 AND $6>$4 AND $6<=$5`, tenantID, eventID, owner, now, now.Add(time.Hour), notBefore)
	if err != nil {
		return translateError("defer outbox event", err)
	}
	if command.RowsAffected() == 0 {
		return catalog.E(catalog.CodeConflict, "outbox event lease is not owned or has expired")
	}
	return nil
}

// Publication retains only the service-owned sealed presentation extension.
// Unknown local control extensions never become exchange metadata.
func readPublicationListingRevision(ctx context.Context, tx pgx.Tx, tenantID, id string) (catalog.ListingRevision, error) {
	query := strings.Replace(listingRevisionReadSQL, "revision.extensions,", `CASE WHEN octet_length((revision.extensions->'https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor')::text)<=32768 THEN jsonb_build_object('https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor',revision.extensions->'https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor') ELSE '{}'::jsonb END,`, 1)
	return scanListingRevision(tx.QueryRow(ctx, query, tenantID, []string{id}, false))
}
