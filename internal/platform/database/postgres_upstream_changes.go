package database

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// PostgreSQL btrim's default only removes ASCII spaces. These are the Unicode
// White_Space characters used by strings.TrimSpace in Version authoring.
const versionWhitespace = "\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"

// Use exact history and membership indexes. Each lateral selects one proof or
// occurrence before paging. Only scalar evidence from the selected page leaves SQL.
var upstreamChangesSQL = indexedApprovedSourcesCTE + `, candidates AS (
 SELECT r.id AS resource,r.namespace_id,observation.id,observation.identifier,observation.kind,observation.media_type,
 observation.version,observation.normalization_profile,observation.source_id,proof.rev,proof.label,proof.p,
 occ.snapshot_id,occ.source_revision,occ.retrieval_digest,occ.entry_location,occ.exact_entry_digest,occ.normalized_entry_digest,
 occ.normalization_input_digest,occ.evidence_complete,occ.observed_at,ss.completed_at,
 CASE WHEN occ.observation_id IS NULL OR proof.original_id IS NULL OR proof.original_source<>observation.source_id OR proof.original_kind<>observation.kind
 OR proof.original_identifier<>observation.identifier OR proof.original_digest IS DISTINCT FROM proof.p->>'normalizedEntryDigest'
 OR proof.original_profile IS DISTINCT FROM proof.p->>'normalizationProfile'
 OR proof.original_exact IS DISTINCT FROM proof.p->>'exactEntryDigest' OR prior_occ.observation_id IS NULL
 OR NOT prior_occ.evidence_complete OR prior_occ.source_revision::text IS DISTINCT FROM proof.p->>'sourceRevision'
 OR prior_occ.exact_entry_digest IS DISTINCT FROM proof.original_exact OR prior_occ.normalized_entry_digest IS DISTINCT FROM proof.original_digest
 OR occ.observed_at>ss.completed_at OR NOT occ.evidence_complete OR occ.exact_entry_digest IS DISTINCT FROM observation.exact_entry_digest
 OR occ.normalized_entry_digest IS DISTINCT FROM observation.normalized_entry_digest OR observation.normalized_entry_digest IS NULL
 OR ss.state<>'complete' OR NOT ss.enumeration_complete OR occ.source_revision IS DISTINCT FROM ss.source_revision
 THEN 'invalid'
 WHEN proof.original_version IS DISTINCT FROM observation.version THEN 'unadopted_version'
 WHEN proof.original_profile IS DISTINCT FROM observation.normalization_profile OR observation.normalization_profile<>'catalog-observation-v1' THEN 'comparison_unavailable'
 WHEN proof.original_digest IS DISTINCT FROM observation.normalized_entry_digest THEN 'changed_entry' ELSE 'equal' END AS outcome
 FROM observations observation JOIN approved_sources source ON source.id=observation.source_id AND source.tenant_id=observation.tenant_id
 JOIN federation_sources fs ON fs.id=source.id AND fs.tenant_id=source.tenant_id
 JOIN resources r ON r.tenant_id=source.tenant_id AND r.canonical_identifier=observation.identifier AND r.kind=observation.kind AND r.deleted_at IS NULL
 JOIN source_snapshots ss ON ss.id=fs.last_complete_snapshot_id AND ss.tenant_id=fs.tenant_id AND ss.source_id=fs.id
 LEFT JOIN LATERAL (SELECT c.observation_id,c.snapshot_id,c.source_revision,c.retrieval_digest,c.entry_location,c.exact_entry_digest,c.normalized_entry_digest,
                      c.normalization_input_digest,c.evidence_complete,c.observed_at
               FROM observation_occurrences c WHERE c.tenant_id=observation.tenant_id AND c.source_id=source.id AND c.snapshot_id=ss.id AND c.observation_id=observation.id
 ORDER BY c.entry_location COLLATE "C",c.retrieval_digest COLLATE "C" LIMIT 1) occ ON true
 JOIN LATERAL (
 SELECT prior.id AS rev,v.version AS label,prior.adoption_provenance AS p,original.id AS original_id,original.source_id AS original_source,
 original.kind AS original_kind,original.identifier AS original_identifier,original.normalized_entry_digest AS original_digest,original.normalization_profile AS original_profile,original.exact_entry_digest AS original_exact,original.version AS original_version
 FROM resource_versions v JOIN resource_version_revisions prior ON prior.tenant_id=v.tenant_id AND prior.resource_version_id=v.id
 LEFT JOIN observations original ON original.tenant_id=prior.tenant_id AND original.id=(prior.adoption_provenance->>'observationId')::uuid
 WHERE v.tenant_id=r.tenant_id AND v.resource_id=r.id AND prior.adoption_provenance IS NOT NULL AND prior.adoption_provenance->>'sourceId'=source.id::text
 AND (original.id IS NULL OR (original.version_present AND btrim(original.version,$8)<>''
 AND (original.version=observation.version OR (btrim(original.version,$8)<>btrim(observation.version,$8)
 AND NOT EXISTS(SELECT 1 FROM resource_versions existing WHERE existing.tenant_id=r.tenant_id AND existing.resource_id=r.id AND existing.version=btrim(observation.version,$8))))))
 ORDER BY (original.id IS NULL) DESC,(original.version=observation.version) DESC,prior.id LIMIT 1
 ) proof ON true
 LEFT JOIN observation_occurrences prior_occ ON prior_occ.tenant_id=r.tenant_id AND prior_occ.source_id=source.id AND prior_occ.observation_id=proof.original_id
 AND prior_occ.snapshot_id=(proof.p->>'snapshotId')::uuid AND prior_occ.entry_location=proof.p->>'entryLocation' AND prior_occ.retrieval_digest=proof.p->>'retrievalDigest'
 WHERE observation.tenant_id=$1 AND ` + collectionPermissionSQL("source.read_evidence", "") + ` AND ` + collectionPermissionSQL("resource.edit", "r.namespace_id") + `
 AND ($2::text='' OR source.id=nullif($2,'')::uuid) AND ($3::text='' OR r.namespace_id=nullif($3,'')::uuid)
 AND ($4::text='' OR observation.id=nullif($4,'')::uuid) AND ($5::text='' OR observation.id>nullif($5,'')::uuid)
 AND (occ.observation_id IS NOT NULL OR observation.snapshot_id=ss.id)
 AND observation.freshness='current' AND observation.completeness='complete' AND observation.version_present AND btrim(observation.version,$8)<>''
 AND observation.compatibility->>'structural'='valid' AND observation.compatibility->>'support'='supported'
 ` + indexedObservationScopeSQL + `
 AND NOT EXISTS (SELECT 1 FROM observations other WHERE other.tenant_id=observation.tenant_id AND other.source_id=source.id
 AND other.identifier=observation.identifier AND other.version_present AND other.version=observation.version AND other.freshness='current' AND other.normalized_entry_digest<>observation.normalized_entry_digest)
 ), page AS (
 SELECT resource,namespace_id,rev,label,version,identifier,kind,media_type,outcome,id,source_id,snapshot_id,source_revision,
        retrieval_digest,entry_location,exact_entry_digest,normalized_entry_digest,normalization_input_digest,
        evidence_complete,observed_at,p,normalization_profile,completed_at
 FROM candidates WHERE outcome<>'equal' AND (outcome='invalid' OR $6='all' OR ($6='entry_comparison' AND outcome<>'unadopted_version') OR $6=outcome) ORDER BY id LIMIT $7)
 SELECT page.resource,page.namespace_id,page.rev,page.label,page.version,page.identifier,page.kind,page.media_type,page.outcome,
 page.id,page.source_id,page.snapshot_id,page.source_revision,page.retrieval_digest,page.entry_location,page.exact_entry_digest,coalesce(page.normalized_entry_digest,''),coalesce(page.normalization_input_digest,''),page.evidence_complete,page.observed_at,
 coalesce(page.p->>'normalizedEntryDigest',''),coalesce(page.p->>'normalizationProfile',''),coalesce(page.normalization_profile,''),page.completed_at,latest.id::text,source.revision,latest.state,latest.completed_at
 FROM page JOIN federation_sources source ON source.tenant_id=$1 AND source.id=page.source_id ` + sourceLatestCompletedJoinSQL + ` ORDER BY page.id`

