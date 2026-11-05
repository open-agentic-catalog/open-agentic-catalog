// Hybrid SQL matches complete filtered populations before global rank fusion.
// Only the requested page is hydrated; observation identity is deduplicated
// before either rank is calculated, so extra sources cannot amplify relevance.
package database

import "strings"

// semanticLexicalScoreSQL uses the same field weights as core Search, with zero
// for nonmatching documents. Such records may still match independently by cosine.
func semanticLexicalScoreSQL(identifier, document, prefix string) string {
	return `CASE WHEN $3<>'' AND ` + identifier + `=$3 THEN 100
 WHEN NOT (` + document + ` @@ (SELECT catalog_core_lexical_query_v1($2))) THEN 0
 ELSE least(99,40
 + CASE WHEN ` + prefix + `display_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 20 ELSE 0 END
 + CASE WHEN ` + prefix + `queries_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 15 ELSE 0 END
 + CASE WHEN ` + prefix + `description_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 10 ELSE 0 END
 + CASE WHEN ` + prefix + `tags_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 5 ELSE 0 END
 + CASE WHEN ` + prefix + `identifier_vector @@ (SELECT catalog_core_lexical_query_v1($2)) THEN 5 ELSE 0 END) END`
}

var semanticSearchInputsSQL = catalogSearchBaseCTE + "," + strings.TrimPrefix(strings.TrimSpace(indexedApprovedSourcesCTE), "WITH ") + `,
 current_eligible AS NOT MATERIALIZED (
 SELECT observation.id,observation.identifier,observation.version,observation.version_present,observation.kind,
 observation.normalized_entry_digest,observation.search_display_name,observation.search_description,observation.search_tags,
 observation.search_publisher,observation.search_vector,observation.source_id,observation.search_license,observation.search_categories,
 observation.search_identifier_vector,observation.search_display_vector,observation.search_description_vector,
 observation.search_tags_vector,observation.search_queries_vector ` + strings.Replace(indexedCurrentEligibleFromSQL, "WHERE observation.tenant_id=$1", "WHERE $20 AND observation.tenant_id=$1", 1) + `
 ), candidates AS (
 SELECT result_id AS item_key,result_id,release_id::text,canonical_identifier AS identifier,
 ''::text AS version,false AS version_present,''::text AS normalized_entry_digest,kind,
 display_name,description,tags,publisher_id::text AS publisher,sort_name,
 ` + semanticLexicalScoreSQL("canonical_identifier", "document_vector", "") + `::integer AS lexical_score
 FROM records WHERE $19 AND ($18='' OR namespace_id::text=$18) AND (coalesce(cardinality($4::text[]),0)=0 OR kind=ANY($4::text[]))
 AND (coalesce(cardinality($5::text[]),0)=0 OR tags @> $5::text[])
 AND ($6='' OR publisher_id::text=$6) AND ($7='' OR visibility=$7)
 AND ($21='' OR license=$21) AND (coalesce(cardinality($22::text[]),0)=0 OR categories @> $22::text[])
 UNION ALL
 SELECT 'observation:'||id::text,
 'indexed:v1:sha256:'||encode(digest(convert_to('catalog-search-result-v1','UTF8')||decode('00','hex')||convert_to(identifier,'UTF8')||decode('ff','hex')||convert_to(coalesce(version,''),'UTF8')||decode('ff','hex')||CASE WHEN version_present THEN decode('01','hex') ELSE decode('00','hex') END||decode('ff','hex'),'sha256'),'hex'),
 ''::text,identifier,coalesce(version,''),version_present,normalized_entry_digest,kind,
 coalesce(nullif(search_display_name,''),identifier),search_description,search_tags,search_publisher,
 catalog_core_sort_name_v1(coalesce(nullif(search_display_name,''),identifier)) COLLATE "C",
 ` + semanticLexicalScoreSQL("identifier", "search_vector", "search_") + `::integer
 FROM current_eligible WHERE $20 AND NOT $12 AND ($7='' OR $7='tenant')
 AND (coalesce(cardinality($4::text[]),0)=0 OR kind=ANY($4::text[]))
 AND (coalesce(cardinality($5::text[]),0)=0 OR search_tags @> $5::text[])
 AND ($6='' OR search_publisher=$6)
 AND ($21='' OR search_license=$21) AND (coalesce(cardinality($22::text[]),0)=0 OR search_categories @> $22::text[])
 )`

