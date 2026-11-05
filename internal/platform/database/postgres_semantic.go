// Semantic persistence uses immutable vectors, explicit tenant keys and existing
// Job leases. Inference belongs outside these short database transactions.
package database

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// PostgresSemanticStore owns only optional derived tables, never source metadata.
type PostgresSemanticStore struct {
	repository   *PostgresRepository
	vectorSchema string
}

// NewPostgresSemanticStore verifies explicit schema provisioning without DDL or
// provider access. Core-only runtime composition must not call this constructor.
func NewPostgresSemanticStore(ctx context.Context, repository *PostgresRepository) (*PostgresSemanticStore, error) {
	if repository == nil {
		return nil, errors.New("semantic repository is required")
	}
	plan, err := PlanSemanticSchema(ctx, repository.pool)
	if err != nil {
		return nil, err
	}
	if !plan.Applied {
		return nil, errors.New("optional semantic schema has not been applied")
	}
	return &PostgresSemanticStore{repository: repository, vectorSchema: plan.ExtensionSchema}, nil
}

// Begin creates an idempotent generation only for a live, profile-bound Job.
// Retrying with different source metadata requires a new Job/generation identity.
func (s *PostgresSemanticStore) Begin(ctx context.Context, g catalog.SemanticGeneration, owner string) error {
	if err := validateSemanticGeneration(g, owner); err != nil {
		return err
	}
	return s.withLease(ctx, g, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO semantic_generations(tenant_id,id,profile,scope,source_generation,dimensions,state,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,'building',statement_timestamp(),statement_timestamp()) ON CONFLICT (tenant_id,id) DO NOTHING`, g.TenantID, g.ID, g.Profile, g.Scope, g.SourceGeneration, g.Dimensions)
		if err != nil {
			return translateError("create semantic generation", err)
		}
		return requireSemanticGeneration(ctx, tx, g, true)
	})
}

// SaveBatch commits a bounded batch atomically while the same Job owns its lease.
// Concurrent identical embeddings reuse the first immutable vector; members from
// other tenants, dimensions, profiles or disclosure scopes cannot be linked.
func (s *PostgresSemanticStore) SaveBatch(ctx context.Context, g catalog.SemanticGeneration, owner string, members []catalog.SemanticMember) error {
	if err := validateSemanticGeneration(g, owner); err != nil {
		return err
	}
	if err := validateSemanticMembers(members, g.Dimensions); err != nil {
		return err
	}
	disclosure := "control"
	if g.Scope != "control" {
		disclosure = "public"
	}
	return s.withLease(ctx, g, owner, func(tx pgx.Tx) error {
		if err := requireSemanticGeneration(ctx, tx, g, false); err != nil {
			return err
		}
		for _, member := range members {
			if member.Failure == "" && !member.Reuse {
				_, err := tx.Exec(ctx, `INSERT INTO semantic_vectors(tenant_id,profile,disclosure,input_digest,dimensions,embedding,created_at)
VALUES($1,$2,$3,$4,$5,$6::real[]::`+pgx.Identifier{s.vectorSchema, "vector"}.Sanitize()+`,clock_timestamp()) ON CONFLICT (tenant_id,profile,disclosure,input_digest) DO NOTHING`, g.TenantID, g.Profile, disclosure, member.InputDigest, g.Dimensions, member.Vector)
				if err != nil {
					return translateError("store semantic vector", err)
				}
			}
			_, err := tx.Exec(ctx, `INSERT INTO semantic_members(tenant_id,generation_id,item_key,profile,dimensions,disclosure,input_digest,failure)
VALUES($1,$2,$3,$4,$5,$6,nullif($7,''),$8) ON CONFLICT (tenant_id,generation_id,item_key) DO NOTHING`, g.TenantID, g.ID, member.Key, g.Profile, g.Dimensions, disclosure, member.InputDigest, member.Failure)
			if err != nil {
				return translateError("store semantic member", err)
			}
			var same bool
			if err := tx.QueryRow(ctx, `SELECT coalesce(input_digest,'')=$4 AND failure=$5 FROM semantic_members WHERE tenant_id=$1 AND generation_id=$2 AND item_key=$3`, g.TenantID, g.ID, member.Key, member.InputDigest, member.Failure).Scan(&same); err != nil {
				return err
			}
			if !same {
				return catalog.E(catalog.CodeConflict, "semantic member input changed")
			}
		}
		return nil
	})
}

// Complete publishes only a finished derived generation whose authoritative
// source still matches and whose membership count agrees with complete source
// enumeration. Search must also check that source generation in its read snapshot.
func (s *PostgresSemanticStore) Complete(ctx context.Context, g catalog.SemanticGeneration, owner string, expectedMembers int64) error {
	if err := validateSemanticGeneration(g, owner); err != nil {
		return err
	}
	if expectedMembers < 0 {
		return catalog.E(catalog.CodeInvalidArgument, "invalid semantic member count")
	}
	return s.withLease(ctx, g, owner, func(tx pgx.Tx) error {
		if err := requireSemanticGeneration(ctx, tx, g, true); err != nil {
			return err
		}
		var count int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM semantic_members WHERE tenant_id=$1 AND generation_id=$2`, g.TenantID, g.ID).Scan(&count); err != nil {
			return err
		}
		if count != expectedMembers {
			return catalog.E(catalog.CodeConflict, "semantic generation is incomplete")
		}
		current, err := semanticSourceGeneration(ctx, tx, g.TenantID, g.Scope)
		if err != nil {
			return err
		}
		if current != g.SourceGeneration {
			return catalog.Wrap(catalog.CodeConflict, "semantic source generation changed", catalog.ErrSearchGenerationChanged)
		}
		_, err = tx.Exec(ctx, `UPDATE semantic_generations SET state='ready',updated_at=greatest(updated_at,statement_timestamp()) WHERE tenant_id=$1 AND id=$2`, g.TenantID, g.ID)
		return translateError("complete semantic generation", err)
	})
}

