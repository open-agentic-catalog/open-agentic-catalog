// Historical comparison reads compact occurrence-bound membership under one
// tenant read transaction. Native documents and current freshness are excluded.
package database

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// comparisonMembersSQL limits each occurrence input before joining observations.
// The primary key begins with tenant/snapshot, so historical selection does not
// scan an observation's entire history or depend on its creation snapshot.
const comparisonMembersSQL = `WITH selected AS MATERIALIZED (
  (SELECT 0 AS side,c.* FROM observation_occurrences c WHERE tenant_id=$1 AND snapshot_id=$3 LIMIT $5)
  UNION ALL
  (SELECT 1 AS side,c.* FROM observation_occurrences c WHERE tenant_id=$1 AND snapshot_id=$4 LIMIT $5)
), members AS MATERIALIZED (
  SELECT c.side,o.source_key,o.id AS observation_id,coalesce(o.identifier,'') AS identifier,
    coalesce(o.version,'') AS version,o.version_present,coalesce(o.kind,'') AS kind,
    c.source_id,c.snapshot_id,c.source_revision,c.evidence_complete,c.observed_at,
    c.retrieval_digest,c.entry_location,
    c.exact_entry_digest,coalesce(c.normalized_entry_digest,'') AS normalized_entry_digest,
    coalesce(c.normalization_input_digest,'') AS normalization_input_digest,o.identity_digest,
    (o.id IS NULL OR c.source_id<>$2 OR c.source_revision IS DISTINCT FROM s.source_revision OR c.evidence_complete IS DISTINCT FROM true
      OR c.exact_entry_digest IS DISTINCT FROM o.exact_entry_digest
      OR c.normalized_entry_digest IS DISTINCT FROM o.normalized_entry_digest
      OR c.observed_at<s.started_at OR c.observed_at>s.completed_at
      OR c.observed_at<o.first_seen_at OR c.observed_at>o.last_seen_at
      OR o.identity_digest IS DISTINCT FROM CASE WHEN coalesce(c.normalization_input_digest,'')=''
        THEN c.exact_entry_digest ELSE 'sha256:'||encode(sha256(
          convert_to('catalog-observation-identity-v1','UTF8')||decode('00','hex')||
          convert_to(c.exact_entry_digest,'UTF8')||decode('00','hex')||
          convert_to(c.normalization_input_digest,'UTF8')),'hex') END) AS invalid,
    (octet_length(o.source_key)+octet_length(o.id::text)+octet_length(coalesce(o.identifier,''))+octet_length(coalesce(o.version,''))+
      octet_length(coalesce(o.kind,''))+octet_length(o.identity_digest)+octet_length(c.observation_id::text)+
      octet_length(c.source_id::text)+octet_length(c.snapshot_id::text)+octet_length(c.retrieval_digest)+
      octet_length(c.entry_location)+octet_length(c.exact_entry_digest)+
      octet_length(coalesce(c.normalized_entry_digest,''))+octet_length(coalesce(c.normalization_input_digest,''))) AS text_bytes
  FROM selected c
  LEFT JOIN LATERAL (
    SELECT id,source_key,identifier,version,version_present,kind,identity_digest,
      exact_entry_digest,normalized_entry_digest,first_seen_at,last_seen_at
    FROM observations WHERE tenant_id=c.tenant_id AND source_id=c.source_id AND id=c.observation_id LIMIT 1
  ) o ON true
  LEFT JOIN LATERAL (
    SELECT source_revision,started_at,completed_at FROM source_snapshots
    WHERE tenant_id=c.tenant_id AND source_id=c.source_id AND id=c.snapshot_id LIMIT 1
  ) s ON true
) `

