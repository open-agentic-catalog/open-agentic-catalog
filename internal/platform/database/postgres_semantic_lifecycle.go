// Generation reads fence derived state against current metadata. Cleanup is
// incremental so a large retired index cannot monopolize a worker transaction.
package database

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// Status reads one current control generation and bounded member accounting.
// It never queries inference and keeps counts within the explicit tenant fence.
func (s *PostgresSemanticStore) Status(ctx context.Context, tenant, profile string, dimensions int) (catalog.SemanticIndexStatus, error) {
	if !identity.ValidTenantID(tenant) || !validSemanticDigest(profile) || dimensions < 1 || dimensions > 4096 {
		return catalog.SemanticIndexStatus{}, catalog.E(catalog.CodeInvalidArgument, "invalid semantic status identity")
	}
	return withTenantTx(ctx, s.repository.pool, tenant, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (catalog.SemanticIndexStatus, error) {
		status := catalog.SemanticIndexStatus{State: "pending"}
		current, err := semanticSourceGeneration(ctx, tx, tenant, "control")
		if err != nil {
			return status, err
		}
		err = tx.QueryRow(ctx, `SELECT generation.id::text,CASE WHEN generation.state='ready' THEN 'ready' WHEN job.state IN ('failed','dead_letter','cancelled') THEN 'failed' ELSE 'building' END
 FROM semantic_generations generation JOIN jobs job ON job.tenant_id=generation.tenant_id AND job.id=generation.id
 WHERE generation.tenant_id=$1 AND generation.profile=$2 AND generation.dimensions=$3 AND generation.scope='control' AND generation.source_generation=$4
 ORDER BY generation.created_at DESC,generation.id DESC LIMIT 1`, tenant, profile, dimensions, current).Scan(&status.JobID, &status.State)
		if errors.Is(err, pgx.ErrNoRows) {
			return status, nil
		}
		if err != nil {
			return status, translateError("read semantic status", err)
		}
		var total int64
		err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE failure=''),count(*) FILTER (WHERE failure<>'') FROM (SELECT failure FROM semantic_members WHERE tenant_id=$1 AND generation_id=$2 LIMIT 100001) members`, tenant, status.JobID).Scan(&total, &status.Indexed, &status.Failed)
		if err != nil {
			return status, translateError("read semantic counts", err)
		}
		if total > 100000 {
			return status, catalog.E(catalog.CodeWorkLimitExceeded, "semantic status work limit exceeded")
		}
		return status, nil
	})
}

// Current returns only the authoritative source fence, without vector access.
func (s *PostgresSemanticStore) Current(ctx context.Context, tenant, scope string) (string, error) {
	if !identity.ValidTenantID(tenant) || scope != "control" && (!strings.HasPrefix(scope, "public:") || !identity.ValidTenantID(strings.TrimPrefix(scope, "public:"))) {
		return "", catalog.E(catalog.CodeInvalidArgument, "invalid semantic scope")
	}
	return withTenantTx(ctx, s.repository.pool, tenant, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (string, error) { return semanticSourceGeneration(ctx, tx, tenant, scope) })
}

// Active returns the newest completely accounted generation for the current
// scope. No ready generation is ordinary indexing unavailability, not corruption.
func (s *PostgresSemanticStore) Active(ctx context.Context, tenant, profile, scope string, dimensions int) (catalog.SemanticGeneration, bool, error) {
	g := catalog.SemanticGeneration{ID: tenant, TenantID: tenant, Profile: profile, Scope: scope, SourceGeneration: "0", Dimensions: dimensions}
	if err := validateSemanticGeneration(g, "reader"); err != nil {
		return catalog.SemanticGeneration{}, false, err
	}
	type result struct {
		generation catalog.SemanticGeneration
		found      bool
	}
	r, err := withTenantTx(ctx, s.repository.pool, tenant, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (result, error) {
		current, err := semanticSourceGeneration(ctx, tx, tenant, scope)
		if err != nil {
			return result{}, err
		}
		g.SourceGeneration = current
		err = tx.QueryRow(ctx, `SELECT id::text,state,created_at,updated_at FROM semantic_generations
 WHERE tenant_id=$1 AND profile=$2 AND scope=$3 AND source_generation=$4 AND dimensions=$5 AND state='ready'
 ORDER BY created_at DESC,id DESC LIMIT 1`, tenant, profile, scope, current, dimensions).Scan(&g.ID, &g.State, &g.CreatedAt, &g.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, nil
		}
		return result{g, err == nil}, translateError("read active semantic generation", err)
	})
	return r.generation, r.found, err
}

// Cleanup releases one obsolete generation's members and then unused vectors,
// at most limit rows per class. Live Jobs retain their generation even after a
// profile change. Current ready generations remain until replaced or excluded.
func (s *PostgresSemanticStore) Cleanup(ctx context.Context, profile string, excluded []string, limit int) (int64, error) {
	if !validSemanticDigest(profile) || limit < 1 || limit > 1000 || len(excluded) > 256 {
		return 0, catalog.E(catalog.CodeInvalidArgument, "invalid semantic cleanup bounds")
	}
	for _, tenant := range excluded {
		if !identity.ValidTenantID(tenant) {
			return 0, catalog.E(catalog.CodeInvalidArgument, "invalid semantic tenant exclusion")
		}
	}
	if excluded == nil {
		excluded = []string{}
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, translateError("begin semantic cleanup", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, semanticAdmissionLock+1).Scan(&locked); err != nil {
		return 0, translateError("lock semantic cleanup", err)
	}
	if !locked {
		return 0, nil
	}
	removed, err := cleanupSemanticTx(ctx, tx, profile, excluded, limit)
	if err != nil {
		return 0, err
	}
	return removed, translateError("commit semantic cleanup", tx.Commit(ctx))
}

// cleanupSemanticTx removes bounded derived rows in the caller's locked
// transaction. The caller validates inputs and owns commit/rollback. Read/write
// failures return zero; successful counts remain provisional until commit.
func cleanupSemanticTx(ctx context.Context, tx pgx.Tx, profile string, excluded []string, limit int) (int64, error) {
	var tenant, generation string
	err := tx.QueryRow(ctx, `WITH scopes AS (`+semanticScopesSQL+`)
SELECT generation.tenant_id::text,generation.id::text FROM semantic_generations generation
 JOIN jobs job ON job.tenant_id=generation.tenant_id AND job.id=generation.id
 WHERE job.state NOT IN ('queued','running','waiting')
 AND (generation.tenant_id=ANY($2::uuid[]) OR generation.state<>'ready'
   OR (generation.profile<>$1 AND EXISTS (SELECT 1 FROM semantic_generations replacement
     WHERE replacement.tenant_id=generation.tenant_id AND replacement.scope=generation.scope
       AND replacement.profile=$1 AND replacement.state='ready'
       AND (replacement.created_at,replacement.id)>(generation.created_at,generation.id)))
   OR NOT EXISTS (SELECT 1 FROM scopes WHERE scopes.tenant_id=generation.tenant_id AND scopes.scope=generation.scope AND scopes.source_generation=generation.source_generation)
   OR EXISTS (SELECT 1 FROM semantic_generations newer WHERE newer.tenant_id=generation.tenant_id
     AND newer.profile=generation.profile AND newer.scope=generation.scope AND newer.source_generation=generation.source_generation
     AND newer.state='ready' AND (newer.created_at,newer.id)>(generation.created_at,generation.id)))
 ORDER BY generation.created_at,generation.tenant_id,generation.id LIMIT 1 FOR UPDATE OF generation SKIP LOCKED`, profile, excluded).Scan(&tenant, &generation)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, translateError("select obsolete semantic generation", err)
	}
	var removed int64
	if err == nil {
		result, err := tx.Exec(ctx, `WITH selected AS (SELECT item_key FROM semantic_members WHERE tenant_id=$1 AND generation_id=$2 ORDER BY item_key LIMIT $3)
DELETE FROM semantic_members member USING selected WHERE member.tenant_id=$1 AND member.generation_id=$2 AND member.item_key=selected.item_key`, tenant, generation, limit)
		if err != nil {
			return 0, translateError("remove obsolete semantic members", err)
		}
		removed += result.RowsAffected()
		if _, err := tx.Exec(ctx, `DELETE FROM semantic_generations WHERE tenant_id=$1 AND id=$2 AND NOT EXISTS (SELECT 1 FROM semantic_members WHERE tenant_id=$1 AND generation_id=$2)`, tenant, generation); err != nil {
			return 0, translateError("release obsolete semantic generation", err)
		}
	}
	// A grace period retains reusable vectors during short indexing interruptions.
	// FK checks serialize deletion with concurrent membership insertion.
	result, err := tx.Exec(ctx, `WITH selected AS (
 SELECT vector.tenant_id,vector.profile,vector.disclosure,vector.input_digest FROM semantic_vectors vector
 WHERE vector.created_at<clock_timestamp()-interval '1 hour'
 AND NOT EXISTS (SELECT 1 FROM semantic_members member WHERE member.tenant_id=vector.tenant_id AND member.profile=vector.profile AND member.disclosure=vector.disclosure AND member.input_digest=vector.input_digest)
 ORDER BY vector.created_at,vector.tenant_id,vector.profile,vector.disclosure,vector.input_digest LIMIT $1)
DELETE FROM semantic_vectors vector USING selected WHERE vector.tenant_id=selected.tenant_id AND vector.profile=selected.profile AND vector.disclosure=selected.disclosure AND vector.input_digest=selected.input_digest`, limit)
	if err != nil {
		return 0, translateError("remove unused semantic vectors", err)
	}
	removed += result.RowsAffected()
	return removed, nil
}
