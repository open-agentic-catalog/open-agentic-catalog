// Indexed Search SQL separates source eligibility, matching totals and bounded
// result ranking from conflict and provenance hydration.

package database

// Limit this compiler policy to indexed selection in the caller's transaction.
// Bounded provenance subplans otherwise incur large LLVM compilation costs.
const indexedSearchPlanningSQL = "SET LOCAL jit=off"

// Keep the shared source-eligible set free of scoring/normalization work: it
// also supplies query-independent conflict and provenance evidence. Compute
// relevance only for matching candidates, with statement-local query parsing.
// Totals count distinct matching identities without constructing scores, sort
// keys or conflict aggregates. Keep scoring in the page-only members CTE:
// shared CTEs can retain unused projections even in count plans.
// Page metadata is calculated only after LIMIT; joining an
// all-identity aggregate into ranking caused repeated full intermediate scans.
// Inline the shared projections: materializing their wide rows spills even
// unused ranking vectors during counts and makes each page-metadata lookup
// rescan an unindexed temporary set instead of using the identity index.
// Identical members need no preliminary DISTINCT: row_number already selects
// one winner per identity, while conflict/provenance reads retain every source.
// Parse current approved source scopes once per statement, not once per
// observation (or page-metadata lookup). Only source metadata is materialized;
// observation projections remain inline and indexable.
const indexedApprovedSourcesCTE = `
WITH approved_sources AS MATERIALIZED (
  SELECT source.tenant_id,source.id,
         coalesce(revision.configuration#>'{scope,excludedIdentifiers}','[]'::jsonb) AS excluded_identifiers,
         CASE WHEN jsonb_typeof(revision.configuration#>'{scope,identifiers}')='array'
           THEN revision.configuration#>'{scope,identifiers}' ELSE '[]'::jsonb END AS identifiers,
         CASE WHEN jsonb_typeof(revision.configuration#>'{scope,versions}')='array'
           THEN revision.configuration#>'{scope,versions}' ELSE '[]'::jsonb END AS versions,
         CASE WHEN jsonb_typeof(revision.configuration#>'{scope,kinds}')='array'
           THEN revision.configuration#>'{scope,kinds}' ELSE '[]'::jsonb END AS kinds,
         CASE WHEN jsonb_typeof(revision.configuration#>'{scope,publishers}')='array'
           THEN revision.configuration#>'{scope,publishers}' ELSE '[]'::jsonb END AS publishers,
         CASE WHEN jsonb_typeof(revision.configuration#>'{scope,tags}')='array'
           THEN revision.configuration#>'{scope,tags}' ELSE '[]'::jsonb END AS tags
    FROM federation_sources source
    JOIN federation_source_revisions revision ON revision.tenant_id=source.tenant_id AND revision.source_id=source.id AND revision.revision=source.revision
   WHERE source.tenant_id=$1 AND source.status IN ('active','error') AND revision.retrieval_mode IN ('indexed','hybrid')
)`