// CompareSnapshots validates complete membership and text/work budgets before
// SQL pairs unique keys and selects limit+1 compact output rows. Duplicate keys
// use separate occurrence rows, avoiding a quadratic join or arbitrary winner.
func (p *PostgresFederationStore) CompareSnapshots(ctx context.Context, tenantID string, request federation.SnapshotComparisonRequest) (federation.SnapshotComparison, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (federation.SnapshotComparison, error) {
		read := func(id string) (federation.Snapshot, error) {
			var s federation.Snapshot
			err := tx.QueryRow(ctx, `SELECT id,tenant_id,source_id,source_revision,state,enumeration_complete,
              withdrawal_authority,member_count,started_at,completed_at
            FROM source_snapshots WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(
				&s.ID, &s.TenantID, &s.SourceID, &s.SourceRevision, &s.State, &s.EnumerationComplete,
				&s.WithdrawalAuthority, &s.MemberCount, &s.StartedAt, &s.CompletedAt)
			return s, translateError("read comparison snapshot", err)
		}
		before, err := read(request.BeforeID)
		if err != nil {
			return federation.SnapshotComparison{}, err
		}
		after, err := read(request.AfterID)
		if err != nil {
			return federation.SnapshotComparison{}, err
		}
		comparison, err := federation.NewSnapshotComparison(before, after, request)
		if err != nil {
			return federation.SnapshotComparison{}, err
		}
		args := []any{tenantID, comparison.SourceID, request.BeforeID, request.AfterID, federation.MaxSynchronizationEntries + 1}
		var beforeCount, afterCount int
		var textBytes int64
		var invalid bool
		err = tx.QueryRow(ctx, comparisonMembersSQL+`SELECT count(*) FILTER (WHERE side=0),count(*) FILTER (WHERE side=1),
          coalesce(sum(text_bytes),0),coalesce(bool_or(invalid),false) FROM members`, args...).Scan(&beforeCount, &afterCount, &textBytes, &invalid)
		if err != nil {
			return federation.SnapshotComparison{}, translateError("validate comparison membership", err)
		}
		if beforeCount > federation.MaxSynchronizationEntries || afterCount > federation.MaxSynchronizationEntries || textBytes > federation.MaxSnapshotComparisonTextBytes {
			return federation.SnapshotComparison{}, catalog.E(catalog.CodePreconditionFailed, "source snapshot comparison exceeds the supported membership work limit")
		}
		if invalid || beforeCount != before.MemberCount || afterCount != after.MemberCount {
			return federation.SnapshotComparison{}, catalog.E(catalog.CodeDependencyUnavailable, "retained source membership evidence is unavailable")
		}
		rows, err := tx.Query(ctx, comparisonMembersSQL+`, counts AS MATERIALIZED (
          SELECT source_key,count(*) FILTER (WHERE side=0)::integer AS before_count,
            count(*) FILTER (WHERE side=1)::integer AS after_count
          FROM members GROUP BY source_key
        ), unique_members AS MATERIALIZED (
          SELECT m.* FROM members m JOIN counts c USING(source_key) WHERE c.before_count<=1 AND c.after_count<=1
        ), changes AS (
          SELECT coalesce(b.source_key,a.source_key) AS source_key,0 AS side,''::text AS retrieval_digest,''::text AS entry_location,
            CASE WHEN b.source_key IS NULL THEN 'added' WHEN a.source_key IS NULL THEN 'absent'
              WHEN (b.exact_entry_digest,b.normalized_entry_digest,b.normalization_input_digest,b.identity_digest)=
                (a.exact_entry_digest,a.normalized_entry_digest,a.normalization_input_digest,a.identity_digest)
              THEN 'unchanged' ELSE 'changed' END AS change,
            b.retrieval_digest AS before_retrieval,b.entry_location AS before_location,
            a.retrieval_digest AS after_retrieval,a.entry_location AS after_location
          FROM (SELECT * FROM unique_members WHERE side=0) b FULL JOIN
            (SELECT * FROM unique_members WHERE side=1) a ON a.source_key=b.source_key
          UNION ALL
          SELECT m.source_key,m.side,m.retrieval_digest,m.entry_location,'multiple',
            CASE WHEN m.side=0 THEN m.retrieval_digest END,CASE WHEN m.side=0 THEN m.entry_location END,
            CASE WHEN m.side=1 THEN m.retrieval_digest END,CASE WHEN m.side=1 THEN m.entry_location END
          FROM members m JOIN counts c USING(source_key) WHERE c.before_count>1 OR c.after_count>1
        ), ordered AS (
          SELECT row_number() OVER(ORDER BY source_key COLLATE "C",side,retrieval_digest COLLATE "C",entry_location COLLATE "C") AS position,
            changes.* FROM changes WHERE change<>'unchanged' OR $6
        ), page AS MATERIALIZED (
          SELECT * FROM ordered WHERE position>$7 ORDER BY position LIMIT $8
        ) SELECT p.position,p.source_key,p.change,c.before_count,c.after_count,
          CASE WHEN b.observation_id IS NOT NULL THEN jsonb_build_object(
            'observationId',b.observation_id,'identifier',b.identifier,'version',b.version,
            'versionPresent',b.version_present,'kind',b.kind,'identityDigest',b.identity_digest,
            'occurrence',jsonb_build_object('observationId',b.observation_id,'sourceId',b.source_id,
              'snapshotId',b.snapshot_id,'sourceRevision',b.source_revision,'retrievalDigest',b.retrieval_digest,
              'entryLocation',b.entry_location,'exactEntryDigest',b.exact_entry_digest,
              'normalizedEntryDigest',b.normalized_entry_digest,
              'normalizationInputDigest',b.normalization_input_digest,
              'evidenceComplete',b.evidence_complete,'observedAt',b.observed_at)) END,
          CASE WHEN a.observation_id IS NOT NULL THEN jsonb_build_object(
            'observationId',a.observation_id,'identifier',a.identifier,'version',a.version,
            'versionPresent',a.version_present,'kind',a.kind,'identityDigest',a.identity_digest,
            'occurrence',jsonb_build_object('observationId',a.observation_id,'sourceId',a.source_id,
              'snapshotId',a.snapshot_id,'sourceRevision',a.source_revision,'retrievalDigest',a.retrieval_digest,
              'entryLocation',a.entry_location,'exactEntryDigest',a.exact_entry_digest,
              'normalizedEntryDigest',a.normalized_entry_digest,
              'normalizationInputDigest',a.normalization_input_digest,
              'evidenceComplete',a.evidence_complete,'observedAt',a.observed_at)) END
        FROM page p JOIN counts c USING(source_key)
        LEFT JOIN members b ON b.side=0 AND b.source_key=p.source_key AND b.retrieval_digest=p.before_retrieval AND b.entry_location=p.before_location
        LEFT JOIN members a ON a.side=1 AND a.source_key=p.source_key AND a.retrieval_digest=p.after_retrieval AND a.entry_location=p.after_location
        ORDER BY p.position`, append(args, request.IncludeUnchanged, request.AfterPosition, request.Limit)...)
		if err != nil {
			return federation.SnapshotComparison{}, translateError("select comparison page", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item federation.SnapshotEntryChange
			var beforeRaw, afterRaw []byte
			if err := rows.Scan(&item.Position, &item.SourceKey, &item.Change, &item.BeforeCount, &item.AfterCount, &beforeRaw, &afterRaw); err != nil {
				return federation.SnapshotComparison{}, translateError("scan comparison page", err)
			}
			if beforeRaw != nil {
				if err := json.Unmarshal(beforeRaw, &item.Before); err != nil {
					return federation.SnapshotComparison{}, catalog.Wrap(catalog.CodeInternal, "decode comparison member", err)
				}
			}
			if afterRaw != nil {
				if err := json.Unmarshal(afterRaw, &item.After); err != nil {
					return federation.SnapshotComparison{}, catalog.Wrap(catalog.CodeInternal, "decode comparison member", err)
				}
			}
			comparison.Items = append(comparison.Items, item)
		}
		return comparison, translateError("iterate comparison page", rows.Err())
	})
}