// The exact-match ceiling is checked, never used to return a truncated success.
// It bounds ranking memory independently of the unfiltered corpus size.
const semanticSearchRanksSQL = `,
 compared AS NOT MATERIALIZED (
 SELECT candidate.*, CASE WHEN vector.input_digest IS NOT NULL THEN
  1-@cosine@(vector.embedding,$10::real[]::@vector@) ELSE NULL END AS similarity
 FROM candidates candidate
 LEFT JOIN semantic_members member ON member.tenant_id=$1 AND member.generation_id=$8 AND member.profile=$9 AND member.item_key=candidate.item_key
 LEFT JOIN semantic_vectors vector ON vector.tenant_id=member.tenant_id AND vector.profile=member.profile
   AND vector.disclosure=member.disclosure AND vector.input_digest=member.input_digest AND vector.dimensions=member.dimensions
 ), matching AS MATERIALIZED (
 SELECT * FROM compared WHERE lexical_score>0 OR similarity>=$11
 ), identities AS MATERIALIZED (
 SELECT result_id,max(lexical_score) AS lexical_score,max(similarity) AS similarity,
 (array_agg(sort_name ORDER BY lexical_score DESC,similarity DESC NULLS LAST,sort_name COLLATE "C",normalized_entry_digest,item_key))[1] AS sort_name
 FROM matching GROUP BY result_id LIMIT 100001
 ), lexical AS (
 SELECT result_id,row_number() OVER (ORDER BY lexical_score DESC,sort_name COLLATE "C",result_id) AS rank
 FROM identities WHERE lexical_score>0
 ), semantic AS (
 SELECT result_id,row_number() OVER (ORDER BY similarity DESC,sort_name COLLATE "C",result_id) AS rank
 FROM identities WHERE similarity>=$11
 ), ranks AS MATERIALIZED (
 SELECT identities.result_id,identities.sort_name,
 CASE WHEN identities.lexical_score=100 THEN 1000000000000::bigint
 ELSE round((coalesce(1.0/(60+lexical.rank),0)+coalesce(1.0/(60+semantic.rank),0))*1000000000000)::bigint END AS rank,
 CASE WHEN identities.lexical_score=100 THEN 100 ELSE least(99,greatest(1,round((coalesce(1.0/(60+lexical.rank),0)+coalesce(1.0/(60+semantic.rank),0))*3050)::integer)) END AS score
 FROM identities LEFT JOIN lexical USING(result_id) LEFT JOIN semantic USING(result_id)
 ), selected AS (
 SELECT ranks.*,winner.item_key,winner.release_id,winner.identifier,winner.version,winner.version_present,winner.kind,
 winner.normalized_entry_digest,winner.display_name,winner.description,winner.tags,winner.publisher
 FROM (SELECT * FROM ranks WHERE NOT $13 OR rank<$14 OR (rank=$14 AND (sort_name COLLATE "C",result_id)>($15 COLLATE "C",$16))
 ORDER BY rank DESC,sort_name COLLATE "C",result_id LIMIT $17) ranks
 CROSS JOIN LATERAL (SELECT * FROM matching WHERE matching.result_id=ranks.result_id
 ORDER BY lexical_score DESC,similarity DESC NULLS LAST,sort_name COLLATE "C",normalized_entry_digest,item_key LIMIT 1) winner
 )
SELECT totals.total,selected.result_id,selected.release_id,selected.identifier,selected.version,selected.version_present,selected.kind,
 selected.normalized_entry_digest,selected.display_name,selected.description,selected.tags,selected.publisher,
 selected.rank,selected.score,selected.sort_name,
 (SELECT count(DISTINCT conflict.normalized_entry_digest)>1 FROM current_eligible conflict WHERE conflict.identifier=selected.identifier AND coalesce(conflict.version,'')=selected.version AND conflict.version_present=selected.version_present),
 EXISTS(SELECT 1 FROM current_eligible conflict WHERE conflict.identifier=selected.identifier AND coalesce(conflict.version,'')=selected.version AND conflict.version_present=selected.version_present GROUP BY source_id HAVING count(DISTINCT normalized_entry_digest)>1),
 (SELECT count(DISTINCT source_id)::integer FROM current_eligible contribution WHERE contribution.identifier=selected.identifier AND coalesce(contribution.version,'')=selected.version AND contribution.version_present=selected.version_present AND contribution.normalized_entry_digest=selected.normalized_entry_digest),
 ARRAY(SELECT DISTINCT source_id::text FROM current_eligible contribution WHERE contribution.identifier=selected.identifier AND coalesce(contribution.version,'')=selected.version AND contribution.version_present=selected.version_present AND contribution.normalized_entry_digest=selected.normalized_entry_digest ORDER BY source_id::text LIMIT 16),
 coalesce(presentation.source_id,''),coalesce(presentation.generation,0),coalesce(presentation.revision,0)
 FROM (SELECT count(*) AS total FROM identities) totals LEFT JOIN selected ON true
 LEFT JOIN LATERAL (
 SELECT source.id::text AS source_id,coalesce((source.configuration->>'presentationGeneration')::bigint,1) AS generation,source.revision
 FROM current_eligible contribution JOIN federation_sources source ON source.tenant_id=$1 AND source.id=contribution.source_id
 WHERE contribution.identifier=selected.identifier AND coalesce(contribution.version,'')=selected.version
 AND contribution.version_present=selected.version_present AND contribution.normalized_entry_digest=selected.normalized_entry_digest
 ORDER BY source.id LIMIT 1
 ) presentation ON selected.release_id=''
 ORDER BY selected.rank DESC,selected.sort_name COLLATE "C",selected.result_id`