// indexedCurrentEligibleFromSQL is shared by reader matching and worker metadata
// enumeration. Workers select only metadata, not API-only ranking projections.
var indexedCurrentEligibleFromSQL = `
    FROM observations observation
    JOIN approved_sources source ON source.tenant_id=observation.tenant_id AND source.id=observation.source_id
   WHERE observation.tenant_id=$1 AND ` + collectionPermissionSQL("federation.discover", "") + ` AND observation.freshness='current'
     AND observation.normalized_entry_digest IS NOT NULL AND observation.search_vector IS NOT NULL
     AND observation.completeness='complete' AND observation.compatibility->>'structural'='valid'
     AND observation.compatibility->>'support'='supported' AND observation.kind IN ('agent','mcp_server','skill','plugin')
     AND octet_length(observation.search_display_name)<=512
     AND octet_length(observation.search_description)<=4000
     AND octet_length(observation.search_publisher)<=256
     AND cardinality(observation.search_tags)<=100
     AND NOT EXISTS (SELECT 1 FROM unnest(observation.search_tags) search_tag WHERE char_length(search_tag)>80)
     AND cardinality(observation.search_representative_queries)<=5
     AND NOT EXISTS (SELECT 1 FROM unnest(observation.search_representative_queries) search_query WHERE octet_length(search_query)>240)
     AND NOT (source.excluded_identifiers @> jsonb_build_array(observation.identifier))
     AND (source.identifiers='[]'::jsonb OR source.identifiers @> jsonb_build_array(observation.identifier))
     AND (source.versions='[]'::jsonb OR source.versions @> jsonb_build_array(coalesce(observation.version,'')))
     AND (source.kinds='[]'::jsonb OR source.kinds @> jsonb_build_array(observation.kind))
     AND (source.publishers='[]'::jsonb OR source.publishers @> jsonb_build_array(observation.search_publisher))
     AND (source.tags='[]'::jsonb OR to_jsonb(observation.search_tags) @> source.tags)
     AND NOT (observation.version_present AND EXISTS (
       SELECT 1
         FROM resources local_resource
         JOIN catalog_listings local_listing
           ON local_listing.tenant_id=local_resource.tenant_id AND local_listing.resource_id=local_resource.id
         JOIN listing_releases local_release
           ON local_release.tenant_id=local_listing.tenant_id AND local_release.id=local_listing.current_release_id
          AND local_release.state IN ('published','deprecated')
         JOIN resource_version_revisions local_revision
           ON local_revision.tenant_id=local_release.tenant_id AND local_revision.id=local_release.version_revision_id
         JOIN resource_versions local_version
           ON local_version.tenant_id=local_revision.tenant_id AND local_version.id=local_revision.resource_version_id
        WHERE local_resource.tenant_id=observation.tenant_id
          AND local_resource.deleted_at IS NULL AND ` + collectionPermissionSQL("resource.read", "local_listing.namespace_id") + `
          AND local_resource.canonical_identifier=observation.identifier
          AND local_version.version=observation.version
     ))
`

var indexedCatalogSearchRecordsCTE = indexedApprovedSourcesCTE + `, current_eligible AS NOT MATERIALIZED (
  SELECT observation.identifier,coalesce(observation.version,'') AS version,observation.version_present,
         observation.search_identifier_vector,observation.search_display_vector,observation.search_description_vector,
         observation.search_tags_vector,observation.search_queries_vector,
         observation.kind,observation.normalized_entry_digest,coalesce(nullif(observation.search_display_name,''),observation.identifier) AS search_display_name,
         observation.search_description,observation.search_tags,observation.search_representative_queries,
         observation.search_publisher,observation.search_vector,observation.source_id,observation.search_license,observation.search_categories
` + indexedCurrentEligibleFromSQL + `), eligible AS NOT MATERIALIZED (
  SELECT identifier,version,version_present,kind,normalized_entry_digest,search_display_name,
         search_identifier_vector,search_display_vector,search_description_vector,search_tags_vector,search_queries_vector,
         search_description,search_tags,search_representative_queries,search_publisher,search_vector,
         source_id
    FROM current_eligible
   WHERE (coalesce(cardinality($4::text[]),0)=0 OR kind=ANY($4::text[]))
     AND (coalesce(cardinality($5::text[]),0)=0 OR search_tags @> $5::text[])
     AND ($6='' OR search_publisher=$6)
     AND ($7='' OR search_license=$7)
     AND (coalesce(cardinality($8::text[]),0)=0 OR search_categories @> $8::text[])
     AND ($2='' OR ($3<>'' AND identifier=$3) OR search_vector @@ (SELECT catalog_core_lexical_query_v1($2)))
), members AS (
  SELECT identifier,version,version_present,kind,normalized_entry_digest,search_display_name,search_description,
         search_tags,search_representative_queries,search_publisher,
         catalog_core_sort_name_v1(coalesce(nullif(search_display_name,''),identifier)) COLLATE "C" AS sort_name,
         'indexed:v1:sha256:' || encode(digest(
           convert_to('catalog-search-result-v1','UTF8') || decode('00','hex') ||
           convert_to(identifier,'UTF8') || decode('ff','hex') ||
           convert_to(coalesce(version,''),'UTF8') || decode('ff','hex') ||
           CASE WHEN version_present THEN decode('01','hex') ELSE decode('00','hex') END || decode('ff','hex'),'sha256'),'hex') AS result_id,
         CASE
           WHEN $2='' THEN 0
           WHEN $3<>'' AND identifier=$3 THEN 100
           ELSE least(99,40
             + CASE WHEN search_display_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 20 ELSE 0 END
             + CASE WHEN search_queries_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 15 ELSE 0 END
             + CASE WHEN search_description_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 10 ELSE 0 END
             + CASE WHEN search_tags_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 5 ELSE 0 END
             + CASE WHEN search_identifier_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 5 ELSE 0 END)
         END::integer AS score
    FROM eligible
), selected_identities AS (
  SELECT DISTINCT identifier,version,version_present FROM eligible
), ranked_members AS (
  SELECT member.identifier,member.version,member.version_present,member.kind,member.normalized_entry_digest,
         member.search_display_name,member.search_description,member.search_tags,member.search_publisher,
         member.sort_name,member.result_id,member.score,
         row_number() OVER (
           PARTITION BY member.identifier,member.version,member.version_present
           ORDER BY member.score DESC,member.sort_name COLLATE "C",member.normalized_entry_digest
         ) AS member_rank
    FROM members member
) `

