package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const publisherVerificationColumns = `id::text,tenant_id::text,publisher_id::text,domain,revision,coalesce(challenge_id::text,''),nonce,challenge_issued_at,challenge_expires_at,challenge_consumed,coalesce(verified_challenge_id::text,''),verified_at,proof_expires_at,last_check_at,coalesce(last_check_challenge_id::text,''),last_check_outcome,created_at,updated_at`

func readPublisherVerification(ctx context.Context, tx pgx.Tx, tenant, publisher, domain string, lock bool) (catalog.PublisherDomainVerification, error) {
	v := catalog.PublisherDomainVerification{TenantID: tenant, PublisherID: publisher, Domain: domain}
	query := `SELECT ` + publisherVerificationColumns + ` FROM publisher_domain_verifications WHERE tenant_id=$1 AND publisher_id=$2 AND domain=$3`
	if lock {
		query += ` FOR UPDATE`
	}
	var issued, expires, verified, proof, last *time.Time
	err := tx.QueryRow(ctx, query, tenant, publisher, domain).Scan(&v.ID, &v.TenantID, &v.PublisherID, &v.Domain, &v.Revision, &v.ChallengeID, &v.Nonce, &issued, &expires, &v.ChallengeConsumed, &v.VerifiedChallengeID, &verified, &proof, &last, &v.LastCheckChallengeID, &v.LastCheckOutcome, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return catalog.PublisherDomainVerification{}, translateError("read publisher domain verification", err)
	}
	if issued != nil {
		v.ChallengeIssuedAt = *issued
	}
	if expires != nil {
		v.ChallengeExpiresAt = *expires
	}
	if verified != nil {
		v.VerifiedAt = *verified
	}
	if proof != nil {
		v.ProofExpiresAt = *proof
	}
	if last != nil {
		v.LastCheckAt = *last
	}
	return v, nil
}

func readVerificationPublisher(ctx context.Context, tx pgx.Tx, tenant, publisher, domain string, lock bool) (catalog.Publisher, error) {
	var p catalog.Publisher
	query := `SELECT id::text,tenant_id::text,display_name,domains,approved_publication_domains,revision,created_at,updated_at FROM publishers WHERE tenant_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	if err := tx.QueryRow(ctx, query, tenant, publisher).Scan(&p.ID, &p.TenantID, &p.DisplayName, &p.Domains, &p.ApprovedPublicationDomains, &p.Revision, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, translateError("read publisher", err)
	}
	if !stringSliceContains(p.Domains, domain) {
		return catalog.Publisher{}, catalog.E(catalog.CodeNotFound, "publisher domain was not found")
	}
	return p, nil
}

func (p *PostgresRepository) ReadPublisherDomainVerification(ctx context.Context, tenant, publisher, domain string) (catalog.Publisher, catalog.PublisherDomainVerification, error) {
	type result struct {
		p catalog.Publisher
		v catalog.PublisherDomainVerification
	}
	r, err := withTenantCollectionRead(ctx, p.pool, tenant, func(tx pgx.Tx) (result, error) {
		owner, err := readVerificationPublisher(ctx, tx, tenant, publisher, domain, false)
		if err != nil {
			return result{}, err
		}
		v, err := readPublisherVerification(ctx, tx, tenant, publisher, domain, false)
		return result{owner, v}, err
	})
	return r.p, r.v, err
}

func verificationTimestamp(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func (p *PostgresRepository) SavePublisherDomainVerification(ctx context.Context, params catalog.PublisherDomainMutation) (catalog.PublisherDomainVerification, error) {
	v := params.State
	return withTenantTx(ctx, p.pool, v.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.PublisherDomainVerification, error) {
		owner, err := readVerificationPublisher(ctx, tx, v.TenantID, v.PublisherID, v.Domain, true)
		if err != nil {
			return catalog.PublisherDomainVerification{}, err
		}
		current, err := readPublisherVerification(ctx, tx, v.TenantID, v.PublisherID, v.Domain, true)
		if err != nil {
			return catalog.PublisherDomainVerification{}, err
		}
		if err = catalog.CheckPublisherDomainVersion(owner, current, params.Expected); err != nil {
			return catalog.PublisherDomainVerification{}, err
		}
		if err = catalog.CheckPublisherDomainCompletion(params, current); err != nil {
			return catalog.PublisherDomainVerification{}, err
		}
		if err := catalog.CheckPublisherDomainTransition(current, v); err != nil {
			return catalog.PublisherDomainVerification{}, err
		}
		args := []any{v.ID, v.TenantID, v.PublisherID, v.Domain, v.Revision, nullableUUID(v.ChallengeID), v.Nonce, verificationTimestamp(v.ChallengeIssuedAt), verificationTimestamp(v.ChallengeExpiresAt), v.ChallengeConsumed, nullableUUID(v.VerifiedChallengeID), verificationTimestamp(v.VerifiedAt), verificationTimestamp(v.ProofExpiresAt), verificationTimestamp(v.LastCheckAt), nullableUUID(v.LastCheckChallengeID), v.LastCheckOutcome, v.CreatedAt, v.UpdatedAt}
		var result pgconn.CommandTag
		if current.ID == "" {
			result, err = tx.Exec(ctx, `INSERT INTO publisher_domain_verifications(id,tenant_id,publisher_id,domain,revision,challenge_id,nonce,challenge_issued_at,challenge_expires_at,challenge_consumed,verified_challenge_id,verified_at,proof_expires_at,last_check_at,last_check_challenge_id,last_check_outcome,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, args...)
		} else {
			result, err = tx.Exec(ctx, `UPDATE publisher_domain_verifications SET revision=$5,challenge_id=$6,nonce=$7,challenge_issued_at=$8,challenge_expires_at=$9,challenge_consumed=$10,verified_challenge_id=$11,verified_at=$12,proof_expires_at=$13,last_check_at=$14,last_check_challenge_id=$15,last_check_outcome=$16,updated_at=$18 WHERE id=$1 AND tenant_id=$2 AND publisher_id=$3 AND domain=$4 AND created_at=$17`, args...)
		}
		if err != nil {
			return catalog.PublisherDomainVerification{}, translateError("save publisher domain verification", err)
		}
		if result.RowsAffected() != 1 {
			return catalog.PublisherDomainVerification{}, catalog.E(catalog.CodePreconditionFailed, "verification state changed before completion")
		}
		if params.Audit.TenantID != v.TenantID || params.Audit.TargetType != "publisher" || params.Audit.TargetID != v.PublisherID || !catalog.PublisherVerificationAction(params.Audit.Action) {
			return catalog.PublisherDomainVerification{}, catalog.E(catalog.CodeInternal, "publisher verification audit binding is invalid")
		}
		if err = insertAuditEvent(ctx, tx, params.Audit); err != nil {
			return catalog.PublisherDomainVerification{}, err
		}
		return v, nil
	})
}