// Similarities compares an explicit bounded set of already-eligible identities.
// A repeatable-read transaction binds vectors to the same authoritative source
// generation. Callers must perform eligibility selection in that snapshot for
// SQL-backed search; this method is for bounded immutable publication records.
func (s *PostgresSemanticStore) Similarities(ctx context.Context, g catalog.SemanticGeneration, vector []float32, keys []string) (map[string]float64, error) {
	if err := validateSemanticGeneration(g, "query"); err != nil {
		return nil, err
	}
	if len(keys) == 0 || len(keys) > catalog.MaxPublicationRecords {
		return nil, catalog.E(catalog.CodeInvalidArgument, "invalid semantic comparison bound")
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if len(key) == 0 || len(key) > 1024 || !utf8.ValidString(key) || strings.ContainsRune(key, 0) || seen[key] {
			return nil, catalog.E(catalog.CodeInvalidArgument, "invalid semantic comparison key")
		}
		seen[key] = true
	}
	if err := validateSemanticMembers([]catalog.SemanticMember{{Key: "query", InputDigest: g.Profile, Vector: vector}}, g.Dimensions); err != nil {
		return nil, err
	}
	return withTenantTx(ctx, s.repository.pool, g.TenantID, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (map[string]float64, error) {
		current, err := semanticSourceGeneration(ctx, tx, g.TenantID, g.Scope)
		if err != nil {
			return nil, err
		}
		if current != g.SourceGeneration {
			return nil, catalog.Wrap(catalog.CodeConflict, "semantic source generation changed", catalog.ErrSearchGenerationChanged)
		}
		var ready bool
		if err := tx.QueryRow(ctx, `SELECT state='ready' AND profile=$3 AND scope=$4 AND source_generation=$5 AND dimensions=$6 FROM semantic_generations WHERE tenant_id=$1 AND id=$2`, g.TenantID, g.ID, g.Profile, g.Scope, g.SourceGeneration, g.Dimensions).Scan(&ready); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, catalog.Wrap(catalog.CodeConflict, "semantic generation changed; repeat the query", catalog.ErrSearchGenerationChanged)
			}
			return nil, translateError("read semantic generation", err)
		}
		if !ready {
			return nil, catalog.Wrap(catalog.CodeConflict, "semantic generation is not ready", catalog.ErrSearchGenerationChanged)
		}
		rows, err := tx.Query(ctx, `SELECT m.item_key,1-`+pgx.Identifier{s.vectorSchema, "cosine_distance"}.Sanitize()+`(v.embedding,$4::real[]::`+pgx.Identifier{s.vectorSchema, "vector"}.Sanitize()+`) FROM semantic_members m JOIN semantic_vectors v ON v.tenant_id=m.tenant_id AND v.profile=m.profile AND v.disclosure=m.disclosure AND v.input_digest=m.input_digest AND v.dimensions=m.dimensions WHERE m.tenant_id=$1 AND m.generation_id=$2 AND m.item_key=ANY($3::text[]) AND m.failure=''`, g.TenantID, g.ID, keys, vector)
		if err != nil {
			return nil, translateError("compare semantic vectors", err)
		}
		defer rows.Close()
		result := make(map[string]float64, len(keys))
		for rows.Next() {
			var key string
			var similarity float64
			if err := rows.Scan(&key, &similarity); err != nil {
				return nil, err
			}
			if math.IsNaN(similarity) || math.IsInf(similarity, 0) {
				return nil, catalog.E(catalog.CodeInternal, "invalid stored semantic vector")
			}
			result[key] = max(-1, min(1, similarity))
		}
		return result, rows.Err()
	})
}

// semanticSourceGeneration reads the population fence, not mutable metadata.
// Public generations are usable only while their exact snapshot is active.
func semanticSourceGeneration(ctx context.Context, tx pgx.Tx, tenantID, scope string) (string, error) {
	var current string
	if scope == "control" {
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT generation::text FROM catalog_search_generations WHERE tenant_id=$1),'0')`, tenantID).Scan(&current); err != nil {
			return "", err
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT generation::text FROM publication_snapshots WHERE tenant_id=$1 AND id=$2 AND state='active'`, tenantID, strings.TrimPrefix(scope, "public:")).Scan(&current); err != nil {
			return "", translateError("check public semantic snapshot", err)
		}
	}
	return current, nil
}

