package database

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

// Restore checks opinions, their minimal history and trigger-owned rollups.
// No score or principal is included in a finding.
func scanRestoreRatings(ctx context.Context, tx pgx.Tx, tenant, after string, limit int) ([]restoreRecord, bool, error) {
	rows, err := tx.Query(ctx, `SELECT key,document,invalid FROM (
 SELECT 'target:'||target.target_key AS key,target.target AS document,
 (target.vote_count<>coalesce(actual.count,0) OR target.score_sum<>coalesce(actual.sum,0) OR target.shared_count<>coalesce(actual.shared_count,0) OR target.shared_sum<>coalesce(actual.shared_sum,0)) AS invalid
 FROM rating_targets target LEFT JOIN LATERAL(SELECT count(*) FILTER(WHERE state='active') AS count,sum(score) FILTER(WHERE state='active') AS sum,count(*) FILTER(WHERE state='active' AND consent) AS shared_count,sum(score) FILTER(WHERE state='active' AND consent) AS shared_sum FROM item_ratings vote WHERE vote.tenant_id=target.tenant_id AND vote.target_key=target.target_key)actual ON true WHERE target.tenant_id=$1
 UNION ALL
 SELECT 'vote:'||vote.id::text,NULL::jsonb,NOT EXISTS(SELECT 1 FROM item_rating_revisions revision WHERE revision.tenant_id=vote.tenant_id AND revision.rating_id=vote.id AND revision.revision=vote.revision AND revision.score=vote.score AND revision.consent=vote.consent AND revision.state=vote.state AND revision.entry_digest=vote.entry_digest AND revision.recorded_at=vote.updated_at) OR
 (SELECT count(*) FROM item_rating_revisions revision WHERE revision.tenant_id=vote.tenant_id AND revision.rating_id=vote.id)<>vote.revision
 FROM item_ratings vote WHERE vote.tenant_id=$1
 UNION ALL
 SELECT 'sharing:'||id::text,jsonb_build_object('host',host,'audience',audience),false FROM rating_sharing WHERE tenant_id=$1
 ) retained WHERE ($2='' OR key>$2) ORDER BY key LIMIT $3`, tenant, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var key string
		var document []byte
		var invalid bool
		if err = rows.Scan(&key, &document, &invalid); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		if strings.HasPrefix(key, "target:") {
			var target ratings.Target
			invalid = invalid || json.Unmarshal(document, &target) != nil || ratings.ValidateTarget(target) != nil || "target:"+ratings.Key(target) != key
		}
		if strings.HasPrefix(key, "sharing:") {
			var scope struct {
				Host     string `json:"host"`
				Audience string `json:"audience"`
			}
			if json.Unmarshal(document, &scope) != nil {
				invalid = true
			} else {
				u, parseErr := url.Parse("https://" + scope.Host)
				invalid = invalid || parseErr != nil || u.Host != scope.Host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || len(scope.Host) == 0 || len(scope.Host) > 253 || scope.Audience != "anonymous" && scope.Audience != "authenticated"
			}
		}
		record := restoreRecord{key: key, decodedBytes: int64(len(document))}
		if invalid {
			record.finding = integrityFinding("RESTORE_RATING_INVALID", "rating", key, "keep traffic disabled and investigate rating identity, history or totals; do not expose individual opinions")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}