func (p *PostgresFederationStore) ListUpstreamChanges(ctx context.Context, tenant string, q federation.UpstreamChangesQuery) ([]federation.UpstreamChange, error) {
	return withTenantCollectionRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) ([]federation.UpstreamChange, error) {
		rows, err := tx.Query(ctx, upstreamChangesSQL, tenant, q.SourceID, q.NamespaceID, q.ObservationID, q.AfterID, q.Check, q.Limit+1, versionWhitespace)
		if err != nil {
			return nil, translateError("select upstream changes", err)
		}
		defer rows.Close()
		out := []federation.UpstreamChange{}
		for rows.Next() {
			var row federation.UpstreamChange
			o := &row.Occurrence
			t := &row.Target
			var attemptID, state *string
			var completed *time.Time
			var revision int64
			if err := rows.Scan(
				&t.ResourceID, &t.NamespaceID, &t.PriorVersionRevisionID, &t.Version, &t.UpstreamVersion,
				&row.Identifier, &row.Kind, &row.MediaType, &row.Outcome,
				&o.ObservationID, &o.SourceID, &o.SnapshotID, &o.SourceRevision, &o.RetrievalDigest,
				&o.EntryLocation, &o.ExactEntryDigest, &o.NormalizedEntryDigest, &o.NormalizationInputDigest,
				&o.EvidenceComplete, &o.ObservedAt, &row.PriorDigest, &row.PriorProfile, &row.CurrentProfile,
				&row.SnapshotCompletedAt, &attemptID, &revision, &state, &completed,
			); err != nil {
				return nil, translateError("read upstream change evidence", err)
			}
			if row.Outcome == "invalid" {
				return nil, catalog.E(catalog.CodeDependencyUnavailable, "retained upstream change evidence is inconsistent")
			}
			if err := row.Validate(); err != nil {
				return nil, err
			}
			if attemptID != nil {
				row.LatestAttempt = &federation.SourceSyncAttempt{SnapshotID: *attemptID, Revision: revision, State: federation.SnapshotState(*state), CompletedAt: *completed}
			}
			out = append(out, row)
		}
		return out, translateError("list upstream changes", rows.Err())
	})
}