const indexedCatalogSearchPageSQL = `
, selected AS (
  SELECT member.identifier,member.version,member.version_present,member.kind,member.normalized_entry_digest,
         member.search_display_name,member.search_description,member.search_tags,member.search_publisher,
         member.sort_name,member.result_id,member.score
    FROM ranked_members member
   WHERE member.member_rank=1
     AND ($9=false OR score<$10 OR (score=$10 AND (sort_name>$11 COLLATE "C" OR (sort_name=$11 COLLATE "C" AND result_id>$12))))
   ORDER BY score DESC,sort_name COLLATE "C",result_id
   LIMIT $13
)
SELECT selected.identifier,selected.version,selected.version_present,selected.kind,selected.normalized_entry_digest,
       selected.search_display_name,selected.search_description,selected.search_tags,selected.search_publisher,
       selected.result_id,selected.score,
       (SELECT count(DISTINCT conflict.normalized_entry_digest)>1 FROM current_eligible conflict
         WHERE conflict.identifier=selected.identifier AND conflict.version_present=selected.version_present
           AND conflict.version=selected.version) AS conflict,
       EXISTS (
         SELECT 1 FROM current_eligible conflict
          WHERE conflict.identifier=selected.identifier AND conflict.version_present=selected.version_present
            AND conflict.version=selected.version
          GROUP BY conflict.source_id HAVING count(DISTINCT conflict.normalized_entry_digest)>1
       ) AS same_source_conflict,
       (SELECT count(DISTINCT contribution.source_id)::integer FROM current_eligible contribution
         WHERE contribution.identifier=selected.identifier AND contribution.version_present=selected.version_present
           AND contribution.version=selected.version AND contribution.normalized_entry_digest=selected.normalized_entry_digest) AS source_count,
       ARRAY(SELECT DISTINCT contribution.source_id::text FROM current_eligible contribution
         WHERE contribution.identifier=selected.identifier AND contribution.version_present=selected.version_present
           AND contribution.version=selected.version AND contribution.normalized_entry_digest=selected.normalized_entry_digest
        ORDER BY contribution.source_id::text LIMIT 16) AS source_ids,
       (SELECT min(contribution.source_id::text) FROM current_eligible contribution WHERE contribution.identifier=selected.identifier AND contribution.version_present=selected.version_present AND contribution.version=selected.version AND contribution.normalized_entry_digest=selected.normalized_entry_digest) AS presentation_source_id,
       (SELECT coalesce((source.configuration->>'presentationGeneration')::bigint,1) FROM federation_sources source WHERE source.tenant_id=$1 AND source.id=(SELECT min(contribution.source_id::text)::uuid FROM current_eligible contribution WHERE contribution.identifier=selected.identifier AND contribution.version_present=selected.version_present AND contribution.version=selected.version AND contribution.normalized_entry_digest=selected.normalized_entry_digest)) AS presentation_generation,
       (SELECT source.revision FROM federation_sources source WHERE source.tenant_id=$1 AND source.id=(SELECT min(contribution.source_id::text)::uuid FROM current_eligible contribution WHERE contribution.identifier=selected.identifier AND contribution.version_present=selected.version_present AND contribution.version=selected.version AND contribution.normalized_entry_digest=selected.normalized_entry_digest)) AS presentation_source_revision
  FROM selected
 ORDER BY selected.score DESC,selected.sort_name COLLATE "C",selected.result_id`
