package database

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// The restore projection checks binding and temporal consistency without
// returning challenge values, domain names or DNS response data.
func scanRestorePublisherVerification(ctx context.Context, tx pgx.Tx, tenant, after string, limit int) ([]restoreRecord, bool, error) {
	rows, err := tx.Query(ctx, `SELECT v.id::text,
 coalesce(v.revision<=0 OR p.id IS NULL OR NOT(v.domain=ANY(p.domains)) OR v.updated_at<v.created_at
 OR NOT((v.challenge_id IS NULL AND v.nonce='' AND v.challenge_issued_at IS NULL AND v.challenge_expires_at IS NULL AND NOT v.challenge_consumed)
 OR (v.challenge_id IS NOT NULL AND v.nonce ~ '^[A-Za-z0-9_-]{43}$' AND v.challenge_issued_at IS NOT NULL AND v.challenge_expires_at IS NOT NULL AND v.challenge_expires_at>v.challenge_issued_at AND v.challenge_issued_at>=v.created_at AND v.challenge_issued_at<=v.updated_at))
 OR NOT((v.verified_at IS NULL AND v.verified_challenge_id IS NULL AND v.proof_expires_at IS NULL)
 OR (v.verified_at IS NOT NULL AND v.verified_challenge_id IS NOT NULL AND v.proof_expires_at IS NOT NULL AND v.proof_expires_at>v.verified_at AND v.verified_at>=v.created_at AND v.verified_at<=v.updated_at))
 OR NOT((v.last_check_at IS NULL AND v.last_check_challenge_id IS NULL AND v.last_check_outcome='')
 OR (v.last_check_at IS NOT NULL AND v.last_check_challenge_id IS NOT NULL AND v.last_check_outcome IN ('matched','mismatch','not_found','unavailable','response_limit') AND v.last_check_at>=v.created_at AND v.last_check_at<=v.updated_at))
 OR (v.challenge_consumed AND (v.verified_challenge_id IS NULL OR v.challenge_id IS DISTINCT FROM v.verified_challenge_id)),true)
 FROM publisher_domain_verifications v LEFT JOIN publishers p ON p.tenant_id=v.tenant_id AND p.id=v.publisher_id
 WHERE v.tenant_id=$1 AND ($2='' OR v.id>nullif($2,'')::uuid) ORDER BY v.id LIMIT $3`, tenant, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := make([]restoreRecord, 0, limit)
	for rows.Next() {
		var id string
		var invalid bool
		if err := rows.Scan(&id, &invalid); err != nil {
			return nil, false, err
		}
		if len(records) == limit {
			return records, true, nil
		}
		record := restoreRecord{key: id}
		if invalid {
			record.finding = integrityFinding("RESTORE_PUBLISHER_VERIFICATION_INVALID", "publisher_domain_verification", id, "keep traffic disabled and investigate retained domain verification evidence")
		}
		records = append(records, record)
	}
	return records, false, rows.Err()
}
