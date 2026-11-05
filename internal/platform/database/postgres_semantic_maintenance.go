// Semantic maintenance selects bounded derived work. Existing Jobs provide
// durable ownership; no inference or source mutation happens during admission.
package database

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const semanticAdmissionLock int64 = 880185172407

// semanticScopesSQL is the current authoritative scope inventory, not a list of
// retained generations. Retired public snapshots never schedule new inference.
const semanticScopesSQL = `
SELECT tenant_id,'control'::text AS scope,generation::text AS source_generation
  FROM catalog_search_generations
UNION ALL
SELECT tenant_id,'public:'||id::text,generation::text
  FROM publication_snapshots WHERE state='active' AND document_family='catalog_bundle'
`

// Due returns a bounded cross-tenant worker batch after exclusions, current
// generation checks and a five-minute terminal-failure cooldown. Replica races
// are rechecked by Admit before inserting any Job.
func (s *PostgresSemanticStore) Due(ctx context.Context, profile string, dimensions int, excluded []string, limit int) ([]catalog.SemanticGeneration, error) {
	if !validSemanticDigest(profile) || dimensions < 1 || dimensions > 4096 || limit < 1 || limit > 32 || len(excluded) > 256 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "invalid semantic maintenance bounds")
	}
	for _, tenant := range excluded {
		if !identity.ValidTenantID(tenant) {
			return nil, catalog.E(catalog.CodeInvalidArgument, "invalid semantic tenant exclusion")
		}
	}
	return listDueSemanticGenerations(ctx, s.repository.pool, profile, dimensions, excluded, limit)
}

// listDueSemanticGenerations reads validated candidates and closes their rows.
// An iteration error can accompany candidates; callers must check it before admission.
func listDueSemanticGenerations(ctx context.Context, query interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, profile string, dimensions int, excluded []string, limit int) ([]catalog.SemanticGeneration, error) {
	if excluded == nil {
		excluded = []string{}
	}
	rows, err := query.Query(ctx, `WITH scopes AS (`+semanticScopesSQL+`)
SELECT tenant_id::text,scope,source_generation FROM scopes candidate
 WHERE NOT (tenant_id=ANY($3::uuid[]))
   AND NOT EXISTS (SELECT 1 FROM semantic_generations generation
     WHERE generation.tenant_id=candidate.tenant_id AND generation.profile=$1
       AND generation.scope=candidate.scope AND generation.source_generation=candidate.source_generation
       AND generation.dimensions=$2 AND generation.state='ready')
   AND NOT EXISTS (SELECT 1 FROM jobs job
     WHERE job.tenant_id=candidate.tenant_id AND job.kind='semantic.reindex'
       AND job.payload_version=1 AND job.payload->>'profile'=$1 AND job.payload->>'scope'=candidate.scope
       AND (job.state IN ('queued','running','waiting') OR
         (job.state IN ('failed','dead_letter','cancelled') AND job.payload->>'sourceGeneration'=candidate.source_generation AND job.updated_at>clock_timestamp()-interval '5 minutes')))
 ORDER BY tenant_id,scope COLLATE "C" LIMIT $4`, profile, dimensions, excluded, limit)
	if err != nil {
		return nil, translateError("select semantic maintenance", err)
	}
	defer rows.Close()
	result := make([]catalog.SemanticGeneration, 0, limit)
	for rows.Next() {
		g := catalog.SemanticGeneration{Profile: profile, Dimensions: dimensions}
		if err := rows.Scan(&g.TenantID, &g.Scope, &g.SourceGeneration); err != nil {
			return nil, translateError("read semantic maintenance", err)
		}
		result = append(result, g)
	}
	return result, translateError("iterate semantic maintenance", rows.Err())
}

// Admit serializes a short admission check across worker replicas. It refuses
// stale scopes, ready generations, duplicate work and a full queue. IDs come
// from the process's injected generator, never from source metadata.
// All inserted lifecycle timestamps use one statement instant: not_before
// cannot precede created_at by taking separate volatile clock readings.
func (s *PostgresSemanticStore) Admit(ctx context.Context, g catalog.SemanticGeneration, excluded []string, maxAttempts int) (bool, error) {
	if maxAttempts < 1 || maxAttempts > 20 {
		return false, catalog.E(catalog.CodeInvalidArgument, "semantic Job attempt limit must be between one and twenty")
	}
	if err := validateSemanticGeneration(g, "semantic-maintenance"); err != nil {
		return false, err
	}
	if len(excluded) > 256 {
		return false, catalog.E(catalog.CodeInvalidArgument, "too many semantic tenant exclusions")
	}
	for _, tenant := range excluded {
		if !identity.ValidTenantID(tenant) {
			return false, catalog.E(catalog.CodeInvalidArgument, "invalid semantic tenant exclusion")
		}
		if strings.EqualFold(tenant, g.TenantID) {
			return false, nil
		}
	}
	if excluded == nil {
		excluded = []string{}
	}
	return withTenantTx(ctx, s.repository.pool, g.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (bool, error) {
		if err := lockTenantSettings(ctx, tx, g.TenantID, true); err != nil {
			return false, err
		}
		attempts, err := resolveNewJobAttempts(ctx, tx, g.TenantID, maxAttempts)
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, semanticAdmissionLock); err != nil {
			return false, translateError("lock semantic admission", err)
		}
		current, err := semanticSourceGeneration(ctx, tx, g.TenantID, g.Scope)
		if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if current != g.SourceGeneration {
			return false, nil
		}
		payload, err := json.Marshal(struct {
			Profile          string `json:"profile"`
			Scope            string `json:"scope"`
			SourceGeneration string `json:"sourceGeneration"`
		}{g.Profile, g.Scope, g.SourceGeneration})
		if err != nil {
			return false, err
		}
		result, err := tx.Exec(ctx, `INSERT INTO jobs
(id,tenant_id,kind,actor,payload_version,payload,state,priority,progress,not_before,attempt,attempt_limit,deduplication_key,created_at,updated_at)
SELECT $1,$2,'semantic.reindex','semantic-maintenance',1,$3,'queued',0,0,statement_timestamp(),0,$10,$4,statement_timestamp(),statement_timestamp()
 WHERE NOT EXISTS (SELECT 1 FROM semantic_generations WHERE tenant_id=$2 AND profile=$5 AND scope=$6 AND source_generation=$7 AND dimensions=$8 AND state='ready')
 AND NOT EXISTS (SELECT 1 FROM jobs WHERE tenant_id=$2 AND kind='semantic.reindex' AND payload_version=1
   AND payload->>'profile'=$5 AND payload->>'scope'=$6
   AND (state IN ('queued','running','waiting') OR
     (state IN ('failed','dead_letter','cancelled') AND payload->>'sourceGeneration'=$7 AND updated_at>clock_timestamp()-interval '5 minutes')))
 AND (SELECT count(*) FROM (SELECT 1 FROM jobs WHERE kind='semantic.reindex' AND payload->>'profile'=$5 AND NOT (tenant_id=ANY($9::uuid[])) AND state IN ('queued','running','waiting') LIMIT 64) live)<64
 ON CONFLICT DO NOTHING`, g.ID, g.TenantID, payload, g.Profile+":"+g.Scope, g.Profile, g.Scope, g.SourceGeneration, g.Dimensions, excluded, attempts)
		return result.RowsAffected() == 1, translateError("admit semantic indexing", err)
	})
}