// withLease holds the exact tenant/Job row and checks wall-clock expiry both
// before and after writes. Failed or expired attempts roll the entire batch back.
func (s *PostgresSemanticStore) withLease(ctx context.Context, g catalog.SemanticGeneration, owner string, work func(pgx.Tx) error) error {
	_, err := withTenantTx(ctx, s.repository.pool, g.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
		check := func() error {
			var live bool
			err := tx.QueryRow(ctx, `SELECT lease_expires_at>clock_timestamp() FROM jobs WHERE tenant_id=$1 AND id=$2 AND state='running' AND NOT cancellation_requested AND lease_owner=$3 AND payload_version=1 AND kind='semantic.reindex' AND payload->>'profile'=$4 AND payload->>'scope'=$5 FOR UPDATE`, g.TenantID, g.ID, owner, g.Profile, g.Scope).Scan(&live)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && !live {
				return catalog.E(catalog.CodeConflict, "semantic Job lease or profile is not current")
			}
			return translateError("check semantic Job lease", err)
		}
		if err := check(); err != nil {
			return struct{}{}, err
		}
		if err := work(tx); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, check()
	})
	return err
}

// requireSemanticGeneration locks the existing build and rejects profile drift,
// attempts to repurpose a Job for another source snapshot. Begin/Complete may
// replay a ready build; batch writes may not modify one.
func requireSemanticGeneration(ctx context.Context, tx pgx.Tx, g catalog.SemanticGeneration, allowReady bool) error {
	var same bool
	if err := tx.QueryRow(ctx, `SELECT profile=$3 AND scope=$4 AND source_generation=$5 AND dimensions=$6 AND (state='building' OR ($7 AND state='ready')) FROM semantic_generations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, g.TenantID, g.ID, g.Profile, g.Scope, g.SourceGeneration, g.Dimensions, allowReady).Scan(&same); err != nil {
		return translateError("read semantic generation", err)
	}
	if !same {
		return catalog.E(catalog.CodeConflict, "semantic generation is not the selected build")
	}
	return nil
}

// validateSemanticGeneration rejects malformed internal command inputs before SQL.
func validateSemanticGeneration(g catalog.SemanticGeneration, owner string) error {
	if !identity.ValidTenantID(g.TenantID) || !identity.ValidTenantID(g.ID) || !validSemanticDigest(g.Profile) || g.Dimensions < 1 || g.Dimensions > 4096 || len(owner) == 0 || len(owner) > 128 || strings.ContainsAny(owner, "\x00\r\n") || len(g.SourceGeneration) == 0 || len(g.SourceGeneration) > 256 || strings.ContainsRune(g.SourceGeneration, 0) || !utf8.ValidString(g.SourceGeneration) {
		return catalog.E(catalog.CodeInvalidArgument, "invalid semantic generation")
	}
	if g.Scope != "control" && (!strings.HasPrefix(g.Scope, "public:") || !identity.ValidTenantID(strings.TrimPrefix(g.Scope, "public:"))) {
		return catalog.E(catalog.CodeInvalidArgument, "invalid semantic scope")
	}
	return nil
}

// validSemanticDigest accepts the exact SHA-256 encoding used for profiles/inputs.
func validSemanticDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

// validateSemanticMembers prevents nonfinite, nonnormalized or duplicate inputs
// from causing a partial batch. Invalid provider data is never persisted.
func validateSemanticMembers(members []catalog.SemanticMember, dimensions int) error {
	if len(members) == 0 || len(members) > 32 {
		return catalog.E(catalog.CodeInvalidArgument, "semantic batch must contain 1 through 32 members")
	}
	seen := make(map[string]bool, len(members))
	for _, m := range members {
		if len(m.Key) == 0 || len(m.Key) > 1024 || !utf8.ValidString(m.Key) || strings.ContainsRune(m.Key, 0) || seen[m.Key] {
			return catalog.E(catalog.CodeInvalidArgument, "invalid semantic member key")
		}
		seen[m.Key] = true
		if m.Failure != "" {
			if (m.Failure != "input_too_large" && m.Failure != "input_rejected") || m.InputDigest != "" || len(m.Vector) != 0 || m.Reuse {
				return catalog.E(catalog.CodeInvalidArgument, "invalid semantic input failure")
			}
			continue
		}
		if m.Reuse {
			if !validSemanticDigest(m.InputDigest) || len(m.Vector) != 0 {
				return catalog.E(catalog.CodeInvalidArgument, "invalid reused vector identity")
			}
			continue
		}
		if !validSemanticDigest(m.InputDigest) || len(m.Vector) != dimensions {
			return catalog.E(catalog.CodeInvalidArgument, "invalid semantic vector identity")
		}
		var norm float64
		for _, v := range m.Vector {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return catalog.E(catalog.CodeInvalidArgument, "nonfinite semantic vector")
			}
			norm += float64(v) * float64(v)
		}
		if math.Abs(math.Sqrt(norm)-1) > 0.0001 {
			return catalog.E(catalog.CodeInvalidArgument, "semantic vector must have unit norm")
		}
	}
	return nil
}
